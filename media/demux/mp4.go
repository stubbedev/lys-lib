package demux

import (
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"slices"
	"sort"
	"time"

	"github.com/stubbedev/lys-lib/intconv"
	"github.com/stubbedev/lys-lib/internal/isobmff"
)

// mp4TopLevel are box types an MP4 file starts with.
var mp4TopLevel = []string{"ftyp", "styp", "moov", "mdat", "free", "skip", "wide", "pnot", "uuid"}

func isMP4Box(typ []byte) bool { return slices.Contains(mp4TopLevel, string(typ)) }

// mp4Sample is one sample of the index: where its bytes are, when it
// decodes and presents in its track's ticks, and whether it is a sync
// sample.
type mp4Sample struct {
	off  int64
	dts  int64
	cts  int32
	size uint32
	dur  uint32
	key  bool
}

func (s *mp4Sample) pts() int64 { return s.dts + int64(s.cts) }

type mp4Defaults struct {
	dur, size, flags uint32
}

type mp4Track struct {
	info      Track
	id        uint32
	timescale uint64
	// shift is the edit list's media time, subtracted from every
	// timestamp, and delay its leading empty edit.
	shift   int64
	delay   time.Duration
	trex    mp4Defaults
	samples []mp4Sample
	nextDTS int64
	cursor  int
	stbl    []byte
}

func (t *mp4Track) time(ticks int64) time.Duration {
	return ticksToTime(ticks-t.shift, t.timescale) + t.delay
}

type mp4File struct {
	src      *source
	trs      []*mp4Track
	infos    []Track
	movieDur time.Duration
}

// box is an ISO BMFF box: its type and payload.
type box struct {
	typ     string
	payload []byte
}

// eachBox calls fn for each box in b, failing on a box whose size runs
// past b.
func eachBox(b []byte, fn func(box) error) error {
	for len(b) > 0 {
		if len(b) < 8 {
			return fmt.Errorf("%d bytes left, short of a box header: %w", len(b), ErrMalformed)
		}
		size, hdr := uint64(binary.BigEndian.Uint32(b)), 8
		typ := string(b[4:8])
		switch size {
		case 0:
			size = uint64(len(b))
		case 1:
			if len(b) < 16 {
				return fmt.Errorf("box %q: short of its 64-bit size: %w", typ, ErrMalformed)
			}
			size, hdr = binary.BigEndian.Uint64(b[8:]), 16
		}
		if size < uint64(hdr) || size > uint64(len(b)) {
			return fmt.Errorf("box %q of %d bytes in %d: %w", typ, size, len(b), ErrMalformed)
		}
		if err := fn(box{typ: typ, payload: b[hdr:size]}); err != nil {
			return err
		}
		b = b[size:]
	}
	return nil
}

// child is the payload of b's first child box of type typ, nil when
// there is none.
func child(b []byte, typ string) ([]byte, error) {
	var found []byte
	err := eachBox(b, func(x box) error {
		if found == nil && x.typ == typ {
			found = x.payload
		}
		return nil
	})
	return found, err
}

func openMP4(src *source) (*mp4File, error) {
	m := &mp4File{src: src}
	var moov []byte
	type moof struct {
		off int64
		b   []byte
	}
	var moofs []moof
	for off := int64(0); off < src.size; {
		h, err := src.upTo(off, 16)
		if err != nil {
			return nil, err
		}
		if len(h) < 8 {
			return nil, fmt.Errorf("%d bytes at offset %d, short of a box header: %w", len(h), off, ErrMalformed)
		}
		size, hdr := int64(binary.BigEndian.Uint32(h)), int64(8)
		typ := string(h[4:8])
		switch size {
		case 0:
			size = src.size - off
		case 1:
			if len(h) < 16 {
				return nil, fmt.Errorf("box %q at offset %d: short of its 64-bit size: %w", typ, off, ErrMalformed)
			}
			large := binary.BigEndian.Uint64(h[8:])
			if large > math.MaxInt64 {
				return nil, fmt.Errorf("box %q at offset %d of %d bytes: %w", typ, off, large, ErrMalformed)
			}
			size, hdr = int64(large), 16
		}
		if size < hdr || size > src.size-off {
			return nil, fmt.Errorf("box %q at offset %d of %d bytes in a %d-byte file: %w", typ, off, size, src.size, ErrMalformed)
		}
		switch typ {
		case "moov":
			if moov != nil {
				return nil, fmt.Errorf("second moov at offset %d: %w", off, ErrMalformed)
			}
			if moov, err = src.load(off+hdr, size-hdr, "moov"); err != nil {
				return nil, err
			}
		case "moof":
			b, err := src.load(off+hdr, size-hdr, "moof")
			if err != nil {
				return nil, err
			}
			moofs = append(moofs, moof{off: off, b: b})
		}
		off += size
	}
	if moov == nil {
		return nil, fmt.Errorf("no moov box: %w", ErrMalformed)
	}
	if err := m.parseMoov(moov); err != nil {
		return nil, err
	}
	for _, f := range moofs {
		if err := m.parseMoof(f.off, f.b); err != nil {
			return nil, fmt.Errorf("moof at offset %d: %w", f.off, err)
		}
	}
	m.infos = make([]Track, len(m.trs))
	for i, t := range m.trs {
		if t.info.Duration == 0 && len(t.samples) > 0 {
			last := &t.samples[len(t.samples)-1]
			t.info.Duration = t.time(last.pts() + int64(last.dur))
		}
		m.infos[i] = t.info
	}
	return m, nil
}

func (m *mp4File) parseMoov(moov []byte) error {
	var movieScale uint64
	err := eachBox(moov, func(x box) error {
		switch x.typ {
		case "mvhd":
			f := fields{b: x.payload}
			v := f.u8()
			f.skip(3)
			var dur uint64
			if v == 1 {
				f.skip(16)
				movieScale = uint64(f.u32())
				dur = f.u64()
			} else {
				f.skip(8)
				movieScale = uint64(f.u32())
				dur = uint64(f.u32())
			}
			if f.short || movieScale == 0 {
				return fmt.Errorf("mvhd: timescale %d: %w", movieScale, ErrMalformed)
			}
			if dur != math.MaxUint32 && dur != math.MaxUint64 {
				m.movieDur = ticksToTime(intconv.Clamp[int64](dur), movieScale)
			}
		case "trak":
			if movieScale == 0 {
				return fmt.Errorf("trak before mvhd: %w", ErrMalformed)
			}
			t, err := parseTrak(x.payload, movieScale)
			if err != nil {
				return fmt.Errorf("trak %d: %w", len(m.trs), err)
			}
			t.info.Index = len(m.trs)
			m.trs = append(m.trs, t)
		case "mvex":
			return eachBox(x.payload, func(y box) error {
				if y.typ != "trex" {
					return nil
				}
				f := fields{b: y.payload}
				f.skip(4)
				id := f.u32()
				f.skip(4)
				d := mp4Defaults{dur: f.u32(), size: f.u32(), flags: f.u32()}
				if f.short {
					return fmt.Errorf("trex: %w", ErrMalformed)
				}
				if t := m.track(id); t != nil {
					t.trex = d
				}
				return nil
			})
		}
		return nil
	})
	if err != nil {
		return err
	}
	if len(m.trs) == 0 {
		return fmt.Errorf("moov has no tracks: %w", ErrMalformed)
	}
	for _, t := range m.trs {
		if err := t.indexSampleTable(m.src.size); err != nil {
			return fmt.Errorf("track %d: %w", t.info.Index, err)
		}
	}
	return nil
}

func (m *mp4File) track(id uint32) *mp4Track {
	for _, t := range m.trs {
		if t.id == id {
			return t
		}
	}
	return nil
}

func parseTrak(trak []byte, movieScale uint64) (*mp4Track, error) {
	t := &mp4Track{}
	err := eachBox(trak, func(x box) error {
		switch x.typ {
		case "tkhd":
			f := fields{b: x.payload}
			v := f.u8()
			f.skip(3)
			if v == 1 {
				f.skip(16)
			} else {
				f.skip(8)
			}
			t.id = f.u32()
			if f.short {
				return fmt.Errorf("tkhd: %w", ErrMalformed)
			}
		case "edts":
			elst, err := child(x.payload, "elst")
			if err != nil || elst == nil {
				return err
			}
			return t.parseEditList(elst, movieScale)
		case "mdia":
			return t.parseMdia(x.payload)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if t.timescale == 0 {
		return nil, fmt.Errorf("no mdhd: %w", ErrMalformed)
	}
	return t, nil
}

func (t *mp4Track) parseEditList(elst []byte, movieScale uint64) error {
	f := fields{b: elst}
	v := f.u8()
	f.skip(3)
	n := f.u32()
	entry := 12
	if v == 1 {
		entry = 20
	}
	if f.short || uint64(n)*uint64(entry) > uint64(len(f.b)) {
		return fmt.Errorf("elst of %d entries in %d bytes: %w", n, len(f.b), ErrMalformed)
	}
	var empty int64
	for range n {
		var dur uint64
		var media int64
		if v == 1 {
			dur, media = f.u64(), intconv.Int64Bits(f.u64())
		} else {
			dur, media = uint64(f.u32()), int64(intconv.Int32Bits(f.u32()))
		}
		f.skip(4)
		if media == -1 {
			empty += intconv.Clamp[int64](dur)
			continue
		}
		t.shift = media
		break
	}
	t.delay = ticksToTime(empty, movieScale)
	return nil
}

func (t *mp4Track) parseMdia(mdia []byte) error {
	var handler string
	var minf []byte
	err := eachBox(mdia, func(x box) error {
		switch x.typ {
		case "mdhd":
			f := fields{b: x.payload}
			v := f.u8()
			f.skip(3)
			var dur uint64
			if v == 1 {
				f.skip(16)
				t.timescale = uint64(f.u32())
				dur = f.u64()
			} else {
				f.skip(8)
				t.timescale = uint64(f.u32())
				dur = uint64(f.u32())
			}
			if f.short || t.timescale == 0 {
				return fmt.Errorf("mdhd: timescale %d: %w", t.timescale, ErrMalformed)
			}
			if dur != 0 && dur != math.MaxUint32 && dur != math.MaxUint64 {
				t.info.Duration = ticksToTime(intconv.Clamp[int64](dur), t.timescale)
			}
		case "hdlr":
			f := fields{b: x.payload}
			f.skip(8)
			handler = string(f.take(4))
		case "minf":
			minf = x.payload
		}
		return nil
	})
	if err != nil {
		return err
	}
	switch handler {
	case "vide":
		t.info.Kind = Video
	case "soun":
		t.info.Kind = Audio
	case "subt", "text", "sbtl":
		t.info.Kind = Subtitle
	}
	if minf == nil {
		return fmt.Errorf("no minf: %w", ErrMalformed)
	}
	stbl, err := child(minf, "stbl")
	if err != nil {
		return err
	}
	if stbl == nil {
		return fmt.Errorf("no stbl: %w", ErrMalformed)
	}
	t.stbl = stbl
	stsd, err := child(stbl, "stsd")
	if err != nil {
		return err
	}
	if len(stsd) < 8 {
		return fmt.Errorf("stsd of %d bytes: %w", len(stsd), ErrMalformed)
	}
	if binary.BigEndian.Uint32(stsd[4:]) == 0 {
		return fmt.Errorf("stsd has no sample entry: %w", ErrMalformed)
	}
	return eachBox(stsd[8:], func(x box) error {
		if t.info.CodecName != "" {
			return nil
		}
		return t.parseSampleEntry(x)
	})
}

var mp4Codecs = map[string]Codec{
	"avc1": H264, "avc3": H264,
	"hvc1": H265, "hev1": H265,
	"av01": AV1,
	"vp08": VP8, "vp09": VP9,
	"Opus": Opus,
	"mp4a": AAC,
	"fLaC": FLAC,
	".mp3": MP3,
}

const (
	visualEntryBytes = 78
	audioEntryBytes  = 28
)

func (t *mp4Track) parseSampleEntry(x box) error {
	t.info.CodecName = x.typ
	t.info.Codec = mp4Codecs[x.typ]
	var kids []byte
	switch t.info.Kind {
	case Video:
		f := fields{b: x.payload}
		f.skip(24)
		t.info.Width, t.info.Height = int(f.u16()), int(f.u16())
		if f.short || len(x.payload) < visualEntryBytes {
			return fmt.Errorf("visual sample entry %q of %d bytes: %w", x.typ, len(x.payload), ErrMalformed)
		}
		kids = x.payload[visualEntryBytes:]
	case Audio:
		f := fields{b: x.payload}
		f.skip(8)
		version := f.u16()
		f.skip(6)
		t.info.Channels = int(f.u16())
		f.skip(6)
		t.info.SampleRate = int(f.u32() >> 16)
		extra := 0
		switch version {
		case 1:
			extra = 16
		case 2:
			extra = 36
		}
		if f.short || len(x.payload) < audioEntryBytes+extra {
			return fmt.Errorf("audio sample entry %q of %d bytes: %w", x.typ, len(x.payload), ErrMalformed)
		}
		kids = x.payload[audioEntryBytes+extra:]
		if t.info.SampleRate == 0 {
			t.info.SampleRate = int(min(t.timescale, math.MaxInt32))
		}
	default:
		return nil
	}
	return eachBox(kids, func(y box) error {
		switch {
		case y.typ == "avcC" && t.info.Codec == H264,
			y.typ == "hvcC" && t.info.Codec == H265,
			y.typ == "av1C" && t.info.Codec == AV1:
			t.info.Config = y.payload
		case y.typ == "vpcC" && t.info.Codec == VP9:
			t.info.Config = y.payload
		case y.typ == "dfLa" && t.info.Codec == FLAC:
			if len(y.payload) < 4 {
				return fmt.Errorf("dfLa of %d bytes: %w", len(y.payload), ErrMalformed)
			}
			t.info.Config = y.payload[4:]
		case y.typ == "dOps" && t.info.Codec == Opus:
			return t.parseDOps(y.payload)
		case y.typ == "esds" && t.info.Codec == AAC:
			return t.parseESDS(y.payload)
		}
		return nil
	})
}

const opusRate = 48000

// parseDOps turns an MP4 Opus specific box into the OpusHead Ogg and
// Matroska carry, so the decoder sees one layout.
func (t *mp4Track) parseDOps(b []byte) error {
	f := fields{b: b}
	if v := f.u8(); v != 0 {
		return fmt.Errorf("dOps version %d: %w", v, ErrMalformed)
	}
	channels := f.u8()
	preSkip := f.u16()
	rate := f.u32()
	gain := f.u16()
	family := f.u8()
	var table []byte
	if family != 0 {
		table = f.take(2 + int(channels))
	}
	if f.short {
		return fmt.Errorf("dOps of %d bytes: %w", len(b), ErrMalformed)
	}
	head := append([]byte("OpusHead"), 1, channels)
	head = binary.LittleEndian.AppendUint16(head, preSkip)
	head = binary.LittleEndian.AppendUint32(head, rate)
	head = binary.LittleEndian.AppendUint16(head, gain)
	head = append(head, family)
	t.info.Config = append(head, table...)
	t.info.Channels = int(channels)
	t.info.SampleRate = opusRate
	t.info.SeekPreRoll = opusPreRoll
	t.info.CodecDelay = ticksToTime(int64(preSkip), opusRate)
	return nil
}

const opusPreRoll = 80 * time.Millisecond

// descriptor reads an MPEG-4 descriptor's tag and payload (ISO/IEC
// 14496-1 8.3.3).
func descriptor(f *fields) (tag uint8, payload []byte) {
	tag = f.u8()
	n := 0
	for range 4 {
		c := f.u8()
		n = n<<7 | int(c&0x7f)
		if c&0x80 == 0 {
			break
		}
	}
	return tag, f.take(n)
}

const (
	tagES            = 3
	tagDecoderConfig = 4
	tagDecoderInfo   = 5
)

var mp4ObjectTypes = map[uint8]Codec{0x40: AAC, 0x66: AAC, 0x67: AAC, 0x68: AAC, 0x69: MP3, 0x6b: MP3, 0xdd: Vorbis}

func (t *mp4Track) parseESDS(b []byte) error {
	f := fields{b: b}
	f.skip(4)
	tag, es := descriptor(&f)
	if f.short || tag != tagES {
		return fmt.Errorf("esds: no ES descriptor: %w", ErrMalformed)
	}
	e := fields{b: es}
	e.skip(2)
	flags := e.u8()
	if flags&0x80 != 0 {
		e.skip(2)
	}
	if flags&0x40 != 0 {
		e.skip(int(e.u8()))
	}
	if flags&0x20 != 0 {
		e.skip(2)
	}
	tag, dc := descriptor(&e)
	if e.short || tag != tagDecoderConfig {
		return fmt.Errorf("esds: no decoder config descriptor: %w", ErrMalformed)
	}
	d := fields{b: dc}
	oti := d.u8()
	d.skip(12)
	if d.short {
		return fmt.Errorf("esds: decoder config of %d bytes: %w", len(dc), ErrMalformed)
	}
	codec, ok := mp4ObjectTypes[oti]
	if !ok {
		t.info.Codec = Unknown
		t.info.CodecName = fmt.Sprintf("mp4a.%02x", oti)
		return nil
	}
	t.info.Codec = codec
	if len(d.b) > 0 {
		if tag, info := descriptor(&d); !d.short && tag == tagDecoderInfo {
			t.info.Config = info
		}
	}
	return nil
}

// indexSampleTable builds the sample index from the stbl tables
// (ISO/IEC 14496-12 8.6, 8.7). A fragmented file's moov has empty
// tables, and its samples come from the moofs.
func (t *mp4Track) indexSampleTable(fileSize int64) error {
	tables := map[string][]byte{}
	if err := eachBox(t.stbl, func(x box) error {
		if _, dup := tables[x.typ]; !dup {
			tables[x.typ] = x.payload
		}
		return nil
	}); err != nil {
		return err
	}
	t.stbl = nil
	sizes, err := sampleSizes(tables, fileSize)
	if err != nil {
		return err
	}
	n := len(sizes.list)
	if !sizes.constant() {
		n = sizes.count
	}
	if n == 0 {
		return nil
	}
	chunks, err := chunkOffsets(tables)
	if err != nil {
		return err
	}
	t.samples = make([]mp4Sample, n)
	if err := t.placeSamples(tables["stsc"], chunks, sizes); err != nil {
		return err
	}
	if err := t.timeSamples(tables["stts"], tables["ctts"]); err != nil {
		return err
	}
	if err := t.markSync(tables["stss"]); err != nil {
		return err
	}
	last := &t.samples[n-1]
	t.nextDTS = last.dts + int64(last.dur)
	return nil
}

type stsz struct {
	fixed uint32
	count int
	list  []uint32
}

func (s stsz) constant() bool { return s.list == nil }

func (s stsz) size(i int) uint32 {
	if s.list == nil {
		return s.fixed
	}
	return s.list[i]
}

func sampleSizes(tables map[string][]byte, fileSize int64) (stsz, error) {
	if b, ok := tables["stsz"]; ok {
		f := fields{b: b}
		f.skip(4)
		fixed, n := f.u32(), f.u32()
		if f.short {
			return stsz{}, fmt.Errorf("stsz of %d bytes: %w", len(b), ErrMalformed)
		}
		if fixed != 0 {
			if uint64(n)*uint64(fixed) > intconv.Must[uint64](fileSize) {
				return stsz{}, fmt.Errorf("stsz: %d samples of %d bytes in a %d-byte file: %w", n, fixed, fileSize, ErrMalformed)
			}
			return stsz{fixed: fixed, count: int(n)}, nil
		}
		if uint64(n)*4 > uint64(len(f.b)) {
			return stsz{}, fmt.Errorf("stsz: %d sizes in %d bytes: %w", n, len(f.b), ErrMalformed)
		}
		list := make([]uint32, n)
		for i := range list {
			list[i] = f.u32()
		}
		return stsz{list: list, count: int(n)}, nil
	}
	b, ok := tables["stz2"]
	if !ok {
		return stsz{}, fmt.Errorf("no stsz or stz2: %w", ErrMalformed)
	}
	f := fields{b: b}
	f.skip(7)
	width, n := f.u8(), f.u32()
	if f.short || (width != 4 && width != 8 && width != 16) {
		return stsz{}, fmt.Errorf("stz2 field size %d: %w", width, ErrMalformed)
	}
	if uint64(n)*uint64(width) > 8*uint64(len(f.b)) {
		return stsz{}, fmt.Errorf("stz2: %d sizes in %d bytes: %w", n, len(f.b), ErrMalformed)
	}
	list := make([]uint32, n)
	for i := range list {
		switch width {
		case 4:
			v := f.b[i/2]
			if i%2 == 0 {
				list[i] = uint32(v >> 4)
			} else {
				list[i] = uint32(v & 0xf)
			}
		case 8:
			list[i] = uint32(f.b[i])
		case 16:
			list[i] = uint32(binary.BigEndian.Uint16(f.b[2*i:]))
		}
	}
	return stsz{list: list, count: int(n)}, nil
}

func chunkOffsets(tables map[string][]byte) ([]int64, error) {
	b, wide := tables["co64"]
	if !wide {
		var ok bool
		if b, ok = tables["stco"]; !ok {
			return nil, fmt.Errorf("no stco or co64: %w", ErrMalformed)
		}
	}
	f := fields{b: b}
	f.skip(4)
	n := f.u32()
	entry := uint64(4)
	if wide {
		entry = 8
	}
	if f.short || uint64(n)*entry > uint64(len(f.b)) {
		return nil, fmt.Errorf("chunk offset table: %d entries in %d bytes: %w", n, len(f.b), ErrMalformed)
	}
	offs := make([]int64, n)
	for i := range offs {
		if wide {
			v := f.u64()
			if v > math.MaxInt64 {
				return nil, fmt.Errorf("co64: chunk %d at offset %d: %w", i, v, ErrMalformed)
			}
			offs[i] = int64(v)
		} else {
			offs[i] = int64(f.u32())
		}
	}
	return offs, nil
}

func (t *mp4Track) placeSamples(stsc []byte, chunks []int64, sizes stsz) error {
	f := fields{b: stsc}
	f.skip(4)
	n := f.u32()
	if f.short || uint64(n)*12 > uint64(len(f.b)) || n == 0 {
		return fmt.Errorf("stsc: %d entries in %d bytes: %w", n, len(f.b), ErrMalformed)
	}
	type run struct{ first, perChunk uint32 }
	runs := make([]run, n)
	for i := range runs {
		runs[i] = run{first: f.u32(), perChunk: f.u32()}
		f.skip(4)
		if runs[i].first == 0 || (i > 0 && runs[i].first <= runs[i-1].first) {
			return fmt.Errorf("stsc entry %d starts at chunk %d: %w", i, runs[i].first, ErrMalformed)
		}
	}
	s := 0
	for r, cur := range runs {
		end := uint64(len(chunks))
		if r+1 < len(runs) {
			end = min(end, uint64(runs[r+1].first-1))
		}
		for c := uint64(cur.first - 1); c < end; c++ {
			off := chunks[c]
			for range cur.perChunk {
				if s == len(t.samples) {
					return fmt.Errorf("stsc places more samples than the %d stsz sizes: %w", len(t.samples), ErrMalformed)
				}
				size := sizes.size(s)
				t.samples[s].off, t.samples[s].size = off, size
				off += int64(size)
				s++
			}
		}
	}
	if s != len(t.samples) {
		return fmt.Errorf("stsc places %d of the %d samples: %w", s, len(t.samples), ErrMalformed)
	}
	return nil
}

func (t *mp4Track) timeSamples(stts, ctts []byte) error {
	f := fields{b: stts}
	f.skip(4)
	n := f.u32()
	if f.short || uint64(n)*8 > uint64(len(f.b)) {
		return fmt.Errorf("stts: %d entries in %d bytes: %w", n, len(f.b), ErrMalformed)
	}
	s, dts := 0, int64(0)
	for range n {
		count, delta := f.u32(), f.u32()
		for range count {
			if s == len(t.samples) {
				return fmt.Errorf("stts times more than the %d samples: %w", len(t.samples), ErrMalformed)
			}
			t.samples[s].dts, t.samples[s].dur = dts, delta
			dts += int64(delta)
			s++
		}
	}
	if s != len(t.samples) {
		return fmt.Errorf("stts times %d of the %d samples: %w", s, len(t.samples), ErrMalformed)
	}
	if ctts == nil {
		return nil
	}
	f = fields{b: ctts}
	f.skip(4)
	n = f.u32()
	if f.short || uint64(n)*8 > uint64(len(f.b)) {
		return fmt.Errorf("ctts: %d entries in %d bytes: %w", n, len(f.b), ErrMalformed)
	}
	s = 0
	for range n {
		count, off := f.u32(), intconv.Int32Bits(f.u32())
		for range count {
			if s == len(t.samples) {
				return fmt.Errorf("ctts offsets more than the %d samples: %w", len(t.samples), ErrMalformed)
			}
			t.samples[s].cts = off
			s++
		}
	}
	return nil
}

func (t *mp4Track) markSync(stss []byte) error {
	if stss == nil {
		for i := range t.samples {
			t.samples[i].key = true
		}
		return nil
	}
	f := fields{b: stss}
	f.skip(4)
	n := f.u32()
	if f.short || uint64(n)*4 > uint64(len(f.b)) {
		return fmt.Errorf("stss: %d entries in %d bytes: %w", n, len(f.b), ErrMalformed)
	}
	for range n {
		i := f.u32()
		if i == 0 || uint64(i) > uint64(len(t.samples)) {
			return fmt.Errorf("stss names sample %d of %d: %w", i, len(t.samples), ErrMalformed)
		}
		t.samples[i-1].key = true
	}
	return nil
}

// parseMoof adds a movie fragment's samples to the index (ISO/IEC
// 14496-12 8.8).
func (m *mp4File) parseMoof(moofOff int64, moof []byte) error {
	dataEnd := moofOff
	return eachBox(moof, func(x box) error {
		if x.typ != "traf" {
			return nil
		}
		var err error
		dataEnd, err = m.parseTraf(moofOff, dataEnd, x.payload)
		return err
	})
}

func (m *mp4File) parseTraf(moofOff, prevEnd int64, traf []byte) (int64, error) {
	tfhd, err := child(traf, "tfhd")
	if err != nil {
		return 0, err
	}
	f := fields{b: tfhd}
	flags := f.u32() & 0xffffff
	id := f.u32()
	t := m.track(id)
	if f.short || t == nil {
		return 0, fmt.Errorf("tfhd names track %d: %w", id, ErrMalformed)
	}
	base := prevEnd
	if flags&isobmff.TfhdDefaultBaseIsMoof != 0 {
		base = moofOff
	}
	if flags&isobmff.TfhdBaseDataOffset != 0 {
		v := f.u64()
		if v > math.MaxInt64 {
			return 0, fmt.Errorf("tfhd base data offset %d: %w", v, ErrMalformed)
		}
		base = int64(v)
	}
	if flags&isobmff.TfhdSampleDescription != 0 {
		f.skip(4)
	}
	d := t.trex
	if flags&isobmff.TfhdDefaultSampleDuration != 0 {
		d.dur = f.u32()
	}
	if flags&isobmff.TfhdDefaultSampleSize != 0 {
		d.size = f.u32()
	}
	if flags&isobmff.TfhdDefaultSampleFlags != 0 {
		d.flags = f.u32()
	}
	if f.short {
		return 0, fmt.Errorf("tfhd of %d bytes: %w", len(tfhd), ErrMalformed)
	}
	end := base
	err = eachBox(traf, func(x box) error {
		switch x.typ {
		case "tfdt":
			g := fields{b: x.payload}
			v := g.u8()
			g.skip(3)
			var dts uint64
			if v == 1 {
				dts = g.u64()
			} else {
				dts = uint64(g.u32())
			}
			if g.short || dts > math.MaxInt64 {
				return fmt.Errorf("tfdt: %w", ErrMalformed)
			}
			t.nextDTS = int64(dts)
		case "trun":
			var err error
			end, err = t.parseTrun(x.payload, base, d, m.src.size)
			return err
		}
		return nil
	})
	return end, err
}

func (t *mp4Track) parseTrun(b []byte, base int64, d mp4Defaults, fileSize int64) (int64, error) {
	f := fields{b: b}
	vf := f.u32()
	version, flags := vf>>24, vf&0xffffff
	n := f.u32()
	off := base
	if flags&isobmff.TrunDataOffset != 0 {
		off += int64(intconv.Int32Bits(f.u32()))
	}
	firstFlags, hasFirst := d.flags, flags&isobmff.TrunFirstSampleFlags != 0
	if hasFirst {
		firstFlags = f.u32()
	}
	if f.short {
		return 0, fmt.Errorf("trun of %d bytes: %w", len(b), ErrMalformed)
	}
	per := 0
	for _, bit := range []uint32{isobmff.TrunSampleDuration, isobmff.TrunSampleSize, isobmff.TrunSampleFlags, isobmff.TrunSampleCTSOffset} {
		if flags&bit != 0 {
			per += 4
		}
	}
	switch {
	case per > 0 && uint64(n)*uint64(per) > uint64(len(f.b)):
		return 0, fmt.Errorf("trun: %d samples of %d bytes in %d: %w", n, per, len(f.b), ErrMalformed)
	case flags&isobmff.TrunSampleSize == 0 && (d.size == 0 || uint64(n)*uint64(d.size) > intconv.Must[uint64](fileSize)):
		return 0, fmt.Errorf("trun: %d samples of the default %d bytes: %w", n, d.size, ErrMalformed)
	}
	t.samples = growSamples(t.samples, int(n))
	for i := range n {
		s := mp4Sample{off: off, dts: t.nextDTS, dur: d.dur, size: d.size}
		sflags := d.flags
		if i == 0 && hasFirst {
			sflags = firstFlags
		}
		if flags&isobmff.TrunSampleDuration != 0 {
			s.dur = f.u32()
		}
		if flags&isobmff.TrunSampleSize != 0 {
			s.size = f.u32()
		}
		if flags&isobmff.TrunSampleFlags != 0 {
			sflags = f.u32()
		}
		if flags&isobmff.TrunSampleCTSOffset != 0 {
			c := f.u32()
			if version == 0 && c > math.MaxInt32 {
				return 0, fmt.Errorf("trun: sample %d composition offset %d: %w", i, c, ErrMalformed)
			}
			s.cts = intconv.Int32Bits(c)
		}
		s.key = sflags&isobmff.SampleNonSync == 0
		if s.off < 0 || s.off > fileSize {
			return 0, fmt.Errorf("trun: sample %d at offset %d: %w", i, s.off, ErrMalformed)
		}
		t.samples = append(t.samples, s)
		off += int64(s.size)
		t.nextDTS += int64(s.dur)
	}
	return off, nil
}

func growSamples(s []mp4Sample, n int) []mp4Sample {
	if cap(s)-len(s) >= n {
		return s
	}
	grown := make([]mp4Sample, len(s), max(2*cap(s), len(s)+n))
	copy(grown, s)
	return grown
}

func (m *mp4File) tracks() []Track { return m.infos }

func (m *mp4File) duration() time.Duration {
	if m.movieDur > 0 {
		return m.movieDur
	}
	var d time.Duration
	for _, t := range m.infos {
		d = max(d, t.Duration)
	}
	return d
}

func (m *mp4File) next(selected []bool, buf []byte) (Packet, error) {
	var t *mp4Track
	for i, c := range m.trs {
		if !selected[i] || c.cursor >= len(c.samples) {
			continue
		}
		if t == nil || c.samples[c.cursor].off < t.samples[t.cursor].off {
			t = c
		}
	}
	if t == nil {
		return Packet{}, io.EOF
	}
	s := &t.samples[t.cursor]
	t.cursor++
	if s.size > maxPacketBytes {
		return Packet{}, fmt.Errorf("track %d sample %d of %d bytes is past the %d-byte bound: %w", t.info.Index, t.cursor-1, s.size, maxPacketBytes, ErrMalformed)
	}
	data, err := m.src.appendAt(buf, s.off, int64(s.size))
	if err != nil {
		return Packet{}, fmt.Errorf("track %d sample %d: %w", t.info.Index, t.cursor-1, err)
	}
	return Packet{
		Track: t.info.Index, PTS: t.time(s.pts()),
		Duration: ticksToTime(int64(s.dur), t.timescale), Key: s.key, Data: data,
	}, nil
}

func (m *mp4File) seek(track int, at time.Duration, selected []bool) (time.Duration, error) {
	t := m.trs[track]
	if len(t.samples) == 0 {
		return 0, fmt.Errorf("track %d has no samples: %w", track, io.EOF)
	}
	key := t.keyAtOrBefore(at)
	if key < 0 {
		return 0, fmt.Errorf("track %d has no sync sample: %w", track, ErrMalformed)
	}
	t.cursor = key
	landed := t.time(t.samples[key].pts())
	for i, o := range m.trs {
		if i == track || !selected[i] {
			continue
		}
		from := landed - o.info.SeekPreRoll
		o.cursor = sort.Search(len(o.samples), func(j int) bool {
			return o.time(o.samples[j].pts()) >= from
		})
	}
	return landed, nil
}

// keyAtOrBefore is the index of the sync sample presented last at or
// before at, else the first sync sample; -1 when there is none.
func (t *mp4Track) keyAtOrBefore(at time.Duration) int {
	end := sort.Search(len(t.samples), func(j int) bool { return t.time(t.samples[j].dts) > at })
	for i := end - 1; i >= 0; i-- {
		s := &t.samples[i]
		if s.key && t.time(s.pts()) <= at {
			return i
		}
	}
	for i := range t.samples {
		if t.samples[i].key {
			return i
		}
	}
	return -1
}
