package gpu

import (
	"fmt"

	"github.com/stubbedev/lys-lib/intconv"
	"github.com/stubbedev/lys-lib/internal/nal"
)

// h264MaxSlots are the DPB slots an H.264 stream can need: its 16
// reference frames and the picture being decoded.
const h264MaxSlots = 17

// h264Ref is a DPB slot's frame as reference picture marking (8.2.5)
// sees it.
type h264Ref struct {
	// frameNum is FrameNum; longIdx LongTermFrameIdx.
	frameNum, longIdx uint32
	poc               [2]int32
	short, long       bool
}

func (r *h264Ref) used() bool { return r.short || r.long }

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
	// poc is TopFieldOrderCnt and BottomFieldOrderCnt; pocMsb and
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
func (p *h264Pic) order() int32 { return min(p.poc[0], p.poc[1]) }

// h264State is the reference frames of a stream being decoded, by DPB
// slot, and the picture order count state: progressive frames only.
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

// place works out p's picture order count and DPB slot. It returns
// errSkipPicture for a picture leading a non-IDR start, which may
// predict from pictures before it; such a picture still counts in
// frame_num and picture order count.
func (s *h264State) place(p *h264Pic) error {
	start := !s.started
	s.pictureOrder(p)
	switch {
	case p.idr:
		s.leading = false
	case start:
		s.leading, s.leadFrom = true, p.order()
	case s.leading && p.order() < s.leadFrom:
		s.advance(p)
		return errSkipPicture
	}
	if err := s.freeSlot(p); err != nil {
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
	*q = h264Pic{hdr: &s.gapHdr, sps: sps, ref: true, nonExisting: true}
	if sps.PicOrderCntType == 0 {
		q.poc = [2]int32{s.prevRefPoc[0] + 2, s.prevRefPoc[1] + 2}
	} else {
		s.pictureOrder(q)
	}
	return s.freeSlot(q)
}

// pictureOrder derives p's picture order count (8.2.1) for a frame.
func (s *h264State) pictureOrder(p *h264Pic) {
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
		p.poc = [2]int32{msb + lsb, msb + lsb + h.DeltaPicOrderCntBottom}
		return
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
		p.poc = [2]int32{t, t}
		return
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
	top := expected + h.DeltaPicOrderCnt[0]
	p.poc = [2]int32{top, top + sps.OffsetForTopToBottomField + h.DeltaPicOrderCnt[1]}
}

// finish marks the reference frames once p has decoded (8.2.5) and
// keeps the state the next picture derives its own from.
func (s *h264State) finish(p *h264Pic) {
	h := p.hdr
	cur := &s.refs[p.slot]
	*cur = h264Ref{frameNum: h.FrameNum, poc: p.poc}
	if p.ref {
		switch {
		case p.idr:
			s.unmarkAll(p.slot)
			s.maxLongIdx = -1
			if h.LongTermReference {
				cur.long, s.maxLongIdx = true, 0
			}
		case h.AdaptiveRefPicMarking:
			s.adaptive(p)
		default:
			s.slidingWindow(p)
		}
		if !cur.long {
			cur.short = true
		}
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
		if p.mmco5 {
			s.prevPocMsb, s.prevPocLsb = 0, p.poc[0]
		}
	}
	s.prevFrameNum, s.prevFrameNumOffset = frameNum, offset
}

// unmarkAll marks every frame but slot keep's unused for reference, in
// the slots past the sequence's too: a new sequence may use fewer.
func (s *h264State) unmarkAll(keep int) {
	for i := range s.refs {
		if i != keep {
			s.refs[i].short, s.refs[i].long = false, false
		}
	}
}

// numRefs counts the reference frames other than slot cur's.
func (s *h264State) numRefs(cur int) int {
	n := 0
	for i := range s.slots {
		if i != cur && s.refs[i].used() {
			n++
		}
	}
	return n
}

// picNum is a short-term frame's PicNum while p decodes: FrameNumWrap.
func (s *h264State) picNum(r *h264Ref, p *h264Pic) int64 {
	if r.frameNum > p.hdr.FrameNum {
		return int64(r.frameNum) - int64(1)<<p.sps.Log2MaxFrameNum
	}
	return int64(r.frameNum)
}

// slidingWindow is 8.2.5.3: with the reference frames full, the
// short-term frame of the smallest FrameNumWrap goes.
func (s *h264State) slidingWindow(p *h264Pic) {
	if s.numRefs(p.slot) < max(int(p.sps.MaxNumRefFrames), 1) {
		return
	}
	oldest := -1
	for i := range s.slots {
		if i == p.slot || !s.refs[i].short {
			continue
		}
		if oldest < 0 || s.picNum(&s.refs[i], p) < s.picNum(&s.refs[oldest], p) {
			oldest = i
		}
	}
	if oldest >= 0 {
		s.refs[oldest].short = false
	}
}

// adaptive is 8.2.5.4: p's memory management control operations.
func (s *h264State) adaptive(p *h264Pic) {
	h := p.hdr
	for _, m := range h.MMCO[:h.NumMMCO] {
		switch m.Op {
		case 1:
			if i := s.shortByPicNum(p, m.DifferenceOfPicNumsMinus1); i >= 0 {
				s.refs[i].short = false
			}
		case 2:
			for i := range s.slots {
				if i != p.slot && s.refs[i].long && s.refs[i].longIdx == m.LongTermPicNum {
					s.refs[i].long = false
				}
			}
		case 3:
			if i := s.shortByPicNum(p, m.DifferenceOfPicNumsMinus1); i >= 0 {
				s.dropLongIdx(m.LongTermFrameIdx, i)
				s.refs[i].short, s.refs[i].long, s.refs[i].longIdx = false, true, m.LongTermFrameIdx
			}
		case 4:
			s.maxLongIdx = int64(m.MaxLongTermFrameIdxPlus1) - 1
			for i := range s.slots {
				if i != p.slot && s.refs[i].long && int64(s.refs[i].longIdx) > s.maxLongIdx {
					s.refs[i].long = false
				}
			}
		case 5:
			s.unmarkAll(p.slot)
			s.maxLongIdx = -1
			p.mmco5 = true
		case 6:
			s.dropLongIdx(m.LongTermFrameIdx, p.slot)
			cur := &s.refs[p.slot]
			cur.long, cur.longIdx = true, m.LongTermFrameIdx
		}
	}
}

// shortByPicNum is the slot of the short-term frame picNumX names,
// CurrPicNum less diff+1; -1 when none does.
func (s *h264State) shortByPicNum(p *h264Pic, diff uint32) int {
	x := int64(p.hdr.FrameNum) - int64(diff) - 1
	for i := range s.slots {
		if i != p.slot && s.refs[i].short && s.picNum(&s.refs[i], p) == x {
			return i
		}
	}
	return -1
}

// dropLongIdx marks the long-term frame with LongTermFrameIdx idx, other
// than slot keep's, unused.
func (s *h264State) dropLongIdx(idx uint32, keep int) {
	for i := range s.slots {
		if i != keep && s.refs[i].long && s.refs[i].longIdx == idx {
			s.refs[i].long = false
		}
	}
}
