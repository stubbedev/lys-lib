// Package vorbis is a pure-Go Ogg Vorbis decoder (Vorbis I) for the
// sound files freedesktop sound themes ship (.oga): it decodes a whole
// stream once into interleaved signed 16-bit PCM, the layout a sample
// cache upload or a playback stream takes.
//
// It covers the whole Vorbis I specification: floor types 0 and 1,
// residue types 0, 1 and 2, codebook lookup types 1 and 2, channel
// coupling, both block sizes and any channel count, and it checks every
// Ogg page's CRC. Output matches libvorbis's to within one least
// significant bit.
//
//	data, err := os.ReadFile("message-new-instant.oga")
//	sound, err := vorbis.Decode(data)
//	// sound.Samples holds sound.Frames()*sound.Channels samples
package vorbis

import (
	"errors"
	"fmt"
	"math"
	"slices"

	"github.com/stubbedev/lys-lib/third_party/gomath"
)

// Decode fails with one of these, wrapped with what was being read.
var (
	// ErrNotVorbis is data that is not an Ogg stream carrying Vorbis.
	ErrNotVorbis = errors.New("not an Ogg Vorbis stream")
	// ErrCorrupt is a stream whose framing, checksums or headers are
	// invalid.
	ErrCorrupt = errors.New("corrupt stream")
	// ErrTruncated is a stream that ends inside a page or a packet.
	ErrTruncated = errors.New("truncated stream")
)

// Sound is a decoded stream.
type Sound struct {
	// SampleRate is in frames per second.
	SampleRate int
	// Channels is the number of interleaved channels, in Vorbis
	// channel order.
	Channels int
	// Samples is the PCM, interleaved by channel.
	Samples []int16
}

// Frames is the length of the sound in frames (samples per channel).
func (s Sound) Frames() int { return len(s.Samples) / s.Channels }

// Decode decodes the first logical Vorbis stream of an Ogg file. Other
// logical streams multiplexed with it are skipped; a chained stream
// after it is not read.
func Decode(data []byte) (Sound, error) {
	ogg := newOggReader(data)
	d, err := readHeaders(ogg)
	if err != nil {
		return Sound{}, err
	}
	frames := lastGranule(data, ogg.serial)
	out := make([]int16, 0, int(frames)*d.channels)
	out, err = d.decodeStream(ogg, out)
	if err != nil {
		return Sound{}, err
	}
	return Sound{SampleRate: d.rate, Channels: d.channels, Samples: out}, nil
}

type mapping struct {
	coupling [][2]int
	mux      []int
	floors   []int
	residues []int
}

type mode struct {
	blockflag int
	mapping   int
}

type decoder struct {
	channels   int
	rate       int
	blocksizes [2]int
	books      []*codebook
	floors     []floor
	residues   []*residue
	mappings   []mapping
	modes      []mode
	modeBits   uint
	mdct       [2]*imdct
	windows    [2][]float32

	r         bitReader
	vectors   [][]float32
	curves    [][]float32
	pcm       [][]float32
	saved     [][]float32
	unused    []bool
	noResidue []bool
	bundle    [][]float32
	bundleOff []bool
	prevLong  bool
	started   bool
}

func readHeaders(ogg *oggReader) (*decoder, error) {
	d := &decoder{}
	parse := [3]func([]byte) error{
		d.parseIdentification,
		func(h []byte) error { return checkHeader(h, 3, "comment") },
		d.parseSetup,
	}
	for i, header := range parse {
		p, ok, err := ogg.next()
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fmt.Errorf("vorbis: stream ends after %d of 3 headers: %w", i, ErrTruncated)
		}
		if err := header(p.data); err != nil {
			return nil, err
		}
	}
	d.allocate()
	return d, nil
}

func checkHeader(h []byte, kind byte, name string) error {
	if len(h) < 7 || h[0] != kind || string(h[1:7]) != "vorbis" {
		return fmt.Errorf("vorbis: %s header: %w", name, ErrNotVorbis)
	}
	return nil
}

func (d *decoder) parseIdentification(h []byte) error {
	if err := checkHeader(h, 1, "identification"); err != nil {
		return err
	}
	r := &d.r
	r.reset(h[7:])
	if version := r.read(32); version != 0 {
		return fmt.Errorf("vorbis: identification header: version %d: %w", version, ErrNotVorbis)
	}
	d.channels = int(r.read(8))
	d.rate = int(r.read(32))
	r.read(32)
	r.read(32)
	r.read(32)
	d.blocksizes[0] = 1 << r.read(4)
	d.blocksizes[1] = 1 << r.read(4)
	framing := r.flag()
	switch {
	case r.eop:
		return fmt.Errorf("vorbis: identification header: %w", ErrTruncated)
	case d.channels == 0 || d.rate == 0:
		return fmt.Errorf("vorbis: identification header: %d channels at %d Hz: %w", d.channels, d.rate, ErrCorrupt)
	case d.blocksizes[0] < 64 || d.blocksizes[1] > 8192 || d.blocksizes[0] > d.blocksizes[1]:
		return fmt.Errorf("vorbis: identification header: block sizes %d and %d: %w", d.blocksizes[0], d.blocksizes[1], ErrCorrupt)
	case !framing:
		return fmt.Errorf("vorbis: identification header: framing bit unset: %w", ErrCorrupt)
	}
	return nil
}

func (d *decoder) parseSetup(h []byte) error {
	if err := checkHeader(h, 5, "setup"); err != nil {
		return err
	}
	r := &d.r
	r.reset(h[7:])
	d.books = make([]*codebook, r.read(8)+1)
	budget := maxSetupValues
	for i := range d.books {
		c, err := parseCodebook(r, i, &budget)
		if err != nil {
			return err
		}
		d.books[i] = c
	}
	times := int(r.read(6) + 1)
	for i := range times {
		if t := r.read(16); t != 0 {
			return fmt.Errorf("vorbis: setup header: time domain transform %d is type %d: %w", i, t, ErrCorrupt)
		}
	}
	d.floors = make([]floor, r.read(6)+1)
	for i := range d.floors {
		var err error
		switch kind := r.read(16); kind {
		case 0:
			d.floors[i], err = parseFloor0(r, i, d.books, d.blocksizes)
		case 1:
			d.floors[i], err = parseFloor1(r, i, d.books)
		default:
			err = fmt.Errorf("vorbis: setup header: floor %d is type %d: %w", i, kind, ErrCorrupt)
		}
		if err != nil {
			return err
		}
	}
	d.residues = make([]*residue, r.read(6)+1)
	for i := range d.residues {
		res, err := parseResidue(r, i, d.books)
		if err != nil {
			return err
		}
		d.residues[i] = res
	}
	d.mappings = make([]mapping, r.read(6)+1)
	for i := range d.mappings {
		m, err := d.parseMapping(r, i)
		if err != nil {
			return err
		}
		d.mappings[i] = m
	}
	d.modes = make([]mode, r.read(6)+1)
	for i := range d.modes {
		m := mode{blockflag: int(r.read(1))}
		window, transform := r.read(16), r.read(16)
		m.mapping = int(r.read(8))
		if window != 0 || transform != 0 || m.mapping >= len(d.mappings) {
			return fmt.Errorf("vorbis: setup header: mode %d has window %d, transform %d, mapping %d of %d: %w", i, window, transform, m.mapping, len(d.mappings), ErrCorrupt)
		}
		d.modes[i] = m
	}
	framing := r.flag()
	if r.eop {
		return fmt.Errorf("vorbis: setup header: %w", ErrTruncated)
	}
	if !framing {
		return fmt.Errorf("vorbis: setup header: framing bit unset: %w", ErrCorrupt)
	}
	d.modeBits = ilog(len(d.modes) - 1)
	return nil
}

func (d *decoder) parseMapping(r *bitReader, index int) (mapping, error) {
	if kind := r.read(16); kind != 0 {
		return mapping{}, fmt.Errorf("vorbis: setup header: mapping %d is type %d: %w", index, kind, ErrCorrupt)
	}
	submaps := 1
	if r.flag() {
		submaps = int(r.read(4) + 1)
	}
	var m mapping
	if r.flag() {
		m.coupling = make([][2]int, r.read(8)+1)
		width := ilog(d.channels - 1)
		for i := range m.coupling {
			mag, ang := int(r.read(width)), int(r.read(width))
			if mag == ang || mag >= d.channels || ang >= d.channels {
				return mapping{}, fmt.Errorf("vorbis: setup header: mapping %d couples channels %d and %d of %d: %w", index, mag, ang, d.channels, ErrCorrupt)
			}
			m.coupling[i] = [2]int{mag, ang}
		}
	}
	if reserved := r.read(2); reserved != 0 {
		return mapping{}, fmt.Errorf("vorbis: setup header: mapping %d reserved bits %d: %w", index, reserved, ErrCorrupt)
	}
	m.mux = make([]int, d.channels)
	if submaps > 1 {
		for i := range m.mux {
			m.mux[i] = int(r.read(4))
			if m.mux[i] >= submaps {
				return mapping{}, fmt.Errorf("vorbis: setup header: mapping %d puts channel %d in submap %d of %d: %w", index, i, m.mux[i], submaps, ErrCorrupt)
			}
		}
	}
	m.floors = make([]int, submaps)
	m.residues = make([]int, submaps)
	for i := range submaps {
		r.read(8)
		m.floors[i] = int(r.read(8))
		m.residues[i] = int(r.read(8))
		if m.floors[i] >= len(d.floors) || m.residues[i] >= len(d.residues) {
			return mapping{}, fmt.Errorf("vorbis: setup header: mapping %d submap %d names floor %d of %d, residue %d of %d: %w", index, i, m.floors[i], len(d.floors), m.residues[i], len(d.residues), ErrCorrupt)
		}
	}
	return m, nil
}

func (d *decoder) allocate() {
	long := d.blocksizes[1]
	for i, n := range d.blocksizes {
		d.mdct[i] = newIMDCT(n)
		d.windows[i] = window(n / 2)
	}
	d.vectors = planes(d.channels, long/2)
	d.curves = planes(d.channels, long/2)
	d.pcm = planes(d.channels, long)
	d.saved = planes(d.channels, long/2)
	d.unused = make([]bool, d.channels)
	d.noResidue = make([]bool, d.channels)
	d.bundle = make([][]float32, 0, d.channels)
	d.bundleOff = make([]bool, 0, d.channels)
	for _, res := range d.residues {
		res.allocate(d.channels, long/2, d.books)
	}
}

func planes(n, size int) [][]float32 {
	backing := make([]float32, n*size)
	p := make([][]float32, n)
	for i := range p {
		p[i] = backing[i*size : (i+1)*size : (i+1)*size]
	}
	return p
}

func window(n int) []float32 {
	w := make([]float32, n)
	for i := range w {
		s := gomath.Sin((float64(i) + 0.5) / float64(n) * math.Pi / 2)
		w[i] = float32(gomath.Sin(math.Pi / 2 * float64(s*s)))
	}
	return w
}

func (d *decoder) decodeStream(ogg *oggReader, out []int16) ([]int16, error) {
	produced := int64(0)
	firstPage := true
	base := int64(0)
	for {
		p, ok, err := ogg.next()
		if err != nil {
			return nil, err
		}
		if !ok {
			return out, nil
		}
		if len(p.data) > 0 {
			before := len(out)
			out, err = d.decodePacket(p.data, out)
			if err != nil {
				return nil, fmt.Errorf("vorbis: packet in page at offset %d: %w", ogg.pageOff, err)
			}
			produced += int64(len(out)-before) / int64(d.channels)
		}
		if p.granule == noGranule {
			continue
		}
		if firstPage && !p.eos {
			if p.granule < produced {
				drop := int(produced-p.granule) * d.channels
				out = append(out[:0], out[drop:]...)
				produced = p.granule
			} else {
				base = p.granule - produced
			}
		}
		firstPage = false
		if p.eos {
			if end := p.granule - base; end >= 0 && end < produced {
				out = out[:end*int64(d.channels)]
				produced = end
			}
		}
	}
}

func (d *decoder) decodePacket(data []byte, out []int16) ([]int16, error) {
	r := &d.r
	r.reset(data)
	if r.read(1) != 0 {
		return nil, fmt.Errorf("header packet among audio packets: %w", ErrCorrupt)
	}
	modeNum := int(r.read(d.modeBits))
	if modeNum >= len(d.modes) {
		return nil, fmt.Errorf("mode %d of %d: %w", modeNum, len(d.modes), ErrCorrupt)
	}
	md := d.modes[modeNum]
	long := md.blockflag == 1
	if long {
		r.read(2)
	}
	if r.eop {
		return nil, fmt.Errorf("packet header: %w", ErrTruncated)
	}
	n := d.blocksizes[md.blockflag]
	half := n / 2
	mp := &d.mappings[md.mapping]
	for ch := range d.channels {
		curve := d.curves[ch][:half]
		used, err := d.floors[mp.floors[mp.mux[ch]]].decode(r, d.books, md.blockflag, curve)
		if err != nil {
			return nil, err
		}
		d.unused[ch] = !used
		d.noResidue[ch] = !used
		clear(d.vectors[ch][:half])
	}
	for _, c := range mp.coupling {
		if !d.noResidue[c[0]] || !d.noResidue[c[1]] {
			d.noResidue[c[0]], d.noResidue[c[1]] = false, false
		}
	}
	for s, ri := range mp.residues {
		d.bundle, d.bundleOff = d.bundle[:0], d.bundleOff[:0]
		for ch := range d.channels {
			if mp.mux[ch] == s {
				d.bundle = append(d.bundle, d.vectors[ch])
				d.bundleOff = append(d.bundleOff, d.noResidue[ch])
			}
		}
		d.residues[ri].decode(r, d.books, d.bundle, d.bundleOff, half)
	}
	for _, c := range slices.Backward(mp.coupling) {
		uncouple(d.vectors[c[0]][:half], d.vectors[c[1]][:half])
	}
	for ch := range d.channels {
		v := d.vectors[ch][:half]
		if d.unused[ch] {
			clear(v)
		} else {
			for i, f := range d.curves[ch][:half] {
				v[i] *= f
			}
		}
		d.mdct[md.blockflag].inverse(v, d.pcm[ch][:n])
	}
	out = d.overlap(long, n, out)
	d.prevLong = long
	return out, nil
}

func uncouple(mag, ang []float32) {
	for j, m := range mag {
		a := ang[j]
		switch {
		case m > 0 && a > 0:
			ang[j] = m - a
		case m > 0:
			ang[j] = m
			mag[j] = m + a
		case a > 0:
			ang[j] = m + a
		default:
			ang[j] = m
			mag[j] = m - a
		}
	}
}

func (d *decoder) overlap(long bool, n int, out []int16) []int16 {
	half := n / 2
	if !d.started {
		d.started = true
		for ch := range d.channels {
			copy(d.saved[ch], d.pcm[ch][half:n])
		}
		return out
	}
	n0 := d.blocksizes[0] / 2
	n1 := d.blocksizes[1] / 2
	prev := n0
	if d.prevLong {
		prev = n1
	}
	frames := prev/2 + half/2
	at := len(out)
	out = slices.Grow(out, frames*d.channels)[:at+frames*d.channels]
	frame := out[at:]
	for ch := range d.channels {
		saved := d.saved[ch]
		p := d.pcm[ch]
		var w []float32
		lead, from := 0, 0
		switch {
		case d.prevLong && long:
			w = d.windows[1]
		case d.prevLong:
			w = d.windows[0]
			lead = n1/2 - n0/2
		case long:
			w = d.windows[0]
			from = n1/2 - n0/2
		default:
			w = d.windows[0]
		}
		k := ch
		for i := range lead {
			frame[k] = toPCM(saved[i])
			k += d.channels
		}
		wn := len(w)
		for i := range wn {
			frame[k] = toPCM(float32(saved[lead+i]*w[wn-1-i]) + float32(p[from+i]*w[i]))
			k += d.channels
		}
		for i := from + wn; i < from+wn+(frames-lead-wn); i++ {
			frame[k] = toPCM(p[i])
			k += d.channels
		}
		copy(saved, p[half:n])
	}
	return out
}

func toPCM(x float32) int16 {
	v := math.RoundToEven(float64(x * 32768))
	switch {
	case v > math.MaxInt16:
		return math.MaxInt16
	case v < math.MinInt16:
		return math.MinInt16
	}
	return int16(v)
}
