package mux

import (
	"encoding/binary"
	"fmt"
	"io"
	"math"
)

const (
	mp4MovieHz     = 1000
	mp4BaseIsMoof  = 0x020000
	mp4RunFlags    = 0x000701
	mp4SyncFlags   = 0x02000000
	mp4DeltaFlags  = 0x01010000
	mp4UnityVolume = 0x0100
)

var mp4Matrix = [9]uint32{0x10000, 0, 0, 0, 0x10000, 0, 0, 0, 0x40000000}

type boxes struct {
	b    []byte
	open []int
}

func (x *boxes) begin(typ string) {
	x.open = append(x.open, len(x.b))
	x.b = append(x.b, 0, 0, 0, 0)
	x.b = append(x.b, typ...)
}

func (x *boxes) full(typ string, version uint8, flags uint32) {
	x.begin(typ)
	x.u32(uint32(version)<<24 | flags)
}

func (x *boxes) end() {
	start := x.open[len(x.open)-1]
	x.open = x.open[:len(x.open)-1]
	binary.BigEndian.PutUint32(x.b[start:], uint32(len(x.b)-start))
}

func (x *boxes) u8(v ...uint8) { x.b = append(x.b, v...) }
func (x *boxes) u16(v uint16)  { x.b = binary.BigEndian.AppendUint16(x.b, v) }
func (x *boxes) u32(v uint32)  { x.b = binary.BigEndian.AppendUint32(x.b, v) }
func (x *boxes) u64(v uint64)  { x.b = binary.BigEndian.AppendUint64(x.b, v) }
func (x *boxes) zeros(n int)   { x.b = append(x.b, make([]byte, n)...) }

func (x *boxes) leaf(typ string, payload []byte) {
	x.begin(typ)
	x.b = append(x.b, payload...)
	x.end()
}

func (x *boxes) matrix() {
	for _, v := range mp4Matrix {
		x.u32(v)
	}
}

type mp4Sample struct {
	size, dur uint32
	key       bool
}

type mp4Run struct {
	base    uint64
	samples []mp4Sample
	data    []byte
}

// MP4 writes a fragmented ISO BMFF file: ftyp and a moov describing
// the tracks up front, then one moof and mdat per fragment. A fragment
// closes before each video keyframe and at least once a second, and is
// written with a single Write.
type MP4 struct {
	base
	w    io.Writer
	runs []mp4Run
	seq  uint32
	out  boxes
}

// NewMP4 writes the file header for tracks to w and returns the muxer.
// Close does not close w.
func NewMP4(w io.Writer, tracks ...Track) (*MP4, error) {
	b, err := newBase(tracks)
	if err != nil {
		return nil, err
	}
	m := &MP4{base: b, w: w, runs: make([]mp4Run, len(tracks))}
	m.header()
	if _, err := w.Write(m.out.b); err != nil {
		return nil, fmt.Errorf("mux: write the MP4 header: %w", err)
	}
	return m, nil
}

func (m *MP4) header() {
	x := &m.out
	x.begin("ftyp")
	x.b = append(x.b, "isom"...)
	x.u32(0x200)
	x.b = append(x.b, "isomiso6mp41"...)
	x.end()
	x.begin("moov")
	x.full("mvhd", 0, 0)
	x.u32(0)
	x.u32(0)
	x.u32(mp4MovieHz)
	x.u32(0)
	x.u32(0x10000)
	x.u16(mp4UnityVolume)
	x.zeros(10)
	x.matrix()
	x.zeros(24)
	x.u32(uint32(len(m.tracks) + 1))
	x.end()
	for i := range m.tracks {
		m.trak(uint32(i+1), &m.tracks[i])
	}
	x.begin("mvex")
	for i := range m.tracks {
		x.full("trex", 0, 0)
		x.u32(uint32(i + 1))
		x.u32(1)
		x.zeros(12)
		x.end()
	}
	x.end()
	x.end()
}

func (m *MP4) trak(id uint32, t *track) {
	x := &m.out
	x.begin("trak")
	x.full("tkhd", 0, 3)
	x.zeros(8)
	x.u32(id)
	x.zeros(4 + 4 + 8)
	x.u16(0)
	x.u16(0)
	if t.video() {
		x.u16(0)
	} else {
		x.u16(mp4UnityVolume)
	}
	x.u16(0)
	x.matrix()
	x.u32(uint32(t.width) << 16)
	x.u32(uint32(t.height) << 16)
	x.end()
	if !t.video() {
		x.begin("edts")
		x.full("elst", 0, 0)
		x.u32(1)
		x.u32(0)
		x.u32(uint32(t.preSkip))
		x.u32(0x10000)
		x.end()
		x.end()
	}
	x.begin("mdia")
	x.full("mdhd", 0, 0)
	x.zeros(8)
	x.u32(t.timescale())
	x.u32(0)
	x.u16(0x55c4)
	x.u16(0)
	x.end()
	x.full("hdlr", 0, 0)
	x.u32(0)
	if t.video() {
		x.b = append(x.b, "vide"...)
		x.zeros(12)
		x.b = append(x.b, "VideoHandler\x00"...)
	} else {
		x.b = append(x.b, "soun"...)
		x.zeros(12)
		x.b = append(x.b, "SoundHandler\x00"...)
	}
	x.end()
	x.begin("minf")
	if t.video() {
		x.full("vmhd", 0, 1)
		x.zeros(8)
	} else {
		x.full("smhd", 0, 0)
		x.zeros(4)
	}
	x.end()
	x.begin("dinf")
	x.full("dref", 0, 0)
	x.u32(1)
	x.full("url ", 0, 1)
	x.end()
	x.end()
	x.end()
	x.begin("stbl")
	x.full("stsd", 0, 0)
	x.u32(1)
	m.sampleEntry(t)
	x.end()
	for _, typ := range []string{"stts", "stsc", "stco"} {
		x.full(typ, 0, 0)
		x.u32(0)
		x.end()
	}
	x.full("stsz", 0, 0)
	x.u32(0)
	x.u32(0)
	x.end()
	x.end()
	x.end()
	x.end()
	x.end()
}

func (m *MP4) sampleEntry(t *track) {
	x := &m.out
	switch t.codec {
	case codecH264, codecH265:
		typ, cfg := "avc1", "avcC"
		if t.codec == codecH265 {
			typ, cfg = "hvc1", "hvcC"
		}
		x.begin(typ)
		x.zeros(6)
		x.u16(1)
		x.zeros(16)
		x.u16(uint16(t.width))
		x.u16(uint16(t.height))
		x.u32(0x00480000)
		x.u32(0x00480000)
		x.u32(0)
		x.u16(1)
		x.zeros(32)
		x.u16(0x18)
		x.u16(0xffff)
		x.leaf(cfg, t.config)
		x.end()
	case codecOpus:
		x.begin("Opus")
		x.zeros(6)
		x.u16(1)
		x.zeros(8)
		x.u16(uint16(t.channels))
		x.u16(16)
		x.u32(0)
		x.u32(opusRate << 16)
		x.begin("dOps")
		x.u8(0, t.channels)
		x.u16(t.preSkip)
		x.u32(opusRate)
		x.u16(0)
		x.u8(0)
		x.end()
		x.end()
	}
}

// WriteSample adds s to the current fragment, first writing the
// fragment out when s starts a new one. After an error the muxer only
// returns that error.
func (m *MP4) WriteSample(s Sample) error {
	u, err := m.accept(s)
	if err != nil {
		return err
	}
	t, r := u.t, &m.runs[s.Track]
	if n := len(r.samples); n > 0 {
		d := t.tickGap()
		if d == 0 || d > math.MaxUint32 {
			return m.fail(fmt.Errorf("mux: track %d sample at %v follows the one at %v by %d ticks of %d Hz", s.Track, t.last, t.prev, d, t.timescale()))
		}
		r.samples[n-1].dur = uint32(d)
	} else {
		r.base = ticks(t.last, t.timescale())
	}
	if m.cut(u) {
		if err := m.flush(false); err != nil {
			return err
		}
	}
	r.samples = append(r.samples, mp4Sample{size: uint32(len(u.data)), key: u.key})
	r.data = append(r.data, u.data...)
	return nil
}

// Close gives each track's last sample the duration of the one before
// it and writes the final fragment.
func (m *MP4) Close() error {
	if m.err != nil {
		return m.err
	}
	for i := range m.runs {
		r, t := &m.runs[i], &m.tracks[i]
		if n := len(r.samples); n > 0 && t.n > 1 {
			r.samples[n-1].dur = uint32(t.tickGap())
		}
	}
	if err := m.flush(true); err != nil {
		return err
	}
	m.err = errClosed
	return nil
}

func (r *mp4Run) ready(all bool) int {
	if all || len(r.samples) == 0 {
		return len(r.samples)
	}
	return len(r.samples) - 1
}

func (m *MP4) flush(all bool) error {
	x := &m.out
	x.b = x.b[:0]
	m.seq++
	x.begin("moof")
	x.full("mfhd", 0, 0)
	x.u32(m.seq)
	x.end()
	offsets := make([]int, 0, len(m.runs))
	data := 0
	for i := range m.runs {
		r := &m.runs[i]
		n := r.ready(all)
		if n == 0 {
			continue
		}
		x.begin("traf")
		x.full("tfhd", 0, mp4BaseIsMoof)
		x.u32(uint32(i + 1))
		x.end()
		x.full("tfdt", 1, 0)
		x.u64(r.base)
		x.end()
		x.full("trun", 0, mp4RunFlags)
		x.u32(uint32(n))
		offsets = append(offsets, len(x.b))
		x.u32(uint32(data))
		for _, s := range r.samples[:n] {
			x.u32(s.dur)
			x.u32(s.size)
			if s.key {
				x.u32(mp4SyncFlags)
			} else {
				x.u32(mp4DeltaFlags)
			}
			data += int(s.size)
		}
		x.end()
		x.end()
	}
	x.end()
	if len(offsets) == 0 {
		m.seq--
		return nil
	}
	moof := len(x.b)
	if moof+8+data > math.MaxInt32 {
		return m.fail(fmt.Errorf("mux: MP4 fragment %d holds %d bytes, more than a fragment's offsets reach", m.seq, data))
	}
	for _, at := range offsets {
		binary.BigEndian.PutUint32(x.b[at:], binary.BigEndian.Uint32(x.b[at:])+uint32(moof+8))
	}
	x.u32(uint32(8 + data))
	x.b = append(x.b, "mdat"...)
	for i := range m.runs {
		r := &m.runs[i]
		n := r.ready(all)
		size := 0
		var dur uint64
		for _, s := range r.samples[:n] {
			size += int(s.size)
			dur += uint64(s.dur)
		}
		x.b = append(x.b, r.data[:size]...)
		r.data = r.data[:copy(r.data, r.data[size:])]
		r.samples = r.samples[:copy(r.samples, r.samples[n:])]
		r.base += dur
	}
	if _, err := m.w.Write(x.b); err != nil {
		return m.fail(fmt.Errorf("mux: write MP4 fragment %d: %w", m.seq, err))
	}
	return nil
}
