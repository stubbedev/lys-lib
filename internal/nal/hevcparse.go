package nal

import (
	"errors"
	"fmt"
	"math/bits"

	"github.com/stubbedev/lys-lib/intconv"
)

// The H.265 NAL unit types a decoder acts on (Table 7-1).
const (
	HEVCNALRADLN   = 6
	HEVCNALRASLN   = 8
	HEVCNALRASLR   = 9
	HEVCNALBLAWLP  = 16
	HEVCNALIDRWRAD = 19
	HEVCNALIDRNLP  = 20
	HEVCNALCRA     = 21
	HEVCNALIRAPMax = 23
	HEVCNALVPS     = 32
	HEVCNALSPS     = 33
	HEVCNALPPS     = 34
	HEVCNALAUD     = 35
)

// HEVCType is an H.265 NAL unit's nal_unit_type.
func HEVCType(unit []byte) uint8 { return unit[0] >> 1 & 0x3f }

// HEVCTemporalID is an H.265 NAL unit's TemporalId; unit is at least
// its two header bytes.
func HEVCTemporalID(unit []byte) uint8 { return max(unit[1]&7, 1) - 1 }

// The H.265 slice types (Table 7-7).
const (
	HEVCSliceB = 0
	HEVCSliceP = 1
	HEVCSliceI = 2
)

// ErrHEVC is an H.265 header a decoder cannot read: malformed, or
// using syntax outside what it decodes. Errors wrap it with what was
// read.
var ErrHEVC = errors.New("nal: H.265")

// HEVCMaxSubLayers is the sub-layers a stream may have.
const HEVCMaxSubLayers = 7

// HEVCProfileTierLevel is a profile_tier_level's general part (7.3.3).
type HEVCProfileTierLevel struct {
	ProfileSpace uint8
	Tier         bool
	ProfileIdc   uint8
	// Compatibility is general_profile_compatibility_flag, flag 0 in
	// bit 31.
	Compatibility uint32
	Progressive   bool
	Interlaced    bool
	NonPacked     bool
	FrameOnly     bool
	LevelIdc      uint8
}

func (p *HEVCProfileTierLevel) parse(r *Reader, subLayers int) {
	p.ProfileSpace = r.U8(2)
	p.Tier = r.Flag()
	p.ProfileIdc = r.U8(5)
	p.Compatibility = r.U(32)
	p.Progressive, p.Interlaced, p.NonPacked, p.FrameOnly = r.Flag(), r.Flag(), r.Flag(), r.Flag()
	r.Skip(44)
	p.LevelIdc = r.U8(8)
	var profile, level [HEVCMaxSubLayers]bool
	for i := range subLayers {
		profile[i], level[i] = r.Flag(), r.Flag()
	}
	if subLayers > 0 {
		r.Skip(2 * (8 - subLayers))
	}
	for i := range subLayers {
		if profile[i] {
			r.Skip(88)
		}
		if level[i] {
			r.Skip(8)
		}
	}
}

// HEVCSubLayerOrdering is the DPB size, reordering and latency of each
// sub-layer, the ones not coded filled in from the highest.
type HEVCSubLayerOrdering struct {
	MaxDecPicBufferingMinus1 [HEVCMaxSubLayers]uint8
	MaxNumReorderPics        [HEVCMaxSubLayers]uint8
	MaxLatencyIncreasePlus1  [HEVCMaxSubLayers]uint32
}

func (o *HEVCSubLayerOrdering) parse(r *Reader, present bool, maxSubLayersMinus1 int) error {
	first := maxSubLayersMinus1
	if present {
		first = 0
	}
	for i := first; i <= maxSubLayersMinus1; i++ {
		dpb, reorder, latency := r.UE(), r.UE(), r.UE()
		if dpb > 15 || reorder > dpb {
			return fmt.Errorf("%w: sub-layer %d DPB of %d pictures reordering %d", ErrHEVC, i, dpb+1, reorder)
		}
		o.MaxDecPicBufferingMinus1[i], o.MaxNumReorderPics[i], o.MaxLatencyIncreasePlus1[i] = intconv.Must[uint8](dpb), intconv.Must[uint8](reorder), latency
	}
	for i := range first {
		o.MaxDecPicBufferingMinus1[i], o.MaxNumReorderPics[i], o.MaxLatencyIncreasePlus1[i] = o.MaxDecPicBufferingMinus1[first], o.MaxNumReorderPics[first], o.MaxLatencyIncreasePlus1[first]
	}
	return nil
}

// HEVCVPS is a video parameter set (7.3.2.1), as far as its timing.
type HEVCVPS struct {
	ID                          uint8
	MaxSubLayersMinus1          uint8
	TemporalIDNesting           bool
	PTL                         HEVCProfileTierLevel
	SubLayerOrderingInfoPresent bool
	Ordering                    HEVCSubLayerOrdering
	TimingInfoPresent           bool
	NumUnitsInTick, TimeScale   uint32
	PocProportionalToTiming     bool
	NumTicksPocDiffOneMinus1    uint32
}

// Parse reads the VPS from rbsp, the NAL unit's payload after its two
// header bytes with emulation prevention removed.
func (v *HEVCVPS) Parse(rbsp []byte) error {
	r := NewReader(rbsp)
	*v = HEVCVPS{ID: r.U8(4)}
	r.Skip(8)
	v.MaxSubLayersMinus1 = r.U8(3)
	if v.MaxSubLayersMinus1 >= HEVCMaxSubLayers {
		return fmt.Errorf("%w VPS %d: %d sub-layers", ErrHEVC, v.ID, v.MaxSubLayersMinus1+1)
	}
	v.TemporalIDNesting = r.Flag()
	r.Skip(16)
	v.PTL.parse(&r, int(v.MaxSubLayersMinus1))
	v.SubLayerOrderingInfoPresent = r.Flag()
	if err := v.Ordering.parse(&r, v.SubLayerOrderingInfoPresent, int(v.MaxSubLayersMinus1)); err != nil {
		return fmt.Errorf("VPS %d: %w", v.ID, err)
	}
	maxLayerID, sets := int(r.U(6)), r.UE()
	if sets > 1023 {
		return fmt.Errorf("%w VPS %d: %d layer sets", ErrHEVC, v.ID, sets+1)
	}
	r.Skip(int(sets) * (maxLayerID + 1))
	if v.TimingInfoPresent = r.Flag(); v.TimingInfoPresent {
		v.NumUnitsInTick, v.TimeScale = r.U(32), r.U(32)
		if v.PocProportionalToTiming = r.Flag(); v.PocProportionalToTiming {
			v.NumTicksPocDiffOneMinus1 = r.UE()
		}
	}
	if r.Short() {
		return fmt.Errorf("%w VPS %d: ends inside its syntax", ErrHEVC, v.ID)
	}
	return nil
}

// HEVCScalingLists are a scaling_list_data's lists in the order the
// bitstream codes them (up-right diagonal), every list resolved: coded,
// a default, or copied from the list it names. List32x32 holds
// matrices 0 and 3 of size 3.
type HEVCScalingLists struct {
	List4x4   [6][16]uint8
	List8x8   [6][64]uint8
	List16x16 [6][64]uint8
	List32x32 [2][64]uint8
	DC16x16   [6]uint8
	DC32x32   [2]uint8
}

// The default 8x8 lists (Table 7-6), up-right diagonal order, which the
// larger sizes upsample.
var (
	hevcDefaultIntra = [64]uint8{
		16, 16, 16, 16, 16, 16, 16, 16, 16, 16, 17, 16, 17, 16, 17, 18,
		17, 18, 18, 17, 18, 21, 19, 20, 21, 20, 19, 21, 24, 22, 22, 24,
		24, 22, 22, 24, 25, 25, 27, 30, 27, 25, 25, 29, 31, 35, 35, 31,
		29, 36, 41, 44, 41, 36, 47, 54, 54, 47, 65, 70, 65, 88, 88, 115,
	}
	hevcDefaultInter = [64]uint8{
		16, 16, 16, 16, 16, 16, 16, 16, 16, 16, 17, 17, 17, 17, 17, 18,
		18, 18, 18, 18, 18, 20, 20, 20, 20, 20, 20, 20, 24, 24, 24, 24,
		24, 24, 24, 24, 25, 25, 25, 25, 25, 25, 25, 28, 28, 28, 28, 28,
		28, 33, 33, 33, 33, 33, 41, 41, 41, 41, 54, 54, 54, 71, 71, 91,
	}
)

// list is matrix m of size s, and its DC coefficient for sizes 2 and 3
// (nil below).
func (l *HEVCScalingLists) list(s, m int) ([]uint8, *uint8) {
	switch s {
	case 0:
		return l.List4x4[m][:], nil
	case 1:
		return l.List8x8[m][:], nil
	case 2:
		return l.List16x16[m][:], &l.DC16x16[m]
	}
	return l.List32x32[m/3][:], &l.DC32x32[m/3]
}

// setDefault sets matrix m of size s to its default.
func (l *HEVCScalingLists) setDefault(s, m int) {
	list, dc := l.list(s, m)
	switch {
	case s == 0:
		for i := range list {
			list[i] = 16
		}
	case m < 3:
		copy(list, hevcDefaultIntra[:])
	default:
		copy(list, hevcDefaultInter[:])
	}
	if dc != nil {
		*dc = 16
	}
}

// Defaults sets every list to its default: what a stream that enables
// scaling lists without coding them uses.
func (l *HEVCScalingLists) Defaults() {
	for s := range 4 {
		for m := 0; m < 6; m += hevcMatrixStep(s) {
			l.setDefault(s, m)
		}
	}
}

func hevcMatrixStep(s int) int {
	if s == 3 {
		return 3
	}
	return 1
}

// parse reads scaling_list_data (7.3.4).
func (l *HEVCScalingLists) parse(r *Reader) error {
	for s := range 4 {
		step := hevcMatrixStep(s)
		for m := 0; m < 6; m += step {
			list, dc := l.list(s, m)
			if !r.Flag() {
				delta := int(r.UE())
				if delta == 0 {
					l.setDefault(s, m)
					continue
				}
				ref := m - delta*step
				if ref < 0 {
					return fmt.Errorf("%w: scaling list %d of size %d copies list %d", ErrHEVC, m, s, ref)
				}
				src, srcDC := l.list(s, ref)
				copy(list, src)
				if dc != nil {
					*dc = *srcDC
				}
				continue
			}
			next := int32(8)
			if s > 1 {
				d := r.SE()
				if d < -7 || d > 247 {
					return fmt.Errorf("%w: scaling list DC coefficient %d out of range", ErrHEVC, d+8)
				}
				next = d + 8
				*dc = uint8(next & 0xff)
			}
			for i := range list {
				next = (next + r.SE() + 256) % 256
				list[i] = uint8(next & 0xff)
			}
		}
	}
	return nil
}

// HEVCShortTermRPS is a short-term reference picture set
// (st_ref_pic_set, 7.3.7): its syntax and the delta POCs it works out
// to (7.4.8).
type HEVCShortTermRPS struct {
	InterRPSPred      bool
	DeltaIdxMinus1    uint32
	DeltaRPSSign      bool
	AbsDeltaRPSMinus1 uint32
	// UsedByCurrPicFlag and UseDeltaFlag are the flags of each entry of
	// the set it predicts from, entry j in bit j.
	UsedByCurrPicFlag uint32
	UseDeltaFlag      uint32
	NumNegative       uint8
	NumPositive       uint8
	DeltaPocS0        [16]int32
	DeltaPocS1        [16]int32
	// UsedS0 and UsedS1 mark the pictures the current one references,
	// entry i in bit i.
	UsedS0, UsedS1 uint16
}

// NumDeltaPocs is how many pictures the set names.
func (s *HEVCShortTermRPS) NumDeltaPocs() int { return int(s.NumNegative) + int(s.NumPositive) }

// parse reads set idx of num the SPS codes, sets being the ones before
// it; idx == num is the one a slice header codes.
func (s *HEVCShortTermRPS) parse(r *Reader, idx, num int, sets []HEVCShortTermRPS) error {
	*s = HEVCShortTermRPS{}
	if idx != 0 {
		s.InterRPSPred = r.Flag()
	}
	if !s.InterRPSPred {
		return s.parseExplicit(r)
	}
	if idx == num {
		d := r.UE()
		if int64(d) >= int64(idx) {
			return fmt.Errorf("%w: reference picture set predicts from %d sets back of %d", ErrHEVC, d+1, idx)
		}
		s.DeltaIdxMinus1 = d
	}
	ref := &sets[idx-int(s.DeltaIdxMinus1)-1]
	s.DeltaRPSSign = r.Flag()
	s.AbsDeltaRPSMinus1 = r.UE()
	if s.AbsDeltaRPSMinus1 > 1<<15-1 {
		return fmt.Errorf("%w: reference picture set delta %d out of range", ErrHEVC, s.AbsDeltaRPSMinus1+1)
	}
	n := ref.NumDeltaPocs()
	for j := range n + 1 {
		used, useDelta := r.Flag(), true
		if !used {
			useDelta = r.Flag()
		}
		if used {
			s.UsedByCurrPicFlag |= 1 << j
		}
		if useDelta {
			s.UseDeltaFlag |= 1 << j
		}
	}
	delta := int32(s.AbsDeltaRPSMinus1) + 1
	if s.DeltaRPSSign {
		delta = -delta
	}
	neg := int(ref.NumNegative)
	add := func(negative bool, d int32, j int) {
		if s.UseDeltaFlag&(1<<j) == 0 || (d < 0) != negative || d == 0 {
			return
		}
		used := s.UsedByCurrPicFlag&(1<<j) != 0
		if negative {
			if used {
				s.UsedS0 |= 1 << s.NumNegative
			}
			s.DeltaPocS0[s.NumNegative%16] = d
			s.NumNegative++
			return
		}
		if used {
			s.UsedS1 |= 1 << s.NumPositive
		}
		s.DeltaPocS1[s.NumPositive%16] = d
		s.NumPositive++
	}
	for j := int(ref.NumPositive) - 1; j >= 0; j-- {
		add(true, ref.DeltaPocS1[j]+delta, neg+j)
	}
	add(true, delta, n)
	for j := range neg {
		add(true, ref.DeltaPocS0[j]+delta, j)
	}
	for j := neg - 1; j >= 0; j-- {
		add(false, ref.DeltaPocS0[j]+delta, j)
	}
	add(false, delta, n)
	for j := range int(ref.NumPositive) {
		add(false, ref.DeltaPocS1[j]+delta, neg+j)
	}
	if s.NumDeltaPocs() > 16 {
		return fmt.Errorf("%w: reference picture set of %d pictures", ErrHEVC, s.NumDeltaPocs())
	}
	return nil
}

func (s *HEVCShortTermRPS) parseExplicit(r *Reader) error {
	neg, pos := r.UE(), r.UE()
	if neg > 16 || pos > 16-neg {
		return fmt.Errorf("%w: reference picture set of %d and %d pictures", ErrHEVC, neg, pos)
	}
	s.NumNegative, s.NumPositive = intconv.Must[uint8](neg), intconv.Must[uint8](pos)
	var poc int32
	for i := range neg {
		d := r.UE()
		if d > 1<<15-1 {
			return fmt.Errorf("%w: reference picture delta %d out of range", ErrHEVC, d+1)
		}
		poc -= int32(d) + 1
		s.DeltaPocS0[i] = poc
		if r.Flag() {
			s.UsedS0 |= 1 << i
		}
	}
	poc = 0
	for i := range pos {
		d := r.UE()
		if d > 1<<15-1 {
			return fmt.Errorf("%w: reference picture delta %d out of range", ErrHEVC, d+1)
		}
		poc += int32(d) + 1
		s.DeltaPocS1[i] = poc
		if r.Flag() {
			s.UsedS1 |= 1 << i
		}
	}
	return nil
}

// HEVCVUI is what an SPS's video usability information says (E.2.1),
// its HRD parameters read past.
type HEVCVUI struct {
	AspectRatioInfoPresent         bool
	AspectRatioIdc                 uint8
	SarWidth, SarHeight            uint16
	OverscanInfoPresent            bool
	OverscanAppropriate            bool
	VideoSignalTypePresent         bool
	VideoFormat                    uint8
	FullRange                      bool
	ColourDescriptionPresent       bool
	ColourPrimaries                uint8
	TransferCharacteristics        uint8
	MatrixCoeffs                   uint8
	ChromaLocInfoPresent           bool
	ChromaSampleLocTypeTop         uint8
	ChromaSampleLocTypeBot         uint8
	NeutralChromaIndication        bool
	FieldSeq                       bool
	FrameFieldInfoPresent          bool
	DefaultDisplayWindow           bool
	DefDispWin                     [4]uint16
	TimingInfoPresent              bool
	NumUnitsInTick, TimeScale      uint32
	PocProportionalToTiming        bool
	NumTicksPocDiffOneMinus1       uint32
	HRDParametersPresent           bool
	BitstreamRestriction           bool
	TilesFixedStructure            bool
	MotionVectorsOverPicBoundaries bool
	RestrictedRefPicLists          bool
	MinSpatialSegmentationIdc      uint16
	MaxBytesPerPicDenom            uint8
	MaxBitsPerMinCuDenom           uint8
	Log2MaxMvLengthHorizontal      uint8
	Log2MaxMvLengthVertical        uint8
}

func (v *HEVCVUI) parse(r *Reader, maxSubLayersMinus1 int) {
	if v.AspectRatioInfoPresent = r.Flag(); v.AspectRatioInfoPresent {
		if v.AspectRatioIdc = r.U8(8); v.AspectRatioIdc == 255 {
			v.SarWidth, v.SarHeight = r.U16(16), r.U16(16)
		}
	}
	if v.OverscanInfoPresent = r.Flag(); v.OverscanInfoPresent {
		v.OverscanAppropriate = r.Flag()
	}
	if v.VideoSignalTypePresent = r.Flag(); v.VideoSignalTypePresent {
		v.VideoFormat, v.FullRange = r.U8(3), r.Flag()
		if v.ColourDescriptionPresent = r.Flag(); v.ColourDescriptionPresent {
			v.ColourPrimaries, v.TransferCharacteristics, v.MatrixCoeffs = r.U8(8), r.U8(8), r.U8(8)
		}
	}
	if v.ChromaLocInfoPresent = r.Flag(); v.ChromaLocInfoPresent {
		v.ChromaSampleLocTypeTop, v.ChromaSampleLocTypeBot = uint8(min(r.UE(), 255)), uint8(min(r.UE(), 255))
	}
	v.NeutralChromaIndication, v.FieldSeq, v.FrameFieldInfoPresent = r.Flag(), r.Flag(), r.Flag()
	if v.DefaultDisplayWindow = r.Flag(); v.DefaultDisplayWindow {
		for i := range v.DefDispWin {
			v.DefDispWin[i] = uint16(min(r.UE(), 65535))
		}
	}
	if v.TimingInfoPresent = r.Flag(); v.TimingInfoPresent {
		v.NumUnitsInTick, v.TimeScale = r.U(32), r.U(32)
		if v.PocProportionalToTiming = r.Flag(); v.PocProportionalToTiming {
			v.NumTicksPocDiffOneMinus1 = r.UE()
		}
		if v.HRDParametersPresent = r.Flag(); v.HRDParametersPresent {
			skipHEVCHRD(r, maxSubLayersMinus1)
		}
	}
	if v.BitstreamRestriction = r.Flag(); v.BitstreamRestriction {
		v.TilesFixedStructure, v.MotionVectorsOverPicBoundaries, v.RestrictedRefPicLists = r.Flag(), r.Flag(), r.Flag()
		v.MinSpatialSegmentationIdc = uint16(min(r.UE(), 4095))
		v.MaxBytesPerPicDenom, v.MaxBitsPerMinCuDenom = uint8(min(r.UE(), 16)), uint8(min(r.UE(), 16))
		v.Log2MaxMvLengthHorizontal, v.Log2MaxMvLengthVertical = uint8(min(r.UE(), 16)), uint8(min(r.UE(), 16))
	}
}

// skipHEVCHRD reads past hrd_parameters(1, maxSubLayersMinus1) (E.2.2).
func skipHEVCHRD(r *Reader, maxSubLayersMinus1 int) {
	nal, vcl := r.Flag(), r.Flag()
	subPic := false
	if nal || vcl {
		if subPic = r.Flag(); subPic {
			r.Skip(8 + 5 + 1 + 5)
		}
		r.Skip(8)
		if subPic {
			r.Skip(4)
		}
		r.Skip(15)
	}
	for range maxSubLayersMinus1 + 1 {
		fixed := r.Flag()
		if !fixed {
			fixed = r.Flag()
		}
		lowDelay := false
		if fixed {
			r.UE()
		} else {
			lowDelay = r.Flag()
		}
		cpbs := uint32(1)
		if !lowDelay {
			cpbs = r.UE() + 1
		}
		if cpbs > 32 {
			r.short = true
			return
		}
		for range btoi(nal) + btoi(vcl) {
			for range cpbs {
				r.UE()
				r.UE()
				if subPic {
					r.UE()
					r.UE()
				}
				r.Skip(1)
			}
		}
	}
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}

// HEVCSPS is a sequence parameter set (7.3.2.2.1).
type HEVCSPS struct {
	VPSID                       uint8
	MaxSubLayersMinus1          uint8
	TemporalIDNesting           bool
	PTL                         HEVCProfileTierLevel
	ID                          uint8
	ChromaFormatIdc             uint8
	SeparateColourPlane         bool
	Width, Height               int
	ConformanceWindow           bool
	Conf                        [4]uint32
	BitDepthLumaMinus8          uint8
	BitDepthChromaMinus8        uint8
	Log2MaxPocLsb               uint8
	SubLayerOrderingInfoPresent bool
	Ordering                    HEVCSubLayerOrdering
	Log2MinCbMinus3             uint8
	Log2DiffMaxMinCb            uint8
	Log2MinTbMinus2             uint8
	Log2DiffMaxMinTb            uint8
	MaxTransformDepthInter      uint8
	MaxTransformDepthIntra      uint8
	ScalingListEnabled          bool
	ScalingListDataPresent      bool
	// Scaling are the sequence's lists: coded, else the defaults.
	Scaling                 HEVCScalingLists
	AMP, SAO                bool
	PCM                     bool
	PCMBitDepthLumaMinus1   uint8
	PCMBitDepthChromaMinus1 uint8
	Log2MinPCMCbMinus3      uint8
	Log2DiffMaxMinPCMCb     uint8
	PCMLoopFilterDisabled   bool
	NumShortTermRPS         uint8
	ShortTermRPS            [64]HEVCShortTermRPS
	LongTermRefPicsPresent  bool
	NumLongTermRefPicsSPS   uint8
	LtRefPicPocLsbSPS       [32]uint32
	// UsedByCurrPicLtSPS marks entry i in bit i.
	UsedByCurrPicLtSPS       uint32
	TemporalMVP              bool
	StrongIntraSmoothing     bool
	VUIPresent               bool
	VUI                      HEVCVUI
	ExtensionPresent         bool
	RangeExtension           bool
	TransformSkipRotation    bool
	TransformSkipContext     bool
	ImplicitRDPCM            bool
	ExplicitRDPCM            bool
	ExtendedPrecision        bool
	IntraSmoothingDisabled   bool
	HighPrecisionOffsets     bool
	PersistentRiceAdaptation bool
	CabacBypassAlignment     bool
}

// Parse reads the SPS from rbsp as HEVCVPS.Parse does.
func (s *HEVCSPS) Parse(rbsp []byte) error {
	r := NewReader(rbsp)
	*s = HEVCSPS{VPSID: r.U8(4), MaxSubLayersMinus1: r.U8(3)}
	if s.MaxSubLayersMinus1 >= HEVCMaxSubLayers {
		return fmt.Errorf("%w SPS: %d sub-layers", ErrHEVC, s.MaxSubLayersMinus1+1)
	}
	s.TemporalIDNesting = r.Flag()
	s.PTL.parse(&r, int(s.MaxSubLayersMinus1))
	id := r.UE()
	if id > 15 {
		return fmt.Errorf("%w SPS: sps_seq_parameter_set_id %d is past 15", ErrHEVC, id)
	}
	s.ID = uint8(id)
	chroma := r.UE()
	if chroma > 3 {
		return fmt.Errorf("%w SPS %d: chroma_format_idc %d is past 3", ErrHEVC, s.ID, chroma)
	}
	s.ChromaFormatIdc = uint8(chroma)
	if chroma == 3 {
		s.SeparateColourPlane = r.Flag()
	}
	w, h := r.UE(), r.UE()
	if w == 0 || h == 0 || w > 16888 || h > 16888 {
		return fmt.Errorf("%w SPS %d: %dx%d luma samples", ErrHEVC, s.ID, w, h)
	}
	s.Width, s.Height = int(w), int(h)
	if s.ConformanceWindow = r.Flag(); s.ConformanceWindow {
		for i := range s.Conf {
			s.Conf[i] = r.UE()
		}
	}
	luma, chromaDepth, lsb := r.UE(), r.UE(), r.UE()
	if luma > 8 || chromaDepth > 8 || lsb > 12 {
		return fmt.Errorf("%w SPS %d: bit depths %d and %d, log2_max_pic_order_cnt_lsb %d out of range", ErrHEVC, s.ID, luma+8, chromaDepth+8, lsb+4)
	}
	s.BitDepthLumaMinus8, s.BitDepthChromaMinus8, s.Log2MaxPocLsb = uint8(luma), uint8(chromaDepth), uint8(lsb+4)
	s.SubLayerOrderingInfoPresent = r.Flag()
	if err := s.Ordering.parse(&r, s.SubLayerOrderingInfoPresent, int(s.MaxSubLayersMinus1)); err != nil {
		return fmt.Errorf("SPS %d: %w", s.ID, err)
	}
	var sizes [6]uint32
	for i := range sizes {
		sizes[i] = r.UE()
	}
	if sizes[0] > 3 || sizes[1] > 3 || sizes[2] > 3 || sizes[3] > 3 || sizes[4] > 4 || sizes[5] > 4 {
		return fmt.Errorf("%w SPS %d: block sizes %v out of range", ErrHEVC, s.ID, sizes)
	}
	s.Log2MinCbMinus3, s.Log2DiffMaxMinCb, s.Log2MinTbMinus2, s.Log2DiffMaxMinTb = intconv.Must[uint8](sizes[0]), intconv.Must[uint8](sizes[1]), intconv.Must[uint8](sizes[2]), intconv.Must[uint8](sizes[3])
	s.MaxTransformDepthInter, s.MaxTransformDepthIntra = intconv.Must[uint8](sizes[4]), intconv.Must[uint8](sizes[5])
	if s.ScalingListEnabled = r.Flag(); s.ScalingListEnabled {
		s.Scaling.Defaults()
		if s.ScalingListDataPresent = r.Flag(); s.ScalingListDataPresent {
			if err := s.Scaling.parse(&r); err != nil {
				return fmt.Errorf("SPS %d: %w", s.ID, err)
			}
		}
	}
	s.AMP, s.SAO = r.Flag(), r.Flag()
	if s.PCM = r.Flag(); s.PCM {
		s.PCMBitDepthLumaMinus1, s.PCMBitDepthChromaMinus1 = r.U8(4), r.U8(4)
		s.Log2MinPCMCbMinus3, s.Log2DiffMaxMinPCMCb = uint8(min(r.UE(), 3)), uint8(min(r.UE(), 3))
		s.PCMLoopFilterDisabled = r.Flag()
	}
	n := r.UE()
	if n > 64 {
		return fmt.Errorf("%w SPS %d: %d short-term reference picture sets", ErrHEVC, s.ID, n)
	}
	s.NumShortTermRPS = uint8(n)
	for i := range int(n) {
		if err := s.ShortTermRPS[i].parse(&r, i, int(n), s.ShortTermRPS[:]); err != nil {
			return fmt.Errorf("SPS %d set %d: %w", s.ID, i, err)
		}
	}
	if s.LongTermRefPicsPresent = r.Flag(); s.LongTermRefPicsPresent {
		lt := r.UE()
		if lt > 32 {
			return fmt.Errorf("%w SPS %d: %d long-term reference pictures", ErrHEVC, s.ID, lt)
		}
		s.NumLongTermRefPicsSPS = uint8(lt)
		for i := range lt {
			s.LtRefPicPocLsbSPS[i] = r.U(int(s.Log2MaxPocLsb))
			if r.Flag() {
				s.UsedByCurrPicLtSPS |= 1 << i
			}
		}
	}
	s.TemporalMVP, s.StrongIntraSmoothing = r.Flag(), r.Flag()
	if s.VUIPresent = r.Flag(); s.VUIPresent {
		s.VUI.parse(&r, int(s.MaxSubLayersMinus1))
	}
	if s.ExtensionPresent = r.Flag(); s.ExtensionPresent {
		s.RangeExtension = r.Flag()
		if others := r.U(7); others != 0 {
			return fmt.Errorf("%w SPS %d: multilayer, 3D or screen content extensions (%#x) are not decoded", ErrHEVC, s.ID, others)
		}
		if s.RangeExtension {
			s.TransformSkipRotation, s.TransformSkipContext, s.ImplicitRDPCM = r.Flag(), r.Flag(), r.Flag()
			s.ExplicitRDPCM, s.ExtendedPrecision, s.IntraSmoothingDisabled = r.Flag(), r.Flag(), r.Flag()
			s.HighPrecisionOffsets, s.PersistentRiceAdaptation, s.CabacBypassAlignment = r.Flag(), r.Flag(), r.Flag()
		}
	}
	if r.Short() {
		return fmt.Errorf("%w SPS %d: ends inside its syntax", ErrHEVC, s.ID)
	}
	if x0, y0, x1, y1 := s.CropRect(); x1 <= x0 || y1 <= y0 {
		return fmt.Errorf("%w SPS %d: conformance window %v leaves no picture of %dx%d", ErrHEVC, s.ID, s.Conf, s.Width, s.Height)
	}
	return nil
}

// CropRect is the conformance window, in luma samples.
func (s *HEVCSPS) CropRect() (x0, y0, x1, y1 int) {
	subW, subH := 1, 1
	if !s.SeparateColourPlane {
		switch s.ChromaFormatIdc {
		case 1:
			subW, subH = 2, 2
		case 2:
			subW = 2
		}
	}
	return subW * int(s.Conf[0]), subH * int(s.Conf[2]), s.Width - subW*int(s.Conf[1]), s.Height - subH*int(s.Conf[3])
}

// HEVCPPS is a picture parameter set (7.3.2.3.1).
type HEVCPPS struct {
	ID, SPSID                       uint8
	DependentSliceSegments          bool
	OutputFlagPresent               bool
	NumExtraSliceHeaderBits         uint8
	SignDataHiding                  bool
	CabacInitPresent                bool
	NumRefIdxL0DefaultActiveMinus1  uint8
	NumRefIdxL1DefaultActiveMinus1  uint8
	InitQpMinus26                   int8
	ConstrainedIntraPred            bool
	TransformSkipEnabled            bool
	CuQpDeltaEnabled                bool
	DiffCuQpDeltaDepth              uint8
	CbQpOffset, CrQpOffset          int8
	SliceChromaQpOffsetsPresent     bool
	WeightedPred, WeightedBipred    bool
	TransquantBypassEnabled         bool
	TilesEnabled                    bool
	EntropyCodingSyncEnabled        bool
	NumTileColumnsMinus1            uint8
	NumTileRowsMinus1               uint8
	UniformSpacing                  bool
	ColumnWidthMinus1               [19]uint16
	RowHeightMinus1                 [21]uint16
	LoopFilterAcrossTiles           bool
	LoopFilterAcrossSlices          bool
	DeblockingFilterControlPresent  bool
	DeblockingFilterOverrideEnabled bool
	DeblockingFilterDisabled        bool
	BetaOffsetDiv2, TcOffsetDiv2    int8
	ScalingListDataPresent          bool
	// Scaling are the picture's coded lists: meaningful when
	// ScalingListDataPresent.
	Scaling                         HEVCScalingLists
	ListsModificationPresent        bool
	Log2ParallelMergeLevelMinus2    uint8
	SliceSegmentHeaderExtension     bool
	ExtensionPresent                bool
	RangeExtension                  bool
	Log2MaxTransformSkipBlockMinus2 uint8
	CrossComponentPrediction        bool
	ChromaQpOffsetListEnabled       bool
	DiffCuChromaQpOffsetDepth       uint8
	ChromaQpOffsetListLenMinus1     uint8
	CbQpOffsetList, CrQpOffsetList  [6]int8
	Log2SaoOffsetScaleLuma          uint8
	Log2SaoOffsetScaleChroma        uint8
}

// Parse reads the PPS from rbsp as HEVCVPS.Parse does; sps are the
// sequence parameter sets by ID, for the one it names.
func (p *HEVCPPS) Parse(rbsp []byte, sps *[16]*HEVCSPS) error {
	r := NewReader(rbsp)
	id, spsID := r.UE(), r.UE()
	if id > 63 || spsID > 15 {
		return fmt.Errorf("%w PPS: pps_pic_parameter_set_id %d or pps_seq_parameter_set_id %d out of range", ErrHEVC, id, spsID)
	}
	s := sps[spsID]
	if s == nil {
		return fmt.Errorf("%w PPS %d: names SPS %d, which the stream has not sent", ErrHEVC, id, spsID)
	}
	*p = HEVCPPS{ID: uint8(id), SPSID: uint8(spsID)}
	p.DependentSliceSegments, p.OutputFlagPresent = r.Flag(), r.Flag()
	p.NumExtraSliceHeaderBits = r.U8(3)
	p.SignDataHiding, p.CabacInitPresent = r.Flag(), r.Flag()
	l0, l1 := r.UE(), r.UE()
	qp := r.SE()
	if l0 > 14 || l1 > 14 || qp < -26-6*int32(s.BitDepthLumaMinus8) || qp > 25 {
		return fmt.Errorf("%w PPS %d: reference counts %d and %d, init_qp_minus26 %d out of range", ErrHEVC, id, l0+1, l1+1, qp)
	}
	p.NumRefIdxL0DefaultActiveMinus1, p.NumRefIdxL1DefaultActiveMinus1, p.InitQpMinus26 = uint8(l0), uint8(l1), intconv.Must[int8](qp)
	p.ConstrainedIntraPred, p.TransformSkipEnabled = r.Flag(), r.Flag()
	if p.CuQpDeltaEnabled = r.Flag(); p.CuQpDeltaEnabled {
		p.DiffCuQpDeltaDepth = uint8(min(r.UE(), 6))
	}
	cb, cr := r.SE(), r.SE()
	if cb < -12 || cb > 12 || cr < -12 || cr > 12 {
		return fmt.Errorf("%w PPS %d: chroma QP offsets %d and %d out of range", ErrHEVC, id, cb, cr)
	}
	p.CbQpOffset, p.CrQpOffset = intconv.Must[int8](cb), intconv.Must[int8](cr)
	p.SliceChromaQpOffsetsPresent = r.Flag()
	p.WeightedPred, p.WeightedBipred = r.Flag(), r.Flag()
	p.TransquantBypassEnabled = r.Flag()
	p.TilesEnabled, p.EntropyCodingSyncEnabled = r.Flag(), r.Flag()
	if p.TilesEnabled {
		if err := p.parseTiles(&r); err != nil {
			return fmt.Errorf("PPS %d: %w", id, err)
		}
	}
	p.LoopFilterAcrossSlices = r.Flag()
	if p.DeblockingFilterControlPresent = r.Flag(); p.DeblockingFilterControlPresent {
		p.DeblockingFilterOverrideEnabled = r.Flag()
		if p.DeblockingFilterDisabled = r.Flag(); !p.DeblockingFilterDisabled {
			beta, tc := r.SE(), r.SE()
			if beta < -6 || beta > 6 || tc < -6 || tc > 6 {
				return fmt.Errorf("%w PPS %d: deblocking offsets %d and %d out of range", ErrHEVC, id, beta, tc)
			}
			p.BetaOffsetDiv2, p.TcOffsetDiv2 = intconv.Must[int8](beta), intconv.Must[int8](tc)
		}
	}
	if p.ScalingListDataPresent = r.Flag(); p.ScalingListDataPresent {
		p.Scaling.Defaults()
		if err := p.Scaling.parse(&r); err != nil {
			return fmt.Errorf("PPS %d: %w", id, err)
		}
	}
	p.ListsModificationPresent = r.Flag()
	p.Log2ParallelMergeLevelMinus2 = uint8(min(r.UE(), 4))
	p.SliceSegmentHeaderExtension = r.Flag()
	if p.ExtensionPresent = r.Flag(); p.ExtensionPresent {
		p.RangeExtension = r.Flag()
		if others := r.U(7); others != 0 {
			return fmt.Errorf("%w PPS %d: multilayer, 3D or screen content extensions (%#x) are not decoded", ErrHEVC, id, others)
		}
		if p.RangeExtension {
			p.parseRange(&r)
		}
	}
	if r.Short() {
		return fmt.Errorf("%w PPS %d: ends inside its syntax", ErrHEVC, id)
	}
	return nil
}

func (p *HEVCPPS) parseTiles(r *Reader) error {
	cols, rows := r.UE(), r.UE()
	if cols > 18 || rows > 20 {
		return fmt.Errorf("%w: %dx%d tiles", ErrHEVC, cols+1, rows+1)
	}
	p.NumTileColumnsMinus1, p.NumTileRowsMinus1 = uint8(cols), uint8(rows)
	if p.UniformSpacing = r.Flag(); !p.UniformSpacing {
		for i := range cols {
			p.ColumnWidthMinus1[i] = uint16(min(r.UE(), 65535))
		}
		for i := range rows {
			p.RowHeightMinus1[i] = uint16(min(r.UE(), 65535))
		}
	}
	p.LoopFilterAcrossTiles = r.Flag()
	return nil
}

func (p *HEVCPPS) parseRange(r *Reader) {
	if p.TransformSkipEnabled {
		p.Log2MaxTransformSkipBlockMinus2 = uint8(min(r.UE(), 3))
	}
	p.CrossComponentPrediction = r.Flag()
	if p.ChromaQpOffsetListEnabled = r.Flag(); p.ChromaQpOffsetListEnabled {
		p.DiffCuChromaQpOffsetDepth = uint8(min(r.UE(), 6))
		p.ChromaQpOffsetListLenMinus1 = uint8(min(r.UE(), 5))
		for i := range int(p.ChromaQpOffsetListLenMinus1) + 1 {
			p.CbQpOffsetList[i], p.CrQpOffsetList[i] = int8(max(min(r.SE(), 12), -12)), int8(max(min(r.SE(), 12), -12))
		}
	}
	p.Log2SaoOffsetScaleLuma, p.Log2SaoOffsetScaleChroma = uint8(min(r.UE(), 6)), uint8(min(r.UE(), 6))
}

// HEVCLongTerm is one long-term reference picture a slice header names.
type HEVCLongTerm struct {
	PocLsb           uint32
	UsedByCurrPic    bool
	MsbPresent       bool
	DeltaPocMsbCycle uint32
}

// HEVCSliceHeader is what a slice segment header (7.3.6.1) says that a
// decoder managing the reference pictures uses.
type HEVCSliceHeader struct {
	FirstSliceSegmentInPic bool
	NoOutputOfPriorPics    bool
	PPSID                  uint8
	Dependent              bool
	SliceType              uint8
	PicOutput              bool
	PicOrderCntLsb         uint32
	ShortTermRefPicSetSPS  bool
	ShortTermRefPicSetIdx  uint8
	// RPS is the short-term set the header codes, STRPSBits its
	// length.
	RPS             HEVCShortTermRPS
	STRPSBits       int
	NumLongTermSPS  uint8
	NumLongTermPics uint8
	LongTerm        [32]HEVCLongTerm
}

// ShortTermRPS is the short-term reference picture set the slice uses.
func (h *HEVCSliceHeader) ShortTermRPS(s *HEVCSPS) *HEVCShortTermRPS {
	if h.ShortTermRefPicSetSPS {
		return &s.ShortTermRPS[h.ShortTermRefPicSetIdx]
	}
	return &h.RPS
}

// Parse reads the first slice segment header of a picture from rbsp,
// the NAL unit's payload after its two header bytes with emulation
// prevention removed, as far as its reference pictures: a prefix of
// the payload holding them is enough. nalType is its nal_unit_type;
// pps and sps the parameter sets by ID.
func (h *HEVCSliceHeader) Parse(rbsp []byte, nalType uint8, pps *[64]*HEVCPPS, sps *[16]*HEVCSPS) error {
	r := NewReader(rbsp)
	*h = HEVCSliceHeader{PicOutput: true}
	h.FirstSliceSegmentInPic = r.Flag()
	if !h.FirstSliceSegmentInPic {
		return fmt.Errorf("%w slice: the picture's first slice segment is not marked first", ErrHEVC)
	}
	if nalType >= HEVCNALBLAWLP && nalType <= HEVCNALIRAPMax {
		h.NoOutputOfPriorPics = r.Flag()
	}
	id := r.UE()
	if id > 63 {
		return fmt.Errorf("%w slice: slice_pic_parameter_set_id %d is past 63", ErrHEVC, id)
	}
	h.PPSID = uint8(id)
	p := pps[h.PPSID]
	if p == nil {
		return fmt.Errorf("%w slice: names PPS %d, which the stream has not sent", ErrHEVC, h.PPSID)
	}
	s := sps[p.SPSID]
	if s == nil {
		return fmt.Errorf("%w slice: PPS %d names SPS %d, which the stream has not sent", ErrHEVC, p.ID, p.SPSID)
	}
	r.Skip(int(p.NumExtraSliceHeaderBits))
	t := r.UE()
	if t > 2 {
		return fmt.Errorf("%w slice: slice_type %d is past 2", ErrHEVC, t)
	}
	h.SliceType = uint8(t)
	if p.OutputFlagPresent {
		h.PicOutput = r.Flag()
	}
	if s.SeparateColourPlane {
		r.Skip(2)
	}
	if nalType == HEVCNALIDRWRAD || nalType == HEVCNALIDRNLP {
		return h.done(&r)
	}
	h.PicOrderCntLsb = r.U(int(s.Log2MaxPocLsb))
	if h.ShortTermRefPicSetSPS = r.Flag(); !h.ShortTermRefPicSetSPS {
		start := r.Pos()
		if err := h.RPS.parse(&r, int(s.NumShortTermRPS), int(s.NumShortTermRPS), s.ShortTermRPS[:]); err != nil {
			return fmt.Errorf("slice: %w", err)
		}
		h.STRPSBits = r.Pos() - start
	} else {
		if s.NumShortTermRPS == 0 {
			return fmt.Errorf("%w slice: names one of SPS %d's short-term reference picture sets, which has none", ErrHEVC, s.ID)
		}
		if s.NumShortTermRPS > 1 {
			idx := r.U(bits.Len(uint(s.NumShortTermRPS - 1)))
			if idx >= uint32(s.NumShortTermRPS) {
				return fmt.Errorf("%w slice: short_term_ref_pic_set_idx %d of %d sets", ErrHEVC, idx, s.NumShortTermRPS)
			}
			h.ShortTermRefPicSetIdx = intconv.Must[uint8](idx)
		}
	}
	if s.LongTermRefPicsPresent {
		if err := h.parseLongTerm(&r, s); err != nil {
			return err
		}
	}
	return h.done(&r)
}

func (h *HEVCSliceHeader) done(r *Reader) error {
	if r.Short() {
		return fmt.Errorf("%w slice: header ends inside its syntax", ErrHEVC)
	}
	return nil
}

func (h *HEVCSliceHeader) parseLongTerm(r *Reader, s *HEVCSPS) error {
	var spsN uint32
	if s.NumLongTermRefPicsSPS > 0 {
		spsN = r.UE()
	}
	pics := r.UE()
	if spsN > uint32(s.NumLongTermRefPicsSPS) || spsN+pics > 32 {
		return fmt.Errorf("%w slice: %d long-term pictures from the SPS and %d more", ErrHEVC, spsN, pics)
	}
	h.NumLongTermSPS, h.NumLongTermPics = intconv.Must[uint8](spsN), intconv.Must[uint8](pics)
	for i := range spsN + pics {
		lt := &h.LongTerm[i]
		if i < spsN {
			idx := uint32(0)
			if s.NumLongTermRefPicsSPS > 1 {
				idx = r.U(bits.Len(uint(s.NumLongTermRefPicsSPS - 1)))
			}
			if idx >= uint32(s.NumLongTermRefPicsSPS) {
				return fmt.Errorf("%w slice: lt_idx_sps %d of %d", ErrHEVC, idx, s.NumLongTermRefPicsSPS)
			}
			lt.PocLsb, lt.UsedByCurrPic = s.LtRefPicPocLsbSPS[idx], s.UsedByCurrPicLtSPS&(1<<idx) != 0
		} else {
			lt.PocLsb, lt.UsedByCurrPic = r.U(int(s.Log2MaxPocLsb)), r.Flag()
		}
		if lt.MsbPresent = r.Flag(); lt.MsbPresent {
			lt.DeltaPocMsbCycle = r.UE()
		}
	}
	return nil
}
