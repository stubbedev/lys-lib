// Package opus is a pure-Go Opus encoder (RFC 6716) for lys-ui's
// recordings: CELT-only fullband 20 ms frames (configuration 31) at a
// constant or constrained variable bitrate, from the media package's PCM
// layout (48 kHz, interleaved signed 16-bit samples, one or two
// channels).
//
// It is a port of the RFC's reference encoder with libopus's later
// analysis: a 3 Hz high-pass, transient detection with short blocks and
// anti-collapse, time-frequency resolution changes, the pitch
// pre-filter, inter/intra energy prediction, dynamic allocation, the
// full bit allocation, spreading, intensity and dual stereo. It leaves
// out libopus's tonality analysis, which steers some of those choices
// when it is compiled in.
//
//	enc, err := opus.NewEncoder(2, 128000, opus.CBR)
//	head := enc.AppendHead(nil)        // the stream's ID header
//	pkt, err := enc.Encode(nil, frame) // FrameSize*2 samples per call
package opus

import (
	"encoding/binary"
	"errors"
	"fmt"
	"slices"
)

// The stream geometry the encoder produces.
const (
	// SampleRate is the input and coding rate in Hz.
	SampleRate = 48000
	// FrameSize is the samples per channel in one packet (20 ms).
	FrameSize = 960
	// PreSkip is the encoder's lookahead in samples at 48 kHz: a
	// decoder drops this many samples from the start of the stream.
	PreSkip = overlap
	// MinBitrate and MaxBitrate bound NewEncoder's bitrate in bits per
	// second.
	MinBitrate = 6000
	MaxBitrate = 510000
)

// RateControl is how an Encoder spends its bitrate.
type RateControl int

const (
	// CBR makes every packet the same size.
	CBR RateControl = iota
	// ConstrainedVBR sizes each packet by how hard its frame is to
	// code (transients and loud frames get more, correlated stereo
	// less) while a bit reservoir holds the stream to the bitrate: it
	// never runs more than one average packet ahead, so no packet
	// exceeds twice the average.
	ConstrainedVBR
)

// tocConfig is the TOC configuration CELT-only fullband 20 ms frames
// carry (RFC 6716 section 3.1).
const tocConfig = 31

// maxFrameBytes is the most a CELT frame can take (RFC 6716 section
// 3.2.1).
const maxFrameBytes = 1275

// preemphasis is the coefficient of CELT's first-order pre-emphasis.
const preemphasis = float32(0.85000610)

// The input high-pass (dc_reject in libopus) at 3 Hz removes DC and
// subsonic content, which would otherwise take bits in the first band.
const (
	highPassCoef = float32(6.3 * 3 / SampleRate)
	// highPassFloor keeps the filter's memory out of denormals.
	highPassFloor = float32(1e-30)
)

// silenceLevel is the largest sample amplitude that still codes as
// digital silence: half an int16 step.
const silenceLevel = float32(0.5)

// Encoder turns PCM frames into Opus packets. It keeps the signal's
// history between frames, so one Encoder codes one stream in order.
type Encoder struct {
	channels int
	rate     RateControl
	bitrate  int
	// maxBytes is the largest packet, TOC byte included.
	maxBytes int
	// vbrRate is a constrained VBR frame's average size in 1/8 bits.
	vbrRate int
	caps    [nbBands]int
	// longBlocksOnly codes every frame with long blocks, the baseline
	// the tests measure short blocks' pre-echo against.
	longBlocksOnly bool

	highPassMem     [2]float32
	preemphMem      [2]float32
	overlapMax      float32
	inMem           [2][overlap]float32
	pre             [2][combMaxPeriod + FrameSize]float32
	pitch           pitchFilter
	tapsetDecision  int
	hfAverage       int
	spread          int
	tonalAverage    int
	intensity       int
	stereoSaving    float32
	specAvg         float32
	oldE            [2][nbBands]float32
	energyError     [2][nbBands]float32
	lastCodedBands  int
	consecTransient int
	vbr             vbrState

	rc           rangeEncoder
	pvq          pvq
	in           [2][FrameSize + overlap]float32
	freq         [2][FrameSize]float32
	norm         [2][FrameSize]float32
	amp          [2][nbBands]float32
	logE         [2][nbBands]float32
	logE2        [2][nbBands]float32
	errs         [2][nbBands]float32
	fftIn        [longN4]complex64
	fftOut       [longN4]complex64
	intraOld     [2][nbBands]float32
	intraErr     [2][nbBands]float32
	intraBuf     []byte
	pitchBuf     [pitchBufLen]float32
	xcorr        [combMaxPeriod/2 + 1]float32
	transientTmp [FrameSize + overlap]float32
	tfTmp        [2][FrameSize]float32
}

// NewEncoder returns an encoder for channels (1 or 2) at bitrate bits
// per second with the given rate control. A CBR bitrate rounds down to
// whole bytes per packet.
func NewEncoder(channels, bitrate int, rate RateControl) (*Encoder, error) {
	if channels != 1 && channels != 2 {
		return nil, fmt.Errorf("opus: %d channels: only 1 or 2 are supported", channels)
	}
	if bitrate < MinBitrate || bitrate > MaxBitrate {
		return nil, fmt.Errorf("opus: bitrate %d b/s outside [%d, %d]", bitrate, MinBitrate, MaxBitrate)
	}
	e := &Encoder{
		channels:     channels,
		rate:         rate,
		bitrate:      bitrate,
		caps:         bandCaps(channels),
		spread:       spreadNormal,
		tonalAverage: 256,
	}
	switch rate {
	case CBR:
		e.maxBytes = bitrate * FrameSize / (8 * SampleRate)
	case ConstrainedVBR:
		e.vbrRate = (bitrate*FrameSize/SampleRate - 8) << bitRes
		e.maxBytes = 1 + min(maxFrameBytes, 2*e.vbrRate>>(bitRes+3))
	default:
		return nil, fmt.Errorf("opus: unknown rate control %d", rate)
	}
	e.intraBuf = make([]byte, e.maxBytes-1)
	return e, nil
}

// Channels is the number of interleaved channels Encode expects.
func (e *Encoder) Channels() int { return e.channels }

// MaxPacketBytes is the largest packet Encode appends; with CBR every
// packet is this size.
func (e *Encoder) MaxPacketBytes() int { return e.maxBytes }

// AppendHead appends the stream's OpusHead identification header (RFC
// 7845 section 5.1): channel mapping family 0, PreSkip, no gain. It is
// Ogg's first packet and Matroska's CodecPrivate.
func (e *Encoder) AppendHead(dst []byte) []byte {
	dst = append(dst, "OpusHead"...)
	dst = append(dst, 1, byte(e.channels))
	dst = binary.LittleEndian.AppendUint16(dst, PreSkip)
	dst = binary.LittleEndian.AppendUint32(dst, SampleRate)
	return append(dst, 0, 0, 0)
}

// Encode appends to dst one packet coding pcm, FrameSize samples per
// channel interleaved.
func (e *Encoder) Encode(dst []byte, pcm []int16) ([]byte, error) {
	if len(pcm) != FrameSize*e.channels {
		return dst, fmt.Errorf("opus: frame of %d samples, want %d (%d channels of %d)", len(pcm), FrameSize*e.channels, e.channels, FrameSize)
	}
	start := len(dst)
	dst = slices.Grow(dst, e.maxBytes)[:start+e.maxBytes]
	toc := byte(tocConfig << 3)
	if e.channels == 2 {
		toc |= 1 << 2
	}
	dst[start] = toc
	n, err := e.encodeFrame(dst[start+1:], pcm)
	if err != nil {
		return dst[:start], err
	}
	return dst[:start+1+n], nil
}

// errOverrun reports a frame whose symbols did not fit its bytes, which
// the allocation's budgeting rules out.
var errOverrun = errors.New("opus: range coder overran the packet")

// encodeFrame is celt_encode_with_ec for one frame into out, the
// largest frame the rate control allows; it returns the bytes used.
func (e *Encoder) encodeFrame(out []byte, pcm []int16) (int, error) {
	c := e.channels
	vbr := e.rate == ConstrainedVBR
	rc := &e.rc
	bytes := len(out)
	effectiveBytes := bytes
	equivRate := min(bytes*8*50, e.bitrate)
	if vbr {
		effectiveBytes = e.vbrRate >> (bitRes + 3)
		bytes = min(max(2, (2*e.vbrRate-e.vbr.reservoir)>>(bitRes+3)), bytes)
	}
	availableBytes := bytes
	rc.reset(out[:bytes])
	totalBits := bytes * 8

	silence := e.loadInput(pcm)
	tell := 1
	rc.bitLogp(silence, 15)
	if silence {
		if vbr {
			bytes = min(bytes, 2)
			effectiveBytes, availableBytes = bytes, bytes
			totalBits = bytes * 8
			rc.shrink(bytes)
		}
		tell = totalBits
		rc.nbitsTotal += tell - rc.tell()
	}

	var history [2][]float32
	for ch := range c {
		history[ch] = e.pre[ch][combMaxPeriod-overlap:]
	}
	tn := detectTone(history[:c], e.transientTmp[:])
	tr := analyseTransient(history[:c], tn, e.transientTmp[:])
	tn.strength = min(tn.strength, 1-tr.estimate)

	pfEnabled := availableBytes > 12*c && !silence && tell+16 <= totalBits
	pf, qg, pfOn := e.prefilter(pfEnabled, tn, tr.estimate, availableBytes)
	switch {
	case pfOn:
		rc.bitLogp(true, 1)
		period := pf.period + 1
		octave := ilog(uint32(period)) - 5
		rc.uint(uint32(octave), 6)
		rc.bits(uint32(period-16<<octave), uint(4+octave))
		rc.bits(uint32(qg), 3)
		rc.icdf(pf.tapset, tapsetICDF, 2)
	case tell+16 <= totalBits:
		rc.bitLogp(false, 1)
	}

	transient := tr.detected && !e.longBlocksOnly
	transientDisabled := rc.tell()+3 > totalBits
	if transientDisabled {
		transient = false
	}
	if transient {
		e.computeEnergies(false, &e.logE2)
		e.shortBlockScale()
	}
	e.computeEnergies(transient, &e.logE)
	temporalVBR := e.temporalVBR(transient)
	if !transient {
		e.logE2 = e.logE
	}
	if !transientDisabled && !transient && !e.longBlocksOnly && energyJumped(c, &e.logE, &e.oldE) {
		transient = true
		e.computeEnergies(true, &e.logE)
		e.shortBlockScale()
		tr.estimate = 0.2
	}
	if !transientDisabled {
		rc.bitLogp(transient, 3)
	}

	dyn := dynallocAnalysis(c, &e.logE, &e.logE2, tn, transient, vbr, vbr, effectiveBytes)

	var tfRes [nbBands]int
	tfSelect := 0
	if effectiveBytes >= 15*c && tn.strength < 0.98 {
		lambda := max(80, 20480/effectiveBytes+2)
		tfSelect = tfAnalysis(e.norm[tr.channel][:], transient, tr.estimate, lambda, &dyn.importance, &tfRes, e.tfTmp[0][:], e.tfTmp[1][:])
	} else {
		for i := range tfRes {
			tfRes[i] = b2i(transient)
		}
	}

	for ch := range c {
		for i := range nbBands {
			if abs32(e.logE[ch][i]-e.oldE[ch][i]) < 2 {
				e.logE[ch][i] -= 0.25 * e.energyError[ch][i]
			}
		}
	}
	e.quantCoarse(totalBits, availableBytes)
	encodeTF(rc, transient, tfSelect, &tfRes)

	if rc.tell()+4 <= totalBits {
		if transient || availableBytes < 10*c {
			e.spread = spreadNormal
		} else {
			e.spread = e.spreadingDecision(&dyn.spreadWeight, pfOn)
		}
		rc.icdf(e.spread, spreadICDF, 5)
	} else {
		e.spread = spreadNormal
	}

	offsets := dyn.offsets
	totalBits8 := totalBits << bitRes
	totalBoost := 0
	dynallocLogp := uint(6)
	tellFrac := rc.tellFrac()
	for i := range nbBands {
		width := c * bandWidth(i) << frameLM
		quanta := min(width<<bitRes, max(6<<bitRes, width))
		loopLogp := dynallocLogp
		boost := 0
		j := 0
		for ; tellFrac+int(loopLogp<<bitRes) < totalBits8-totalBoost && boost < e.caps[i]; j++ {
			flag := j < offsets[i]
			rc.bitLogp(flag, loopLogp)
			tellFrac = rc.tellFrac()
			if !flag {
				break
			}
			boost += quanta
			totalBoost += quanta
			loopLogp = 1
		}
		if j != 0 {
			dynallocLogp = max(2, dynallocLogp-1)
		}
		offsets[i] = boost
	}

	dual := false
	if c == 2 {
		dual = e.stereoAnalysis()
		e.intensity = intensityBand(equivRate/1000, e.intensity)
	}

	trim := 5
	if tellFrac+(6<<bitRes) <= totalBits8-totalBoost {
		trim = e.allocTrim(equivRate, tr.estimate)
		rc.icdf(trim, trimICDF, 7)
		tellFrac = rc.tellFrac()
	}

	if vbr {
		minBytes := (tellFrac+totalBoost+(1<<(bitRes+3))-1)>>(bitRes+3) + 2
		base := e.vbrRate - (40*c+20)<<bitRes + e.vbr.offset
		target := e.vbrTarget(base, equivRate, &dyn, tr.estimate, temporalVBR) + tellFrac
		bytes = e.vbr.frameBytes(e.vbrRate, target, minBytes, bytes, silence)
		rc.shrink(bytes)
	}

	bits := bytes*8<<bitRes - rc.tellFrac() - 1
	antiCollapseRsv := 0
	if transient && bits >= (frameLM+2)<<bitRes {
		antiCollapseRsv = 1 << bitRes
	}
	bits -= antiCollapseRsv
	a := allocate(rc, c, &offsets, &e.caps, trim, e.intensity, dual, bits, e.lastCodedBands)
	if c == 2 {
		e.intensity = a.intensity
	}
	if e.lastCodedBands != 0 {
		e.lastCodedBands = min(e.lastCodedBands+1, max(e.lastCodedBands-1, a.codedBands))
	} else {
		e.lastCodedBands = a.codedBands
	}

	fineEnergy(rc, c, &e.oldE, &e.errs, &a.fine)

	blocks := 1
	if transient {
		blocks = shortBlocks
	}
	bc := bandCoder{rc: rc, pvq: &e.pvq, amp: &e.amp, spread: e.spread, intensity: a.intensity, blocks: blocks}
	var y []float32
	if c == 2 {
		y = e.norm[1][:]
	}
	bc.quantAll(e.norm[0][:], y, &a, &tfRes, bytes*8<<bitRes-antiCollapseRsv)

	if antiCollapseRsv > 0 {
		rc.bits(uint32(b2i(e.consecTransient < 2)), 1)
	}
	finaliseEnergy(rc, c, &e.oldE, &e.errs, &a.fine, &a.priority, bytes*8-rc.tell())
	for ch := range c {
		for i := range nbBands {
			e.energyError[ch][i] = max(-0.5, min(0.5, e.errs[ch][i]))
		}
	}

	if silence {
		for ch := range c {
			for i := range nbBands {
				e.oldE[ch][i] = -28
			}
		}
	}
	e.pitch = pf
	if transient || transientDisabled {
		e.consecTransient++
	} else {
		e.consecTransient = 0
	}
	rc.done()
	if rc.failed {
		return 0, fmt.Errorf("%w (%d bytes)", errOverrun, bytes)
	}
	return bytes, nil
}

// loadInput high-passes pcm, pre-emphasises it into the pre-filter's
// history and reports whether the frame, with the previous frame's
// overlap, is digital silence.
func (e *Encoder) loadInput(pcm []int16) bool {
	peak := e.overlapMax
	var tail float32
	for ch := range e.channels {
		hp, pm := e.highPassMem[ch], e.preemphMem[ch]
		dst := e.pre[ch][combMaxPeriod:]
		for i := range FrameSize {
			x := float32(pcm[i*e.channels+ch])
			y := x - hp
			hp = highPassCoef*x + highPassFloor + (1-highPassCoef)*hp
			if i < FrameSize-overlap {
				peak = max(peak, abs32(y))
			} else {
				tail = max(tail, abs32(y))
			}
			dst[i] = y - pm
			pm = preemphasis * y
		}
		e.highPassMem[ch], e.preemphMem[ch] = hp, pm
	}
	e.overlapMax = tail
	return max(peak, tail) <= silenceLevel
}

// computeEnergies runs the long or short-block MDCTs over the filtered
// input and sets the band amplitudes, the shapes and logE.
func (e *Encoder) computeEnergies(short bool, logE *[2][nbBands]float32) {
	for ch := range e.channels {
		if short {
			for b := range shortBlocks {
				shortMDCT.forward(e.in[ch][b*shortMdct:], e.freq[ch][b:], shortBlocks, e.fftIn[:], e.fftOut[:])
			}
		} else {
			longMDCT.forward(e.in[ch][:], e.freq[ch][:], 1, e.fftIn[:], e.fftOut[:])
		}
		bandEnergies(e.freq[ch][:], e.norm[ch][:], &e.amp[ch], &logE[ch])
	}
}

// shortBlockScale brings the long-block energies in logE2 to the short
// blocks' scale.
func (e *Encoder) shortBlockScale() {
	for ch := range e.channels {
		for i := range nbBands {
			e.logE2[ch][i] += 0.5 * frameLM
		}
	}
}

// temporalVBR follows the frame's spectral envelope against its long
// term average; a frame louder than usual gets more VBR bits.
func (e *Encoder) temporalVBR(short bool) float32 {
	offset := float32(0)
	if short {
		offset = 0.5 * frameLM
	}
	follow := float32(-10)
	var avg float32
	for i := range nbBands {
		follow = max(follow-1, e.logE[0][i]-offset)
		if e.channels == 2 {
			follow = max(follow, e.logE[1][i]-offset)
		}
		avg += follow
	}
	avg /= nbBands
	t := max(-1.5, min(3, avg-e.specAvg))
	e.specAvg += 0.02 * t
	return t
}

// quantCoarse codes the coarse energies twice, intra and predicted from
// the previous frame, and keeps whichever strays less from the ideal
// codes (or costs fewer bits on a tie).
func (e *Encoder) quantCoarse(budget, bytes int) {
	rc := &e.rc
	maxDecay := min(16, 0.125*float32(bytes))
	startState := *rc
	e.intraOld = e.oldE
	intraBadness := 0
	if rc.tell()+3 <= budget {
		intraBadness = coarseEnergy(rc, e.channels, &e.logE, &e.intraOld, &e.intraErr, budget, true, maxDecay)
	} else {
		coarseEnergy(rc, e.channels, &e.logE, &e.oldE, &e.errs, budget, false, maxDecay)
		return
	}
	intraState := *rc
	intraTell := rc.tellFrac()
	saved := e.intraBuf[:intraState.offs-startState.offs]
	copy(saved, rc.buf[startState.offs:intraState.offs])
	*rc = startState
	interBadness := coarseEnergy(rc, e.channels, &e.logE, &e.oldE, &e.errs, budget, false, maxDecay)
	if intraBadness < interBadness || (intraBadness == interBadness && rc.tellFrac() > intraTell) {
		*rc = intraState
		copy(rc.buf[startState.offs:], saved)
		e.oldE = e.intraOld
		e.errs = e.intraErr
	}
}
