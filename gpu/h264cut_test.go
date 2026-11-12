package gpu

import (
	"fmt"
	"math/bits"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/stubbedev/lys-lib/internal/nal"
	"github.com/stubbedev/lys-lib/media/demux"
)

// No encoder ffmpeg has codes H.264 field pictures: x264's interlaced
// coding is frames of field and frame macroblock pairs. The field
// pictures the tests decode are made of the pictures of streams x264
// coded progressive at half the height, their slice headers rewritten:
// a field picture's slice data is a frame's of the field's size, and
// with CAVLC it reads the same, so each such picture decodes, as the
// field it is made, to a picture no encoder meant (its coefficients
// scanned as a field's, its references whatever the lists of a field
// hold) but that the standard defines all the same, which ffmpeg
// decodes as the reference.

// h264Source is a stream x264 coded, cut into its pictures.
type h264Source struct {
	sps, pps []byte
	seq      nal.H264SPS
	pic      nal.H264PPS
	spsByID  [32]*nal.H264SPS
	ppsByID  [256]*nal.H264PPS
	pics     []h264SourcePic
}

// h264SourcePic is a picture of a source: its slices, whether it is an
// IDR picture and a reference, and its place in output order among the
// pictures since the IDR picture before it.
type h264SourcePic struct {
	slices  [][]byte
	idr     bool
	ref     bool
	display int
}

// x264Source is the test pattern at w x h coded by x264 with args, with
// CAVLC, one slice a picture, no weighted prediction and no reordered
// reference lists: what cutting its pictures into another stream takes.
func x264Source(t *testing.T, w, h int, secs float64, args ...string) *h264Source {
	t.Helper()
	path := filepath.Join(t.TempDir(), "source.h264")
	pattern := fmt.Sprintf("testsrc2=size=%dx%d:rate=25:duration=%g", w, h, secs)
	enc := []string{"-v", "error", "-y", "-f", "lavfi", "-i", pattern, "-pix_fmt", "yuv420p", "-c:v", "libx264", "-profile:v", "main", "-coder", "0", "-weightp", "0"}
	runFFmpeg(t, append(append(enc, args...), path)...)
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := &h264Source{}
	var prevMsb, prevLsb int32
	for _, u := range annexBUnits(b) {
		switch nal.H264Type(u) {
		case nal.H264NALSPS:
			if s.sps != nil {
				continue
			}
			s.sps = u
			if err := s.seq.Parse(nal.Unescape(nil, u[1:])); err != nil {
				t.Fatal(err)
			}
			s.spsByID[s.seq.ID] = &s.seq
		case nal.H264NALPPS:
			if s.pps != nil {
				continue
			}
			s.pps = u
			if err := s.pic.Parse(nal.Unescape(nil, u[1:]), &s.spsByID); err != nil {
				t.Fatal(err)
			}
			s.ppsByID[s.pic.ID] = &s.pic
		case nal.H264NALSlice, nal.H264NALIDR:
			var h nal.H264SliceHeader
			if err := h.Parse(nal.Unescape(nil, u[1:]), u[0], &s.ppsByID, &s.spsByID); err != nil {
				t.Fatal(err)
			}
			if h.FirstMbInSlice != 0 {
				s.pics[len(s.pics)-1].slices = append(s.pics[len(s.pics)-1].slices, u)
				continue
			}
			p := h264SourcePic{slices: [][]byte{u}, idr: nal.H264Type(u) == nal.H264NALIDR, ref: nal.H264RefIdc(u) != 0}
			switch {
			case p.idr:
				prevMsb, prevLsb = 0, 0
			case s.seq.PicOrderCntType == 2:
				p.display = s.pics[len(s.pics)-1].display + 1
			}
			if s.seq.PicOrderCntType == 0 {
				maxLsb, lsb, msb := int32(1)<<s.seq.Log2MaxPicOrderCntLsb, int32(h.PicOrderCntLsb), prevMsb
				switch {
				case lsb < prevLsb && prevLsb-lsb >= maxLsb/2:
					msb += maxLsb
				case lsb > prevLsb && lsb-prevLsb > maxLsb/2:
					msb -= maxLsb
				}
				p.display = int(msb + lsb)
				if p.ref {
					prevMsb, prevLsb = msb, lsb
				}
			}
			s.pics = append(s.pics, p)
		}
	}
	switch {
	case s.sps == nil || s.pps == nil || len(s.pics) == 0:
		t.Fatal("x264 wrote no parameter sets or no pictures")
	case s.pic.EntropyCodingMode || s.pic.WeightedPred || s.pic.WeightedBipredIdc == 1 || s.pic.RedundantPicCntPresent || s.seq.PicOrderCntType == 1:
		t.Fatalf("x264 wrote CABAC, weighted prediction tables, redundant pictures or picture order count type 1: PPS %+v", s.pic)
	}
	return s
}

// copyBits copies r's bits from where it is up to bit end into w.
func copyBits(w *nal.Writer, r *nal.Reader, end int) {
	for r.Pos() < end {
		n := min(end-r.Pos(), 32)
		w.U(uint(n), r.U(n))
	}
}

// stopBit is where rbsp's trailing bits start: its last set bit.
func stopBit(t *testing.T, rbsp []byte) int {
	t.Helper()
	for i, b := range slices.Backward(rbsp) {
		if b != 0 {
			return 8*i + 7 - bits.TrailingZeros8(b)
		}
	}
	t.Fatal("an RBSP without its stop bit")
	return 0
}

// h264Main is the Main profile's profile_idc; h264Baseline the Baseline
// profile's, which x264 names a stream that uses nothing past it.
const (
	h264Baseline = 66
	h264Main     = 77
)

// fieldSPS is sps, a Main or Baseline profile SPS of frames, as the
// Main profile SPS of a sequence of twice the height whose pictures
// are frames or fields (frame_mbs_only_flag 0), without
// macroblock-adaptive frames: its map units, the macroblock rows of a
// field, are the rows it counted. With gaps it allows gaps in
// frame_num.
func fieldSPS(t *testing.T, sps []byte, gaps bool) []byte {
	t.Helper()
	rbsp := nal.Unescape(nil, sps[1:])
	r := nal.NewReader(rbsp)
	if profile := r.U(8); profile != h264Main && profile != h264Baseline {
		t.Fatalf("SPS of profile_idc %d, want Main or Baseline", profile)
	}
	r.Skip(16)
	r.UE()
	r.UE()
	switch r.UE() {
	case 0:
		r.UE()
	case 2:
	default:
		t.Fatal("SPS of pic_order_cnt_type 1")
	}
	r.UE()
	gapsAt := r.Pos()
	r.Skip(1)
	r.UE()
	r.UE()
	at := r.Pos()
	if frames, direct8x8 := r.Flag(), r.Flag(); !frames || !direct8x8 {
		t.Fatal("the SPS codes fields already, or infers no 8x8 direct blocks, as one that codes fields must")
	}
	var w nal.Writer
	r = nal.NewReader(rbsp)
	r.Skip(8)
	w.U(8, h264Main)
	copyBits(&w, &r, gapsAt)
	r.Skip(1)
	w.Flag(gaps)
	copyBits(&w, &r, at)
	r.Skip(1)
	w.U(2, 0)
	copyBits(&w, &r, stopBit(t, rbsp))
	w.Trailing()
	return w.Unit(sps[0])
}

// ppsWithQP is pps, a PPS of one slice group, with qp added to its
// pic_init_qp.
func ppsWithQP(t *testing.T, pps []byte, qp int32) []byte {
	t.Helper()
	rbsp := nal.Unescape(nil, pps[1:])
	r := nal.NewReader(rbsp)
	var w nal.Writer
	w.UE(r.UE())
	w.UE(r.UE())
	w.U(2, r.U(2))
	if groups := r.UE(); groups != 0 {
		t.Fatalf("a PPS of %d slice groups", groups+1)
	}
	w.UE(0)
	w.UE(r.UE())
	w.UE(r.UE())
	w.U(3, r.U(3))
	w.SE(r.SE() + qp)
	copyBits(&w, &r, stopBit(t, rbsp))
	w.Trailing()
	return w.Unit(pps[0])
}

// extendedSPS is sps, a Main or Baseline profile SPS, naming the
// Extended profile and saying the stream obeys Main's constraints
// (constraint_set1_flag), which a stream that uses none of the Extended
// profile's own tools may.
func extendedSPS(sps []byte) []byte {
	out := slices.Clone(sps)
	out[1], out[2] = h264Extended, out[2]|0x40
	return out
}

// cutPicture is a picture of a stream being cut together: picture
// index of src, as fields says a frame or a field, the second field of
// the frame the picture before it started when second. It is a
// reference when ref, with the memory management control operations
// mmco if any, else marked by the sliding window. order is its picture
// order count since the IDR picture before it, a frame's top field's,
// with the bottom field bottom after it. pps, if any, is a PPS sent
// before the picture, of the stream's ID, whose pic_init_qp is qp more
// than the stream's first: the picture's slices code as much less.
type cutPicture struct {
	src    *h264Source
	index  int
	fields h264Fields
	second bool
	ref    bool
	order  int32
	bottom int32
	mmco   []nal.H264MMCO
	pps    []byte
	qp     int32
}

// h264Cut is a stream cut together of the pictures of sources: its
// parameter sets and its pictures, each its NAL units, with what each
// was made as.
type h264Cut struct {
	t        *testing.T
	sps, pps []byte
	seq      nal.H264SPS
	pic      nal.H264PPS
	units    [][][]byte
	made     []cutPicture
	// frameNum is the frame_num of the frame being cut, prevRef the one
	// of the reference picture before it; frames counts the frames
	// since the IDR picture before, the one being cut too.
	frameNum, prevRef uint32
	frames            int32
}

// newH264Cut starts a stream of the parameter sets sps and pps.
func newH264Cut(t *testing.T, sps, pps []byte) *h264Cut {
	t.Helper()
	c := &h264Cut{t: t, sps: sps, pps: pps}
	if err := c.seq.Parse(nal.Unescape(nil, sps[1:])); err != nil {
		t.Fatal(err)
	}
	var sets [32]*nal.H264SPS
	sets[c.seq.ID] = &c.seq
	if err := c.pic.Parse(nal.Unescape(nil, pps[1:]), &sets); err != nil {
		t.Fatal(err)
	}
	return c
}

// add cuts p in after the pictures there are: its slices with their
// headers rewritten for what p says it is, the frame_num that follows
// from the pictures before it and its picture order count; the rest of
// each slice, from its quantizer on, is the source's bit for bit.
func (c *h264Cut) add(p cutPicture) {
	c.t.Helper()
	src := &p.src.pics[p.index]
	if theirs, ours := p.src.pic, c.pic; theirs != ours {
		theirs.BottomFieldPicOrderInFramePresent = ours.BottomFieldPicOrderInFramePresent
		if theirs != ours {
			c.t.Fatalf("the source's PPS %+v codes its slices otherwise than the stream's %+v", p.src.pic, c.pic)
		}
	}
	switch {
	case p.second:
	case src.idr:
		c.frameNum = 0
	default:
		c.frameNum = (c.prevRef + 1) % (1 << c.seq.Log2MaxFrameNum)
	}
	if p.ref {
		c.prevRef = c.frameNum
	}
	var units [][]byte
	if p.pps != nil {
		units = append(units, p.pps)
	}
	for _, u := range src.slices {
		units = append(units, c.slice(u, src, &p))
	}
	c.units, c.made = append(c.units, units), append(c.made, p)
}

// slice is unit, a slice of src, with its header rewritten for p.
func (c *h264Cut) slice(unit []byte, src *h264SourcePic, p *cutPicture) []byte {
	c.t.Helper()
	from, to := &p.src.seq, &c.seq
	rbsp := nal.Unescape(nil, unit[1:])
	r := nal.NewReader(rbsp)
	var w nal.Writer
	w.UE(r.UE())
	kind := r.UE()
	w.UE(kind)
	kind %= 5
	w.UE(r.UE())
	r.Skip(int(from.Log2MaxFrameNum))
	w.U(uint(to.Log2MaxFrameNum), c.frameNum)
	if !from.FrameMbsOnly && r.Flag() {
		c.t.Fatal("the source's picture is a field already")
	}
	if to.FrameMbsOnly && p.fields != h264Frame {
		c.t.Fatal("a field in a sequence of frames")
	}
	if !to.FrameMbsOnly {
		w.Flag(p.fields != h264Frame)
		if p.fields != h264Frame {
			w.Flag(p.fields == h264Bottom)
		}
	}
	if src.idr {
		w.UE(r.UE())
	}
	if from.PicOrderCntType == 0 {
		r.Skip(int(from.Log2MaxPicOrderCntLsb))
		if p.src.pic.BottomFieldPicOrderInFramePresent {
			r.SE()
		}
	}
	if to.PicOrderCntType == 0 {
		w.U(uint(to.Log2MaxPicOrderCntLsb), uint32(p.order)&(1<<to.Log2MaxPicOrderCntLsb-1))
		if c.pic.BottomFieldPicOrderInFramePresent && p.fields == h264Frame {
			w.SE(p.bottom)
		}
	}
	if kind == nal.H264SliceB {
		w.Flag(r.Flag())
	}
	if kind == nal.H264SliceP || kind == nal.H264SliceB {
		override := r.Flag()
		w.Flag(override)
		if override {
			w.UE(r.UE())
			if kind == nal.H264SliceB {
				w.UE(r.UE())
			}
		}
		for range 1 + b2i(kind == nal.H264SliceB) {
			if r.Flag() {
				c.t.Fatal("the source reorders a reference list, by picture numbers that are other pictures' in another stream")
			}
			w.Flag(false)
		}
	}
	if src.ref {
		switch {
		case src.idr:
			w.U(2, r.U(2))
		case r.Flag():
			c.t.Fatal("the source marks its reference pictures by memory management control operations, which name other pictures in another stream")
		}
	}
	switch {
	case !p.ref && src.idr, !p.ref && len(p.mmco) > 0, src.idr && len(p.mmco) > 0:
		c.t.Fatal("an IDR picture that is no reference or has memory management control operations, or such operations in a picture that is no reference")
	case p.ref && !src.idr:
		w.Flag(len(p.mmco) > 0)
		for _, m := range p.mmco {
			w.UE(uint32(m.Op))
			if m.Op == 1 || m.Op == 3 {
				w.UE(m.DifferenceOfPicNumsMinus1)
			}
			if m.Op == 2 {
				w.UE(m.LongTermPicNum)
			}
			if m.Op == 3 || m.Op == 6 {
				w.UE(m.LongTermFrameIdx)
			}
			if m.Op == 4 {
				w.UE(m.MaxLongTermFrameIdxPlus1)
			}
		}
		if len(p.mmco) > 0 {
			w.UE(0)
		}
	}
	if p.qp != 0 {
		w.SE(r.SE() - p.qp)
	}
	copyBits(&w, &r, stopBit(c.t, rbsp))
	w.Trailing()
	header := unit[0] & 0x1f
	if p.ref {
		header |= 1 << 5
	}
	return w.Unit(header)
}

// annexB is the stream as an Annex B byte stream in a file, for ffmpeg
// to decode.
func (c *h264Cut) annexB() string {
	c.t.Helper()
	units := [][]byte{c.sps, c.pps}
	for _, p := range c.units {
		units = append(units, p...)
	}
	path := filepath.Join(c.t.TempDir(), "cut.h264")
	if err := os.WriteFile(path, nal.AnnexB(units...), 0o600); err != nil {
		c.t.Fatal(err)
	}
	return path
}

// track is the stream's track: its parameter sets as an avcC with NAL
// unit lengths of four bytes.
func (c *h264Cut) track() demux.Track {
	config := []byte{1, c.sps[1], c.sps[2], c.sps[3], 0xff, 0xe1, byte(len(c.sps) >> 8), byte(len(c.sps))}
	config = append(config, c.sps...)
	config = append(config, 1, byte(len(c.pps)>>8), byte(len(c.pps)))
	config = append(config, c.pps...)
	return demux.Track{Kind: demux.Video, Codec: demux.H264, Config: config}
}

// fieldPeriod is the time between two fields, half a frame's.
const fieldPeriod = frameApart / 2

// packets are the stream's packets, a frame's two fields in one packet
// as MP4 holds them, or with apart each in its own as a transport
// stream does. A packet presents where its picture order count puts it
// among the pictures since the IDR picture before it.
func (c *h264Cut) packets(apart bool) []demux.Packet {
	var pkts []demux.Packet
	var base, next time.Duration
	for i, units := range c.units {
		p := &c.made[i]
		idr := p.src.pics[p.index].idr && !p.second
		if idr {
			base = next
		}
		var data []byte
		for _, u := range units {
			data = append(data, byte(len(u)>>24), byte(len(u)>>16), byte(len(u)>>8), byte(len(u)))
			data = append(data, u...)
		}
		pts := base + time.Duration(p.order)*fieldPeriod
		next = max(next, pts+frameApart)
		if p.second && !apart {
			last := &pkts[len(pkts)-1]
			last.Data = append(last.Data, data...)
			continue
		}
		pkts = append(pkts, demux.Packet{PTS: pts, Duration: fieldPeriod * time.Duration(1+b2i(p.fields == h264Frame || !apart)), Key: idr, Data: data})
	}
	return pkts
}

// reference is the frames ffmpeg decodes from the stream, w x h, which
// it is not to conceal anything in: an error fails the test.
func (c *h264Cut) reference(w, h int) stream {
	c.t.Helper()
	return ffmpegDecoded(c.t, c.annexB(), w, h, nv12Samples, "-xerror", "-err_detect", "explode")
}

// fieldPairs cuts src's pictures in as fields, two that follow each
// other a frame's, the first the field first says: both references
// where either was, in the output order of the earlier of the two. A
// picture before an IDR picture that has no other to pair with stays a
// field without its frame's other.
func (c *h264Cut) fieldPairs(src *h264Source, first h264Fields) {
	c.t.Helper()
	type unit struct {
		a, b, key, rank int
	}
	var units []unit
	for i := 0; i < len(src.pics); i++ {
		u := unit{a: i, b: -1, key: src.pics[i].display}
		if i+1 < len(src.pics) && !src.pics[i+1].idr {
			u.b, u.key = i+1, min(u.key, src.pics[i+1].display)
			i++
		}
		units = append(units, u)
	}
	for i := range units {
		for j := i - 1; j >= 0 && !src.pics[units[j+1].a].idr; j-- {
			if units[j].key < units[i].key {
				units[i].rank++
			} else {
				units[j].rank++
			}
		}
	}
	for _, u := range units {
		p := cutPicture{src: src, index: u.a, fields: first, ref: src.pics[u.a].ref, order: int32(2 * u.rank)}
		if u.b < 0 {
			c.add(p)
			continue
		}
		p.ref = p.ref || src.pics[u.b].ref
		c.add(p)
		c.add(cutPicture{src: src, index: u.b, fields: h264Frame &^ first, second: true, ref: p.ref, order: p.order + 1})
	}
}

// field cuts picture index of src in as the field fields says, the
// second of the frame the field before it started when second, with
// the memory management control operations mmco if any: a reference
// where the source's picture is, in output order where it is cut in.
func (c *h264Cut) field(src *h264Source, index int, fields h264Fields, second bool, mmco ...nal.H264MMCO) {
	c.t.Helper()
	c.picture(cutPicture{src: src, index: index, fields: fields, second: second, ref: src.pics[index].ref, mmco: mmco})
}

// frame cuts picture index of src in as a frame, its bottom field a
// field period after its top field, with the memory management control
// operations mmco if any.
func (c *h264Cut) frame(src *h264Source, index int, mmco ...nal.H264MMCO) {
	c.t.Helper()
	c.picture(cutPicture{src: src, index: index, fields: h264Frame, ref: src.pics[index].ref, bottom: 1, mmco: mmco})
}

// picture cuts p in, in output order where it is cut in.
func (c *h264Cut) picture(p cutPicture) {
	c.t.Helper()
	switch {
	case p.second:
	case p.src.pics[p.index].idr:
		c.frames = 1
	default:
		c.frames++
	}
	p.order = 2*(c.frames-1) + int32(b2i(p.second))
	c.add(p)
}
