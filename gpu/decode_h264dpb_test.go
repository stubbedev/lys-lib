package gpu

import (
	"errors"
	"slices"
	"testing"

	"github.com/stubbedev/lys-lib/internal/nal"
)

// dpbRun decodes pictures through a h264State as the decoder does.
type dpbRun struct {
	t     *testing.T
	sps   nal.H264SPS
	state h264State
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
	p := h264Pic{hdr: &h, sps: &r.sps, idr: idr, ref: ref, intra: intra}
	if err := r.state.begin(&p); err != nil {
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
		case ref.short:
			out = append(out, int(ref.frameNum))
		case ref.long:
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

// A gap in frame_num is an error; before the first picture decoding
// can start at, pictures are dropped, and after a start at an I picture
// that is no IDR, so are the pictures leading it.
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
		t.Fatalf("frame_num jumping from 6 to 9: %v, want an error", err)
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
