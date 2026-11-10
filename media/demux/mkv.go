package demux

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/stubbedev/lys-lib/intconv"
	"github.com/stubbedev/lys-lib/internal/matroska"
	"github.com/stubbedev/lys-lib/media/internal/mp4audio"
)

const (
	unknownSize      = -1
	maxEBMLHeader    = 4 << 10
	defaultTickScale = 1000000
	laceHeaderBytes  = 4 << 10
)

// mkvLevel1 are the Segment's children: an unknown-size Cluster ends
// at the next of them.
var mkvLevel1 = map[matroska.ID]bool{
	matroska.SeekHead: true, matroska.Info: true, matroska.Tracks: true, matroska.Cues: true,
	matroska.Cluster: true, matroska.Chapters: true, matroska.Tags: true, matroska.Attachments: true,
}

// element is an EBML element in the file: its ID, where its header
// starts, where its payload starts and how long it is (unknownSize
// when the header leaves it open).
type element struct {
	id   matroska.ID
	off  int64
	data int64
	size int64
}

func (e element) end() int64 { return e.data + e.size }

// vint reads an EBML variable-length integer, its length marker
// stripped; all ones is unknownSize.
func vint(b []byte) (v int64, n int, err error) {
	if len(b) == 0 || b[0] == 0 {
		return 0, 0, fmt.Errorf("invalid EBML size: %w", ErrMalformed)
	}
	n = 9 - bitsLen8(b[0])
	if n > len(b) {
		return 0, 0, fmt.Errorf("EBML size of %d bytes in %d: %w", n, len(b), ErrMalformed)
	}
	u := uint64(b[0]) & (0xff >> n)
	for _, c := range b[1:n] {
		u = u<<8 | uint64(c)
	}
	if u == 1<<(7*n)-1 {
		return unknownSize, n, nil
	}
	return intconv.Must[int64](u), n, nil
}

func bitsLen8(c byte) int {
	n := 0
	for ; c != 0; c >>= 1 {
		n++
	}
	return n
}

func readID(b []byte) (matroska.ID, int, error) {
	if len(b) == 0 || b[0] < 0x10 {
		return 0, 0, fmt.Errorf("invalid EBML ID: %w", ErrMalformed)
	}
	n := 9 - bitsLen8(b[0])
	if n > len(b) {
		return 0, 0, fmt.Errorf("EBML ID of %d bytes in %d: %w", n, len(b), ErrMalformed)
	}
	var id uint32
	for _, c := range b[:n] {
		id = id<<8 | uint32(c)
	}
	return matroska.ID(id), n, nil
}

// parseHeader reads the element header at the start of b, which is at
// off in the file.
func parseHeader(b []byte, off int64) (element, error) {
	id, n, err := readID(b)
	if err != nil {
		return element{}, fmt.Errorf("element at offset %d: %w", off, err)
	}
	size, m, err := vint(b[n:])
	if err != nil {
		return element{}, fmt.Errorf("element %x at offset %d: %w", uint32(id), off, err)
	}
	return element{id: id, off: off, data: off + int64(n+m), size: size}, nil
}

// eachElement calls fn for each element in a master element's payload
// held in memory.
func eachElement(b []byte, fn func(id matroska.ID, payload []byte) error) error {
	for off := 0; off < len(b); {
		e, err := parseHeader(b[off:], int64(off))
		if err != nil {
			return err
		}
		if e.size == unknownSize || e.size > int64(len(b))-e.data {
			return fmt.Errorf("element %x of %d bytes at %d in %d: %w", uint32(e.id), e.size, off, len(b), ErrMalformed)
		}
		if err := fn(e.id, b[e.data:e.end()]); err != nil {
			return err
		}
		off = int(e.end())
	}
	return nil
}

func ebmlUint(b []byte) (uint64, error) {
	if len(b) > 8 {
		return 0, fmt.Errorf("unsigned integer of %d bytes: %w", len(b), ErrMalformed)
	}
	var v uint64
	for _, c := range b {
		v = v<<8 | uint64(c)
	}
	return v, nil
}

func ebmlFloat(b []byte) (float64, error) {
	switch len(b) {
	case 0:
		return 0, nil
	case 4:
		return float64(math.Float32frombits(binary.BigEndian.Uint32(b))), nil
	case 8:
		return math.Float64frombits(binary.BigEndian.Uint64(b)), nil
	}
	return 0, fmt.Errorf("float of %d bytes: %w", len(b), ErrMalformed)
}

// mkvCue is a keyframe the file can seek to: its block timestamp in
// nanoseconds and its cluster's position in the file.
type mkvCue struct {
	time    time.Duration
	cluster int64
}

type mkvTrack struct {
	info       Track
	number     uint64
	defaultDur time.Duration
	strip      []byte
	inflate    bool
	cues       []mkvCue
	scanned    bool
}

// mkvBlock is a block's header: its track, timestamp, key flag,
// duration and where each of its frames is.
type mkvBlock struct {
	track  *mkvTrack
	time   time.Duration
	dur    time.Duration
	key    bool
	frames []mkvFrame
}

type mkvFrame struct{ off, size int64 }

// blockKind is how a block says whether it is a keyframe: a SimpleBlock
// by its flag, a Block by the BlockGroup having no ReferenceBlock.
type blockKind uint8

const (
	blockSimple blockKind = iota
	blockIntra
	blockDelta
)

// mkvCursor is a position in the Segment's clusters.
type mkvCursor struct {
	pos       int64
	cluster   bool
	clusterAt int64
	end       int64
	clusterTS int64
	hasTS     bool
}

type mkvFile struct {
	src          *source
	segData      int64
	segEnd       int64
	scale        uint64
	dur          time.Duration
	trs          []*mkvTrack
	infos        []Track
	firstCluster int64

	cur       mkvCursor
	blk       mkvBlock
	frame     int
	seekTrack *mkvTrack
	waitKey   bool
	floor     time.Duration
}

func (m *mkvFile) header(off int64) (element, error) {
	b, err := m.src.upTo(off, 12)
	if err != nil {
		return element{}, err
	}
	return parseHeader(b, off)
}

func openMKV(src *source) (*mkvFile, bool, error) {
	m := &mkvFile{src: src, scale: defaultTickScale}
	head, err := m.header(0)
	if err != nil {
		return nil, false, err
	}
	if head.size == unknownSize || head.size > maxEBMLHeader {
		return nil, false, fmt.Errorf("EBML header of %d bytes: %w", head.size, ErrMalformed)
	}
	hb, err := src.load(head.data, head.size, "EBML header")
	if err != nil {
		return nil, false, err
	}
	docType := "matroska"
	if err := eachElement(hb, func(id matroska.ID, p []byte) error {
		if id == matroska.DocType {
			docType = string(bytes.TrimRight(p, "\x00"))
		}
		return nil
	}); err != nil {
		return nil, false, fmt.Errorf("EBML header: %w", err)
	}
	if docType != "matroska" && docType != "webm" {
		return nil, false, fmt.Errorf("EBML document type %q: %w", docType, ErrUnknownFormat)
	}
	seg, err := m.header(head.end())
	if err != nil {
		return nil, false, err
	}
	if seg.id != matroska.Segment {
		return nil, false, fmt.Errorf("element %x at offset %d where the Segment belongs: %w", uint32(seg.id), seg.off, ErrMalformed)
	}
	m.segData, m.segEnd = seg.data, src.size
	if seg.size != unknownSize {
		m.segEnd = min(seg.end(), src.size)
	}
	if err := m.readMetadata(); err != nil {
		return nil, false, err
	}
	m.cur = mkvCursor{pos: m.firstCluster}
	return m, docType == "webm", nil
}

func (m *mkvFile) readMetadata() error {
	seeks := map[matroska.ID]int64{}
	var info, tracks, cues bool
	m.firstCluster = m.segEnd
	parse := func(e element) error {
		if e.size == unknownSize {
			return fmt.Errorf("element %x at offset %d of unknown size: %w", uint32(e.id), e.off, ErrMalformed)
		}
		b, err := m.src.load(e.data, e.size, fmt.Sprintf("element %x at offset %d", uint32(e.id), e.off))
		if err != nil {
			return err
		}
		switch e.id {
		case matroska.SeekHead:
			return parseSeekHead(b, m.segData, seeks)
		case matroska.Info:
			info = true
			return m.parseInfo(b)
		case matroska.Tracks:
			tracks = true
			return m.parseTracks(b)
		case matroska.Cues:
			cues = true
			return m.parseCues(b)
		}
		return nil
	}
	for off := m.segData; off < m.segEnd; {
		e, err := m.header(off)
		if err != nil {
			return err
		}
		if e.id == matroska.Cluster {
			m.firstCluster = off
			break
		}
		if e.size == unknownSize {
			return fmt.Errorf("element %x at offset %d of unknown size: %w", uint32(e.id), off, ErrMalformed)
		}
		switch e.id {
		case matroska.SeekHead, matroska.Info, matroska.Tracks, matroska.Cues:
			if err := parse(e); err != nil {
				return err
			}
		}
		off = e.end()
	}
	for _, want := range []struct {
		id   matroska.ID
		done *bool
	}{{matroska.Info, &info}, {matroska.Tracks, &tracks}, {matroska.Cues, &cues}} {
		pos, ok := seeks[want.id]
		if *want.done || !ok {
			continue
		}
		e, err := m.header(pos)
		if err != nil {
			return fmt.Errorf("SeekHead entry for %x: %w", uint32(want.id), err)
		}
		if e.id != want.id {
			return fmt.Errorf("SeekHead entry for %x points at element %x at offset %d: %w", uint32(want.id), uint32(e.id), pos, ErrMalformed)
		}
		if err := parse(e); err != nil {
			return err
		}
	}
	if !tracks {
		return fmt.Errorf("no Tracks element: %w", ErrMalformed)
	}
	m.infos = make([]Track, len(m.trs))
	for i, t := range m.trs {
		sortCues(t.cues)
		t.info.Duration = m.dur
		m.infos[i] = t.info
	}
	return nil
}

func parseSeekHead(b []byte, segData int64, seeks map[matroska.ID]int64) error {
	return eachElement(b, func(id matroska.ID, p []byte) error {
		if id != matroska.Seek {
			return nil
		}
		var target matroska.ID
		var pos uint64
		var hasPos bool
		err := eachElement(p, func(id matroska.ID, v []byte) error {
			var err error
			switch id {
			case matroska.SeekID:
				var u uint64
				u, err = ebmlUint(v)
				target = matroska.ID(intconv.Clamp[uint32](u))
			case matroska.SeekPosition:
				pos, err = ebmlUint(v)
				hasPos = true
			}
			return err
		})
		if err != nil {
			return fmt.Errorf("SeekHead: %w", err)
		}
		if p, err := intconv.Exact[int64](pos); hasPos && err == nil && p <= math.MaxInt64-segData {
			seeks[target] = segData + p
		}
		return nil
	})
}

func (m *mkvFile) parseInfo(b []byte) error {
	var dur float64
	err := eachElement(b, func(id matroska.ID, p []byte) error {
		var err error
		switch id {
		case matroska.TimestampScale:
			m.scale, err = ebmlUint(p)
		case matroska.Duration:
			dur, err = ebmlFloat(p)
		}
		return err
	})
	if err != nil {
		return fmt.Errorf("info: %w", err)
	}
	if m.scale == 0 || m.scale > uint64(time.Second) {
		return fmt.Errorf("info: timestamp scale %d ns: %w", m.scale, ErrMalformed)
	}
	if ns := dur * float64(m.scale); ns > 0 && ns < math.MaxInt64 {
		m.dur = time.Duration(ns)
	}
	return nil
}

var mkvCodecs = map[string]Codec{
	matroska.CodecH264:   H264,
	matroska.CodecH265:   H265,
	matroska.CodecAV1:    AV1,
	matroska.CodecVP8:    VP8,
	matroska.CodecVP9:    VP9,
	matroska.CodecOpus:   Opus,
	matroska.CodecVorbis: Vorbis,
	matroska.CodecFLAC:   FLAC,
	matroska.CodecMP3:    MP3,
}

func mkvCodec(id string) Codec {
	if c, ok := mkvCodecs[id]; ok {
		return c
	}
	if strings.HasPrefix(id, matroska.CodecAAC) {
		return AAC
	}
	return Unknown
}

func (m *mkvFile) parseTracks(b []byte) error {
	return eachElement(b, func(id matroska.ID, p []byte) error {
		if id != matroska.TrackEntry {
			return nil
		}
		t, err := m.parseTrackEntry(p)
		if err != nil {
			return fmt.Errorf("TrackEntry %d: %w", len(m.trs), err)
		}
		for _, o := range m.trs {
			if o.number == t.number {
				return fmt.Errorf("TrackEntry %d repeats track number %d: %w", len(m.trs), t.number, ErrMalformed)
			}
		}
		t.info.Index = len(m.trs)
		m.trs = append(m.trs, t)
		return nil
	})
}

func (m *mkvFile) parseTrackEntry(b []byte) (*mkvTrack, error) {
	t := &mkvTrack{}
	t.info.SampleRate, t.info.Channels = 8000, 1
	var kind uint64
	var encodings []byte
	err := eachElement(b, func(id matroska.ID, p []byte) error {
		var err error
		var u uint64
		switch id {
		case matroska.TrackNumber:
			t.number, err = ebmlUint(p)
		case matroska.TrackType:
			kind, err = ebmlUint(p)
		case matroska.CodecID:
			t.info.CodecName = string(bytes.TrimRight(p, "\x00"))
		case matroska.CodecPrivate:
			t.info.Config = p
		case matroska.DefaultDuration:
			u, err = ebmlUint(p)
			t.defaultDur = time.Duration(intconv.Clamp[int64](u))
		case matroska.CodecDelay:
			u, err = ebmlUint(p)
			t.info.CodecDelay = time.Duration(intconv.Clamp[int64](u))
		case matroska.SeekPreRoll:
			u, err = ebmlUint(p)
			t.info.SeekPreRoll = time.Duration(intconv.Clamp[int64](u))
		case matroska.Video:
			err = eachElement(p, func(id matroska.ID, v []byte) error {
				var err error
				var u uint64
				switch id {
				case matroska.PixelWidth:
					u, err = ebmlUint(v)
					t.info.Width = intconv.Clamp[int](u)
				case matroska.PixelHeight:
					u, err = ebmlUint(v)
					t.info.Height = intconv.Clamp[int](u)
				}
				return err
			})
		case matroska.Audio:
			err = eachElement(p, func(id matroska.ID, v []byte) error {
				var err error
				switch id {
				case matroska.SamplingFrequency:
					var f float64
					f, err = ebmlFloat(v)
					if f >= 1 && f <= math.MaxInt32 {
						t.info.SampleRate = int(f)
					}
				case matroska.Channels:
					var u uint64
					u, err = ebmlUint(v)
					t.info.Channels = intconv.Clamp[int](u)
				}
				return err
			})
		case matroska.ContentEncodings:
			encodings = p
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	if t.number == 0 {
		return nil, fmt.Errorf("no track number: %w", ErrMalformed)
	}
	switch kind {
	case matroska.TrackVideo:
		t.info.Kind = Video
	case matroska.TrackAudio:
		t.info.Kind = Audio
	case matroska.TrackSubtitle:
		t.info.Kind = Subtitle
	}
	t.info.Codec = mkvCodec(t.info.CodecName)
	if encodings != nil {
		if err := t.parseEncodings(encodings); err != nil {
			return nil, err
		}
	}
	if t.info.Codec == AAC && len(t.info.Config) == 0 {
		t.info.Config = legacyAACConfig(t.info.CodecName, t.info.SampleRate, t.info.Channels)
	}
	return t, nil
}

var legacyAACObjectTypes = map[string]uint64{
	"MAIN":   1,
	"LC":     2,
	"LC/SBR": 2,
	"SSR":    3,
	"LTP":    4,
}

func legacyAACConfig(codecID string, rate, channels int) []byte {
	profile, ok := strings.CutPrefix(codecID, matroska.CodecAAC+"/MPEG4/")
	if !ok {
		profile, ok = strings.CutPrefix(codecID, matroska.CodecAAC+"/MPEG2/")
	}
	objectType, known := legacyAACObjectTypes[profile]
	if !ok || !known {
		return nil
	}
	var channelConfig uint64
	switch {
	case channels >= 1 && channels <= 6:
		channelConfig = uint64(channels)
	case channels == 8:
		channelConfig = 7
	}
	v, n := objectType, 5
	if i, ok := mp4audio.SampleRateIndex(rate); ok {
		v, n = v<<4|intconv.Must[uint64](i), n+4
	} else {
		v, n = v<<28|mp4audio.ExplicitRate<<24|intconv.Clamp[uint64](rate)&0xffffff, n+28
	}
	v, n = v<<7|channelConfig<<3, n+7
	return binary.BigEndian.AppendUint64(nil, v)[8-n/8:]
}

const (
	compZlib  = 0
	compStrip = 3
	scopeData = 1
	scopePriv = 2
)

func (t *mkvTrack) parseEncodings(b []byte) error {
	return eachElement(b, func(id matroska.ID, p []byte) error {
		if id != matroska.ContentEncoding {
			return nil
		}
		scope, kind, algo := uint64(scopeData), uint64(0), uint64(compZlib)
		var settings []byte
		encrypted := false
		err := eachElement(p, func(id matroska.ID, v []byte) error {
			var err error
			switch id {
			case matroska.ContentEncodingScope:
				scope, err = ebmlUint(v)
			case matroska.ContentEncodingType:
				kind, err = ebmlUint(v)
			case matroska.ContentEncryption:
				encrypted = true
			case matroska.ContentCompression:
				err = eachElement(v, func(id matroska.ID, c []byte) error {
					var err error
					switch id {
					case matroska.ContentCompAlgo:
						algo, err = ebmlUint(c)
					case matroska.ContentCompSettings:
						settings = c
					}
					return err
				})
			}
			return err
		})
		if err != nil {
			return fmt.Errorf("ContentEncoding: %w", err)
		}
		switch {
		case encrypted || kind != 0:
			t.unsupported("encrypted")
		case algo == compStrip:
			if scope&scopeData != 0 {
				t.strip = settings
			}
			if scope&scopePriv != 0 {
				t.info.Config = append(append([]byte(nil), settings...), t.info.Config...)
			}
		case algo == compZlib:
			t.inflate = t.inflate || scope&scopeData != 0
			if scope&scopePriv != 0 {
				priv, err := inflateAll(t.info.Config)
				if err != nil {
					return fmt.Errorf("CodecPrivate: %w", err)
				}
				t.info.Config = priv
			}
		default:
			t.unsupported(fmt.Sprintf("compression algorithm %d", algo))
		}
		return nil
	})
}

func (t *mkvTrack) unsupported(why string) {
	t.info.Codec = Unknown
	t.info.CodecName = fmt.Sprintf("%s (%s)", t.info.CodecName, why)
}

func inflateAll(b []byte) ([]byte, error) {
	r, err := zlib.NewReader(bytes.NewReader(b))
	if err != nil {
		return nil, fmt.Errorf("zlib: %w", err)
	}
	defer r.Close()
	out, err := io.ReadAll(io.LimitReader(r, maxPacketBytes+1))
	if err != nil {
		return nil, fmt.Errorf("zlib: %w", err)
	}
	if len(out) > maxPacketBytes {
		return nil, fmt.Errorf("zlib data inflates past %d bytes: %w", maxPacketBytes, ErrMalformed)
	}
	return out, nil
}

func (m *mkvFile) trackByNumber(n uint64) *mkvTrack {
	for _, t := range m.trs {
		if t.number == n {
			return t
		}
	}
	return nil
}

func (m *mkvFile) parseCues(b []byte) error {
	return eachElement(b, func(id matroska.ID, p []byte) error {
		if id != matroska.CuePoint {
			return nil
		}
		var ticks uint64
		type pos struct{ track, cluster uint64 }
		var poss []pos
		err := eachElement(p, func(id matroska.ID, v []byte) error {
			var err error
			switch id {
			case matroska.CueTime:
				ticks, err = ebmlUint(v)
			case matroska.CueTrackPositions:
				var ps pos
				err = eachElement(v, func(id matroska.ID, w []byte) error {
					var err error
					switch id {
					case matroska.CueTrack:
						ps.track, err = ebmlUint(w)
					case matroska.CueClusterPosition:
						ps.cluster, err = ebmlUint(w)
					}
					return err
				})
				poss = append(poss, ps)
			}
			return err
		})
		if err != nil {
			return fmt.Errorf("cues: %w", err)
		}
		at := m.blockTime(intconv.Clamp[int64](ticks))
		for _, ps := range poss {
			t := m.trackByNumber(ps.track)
			pos, err := intconv.Exact[int64](ps.cluster)
			if t == nil || err != nil || pos > m.segEnd-m.segData {
				continue
			}
			t.cues = append(t.cues, mkvCue{time: at - t.info.CodecDelay, cluster: m.segData + pos})
		}
		return nil
	})
}

func (m *mkvFile) blockTime(ticks int64) time.Duration {
	return ticksToTime(ticks, uint64(time.Second)/m.scale)
}

func (m *mkvFile) tracks() []Track { return m.infos }

func (m *mkvFile) duration() time.Duration { return m.dur }

// nextBlock reads the next block header after c, with its frames'
// positions when frames is set; io.EOF after the last cluster.
func (m *mkvFile) nextBlock(c *mkvCursor, b *mkvBlock, frames bool) error {
	for {
		if c.pos >= m.segEnd {
			return io.EOF
		}
		if c.cluster && c.end != unknownSize && c.pos >= c.end {
			c.cluster = false
		}
		e, err := m.header(c.pos)
		if err != nil {
			return err
		}
		if c.cluster && c.end == unknownSize && mkvLevel1[e.id] {
			c.cluster = false
		}
		if !c.cluster {
			if e.id == matroska.Cluster {
				c.cluster, c.clusterAt, c.hasTS, c.pos, c.end = true, e.off, false, e.data, unknownSize
				if e.size != unknownSize {
					c.end = min(e.end(), m.segEnd)
				}
				continue
			}
			if e.size == unknownSize {
				return fmt.Errorf("element %x at offset %d of unknown size: %w", uint32(e.id), e.off, ErrMalformed)
			}
			c.pos = e.end()
			continue
		}
		if e.size == unknownSize || (c.end != unknownSize && e.end() > c.end) {
			return fmt.Errorf("cluster child %x at offset %d of %d bytes: %w", uint32(e.id), e.off, e.size, ErrMalformed)
		}
		c.pos = e.end()
		switch e.id {
		case matroska.Timestamp:
			v, err := m.src.view(e.data, int(min(e.size, 9)))
			if err != nil {
				return err
			}
			u, err := ebmlUint(v)
			if err != nil {
				return fmt.Errorf("cluster timestamp at offset %d: %w", e.off, err)
			}
			c.clusterTS, c.hasTS = intconv.Clamp[int64](u), true
		case matroska.SimpleBlock:
			ok, err := m.parseBlock(c, e.data, e.size, blockSimple, b, frames)
			if err != nil || ok {
				return err
			}
		case matroska.BlockGroup:
			ok, err := m.parseGroup(c, e, b, frames)
			if err != nil || ok {
				return err
			}
		}
	}
}

func (m *mkvFile) parseGroup(c *mkvCursor, g element, b *mkvBlock, frames bool) (bool, error) {
	var block element
	var dur uint64
	hasDur, ref := false, false
	for off := g.data; off < g.end(); {
		e, err := m.header(off)
		if err != nil {
			return false, err
		}
		if e.size == unknownSize || e.end() > g.end() {
			return false, fmt.Errorf("BlockGroup child %x at offset %d: %w", uint32(e.id), off, ErrMalformed)
		}
		switch e.id {
		case matroska.Block:
			block = e
		case matroska.ReferenceBlock:
			ref = true
		case matroska.BlockDuration:
			v, err := m.src.view(e.data, int(min(e.size, 9)))
			if err != nil {
				return false, err
			}
			if dur, err = ebmlUint(v); err != nil {
				return false, fmt.Errorf("BlockDuration at offset %d: %w", e.off, err)
			}
			hasDur = true
		}
		off = e.end()
	}
	if block.id != matroska.Block {
		return false, nil
	}
	kind := blockDelta
	if !ref {
		kind = blockIntra
	}
	ok, err := m.parseBlock(c, block.data, block.size, kind, b, frames)
	if ok && hasDur {
		b.dur = m.blockTime(intconv.Clamp[int64](dur))
	}
	return ok, err
}

// parseBlock reads the block header at off; it reports false for a
// block of a track the file does not declare.
func (m *mkvFile) parseBlock(c *mkvCursor, off, size int64, kind blockKind, b *mkvBlock, frames bool) (bool, error) {
	if !c.hasTS {
		return false, fmt.Errorf("block at offset %d before its cluster's timestamp: %w", off, ErrMalformed)
	}
	h, err := m.src.view(off, int(min(size, laceHeaderBytes)))
	if err != nil {
		return false, err
	}
	num, n, err := vint(h)
	if err != nil || len(h) < n+3 {
		return false, fmt.Errorf("block header at offset %d: %w", off, errors.Join(err, ErrMalformed))
	}
	t := m.trackByNumber(uint64(max(num, 0)))
	if t == nil {
		return false, nil
	}
	rel := int64(binary.BigEndian.Uint16(h[n:]))
	if rel >= 1<<15 {
		rel -= 1 << 16
	}
	flags := h[n+2]
	b.track, b.dur, b.frames = t, t.defaultDur, b.frames[:0]
	b.time = m.blockTime(c.clusterTS+rel) - t.info.CodecDelay
	b.key = kind == blockIntra || kind == blockSimple && flags&matroska.FlagKeyframe != 0
	if !frames {
		return true, nil
	}
	hdr := n + 3
	body := size - int64(hdr)
	if flags&matroska.FlagLacingMask == 0 {
		b.frames = append(b.frames, mkvFrame{off: off + int64(hdr), size: body})
		return true, nil
	}
	if err := b.lace(h[hdr:], flags, off+int64(hdr), body); err != nil {
		return false, fmt.Errorf("block at offset %d: %w", off, err)
	}
	return true, nil
}

const (
	laceXiph  = 0x02
	laceFixed = 0x04
	laceEBML  = 0x06
)

// lace splits a laced block's body (off, size; h holds its start)
// into frames (Matroska 10.3).
func (b *mkvBlock) lace(h []byte, flags byte, off, size int64) error {
	if len(h) == 0 {
		return fmt.Errorf("laced block without a frame count: %w", ErrMalformed)
	}
	count := int(h[0]) + 1
	p := 1
	sizes := make([]int64, 0, count)
	switch flags & matroska.FlagLacingMask {
	case laceXiph:
		for range count - 1 {
			var s int64
			for {
				if p >= len(h) {
					return fmt.Errorf("xiph lace sizes run past the header: %w", ErrMalformed)
				}
				c := h[p]
				p++
				s += int64(c)
				if c != 0xff {
					break
				}
			}
			sizes = append(sizes, s)
		}
	case laceEBML:
		if count == 1 {
			break
		}
		first, n, err := vint(h[p:])
		if err != nil || first == unknownSize {
			return fmt.Errorf("EBML lace size: %w", errors.Join(err, ErrMalformed))
		}
		p += n
		sizes = append(sizes, first)
		prev := first
		for range count - 2 {
			raw, n, err := vint(h[p:])
			if err != nil || raw == unknownSize {
				return fmt.Errorf("EBML lace size: %w", errors.Join(err, ErrMalformed))
			}
			p += n
			s := prev + raw - (int64(1)<<(7*n-1) - 1)
			sizes = append(sizes, s)
			prev = s
		}
	case laceFixed:
		body := size - int64(p)
		if body%int64(count) != 0 {
			return fmt.Errorf("%d bytes do not split into %d equal frames: %w", body, count, ErrMalformed)
		}
		for range count - 1 {
			sizes = append(sizes, body/int64(count))
		}
	}
	at := off + int64(p)
	left := size - int64(p)
	for _, s := range sizes {
		if s < 0 || s > left {
			return fmt.Errorf("laced frame of %d bytes with %d left: %w", s, left, ErrMalformed)
		}
		b.frames = append(b.frames, mkvFrame{off: at, size: s})
		at += s
		left -= s
	}
	b.frames = append(b.frames, mkvFrame{off: at, size: left})
	return nil
}

func (m *mkvFile) next(selected []bool, buf []byte) (Packet, error) {
	for {
		if m.frame >= len(m.blk.frames) {
			if err := m.nextBlock(&m.cur, &m.blk, true); err != nil {
				return Packet{}, err
			}
			m.frame = 0
			continue
		}
		i := m.frame
		m.frame++
		t := m.blk.track
		if !selected[t.info.Index] {
			m.frame = len(m.blk.frames)
			continue
		}
		pts := m.blk.time + time.Duration(i)*t.defaultDur
		if !m.wanted(t, pts) {
			continue
		}
		fr := m.blk.frames[i]
		data, err := m.frameData(t, fr, buf)
		if err != nil {
			return Packet{}, fmt.Errorf("track %d block at offset %d: %w", t.info.Index, fr.off, err)
		}
		return Packet{Track: t.info.Index, PTS: pts, Duration: m.blk.dur, Key: m.blk.key, Data: data}, nil
	}
}

// wanted drops what a seek skips: the seek track's packets until its
// keyframe, the other tracks' presented before that keyframe less
// their pre-roll.
func (m *mkvFile) wanted(t *mkvTrack, pts time.Duration) bool {
	if m.seekTrack == nil {
		return true
	}
	if t != m.seekTrack {
		return pts >= m.floor-t.info.SeekPreRoll
	}
	if !m.waitKey {
		return true
	}
	if !m.blk.key || pts < m.floor {
		return false
	}
	m.waitKey = false
	return true
}

func (m *mkvFile) frameData(t *mkvTrack, fr mkvFrame, buf []byte) ([]byte, error) {
	if fr.size+int64(len(t.strip)) > maxPacketBytes {
		return nil, fmt.Errorf("frame of %d bytes is past the %d-byte bound: %w", fr.size, maxPacketBytes, ErrMalformed)
	}
	if !t.inflate {
		return m.src.appendAt(append(buf, t.strip...), fr.off, fr.size)
	}
	raw, err := m.src.appendAt(nil, fr.off, fr.size)
	if err != nil {
		return nil, err
	}
	out, err := inflateAll(raw)
	if err != nil {
		return nil, err
	}
	return append(append(buf, t.strip...), out...), nil
}

func (m *mkvFile) seek(track int, at time.Duration, selected []bool) (time.Duration, error) {
	t := m.trs[track]
	if len(t.cues) == 0 && !t.scanned {
		if err := m.scanClusters(t); err != nil {
			return 0, err
		}
	}
	if len(t.cues) == 0 {
		return 0, fmt.Errorf("track %d has no keyframe: %w", track, io.EOF)
	}
	cue := t.cues[cueAtOrBefore(t.cues, at)]
	var preRoll time.Duration
	for i, o := range m.trs {
		if selected[i] && o != t {
			preRoll = max(preRoll, o.info.SeekPreRoll)
		}
	}
	m.cur = mkvCursor{pos: t.cues[cueAtOrBefore(t.cues, cue.time-preRoll)].cluster}
	m.blk.frames, m.frame = m.blk.frames[:0], 0
	m.seekTrack, m.waitKey, m.floor = t, true, cue.time
	return cue.time, nil
}

// scanClusters builds t's seek index from the clusters themselves,
// for a file without Cues for it: every keyframe block of t, read
// once on the first seek.
func (m *mkvFile) scanClusters(t *mkvTrack) error {
	c := mkvCursor{pos: m.firstCluster}
	var b mkvBlock
	for {
		err := m.nextBlock(&c, &b, false)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("scan the clusters for keyframes: %w", err)
		}
		if b.track == t && b.key {
			t.cues = append(t.cues, mkvCue{time: b.time, cluster: c.clusterAt})
		}
	}
	sortCues(t.cues)
	t.scanned = true
	return nil
}

// cueAtOrBefore is the index of the last cue at or before at, else the
// first.
func cueAtOrBefore(cues []mkvCue, at time.Duration) int {
	return max(sort.Search(len(cues), func(j int) bool { return cues[j].time > at })-1, 0)
}

func sortCues(cues []mkvCue) {
	sort.SliceStable(cues, func(i, j int) bool { return cues[i].time < cues[j].time })
}
