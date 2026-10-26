// Package opus is a pure-Go Opus encoder (RFC 6716) for lys-ui's
// recordings: CELT-only fullband 20 ms frames (configuration 31) at a
// constant bitrate, from the media package's PCM layout (48 kHz,
// interleaved signed 16-bit samples, one or two channels).
//
// It codes long blocks only, with no pitch pre-filter; every other
// CELT tool (inter/intra energy prediction, the full bit allocation,
// spreading, intensity and dual stereo) is the reference encoder's.
//
//	enc, err := opus.NewEncoder(2, 128000)
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

// tocConfig is the TOC configuration CELT-only fullband 20 ms frames
// carry (RFC 6716 section 3.1).
const tocConfig = 31

// preemphasis is the coefficient of CELT's first-order pre-emphasis.
const preemphasis = float32(0.85000610)

// Encoder turns PCM frames into Opus packets. It keeps the signal's
// history between frames, so one Encoder codes one stream in order.
type Encoder struct {
	channels    int
	packetBytes int
	caps        [nbBands]int

	inMem          [2][overlap]float32
	preemphMem     [2]float32
	oldE           [2][nbBands]float32
	tonalAverage   int
	lastCodedBands int

	rc       rangeEncoder
	pvq      pvq
	in       [2][FrameSize + overlap]float32
	freq     [2][FrameSize]float32
	norm     [2][FrameSize]float32
	amp      [2][nbBands]float32
	logE     [2][nbBands]float32
	errs     [2][nbBands]float32
	fftIn    [mdctN4]complex64
	fftOut   [mdctN4]complex64
	intraOld [2][nbBands]float32
	intraErr [2][nbBands]float32
	intraBuf []byte
}

// NewEncoder returns an encoder for channels (1 or 2) at bitrate bits
// per second, rounded down to whole bytes per packet.
func NewEncoder(channels, bitrate int) (*Encoder, error) {
	if channels != 1 && channels != 2 {
		return nil, fmt.Errorf("opus: %d channels: only 1 or 2 are supported", channels)
	}
	if bitrate < MinBitrate || bitrate > MaxBitrate {
		return nil, fmt.Errorf("opus: bitrate %d b/s outside [%d, %d]", bitrate, MinBitrate, MaxBitrate)
	}
	packetBytes := bitrate * FrameSize / (8 * SampleRate)
	return &Encoder{
		channels:     channels,
		packetBytes:  packetBytes,
		caps:         bandCaps(channels),
		tonalAverage: 256,
		intraBuf:     make([]byte, packetBytes),
	}, nil
}

// Channels is the number of interleaved channels Encode expects.
func (e *Encoder) Channels() int { return e.channels }

// PacketBytes is the size of every packet Encode appends.
func (e *Encoder) PacketBytes() int { return e.packetBytes }

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
	dst = slices.Grow(dst, e.packetBytes)[:start+e.packetBytes]
	toc := byte(tocConfig << 3)
	if e.channels == 2 {
		toc |= 1 << 2
	}
	dst[start] = toc
	if err := e.encodeFrame(dst[start+1:], pcm); err != nil {
		return dst[:start], err
	}
	return dst, nil
}

// errOverrun reports a frame whose symbols did not fit its bytes, which
// the allocation's budgeting rules out.
var errOverrun = errors.New("opus: range coder overran the packet")

// encodeFrame is celt_encode_with_ec for one CBR frame filling out.
func (e *Encoder) encodeFrame(out []byte, pcm []int16) error {
	c := e.channels
	bytes := len(out)
	totalBits := bytes * 8
	rc := &e.rc
	rc.reset(out)

	silence := e.preemphasise(pcm)
	tell := 1
	rc.bitLogp(silence, 15)
	if silence {
		tell = totalBits
		rc.nbitsTotal += tell - rc.tell()
	}
	if tell+16 <= totalBits {
		rc.bitLogp(false, 1)
	}
	if rc.tell()+3 <= totalBits {
		rc.bitLogp(false, 3)
	}

	for ch := range c {
		mdctConsts.mdct(e.in[ch][:], e.freq[ch][:], &e.fftIn, &e.fftOut)
		bandEnergies(e.freq[ch][:], e.norm[ch][:], &e.amp[ch], &e.logE[ch])
	}

	e.quantCoarse(totalBits, bytes)
	e.encodeTF(totalBits)

	spread := spreadNormal
	if rc.tell()+4 <= totalBits {
		if bytes >= 10*c {
			spread = e.spreadingDecision()
		}
		rc.icdf(spread, spreadICDF, 5)
	}

	offsets := e.dynalloc(bytes)
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

	trim := 5
	if tellFrac+(6<<bitRes) <= totalBits8-totalBoost {
		trim = e.allocTrim()
		rc.icdf(trim, trimICDF, 7)
	}

	intensity, dual := 0, false
	if c == 2 {
		dual = e.stereoAnalysis()
		rate := 2 * ((8*bytes - 80) >> frameLM) / 5
		intensity = nbBands
		for _, t := range [...]struct{ below, bands int }{{35, 8}, {50, 12}, {68, 16}, {84, 18}, {102, 19}, {130, 20}} {
			if rate < t.below {
				intensity = t.bands
				break
			}
		}
	}

	bits := totalBits8 - rc.tellFrac() - 1
	a := allocate(rc, c, &offsets, &e.caps, trim, intensity, dual, bits, e.lastCodedBands)
	e.lastCodedBands = a.codedBands

	fineEnergy(rc, c, &e.oldE, &e.errs, &a.fine)

	bc := bandCoder{rc: rc, pvq: &e.pvq, amp: &e.amp, spread: spread, intensity: a.intensity}
	var y []float32
	if c == 2 {
		y = e.norm[1][:]
	}
	bc.quantAll(e.norm[0][:], y, &a, totalBits8)

	finaliseEnergy(rc, c, &e.oldE, &e.errs, &a.fine, &a.priority, totalBits-rc.tell())

	if silence {
		for ch := range c {
			for i := range nbBands {
				e.oldE[ch][i] = -28
			}
		}
	}
	rc.done()
	if rc.failed {
		return fmt.Errorf("%w (%d bytes)", errOverrun, bytes)
	}
	return nil
}

// preemphasise filters pcm into the MDCT input behind the previous
// frame's overlap and reports whether the frame is digital silence.
func (e *Encoder) preemphasise(pcm []int16) bool {
	silence := true
	for ch := range e.channels {
		in := &e.in[ch]
		copy(in[:overlap], e.inMem[ch][:])
		mem := e.preemphMem[ch]
		for i := range FrameSize {
			x := float32(pcm[i*e.channels+ch])
			v := x + mem
			in[overlap+i] = v
			mem = -preemphasis * x
			silence = silence && v == 0
		}
		e.preemphMem[ch] = mem
		copy(e.inMem[ch][:], in[FrameSize:])
	}
	return silence
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

// encodeTF codes every band's time-frequency change as none; long
// blocks then keep their full frequency resolution.
func (e *Encoder) encodeTF(totalBits int) {
	rc := &e.rc
	budget := totalBits
	tell := rc.tell()
	logp := 4
	if tell+logp+1 <= budget {
		budget--
	}
	for range nbBands {
		if tell+logp <= budget {
			rc.bitLogp(false, uint(logp))
			tell = rc.tell()
		}
		logp = 5
	}
}

// spreadingDecision picks the rotation strength from how peaky the
// band shapes are, smoothed over frames (spreading_decision).
func (e *Encoder) spreadingDecision() int {
	sum, nbands := 0, 0
	for ch := range e.channels {
		for i := range nbBands {
			x := e.norm[ch][bandStart(i):bandStart(i+1)]
			n := len(x)
			if n <= 8 {
				continue
			}
			var tcount [3]int
			for _, v := range x {
				x2n := v * v * float32(n)
				if x2n < 0.25 {
					tcount[0]++
				}
				if x2n < 0.0625 {
					tcount[1]++
				}
				if x2n < 0.015625 {
					tcount[2]++
				}
			}
			sum += 256 * (b2i(2*tcount[2] >= n) + b2i(2*tcount[1] >= n) + b2i(2*tcount[0] >= n))
			nbands++
		}
	}
	sum /= nbands
	sum = (sum + e.tonalAverage) >> 1
	e.tonalAverage = sum
	sum = (3*sum + ((3-spreadNormal)<<7 + 64) + 2) >> 2
	switch {
	case sum < 80:
		return spreadAggressive
	case sum < 256:
		return spreadNormal
	case sum < 384:
		return spreadLight
	}
	return spreadNone
}

// dynalloc asks for extra bits in bands that stand out above their
// neighbours.
func (e *Encoder) dynalloc(bytes int) [nbBands]int {
	var offsets [nbBands]int
	if bytes <= 50 {
		return offsets
	}
	const t1, t2 = 2, 4
	for i := 1; i < nbBands-1; i++ {
		l := e.logE[0][:]
		d2 := 2*l[i] - l[i-1] - l[i+1]
		if e.channels == 2 {
			r := e.logE[1][:]
			d2 = 0.5 * (d2 + 2*r[i] - r[i-1] - r[i+1])
		}
		if d2 > t1 {
			offsets[i]++
		}
		if d2 > t2 {
			offsets[i]++
		}
	}
	return offsets
}

// allocTrim tilts the allocation towards low or high bands from the
// spectral tilt and, in stereo, the channels' correlation.
func (e *Encoder) allocTrim() int {
	trim := 5
	if e.channels == 2 {
		var sum float32
		for i := range 8 {
			var partial float32
			for j := bandStart(i); j < bandStart(i+1); j++ {
				partial += e.norm[0][j] * e.norm[1][j]
			}
			sum += partial
		}
		sum *= 1.0 / 8
		switch {
		case sum > 0.995:
			trim -= 4
		case sum > 0.92:
			trim -= 3
		case sum > 0.85:
			trim -= 2
		case sum > 0.8:
			trim--
		}
	}
	var diff float32
	for ch := range e.channels {
		for i := range nbBands - 1 {
			diff += e.logE[ch][i] * float32(2+2*i-nbBands)
		}
	}
	diff /= float32(2 * e.channels * (nbBands - 1))
	if diff > 2 {
		trim--
	}
	if diff > 8 {
		trim--
	}
	if diff < -4 {
		trim++
	}
	if diff < -10 {
		trim++
	}
	return max(0, min(10, trim))
}

// stereoAnalysis reports whether coding left and right apart (dual
// stereo) is cheaper than mid and side, by their L1 norms.
func (e *Encoder) stereoAnalysis() bool {
	sumLR, sumMS := float32(1e-15), float32(1e-15)
	for j := range bandStart(13) {
		l, r := e.norm[0][j], e.norm[1][j]
		sumLR += abs32(l) + abs32(r)
		sumMS += abs32(l+r) + abs32(l-r)
	}
	sumMS *= 0.707107
	const thetas = 13
	width := float32(eBands[13] << (frameLM + 1))
	return (width+thetas)*sumMS > width*sumLR
}

func abs32(v float32) float32 {
	if v < 0 {
		return -v
	}
	return v
}
