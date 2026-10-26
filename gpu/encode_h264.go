package gpu

import (
	"fmt"
	"math/bits"
	"unsafe"

	"github.com/stubbedev/lys-ui/third_party/vk"
)

// h264Levels are H.264's levels (Table A-1) in order, as the StdVideo
// level enumerants number them: the largest frame (macroblocks) and
// the highest macroblock rate each allows.
var h264Levels = []struct{ frame, rate int }{
	{99, 1485},
	{396, 3000},
	{396, 6000},
	{396, 11880},
	{396, 11880},
	{792, 19800},
	{1620, 20250},
	{1620, 40500},
	{3600, 108000},
	{5120, 216000},
	{8192, 245760},
	{8192, 245760},
	{8704, 522240},
	{22080, 589824},
	{36864, 983040},
	{36864, 2073600},
	{139264, 4177920},
	{139264, 8355840},
	{139264, 16711680},
}

// h264MinLevel is level 4.1, the lowest the encoder signals: its
// bitrate ceiling leaves constant-QP desktop video room on every
// frame size it covers.
const h264MinLevel = 11

// h264Level is the lowest level at or above 4.1 that carries mbs
// macroblocks a frame at fps frames a second, which must not pass
// limit, the device's highest.
func h264Level(mbs, fps int, limit vk.StdVideoH264LevelIdc) (vk.StdVideoH264LevelIdc, error) {
	for i := h264MinLevel; i < len(h264Levels); i++ {
		if l := h264Levels[i]; mbs <= l.frame && mbs*fps <= l.rate {
			if vk.StdVideoH264LevelIdc(i) > limit {
				break
			}
			return vk.StdVideoH264LevelIdc(i), nil
		}
	}
	return 0, fmt.Errorf("gpu: no H.264 level up to the device's %d carries %d macroblocks at %d frames a second", limit, mbs, fps)
}

// h264Params are the sequence and picture parameter sets the session
// is made with, and what they point at.
type h264Params struct {
	vui vk.StdVideoH264SequenceParameterSetVui
	sps vk.StdVideoH264SequenceParameterSet
	pps vk.StdVideoH264PictureParameterSet
}

// log2MaxFrameNum is log2 of MaxFrameNum for a key interval: frame_num
// counts the frames since the last IDR and never wraps.
func log2MaxFrameNum(keyInterval int) int { return max(4, bits.Len(uint(keyInterval))) }

// newH264Params lays out the parameter sets for a picture of size
// width x height coded as coded (macroblock multiples), cropped back to
// the even size around it: BT.709 limited range, frames only, one
// reference picture, picture order count type 2 (output order is
// decode order), and the entropy coder and 8x8 transform where the
// profile and device have them.
func newH264Params(profile vk.StdVideoH264ProfileIdc, level vk.StdVideoH264LevelIdc, width, height int, coded [2]int, keyInterval int, syntax vk.VideoEncodeH264StdFlagsKHR) *h264Params {
	p := &h264Params{}
	p.vui = vk.StdVideoH264SequenceParameterSetVui{
		Flags:                   vk.StdVideoH264VuiVideoSignalTypePresent | vk.StdVideoH264VuiColorDescriptionPresent | vk.StdVideoH264VuiBitstreamRestriction,
		VideoFormat:             5,
		ColourPrimaries:         1,
		TransferCharacteristics: 1,
		MatrixCoefficients:      1,
		MaxDecFrameBuffering:    1,
	}
	cropRight, cropBottom := (coded[0]-evenUp(width))/2, (coded[1]-evenUp(height))/2
	flags := vk.StdVideoH264SpsDirect8x8Inference | vk.StdVideoH264SpsFrameMbsOnly | vk.StdVideoH264SpsVuiParametersPresent
	if cropRight != 0 || cropBottom != 0 {
		flags |= vk.StdVideoH264SpsFrameCropping
	}
	p.sps = vk.StdVideoH264SequenceParameterSet{
		Flags:                     flags,
		ProfileIdc:                profile,
		LevelIdc:                  level,
		ChromaFormatIdc:           vk.StdVideoH264ChromaFormatIdc420,
		Log2MaxFrameNumMinus4:     uint8(log2MaxFrameNum(keyInterval) - 4),
		PicOrderCntType:           vk.StdVideoH264PocType2,
		MaxNumRefFrames:           1,
		PicWidthInMbsMinus1:       uint32(coded[0]/16 - 1),
		PicHeightInMapUnitsMinus1: uint32(coded[1]/16 - 1),
		FrameCropRightOffset:      uint32(cropRight),
		FrameCropBottomOffset:     uint32(cropBottom),
		PSequenceParameterSetVui:  &p.vui,
	}
	var ppsFlags uint32
	if syntax&vk.VideoEncodeH264StdFlagsKHR(vk.VideoEncodeH264StdEntropyCodingModeFlagSetBitKhr) != 0 {
		ppsFlags |= vk.StdVideoH264PpsEntropyCodingMode
	}
	if profile == vk.StdVideoH264ProfileIdcHigh && syntax&vk.VideoEncodeH264StdFlagsKHR(vk.VideoEncodeH264StdTransform8x8ModeFlagSetBitKhr) != 0 {
		ppsFlags |= vk.StdVideoH264PpsTransform8x8Mode
	}
	p.pps = vk.StdVideoH264PictureParameterSet{Flags: ppsFlags}
	return p
}

// h264Picture is one frame's picture state: its picture info, slice
// header and reference lists, and the reference info of the picture
// it reconstructs and of the one it predicts from.
type h264Picture struct {
	info   vk.StdVideoEncodeH264PictureInfo
	lists  vk.StdVideoEncodeH264ReferenceListsInfo
	slice  vk.StdVideoEncodeH264SliceHeader
	setup  vk.StdVideoEncodeH264ReferenceInfo
	ref    vk.StdVideoEncodeH264ReferenceInfo
	nalu   vk.VideoEncodeH264NaluSliceInfoKHR
	vkInfo vk.VideoEncodeH264PictureInfoKHR
	dpb    [2]vk.VideoEncodeH264DpbSlotInfoKHR
}

// fill lays out frame sinceIDR of its key interval (0 is the IDR),
// numbered idrID among IDRs, predicting from DPB slot prev, which
// holds the frame before; qp is the constant quantiser, 0 under the
// device's rate control.
func (p *h264Picture) fill(sinceIDR, idrID, prev, qp int) {
	idr := sinceIDR == 0
	picType, sliceType := vk.StdVideoH264PictureTypeP, vk.StdVideoH264SliceTypeP
	flags := vk.StdVideoEncodeH264PictureIsReference
	if idr {
		picType, sliceType = vk.StdVideoH264PictureTypeIdr, vk.StdVideoH264SliceTypeI
		flags |= vk.StdVideoEncodeH264PictureIdr
	}
	p.lists = vk.StdVideoEncodeH264ReferenceListsInfo{}
	for i := range p.lists.RefPicList0 {
		p.lists.RefPicList0[i] = vk.StdVideoH264NoReferencePicture
		p.lists.RefPicList1[i] = vk.StdVideoH264NoReferencePicture
	}
	if !idr {
		p.lists.RefPicList0[0] = uint8(prev)
	}
	p.info = vk.StdVideoEncodeH264PictureInfo{
		Flags: flags, IdrPicID: uint16(idrID), PrimaryPicType: picType,
		FrameNum: uint32(sinceIDR), PicOrderCnt: int32(2 * sinceIDR), PRefLists: &p.lists,
	}
	p.slice = vk.StdVideoEncodeH264SliceHeader{SliceType: sliceType}
	p.ref = vk.StdVideoEncodeH264ReferenceInfo{
		PrimaryPicType: vk.StdVideoH264PictureTypeP, FrameNum: uint32(sinceIDR - 1), PicOrderCnt: int32(2 * (sinceIDR - 1)),
	}
	if sinceIDR == 1 {
		p.ref.PrimaryPicType = vk.StdVideoH264PictureTypeIdr
	}
	p.setup = vk.StdVideoEncodeH264ReferenceInfo{PrimaryPicType: picType, FrameNum: uint32(sinceIDR), PicOrderCnt: int32(2 * sinceIDR)}
	p.nalu = vk.VideoEncodeH264NaluSliceInfoKHR{SType: vk.StructureTypeVideoEncodeH264NaluSliceInfoKhr, ConstantQp: int32(qp), PStdSliceHeader: &p.slice}
	p.vkInfo = vk.VideoEncodeH264PictureInfoKHR{
		SType: vk.StructureTypeVideoEncodeH264PictureInfoKhr, NaluSliceEntryCount: 1, PNaluSliceEntries: &p.nalu, PStdPictureInfo: &p.info,
	}
	p.dpb[0] = vk.VideoEncodeH264DpbSlotInfoKHR{SType: vk.StructureTypeVideoEncodeH264DpbSlotInfoKhr, PStdReferenceInfo: &p.setup}
	p.dpb[1] = vk.VideoEncodeH264DpbSlotInfoKHR{SType: vk.StructureTypeVideoEncodeH264DpbSlotInfoKhr, PStdReferenceInfo: &p.ref}
}

// h264Profiles are the profiles the encoder tries, best first.
var h264Profiles = []vk.StdVideoH264ProfileIdc{vk.StdVideoH264ProfileIdcHigh, vk.StdVideoH264ProfileIdcMain}

// h264Codec is H.264's part of an encoder.
type h264Codec struct {
	profileInfo vk.VideoEncodeH264ProfileInfoKHR
	capsInfo    vk.VideoEncodeH264CapabilitiesKHR
	params      *h264Params
	add         vk.VideoEncodeH264SessionParametersAddInfoKHR
	create      vk.VideoEncodeH264SessionParametersCreateInfoKHR
	get         vk.VideoEncodeH264SessionParametersGetInfoKHR
	rc          vk.VideoEncodeH264RateControlInfoKHR
	pic         h264Picture
}

func (c *h264Codec) profiles() int { return len(h264Profiles) }

func (c *h264Codec) profile(i int, next unsafe.Pointer) unsafe.Pointer {
	c.profileInfo = vk.VideoEncodeH264ProfileInfoKHR{SType: vk.StructureTypeVideoEncodeH264ProfileInfoKhr, PNext: (*uintptr)(next), StdProfileIdc: h264Profiles[i]}
	return unsafe.Pointer(&c.profileInfo)
}

func (c *h264Codec) caps() unsafe.Pointer {
	c.capsInfo = vk.VideoEncodeH264CapabilitiesKHR{SType: vk.StructureTypeVideoEncodeH264CapabilitiesKhr}
	return unsafe.Pointer(&c.capsInfo)
}

func (c *h264Codec) limits() codecLimits {
	return codecLimits{minQP: int(c.capsInfo.MinQp), maxQP: int(c.capsInfo.MaxQp), pRefs: int(c.capsInfo.MaxPPictureL0ReferenceCount)}
}

func (c *h264Codec) align() int { return 16 }

func (c *h264Codec) parameters(cfg EncoderConfig, coded [2]int) (create, get unsafe.Pointer, nals int, err error) {
	level, err := h264Level(coded[0]/16*(coded[1]/16), cfg.FrameRate, c.capsInfo.MaxLevelIdc)
	if err != nil {
		return nil, nil, 0, err
	}
	c.params = newH264Params(c.profileInfo.StdProfileIdc, level, cfg.Width, cfg.Height, coded, cfg.KeyInterval, c.capsInfo.StdSyntaxFlags)
	c.add = vk.VideoEncodeH264SessionParametersAddInfoKHR{
		SType:       vk.StructureTypeVideoEncodeH264SessionParametersAddInfoKhr,
		StdSPSCount: 1, PStdSPSs: &c.params.sps,
		StdPPSCount: 1, PStdPPSs: &c.params.pps,
	}
	c.create = vk.VideoEncodeH264SessionParametersCreateInfoKHR{
		SType: vk.StructureTypeVideoEncodeH264SessionParametersCreateInfoKhr, MaxStdSPSCount: 1, MaxStdPPSCount: 1, PParametersAddInfo: &c.add,
	}
	c.get = vk.VideoEncodeH264SessionParametersGetInfoKHR{SType: vk.StructureTypeVideoEncodeH264SessionParametersGetInfoKhr, WriteStdSPS: vk.True, WriteStdPPS: vk.True}
	return unsafe.Pointer(&c.create), unsafe.Pointer(&c.get), 2, nil
}

func (c *h264Codec) rateControl(cfg EncoderConfig) unsafe.Pointer {
	c.rc = vk.VideoEncodeH264RateControlInfoKHR{
		SType:         vk.StructureTypeVideoEncodeH264RateControlInfoKhr,
		Flags:         vk.VideoEncodeH264RateControlFlagsKHR(vk.VideoEncodeH264RateControlRegularGopBitKhr | vk.VideoEncodeH264RateControlReferencePatternFlatBitKhr),
		GopFrameCount: uint32(cfg.KeyInterval), IdrPeriod: uint32(cfg.KeyInterval), TemporalLayerCount: 1,
	}
	return unsafe.Pointer(&c.rc)
}

func (c *h264Codec) picture(sinceIDR, idrs, prev, qp int) (info, setup, ref unsafe.Pointer) {
	c.pic.fill(sinceIDR, idrs, prev, qp)
	return unsafe.Pointer(&c.pic.vkInfo), unsafe.Pointer(&c.pic.dpb[0]), unsafe.Pointer(&c.pic.dpb[1])
}
