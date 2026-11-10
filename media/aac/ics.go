package aac

import (
	"fmt"
	"math"

	"github.com/stubbedev/lys-lib/intconv"
	"github.com/stubbedev/lys-lib/third_party/gomath"
)

// windowSequence is a frame's window sequence (table 4.85).
type windowSequence uint8

const (
	onlyLong windowSequence = iota
	longStart
	eightShort
	longStop
)

// Band types (codebooks) with a meaning beyond Huffman coding.
const (
	zeroHCB       = 0
	escHCB        = 11
	reservedHCB   = 12
	noiseHCB      = 13
	intensityHCB2 = 14
	intensityHCB  = 15
)

const (
	maxWindows   = 8
	maxSFBLong   = 51
	maxBands     = maxWindows * 16
	maxTNSLong   = 12
	maxTNSShort  = 7
	maxQuant     = 8191
	pulseMaxAmp  = 15
	noiseOffset  = 90
	globalOffset = 100
	sfDiffZero   = 60
	pnsSeed      = 0x1f2e3d4c
)

// bandLayout is the scale factor bands of one window length at one
// sampling rate.
type bandLayout struct {
	offsets  []uint16
	tnsBands int
}

// numBands is the layout's scale factor band count.
func (b *bandLayout) numBands() int { return len(b.offsets) - 1 }

var (
	longLayouts = func() (l [13]bandLayout) {
		offsets := [13][]uint16{
			swbOffsetLong96[:], swbOffsetLong96[:], swbOffsetLong64[:], swbOffsetLong48[:], swbOffsetLong48[:],
			swbOffsetLong32[:], swbOffsetLong24[:], swbOffsetLong24[:], swbOffsetLong16[:], swbOffsetLong16[:],
			swbOffsetLong16[:], swbOffsetLong8[:], swbOffsetLong8[:],
		}
		for i := range l {
			l[i] = bandLayout{offsets: offsets[i], tnsBands: int(tnsMaxBandsLong[i])}
		}
		return l
	}()
	shortLayouts = func() (l [13]bandLayout) {
		offsets := [13][]uint16{
			swbOffsetShort96[:], swbOffsetShort96[:], swbOffsetShort96[:], swbOffsetShort48[:], swbOffsetShort48[:],
			swbOffsetShort48[:], swbOffsetShort24[:], swbOffsetShort24[:], swbOffsetShort16[:], swbOffsetShort16[:],
			swbOffsetShort16[:], swbOffsetShort8[:], swbOffsetShort8[:],
		}
		for i := range l {
			l[i] = bandLayout{offsets: offsets[i], tnsBands: int(tnsMaxBandsShort[i])}
		}
		return l
	}()
	tnsCoefs = [4][]float32{tnsCoef03[:], tnsCoef04[:], tnsCoef13[:], tnsCoef14[:]}
)

// The codebooks: the scale factor differences and the 11 spectral
// codebooks, with each spectral one's dimension, whether its values
// are unsigned (signs follow) and its largest absolute value.
var (
	sfHuffman       = newHuffman(sfCodes[:], sfBits[:], 9)
	spectralHuffman = [12]*huffman{
		1: newHuffman(spectralCodes1[:], spectralBits1[:], 8), 2: newHuffman(spectralCodes2[:], spectralBits2[:], 8),
		3: newHuffman(spectralCodes3[:], spectralBits3[:], 8), 4: newHuffman(spectralCodes4[:], spectralBits4[:], 8),
		5: newHuffman(spectralCodes5[:], spectralBits5[:], 8), 6: newHuffman(spectralCodes6[:], spectralBits6[:], 8),
		7: newHuffman(spectralCodes7[:], spectralBits7[:], 8), 8: newHuffman(spectralCodes8[:], spectralBits8[:], 8),
		9: newHuffman(spectralCodes9[:], spectralBits9[:], 8), 10: newHuffman(spectralCodes10[:], spectralBits10[:], 8),
		11: newHuffman(spectralCodes11[:], spectralBits11[:], 8),
	}
	codebookDim      = [12]int{1: 4, 2: 4, 3: 4, 4: 4, 5: 2, 6: 2, 7: 2, 8: 2, 9: 2, 10: 2, 11: 2}
	codebookUnsigned = [12]bool{3: true, 4: true, 7: true, 8: true, 9: true, 10: true, 11: true}
	codebookMod      = [12]int32{1: 3, 2: 3, 3: 3, 4: 3, 5: 9, 6: 9, 7: 8, 8: 8, 9: 13, 10: 13, 11: 17}
)

// pow43 is |q|^(4/3) for every quantised magnitude a frame can carry.
var pow43 = func() (t [maxQuant + pulseMaxAmp + 1]float32) {
	for i := range t {
		f := float64(i)
		t[i] = float32(f * gomath.Cbrt(f))
	}
	return t
}()

// sfGain is 2^((sf-100)/4) for every scale factor.
var sfGain = func() (t [256]float32) {
	for i := range t {
		t[i] = float32(gomath.Exp2(0.25 * float64(i-globalOffset)))
	}
	return t
}()

// icsInfo is ics_info (4.4.2.6): a frame's windows and grouping.
type icsInfo struct {
	seq      windowSequence
	shape    uint8
	maxSFB   int
	groups   int
	groupLen [maxWindows]int
	layout   *bandLayout
}

// windows is the number of transform windows in the frame.
func (i *icsInfo) windows() int {
	if i.seq == eightShort {
		return maxWindows
	}
	return 1
}

// tnsFilter is one TNS filter: the bands it spans, its direction and
// its LPC coefficients.
type tnsFilter struct {
	length   int
	order    int
	backward bool
	lpc      [maxTNSLong]float32
}

// tnsData is tns_data (4.4.2.10) per window.
type tnsData struct {
	present bool
	nFilt   [maxWindows]int
	filters [maxWindows][3]tnsFilter
}

// msMask is a channel pair's mid/side decision per band.
type msMask struct {
	present int
	used    [maxBands]bool
}

// channelStream is one individual_channel_stream (4.4.2.7) being
// decoded: its side information, quantised values and spectrum.
type channelStream struct {
	info       icsInfo
	globalGain int
	bandType   [maxBands]uint8
	sf         [maxBands]int
	q          [FrameSize]int32
	tns        tnsData
	coef       *[FrameSize]float32
}

// readInfo reads ics_info.
func (d *Decoder) readInfo(r *bitReader, info *icsInfo) error {
	if r.bit() {
		return fmt.Errorf("ics_info reserved bit set: %w", ErrInvalid)
	}
	info.seq = windowSequence(r.read(2) & 3)
	info.shape = uint8(r.read(1) & 1)
	info.groups = 1
	info.groupLen = [maxWindows]int{1}
	if info.seq == eightShort {
		info.layout = d.short
		info.maxSFB = r.readInt(4)
		grouping := r.read(7)
		for i := 6; i >= 0; i-- {
			if grouping>>uint(i)&1 != 0 {
				info.groupLen[info.groups-1]++
			} else {
				info.groups++
				info.groupLen[info.groups-1] = 1
			}
		}
	} else {
		info.layout = d.long
		info.maxSFB = r.readInt(6)
		if r.bit() {
			return fmt.Errorf("predictor data in an AAC-LC frame: %w", ErrInvalid)
		}
	}
	if info.maxSFB > info.layout.numBands() {
		return fmt.Errorf("max_sfb %d of %d bands: %w", info.maxSFB, info.layout.numBands(), ErrInvalid)
	}
	return nil
}

// readStream reads an individual_channel_stream's side information
// and quantised spectrum into cs.
func (d *Decoder) readStream(r *bitReader, cs *channelStream, common bool) error {
	cs.globalGain = r.readInt(8)
	if !common {
		if err := d.readInfo(r, &cs.info); err != nil {
			return err
		}
	}
	if err := cs.readSections(r); err != nil {
		return err
	}
	if err := cs.readScaleFactors(r); err != nil {
		return err
	}
	clear(cs.q[:])
	var pulses pulseData
	if r.bit() {
		if cs.info.seq == eightShort {
			return fmt.Errorf("pulse data in a short-window frame: %w", ErrInvalid)
		}
		if err := pulses.read(r, cs.info.layout); err != nil {
			return err
		}
	}
	cs.tns.present = r.bit()
	if cs.tns.present {
		if err := cs.readTNS(r); err != nil {
			return err
		}
	}
	if r.bit() {
		return fmt.Errorf("gain control data (AAC SSR): %w", ErrUnsupported)
	}
	if err := cs.readSpectrum(r); err != nil {
		return err
	}
	pulses.apply(&cs.q)
	if r.overrun {
		return fmt.Errorf("channel stream past the end: %w", ErrInvalid)
	}
	return nil
}

// readSections reads section_data: each band's codebook.
func (cs *channelStream) readSections(r *bitReader) error {
	info := &cs.info
	bits := uint(5)
	if info.seq == eightShort {
		bits = 3
	}
	esc := 1<<bits - 1
	for g := range info.groups {
		for k := 0; k < info.maxSFB; {
			cb := uint8(r.read(4) & 15)
			if cb == reservedHCB {
				return fmt.Errorf("reserved codebook 12: %w", ErrInvalid)
			}
			end := k
			for {
				inc := r.readInt(bits)
				end += inc
				if r.overrun || end > info.maxSFB {
					return fmt.Errorf("section past max_sfb %d: %w", info.maxSFB, ErrInvalid)
				}
				if inc != esc {
					break
				}
			}
			for ; k < end; k++ {
				cs.bandType[g*info.maxSFB+k] = cb
			}
		}
	}
	return nil
}

// readScaleFactors reads scale_factor_data: each band's scale factor,
// intensity position or noise energy.
func (cs *channelStream) readScaleFactors(r *bitReader) error {
	info := &cs.info
	global, noise, is := cs.globalGain, cs.globalGain-noiseOffset, 0
	firstNoise := true
	for g := range info.groups {
		for sfb := range info.maxSFB {
			i := g*info.maxSFB + sfb
			switch cs.bandType[i] {
			case zeroHCB:
				cs.sf[i] = 0
			case intensityHCB, intensityHCB2:
				is += int(sfHuffman.decode(r)) - sfDiffZero
				cs.sf[i] = is
			case noiseHCB:
				if firstNoise {
					noise += r.readInt(9) - 256
					firstNoise = false
				} else {
					noise += int(sfHuffman.decode(r)) - sfDiffZero
				}
				cs.sf[i] = noise
			default:
				global += int(sfHuffman.decode(r)) - sfDiffZero
				if global < 0 || global > 255 {
					return fmt.Errorf("scale factor %d: %w", global, ErrInvalid)
				}
				cs.sf[i] = global
			}
		}
	}
	return nil
}

// pulseData is pulse_data (4.4.2.9): magnitudes added to a long
// frame's quantised values.
type pulseData struct {
	n   int
	pos [4]int
	amp [4]int32
}

func (p *pulseData) read(r *bitReader, layout *bandLayout) error {
	p.n = r.readInt(2) + 1
	start := r.readInt(6)
	if start >= layout.numBands() {
		return fmt.Errorf("pulse start band %d: %w", start, ErrInvalid)
	}
	pos := int(layout.offsets[start])
	for i := range p.n {
		pos += r.readInt(5)
		if pos >= FrameSize {
			return fmt.Errorf("pulse at %d: %w", pos, ErrInvalid)
		}
		p.pos[i] = pos
		p.amp[i] = int32(r.read(4) & 15)
	}
	return nil
}

func (p *pulseData) apply(q *[FrameSize]int32) {
	for i := range p.n {
		if q[p.pos[i]] > 0 {
			q[p.pos[i]] += p.amp[i]
		} else {
			q[p.pos[i]] -= p.amp[i]
		}
	}
}

// readTNS reads tns_data.
func (cs *channelStream) readTNS(r *bitReader) error {
	short := cs.info.seq == eightShort
	nBits, lenBits, orderBits, maxOrder := uint(2), uint(6), uint(5), maxTNSLong
	if short {
		nBits, lenBits, orderBits, maxOrder = 1, 4, 3, maxTNSShort
	}
	t := &cs.tns
	for w := range cs.info.windows() {
		t.nFilt[w] = r.readInt(nBits)
		if t.nFilt[w] == 0 {
			continue
		}
		res := r.readInt(1)
		for f := range t.nFilt[w] {
			flt := &t.filters[w][f]
			flt.length = r.readInt(lenBits)
			flt.order = r.readInt(orderBits)
			if flt.order > maxOrder {
				return fmt.Errorf("TNS order %d: %w", flt.order, ErrInvalid)
			}
			if flt.order == 0 {
				continue
			}
			flt.backward = r.bit()
			compress := r.readInt(1)
			table := tnsCoefs[2*compress+res]
			var parcor [maxTNSLong]float32
			for i := range flt.order {
				parcor[i] = table[r.read(uint(res+3-compress))]
			}
			flt.lpc = parcorToLPC(&parcor, flt.order)
		}
	}
	return nil
}

// parcorToLPC turns TNS reflection coefficients into direct-form LPC
// coefficients (4.6.9.3).
func parcorToLPC(parcor *[maxTNSLong]float32, order int) (lpc [maxTNSLong]float32) {
	for i := range order {
		r := parcor[i]
		lpc[i] = r
		for j := range (i + 1) >> 1 {
			a, b := lpc[j], lpc[i-1-j]
			lpc[j] = a + float32(r*b)
			lpc[i-1-j] = b + float32(r*a)
		}
	}
	return lpc
}

// readSpectrum reads spectral_data into the quantised values q, laid
// out window by window.
func (cs *channelStream) readSpectrum(r *bitReader) error {
	info := &cs.info
	offsets := info.layout.offsets
	win := 0
	for g := range info.groups {
		for sfb := range info.maxSFB {
			cb := cs.bandType[g*info.maxSFB+sfb]
			if cb == zeroHCB || cb >= noiseHCB {
				continue
			}
			h := spectralHuffman[cb]
			dim, mod, unsigned := codebookDim[cb], codebookMod[cb], codebookUnsigned[cb]
			lo, hi := int(offsets[sfb]), int(offsets[sfb+1])
			for w := win; w < win+info.groupLen[g]; w++ {
				band := cs.q[w*128+lo : w*128+hi]
				if info.seq != eightShort {
					band = cs.q[lo:hi]
				}
				for k := 0; k+dim <= len(band); k += dim {
					s := h.decode(r)
					if s < 0 {
						return fmt.Errorf("no codeword in codebook %d: %w", cb, ErrInvalid)
					}
					v := band[k : k+dim]
					for j := dim - 1; j >= 0; j-- {
						v[j] = s % mod
						s /= mod
					}
					if !unsigned {
						off := (mod - 1) / 2
						for j := range v {
							v[j] -= off
						}
						continue
					}
					for j := range v {
						if v[j] != 0 && r.bit() {
							v[j] = -v[j]
						}
					}
					if cb == escHCB {
						for j := range v {
							if v[j] == 16 || v[j] == -16 {
								e, err := readEscape(r)
								if err != nil {
									return err
								}
								if v[j] < 0 {
									e = -e
								}
								v[j] = e
							}
						}
					}
				}
			}
		}
		win += info.groupLen[g]
	}
	return nil
}

// readEscape reads an escape sequence's magnitude (4.6.3.3).
func readEscape(r *bitReader) (int32, error) {
	n := uint(4)
	for r.bit() {
		n++
		if n > 12 {
			return 0, fmt.Errorf("escape sequence of more than 8 prefix bits: %w", ErrInvalid)
		}
	}
	return 1<<n + int32(r.read(n)&0xfff), nil
}

// dequantise turns the quantised values into the spectrum: each
// band's |q|^(4/3) scaled by its scale factor, noise for PNS bands. A
// right channel of a common window passes its left channel and mask so
// noise bands coded mid/side reuse the left channel's noise.
func (cs *channelStream) dequantise(seed *uint32, left *channelStream, ms *msMask) {
	info := &cs.info
	offsets := info.layout.offsets
	coef := cs.coef
	clear(coef[:])
	win := 0
	for g := range info.groups {
		for sfb := range info.maxSFB {
			i := g*info.maxSFB + sfb
			lo, hi := int(offsets[sfb]), int(offsets[sfb+1])
			cb := cs.bandType[i]
			for w := win; w < win+info.groupLen[g]; w++ {
				band := coef[w*128+lo : w*128+hi]
				q := cs.q[w*128+lo : w*128+hi]
				if info.seq != eightShort {
					band, q = coef[lo:hi], cs.q[lo:hi]
				}
				switch {
				case cb == noiseHCB:
					if left != nil && ms.present != 0 && (ms.present == 2 || ms.used[i]) && left.bandType[i] == noiseHCB {
						ratio := float32(gomath.Exp2(0.25 * float64(cs.sf[i]-left.sf[i])))
						src := left.coef[w*128+lo : w*128+hi]
						if info.seq != eightShort {
							src = left.coef[lo:hi]
						}
						for k, v := range src {
							band[k] = v * ratio
						}
						continue
					}
					noiseBand(band, seed, cs.sf[i])
				case cb != zeroHCB && cb < noiseHCB:
					gain := sfGain[cs.sf[i]]
					for k, v := range q {
						m := v
						if m < 0 {
							m = -m
						}
						x := pow43[m] * gain
						if v < 0 {
							x = -x
						}
						band[k] = x
					}
				}
			}
		}
		win += info.groupLen[g]
	}
}

// noiseBand fills band with uniform noise at energy 2^(sf/2)
// (4.6.13.3).
func noiseBand(band []float32, seed *uint32, sf int) {
	s := *seed
	var energy float32
	for k := range band {
		s = s*1664525 + 1013904223
		v := float32(intconv.Int32Bits(s))
		band[k] = v
		energy += float32(v * v)
	}
	*seed = s
	scale := float32(gomath.Exp2(0.25*float64(sf))) / float32(math.Sqrt(float64(energy)))
	for k := range band {
		band[k] *= scale
	}
}

// midSide undoes mid/side stereo in the bands that use it.
func midSide(l, r *channelStream, ms *msMask) {
	if ms.present == 0 {
		return
	}
	info := &l.info
	offsets := info.layout.offsets
	win := 0
	for g := range info.groups {
		for sfb := range info.maxSFB {
			i := g*info.maxSFB + sfb
			if ms.present == 1 && !ms.used[i] || l.bandType[i] >= noiseHCB || r.bandType[i] >= noiseHCB {
				continue
			}
			lo, hi := int(offsets[sfb]), int(offsets[sfb+1])
			for w := win; w < win+info.groupLen[g]; w++ {
				a, b := l.coef[w*128+lo:w*128+hi], r.coef[w*128+lo:w*128+hi]
				if info.seq != eightShort {
					a, b = l.coef[lo:hi], r.coef[lo:hi]
				}
				for k := range a {
					m, s := a[k], b[k]
					a[k], b[k] = m+s, m-s
				}
			}
		}
		win += info.groupLen[g]
	}
}

// intensity derives the right channel's intensity bands from the left
// channel's.
func intensity(l, r *channelStream, ms *msMask) {
	info := &r.info
	offsets := info.layout.offsets
	win := 0
	for g := range info.groups {
		for sfb := range info.maxSFB {
			i := g*info.maxSFB + sfb
			cb := r.bandType[i]
			if cb != intensityHCB && cb != intensityHCB2 {
				continue
			}
			scale := float32(gomath.Exp2(-0.25 * float64(r.sf[i])))
			if cb == intensityHCB2 {
				scale = -scale
			}
			if ms.present == 2 || ms.present == 1 && ms.used[i] {
				scale = -scale
			}
			lo, hi := int(offsets[sfb]), int(offsets[sfb+1])
			for w := win; w < win+info.groupLen[g]; w++ {
				a, b := l.coef[w*128+lo:w*128+hi], r.coef[w*128+lo:w*128+hi]
				if info.seq != eightShort {
					a, b = l.coef[lo:hi], r.coef[lo:hi]
				}
				for k, v := range a {
					b[k] = v * scale
				}
			}
		}
		win += info.groupLen[g]
	}
}

// applyTNS runs each TNS filter over its bands of the spectrum
// (4.6.9.3).
func (cs *channelStream) applyTNS(long, short *bandLayout) {
	if !cs.tns.present {
		return
	}
	info := &cs.info
	layout := long
	size := FrameSize
	if info.seq == eightShort {
		layout, size = short, 128
	}
	bands := min(layout.tnsBands, info.maxSFB)
	for w := range info.windows() {
		spec := cs.coef[w*size : (w+1)*size]
		bottom := layout.numBands()
		for f := range cs.tns.nFilt[w] {
			flt := &cs.tns.filters[w][f]
			top := bottom
			bottom = max(top-flt.length, 0)
			if flt.order == 0 {
				continue
			}
			start := int(layout.offsets[min(bottom, bands)])
			end := int(layout.offsets[min(top, bands)])
			n := end - start
			if n <= 0 {
				continue
			}
			at, inc := start, 1
			if flt.backward {
				at, inc = end-1, -1
			}
			for m := range n {
				acc := spec[at]
				for i := 1; i <= min(m, flt.order); i++ {
					acc -= float32(spec[at-i*inc] * flt.lpc[i-1])
				}
				spec[at] = acc
				at += inc
			}
		}
	}
}
