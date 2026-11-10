package nal

import (
	"errors"
	"fmt"

	"github.com/stubbedev/lys-lib/intconv"
)

// The H.264 NAL unit types a decoder acts on (Table 7-1).
const (
	H264NALSlice = 1
	H264NALIDR   = 5
	H264NALSPS   = 7
	H264NALPPS   = 8
	H264NALAUD   = 9
)

// H264Type is an H.264 NAL unit's nal_unit_type.
func H264Type(unit []byte) uint8 { return unit[0] & 0x1f }

// H264RefIdc is an H.264 NAL unit's nal_ref_idc.
func H264RefIdc(unit []byte) uint8 { return unit[0] >> 5 & 3 }

// The H.264 slice types, slice_type modulo 5 (Table 7-6).
const (
	H264SliceP  = 0
	H264SliceB  = 1
	H264SliceI  = 2
	H264SliceSP = 3
	H264SliceSI = 4
)

// ErrH264 is an H.264 header a decoder cannot read: malformed, or using
// syntax outside what it decodes. Errors wrap it with what was read.
var ErrH264 = errors.New("nal: H.264")

// H264ScalingLists are the 4x4 and 8x8 scaling lists in the order the
// bitstream codes them (zig-zag), every list resolved: present, a
// default, or what the fall-back rules carry over.
type H264ScalingLists struct {
	List4x4 [6][16]uint8
	List8x8 [6][64]uint8
}

// The default scaling lists (Tables 7-3 and 7-4), in zig-zag order.
var (
	h264Default4x4Intra = [16]uint8{6, 13, 13, 20, 20, 20, 28, 28, 28, 28, 32, 32, 32, 37, 37, 42}
	h264Default4x4Inter = [16]uint8{10, 14, 14, 20, 20, 20, 24, 24, 24, 24, 27, 27, 27, 30, 30, 34}
	h264Default8x8Intra = [64]uint8{
		6, 10, 10, 13, 11, 13, 16, 16, 16, 16, 18, 18, 18, 18, 18, 23,
		23, 23, 23, 23, 23, 25, 25, 25, 25, 25, 25, 25, 27, 27, 27, 27,
		27, 27, 27, 27, 29, 29, 29, 29, 29, 29, 29, 31, 31, 31, 31, 31,
		31, 33, 33, 33, 33, 33, 36, 36, 36, 36, 38, 38, 38, 40, 40, 42,
	}
	h264Default8x8Inter = [64]uint8{
		9, 13, 13, 15, 13, 15, 17, 17, 17, 17, 19, 19, 19, 19, 19, 21,
		21, 21, 21, 21, 21, 22, 22, 22, 22, 22, 22, 22, 24, 24, 24, 24,
		24, 24, 24, 24, 25, 25, 25, 25, 25, 25, 25, 27, 27, 27, 27, 27,
		27, 28, 28, 28, 28, 28, 30, 30, 30, 30, 32, 32, 32, 33, 33, 35,
	}
)

// flat fills l with Flat_4x4_16 and Flat_8x8_16.
func (l *H264ScalingLists) flat() {
	for i := range l.List4x4 {
		for j := range l.List4x4[i] {
			l.List4x4[i][j] = 16
		}
		for j := range l.List8x8[i] {
			l.List8x8[i][j] = 16
		}
	}
}

// parse reads the scaling_list syntax of n lists (7.3.2.1.1.1), each
// absent one resolved by fall-back rule A when seq is nil (an SPS, or
// a PPS whose SPS has no matrix) or rule B from seq's lists (Table 7-2).
func (l *H264ScalingLists) parse(r *Reader, n int, seq *H264ScalingLists) {
	for i := range 12 {
		present := i < n && r.Flag()
		useDefault := false
		if present {
			useDefault = scalingList(r, l.list(i))
		}
		switch {
		case useDefault:
			l.setDefault(i)
		case !present:
			l.fallBack(i, seq)
		}
	}
}

// list is list i of the twelve: the six 4x4 lists, then the six 8x8.
func (l *H264ScalingLists) list(i int) []uint8 {
	if i < 6 {
		return l.List4x4[i][:]
	}
	return l.List8x8[i-6][:]
}

// setDefault sets list i to its default.
func (l *H264ScalingLists) setDefault(i int) {
	switch {
	case i < 3:
		l.List4x4[i] = h264Default4x4Intra
	case i < 6:
		l.List4x4[i] = h264Default4x4Inter
	case i%2 == 0:
		l.List8x8[i-6] = h264Default8x8Intra
	default:
		l.List8x8[i-6] = h264Default8x8Inter
	}
}

// fallBack resolves list i, absent: from the list before it of the
// same kind, or for the first of a kind from seq (rule B), or without
// seq its default (rule A).
func (l *H264ScalingLists) fallBack(i int, seq *H264ScalingLists) {
	first := i == 0 || i == 3 || i == 6 || i == 7
	switch {
	case !first && i < 6:
		l.List4x4[i] = l.List4x4[i-1]
	case !first:
		l.List8x8[i-6] = l.List8x8[i-8]
	case seq != nil:
		copy(l.list(i), seq.list(i))
	default:
		l.setDefault(i)
	}
}

// scalingList reads one scaling list into list, reporting whether it
// says to use the default list instead.
func scalingList(r *Reader, list []uint8) (useDefault bool) {
	last, next := int32(8), int32(8)
	for j := range list {
		if next != 0 {
			next = (last + r.SE() + 256) % 256
			if j == 0 && next == 0 {
				return true
			}
		}
		if next != 0 {
			last = next
		}
		list[j] = uint8(last)
	}
	return false
}

// H264VUI is what an SPS's video usability information says that a
// decoder uses.
type H264VUI struct {
	AspectRatioInfoPresent   bool
	AspectRatioIdc           uint8
	SarWidth, SarHeight      uint16
	OverscanInfoPresent      bool
	OverscanAppropriate      bool
	VideoSignalTypePresent   bool
	VideoFormat              uint8
	FullRange                bool
	ColourDescriptionPresent bool
	ColourPrimaries          uint8
	TransferCharacteristics  uint8
	MatrixCoefficients       uint8
	ChromaLocInfoPresent     bool
	ChromaSampleLocTypeTop   uint8
	ChromaSampleLocTypeBot   uint8
	TimingInfoPresent        bool
	NumUnitsInTick           uint32
	TimeScale                uint32
	FixedFrameRate           bool
	BitstreamRestriction     bool
	MaxNumReorderFrames      uint8
	MaxDecFrameBuffering     uint8
}

// H264SPS is a sequence parameter set (7.3.2.1.1).
type H264SPS struct {
	ProfileIdc uint8
	// Constraints are constraint_set0_flag to constraint_set5_flag and
	// reserved_zero_2bits as coded: constraint_set0_flag in bit 7.
	Constraints                 uint8
	LevelIdc                    uint8
	ID                          uint8
	ChromaFormatIdc             uint8
	SeparateColourPlane         bool
	BitDepthLumaMinus8          uint8
	BitDepthChromaMinus8        uint8
	QpprimeYZeroTransformBypass bool
	ScalingMatrixPresent        bool
	// Scaling are the sequence's scaling lists, flat when it has none.
	Scaling                        H264ScalingLists
	Log2MaxFrameNum                uint8
	PicOrderCntType                uint8
	Log2MaxPicOrderCntLsb          uint8
	DeltaPicOrderAlwaysZero        bool
	OffsetForNonRefPic             int32
	OffsetForTopToBottomField      int32
	NumRefFramesInPicOrderCntCycle uint8
	OffsetForRefFrame              [255]int32
	MaxNumRefFrames                uint8
	GapsInFrameNumAllowed          bool
	// WidthMbs and HeightMapUnits are the coded size in macroblocks
	// and map units (macroblock pairs in a stream of fields).
	WidthMbs, HeightMapUnits int
	FrameMbsOnly             bool
	MbAdaptiveFrameField     bool
	Direct8x8Inference       bool
	FrameCropping            bool
	// Crop is frame_crop_left, right, top and bottom_offset.
	Crop       [4]uint32
	VUIPresent bool
	VUI        H264VUI
}

var h264HighProfiles = [256]bool{100: true, 110: true, 122: true, 244: true, 44: true, 83: true, 86: true, 118: true, 128: true, 138: true, 139: true, 134: true, 135: true}

// H264HighProfile reports whether an SPS of profile_idc carries the
// chroma format, bit depth and scaling matrix syntax.
func H264HighProfile(idc uint8) bool { return h264HighProfiles[idc] }

// Parse reads the SPS from rbsp, the NAL unit's payload after its
// header byte with emulation prevention removed.
func (s *H264SPS) Parse(rbsp []byte) error {
	r := NewReader(rbsp)
	*s = H264SPS{ProfileIdc: r.U8(8), ChromaFormatIdc: 1}
	s.Constraints = r.U8(8)
	s.LevelIdc = r.U8(8)
	id := r.UE()
	if id > 31 {
		return fmt.Errorf("%w SPS: seq_parameter_set_id %d is past 31", ErrH264, id)
	}
	s.ID = uint8(id)
	s.Scaling.flat()
	if H264HighProfile(s.ProfileIdc) {
		chroma := r.UE()
		if chroma > 3 {
			return fmt.Errorf("%w SPS %d: chroma_format_idc %d is past 3", ErrH264, s.ID, chroma)
		}
		s.ChromaFormatIdc = uint8(chroma)
		if chroma == 3 {
			s.SeparateColourPlane = r.Flag()
		}
		luma, chromaDepth := r.UE(), r.UE()
		if luma > 6 || chromaDepth > 6 {
			return fmt.Errorf("%w SPS %d: bit depths %d and %d (minus 8) are past 14", ErrH264, s.ID, luma, chromaDepth)
		}
		s.BitDepthLumaMinus8, s.BitDepthChromaMinus8 = uint8(luma), uint8(chromaDepth)
		s.QpprimeYZeroTransformBypass = r.Flag()
		if s.ScalingMatrixPresent = r.Flag(); s.ScalingMatrixPresent {
			n := 8
			if chroma == 3 {
				n = 12
			}
			s.Scaling.parse(&r, n, nil)
		}
	}
	maxFrameNum := r.UE()
	if maxFrameNum > 12 {
		return fmt.Errorf("%w SPS %d: log2_max_frame_num_minus4 %d is past 12", ErrH264, s.ID, maxFrameNum)
	}
	s.Log2MaxFrameNum = uint8(maxFrameNum + 4)
	poc := r.UE()
	switch poc {
	case 0:
		lsb := r.UE()
		if lsb > 12 {
			return fmt.Errorf("%w SPS %d: log2_max_pic_order_cnt_lsb_minus4 %d is past 12", ErrH264, s.ID, lsb)
		}
		s.Log2MaxPicOrderCntLsb = uint8(lsb + 4)
	case 1:
		s.DeltaPicOrderAlwaysZero = r.Flag()
		s.OffsetForNonRefPic = r.SE()
		s.OffsetForTopToBottomField = r.SE()
		n := r.UE()
		if n > 255 {
			return fmt.Errorf("%w SPS %d: num_ref_frames_in_pic_order_cnt_cycle %d is past 255", ErrH264, s.ID, n)
		}
		s.NumRefFramesInPicOrderCntCycle = uint8(n)
		for i := range n {
			s.OffsetForRefFrame[i] = r.SE()
		}
	case 2:
	default:
		return fmt.Errorf("%w SPS %d: pic_order_cnt_type %d is past 2", ErrH264, s.ID, poc)
	}
	s.PicOrderCntType = intconv.Must[uint8](poc)
	refs := r.UE()
	if refs > 16 {
		return fmt.Errorf("%w SPS %d: max_num_ref_frames %d is past 16", ErrH264, s.ID, refs)
	}
	s.MaxNumRefFrames = uint8(refs)
	s.GapsInFrameNumAllowed = r.Flag()
	w, h := r.UE(), r.UE()
	if w > 1023 || h > 1023 {
		return fmt.Errorf("%w SPS %d: %dx%d macroblocks is past the largest level's frame", ErrH264, s.ID, w+1, h+1)
	}
	s.WidthMbs, s.HeightMapUnits = int(w)+1, int(h)+1
	if s.FrameMbsOnly = r.Flag(); !s.FrameMbsOnly {
		s.MbAdaptiveFrameField = r.Flag()
	}
	s.Direct8x8Inference = r.Flag()
	if s.FrameCropping = r.Flag(); s.FrameCropping {
		for i := range s.Crop {
			s.Crop[i] = r.UE()
		}
	}
	if s.VUIPresent = r.Flag(); s.VUIPresent {
		s.VUI.parse(&r)
	}
	if r.Short() {
		return fmt.Errorf("%w SPS %d: ends inside its syntax", ErrH264, s.ID)
	}
	x0, y0, x1, y1 := s.CropRect()
	if x1 <= x0 || y1 <= y0 {
		return fmt.Errorf("%w SPS %d: cropping %v leaves no picture of %dx%d macroblocks", ErrH264, s.ID, s.Crop, s.WidthMbs, s.FrameHeightMbs())
	}
	return nil
}

func (v *H264VUI) parse(r *Reader) {
	if v.AspectRatioInfoPresent = r.Flag(); v.AspectRatioInfoPresent {
		if v.AspectRatioIdc = r.U8(8); v.AspectRatioIdc == 255 {
			v.SarWidth, v.SarHeight = r.U16(16), r.U16(16)
		}
	}
	if v.OverscanInfoPresent = r.Flag(); v.OverscanInfoPresent {
		v.OverscanAppropriate = r.Flag()
	}
	if v.VideoSignalTypePresent = r.Flag(); v.VideoSignalTypePresent {
		v.VideoFormat = r.U8(3)
		v.FullRange = r.Flag()
		if v.ColourDescriptionPresent = r.Flag(); v.ColourDescriptionPresent {
			v.ColourPrimaries, v.TransferCharacteristics, v.MatrixCoefficients = r.U8(8), r.U8(8), r.U8(8)
		}
	}
	if v.ChromaLocInfoPresent = r.Flag(); v.ChromaLocInfoPresent {
		v.ChromaSampleLocTypeTop, v.ChromaSampleLocTypeBot = uint8(min(r.UE(), 255)), uint8(min(r.UE(), 255))
	}
	if v.TimingInfoPresent = r.Flag(); v.TimingInfoPresent {
		v.NumUnitsInTick, v.TimeScale = r.U(32), r.U(32)
		v.FixedFrameRate = r.Flag()
	}
	nal, vcl := r.Flag(), false
	if nal {
		skipHRD(r)
	}
	if vcl = r.Flag(); vcl {
		skipHRD(r)
	}
	if nal || vcl {
		r.Skip(1)
	}
	r.Skip(1)
	if v.BitstreamRestriction = r.Flag(); v.BitstreamRestriction {
		r.Skip(1)
		for range 4 {
			r.UE()
		}
		v.MaxNumReorderFrames, v.MaxDecFrameBuffering = uint8(min(r.UE(), 255)), uint8(min(r.UE(), 255))
	}
}

// skipHRD reads past hrd_parameters (E.1.2).
func skipHRD(r *Reader) {
	n := r.UE()
	if n > 31 {
		r.short = true
		return
	}
	r.Skip(8)
	for range n + 1 {
		r.UE()
		r.UE()
		r.Skip(1)
	}
	r.Skip(20)
}

// FrameHeightMbs is the frame's height in macroblocks.
func (s *H264SPS) FrameHeightMbs() int {
	if s.FrameMbsOnly {
		return s.HeightMapUnits
	}
	return 2 * s.HeightMapUnits
}

// ChromaArrayType is ChromaArrayType: 0 for monochrome or separately
// coded planes, else chroma_format_idc.
func (s *H264SPS) ChromaArrayType() uint8 {
	if s.SeparateColourPlane {
		return 0
	}
	return s.ChromaFormatIdc
}

// CropRect is the picture inside the coded frame, in luma samples.
func (s *H264SPS) CropRect() (x0, y0, x1, y1 int) {
	w, h := 16*s.WidthMbs, 16*s.FrameHeightMbs()
	if !s.FrameCropping {
		return 0, 0, w, h
	}
	field := 1
	if !s.FrameMbsOnly {
		field = 2
	}
	unitX, unitY := 1, field
	switch s.ChromaArrayType() {
	case 1:
		unitX, unitY = 2, 2*field
	case 2:
		unitX = 2
	}
	return unitX * int(s.Crop[0]), unitY * int(s.Crop[2]), w - unitX*int(s.Crop[1]), h - unitY*int(s.Crop[3])
}

// H264PPS is a picture parameter set (7.3.2.2).
type H264PPS struct {
	ID, SPSID                         uint8
	EntropyCodingMode                 bool
	BottomFieldPicOrderInFramePresent bool
	NumRefIdxL0DefaultActiveMinus1    uint8
	NumRefIdxL1DefaultActiveMinus1    uint8
	WeightedPred                      bool
	WeightedBipredIdc                 uint8
	PicInitQpMinus26                  int8
	PicInitQsMinus26                  int8
	ChromaQpIndexOffset               int8
	DeblockingFilterControlPresent    bool
	ConstrainedIntraPred              bool
	RedundantPicCntPresent            bool
	Transform8x8Mode                  bool
	ScalingMatrixPresent              bool
	SecondChromaQpIndexOffset         int8
	// Scaling are the picture's scaling lists, resolved against its
	// SPS's: meaningful when ScalingMatrixPresent.
	Scaling H264ScalingLists
}

// Parse reads the PPS from rbsp as H264SPS.Parse does; sps are the
// sequence parameter sets by ID, for the one it names.
func (p *H264PPS) Parse(rbsp []byte, sps *[32]*H264SPS) error {
	r := NewReader(rbsp)
	id, spsID := r.UE(), r.UE()
	if id > 255 || spsID > 31 {
		return fmt.Errorf("%w PPS: pic_parameter_set_id %d or seq_parameter_set_id %d out of range", ErrH264, id, spsID)
	}
	s := sps[spsID]
	if s == nil {
		return fmt.Errorf("%w PPS %d: names SPS %d, which the stream has not sent", ErrH264, id, spsID)
	}
	*p = H264PPS{ID: uint8(id), SPSID: uint8(spsID)}
	p.EntropyCodingMode = r.Flag()
	p.BottomFieldPicOrderInFramePresent = r.Flag()
	if groups := r.UE(); groups != 0 {
		return fmt.Errorf("%w PPS %d: %d slice groups (flexible macroblock ordering) are not decoded", ErrH264, id, groups+1)
	}
	l0, l1 := r.UE(), r.UE()
	if l0 > 31 || l1 > 31 {
		return fmt.Errorf("%w PPS %d: default reference counts %d and %d are past 32", ErrH264, id, l0+1, l1+1)
	}
	p.NumRefIdxL0DefaultActiveMinus1, p.NumRefIdxL1DefaultActiveMinus1 = uint8(l0), uint8(l1)
	p.WeightedPred = r.Flag()
	p.WeightedBipredIdc = r.U8(2)
	qp, qs, cqp := r.SE(), r.SE(), r.SE()
	if qp < -26-6*int32(s.BitDepthLumaMinus8) || qp > 25 || qs < -26 || qs > 25 || cqp < -12 || cqp > 12 {
		return fmt.Errorf("%w PPS %d: quantiser offsets %d, %d, %d out of range", ErrH264, id, qp, qs, cqp)
	}
	p.PicInitQpMinus26, p.PicInitQsMinus26, p.ChromaQpIndexOffset = intconv.Must[int8](qp), intconv.Must[int8](qs), intconv.Must[int8](cqp)
	p.SecondChromaQpIndexOffset = p.ChromaQpIndexOffset
	p.DeblockingFilterControlPresent = r.Flag()
	p.ConstrainedIntraPred = r.Flag()
	p.RedundantPicCntPresent = r.Flag()
	if r.MoreRBSPData() {
		p.Transform8x8Mode = r.Flag()
		if p.ScalingMatrixPresent = r.Flag(); p.ScalingMatrixPresent {
			n := 6
			if p.Transform8x8Mode {
				n += 2
				if s.ChromaFormatIdc == 3 {
					n += 4
				}
			}
			var seq *H264ScalingLists
			if s.ScalingMatrixPresent {
				seq = &s.Scaling
			}
			p.Scaling.parse(&r, n, seq)
		}
		second := r.SE()
		if second < -12 || second > 12 {
			return fmt.Errorf("%w PPS %d: second_chroma_qp_index_offset %d out of range", ErrH264, id, second)
		}
		p.SecondChromaQpIndexOffset = int8(second)
	}
	if r.Short() {
		return fmt.Errorf("%w PPS %d: ends inside its syntax", ErrH264, id)
	}
	return nil
}

// H264MaxMMCO bounds the memory management control operations a slice
// header carries: one per reference frame and field, and two more.
const H264MaxMMCO = 66

// H264MMCO is one memory_management_control_operation and its
// arguments (7.3.3.3).
type H264MMCO struct {
	Op                        uint8
	DifferenceOfPicNumsMinus1 uint32
	LongTermPicNum            uint32
	LongTermFrameIdx          uint32
	MaxLongTermFrameIdxPlus1  uint32
}

// H264SliceHeader is what a slice header (7.3.3) says that a decoder
// managing the reference pictures uses.
type H264SliceHeader struct {
	FirstMbInSlice uint32
	// SliceType is slice_type modulo 5.
	SliceType              uint8
	PPSID                  uint8
	FrameNum               uint32
	FieldPic, BottomField  bool
	IDRPicID               uint16
	PicOrderCntLsb         uint32
	DeltaPicOrderCntBottom int32
	DeltaPicOrderCnt       [2]int32
	NoOutputOfPriorPics    bool
	LongTermReference      bool
	AdaptiveRefPicMarking  bool
	NumMMCO                int
	MMCO                   [H264MaxMMCO]H264MMCO
}

// ParseType reads only first_mb_in_slice, slice_type and
// pic_parameter_set_id: enough to tell a picture's slices apart.
func (h *H264SliceHeader) ParseType(rbsp []byte) error {
	r := NewReader(rbsp)
	return h.parseType(&r)
}

func (h *H264SliceHeader) parseType(r *Reader) error {
	h.FirstMbInSlice = r.UE()
	t := r.UE()
	pps := r.UE()
	if t > 9 || pps > 255 || r.Short() {
		return fmt.Errorf("%w slice: slice_type %d or pic_parameter_set_id %d out of range", ErrH264, t, pps)
	}
	h.SliceType, h.PPSID = uint8(t%5), uint8(pps)
	return nil
}

// Parse reads the slice header from rbsp, the slice NAL unit's payload
// after its header byte (unit[0]) with emulation prevention removed,
// as far as its reference picture marking: a prefix of the payload
// holding the header is enough. pps and sps are the parameter sets by
// ID.
func (h *H264SliceHeader) Parse(rbsp []byte, header byte, pps *[256]*H264PPS, sps *[32]*H264SPS) error {
	r := NewReader(rbsp)
	if err := h.parseType(&r); err != nil {
		return err
	}
	p := pps[h.PPSID]
	if p == nil {
		return fmt.Errorf("%w slice: names PPS %d, which the stream has not sent", ErrH264, h.PPSID)
	}
	s := sps[p.SPSID]
	if s == nil {
		return fmt.Errorf("%w slice: PPS %d names SPS %d, which the stream has not sent", ErrH264, p.ID, p.SPSID)
	}
	idr := header&0x1f == H264NALIDR
	if s.SeparateColourPlane {
		r.Skip(2)
	}
	h.FrameNum = r.U(int(s.Log2MaxFrameNum))
	h.FieldPic, h.BottomField = false, false
	if !s.FrameMbsOnly {
		if h.FieldPic = r.Flag(); h.FieldPic {
			h.BottomField = r.Flag()
		}
	}
	h.IDRPicID = 0
	if idr {
		id := r.UE()
		if id > 65535 {
			return fmt.Errorf("%w slice: idr_pic_id %d is past 65535", ErrH264, id)
		}
		h.IDRPicID = uint16(id)
	}
	h.PicOrderCntLsb, h.DeltaPicOrderCntBottom, h.DeltaPicOrderCnt = 0, 0, [2]int32{}
	switch s.PicOrderCntType {
	case 0:
		h.PicOrderCntLsb = r.U(int(s.Log2MaxPicOrderCntLsb))
		if p.BottomFieldPicOrderInFramePresent && !h.FieldPic {
			h.DeltaPicOrderCntBottom = r.SE()
		}
	case 1:
		if !s.DeltaPicOrderAlwaysZero {
			h.DeltaPicOrderCnt[0] = r.SE()
			if p.BottomFieldPicOrderInFramePresent && !h.FieldPic {
				h.DeltaPicOrderCnt[1] = r.SE()
			}
		}
	}
	if p.RedundantPicCntPresent {
		r.UE()
	}
	if h.SliceType == H264SliceB {
		r.Skip(1)
	}
	l0, l1 := uint32(p.NumRefIdxL0DefaultActiveMinus1), uint32(p.NumRefIdxL1DefaultActiveMinus1)
	switch h.SliceType {
	case H264SliceP, H264SliceSP, H264SliceB:
		if r.Flag() {
			l0 = r.UE()
			if h.SliceType == H264SliceB {
				l1 = r.UE()
			}
		}
		if l0 > 31 || l1 > 31 {
			return fmt.Errorf("%w slice: reference counts %d and %d are past 32", ErrH264, l0+1, l1+1)
		}
		skipRefPicListModification(&r)
		if h.SliceType == H264SliceB {
			skipRefPicListModification(&r)
		}
	}
	if p.WeightedPred && (h.SliceType == H264SliceP || h.SliceType == H264SliceSP) || p.WeightedBipredIdc == 1 && h.SliceType == H264SliceB {
		skipPredWeightTable(&r, s.ChromaArrayType() != 0, l0, l1, h.SliceType == H264SliceB)
	}
	h.NoOutputOfPriorPics, h.LongTermReference, h.AdaptiveRefPicMarking, h.NumMMCO = false, false, false, 0
	if header>>5&3 != 0 {
		if err := h.parseMarking(&r, idr); err != nil {
			return err
		}
	}
	if r.Short() {
		return fmt.Errorf("%w slice: header ends inside its syntax", ErrH264)
	}
	return nil
}

// skipRefPicListModification reads past one list's
// ref_pic_list_modification (7.3.3.1).
func skipRefPicListModification(r *Reader) {
	if !r.Flag() {
		return
	}
	for range 33 {
		op := r.UE()
		if op == 3 || r.Short() {
			return
		}
		r.UE()
	}
	r.short = true
}

// skipPredWeightTable reads past pred_weight_table (7.3.3.2).
func skipPredWeightTable(r *Reader, chroma bool, l0, l1 uint32, b bool) {
	r.UE()
	if chroma {
		r.UE()
	}
	lists := []uint32{l0}
	if b {
		lists = append(lists[:1:1], l1)
	}
	for _, n := range lists {
		for range n + 1 {
			if r.Flag() {
				r.SE()
				r.SE()
			}
			if chroma && r.Flag() {
				for range 4 {
					r.SE()
				}
			}
		}
	}
}

// parseMarking reads dec_ref_pic_marking (7.3.3.3).
func (h *H264SliceHeader) parseMarking(r *Reader, idr bool) error {
	if idr {
		h.NoOutputOfPriorPics, h.LongTermReference = r.Flag(), r.Flag()
		return nil
	}
	if h.AdaptiveRefPicMarking = r.Flag(); !h.AdaptiveRefPicMarking {
		return nil
	}
	for {
		op := r.UE()
		if op == 0 || r.Short() {
			return nil
		}
		if op > 6 {
			return fmt.Errorf("%w slice: memory_management_control_operation %d is past 6", ErrH264, op)
		}
		if h.NumMMCO == H264MaxMMCO {
			return fmt.Errorf("%w slice: more than %d memory management control operations", ErrH264, H264MaxMMCO)
		}
		m := H264MMCO{Op: uint8(op)}
		if op == 1 || op == 3 {
			m.DifferenceOfPicNumsMinus1 = r.UE()
		}
		if op == 2 {
			m.LongTermPicNum = r.UE()
		}
		if op == 3 || op == 6 {
			m.LongTermFrameIdx = r.UE()
		}
		if op == 4 {
			m.MaxLongTermFrameIdxPlus1 = r.UE()
		}
		h.MMCO[h.NumMMCO] = m
		h.NumMMCO++
	}
}
