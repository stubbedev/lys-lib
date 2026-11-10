package mux

import (
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"math/bits"
	"time"

	"github.com/stubbedev/lys-lib/intconv"
	"github.com/stubbedev/lys-lib/internal/matroska"
)

const (
	mkvSizeLen     = 8
	mkvUnknownSize = 1<<(7*mkvSizeLen) - 1
	mkvTickScale   = time.Millisecond
	mkvApp         = "lys-lib"
	mkvDurationLen = 2 + 1 + 8
)

var mkvCodecIDs = map[codec]string{
	codecH264: matroska.CodecH264,
	codecH265: matroska.CodecH265,
	codecOpus: matroska.CodecOpus,
}

type ebml struct {
	b    []byte
	open []int
}

func (e *ebml) id(id matroska.ID) {
	n := (bits.Len32(uint32(id)) + 7) / 8
	for i := n - 1; i >= 0; i-- {
		e.b = append(e.b, byte(id>>(8*i)&0xff))
	}
}

func (e *ebml) size(n uint64) {
	l := 1
	for n >= 1<<(7*l)-1 {
		l++
	}
	e.sizeN(n, l)
}

func (e *ebml) sizeN(n uint64, l int) {
	v := n | 1<<(7*l)
	for i := l - 1; i >= 0; i-- {
		e.b = append(e.b, byte(v>>(8*i)&0xff))
	}
}

func (e *ebml) begin(id matroska.ID) {
	e.id(id)
	e.open = append(e.open, len(e.b))
	e.b = append(e.b, make([]byte, mkvSizeLen)...)
}

func (e *ebml) end() {
	start := e.open[len(e.open)-1]
	e.open = e.open[:len(e.open)-1]
	binary.BigEndian.PutUint64(e.b[start:], intconv.Must[uint64](len(e.b)-start-mkvSizeLen)|1<<(7*mkvSizeLen))
}

func (e *ebml) uintN(id matroska.ID, v uint64, n int) {
	e.id(id)
	e.size(intconv.Must[uint64](n))
	for i := n - 1; i >= 0; i-- {
		e.b = append(e.b, byte(v>>(8*i)&0xff))
	}
}

func (e *ebml) uint(id matroska.ID, v uint64) { e.uintN(id, v, max(1, (bits.Len64(v)+7)/8)) }

func (e *ebml) bytes(id matroska.ID, v []byte) {
	e.id(id)
	e.size(uint64(len(v)))
	e.b = append(e.b, v...)
}

func (e *ebml) float(id matroska.ID, v float64) { e.uintN(id, math.Float64bits(v), 8) }

func (e *ebml) void(total int) {
	e.id(matroska.Void)
	e.size(intconv.Must[uint64](total - 2))
	e.b = append(e.b, make([]byte, total-2)...)
}

type mkvCue struct {
	ms    uint64
	track int
	pos   uint64
}

// MKV writes a Matroska file: the EBML header, then a Segment with a
// SeekHead, Info and Tracks, one Cluster of SimpleBlocks per stretch
// between video keyframes (at most a second), and Cues at Close. Each
// Cluster is written whole with a single Write. When w is an
// io.WriteSeeker, Close also patches the Segment size, the SeekHead's
// entry for the Cues and the Duration; otherwise the Segment keeps an
// unknown size.
type MKV struct {
	base
	w        io.Writer
	ws       io.WriteSeeker
	origin   int64
	segData  int
	pos      uint64
	seekLen  int
	tracksAt int
	cluster  ebml
	clusterT int64
	cues     []mkvCue
	video    bool
}

// NewMKV writes the file header for tracks to w and returns the muxer.
// Close does not close w.
func NewMKV(w io.Writer, tracks ...Track) (*MKV, error) {
	b, err := newBase(tracks)
	if err != nil {
		return nil, err
	}
	m := &MKV{base: b, w: w}
	if ws, ok := w.(io.WriteSeeker); ok {
		if origin, err := ws.Seek(0, io.SeekCurrent); err == nil {
			m.ws, m.origin = ws, origin
		}
	}
	for i := range m.tracks {
		m.video = m.video || m.tracks[i].video()
	}
	var e ebml
	e.begin(matroska.EBML)
	e.uint(matroska.EBMLVersion, 1)
	e.uint(matroska.EBMLReadVersion, 1)
	e.uint(matroska.EBMLMaxIDLength, 4)
	e.uint(matroska.EBMLMaxSizeLength, 8)
	e.bytes(matroska.DocType, []byte("matroska"))
	e.uint(matroska.DocTypeVersion, 4)
	e.uint(matroska.DocTypeReadVersion, 2)
	e.end()
	e.id(matroska.Segment)
	e.sizeN(mkvUnknownSize, mkvSizeLen)
	m.segData = len(e.b)
	m.seekLen = len(seekHead(0, 0, 0, true))
	info := m.info()
	m.tracksAt = m.seekLen + len(info)
	e.b = append(e.b, seekHead(intconv.Must[uint64](m.seekLen), intconv.Must[uint64](m.tracksAt), 0, false)...)
	e.void(m.seekLen - (len(e.b) - m.segData))
	e.b = append(e.b, info...)
	e.b = append(e.b, m.tracksElement()...)
	m.pos = intconv.Must[uint64](len(e.b) - m.segData)
	if _, err := w.Write(e.b); err != nil {
		return nil, fmt.Errorf("mux: write the Matroska header: %w", err)
	}
	return m, nil
}

func seekHead(info, tracks, cues uint64, withCues bool) []byte {
	var e ebml
	e.begin(matroska.SeekHead)
	entry := func(id matroska.ID, pos uint64) {
		e.begin(matroska.Seek)
		e.uintN(matroska.SeekID, uint64(id), 4)
		e.uintN(matroska.SeekPosition, pos, 8)
		e.end()
	}
	entry(matroska.Info, info)
	entry(matroska.Tracks, tracks)
	if withCues {
		entry(matroska.Cues, cues)
	}
	e.end()
	return e.b
}

func (m *MKV) info() []byte {
	var e ebml
	e.begin(matroska.Info)
	e.uint(matroska.TimestampScale, uint64(mkvTickScale))
	e.bytes(matroska.MuxingApp, []byte(mkvApp))
	e.bytes(matroska.WritingApp, []byte(mkvApp))
	if m.ws != nil {
		e.void(mkvDurationLen)
	}
	e.end()
	return e.b
}

func (m *MKV) tracksElement() []byte {
	var e ebml
	e.begin(matroska.Tracks)
	for i := range m.tracks {
		t := &m.tracks[i]
		e.begin(matroska.TrackEntry)
		e.uint(matroska.TrackNumber, uint64(i+1))
		e.uint(matroska.TrackUID, uint64(i+1))
		e.uint(matroska.FlagLacing, 0)
		e.bytes(matroska.CodecID, []byte(mkvCodecIDs[t.codec]))
		e.bytes(matroska.CodecPrivate, t.config)
		if t.video() {
			e.uint(matroska.TrackType, matroska.TrackVideo)
			e.begin(matroska.Video)
			e.uint(matroska.PixelWidth, uint64(t.width))
			e.uint(matroska.PixelHeight, uint64(t.height))
			e.end()
		} else {
			e.uint(matroska.TrackType, matroska.TrackAudio)
			e.uint(matroska.CodecDelay, uint64(t.preSkip)*uint64(time.Second)/opusRate)
			e.uint(matroska.SeekPreRoll, uint64(opusPreRoll))
			e.begin(matroska.Audio)
			e.uintN(matroska.SamplingFrequency, uint64(math.Float32bits(opusRate)), 4)
			e.uint(matroska.Channels, uint64(t.channels))
			e.end()
		}
		e.end()
	}
	e.end()
	return e.b
}

// WriteSample adds s to the current cluster, first writing the cluster
// out when s starts a new one. After an error the muxer only returns
// that error.
func (m *MKV) WriteSample(s Sample) error {
	u, err := m.accept(s)
	if err != nil {
		return err
	}
	ms := int64(u.t.last / mkvTickScale)
	if m.cut(u) {
		if err := m.flush(); err != nil {
			return err
		}
	}
	if len(m.cluster.b) == 0 {
		m.clusterT = ms
		m.cluster.begin(matroska.Cluster)
		m.cluster.uint(matroska.Timestamp, intconv.Must[uint64](ms))
		if u.key && (u.t.video() || !m.video) {
			m.cues = append(m.cues, mkvCue{ms: intconv.Must[uint64](ms), track: s.Track + 1, pos: m.pos})
		}
	}
	rel := ms - m.clusterT
	if rel < math.MinInt16 || rel > math.MaxInt16 {
		return m.fail(fmt.Errorf("mux: track %d sample at %v lies %d ms from its cluster's start, past a block's reach", s.Track, u.t.last, rel))
	}
	c := &m.cluster
	c.id(matroska.SimpleBlock)
	var head ebml
	head.size(intconv.Must[uint64](s.Track + 1))
	c.size(uint64(len(head.b) + 3 + len(u.data)))
	c.b = append(c.b, head.b...)
	c.b = append(c.b, byte(rel>>8&0xff), byte(rel&0xff))
	flags := byte(0)
	if u.key {
		flags = matroska.FlagKeyframe
	}
	c.b = append(c.b, flags)
	c.b = append(c.b, u.data...)
	return nil
}

func (m *MKV) write(what string, b []byte) error {
	if _, err := m.w.Write(b); err != nil {
		return m.fail(fmt.Errorf("mux: write Matroska %s: %w", what, err))
	}
	m.pos += uint64(len(b))
	return nil
}

func (m *MKV) flush() error {
	if len(m.cluster.b) == 0 {
		return nil
	}
	m.cluster.end()
	err := m.write(fmt.Sprintf("cluster at %d ms", m.clusterT), m.cluster.b)
	m.cluster.b = m.cluster.b[:0]
	return err
}

// Close writes the last cluster and the Cues and, when the writer can
// seek, patches the header and returns the writer to the file's end.
func (m *MKV) Close() error {
	if m.err != nil {
		return m.err
	}
	if err := m.flush(); err != nil {
		return err
	}
	cuesAt := m.pos
	var e ebml
	e.begin(matroska.Cues)
	for _, c := range m.cues {
		e.begin(matroska.CuePoint)
		e.uint(matroska.CueTime, c.ms)
		e.begin(matroska.CueTrackPositions)
		e.uint(matroska.CueTrack, intconv.Must[uint64](c.track))
		e.uint(matroska.CueClusterPosition, c.pos)
		e.end()
		e.end()
	}
	e.end()
	if err := m.write("cues", e.b); err != nil {
		return err
	}
	m.err = errClosed
	if m.ws == nil {
		return nil
	}
	return m.patch(cuesAt)
}

func (m *MKV) patch(cuesAt uint64) error {
	var end time.Duration
	for i := range m.tracks {
		t := &m.tracks[i]
		if t.n > 0 {
			end = max(end, t.last+t.lastDuration())
		}
	}
	var size, dur ebml
	size.sizeN(m.pos, mkvSizeLen)
	dur.float(matroska.Duration, float64(end/mkvTickScale))
	for _, p := range []struct {
		what string
		at   int64
		b    []byte
	}{
		{"segment size", int64(m.segData - mkvSizeLen), size.b},
		{"seek head", int64(m.segData), seekHead(intconv.Must[uint64](m.seekLen), intconv.Must[uint64](m.tracksAt), cuesAt, true)},
		{"duration", int64(m.segData + m.tracksAt - mkvDurationLen), dur.b},
	} {
		if _, err := m.ws.Seek(m.origin+p.at, io.SeekStart); err != nil {
			return fmt.Errorf("mux: seek to the Matroska %s: %w", p.what, err)
		}
		if _, err := m.ws.Write(p.b); err != nil {
			return fmt.Errorf("mux: patch the Matroska %s: %w", p.what, err)
		}
	}
	pos, err := intconv.Exact[int64](m.pos)
	if err != nil {
		return fmt.Errorf("mux: the Matroska file's end: %w", err)
	}
	if _, err := m.ws.Seek(m.origin+int64(m.segData)+pos, io.SeekStart); err != nil {
		return fmt.Errorf("mux: seek to the Matroska file's end: %w", err)
	}
	return nil
}
