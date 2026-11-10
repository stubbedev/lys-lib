package opus

import (
	"math"
	"slices"

	"github.com/stubbedev/lys-lib/third_party/gomath"
)

// decodeBufferSize is the synthesis history each channel keeps, which
// the post-filter reaches back into.
const decodeBufferSize = 2048

// celtDecoder is celt_decode_with_ec's state: the synthesis history,
// the band energies of the last frames, the post-filter and the noise
// seed, for channels output channels.
type celtDecoder struct {
	channels  int
	mem       [2][decodeBufferSize + overlap]float32
	oldE      [2][nbBands]float32
	oldLogE   [2][nbBands]float32
	oldLogE2  [2][nbBands]float32
	preemph   [2]float32
	pf, pfOld pitchFilter
	rng       uint32

	rc    rangeDecoder
	bands bandDecoder
	x     [2][FrameSize]float32
	freq  [2][FrameSize]float32
	fftIn [longN4]complex64
	fftO  [longN4]complex64
	masks [2 * nbBands]uint
}

func newCELTDecoder(channels int) *celtDecoder {
	d := &celtDecoder{channels: channels}
	d.reset()
	return d
}

// reset is OPUS_RESET_STATE: silence before the next frame.
func (d *celtDecoder) reset() {
	*d = celtDecoder{channels: d.channels}
	for c := range 2 {
		for i := range nbBands {
			d.oldLogE[c][i], d.oldLogE2[c][i] = -28, -28
		}
	}
	d.bands.disableInv = d.channels == 1
}

// decode decodes one CELT frame of 2.5 ms << lm in streamChannels
// channels coding bands [0, end) and appends its 48 kHz samples, as
// float in int16 scale, interleaved over the output channels to out.
func (d *celtDecoder) decode(frame []byte, lm, end, streamChannels int, out []float32) []float32 {
	c := streamChannels
	cc := d.channels
	m := 1 << lm
	n := shortMdct << lm
	rc := &d.rc
	rc.reset(frame)
	if c == 1 {
		for i := range nbBands {
			d.oldE[0][i] = max(d.oldE[0][i], d.oldE[1][i])
		}
	}
	totalBits := len(frame) * 8
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
	if tell+16 <= totalBits {
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
	decodeCoarseEnergy(rc, c, lm, end, &d.oldE, intra)
	var tfRes [nbBands]int
	decodeTF(rc, transient, lm, end, &tfRes)
	spread := spreadNormal
	if rc.tell()+4 <= totalBits {
		spread = rc.icdf(spreadICDF, 5)
	}
	caps := bandCaps(c, lm)
	var offsets [nbBands]int
	dynallocLogp := uint(6)
	totalBits8 := totalBits << bitRes
	tellFrac := rc.tellFrac()
	for i := range end {
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
	bits := len(frame)*8<<bitRes - rc.tellFrac() - 1
	antiCollapseRsv := 0
	if transient && lm >= 2 && bits >= (lm+2)<<bitRes {
		antiCollapseRsv = 1 << bitRes
	}
	bits -= antiCollapseRsv
	a := allocate(allocDecoder{rc: rc}, c, lm, end, &offsets, &caps, trim, bits)
	decodeFineEnergy(rc, c, end, &d.oldE, &a.fine)
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
	q.decodeAll(d.x[0][:n], y, &d.masks, &a, end, blocks, &tfRes, len(frame)*8<<bitRes-antiCollapseRsv)
	d.rng = q.seed
	antiCollapseOn := antiCollapseRsv > 0 && rc.bits(1) == 1
	decodeFinalEnergy(rc, c, end, &d.oldE, &a.fine, &a.priority, len(frame)*8-rc.tell())
	if antiCollapseOn {
		d.antiCollapse(c, lm, end, &a)
	}
	if silence {
		for ch := range 2 {
			for i := range nbBands {
				d.oldE[ch][i] = -28
			}
		}
	}
	d.synthesise(c, lm, end, transient, silence)
	for ch := range cc {
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
	for ch := range 2 {
		for i := end; i < nbBands; i++ {
			d.oldE[ch][i] = 0
			d.oldLogE[ch][i], d.oldLogE2[ch][i] = -28, -28
		}
	}
	d.rng = rc.rng
	return d.deemphasis(n, out)
}

// synthesise scales the decoded shapes by their band energies into
// MDCT coefficients and inverts them into each output channel's
// history, duplicating a mono stream or downmixing a stereo one when
// the stream's channels are not the output's.
func (d *celtDecoder) synthesise(c, lm, end int, transient, silence bool) {
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
		denormalise(d.x[0][:n], d.freq[0][:n], &d.oldE[0], lm, end, silence)
		copy(d.freq[1][:n], d.freq[0][:n])
		inverse(d.freq[0][:n], 0)
		inverse(d.freq[1][:n], 1)
	case d.channels == 1 && c == 2:
		denormalise(d.x[0][:n], d.freq[0][:n], &d.oldE[0], lm, end, silence)
		denormalise(d.x[1][:n], d.freq[1][:n], &d.oldE[1], lm, end, silence)
		for i := range n {
			d.freq[0][i] = float32(0.5*d.freq[0][i]) + float32(0.5*d.freq[1][i])
		}
		inverse(d.freq[0][:n], 0)
	default:
		for ch := range c {
			denormalise(d.x[ch][:n], d.freq[ch][:n], &d.oldE[ch], lm, end, silence)
			inverse(d.freq[ch][:n], ch)
		}
	}
}

// denormalise is denormalise_bands: each band's shape in x scaled by
// its energy into freq, zero above end.
func denormalise(x, freq []float32, logE *[nbBands]float32, lm, end int, silence bool) {
	m := 1 << lm
	bound := m * eBands[end]
	if silence {
		bound, end = 0, 0
	}
	for i := range end {
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
func (d *celtDecoder) antiCollapse(c, lm, end int, a *allocation) {
	seed := d.rng
	for i := range end {
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
// samples per channel and appends them interleaved to out.
func (d *celtDecoder) deemphasis(n int, out []float32) []float32 {
	at := len(out)
	cc := d.channels
	out = slices.Grow(out, n*cc)[:at+n*cc]
	dst := out[at:]
	for ch := range cc {
		syn := d.mem[ch][decodeBufferSize-n : decodeBufferSize]
		mem := d.preemph[ch]
		for j, v := range syn {
			tmp := v + 1e-30 + mem
			mem = preemphasis * tmp
			dst[j*cc+ch] = tmp
		}
		d.preemph[ch] = mem
	}
	return out
}
