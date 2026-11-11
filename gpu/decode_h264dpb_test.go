package gpu

import (
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/stubbedev/lys-lib/internal/nal"
)

// dpbRun decodes pictures through a h264State as the decoder does;
// inferred are the frames gaps in frame_num inferred.
type dpbRun struct {
	t        *testing.T
	sps      nal.H264SPS
	state    h264State
	inferred []h264Pic
}

func newDPBRun(t *testing.T, sps nal.H264SPS) *dpbRun {
	r := &dpbRun{t: t, sps: sps}
	r.state.reset()
	r.state.slots = max(int(sps.MaxNumRefFrames), 1) + 1
	return r
}

// pic decodes the picture h describes, returning it as decoded.
func (r *dpbRun) pic(h nal.H264SliceHeader, idr, ref, intra bool) (h264Pic, error) {
	r.t.Helper()
	p := h264Pic{hdr: &h, sps: &r.sps, idr: idr, ref: ref, intra: intra, fields: h264FieldsOf(&h)}
	gap, err := r.state.begin(&p)
	if err != nil {
		return p, err
	}
	for range gap {
		var q h264Pic
		if err := r.state.infer(&q, &r.sps); err != nil {
			return p, err
		}
		r.state.finish(&q)
		q.hdr = &nal.H264SliceHeader{FrameNum: q.hdr.FrameNum}
		r.inferred = append(r.inferred, q)
	}
	if err := r.state.place(&p); err != nil {
		return p, err
	}
	r.state.finish(&p)
	return p, nil
}

func (r *dpbRun) must(h nal.H264SliceHeader, idr, ref bool) h264Pic {
	r.t.Helper()
	p, err := r.pic(h, idr, ref, idr)
	if err != nil {
		r.t.Fatal(err)
	}
	return p
}

// refs are the reference frames by FrameNum, long-term ones by
// LongTermFrameIdx as negative numbers less one.
func (r *dpbRun) refs() []int {
	var out []int
	for i := range r.state.slots {
		switch ref := r.state.refs[i]; {
		case ref.short != 0:
			out = append(out, int(ref.frameNum))
		case ref.long != 0:
			out = append(out, -1-int(ref.longIdx))
		}
	}
	slices.Sort(out)
	return out
}

// Picture order count type 0 counts on past the wrap of
// pic_order_cnt_lsb, from the previous reference picture's.
func TestH264PictureOrderType0(t *testing.T) {
	r := newDPBRun(t, nal.H264SPS{Log2MaxFrameNum: 4, Log2MaxPicOrderCntLsb: 4, MaxNumRefFrames: 4})
	var got []int32
	for i, lsb := range []uint32{0, 6, 12, 2, 8, 14, 4} {
		p := r.must(nal.H264SliceHeader{FrameNum: uint32(i), PicOrderCntLsb: lsb}, i == 0, true)
		got = append(got, p.poc[0])
	}
	if want := []int32{0, 6, 12, 18, 24, 30, 36}; !slices.Equal(got, want) {
		t.Errorf("POC %v, want %v", got, want)
	}
	p := r.must(nal.H264SliceHeader{FrameNum: 7, PicOrderCntLsb: 2, DeltaPicOrderCntBottom: 1}, false, false)
	if p.poc != [2]int32{34, 35} {
		t.Errorf("POC %v, want [34 35], before the last reference picture's", p.poc)
	}
}

// Picture order count type 1 follows the expected deltas of its cycle,
// offset for non-reference pictures (8.2.1.2).
func TestH264PictureOrderType1(t *testing.T) {
	sps := nal.H264SPS{Log2MaxFrameNum: 4, PicOrderCntType: 1, MaxNumRefFrames: 2, NumRefFramesInPicOrderCntCycle: 2, OffsetForNonRefPic: -1, OffsetForTopToBottomField: 1}
	sps.OffsetForRefFrame[0], sps.OffsetForRefFrame[1] = 2, 4
	r := newDPBRun(t, sps)
	var got [][2]int32
	for _, c := range []struct {
		frameNum uint32
		ref      bool
	}{{0, true}, {1, true}, {2, true}, {3, false}, {3, true}} {
		got = append(got, r.must(nal.H264SliceHeader{FrameNum: c.frameNum}, c.frameNum == 0 && c.ref, c.ref).poc)
	}
	want := [][2]int32{{0, 1}, {2, 3}, {6, 7}, {5, 6}, {8, 9}}
	if !slices.Equal(got, want) {
		t.Errorf("POC %v, want %v", got, want)
	}
}

// Picture order count type 2 is twice the frame count, one less for a
// non-reference picture.
func TestH264PictureOrderType2(t *testing.T) {
	r := newDPBRun(t, nal.H264SPS{Log2MaxFrameNum: 4, PicOrderCntType: 2, MaxNumRefFrames: 1})
	var got []int32
	for i := range 20 {
		got = append(got, r.must(nal.H264SliceHeader{FrameNum: uint32(i % 16)}, i == 0, true).poc[0])
	}
	if got[17] != 34 {
		t.Errorf("POC %v: 34 at frame 17, counting past frame_num's wrap", got)
	}
	if p := r.must(nal.H264SliceHeader{FrameNum: 4}, false, false); p.poc[0] != 39 {
		t.Errorf("non-reference POC %d, want 39", p.poc[0])
	}
}

// With the reference frames full, the sliding window drops the one of
// the smallest FrameNumWrap, across frame_num's wrap.
func TestH264SlidingWindow(t *testing.T) {
	r := newDPBRun(t, nal.H264SPS{Log2MaxFrameNum: 4, PicOrderCntType: 2, MaxNumRefFrames: 3})
	for i := range 18 {
		r.must(nal.H264SliceHeader{FrameNum: uint32(i % 16)}, i == 0, true)
	}
	if got := r.refs(); !slices.Equal(got, []int{0, 1, 15}) {
		t.Errorf("references %v, want frames 15, 0 and 1", got)
	}
	r.must(nal.H264SliceHeader{FrameNum: 2}, false, false)
	if got := r.refs(); !slices.Equal(got, []int{0, 1, 15}) {
		t.Errorf("a non-reference picture changed the references to %v", got)
	}
}

func mmco(ops ...nal.H264MMCO) nal.H264SliceHeader {
	h := nal.H264SliceHeader{AdaptiveRefPicMarking: true, NumMMCO: len(ops)}
	copy(h.MMCO[:], ops)
	return h
}

// Memory management control operations mark frames unused, long-term
// and cap the long-term indices as 8.2.5.4 says; operation 5 resets the
// count of frames and pictures.
func TestH264MemoryManagement(t *testing.T) {
	r := newDPBRun(t, nal.H264SPS{Log2MaxFrameNum: 4, PicOrderCntType: 2, MaxNumRefFrames: 4})
	r.must(nal.H264SliceHeader{FrameNum: 0}, true, true)
	r.must(nal.H264SliceHeader{FrameNum: 1}, false, true)
	h := mmco(nal.H264MMCO{Op: 3, DifferenceOfPicNumsMinus1: 1, LongTermFrameIdx: 2}, nal.H264MMCO{Op: 1, DifferenceOfPicNumsMinus1: 0})
	h.FrameNum = 2
	r.must(h, false, true)
	if got := r.refs(); !slices.Equal(got, []int{-3, 2}) {
		t.Fatalf("references %v, want frame 0 long-term at 2 and frame 2", got)
	}
	h = mmco(nal.H264MMCO{Op: 6, LongTermFrameIdx: 2})
	h.FrameNum = 3
	r.must(h, false, true)
	if got := r.refs(); !slices.Equal(got, []int{-3, 2}) {
		t.Fatalf("references %v, want frame 3 replacing frame 0 at long-term 2", got)
	}
	h = mmco(nal.H264MMCO{Op: 4, MaxLongTermFrameIdxPlus1: 2})
	h.FrameNum = 4
	r.must(h, false, true)
	if got := r.refs(); !slices.Equal(got, []int{2, 4}) {
		t.Fatalf("references %v, want long-term 2 dropped past the new maximum", got)
	}
	h = mmco(nal.H264MMCO{Op: 6, LongTermFrameIdx: 0}, nal.H264MMCO{Op: 2, LongTermPicNum: 0})
	h.FrameNum = 5
	r.must(h, false, true)
	if got := r.refs(); !slices.Equal(got, []int{-1, 2, 4}) {
		t.Fatalf("references %v, want frame 5 long-term at 0", got)
	}
	h = mmco(nal.H264MMCO{Op: 5})
	h.FrameNum = 6
	p := r.must(h, false, true)
	if got := r.refs(); !slices.Equal(got, []int{0}) || p.poc != [2]int32{0, 0} || !p.mmco5 {
		t.Fatalf("after operation 5: references %v, POC %v, want only it as frame 0 at POC 0", got, p.poc)
	}
	if p := r.must(nal.H264SliceHeader{FrameNum: 1}, false, true); p.poc[0] != 2 {
		t.Errorf("POC %d after operation 5, want 2", p.poc[0])
	}
}

// A gap in frame_num an SPS allows none of is an error; before the
// first picture decoding can start at, pictures are dropped, and after
// a start at an I picture that is no IDR, so are the pictures leading
// it.
func TestH264StartAndGaps(t *testing.T) {
	r := newDPBRun(t, nal.H264SPS{Log2MaxFrameNum: 4, Log2MaxPicOrderCntLsb: 6, MaxNumRefFrames: 2})
	if _, err := r.pic(nal.H264SliceHeader{FrameNum: 3, PicOrderCntLsb: 10}, false, true, false); !errors.Is(err, errSkipPicture) {
		t.Fatalf("a P picture before any start: %v, want errSkipPicture", err)
	}
	if _, err := r.pic(nal.H264SliceHeader{FrameNum: 4, PicOrderCntLsb: 20}, false, true, true); err != nil {
		t.Fatalf("an I picture starts decoding: %v", err)
	}
	if _, err := r.pic(nal.H264SliceHeader{FrameNum: 5, PicOrderCntLsb: 16}, false, true, false); !errors.Is(err, errSkipPicture) {
		t.Fatalf("a picture leading the I picture: %v, want errSkipPicture", err)
	}
	if _, err := r.pic(nal.H264SliceHeader{FrameNum: 6, PicOrderCntLsb: 26}, false, true, false); err != nil {
		t.Fatalf("a picture after the I picture: %v", err)
	}
	if _, err := r.pic(nal.H264SliceHeader{FrameNum: 9, PicOrderCntLsb: 30}, false, true, false); err == nil || errors.Is(err, errSkipPicture) {
		t.Fatalf("frame_num jumping from 6 to 9 in an SPS that allows no gaps: %v, want an error", err)
	}
}

// A gap in frame_num that the SPS allows infers the frames it leaves
// non-existing (8.2.5.2): short-term references of the frame numbers
// missing, which sliding window marking takes in, counted in picture
// order as their frame_num gives (type 2) or on from the reference
// frame before (type 0); of a gap longer than the reference frames,
// only the last max_num_ref_frames.
func TestH264FrameNumGap(t *testing.T) {
	r := newDPBRun(t, nal.H264SPS{Log2MaxFrameNum: 4, PicOrderCntType: 2, MaxNumRefFrames: 3, GapsInFrameNumAllowed: true})
	for i := range 3 {
		r.must(nal.H264SliceHeader{FrameNum: uint32(i)}, i == 0, true)
	}
	p := r.must(nal.H264SliceHeader{FrameNum: 6}, false, true)
	var nums []uint32
	var pocs []int32
	for _, q := range r.inferred {
		nums, pocs = append(nums, q.hdr.FrameNum), append(pocs, q.poc[0])
		if !q.nonExisting || !q.ref {
			t.Errorf("inferred frame %d is non-existing %v, a reference %v; want both", q.hdr.FrameNum, q.nonExisting, q.ref)
		}
	}
	if !slices.Equal(nums, []uint32{3, 4, 5}) || !slices.Equal(pocs, []int32{6, 8, 10}) || p.poc[0] != 12 {
		t.Errorf("inferred frames %v at POC %v, the next at %d; want 3, 4, 5 at 6, 8, 10, then 12", nums, pocs, p.poc[0])
	}
	if got := r.refs(); !slices.Equal(got, []int{4, 5, 6}) {
		t.Errorf("references %v, want inferred frames 4 and 5 and frame 6", got)
	}
	r.inferred = r.inferred[:0]
	r.must(nal.H264SliceHeader{FrameNum: 3}, false, true)
	if len(r.inferred) != 3 || r.inferred[0].hdr.FrameNum != 0 || r.inferred[2].hdr.FrameNum != 2 {
		t.Errorf("a gap of 12 frames across the wrap infers %d, want the last 3, frames 0 to 2", len(r.inferred))
	}

	r = newDPBRun(t, nal.H264SPS{Log2MaxFrameNum: 4, Log2MaxPicOrderCntLsb: 6, MaxNumRefFrames: 2, GapsInFrameNumAllowed: true})
	r.must(nal.H264SliceHeader{FrameNum: 0}, true, true)
	r.must(nal.H264SliceHeader{FrameNum: 1, PicOrderCntLsb: 4}, false, true)
	p = r.must(nal.H264SliceHeader{FrameNum: 3, PicOrderCntLsb: 8}, false, true)
	if len(r.inferred) != 1 || r.inferred[0].poc != [2]int32{6, 6} || p.poc[0] != 8 {
		t.Errorf("type 0: inferred %v, then POC %d; want one frame at POC 6, then 8", r.inferred, p.poc[0])
	}
}

// Frames come out of reorder by order once more than its depth wait,
// and all of them, in order, on a drain.
func TestReorder(t *testing.T) {
	frames := make([]*VideoFrame, 6)
	for i := range frames {
		frames[i] = &VideoFrame{}
	}
	var out []int
	emit := func(f *VideoFrame) { out = append(out, slices.Index(frames, f)) }
	r := reorder{depth: 2}
	for i, order := range []int32{0, 6, 2, 4, 10, 8} {
		r.add(frames[i], order, emit)
	}
	r.drain(emit)
	if want := []int{0, 2, 3, 1, 5, 4}; !slices.Equal(out, want) {
		t.Errorf("frames out %v, want %v", out, want)
	}
}

// field is the header of a field of frame frameNum, the bottom one when
// bottom, with pic_order_cnt_lsb lsb and the memory management control
// operations ops.
func field(frameNum uint32, bottom bool, lsb uint32, ops ...nal.H264MMCO) nal.H264SliceHeader {
	h := nal.H264SliceHeader{FrameNum: frameNum, FieldPic: true, BottomField: bottom, PicOrderCntLsb: lsb}
	if len(ops) > 0 {
		h.AdaptiveRefPicMarking, h.NumMMCO = true, len(ops)
		copy(h.MMCO[:], ops)
	}
	return h
}

// marks are the reference fields, a slot each in the order of
// FrameNum: the frame's FrameNum, then T and B for its top and bottom
// field where short-term references, t and b where long-term ones.
func (r *dpbRun) marks() string {
	var frames []string
	for i := range r.state.slots {
		ref := &r.state.refs[i]
		if !ref.used() {
			continue
		}
		s := []byte(strconv.Itoa(int(ref.frameNum)))
		for j, c := range []struct {
			in   h264Fields
			name byte
		}{{ref.short & h264Top, 'T'}, {ref.short & h264Bottom, 'B'}, {ref.long & h264Top, 't'}, {ref.long & h264Bottom, 'b'}} {
			if c.in != 0 {
				s = append(s, c.name)
			}
			if j == 3 && ref.long != 0 {
				s = append(s, '@', '0'+byte(ref.longIdx))
			}
		}
		frames = append(frames, string(s))
	}
	slices.Sort(frames)
	return strings.Join(frames, " ")
}

// A field's picture order count is its own, in whichever way the
// sequence counts (type 0 past the wrap of pic_order_cnt_lsb, from the
// field before), and its frame's are its two fields': the frame comes
// out where the earlier of them is.
func TestH264FieldPictureOrder(t *testing.T) {
	for _, c := range []struct {
		name string
		sps  nal.H264SPS
		// want is the frame's counts decoded top field first, and bottom
		// field first.
		want [2][2]int32
	}{
		{"type 0", nal.H264SPS{Log2MaxFrameNum: 4, Log2MaxPicOrderCntLsb: 4, MaxNumRefFrames: 2}, [2][2]int32{{15, 17}, {17, 15}}},
		{"type 1", nal.H264SPS{Log2MaxFrameNum: 4, PicOrderCntType: 1, MaxNumRefFrames: 2, NumRefFramesInPicOrderCntCycle: 1, OffsetForRefFrame: [255]int32{4}, OffsetForTopToBottomField: 3}, [2][2]int32{{4, 7}, {4, 7}}},
		{"type 2", nal.H264SPS{Log2MaxFrameNum: 4, PicOrderCntType: 2, MaxNumRefFrames: 2}, [2][2]int32{{2, 2}, {2, 2}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			for _, bottomFirst := range []bool{false, true} {
				want := c.want[b2i(bottomFirst)]
				r := newDPBRun(t, c.sps)
				r.must(field(0, bottomFirst, 0), true, true)
				r.must(field(0, !bottomFirst, 7), false, true)
				first := r.must(field(1, bottomFirst, 15), false, true)
				second := r.must(field(1, !bottomFirst, 1), false, true)
				if first.second || !second.second || second.slot != first.slot {
					t.Fatalf("the fields of frame 1 are in slots %d and %d, the second a second field: %v", first.slot, second.slot, second.second)
				}
				if own := first.fields.index(); first.poc[own] != want[own] || second.poc != want {
					t.Errorf("bottom field first %v: the first field counts %d, the frame %v, want %d and %v", bottomFirst, first.poc[own], second.poc, want[own], want)
				}
				if got := r.state.refs[second.slot].order(); got != min(want[0], want[1]) {
					t.Errorf("bottom field first %v: the frame comes out at %d, want %d", bottomFirst, got, min(want[0], want[1]))
				}
			}
		})
	}
}

// Two fields are a frame's where the second follows the first with the
// other parity and the same frame_num, and is neither an IDR picture
// nor one that marks every reference unused; any other field starts a
// frame of its own, and the field before it stays alone in its slot, a
// reference as it was. A reference field and one that is none are no
// frame.
func TestH264FieldsPairIntoFrames(t *testing.T) {
	r := newDPBRun(t, nal.H264SPS{Log2MaxFrameNum: 4, PicOrderCntType: 2, MaxNumRefFrames: 8})
	top, bottom := false, true
	steps := []struct {
		name   string
		h      nal.H264SliceHeader
		idr    bool
		second bool
		marks  string
	}{
		{"an IDR field", field(0, top, 0), true, false, "0T"},
		{"its frame's other", field(0, bottom, 0), false, true, "0TB"},
		{"a field", field(1, top, 0), false, false, "0TB 1T"},
		{"a field of the same parity", field(2, top, 0), false, false, "0TB 1T 2T"},
		{"its frame's other", field(2, bottom, 0), false, true, "0TB 1T 2TB"},
		{"a bottom field first", field(3, bottom, 0), false, false, "0TB 1T 2TB 3B"},
		{"a field of another frame_num", field(4, top, 0), false, false, "0TB 1T 2TB 3B 4T"},
		{"a field that marks every reference unused", field(4, bottom, 0, nal.H264MMCO{Op: 5}), false, false, "0B"},
		{"a field of frame_num 0 after it, its frame's other", field(0, top, 0), false, true, "0TB"},
		{"a frame", nal.H264SliceHeader{FrameNum: 1}, false, false, "0TB 1TB"},
		{"a field after a frame", field(2, bottom, 0), false, false, "0TB 1TB 2B"},
		{"an IDR field of the other parity and frame_num 0", field(0, top, 0), true, false, "0T"},
	}
	for i, s := range steps {
		p := r.must(s.h, s.idr, true)
		if p.second != s.second {
			t.Fatalf("step %d, %s: a second field: %v, want %v", i, s.name, p.second, s.second)
		}
		if got := r.marks(); got != s.marks {
			t.Fatalf("step %d, %s: references %q, want %q", i, s.name, got, s.marks)
		}
	}
	r.must(field(1, top, 0), false, true)
	if _, err := r.pic(field(1, bottom, 0), false, false, false); !errors.Is(err, nal.ErrUnsupported) {
		t.Errorf("a field that is no reference after its frame's reference field: %v, want nal.ErrUnsupported", err)
	}
}

// The sliding window counts frames, field pairs and fields alone alike
// and drops both fields of the oldest; the second field of a frame
// whose first is a short-term reference joins it and drops nothing.
func TestH264FieldSlidingWindow(t *testing.T) {
	r := newDPBRun(t, nal.H264SPS{Log2MaxFrameNum: 4, PicOrderCntType: 2, MaxNumRefFrames: 3})
	steps := []struct {
		h     nal.H264SliceHeader
		marks string
	}{
		{field(0, false, 0), "0T"},
		{field(0, true, 0), "0TB"},
		{field(1, true, 0), "0TB 1B"},
		{nal.H264SliceHeader{FrameNum: 2}, "0TB 1B 2TB"},
		{field(3, false, 0), "1B 2TB 3T"},
		{field(3, true, 0), "1B 2TB 3TB"},
		{field(4, false, 0), "2TB 3TB 4T"},
		{field(4, true, 0), "2TB 3TB 4TB"},
		{field(5, true, 0), "3TB 4TB 5B"},
	}
	for i, s := range steps {
		r.must(s.h, i == 0, true)
		if got := r.marks(); got != s.marks {
			t.Fatalf("step %d: references %q, want %q", i, got, s.marks)
		}
	}
}

// A field's memory management control operations name fields, by
// picture numbers that are twice a frame's plus one for a field of its
// own parity: one field of a frame marked unused, short-term or
// long-term; a frame's fields made long-term one by one, at one index,
// the second keeping the first; a long-term index taken from another
// frame, and from a field alone; and the cap of the indices.
func TestH264FieldMemoryManagement(t *testing.T) {
	r := newDPBRun(t, nal.H264SPS{Log2MaxFrameNum: 4, PicOrderCntType: 2, MaxNumRefFrames: 6})
	same, other := func(frame uint32) uint32 { return 2*frame + 1 }, func(frame uint32) uint32 { return 2 * frame }
	unmark := func(curr, picNum uint32) nal.H264MMCO {
		return nal.H264MMCO{Op: 1, DifferenceOfPicNumsMinus1: curr - picNum - 1}
	}
	toLong := func(curr, picNum, idx uint32) nal.H264MMCO {
		return nal.H264MMCO{Op: 3, DifferenceOfPicNumsMinus1: curr - picNum - 1, LongTermFrameIdx: idx}
	}
	long := func(idx uint32) nal.H264MMCO { return nal.H264MMCO{Op: 6, LongTermFrameIdx: idx} }
	top, bottom := false, true
	steps := []struct {
		name  string
		h     nal.H264SliceHeader
		marks string
	}{
		{"", field(0, top, 0), "0T"},
		{"", field(0, bottom, 0), "0TB"},
		{"the current field made long-term", field(1, top, 0, nal.H264MMCO{Op: 4, MaxLongTermFrameIdxPlus1: 3}, long(0)), "0TB 1t@0"},
		{"its frame's other made long-term at the same index, keeping it", field(1, bottom, 0, long(0)), "0TB 1tb@0"},
		{"", field(2, top, 0), "0TB 1tb@0 2T"},
		{"a field of the other parity marked unused, from a second field", field(2, bottom, 0, unmark(same(2), other(0))), "0B 1tb@0 2TB"},
		{"a field of the same parity made long-term", field(3, top, 0, toLong(same(3), same(2), 1)), "0B 1tb@0 2Bt@1 3T"},
		{"its frame's other made long-term at the same index, keeping it", field(3, bottom, 0, toLong(same(3), same(2), 1)), "0B 1tb@0 2tb@1 3TB"},
		{"a long-term field of the other parity marked unused", field(4, top, 0, nal.H264MMCO{Op: 2, LongTermPicNum: other(0)}), "0B 1t@0 2tb@1 3TB 4T"},
		{"the first field of the current frame marked unused", field(4, bottom, 0, unmark(same(4), other(4))), "0B 1t@0 2tb@1 3TB 4B"},
		{"a field made long-term at the index of another frame, which loses it", field(5, top, 0, toLong(same(5), other(0), 1)), "0b@1 1t@0 3TB 4B 5T"},
		{"the current field made long-term at the index of a field alone, which loses it", field(5, bottom, 0, unmark(same(5), other(5)), long(0)), "0b@1 5b@0 3TB 4B"},
		{"the long-term indices capped", field(6, top, 0, nal.H264MMCO{Op: 4, MaxLongTermFrameIdxPlus1: 1}), "3TB 4B 5b@0 6T"},
	}
	for i, s := range steps {
		r.must(s.h, i == 0, true)
		got, want := strings.Fields(r.marks()), strings.Fields(s.marks)
		slices.Sort(want)
		if !slices.Equal(got, want) {
			t.Fatalf("step %d, %s: references %q, want %q", i, s.name, got, want)
		}
	}
}
