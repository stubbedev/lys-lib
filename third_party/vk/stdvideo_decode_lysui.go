package vk

// The H.264 decode structures of vulkan_video_codec_h264std_decode.h,
// laid out as the C header lays them (vk-gen carries them as opaque
// byte arrays). C bitfields are a uint32 of the flag bits below.

// StdVideoDecodeH264PictureInfoFlags bits.
const (
	StdVideoDecodeH264PictureFieldPic               uint32 = 1 << 0
	StdVideoDecodeH264PictureIsIntra                uint32 = 1 << 1
	StdVideoDecodeH264PictureIdr                    uint32 = 1 << 2
	StdVideoDecodeH264PictureBottomField            uint32 = 1 << 3
	StdVideoDecodeH264PictureIsReference            uint32 = 1 << 4
	StdVideoDecodeH264PictureComplementaryFieldPair uint32 = 1 << 5
	StdVideoDecodeH264ReferenceTopField             uint32 = 1 << 0
	StdVideoDecodeH264ReferenceBottomField          uint32 = 1 << 1
	StdVideoDecodeH264ReferenceUsedForLongTerm      uint32 = 1 << 2
	StdVideoDecodeH264ReferenceIsNonExisting        uint32 = 1 << 3
)

// StdVideoDecodeH264PictureInfo is StdVideoDecodeH264PictureInfo.
type StdVideoDecodeH264PictureInfo struct {
	Flags             uint32
	SeqParameterSetID uint8
	PicParameterSetID uint8
	Reserved1         uint8
	Reserved2         uint8
	FrameNum          uint16
	IdrPicID          uint16
	PicOrderCnt       [2]int32
}

// StdVideoDecodeH264ReferenceInfo is StdVideoDecodeH264ReferenceInfo.
type StdVideoDecodeH264ReferenceInfo struct {
	Flags       uint32
	FrameNum    uint16
	Reserved    uint16
	PicOrderCnt [2]int32
}

// StdVideoH264ScalingLists is StdVideoH264ScalingLists: the lists in
// the order the bitstream codes them.
type StdVideoH264ScalingLists struct {
	ScalingListPresentMask      uint16
	UseDefaultScalingMatrixMask uint16
	ScalingList4x4              [6][16]uint8
	ScalingList8x8              [6][64]uint8
}

// The H.264 flag bits and enumerants the decoder sets beyond the
// encoder's, by their bitfield's position.
const (
	StdVideoH264SpsConstraintSet0                    uint32 = 1 << 0
	StdVideoH264SpsMbAdaptiveFrameField              uint32 = 1 << 7
	StdVideoH264SpsDeltaPicOrderAlwaysZero           uint32 = 1 << 9
	StdVideoH264SpsSeparateColourPlane               uint32 = 1 << 10
	StdVideoH264SpsGapsInFrameNumAllowed             uint32 = 1 << 11
	StdVideoH264SpsQpprimeYZeroTransformBypass       uint32 = 1 << 12
	StdVideoH264SpsSeqScalingMatrixPresent           uint32 = 1 << 14
	StdVideoH264VuiAspectRatioInfoPresent            uint32 = 1 << 0
	StdVideoH264VuiOverscanInfoPresent               uint32 = 1 << 1
	StdVideoH264VuiOverscanAppropriate               uint32 = 1 << 2
	StdVideoH264VuiVideoFullRange                    uint32 = 1 << 4
	StdVideoH264VuiChromaLocInfoPresent              uint32 = 1 << 6
	StdVideoH264VuiFixedFrameRate                    uint32 = 1 << 8
	StdVideoH264PpsRedundantPicCntPresent            uint32 = 1 << 1
	StdVideoH264PpsConstrainedIntraPred              uint32 = 1 << 2
	StdVideoH264PpsWeightedPred                      uint32 = 1 << 4
	StdVideoH264PpsBottomFieldPicOrderInFramePresent uint32 = 1 << 5
	StdVideoH264PpsPicScalingMatrixPresent           uint32 = 1 << 7

	StdVideoH264ProfileIdcBaseline StdVideoH264ProfileIdc = 66
)

// The H.265 decode structures of vulkan_video_codec_h265std_decode.h.

// StdVideoDecodeH265PictureInfoFlags and
// StdVideoDecodeH265ReferenceInfoFlags bits.
const (
	StdVideoDecodeH265PictureIrap                  uint32 = 1 << 0
	StdVideoDecodeH265PictureIdr                   uint32 = 1 << 1
	StdVideoDecodeH265PictureIsReference           uint32 = 1 << 2
	StdVideoDecodeH265PictureShortTermRefPicSetSps uint32 = 1 << 3
	StdVideoDecodeH265ReferenceUsedForLongTerm     uint32 = 1 << 0
	StdVideoDecodeH265ReferenceUnusedForReference  uint32 = 1 << 1
	StdVideoDecodeH265RefPicSetListSize                   = 8
)

// StdVideoDecodeH265PictureInfo is StdVideoDecodeH265PictureInfo.
type StdVideoDecodeH265PictureInfo struct {
	Flags                        uint32
	SpsVideoParameterSetID       uint8
	PpsSeqParameterSetID         uint8
	PpsPicParameterSetID         uint8
	NumDeltaPocsOfRefRpsIdx      uint8
	PicOrderCntVal               int32
	NumBitsForSTRefPicSetInSlice uint16
	Reserved                     uint16
	RefPicSetStCurrBefore        [StdVideoDecodeH265RefPicSetListSize]uint8
	RefPicSetStCurrAfter         [StdVideoDecodeH265RefPicSetListSize]uint8
	RefPicSetLtCurr              [StdVideoDecodeH265RefPicSetListSize]uint8
}

// StdVideoDecodeH265ReferenceInfo is StdVideoDecodeH265ReferenceInfo.
type StdVideoDecodeH265ReferenceInfo struct {
	Flags          uint32
	PicOrderCntVal int32
}

// The H.265 flag bits the decoder sets beyond the encoder's, by their
// bitfield's position.
const (
	StdVideoH265VpsTimingInfoPresent                  uint32 = 1 << 2
	StdVideoH265VpsPocProportionalToTiming            uint32 = 1 << 3
	StdVideoH265PtlTier                               uint32 = 1 << 0
	StdVideoH265PtlInterlacedSource                   uint32 = 1 << 2
	StdVideoH265PtlNonPackedConstraint                uint32 = 1 << 3
	StdVideoH265SpsSeparateColourPlane                uint32 = 1 << 1
	StdVideoH265SpsScalingListEnabled                 uint32 = 1 << 4
	StdVideoH265SpsScalingListDataPresent             uint32 = 1 << 5
	StdVideoH265SpsPcmEnabled                         uint32 = 1 << 8
	StdVideoH265SpsPcmLoopFilterDisabled              uint32 = 1 << 9
	StdVideoH265SpsLongTermRefPicsPresent             uint32 = 1 << 10
	StdVideoH265SpsTemporalMvpEnabled                 uint32 = 1 << 11
	StdVideoH265SpsStrongIntraSmoothing               uint32 = 1 << 12
	StdVideoH265SpsExtensionPresent                   uint32 = 1 << 14
	StdVideoH265SpsRangeExtension                     uint32 = 1 << 15
	StdVideoH265SpsTransformSkipRotation              uint32 = 1 << 16
	StdVideoH265SpsTransformSkipContext               uint32 = 1 << 17
	StdVideoH265SpsImplicitRdpcm                      uint32 = 1 << 18
	StdVideoH265SpsExplicitRdpcm                      uint32 = 1 << 19
	StdVideoH265SpsExtendedPrecisionProcessing        uint32 = 1 << 20
	StdVideoH265SpsIntraSmoothingDisabled             uint32 = 1 << 21
	StdVideoH265SpsHighPrecisionOffsets               uint32 = 1 << 22
	StdVideoH265SpsPersistentRiceAdaptation           uint32 = 1 << 23
	StdVideoH265SpsCabacBypassAlignment               uint32 = 1 << 24
	StdVideoH265VuiAspectRatioInfoPresent             uint32 = 1 << 0
	StdVideoH265VuiOverscanInfoPresent                uint32 = 1 << 1
	StdVideoH265VuiOverscanAppropriate                uint32 = 1 << 2
	StdVideoH265VuiVideoFullRange                     uint32 = 1 << 4
	StdVideoH265VuiChromaLocInfoPresent               uint32 = 1 << 6
	StdVideoH265VuiNeutralChromaIndication            uint32 = 1 << 7
	StdVideoH265VuiFieldSeq                           uint32 = 1 << 8
	StdVideoH265VuiFrameFieldInfoPresent              uint32 = 1 << 9
	StdVideoH265VuiDefaultDisplayWindow               uint32 = 1 << 10
	StdVideoH265VuiTimingInfoPresent                  uint32 = 1 << 11
	StdVideoH265VuiPocProportionalToTiming            uint32 = 1 << 12
	StdVideoH265VuiBitstreamRestriction               uint32 = 1 << 14
	StdVideoH265VuiTilesFixedStructure                uint32 = 1 << 15
	StdVideoH265VuiMotionVectorsOverPicBoundaries     uint32 = 1 << 16
	StdVideoH265VuiRestrictedRefPicLists              uint32 = 1 << 17
	StdVideoH265PpsDependentSliceSegments             uint32 = 1 << 0
	StdVideoH265PpsOutputFlagPresent                  uint32 = 1 << 1
	StdVideoH265PpsSignDataHiding                     uint32 = 1 << 2
	StdVideoH265PpsCabacInitPresent                   uint32 = 1 << 3
	StdVideoH265PpsConstrainedIntraPred               uint32 = 1 << 4
	StdVideoH265PpsTransformSkipEnabled               uint32 = 1 << 5
	StdVideoH265PpsSliceChromaQpOffsetsPresent        uint32 = 1 << 7
	StdVideoH265PpsWeightedPred                       uint32 = 1 << 8
	StdVideoH265PpsWeightedBipred                     uint32 = 1 << 9
	StdVideoH265PpsTransquantBypassEnabled            uint32 = 1 << 10
	StdVideoH265PpsTilesEnabled                       uint32 = 1 << 11
	StdVideoH265PpsEntropyCodingSyncEnabled           uint32 = 1 << 12
	StdVideoH265PpsUniformSpacing                     uint32 = 1 << 13
	StdVideoH265PpsLoopFilterAcrossTiles              uint32 = 1 << 14
	StdVideoH265PpsDeblockingFilterControlPresent     uint32 = 1 << 16
	StdVideoH265PpsDeblockingFilterOverrideEnabled    uint32 = 1 << 17
	StdVideoH265PpsDeblockingFilterDisabled           uint32 = 1 << 18
	StdVideoH265PpsScalingListDataPresent             uint32 = 1 << 19
	StdVideoH265PpsListsModificationPresent           uint32 = 1 << 20
	StdVideoH265PpsSliceSegmentHeaderExtensionPresent uint32 = 1 << 21
	StdVideoH265PpsExtensionPresent                   uint32 = 1 << 22
	StdVideoH265PpsCrossComponentPrediction           uint32 = 1 << 23
	StdVideoH265PpsChromaQpOffsetListEnabled          uint32 = 1 << 24
	StdVideoH265PpsRangeExtension                     uint32 = 1 << 30
	StdVideoH265StrpsInterRefPicSetPrediction         uint32 = 1 << 0
	StdVideoH265StrpsDeltaRpsSign                     uint32 = 1 << 1

	StdVideoH265ProfileIdcMain10                StdVideoH265ProfileIdc = 2
	StdVideoH265ProfileIdcMainStillPicture      StdVideoH265ProfileIdc = 3
	StdVideoH265ProfileIdcFormatRangeExtensions StdVideoH265ProfileIdc = 4
	StdVideoH264ProfileIdcHigh444Predictive     StdVideoH264ProfileIdc = 244
)
