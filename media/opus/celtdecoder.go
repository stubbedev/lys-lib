package opus

import (
	"math"

	"github.com/stubbedev/lys-lib/intconv"
	"github.com/stubbedev/lys-lib/third_party/gomath"
)

const (
	// decodeBufferSize is the synthesis history each channel keeps,
	// which the post-filter and the loss concealment reach back into.
	decodeBufferSize = 2048
	celtLPCOrder     = 24
	maxPeriod        = 1024
	plcPitchLagMax   = 720
	plcPitchLagMin   = 100
	// plcLagWindow is the squared step of the concealment LPC's lag
	// window.
	plcLagWindow = float32(0.008) * float32(0.008)
)

// celtFrameKind is how the last frame was produced, which decides how
// the next lost one is concealed.
type celtFrameKind int8

const (
	frameNone celtFrameKind = iota
	frameNormal
	framePLCNoise
	framePLCPeriodic
)

// celtDecoder is celt_decode_with_ec's state: the coded band range,
// the synthesis history, the band energies of the last frames and the
// background noise, the post-filter, the concealment's history and
// the noise seed, for channels output channels.
type celtDecoder struct {
	channels       int
	streamChannels int
	start, end     int

	rng              uint32
	lastPitchIndex   int
	lossDuration     int
	plcDuration      int
	lastFrame        celtFrameKind
	skipPLC          bool
	pf, pfOld        pitchFilter
	prefilterAndFold bool
	preemph          [2]float32
	mem              [2][decodeBufferSize + overlap]float32
	oldE             [2][nbBands]float32
	oldLogE          [2][nbBands]float32
	oldLogE2         [2][nbBands]float32
	backgroundLogE   [2][nbBands]float32
	lpc              [2][celtLPCOrder]float32

	rc     rangeDecoder
	bands  bandDecoder
	x      [2][FrameSize]float32
	freq   [2][FrameSize]float32
	fftIn  [longN4]complex64
	fftO   [longN4]complex64
	masks  [2 * nbBands]uint
	pitchX [decodeBufferSize >> 1]float32
	xcorr  [plcPitchLagMax >> 1]float32
	exc    [maxPeriod + celtLPCOrder]float32
	excTmp [maxPeriod]float32
	iirTmp [FrameSize + overlap + celtLPCOrder]float32
}

func newCELTDecoder(channels int) *celtDecoder {
	d := &celtDecoder{channels: channels, streamChannels: channels, end: nbBands}
	d.reset()
	return d
}

// reset is OPUS_RESET_STATE: silence before the next frame, keeping
// the channel counts and the coded band range.
func (d *celtDecoder) reset() {
	*d = celtDecoder{channels: d.channels, streamChannels: d.streamChannels, start: d.start, end: d.end}
	for c := range 2 {
		for i := range nbBands {
			d.oldLogE[c][i], d.oldLogE2[c][i] = -28, -28
		}
	}
	d.skipPLC = true
	d.bands.disableInv = d.channels == 1
}

// decodeFrame decodes the CELT frame frame of 2.5 ms << lm, a frame of
// at most one byte as lost, into out (see decode).
func (d *celtDecoder) decodeFrame(frame []byte, lm int, out []float32, accum bool) {
	if len(frame) <= 1 {
		d.conceal(lm, out, accum)
		return
	}
	d.rc.reset(frame)
	d.decode(&d.rc, lm, out, accum)
}

// conceal is celt_decode_with_ec without data: celt_decode_lost's
// estimate of a lost frame of 2.5 ms << lm into out.
func (d *celtDecoder) conceal(lm int, out []float32, accum bool) {
	n := shortMdct << lm
	d.decodeLost(n, lm)
	d.deemphasis(n, out, accum)
}

// decode decodes one CELT frame of 2.5 ms << lm in streamChannels
// channels coding bands [start, end) from rc, which a hybrid frame's
// SILK layer may have read from already, and writes its 48 kHz samples,
// as float in int16 scale, interleaved over the output channels into
// out, or adds them to out when accum is set.
func (d *celtDecoder) decode(rc *rangeDecoder, lm int, out []float32, accum bool) {
	c := d.streamChannels
	cc := d.channels
	start, end := d.start, d.end
	m := 1 << lm
	n := shortMdct << lm
	if d.lossDuration == 0 {
		d.skipPLC = false
	}
	if c == 1 {
		for i := range nbBands {
			d.oldE[0][i] = max(d.oldE[0][i], d.oldE[1][i])
		}
	}
	totalBits := len(rc.buf) * 8
	tell := rc.tell()
	silence := tell >= totalBits
	if tell == 1 {
		silence = rc.bitLogp(15)
	}
	if silence {
		tell = totalBits
		rc.nbitsTotal += tell - rc.tell()
	}
	var pf pitchFilter
	if start == 0 && tell+16 <= totalBits {
		if rc.bitLogp(1) {
			octave := int(rc.uint(6))
			pf.period = 16<<octave + int(rc.bits(uint(4+octave))) - 1
			qg := int(rc.bits(3))
			if rc.tell()+2 <= totalBits {
				pf.tapset = rc.icdf(tapsetICDF, 2)
			}
			pf.gain = 0.09375 * float32(qg+1)
		}
		tell = rc.tell()
	}
	transient := false
	if lm > 0 && tell+3 <= totalBits {
		transient = rc.bitLogp(3)
		tell = rc.tell()
	}
	intra := tell+3 <= totalBits && rc.bitLogp(3)
	if !intra && d.lossDuration != 0 {
		d.safeEnergies(lm)
	}
	decodeCoarseEnergy(rc, c, lm, start, end, &d.oldE, intra)
	var tfRes [nbBands]int
	decodeTF(rc, transient, lm, start, end, &tfRes)
	spread := spreadNormal
	if rc.tell()+4 <= totalBits {
		spread = rc.icdf(spreadICDF, 5)
	}
	caps := bandCaps(c, lm)
	var offsets [nbBands]int
	dynallocLogp := uint(6)
	totalBits8 := totalBits << bitRes
	tellFrac := rc.tellFrac()
	for i := start; i < end; i++ {
		width := c * bandWidth(i) << lm
		quanta := min(width<<bitRes, max(6<<bitRes, width))
		loopLogp := dynallocLogp
		boost := 0
		for tellFrac+int(loopLogp<<bitRes) < totalBits8 && boost < caps[i] {
			flag := rc.bitLogp(loopLogp)
			tellFrac = rc.tellFrac()
			if !flag {
				break
			}
			boost += quanta
			totalBits8 -= quanta
			loopLogp = 1
		}
		offsets[i] = boost
		if boost > 0 {
			dynallocLogp = max(2, dynallocLogp-1)
		}
	}
	trim := 5
	if tellFrac+6<<bitRes <= totalBits8 {
		trim = rc.icdf(trimICDF, 7)
	}
	bits := totalBits<<bitRes - rc.tellFrac() - 1
	antiCollapseRsv := 0
	if transient && lm >= 2 && bits >= (lm+2)<<bitRes {
		antiCollapseRsv = 1 << bitRes
	}
	bits -= antiCollapseRsv
	a := allocate(allocDecoder{rc: rc}, c, lm, start, end, &offsets, &caps, trim, bits)
	decodeFineEnergy(rc, c, start, end, &d.oldE, &a.fine)
	for ch := range cc {
		copy(d.mem[ch][:], d.mem[ch][n:])
	}
	blocks := 1
	if transient {
		blocks = m
	}
	q := &d.bands
	q.rc, q.lm, q.spread, q.seed = rc, lm, spread, d.rng
	var y []float32
	if c == 2 {
		y = d.x[1][:n]
	}
	q.decodeAll(d.x[0][:n], y, &d.masks, &a, start, end, blocks, &tfRes, totalBits<<bitRes-antiCollapseRsv)
	d.rng = q.seed
	antiCollapseOn := antiCollapseRsv > 0 && rc.bits(1) == 1
	decodeFinalEnergy(rc, c, start, end, &d.oldE, &a.fine, &a.priority, totalBits-rc.tell())
	if antiCollapseOn {
		d.antiCollapse(c, lm, start, end, &a)
	}
	if silence {
		for ch := range 2 {
			for i := range nbBands {
				d.oldE[ch][i] = -28
			}
		}
	}
	if d.prefilterAndFold {
		d.foldPrefilter(n)
	}
	d.synthesise(c, lm, start, end, transient, silence)
	d.postfilter(n, lm, pf)
	if c == 1 {
		d.oldE[1] = d.oldE[0]
	}
	if transient {
		for ch := range 2 {
			for i := range nbBands {
				d.oldLogE[ch][i] = min(d.oldLogE[ch][i], d.oldE[ch][i])
			}
		}
	} else {
		d.oldLogE2 = d.oldLogE
		d.oldLogE = d.oldE
	}
	increase := float32(float32(min(160, d.lossDuration+m)) * 0.001)
	for ch := range 2 {
		for i := range nbBands {
			d.backgroundLogE[ch][i] = min(d.backgroundLogE[ch][i]+increase, d.oldE[ch][i])
		}
	}
	for ch := range 2 {
		for i := range nbBands {
			if i >= start && i < end {
				continue
			}
			d.oldE[ch][i] = 0
			d.oldLogE[ch][i], d.oldLogE2[ch][i] = -28, -28
		}
	}
	d.rng = rc.rng
	d.deemphasis(n, out, accum)
	d.lossDuration = 0
	d.plcDuration = 0
	d.lastFrame = frameNormal
	d.prefilterAndFold = false
}

// safeEnergies lowers the predicted energies of a frame after a loss,
// continuing a falling trend or taking the minimum of the last
// frames, so concealment does not carry over into loud artifacts.
func (d *celtDecoder) safeEnergies(lm int) {
	missing := min(10, d.lossDuration>>lm)
	safety := float32(0)
	switch lm {
	case 0:
		safety = 1.5
	case 1:
		safety = 0.5
	}
	for c := range 2 {
		for i := d.start; i < d.end; i++ {
			e0, e1, e2 := d.oldE[c][i], d.oldLogE[c][i], d.oldLogE2[c][i]
			if e0 < max(e1, e2) {
				slope := max(e1-e0, 0.5*(e2-e0))
				slope = min(slope, 2)
				e0 -= max(0, float32(1+missing)*slope)
				d.oldE[c][i] = max(-20, e0)
			} else {
				d.oldE[c][i] = min(e0, e1, e2)
			}
			d.oldE[c][i] -= safety
		}
	}
}

// postfilter runs the comb post-filter over the frame's n samples per
// channel, cross-fading from the last frame's filter to pf.
func (d *celtDecoder) postfilter(n, lm int, pf pitchFilter) {
	d.pf.period = max(d.pf.period, combMinPeriod)
	d.pfOld.period = max(d.pfOld.period, combMinPeriod)
	for ch := range d.channels {
		syn := d.mem[ch][decodeBufferSize-n:]
		buf := d.mem[ch][:]
		at := decodeBufferSize - n
		combFilter(syn[:shortMdct], buf, at, d.pfOld, d.pf, mdctWindow[:])
		if lm != 0 {
			combFilter(syn[shortMdct:n], buf, at+shortMdct, d.pf, pf, mdctWindow[:])
		}
	}
	d.pfOld, d.pf = d.pf, pf
	if lm != 0 {
		d.pfOld = d.pf
	}
}

// foldPrefilter is prefilter_and_fold: it runs the pre-filter over the
// overlap a concealed frame left and folds it as the MDCT's TDAC
// would, so the next decoded frame blends in.
func (d *celtDecoder) foldPrefilter(n int) {
	var etmp [overlap]float32
	f0 := pitchFilter{period: d.pfOld.period, gain: -d.pfOld.gain, tapset: d.pfOld.tapset}
	f1 := pitchFilter{period: d.pf.period, gain: -d.pf.gain, tapset: d.pf.tapset}
	w := mdctWindow
	for ch := range d.channels {
		buf := d.mem[ch][:]
		at := decodeBufferSize - n
		combFilter(etmp[:], buf, at, f0, f1, nil)
		for i := range overlap / 2 {
			buf[at+i] = float32(w[i]*etmp[overlap-1-i]) + float32(w[overlap-i-1]*etmp[i])
		}
	}
}

// decodeLost is celt_decode_lost: a frame of n samples per channel
// from noise at the decaying band energies, or by extrapolating the
// pitch period of the last decoded audio through its LPC filter.
func (d *celtDecoder) decodeLost(n, lm int) {
	cc := d.channels
	start, end := d.start, d.end
	kind := framePLCPeriodic
	if d.plcDuration >= 40 || start != 0 || d.skipPLC {
		kind = framePLCNoise
	}
	if kind == framePLCNoise {
		for ch := range cc {
			copy(d.mem[ch][:], d.mem[ch][n:])
		}
		if d.prefilterAndFold {
			d.foldPrefilter(n)
		}
		decay := float32(0.5)
		if d.lossDuration == 0 {
			decay = 1.5
		}
		for ch := range cc {
			for i := start; i < end; i++ {
				d.oldE[ch][i] = max(d.backgroundLogE[ch][i], d.oldE[ch][i]-decay)
			}
		}
		seed := d.rng
		for ch := range cc {
			for i := start; i < end; i++ {
				band := d.x[ch][eBands[i]<<lm : eBands[i+1]<<lm]
				for j := range band {
					seed = lcgRand(seed)
					band[j] = float32(intconv.Int32Bits(seed) >> 20)
				}
				renormalise(band, 1)
			}
		}
		d.rng = seed
		d.synthesise(cc, lm, start, end, false, false)
		d.postfilter(n, lm, d.pf)
		d.prefilterAndFold = false
		d.skipPLC = true
	} else {
		d.extrapolatePitch(n)
		d.prefilterAndFold = true
	}
	d.lossDuration = min(10000, d.lossDuration+1<<lm)
	d.plcDuration = min(10000, d.plcDuration+1<<lm)
	d.lastFrame = kind
}

// extrapolatePitch is celt_decode_lost's periodic concealment: each
// channel's LPC excitation repeated at the pitch period with the
// decay of the last periods, filtered back and kept from exploding.
func (d *celtDecoder) extrapolatePitch(n int) {
	fade := float32(1)
	var pitch int
	if d.lastFrame != framePLCPeriodic {
		pitch = d.plcPitchSearch()
		d.lastPitchIndex = pitch
	} else {
		pitch = d.lastPitchIndex
		fade = 0.8
	}
	excLength := min(2*pitch, maxPeriod)
	w := mdctWindow
	for ch := range d.channels {
		buf := d.mem[ch][:]
		excBuf := d.exc[:]
		copy(excBuf, buf[decodeBufferSize-maxPeriod-celtLPCOrder:decodeBufferSize])
		exc := excBuf[celtLPCOrder:]
		lpc := &d.lpc[ch]
		if d.lastFrame != framePLCPeriodic {
			var ac [celtLPCOrder + 1]float32
			autocorr(exc[:maxPeriod], ac[:], w[:])
			ac[0] *= 1.0001
			for i := 1; i <= celtLPCOrder; i++ {
				ac[i] -= float32(float32(float32(ac[i]*plcLagWindow)*float32(i)) * float32(i))
			}
			*lpc = [celtLPCOrder]float32{}
			lpcFromAutocorr(ac[:], lpc[:])
		}
		from := celtLPCOrder + maxPeriod - excLength
		fir(excBuf[from-celtLPCOrder:from+excLength], lpc, d.excTmp[:excLength])
		copy(excBuf[from:], d.excTmp[:excLength])
		e1, e2 := float32(1), float32(1)
		decayLength := excLength >> 1
		for i := range decayLength {
			e := exc[maxPeriod-decayLength+i]
			e1 += float32(e * e)
			e = exc[maxPeriod-2*decayLength+i]
			e2 += float32(e * e)
		}
		e1 = min(e1, e2)
		decay := float32(math.Sqrt(float64(e1 / e2)))
		copy(buf, buf[n:decodeBufferSize])
		offset := maxPeriod - pitch
		extLen := n + overlap
		attenuation := fade * decay
		var s1 float32
		at := decodeBufferSize - n
		for i, j := 0, 0; i < extLen; i, j = i+1, j+1 {
			if j >= pitch {
				j -= pitch
				attenuation *= decay
			}
			buf[at+i] = attenuation * exc[offset+j]
			t := buf[decodeBufferSize-maxPeriod-n+offset+j]
			s1 += float32(t * t)
		}
		var lpcMem [celtLPCOrder]float32
		for i := range lpcMem {
			lpcMem[i] = buf[at-1-i]
		}
		iir(buf[at:at+extLen], lpc, &lpcMem, d.iirTmp[:extLen+celtLPCOrder])
		var s2 float32
		for _, v := range buf[at : at+extLen] {
			s2 += float32(v * v)
		}
		switch {
		case !(s1 > float32(0.2*s2)):
			clear(buf[at : at+extLen])
		case s1 < s2:
			ratio := float32(math.Sqrt(float64((s1 + 1) / (s2 + 1))))
			for i := range overlap {
				buf[at+i] *= 1 - float32(w[i]*(1-ratio))
			}
			for i := overlap; i < extLen; i++ {
				buf[at+i] *= ratio
			}
		}
	}
}

// plcPitchSearch is celt_plc_pitch_search: the pitch period of the
// last decoded audio, in samples.
func (d *celtDecoder) plcPitchSearch() int {
	var in [2][]float32
	for ch := range d.channels {
		in[ch] = d.mem[ch][:decodeBufferSize]
	}
	lp := d.pitchX[:]
	pitchDownsample(in[:d.channels], lp)
	p := pitchSearch(lp[plcPitchLagMax>>1:], lp, decodeBufferSize-plcPitchLagMax, plcPitchLagMax-plcPitchLagMin, d.xcorr[:])
	return plcPitchLagMax - p
}

// autocorr is _celt_autocorr: the autocorrelation of x, its ends
// tapered by window, at lags 0 to len(ac)-1.
func autocorr(x, ac, window []float32) {
	var xx [maxPeriod]float32
	n := len(x)
	copy(xx[:n], x)
	for i, w := range window {
		xx[i] = x[i] * w
		xx[n-i-1] = x[n-i-1] * w
	}
	lag := len(ac) - 1
	fast := n - lag
	for k := range ac {
		var sum float32
		for j := range fast {
			sum += float32(xx[j] * xx[j+k])
		}
		var tail float32
		for i := k + fast; i < n; i++ {
			tail += float32(xx[i] * xx[i-k])
		}
		ac[k] = sum + tail
	}
}

// fir is celt_fir: y[i] = x[ord+i] plus the predictor lpc over the ord
// samples before it.
func fir(x []float32, lpc *[celtLPCOrder]float32, y []float32) {
	for i := range y {
		sum := x[celtLPCOrder+i]
		for j := range celtLPCOrder {
			sum += float32(lpc[celtLPCOrder-1-j] * x[i+j])
		}
		y[i] = sum
	}
}

// iir is celt_iir in place: x through the all-pole filter 1/(1+lpc)
// with past outputs mem (most recent first), in libopus's blocks of
// four.
func iir(x []float32, lpc *[celtLPCOrder]float32, mem *[celtLPCOrder]float32, y []float32) {
	const ord = celtLPCOrder
	n := len(x)
	for i := range ord {
		y[i] = -mem[ord-i-1]
	}
	clear(y[ord:])
	kernel := func(sum *[4]float32, at int) {
		for j := range ord {
			r := lpc[ord-1-j]
			for k := range 4 {
				sum[k] += float32(r * y[at+k+j])
			}
		}
	}
	i := 0
	for ; i < n-3; i += 4 {
		sum := [4]float32{x[i], x[i+1], x[i+2], x[i+3]}
		kernel(&sum, i)
		y[i+ord] = -sum[0]
		x[i] = sum[0]
		sum[1] += float32(y[i+ord] * lpc[0])
		y[i+ord+1] = -sum[1]
		x[i+1] = sum[1]
		sum[2] += float32(y[i+ord+1] * lpc[0])
		sum[2] += float32(y[i+ord] * lpc[1])
		y[i+ord+2] = -sum[2]
		x[i+2] = sum[2]
		sum[3] += float32(y[i+ord+2] * lpc[0])
		sum[3] += float32(y[i+ord+1] * lpc[1])
		sum[3] += float32(y[i+ord] * lpc[2])
		y[i+ord+3] = -sum[3]
		x[i+3] = sum[3]
	}
	for ; i < n; i++ {
		sum := x[i]
		for j := range ord {
			sum -= float32(lpc[ord-1-j] * y[i+j])
		}
		y[i+ord] = sum
		x[i] = sum
	}
	for i := range ord {
		mem[i] = x[n-i-1]
	}
}

// synthesise scales the decoded shapes by their band energies into
// MDCT coefficients and inverts them into each output channel's
// history, duplicating a mono stream or downmixing a stereo one when
// the stream's channels are not the output's.
func (d *celtDecoder) synthesise(c, lm, start, end int, transient, silence bool) {
	n := shortMdct << lm
	blocks, nb, t := 1, n, imdcts[lm]
	if transient {
		blocks, nb, t = 1<<lm, shortMdct, imdcts[0]
	}
	inverse := func(freq []float32, ch int) {
		syn := d.mem[ch][decodeBufferSize-n:]
		for b := range blocks {
			t.backward(freq[b:], blocks, syn[nb*b:], d.fftIn[:], d.fftO[:])
		}
	}
	switch {
	case d.channels == 2 && c == 1:
		denormalise(d.x[0][:n], d.freq[0][:n], &d.oldE[0], lm, start, end, silence)
		copy(d.freq[1][:n], d.freq[0][:n])
		inverse(d.freq[0][:n], 0)
		inverse(d.freq[1][:n], 1)
	case d.channels == 1 && c == 2:
		denormalise(d.x[0][:n], d.freq[0][:n], &d.oldE[0], lm, start, end, silence)
		denormalise(d.x[1][:n], d.freq[1][:n], &d.oldE[1], lm, start, end, silence)
		for i := range n {
			d.freq[0][i] = float32(0.5*d.freq[0][i]) + float32(0.5*d.freq[1][i])
		}
		inverse(d.freq[0][:n], 0)
	default:
		for ch := range c {
			denormalise(d.x[ch][:n], d.freq[ch][:n], &d.oldE[ch], lm, start, end, silence)
			inverse(d.freq[ch][:n], ch)
		}
	}
}

// denormalise is denormalise_bands: each band's shape in x scaled by
// its energy into freq, zero outside [start, end).
func denormalise(x, freq []float32, logE *[nbBands]float32, lm, start, end int, silence bool) {
	m := 1 << lm
	bound := m * eBands[end]
	if silence {
		bound, start, end = 0, 0, 0
	}
	clear(freq[:m*eBands[start]])
	for i := start; i < end; i++ {
		g := float32(gomath.Exp2(float64(min(32, logE[i]+eMeans[i]))))
		for j := m * eBands[i]; j < m*eBands[i+1]; j++ {
			freq[j] = x[j] * g
		}
	}
	clear(freq[bound:])
}

// antiCollapse fills the short blocks of transient bands that got no
// pulses with noise at the energy of the frames before, so a
// transient does not leave holes in time.
func (d *celtDecoder) antiCollapse(c, lm, start, end int, a *allocation) {
	seed := d.rng
	for i := start; i < end; i++ {
		n0 := bandWidth(i)
		depth := (1 + a.pulses[i]) / n0 >> lm
		thresh := float32(0.5 * gomath.Exp2(-0.125*float64(depth)))
		sqrt1 := 1 / float32(math.Sqrt(float64(n0<<lm)))
		for ch := range c {
			prev1, prev2 := d.oldLogE[ch][i], d.oldLogE2[ch][i]
			if c == 1 {
				prev1 = max(prev1, d.oldLogE[1][i])
				prev2 = max(prev2, d.oldLogE2[1][i])
			}
			ediff := max(0, d.oldE[ch][i]-min(prev1, prev2))
			r := float32(2 * gomath.Exp2(-float64(ediff)))
			if lm == 3 {
				r *= 1.41421356
			}
			r = min(thresh, r) * sqrt1
			x := d.x[ch][eBands[i]<<lm : eBands[i+1]<<lm]
			renormalize := false
			for k := range 1 << lm {
				if d.masks[i*c+ch]&(1<<k) != 0 {
					continue
				}
				for j := range n0 {
					seed = lcgRand(seed)
					v := r
					if seed&0x8000 == 0 {
						v = -r
					}
					x[j<<lm+k] = v
				}
				renormalize = true
			}
			if renormalize {
				renormalise(x, 1)
			}
		}
	}
}

// deemphasis undoes the encoder's pre-emphasis on the frame's n
// samples per channel and writes them interleaved into out, or adds
// them to out when accum is set.
func (d *celtDecoder) deemphasis(n int, out []float32, accum bool) {
	cc := d.channels
	dst := out[:n*cc]
	for ch := range cc {
		syn := d.mem[ch][decodeBufferSize-n : decodeBufferSize]
		mem := d.preemph[ch]
		if accum {
			for j, v := range syn {
				tmp := v + mem + 1e-30
				mem = preemphasis * tmp
				dst[j*cc+ch] += tmp
			}
		} else {
			for j, v := range syn {
				tmp := v + 1e-30 + mem
				mem = preemphasis * tmp
				dst[j*cc+ch] = tmp
			}
		}
		d.preemph[ch] = mem
	}
}
