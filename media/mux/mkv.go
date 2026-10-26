package mux

import (
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"math/bits"
	"time"
)

const (
	idEBML               = 0x1a45dfa3
	idEBMLVersion        = 0x4286
	idEBMLReadVersion    = 0x42f7
	idEBMLMaxIDLength    = 0x42f2
	idEBMLMaxSizeLength  = 0x42f3
	idDocType            = 0x4282
	idDocTypeVersion     = 0x4287
	idDocTypeReadVersion = 0x4285
	idSegment            = 0x18538067
	idSeekHead           = 0x114d9b74
	idSeek               = 0x4dbb
	idSeekID             = 0x53ab
	idSeekPosition       = 0x53ac
	idInfo               = 0x1549a966
	idTimestampScale     = 0x2ad7b1
	idDuration           = 0x4489
	idMuxingApp          = 0x4d80
	idWritingApp         = 0x5741
	idTracks             = 0x1654ae6b
	idTrackEntry         = 0xae
	idTrackNumber        = 0xd7
	idTrackUID           = 0x73c5
	idTrackType          = 0x83
	idFlagLacing         = 0x9c
	idCodecID            = 0x86
	idCodecPrivate       = 0x63a2
	idCodecDelay         = 0x56aa
	idSeekPreRoll        = 0x56bb
	idVideo              = 0xe0
	idPixelWidth         = 0xb0
	idPixelHeight        = 0xba
	idAudio              = 0xe1
	idSamplingFrequency  = 0xb5
	idChannels           = 0x9f
	idCluster            = 0x1f43b675
	idTimestamp          = 0xe7
	idSimpleBlock        = 0xa3
	idCues               = 0x1c53bb6b
	idCuePoint           = 0xbb
	idCueTime            = 0xb3
	idCueTrackPositions  = 0xb7
	idCueTrack           = 0xf7
	idCueClusterPosition = 0xf1
	idVoid               = 0xec

	mkvSizeLen     = 8
	mkvUnknownSize = 1<<(7*mkvSizeLen) - 1
	mkvTickScale   = time.Millisecond
	mkvTrackVideo  = 1
	mkvTrackAudio  = 2
	mkvKeyframe    = 0x80
	mkvApp         = "lys-ui"
	mkvDurationLen = 2 + 1 + 8
)

var mkvCodecIDs = map[codec]string{
	codecH264: "V_MPEG4/ISO/AVC",
	codecH265: "V_MPEGH/ISO/HEVC",
	codecOpus: "A_OPUS",
}

type ebml struct {
	b    []byte
	open []int
}

func (e *ebml) id(id uint32) {
	n := (bits.Len32(id) + 7) / 8
	for i := n - 1; i >= 0; i-- {
		e.b = append(e.b, byte(id>>(8*i)))
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
		e.b = append(e.b, byte(v>>(8*i)))
	}
}

func (e *ebml) begin(id uint32) {
	e.id(id)
	e.open = append(e.open, len(e.b))
	e.b = append(e.b, make([]byte, mkvSizeLen)...)
}

func (e *ebml) end() {
	start := e.open[len(e.open)-1]
	e.open = e.open[:len(e.open)-1]
	binary.BigEndian.PutUint64(e.b[start:], uint64(len(e.b)-start-mkvSizeLen)|1<<(7*mkvSizeLen))
}

func (e *ebml) uintN(id uint32, v uint64, n int) {
	e.id(id)
	e.size(uint64(n))
	for i := n - 1; i >= 0; i-- {
		e.b = append(e.b, byte(v>>(8*i)))
	}
}

func (e *ebml) uint(id uint32, v uint64) { e.uintN(id, v, max(1, (bits.Len64(v)+7)/8)) }

func (e *ebml) bytes(id uint32, v []byte) {
	e.id(id)
	e.size(uint64(len(v)))
	e.b = append(e.b, v...)
}

func (e *ebml) float(id uint32, v float64) { e.uintN(id, math.Float64bits(v), 8) }

func (e *ebml) void(total int) {
	e.id(idVoid)
	e.size(uint64(total - 2))
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
	segData  int64
	pos      uint64
	seekLen  int
	tracksAt uint64
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
	e.begin(idEBML)
	e.uint(idEBMLVersion, 1)
	e.uint(idEBMLReadVersion, 1)
	e.uint(idEBMLMaxIDLength, 4)
	e.uint(idEBMLMaxSizeLength, 8)
	e.bytes(idDocType, []byte("matroska"))
	e.uint(idDocTypeVersion, 4)
	e.uint(idDocTypeReadVersion, 2)
	e.end()
	e.id(idSegment)
	e.sizeN(mkvUnknownSize, mkvSizeLen)
	m.segData = int64(len(e.b))
	m.seekLen = len(seekHead(0, 0, 0, true))
	info := m.info()
	m.tracksAt = uint64(m.seekLen + len(info))
	e.b = append(e.b, seekHead(uint64(m.seekLen), m.tracksAt, 0, false)...)
	e.void(m.seekLen - (len(e.b) - int(m.segData)))
	e.b = append(e.b, info...)
	e.b = append(e.b, m.tracksElement()...)
	m.pos = uint64(len(e.b)) - uint64(m.segData)
	if _, err := w.Write(e.b); err != nil {
		return nil, fmt.Errorf("mux: write the Matroska header: %w", err)
	}
	return m, nil
}

func seekHead(info, tracks, cues uint64, withCues bool) []byte {
	var e ebml
	e.begin(idSeekHead)
	entry := func(id uint32, pos uint64) {
		e.begin(idSeek)
		e.uintN(idSeekID, uint64(id), 4)
		e.uintN(idSeekPosition, pos, 8)
		e.end()
	}
	entry(idInfo, info)
	entry(idTracks, tracks)
	if withCues {
		entry(idCues, cues)
	}
	e.end()
	return e.b
}

func (m *MKV) info() []byte {
	var e ebml
	e.begin(idInfo)
	e.uint(idTimestampScale, uint64(mkvTickScale))
	e.bytes(idMuxingApp, []byte(mkvApp))
	e.bytes(idWritingApp, []byte(mkvApp))
	if m.ws != nil {
		e.void(mkvDurationLen)
	}
	e.end()
	return e.b
}

func (m *MKV) tracksElement() []byte {
	var e ebml
	e.begin(idTracks)
	for i := range m.tracks {
		t := &m.tracks[i]
		e.begin(idTrackEntry)
		e.uint(idTrackNumber, uint64(i+1))
		e.uint(idTrackUID, uint64(i+1))
		e.uint(idFlagLacing, 0)
		e.bytes(idCodecID, []byte(mkvCodecIDs[t.codec]))
		e.bytes(idCodecPrivate, t.config)
		if t.video() {
			e.uint(idTrackType, mkvTrackVideo)
			e.begin(idVideo)
			e.uint(idPixelWidth, uint64(t.width))
			e.uint(idPixelHeight, uint64(t.height))
			e.end()
		} else {
			e.uint(idTrackType, mkvTrackAudio)
			e.uint(idCodecDelay, uint64(t.preSkip)*uint64(time.Second)/opusRate)
			e.uint(idSeekPreRoll, uint64(opusPreRoll))
			e.begin(idAudio)
			e.uintN(idSamplingFrequency, uint64(math.Float32bits(opusRate)), 4)
			e.uint(idChannels, uint64(t.channels))
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
		m.cluster.begin(idCluster)
		m.cluster.uint(idTimestamp, uint64(ms))
		if u.key && (u.t.video() || !m.video) {
			m.cues = append(m.cues, mkvCue{ms: uint64(ms), track: s.Track + 1, pos: m.pos})
		}
	}
	rel := ms - m.clusterT
	if rel < math.MinInt16 || rel > math.MaxInt16 {
		return m.fail(fmt.Errorf("mux: track %d sample at %v lies %d ms from its cluster's start, past a block's reach", s.Track, u.t.last, rel))
	}
	c := &m.cluster
	c.id(idSimpleBlock)
	var head ebml
	head.size(uint64(s.Track + 1))
	c.size(uint64(len(head.b) + 3 + len(u.data)))
	c.b = append(c.b, head.b...)
	c.b = binary.BigEndian.AppendUint16(c.b, uint16(int16(rel)))
	flags := byte(0)
	if u.key {
		flags = mkvKeyframe
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
	e.begin(idCues)
	for _, c := range m.cues {
		e.begin(idCuePoint)
		e.uint(idCueTime, c.ms)
		e.begin(idCueTrackPositions)
		e.uint(idCueTrack, uint64(c.track))
		e.uint(idCueClusterPosition, c.pos)
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
	dur.float(idDuration, float64(end/mkvTickScale))
	for _, p := range []struct {
		what string
		at   int64
		b    []byte
	}{
		{"segment size", m.segData - mkvSizeLen, size.b},
		{"seek head", m.segData, seekHead(uint64(m.seekLen), m.tracksAt, cuesAt, true)},
		{"duration", m.segData + int64(m.tracksAt) - mkvDurationLen, dur.b},
	} {
		if _, err := m.ws.Seek(m.origin+p.at, io.SeekStart); err != nil {
			return fmt.Errorf("mux: seek to the Matroska %s: %w", p.what, err)
		}
		if _, err := m.ws.Write(p.b); err != nil {
			return fmt.Errorf("mux: patch the Matroska %s: %w", p.what, err)
		}
	}
	if _, err := m.ws.Seek(m.origin+m.segData+int64(m.pos), io.SeekStart); err != nil {
		return fmt.Errorf("mux: seek to the Matroska file's end: %w", err)
	}
	return nil
}
