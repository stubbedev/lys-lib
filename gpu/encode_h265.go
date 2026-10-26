package gpu

import (
	"fmt"
	"math/bits"
	"unsafe"

	"github.com/stubbedev/lys-lib/internal/nal"
	"github.com/stubbedev/lys-lib/third_party/vk"
)

// h265Levels are H.265's levels (Table A.8) in order, as the StdVideo
// level enumerants number them: general_level_idc, the largest picture
// (luma samples) and the highest luma sample rate each allows.
var h265Levels = []struct{ idc, picture, rate int }{
	{30, 36864, 552960},
	{60, 122880, 3686400},
	{63, 245760, 7372800},
	{90, 552960, 16588800},
	{93, 983040, 33177600},
	{120, 2228224, 66846720},
	{123, 2228224, 133693440},
	{150, 8912896, 267386880},
	{153, 8912896, 534773760},
	{156, 8912896, 1069547520},
	{180, 35651584, 1069547520},
	{183, 35651584, 2139095040},
	{186, 35651584, 4278190080},
}

// h265MinLevel is level 4.1, the lowest the encoder signals, as with
// H.264.
const h265MinLevel = 6

// h265Level is the lowest level at or above 4.1 that carries a w x h
// picture at fps frames a second, which must not pass limit, the
// device's highest. A level also bounds each side to the square root
// of eight times its largest picture.
func h265Level(w, h, fps int, limit vk.StdVideoH265LevelIdc) (vk.StdVideoH265LevelIdc, error) {
	for i := h265MinLevel; i < len(h265Levels); i++ {
		l := h265Levels[i]
		if w*h <= l.picture && w*h*fps <= l.rate && w*w <= 8*l.picture && h*h <= 8*l.picture {
			if vk.StdVideoH265LevelIdc(i) > limit {
				break
			}
			return vk.StdVideoH265LevelIdc(i), nil
		}
	}
	return 0, fmt.Errorf("gpu: no H.265 level up to the device's %d carries %dx%d at %d frames a second", limit, w, h, fps)
}

// h265Params are the video, sequence and picture parameter sets the
// session is made with, and what they point at.
type h265Params struct {
	ptl vk.StdVideoH265ProfileTierLevel
	dpb vk.StdVideoH265DecPicBufMgr
	vps vk.StdVideoH265VideoParameterSet
	vui vk.StdVideoH265SequenceParameterSetVui
	rps vk.StdVideoH265ShortTermRefPicSet
	sps vk.StdVideoH265SequenceParameterSet
	pps vk.StdVideoH265PictureParameterSet
}

// h265Blocks are the block sizes, as log2, the encoder codes with:
// the smallest coding block, the coding tree block, the smallest and
// largest transform blocks, and the transform tree's depth.
type h265Blocks struct {
	minCB, ctb, minTB, maxTB, depth int
}

// newH265Blocks are the blocks for a Vulkan device's sizes: 8x8 coding
// blocks, the largest coding tree and transform blocks it offers, the
// transforms no larger than 32 or the tree, and the deepest transform
// tree they allow.
func newH265Blocks(ctbs vk.VideoEncodeH265CtbSizeFlagsKHR, tbs vk.VideoEncodeH265TransformBlockSizeFlagsKHR) (h265Blocks, error) {
	if ctbs == 0 || tbs == 0 {
		return h265Blocks{}, fmt.Errorf("%w: the device offers no H.265 coding tree (%#x) or transform (%#x) size", ErrNoVideoEncoder, ctbs, tbs)
	}
	b := h265Blocks{minCB: 3, ctb: 3 + bits.Len32(uint32(ctbs)), minTB: 2 + bits.TrailingZeros32(uint32(tbs))}
	b.maxTB = min(1+bits.Len32(uint32(tbs)), b.ctb, 5)
	b.depth = b.ctb - b.minTB
	return b, nil
}

// h265Header is the stream's parameter sets for cfg coded at coded at
// level (a StdVideo level enumerant): Main profile, BT.709 limited
// range, one temporal layer, one reference picture named by a single
// short-term reference set (the picture before), cropped back to the
// even size around the frame, in blocks, with the tools as given and
// initQP the pictures' initial quantiser. Both backends build from it.
func h265Header(cfg EncoderConfig, coded [2]int, level vk.StdVideoH265LevelIdc, blocks h265Blocks, amp, sao, temporalMVP, cuQPDelta bool, initQP int) nal.HEVC {
	return nal.HEVC{
		LevelIdc: uint8(h265Levels[level].idc), Width: coded[0], Height: coded[1],
		ConfRight: (coded[0] - evenUp(cfg.Width)) / 2, ConfBottom: (coded[1] - evenUp(cfg.Height)) / 2,
		Log2MaxPocLsb: min(max(4, bits.Len(uint(cfg.KeyInterval))+1), 16),
		Log2MinCb:     blocks.minCB, Log2Ctb: blocks.ctb, Log2MinTb: blocks.minTB, Log2MaxTb: blocks.maxTB, TransformDepth: blocks.depth,
		AMP: amp, SAO: sao, TemporalMVP: temporalMVP, InitQP: initQP, CuQPDelta: cuQPDelta,
	}
}

// newH265Params lays h out as StdVideo parameter sets, at level (its
// StdVideo enumerant).
func newH265Params(h nal.HEVC, level vk.StdVideoH265LevelIdc) *h265Params {
	p := &h265Params{}
	p.ptl = vk.StdVideoH265ProfileTierLevel{
		Flags:             vk.StdVideoH265PtlProgressiveSource | vk.StdVideoH265PtlFrameOnlyConstraint,
		GeneralProfileIdc: vk.StdVideoH265ProfileIdcMain,
		GeneralLevelIdc:   level,
	}
	p.dpb.MaxDecPicBufferingMinus1[0] = 1
	p.vps = vk.StdVideoH265VideoParameterSet{
		Flags:             vk.StdVideoH265VpsTemporalIDNesting | vk.StdVideoH265VpsSubLayerOrderingInfoPresent,
		PDecPicBufMgr:     &p.dpb,
		PProfileTierLevel: &p.ptl,
	}
	p.vui = vk.StdVideoH265SequenceParameterSetVui{
		Flags:                   vk.StdVideoH265VuiVideoSignalTypePresent | vk.StdVideoH265VuiColourDescriptionPresent,
		VideoFormat:             5,
		ColourPrimaries:         1,
		TransferCharacteristics: 1,
		MatrixCoeffs:            1,
	}
	p.rps = vk.StdVideoH265ShortTermRefPicSet{NumNegativePics: 1, UsedByCurrPicS0Flag: 1}
	flags := vk.StdVideoH265SpsTemporalIDNesting | vk.StdVideoH265SpsSubLayerOrderingInfoPresent | vk.StdVideoH265SpsVuiParametersPresent
	if h.ConfRight != 0 || h.ConfBottom != 0 {
		flags |= vk.StdVideoH265SpsConformanceWindow
	}
	if h.AMP {
		flags |= vk.StdVideoH265SpsAmpEnabled
	}
	if h.SAO {
		flags |= vk.StdVideoH265SpsSampleAdaptiveOffset
	}
	p.sps = vk.StdVideoH265SequenceParameterSet{
		Flags:                                flags,
		ChromaFormatIdc:                      vk.StdVideoH265ChromaFormatIdc420,
		PicWidthInLumaSamples:                uint32(h.Width),
		PicHeightInLumaSamples:               uint32(h.Height),
		Log2MaxPicOrderCntLsbMinus4:          uint8(h.Log2MaxPocLsb - 4),
		Log2MinLumaCodingBlockSizeMinus3:     uint8(h.Log2MinCb - 3),
		Log2DiffMaxMinLumaCodingBlockSize:    uint8(h.Log2Ctb - h.Log2MinCb),
		Log2MinLumaTransformBlockSizeMinus2:  uint8(h.Log2MinTb - 2),
		Log2DiffMaxMinLumaTransformBlockSize: uint8(h.Log2MaxTb - h.Log2MinTb),
		MaxTransformHierarchyDepthInter:      uint8(h.TransformDepth),
		MaxTransformHierarchyDepthIntra:      uint8(h.TransformDepth),
		NumShortTermRefPicSets:               1,
		ConfWinRightOffset:                   uint32(h.ConfRight),
		ConfWinBottomOffset:                  uint32(h.ConfBottom),
		PProfileTierLevel:                    &p.ptl,
		PDecPicBufMgr:                        &p.dpb,
		PShortTermRefPicSet:                  &p.rps,
		PSequenceParameterSetVui:             &p.vui,
	}
	ppsFlags := vk.StdVideoH265PpsLoopFilterAcrossSlices
	if h.CuQPDelta {
		ppsFlags |= vk.StdVideoH265PpsCuQpDeltaEnabled
	}
	p.pps = vk.StdVideoH265PictureParameterSet{Flags: ppsFlags, InitQpMinus26: int8(h.InitQP - 26)}
	return p
}

// h265Picture is one frame's picture state, as h264Picture is.
type h265Picture struct {
	info   vk.StdVideoEncodeH265PictureInfo
	lists  vk.StdVideoEncodeH265ReferenceListsInfo
	slice  vk.StdVideoEncodeH265SliceSegmentHeader
	setup  vk.StdVideoEncodeH265ReferenceInfo
	ref    vk.StdVideoEncodeH265ReferenceInfo
	nalu   vk.VideoEncodeH265NaluSliceSegmentInfoKHR
	vkInfo vk.VideoEncodeH265PictureInfoKHR
	dpb    [2]vk.VideoEncodeH265DpbSlotInfoKHR
}

// fill lays out frame sinceIDR of its key interval (0 is the IDR),
// predicting from DPB slot prev, which holds the frame before, through
// the sequence's one short-term reference set rps; sao is whether the
// slices use sample adaptive offset, qp the constant quantiser (0
// under the device's rate control).
func (p *h265Picture) fill(sinceIDR, prev, qp int, rps *vk.StdVideoH265ShortTermRefPicSet, sao bool) {
	idr := sinceIDR == 0
	picType, sliceType := vk.StdVideoH265PictureTypeP, vk.StdVideoH265SliceTypeP
	flags := vk.StdVideoEncodeH265PictureIsReference | vk.StdVideoEncodeH265PictureOutput | vk.StdVideoEncodeH265PictureShortTermRpsSps
	if idr {
		picType, sliceType = vk.StdVideoH265PictureTypeIdr, vk.StdVideoH265SliceTypeI
		flags = vk.StdVideoEncodeH265PictureIsReference | vk.StdVideoEncodeH265PictureOutput | vk.StdVideoEncodeH265PictureIrap
	}
	p.lists = vk.StdVideoEncodeH265ReferenceListsInfo{}
	for i := range p.lists.RefPicList0 {
		p.lists.RefPicList0[i] = vk.StdVideoH265NoReferencePicture
		p.lists.RefPicList1[i] = vk.StdVideoH265NoReferencePicture
	}
	if !idr {
		p.lists.RefPicList0[0] = uint8(prev)
	}
	p.info = vk.StdVideoEncodeH265PictureInfo{
		Flags: flags, PicType: picType, PicOrderCntVal: int32(sinceIDR), PRefLists: &p.lists, PShortTermRefPicSet: rps,
	}
	sliceFlags := vk.StdVideoEncodeH265SliceFirstInPic | vk.StdVideoEncodeH265SliceLoopFilterAcross
	if sao {
		sliceFlags |= vk.StdVideoEncodeH265SliceSaoLuma | vk.StdVideoEncodeH265SliceSaoChroma
	}
	p.slice = vk.StdVideoEncodeH265SliceSegmentHeader{Flags: sliceFlags, SliceType: sliceType, MaxNumMergeCand: 5}
	p.ref = vk.StdVideoEncodeH265ReferenceInfo{PicType: vk.StdVideoH265PictureTypeP, PicOrderCntVal: int32(sinceIDR - 1)}
	if sinceIDR == 1 {
		p.ref.PicType = vk.StdVideoH265PictureTypeIdr
	}
	p.setup = vk.StdVideoEncodeH265ReferenceInfo{PicType: picType, PicOrderCntVal: int32(sinceIDR)}
	p.nalu = vk.VideoEncodeH265NaluSliceSegmentInfoKHR{SType: vk.StructureTypeVideoEncodeH265NaluSliceSegmentInfoKhr, ConstantQp: int32(qp), PStdSliceSegmentHeader: &p.slice}
	p.vkInfo = vk.VideoEncodeH265PictureInfoKHR{
		SType: vk.StructureTypeVideoEncodeH265PictureInfoKhr, NaluSliceSegmentEntryCount: 1, PNaluSliceSegmentEntries: &p.nalu, PStdPictureInfo: &p.info,
	}
	p.dpb[0] = vk.VideoEncodeH265DpbSlotInfoKHR{SType: vk.StructureTypeVideoEncodeH265DpbSlotInfoKhr, PStdReferenceInfo: &p.setup}
	p.dpb[1] = vk.VideoEncodeH265DpbSlotInfoKHR{SType: vk.StructureTypeVideoEncodeH265DpbSlotInfoKhr, PStdReferenceInfo: &p.ref}
}

// h265Codec is H.265's part of an encoder.
type h265Codec struct {
	profileInfo vk.VideoEncodeH265ProfileInfoKHR
	capsInfo    vk.VideoEncodeH265CapabilitiesKHR
	header      nal.HEVC
	params      *h265Params
	add         vk.VideoEncodeH265SessionParametersAddInfoKHR
	create      vk.VideoEncodeH265SessionParametersCreateInfoKHR
	get         vk.VideoEncodeH265SessionParametersGetInfoKHR
	rc          vk.VideoEncodeH265RateControlInfoKHR
	pic         h265Picture
}

func (c *h265Codec) profiles() int { return 1 }

func (c *h265Codec) profile(_ int, next unsafe.Pointer) unsafe.Pointer {
	c.profileInfo = vk.VideoEncodeH265ProfileInfoKHR{SType: vk.StructureTypeVideoEncodeH265ProfileInfoKhr, PNext: (*uintptr)(next), StdProfileIdc: vk.StdVideoH265ProfileIdcMain}
	return unsafe.Pointer(&c.profileInfo)
}

func (c *h265Codec) caps() unsafe.Pointer {
	c.capsInfo = vk.VideoEncodeH265CapabilitiesKHR{SType: vk.StructureTypeVideoEncodeH265CapabilitiesKhr}
	return unsafe.Pointer(&c.capsInfo)
}

func (c *h265Codec) limits() codecLimits {
	return codecLimits{minQP: int(c.capsInfo.MinQp), maxQP: int(c.capsInfo.MaxQp), pRefs: int(c.capsInfo.MaxPPictureL0ReferenceCount)}
}

// align is the smallest coding block, 8x8, which the picture size is a
// multiple of.
func (c *h265Codec) align() int { return 8 }

func (c *h265Codec) parameters(cfg EncoderConfig, coded [2]int) (create, get unsafe.Pointer, nals int, err error) {
	level, err := h265Level(coded[0], coded[1], cfg.FrameRate, c.capsInfo.MaxLevelIdc)
	if err != nil {
		return nil, nil, 0, err
	}
	blocks, err := newH265Blocks(c.capsInfo.CtbSizes, c.capsInfo.TransformBlockSizes)
	if err != nil {
		return nil, nil, 0, err
	}
	sao := c.capsInfo.StdSyntaxFlags&vk.VideoEncodeH265StdFlagsKHR(vk.VideoEncodeH265StdSampleAdaptiveOffsetEnabledFlagSetBitKhr) != 0
	c.header = h265Header(cfg, coded, level, blocks, true, sao, false, false, 26)
	c.params = newH265Params(c.header, level)
	c.add = vk.VideoEncodeH265SessionParametersAddInfoKHR{
		SType:       vk.StructureTypeVideoEncodeH265SessionParametersAddInfoKhr,
		StdVPSCount: 1, PStdVPSs: &c.params.vps,
		StdSPSCount: 1, PStdSPSs: &c.params.sps,
		StdPPSCount: 1, PStdPPSs: &c.params.pps,
	}
	c.create = vk.VideoEncodeH265SessionParametersCreateInfoKHR{
		SType: vk.StructureTypeVideoEncodeH265SessionParametersCreateInfoKhr, MaxStdVPSCount: 1, MaxStdSPSCount: 1, MaxStdPPSCount: 1, PParametersAddInfo: &c.add,
	}
	c.get = vk.VideoEncodeH265SessionParametersGetInfoKHR{
		SType: vk.StructureTypeVideoEncodeH265SessionParametersGetInfoKhr, WriteStdVPS: vk.True, WriteStdSPS: vk.True, WriteStdPPS: vk.True,
	}
	return unsafe.Pointer(&c.create), unsafe.Pointer(&c.get), 3, nil
}

func (c *h265Codec) rateControl(cfg EncoderConfig) unsafe.Pointer {
	c.rc = vk.VideoEncodeH265RateControlInfoKHR{
		SType:         vk.StructureTypeVideoEncodeH265RateControlInfoKhr,
		Flags:         vk.VideoEncodeH265RateControlFlagsKHR(vk.VideoEncodeH265RateControlRegularGopBitKhr | vk.VideoEncodeH265RateControlReferencePatternFlatBitKhr),
		GopFrameCount: uint32(cfg.KeyInterval), IdrPeriod: uint32(cfg.KeyInterval), SubLayerCount: 1,
	}
	return unsafe.Pointer(&c.rc)
}

func (c *h265Codec) picture(sinceIDR, _, prev, qp int) (info, setup, ref unsafe.Pointer) {
	c.pic.fill(sinceIDR, prev, qp, &c.params.rps, c.params.sps.Flags&vk.StdVideoH265SpsSampleAdaptiveOffset != 0)
	return unsafe.Pointer(&c.pic.vkInfo), unsafe.Pointer(&c.pic.dpb[0]), unsafe.Pointer(&c.pic.dpb[1])
}
