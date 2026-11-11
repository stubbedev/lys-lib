package gpu

import (
	"fmt"

	"github.com/stubbedev/lys-lib/intconv"
	"github.com/stubbedev/lys-lib/internal/nal"
)

// h264MaxSlots are the DPB slots an H.264 stream can need: its 16
// reference frames and the picture being decoded.
const h264MaxSlots = 17

// h264Fields are fields of a frame, a bit each: a field picture is one
// of them, a frame both.
type h264Fields uint8

// The fields.
const (
	h264Top h264Fields = 1 << iota
	h264Bottom
	h264Frame = h264Top | h264Bottom
)

// index is where a field's picture order count is among a frame's.
func (f h264Fields) index() int {
	if f == h264Bottom {
		return 1
	}
	return 0
}

func (f h264Fields) String() string {
	switch f {
	case h264Top:
		return "top field"
	case h264Bottom:
		return "bottom field"
	}
	return "frame"
}

// h264FieldsOf is the field a picture whose slices h heads is, or
// h264Frame.
func h264FieldsOf(h *nal.H264SliceHeader) h264Fields {
	switch {
	case !h.FieldPic:
		return h264Frame
	case h.BottomField:
		return h264Bottom
	}
	return h264Top
}

// h264Ref is a DPB slot's frame as reference picture marking (8.2.5)
// sees it: a frame, or the fields of one decoded so far.
type h264Ref struct {
	// frameNum is FrameNum; longIdx LongTermFrameIdx.
	frameNum, longIdx uint32
	poc               [2]int32
	// held are the fields the slot's picture holds, decoded as one
	// picture when whole, else each as a picture of its own; short and
	// long the ones used for short-term and for long-term reference.
	held, short, long h264Fields
	whole             bool
}

// marked are the fields used for reference.
func (r *h264Ref) marked() h264Fields { return r.short | r.long }

func (r *h264Ref) used() bool { return r.marked() != 0 }

// order is the picture order count of the fields the slot holds: the
// first of them in output order.
func (r *h264Ref) order() int32 {
	switch r.held {
	case h264Top:
		return r.poc[0]
	case h264Bottom:
		return r.poc[1]
	}
	return min(r.poc[0], r.poc[1])
}

// h264Pic is the picture being decoded: its first slice's header and
// what decoding it works out.
type h264Pic struct {
	hdr *nal.H264SliceHeader
	sps *nal.H264SPS
	idr bool
	// ref is nal_ref_idc != 0.
	ref bool
	// intra is every slice an I or SI slice.
	intra bool
	// fields is the field the picture is, or h264Frame; second marks
	// the second field of a frame, whose first field its slot holds.
	fields h264Fields
	second bool
	// poc is TopFieldOrderCnt and BottomFieldOrderCnt, a field picture's
	// own alone until its frame's other field joins it; pocMsb and
	// frameNumOffset the state that derived it (8.2.1).
	poc            [2]int32
	pocMsb         int32
	frameNumOffset int32
	slot           int
	mmco5          bool
	// nonExisting marks a frame a gap in frame_num leaves non-existing
	// (8.2.5.2), which the decoder infers.
	nonExisting bool
}

// order is the picture's position in output order among the pictures
// since the last IDR or memory_management_control_operation 5.
func (p *h264Pic) order() int32 {
	if p.fields == h264Frame {
		return min(p.poc[0], p.poc[1])
	}
	return p.poc[p.fields.index()]
}

// h264State is the reference frames of a stream being decoded, by DPB
// slot, and the picture order count state.
type h264State struct {
	// slots is how many DPB slots the sequence uses.
	slots int
	refs  [h264MaxSlots]h264Ref
	// maxLongIdx is MaxLongTermFrameIdx, -1 for none.
	maxLongIdx int64
	// prevRefFrameNum, prevPocMsb and prevPocLsb are the previous
	// reference picture's; prevFrameNum and prevFrameNumOffset the
	// previous picture's.
	prevRefFrameNum    uint32
	prevPocMsb         int32
	prevPocLsb         int32
	prevFrameNum       uint32
	prevFrameNumOffset int32
	// prevRefPoc is the previous reference frame's picture order count,
	// which the frames a gap infers count on from.
	prevRefPoc [2]int32
	// gapHdr is the slice header of the frame a gap infers.
	gapHdr nal.H264SliceHeader
	// first is the field decoded last, when the next picture may be the
	// other field of its frame: its slot, its frame_num and whether it
	// is a reference.
	first struct {
		open     bool
		field    h264Fields
		slot     int
		frameNum uint32
		ref      bool
	}
	// started is set once a picture that needs no earlier one (an IDR
	// or an I picture) began decoding; leadFrom is the order before
	// which pictures after a non-IDR start are dropped, for they may
	// predict from pictures before it.
	started  bool
	leading  bool
	leadFrom int32
}

// reset forgets every reference frame: what a seek does.
func (s *h264State) reset() {
	*s = h264State{slots: s.slots, maxLongIdx: -1}
}

// begin starts p, returning how many frames a gap in frame_num before
// it leaves non-existing, for the decoder to infer (infer) before it
// places p. It returns errSkipPicture for a picture before the first
// one decoding can start at.
func (s *h264State) begin(p *h264Pic) (gap int, err error) {
	if !s.started {
		if !p.idr && !p.intra {
			return 0, errSkipPicture
		}
		s.prevRefFrameNum, s.prevFrameNum = p.hdr.FrameNum, p.hdr.FrameNum
	}
	return s.frameNumGap(p)
}

// pairs reports whether p is the second field of the frame whose first
// field was decoded before it: a field of the other parity and the same
// frame_num, neither an IDR picture nor one that marks every reference
// unused, which start a frame of their own (3.30).
func (s *h264State) pairs(p *h264Pic) bool {
	f := &s.first
	if !f.open || p.fields == h264Frame || p.fields == f.field || p.hdr.FrameNum != f.frameNum || p.idr {
		return false
	}
	for _, m := range p.hdr.MMCO[:p.hdr.NumMMCO] {
		if m.Op == 5 {
			return false
		}
	}
	return true
}

// place works out p's picture order count and DPB slot, the slot of its
// frame's first field for a second field. It returns errSkipPicture
// for a picture leading a non-IDR start, which may predict from
// pictures before it; such a picture still counts in frame_num and
// picture order count. A field decoded before p that p is not the
// other field of stays unpaired.
func (s *h264State) place(p *h264Pic) error {
	start := !s.started
	p.second = s.pairs(p)
	if p.second && p.ref != s.first.ref {
		return fmt.Errorf("%w: the fields of frame %d are a reference field and one that is none, which no decoder pairs: %w", nal.ErrH264, p.hdr.FrameNum, nal.ErrUnsupported)
	}
	s.first.open = false
	s.pictureOrder(p)
	switch {
	case p.idr:
		s.leading = false
	case p.second:
	case start:
		s.leading, s.leadFrom = true, p.order()
	case s.leading && p.order() < s.leadFrom:
		s.advance(p)
		return errSkipPicture
	}
	if p.second {
		p.slot = s.first.slot
		other := 1 - p.fields.index()
		p.poc[other] = s.refs[p.slot].poc[other]
	} else if err := s.freeSlot(p); err != nil {
		return err
	}
	s.started = true
	return nil
}

// freeSlot gives p a DPB slot no reference frame holds.
func (s *h264State) freeSlot(p *h264Pic) error {
	for i := range s.slots {
		if !s.refs[i].used() {
			p.slot = i
			return nil
		}
	}
	return fmt.Errorf("gpu: H.264 frame %d finds all %d DPB slots holding reference frames", p.hdr.FrameNum, s.slots)
}

// frameNumGap is how many of the frames a gap in frame_num before p
// leaves non-existing (7.4.3, 8.2.5.2) the decoder infers: of a gap
// longer than the reference frames, the ones before the last
// max_num_ref_frames would slide out of the reference frames
// unreferenced, and only count in frame_num. A gap in a sequence that
// allows none is pictures lost on the way, an error.
func (s *h264State) frameNumGap(p *h264Pic) (int, error) {
	if p.idr {
		return 0, nil
	}
	fn, maxFrameNum := p.hdr.FrameNum, uint32(1)<<p.sps.Log2MaxFrameNum
	if fn == s.prevRefFrameNum || fn == (s.prevRefFrameNum+1)%maxFrameNum {
		return 0, nil
	}
	if !p.sps.GapsInFrameNumAllowed {
		return 0, fmt.Errorf("gpu: H.264 frame_num jumps from %d to %d in SPS %d, which allows no gaps: frames are missing", s.prevRefFrameNum, fn, p.sps.ID)
	}
	gap := int((fn + maxFrameNum - s.prevRefFrameNum - 1) % maxFrameNum)
	keep := min(gap, int(max(p.sps.MaxNumRefFrames, 1)))
	s.prevRefFrameNum = (s.prevRefFrameNum + intconv.Must[uint32](gap-keep)) % maxFrameNum
	return keep, nil
}

// infer sets q up as the next frame of a gap in frame_num, as 8.2.5.2
// infers it: a short-term reference frame of the next frame_num, which
// sliding window marking takes in when it finishes. Its picture order
// count is the decoding process's where pic_order_cnt_type derives it
// from frame_num, else (where the standard leaves it unspecified) the
// previous reference frame's plus 2, as ffmpeg counts.
func (s *h264State) infer(q *h264Pic, sps *nal.H264SPS) error {
	s.gapHdr = nal.H264SliceHeader{SliceType: nal.H264SliceP, FrameNum: (s.prevRefFrameNum + 1) % (uint32(1) << sps.Log2MaxFrameNum)}
	*q = h264Pic{hdr: &s.gapHdr, sps: sps, ref: true, fields: h264Frame, nonExisting: true}
	s.first.open = false
	if sps.PicOrderCntType == 0 {
		q.poc = [2]int32{s.prevRefPoc[0] + 2, s.prevRefPoc[1] + 2}
	} else {
		s.pictureOrder(q)
	}
	return s.freeSlot(q)
}

// pictureOrder derives p's picture order count (8.2.1): a frame's two,
// or a field's own, in both places until place gives a second field
// its frame's other.
func (s *h264State) pictureOrder(p *h264Pic) {
	top, bottom := s.fieldOrders(p)
	switch p.fields {
	case h264Top:
		bottom = top
	case h264Bottom:
		top = bottom
	}
	p.poc = [2]int32{top, bottom}
}

// fieldOrders is TopFieldOrderCnt and BottomFieldOrderCnt as p's
// header codes them, of which a field picture has its own alone.
func (s *h264State) fieldOrders(p *h264Pic) (top, bottom int32) {
	sps, h := p.sps, p.hdr
	if sps.PicOrderCntType == 0 {
		if p.idr {
			s.prevPocMsb, s.prevPocLsb = 0, 0
		}
		maxLsb := int32(1) << sps.Log2MaxPicOrderCntLsb
		lsb := intconv.Must[int32](h.PicOrderCntLsb)
		msb := s.prevPocMsb
		switch {
		case lsb < s.prevPocLsb && s.prevPocLsb-lsb >= maxLsb/2:
			msb += maxLsb
		case lsb > s.prevPocLsb && lsb-s.prevPocLsb > maxLsb/2:
			msb -= maxLsb
		}
		p.pocMsb = msb
		if p.fields != h264Frame {
			return msb + lsb, msb + lsb
		}
		return msb + lsb, msb + lsb + h.DeltaPicOrderCntBottom
	}
	maxFrameNum := int32(1) << sps.Log2MaxFrameNum
	frameNum := intconv.Must[int32](h.FrameNum)
	switch {
	case p.idr:
		p.frameNumOffset = 0
	case s.prevFrameNum > h.FrameNum:
		p.frameNumOffset = s.prevFrameNumOffset + maxFrameNum
	default:
		p.frameNumOffset = s.prevFrameNumOffset
	}
	if sps.PicOrderCntType == 2 {
		var t int32
		switch {
		case p.idr:
		case !p.ref:
			t = 2*(p.frameNumOffset+frameNum) - 1
		default:
			t = 2 * (p.frameNumOffset + frameNum)
		}
		return t, t
	}
	n := int32(sps.NumRefFramesInPicOrderCntCycle)
	abs := int32(0)
	if n != 0 {
		abs = p.frameNumOffset + frameNum
	}
	if !p.ref && abs > 0 {
		abs--
	}
	var expected int32
	if abs > 0 {
		var cycle int32
		for _, d := range sps.OffsetForRefFrame[:n] {
			cycle += d
		}
		cycles, in := (abs-1)/n, (abs-1)%n
		expected = cycles * cycle
		for _, d := range sps.OffsetForRefFrame[:in+1] {
			expected += d
		}
	}
	if !p.ref {
		expected += sps.OffsetForNonRefPic
	}
	top = expected + h.DeltaPicOrderCnt[0]
	if p.fields != h264Frame {
		return top, top + sps.OffsetForTopToBottomField
	}
	return top, top + sps.OffsetForTopToBottomField + h.DeltaPicOrderCnt[1]
}

// finish marks the reference pictures once p has decoded (8.2.5) and
// keeps the state the next picture derives its own from. A second
// field joins its frame's first in the slot.
func (s *h264State) finish(p *h264Pic) {
	h := p.hdr
	cur := &s.refs[p.slot]
	if !p.second {
		*cur = h264Ref{frameNum: h.FrameNum, whole: p.fields == h264Frame}
	}
	cur.held |= p.fields
	cur.poc = p.poc
	if p.ref {
		switch {
		case p.idr:
			s.unmarkAll(p.slot)
			s.maxLongIdx = -1
			if h.LongTermReference {
				cur.long, cur.longIdx, s.maxLongIdx = p.fields, 0, 0
			}
		case h.AdaptiveRefPicMarking:
			s.adaptive(p)
		default:
			s.slidingWindow(p)
		}
		cur.short |= p.fields &^ cur.long
	}
	if p.mmco5 {
		t := p.order()
		p.poc[0] -= t
		p.poc[1] -= t
		cur.poc, cur.frameNum = p.poc, 0
	}
	if p.ref {
		s.prevRefPoc = p.poc
	}
	s.advance(p)
	s.first.open, s.first.field, s.first.slot, s.first.frameNum, s.first.ref = p.fields != h264Frame && !p.second, p.fields, p.slot, cur.frameNum, p.ref
}

// advance keeps what the next picture derives its frame_num check and
// picture order count from, once p has decoded or been dropped.
func (s *h264State) advance(p *h264Pic) {
	h := p.hdr
	frameNum, offset := h.FrameNum, p.frameNumOffset
	if p.mmco5 {
		frameNum, offset = 0, 0
	}
	if p.ref {
		s.prevRefFrameNum = frameNum
	}
	if p.ref && !p.nonExisting {
		s.prevPocMsb, s.prevPocLsb = p.pocMsb, intconv.Must[int32](h.PicOrderCntLsb)
		switch {
		case p.mmco5 && p.fields == h264Bottom:
			s.prevPocMsb, s.prevPocLsb = 0, 0
		case p.mmco5:
			s.prevPocMsb, s.prevPocLsb = 0, p.poc[0]
		}
	}
	s.prevFrameNum, s.prevFrameNumOffset = frameNum, offset
}

// unmarkAll marks every picture but slot keep's unused for reference,
// in the slots past the sequence's too: a new sequence may use fewer.
func (s *h264State) unmarkAll(keep int) {
	for i := range s.refs {
		if i != keep {
			s.refs[i].short, s.refs[i].long = 0, 0
		}
	}
}

// numRefs counts the reference frames, complementary field pairs and
// unpaired fields: the ones with a short-term field and the ones with
// a long-term field (8.2.5.3).
func (s *h264State) numRefs() int {
	n := 0
	for i := range s.slots {
		if s.refs[i].short != 0 {
			n++
		}
		if s.refs[i].long != 0 {
			n++
		}
	}
	return n
}

// frameNumWrap is a short-term picture's FrameNumWrap while p decodes.
func (s *h264State) frameNumWrap(r *h264Ref, p *h264Pic) int64 {
	if r.frameNum > p.hdr.FrameNum {
		return int64(r.frameNum) - int64(1)<<p.sps.Log2MaxFrameNum
	}
	return int64(r.frameNum)
}

// slidingWindow is 8.2.5.3: with the reference frames full, the
// short-term frame, field pair or unpaired field of the smallest
// FrameNumWrap goes, both its fields. The second field of a frame
// whose first field is a short-term reference joins it and moves
// nothing.
func (s *h264State) slidingWindow(p *h264Pic) {
	if p.second && s.refs[p.slot].short != 0 {
		return
	}
	if s.numRefs() < max(int(p.sps.MaxNumRefFrames), 1) {
		return
	}
	oldest := -1
	for i := range s.slots {
		if i == p.slot || s.refs[i].short == 0 {
			continue
		}
		if oldest < 0 || s.frameNumWrap(&s.refs[i], p) < s.frameNumWrap(&s.refs[oldest], p) {
			oldest = i
		}
	}
	if oldest >= 0 {
		s.refs[oldest].short = 0
	}
}

// adaptive is 8.2.5.4: p's memory management control operations, which
// name frames where p is a frame and fields where it is a field.
func (s *h264State) adaptive(p *h264Pic) {
	h := p.hdr
	cur := &s.refs[p.slot]
	for _, m := range h.MMCO[:h.NumMMCO] {
		switch m.Op {
		case 1:
			if i, f := s.shortByPicNum(p, m.DifferenceOfPicNumsMinus1); i >= 0 {
				s.refs[i].short &^= f
			}
		case 2:
			if i, f := s.longByPicNum(p, m.LongTermPicNum); i >= 0 {
				s.refs[i].long &^= f
			}
		case 3:
			if i, f := s.shortByPicNum(p, m.DifferenceOfPicNumsMinus1); i >= 0 {
				s.dropLongIdx(m.LongTermFrameIdx, i)
				r := &s.refs[i]
				r.short, r.long, r.longIdx = r.short&^f, r.long|f, m.LongTermFrameIdx
			}
		case 4:
			s.maxLongIdx = int64(m.MaxLongTermFrameIdxPlus1) - 1
			for i := range s.slots {
				if s.refs[i].long != 0 && int64(s.refs[i].longIdx) > s.maxLongIdx {
					s.refs[i].long = 0
				}
			}
		case 5:
			s.unmarkAll(p.slot)
			s.maxLongIdx = -1
			p.mmco5 = true
		case 6:
			s.dropLongIdx(m.LongTermFrameIdx, p.slot)
			cur.long, cur.longIdx = cur.long|p.fields, m.LongTermFrameIdx
		}
	}
}

// picNums walks the pictures a memory management control operation of
// p can name with their picture numbers (8.2.4.1): frames whose fields
// both are marked, by FrameNumWrap or LongTermFrameIdx, where p is a
// frame; each marked field where p is a field, by twice that, plus one
// for a field of p's parity. marked is short or long.
func (s *h264State) picNums(p *h264Pic, long bool, yield func(slot int, f h264Fields, num int64) bool) {
	for i := range s.slots {
		r := &s.refs[i]
		marked, num := r.short, s.frameNumWrap(r, p)
		if long {
			marked, num = r.long, int64(r.longIdx)
		}
		if p.fields == h264Frame {
			if i != p.slot && marked == h264Frame && !yield(i, h264Frame, num) {
				return
			}
			continue
		}
		for _, f := range [...]h264Fields{h264Top, h264Bottom} {
			if marked&f == 0 {
				continue
			}
			n := 2 * num
			if f == p.fields {
				n++
			}
			if !yield(i, f, n) {
				return
			}
		}
	}
}

// shortByPicNum is the slot and the fields of the short-term picture
// picNumX names, CurrPicNum less diff+1; slot -1 when none does.
func (s *h264State) shortByPicNum(p *h264Pic, diff uint32) (slot int, fields h264Fields) {
	curr := int64(p.hdr.FrameNum)
	if p.fields != h264Frame {
		curr = 2*curr + 1
	}
	slot = -1
	s.picNums(p, false, func(i int, f h264Fields, num int64) bool {
		if num != curr-int64(diff)-1 {
			return true
		}
		slot, fields = i, f
		return false
	})
	return slot, fields
}

// longByPicNum is the slot and the fields of the long-term picture
// LongTermPicNum num names; slot -1 when none does.
func (s *h264State) longByPicNum(p *h264Pic, num uint32) (slot int, fields h264Fields) {
	slot = -1
	s.picNums(p, true, func(i int, f h264Fields, n int64) bool {
		if n != int64(num) {
			return true
		}
		slot, fields = i, f
		return false
	})
	return slot, fields
}

// dropLongIdx marks the long-term pictures with LongTermFrameIdx idx
// unused, but slot keep's: the picture given the index, or the frame
// whose other field has it.
func (s *h264State) dropLongIdx(idx uint32, keep int) {
	for i := range s.slots {
		if i != keep && s.refs[i].long != 0 && s.refs[i].longIdx == idx {
			s.refs[i].long = 0
		}
	}
}
