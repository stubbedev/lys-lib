package opus

import "github.com/stubbedev/lys-lib/intconv"

// silkFIROrder is the interpolation FIR's length.
const silkFIROrder = 8

// silkResampler is silk_resampler_state_struct for the decoder's only
// conversion, 8, 12 or 16 kHz up to 48 kHz: silk_resampler_private_IIR_FIR,
// a 2x all-pass upsampler followed by a 12-phase FIR.
type silkResampler struct {
	sIIR       [6]int32
	sFIR       [silkFIROrder]int16
	delayBuf   [silkMaxFsKHz]int16
	batchSize  int
	invRatio   int32
	fsInKHz    int
	inputDelay int
	buf        [2*10*silkMaxFsKHz + silkFIROrder]int16
}

// init is silk_resampler_init(fsKHz*1000, 48000, decoder).
func (r *silkResampler) init(fsKHz int32) {
	*r = silkResampler{fsInKHz: int(fsKHz), batchSize: int(fsKHz) * 10}
	switch fsKHz {
	case 12:
		r.inputDelay = 4
	case 16:
		r.inputDelay = 7
	}
	in := fsKHz * 1000
	r.invRatio = ((in << 15) / 48000) << 2
	for smulww(r.invRatio, 48000) < in<<1 {
		r.invRatio++
	}
}

// resample is silk_resampler: in at the internal rate to out at
// 48 kHz, 48/fsInKHz times as long.
func (r *silkResampler) resample(out, in []int16) {
	n := r.fsInKHz - r.inputDelay
	copy(r.delayBuf[r.inputDelay:r.fsInKHz], in[:n])
	r.iirFIR(out[:48], r.delayBuf[:r.fsInKHz])
	r.iirFIR(out[48:], in[n:len(in)-r.inputDelay])
	copy(r.delayBuf[:r.inputDelay], in[len(in)-r.inputDelay:])
}

// iirFIR is silk_resampler_private_IIR_FIR.
func (r *silkResampler) iirFIR(out, in []int16) {
	buf := r.buf[:]
	copy(buf, r.sFIR[:])
	inc := r.invRatio
	var nIn int
	for {
		nIn = min(len(in), r.batchSize)
		up2HQ(&r.sIIR, buf[silkFIROrder:silkFIROrder+2*nIn], in[:nIn])
		maxIndex := intconv.Must[int32](nIn) << 17
		for index := int32(0); index < maxIndex; index += inc {
			phase := smulwb(index&0xffff, 12)
			b := (*[silkFIROrder]int16)(buf[index>>16:])
			lo, hi := &silkResamplerFracFIR12[phase], &silkResamplerFracFIR12[11-phase]
			acc := int32(b[0]) * int32(lo[0])
			acc += int32(b[1]) * int32(lo[1])
			acc += int32(b[2]) * int32(lo[2])
			acc += int32(b[3]) * int32(lo[3])
			acc += int32(b[4]) * int32(hi[3])
			acc += int32(b[5]) * int32(hi[2])
			acc += int32(b[6]) * int32(hi[1])
			acc += int32(b[7]) * int32(hi[0])
			out[0] = sat16(rshiftRound(acc, 15))
			out = out[1:]
		}
		in = in[nIn:]
		if len(in) == 0 {
			break
		}
		copy(buf, buf[nIn<<1:nIn<<1+silkFIROrder])
	}
	copy(r.sFIR[:], buf[nIn<<1:nIn<<1+silkFIROrder])
}

var (
	silkUp2HQ0 = [3]int32{1746, 14986, 39083 - 65536}
	silkUp2HQ1 = [3]int32{6854, 25769, 55542 - 65536}
)

// up2HQ is silk_resampler_private_up2_HQ: in upsampled 2x into out by
// two three-stage all-pass chains.
func up2HQ(s *[6]int32, out, in []int16) {
	for k, v := range in {
		in32 := int32(v) << 10
		y := in32 - s[0]
		x := smulwb(y, silkUp2HQ0[0])
		o1 := s[0] + x
		s[0] = in32 + x
		y = o1 - s[1]
		x = smulwb(y, silkUp2HQ0[1])
		o2 := s[1] + x
		s[1] = o1 + x
		y = o2 - s[2]
		x = smlawb(y, y, silkUp2HQ0[2])
		o1 = s[2] + x
		s[2] = o2 + x
		out[2*k] = sat16(rshiftRound(o1, 10))
		y = in32 - s[3]
		x = smulwb(y, silkUp2HQ1[0])
		o1 = s[3] + x
		s[3] = in32 + x
		y = o1 - s[4]
		x = smulwb(y, silkUp2HQ1[1])
		o2 = s[4] + x
		s[4] = o1 + x
		y = o2 - s[5]
		x = smlawb(y, y, silkUp2HQ1[2])
		o1 = s[5] + x
		s[5] = o2 + x
		out[2*k+1] = sat16(rshiftRound(o1, 10))
	}
}

// silkFrame describes the SILK layer of an Opus frame: its coded
// channels, internal rate and duration.
type silkFrame struct {
	channels int
	fsKHz    int32
	ms       int
}

// silkDecoder is silk_decoder, silk_Decode's state: the coded
// channels, the mid/side predictor history and the channel counts of
// the last frame.
type silkDecoder struct {
	ch                   [2]silkChannel
	predPrev             [2]int16
	sMid, sSide          [2]int16
	apiChannels          int
	internalChannels     int
	prevDecodeOnlyMiddle bool
	tmp                  [2][silkMaxFrameLength + 2]int16
	resampled            [48 * 20]int16
	pulses               [silkMaxFrameLength]int32
}

// reset is silk_InitDecoder and silk_ResetDecoder: no history.
func (d *silkDecoder) reset() {
	for i := range d.ch {
		d.ch[i].reset()
	}
	d.predPrev, d.sMid, d.sSide = [2]int16{}, [2]int16{}, [2]int16{}
	d.prevDecodeOnlyMiddle = false
}

// decode is silk_Decode for 48 kHz output in apiChannels channels: it
// decodes (or, lost, conceals) the next SILK frame of the packet into
// out, interleaved and in int16 scale, and returns the samples per
// channel written. first marks a packet's first call.
func (d *silkDecoder) decode(rc *rangeDecoder, f silkFrame, apiChannels int, lost, first bool, out []float32) int {
	chs := &d.ch
	if first {
		for n := range f.channels {
			chs[n].nFramesDecoded = 0
		}
	}
	if f.channels > d.internalChannels {
		chs[1].reset()
	}
	stereoToMono := f.channels == 1 && d.internalChannels == 2 && f.fsKHz == chs[0].fsKHz
	if chs[0].nFramesDecoded == 0 {
		for n := range f.channels {
			ch := &chs[n]
			switch f.ms {
			case 10:
				ch.nFramesPerPacket, ch.nbSubfr = 1, 2
			case 20:
				ch.nFramesPerPacket, ch.nbSubfr = 1, 4
			case 40:
				ch.nFramesPerPacket, ch.nbSubfr = 2, 4
			case 60:
				ch.nFramesPerPacket, ch.nbSubfr = 3, 4
			}
			ch.setFs(f.fsKHz)
		}
	}
	if apiChannels == 2 && f.channels == 2 && (d.apiChannels == 1 || d.internalChannels == 1) {
		d.predPrev = [2]int16{}
		d.sSide = [2]int16{}
		chs[1].resampler = chs[0].resampler
	}
	d.apiChannels = apiChannels
	d.internalChannels = f.channels
	if !lost && chs[0].nFramesDecoded == 0 {
		d.readFlags(rc, f.channels)
	}
	var pred [2]int32
	decodeOnlyMiddle := false
	if f.channels == 2 {
		if !lost {
			pred = decodeStereoPred(rc)
			if !chs[1].vadFlags[chs[0].nFramesDecoded] {
				decodeOnlyMiddle = rc.icdf(silkStereoOnlyCodeMidICDF[:], 8) == 1
			}
		} else {
			pred = [2]int32{int32(d.predPrev[0]), int32(d.predPrev[1])}
		}
	}
	if f.channels == 2 && !decodeOnlyMiddle && d.prevDecodeOnlyMiddle {
		side := &chs[1]
		clear(side.outBuf[:])
		clear(side.sLPC[:])
		side.lagPrev = 100
		side.lastGainIndex = 10
		side.prevSignalType = silkInactive
		side.firstFrameAfterReset = true
	}
	hasSide := !d.prevDecodeOnlyMiddle
	if !lost {
		hasSide = !decodeOnlyMiddle
	}
	frameLength := chs[0].frameLength
	nDec := 0
	for n := range f.channels {
		ch := &chs[n]
		out := d.tmp[n][2 : 2+frameLength]
		if n == 0 || hasSide {
			frameIndex := chs[0].nFramesDecoded - n
			coding := codeConditionally
			switch {
			case frameIndex <= 0:
				coding = codeIndependently
			case n > 0 && d.prevDecodeOnlyMiddle:
				coding = codeIndependentlyNoLTPScaling
			}
			ch.decodeFrame(rc, out, d.pulses[:], lost, coding)
			nDec = ch.frameLength
		} else {
			clear(out[:nDec])
		}
		ch.nFramesDecoded++
	}
	if apiChannels == 2 && f.channels == 2 {
		d.msToLR(pred, chs[0].fsKHz, nDec)
	} else {
		copy(d.tmp[0][:2], d.sMid[:])
		copy(d.sMid[:], d.tmp[0][nDec:nDec+2])
	}
	nOut := nDec * 48 / int(chs[0].fsKHz)
	res := d.resampled[:nOut]
	for n := range min(apiChannels, f.channels) {
		chs[n].resampler.resample(res, d.tmp[n][1:1+nDec])
		for i, v := range res {
			out[i*apiChannels+n] = float32(v)
		}
	}
	if apiChannels == 2 && f.channels == 1 {
		if stereoToMono {
			chs[1].resampler.resample(res, d.tmp[0][1:1+nDec])
			for i, v := range res {
				out[2*i+1] = float32(v)
			}
		} else {
			for i := range nOut {
				out[2*i+1] = out[2*i]
			}
		}
	}
	if lost {
		for i := range d.internalChannels {
			chs[i].lastGainIndex = 10
		}
	} else {
		d.prevDecodeOnlyMiddle = decodeOnlyMiddle
	}
	return nOut
}

// readFlags reads a packet's VAD and LBRR flags and skips its LBRR
// frames, which only forward error correction uses.
func (d *silkDecoder) readFlags(rc *rangeDecoder, channels int) {
	chs := &d.ch
	for n := range channels {
		ch := &chs[n]
		for i := range ch.nFramesPerPacket {
			ch.vadFlags[i] = rc.bitLogp(1)
		}
		ch.lbrrFlag = rc.bitLogp(1)
	}
	for n := range channels {
		ch := &chs[n]
		ch.lbrrFlags = [silkMaxFrames]bool{}
		if !ch.lbrrFlag {
			continue
		}
		if ch.nFramesPerPacket == 1 {
			ch.lbrrFlags[0] = true
			continue
		}
		table := silkLBRRFlags2ICDF[:]
		if ch.nFramesPerPacket == 3 {
			table = silkLBRRFlags3ICDF[:]
		}
		sym := rc.icdf(table, 8) + 1
		for i := range ch.nFramesPerPacket {
			ch.lbrrFlags[i] = (sym>>i)&1 != 0
		}
	}
	for i := range chs[0].nFramesPerPacket {
		for n := range channels {
			ch := &chs[n]
			if !ch.lbrrFlags[i] {
				continue
			}
			if channels == 2 && n == 0 {
				decodeStereoPred(rc)
				if !chs[1].lbrrFlags[i] {
					rc.icdf(silkStereoOnlyCodeMidICDF[:], 8)
				}
			}
			coding := codeIndependently
			if i > 0 && ch.lbrrFlags[i-1] {
				coding = codeConditionally
			}
			ch.decodeIndices(rc, i, true, coding)
			decodePulses(rc, d.pulses[:], ch.indices.signalType, ch.indices.quantOffsetType, ch.frameLength)
		}
	}
}

// decodeStereoPred is silk_stereo_decode_pred: the frame's mid/side
// predictors in Q13.
func decodeStereoPred(rc *rangeDecoder) [2]int32 {
	var ix [2][3]int32
	n := rc.icdf32(silkStereoPredJointICDF[:], 8)
	ix[0][2] = n / 5
	ix[1][2] = n - 5*ix[0][2]
	for i := range 2 {
		ix[i][0] = rc.icdf32(silkUniform3ICDF[:], 8)
		ix[i][1] = rc.icdf32(silkUniform5ICDF[:], 8)
	}
	var pred [2]int32
	for i := range 2 {
		ix[i][0] += 3 * ix[i][2]
		low := int32(silkStereoPredQuant[ix[i][0]])
		step := smulwb(int32(silkStereoPredQuant[ix[i][0]+1])-low, 6554)
		pred[i] = smlabb(low, step, 2*ix[i][1]+1)
	}
	pred[0] -= pred[1]
	return pred
}

// msToLR is silk_stereo_MS_to_LR on the decoded mid and side in tmp,
// interpolating the predictors over the first 8 ms.
func (d *silkDecoder) msToLR(pred [2]int32, fsKHz int32, n int) {
	x1, x2 := d.tmp[0][:n+2], d.tmp[1][:n+2]
	copy(x1[:2], d.sMid[:])
	copy(x2[:2], d.sSide[:])
	copy(d.sMid[:], x1[n:n+2])
	copy(d.sSide[:], x2[n:n+2])
	pred0, pred1 := int32(d.predPrev[0]), int32(d.predPrev[1])
	interp := 8 * fsKHz
	denom := int32(1<<16) / interp
	delta0 := rshiftRound(smulbb(pred[0]-int32(d.predPrev[0]), denom), 16)
	delta1 := rshiftRound(smulbb(pred[1]-int32(d.predPrev[1]), denom), 16)
	for i := range n {
		if i < int(interp) {
			pred0 += delta0
			pred1 += delta1
		} else {
			pred0, pred1 = pred[0], pred[1]
		}
		sum := (int32(x1[i]) + int32(x1[i+2]) + int32(x1[i+1])<<1) << 9
		sum = smlawb(int32(x2[i+1])<<8, sum, pred0)
		sum = smlawb(sum, int32(x1[i+1])<<11, pred1)
		x2[i+1] = sat16(rshiftRound(sum, 8))
	}
	d.predPrev = [2]int16{wrap16(pred[0]), wrap16(pred[1])}
	for i := range n {
		s := int32(x1[i+1]) + int32(x2[i+1])
		df := int32(x1[i+1]) - int32(x2[i+1])
		x1[i+1] = sat16(s)
		x2[i+1] = sat16(df)
	}
}
