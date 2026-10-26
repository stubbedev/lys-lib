package vk

import "unsafe"

// The H.264 codec structures of vulkan_video_codec_h264std.h and
// vulkan_video_codec_h264std_encode.h, laid out as the C headers lay
// them (vk-gen carries them as opaque byte arrays). C bitfields are a
// uint32 of the flag bits below.

// StdVideoH264SpsFlags bits.
const (
	StdVideoH264SpsDirect8x8Inference        uint32 = 1 << 6
	StdVideoH264SpsFrameMbsOnly              uint32 = 1 << 8
	StdVideoH264SpsFrameCropping             uint32 = 1 << 13
	StdVideoH264SpsVuiParametersPresent      uint32 = 1 << 15
	StdVideoH264VuiVideoSignalTypePresent    uint32 = 1 << 3
	StdVideoH264VuiColorDescriptionPresent   uint32 = 1 << 5
	StdVideoH264VuiTimingInfoPresent         uint32 = 1 << 7
	StdVideoH264VuiBitstreamRestriction      uint32 = 1 << 9
	StdVideoH264PpsTransform8x8Mode          uint32 = 1 << 0
	StdVideoH264PpsDeblockingFilterControl   uint32 = 1 << 3
	StdVideoH264PpsEntropyCodingMode         uint32 = 1 << 6
	StdVideoEncodeH264PictureIdr             uint32 = 1 << 0
	StdVideoEncodeH264PictureIsReference     uint32 = 1 << 1
	StdVideoEncodeH264SliceNumRefIdxOverride uint32 = 1 << 1
)

// The H.264 enumerants the encoder names.
const (
	StdVideoH264ProfileIdcMain       StdVideoH264ProfileIdc      = 77
	StdVideoH264ProfileIdcHigh       StdVideoH264ProfileIdc      = 100
	StdVideoH264ChromaFormatIdc420   StdVideoH264ChromaFormatIdc = 1
	StdVideoH264PocType2             StdVideoH264PocType         = 2
	StdVideoH264SliceTypeP           StdVideoH264SliceType       = 0
	StdVideoH264SliceTypeI           StdVideoH264SliceType       = 2
	StdVideoH264PictureTypeP         StdVideoH264PictureType     = 0
	StdVideoH264PictureTypeIdr       StdVideoH264PictureType     = 5
	StdVideoH264AspectRatioIdcSquare StdVideoH264AspectRatioIdc  = 1
	StdVideoH264NoReferencePicture   uint8                       = 0xff
	StdVideoH264MaxNumListRef                                    = 32
)

// StdVideoH264SequenceParameterSetVui is StdVideoH264SequenceParameterSetVui.
type StdVideoH264SequenceParameterSetVui struct {
	Flags                          uint32
	AspectRatioIdc                 StdVideoH264AspectRatioIdc
	SarWidth, SarHeight            uint16
	VideoFormat                    uint8
	ColourPrimaries                uint8
	TransferCharacteristics        uint8
	MatrixCoefficients             uint8
	NumUnitsInTick, TimeScale      uint32
	MaxNumReorderFrames            uint8
	MaxDecFrameBuffering           uint8
	ChromaSampleLocTypeTopField    uint8
	ChromaSampleLocTypeBottomField uint8
	Reserved1                      uint32
	PHrdParameters                 unsafe.Pointer
}

// StdVideoH264SequenceParameterSet is StdVideoH264SequenceParameterSet.
type StdVideoH264SequenceParameterSet struct {
	Flags                          uint32
	ProfileIdc                     StdVideoH264ProfileIdc
	LevelIdc                       StdVideoH264LevelIdc
	ChromaFormatIdc                StdVideoH264ChromaFormatIdc
	SeqParameterSetID              uint8
	BitDepthLumaMinus8             uint8
	BitDepthChromaMinus8           uint8
	Log2MaxFrameNumMinus4          uint8
	PicOrderCntType                StdVideoH264PocType
	OffsetForNonRefPic             int32
	OffsetForTopToBottomField      int32
	Log2MaxPicOrderCntLsbMinus4    uint8
	NumRefFramesInPicOrderCntCycle uint8
	MaxNumRefFrames                uint8
	Reserved1                      uint8
	PicWidthInMbsMinus1            uint32
	PicHeightInMapUnitsMinus1      uint32
	FrameCropLeftOffset            uint32
	FrameCropRightOffset           uint32
	FrameCropTopOffset             uint32
	FrameCropBottomOffset          uint32
	Reserved2                      uint32
	POffsetForRefFrame             *int32
	PScalingLists                  unsafe.Pointer
	PSequenceParameterSetVui       *StdVideoH264SequenceParameterSetVui
}

// StdVideoH264PictureParameterSet is StdVideoH264PictureParameterSet.
type StdVideoH264PictureParameterSet struct {
	Flags                          uint32
	SeqParameterSetID              uint8
	PicParameterSetID              uint8
	NumRefIdxL0DefaultActiveMinus1 uint8
	NumRefIdxL1DefaultActiveMinus1 uint8
	WeightedBipredIdc              StdVideoH264WeightedBipredIdc
	PicInitQpMinus26               int8
	PicInitQsMinus26               int8
	ChromaQpIndexOffset            int8
	SecondChromaQpIndexOffset      int8
	PScalingLists                  unsafe.Pointer
}

// StdVideoEncodeH264ReferenceListsInfo is StdVideoEncodeH264ReferenceListsInfo.
type StdVideoEncodeH264ReferenceListsInfo struct {
	Flags                    uint32
	NumRefIdxL0ActiveMinus1  uint8
	NumRefIdxL1ActiveMinus1  uint8
	RefPicList0              [StdVideoH264MaxNumListRef]uint8
	RefPicList1              [StdVideoH264MaxNumListRef]uint8
	RefList0ModOpCount       uint8
	RefList1ModOpCount       uint8
	RefPicMarkingOpCount     uint8
	Reserved1                [7]uint8
	PRefList0ModOperations   unsafe.Pointer
	PRefList1ModOperations   unsafe.Pointer
	PRefPicMarkingOperations unsafe.Pointer
}

// StdVideoEncodeH264PictureInfo is StdVideoEncodeH264PictureInfo.
type StdVideoEncodeH264PictureInfo struct {
	Flags             uint32
	SeqParameterSetID uint8
	PicParameterSetID uint8
	IdrPicID          uint16
	PrimaryPicType    StdVideoH264PictureType
	FrameNum          uint32
	PicOrderCnt       int32
	TemporalID        uint8
	Reserved1         [3]uint8
	PRefLists         *StdVideoEncodeH264ReferenceListsInfo
}

// StdVideoEncodeH264ReferenceInfo is StdVideoEncodeH264ReferenceInfo.
type StdVideoEncodeH264ReferenceInfo struct {
	Flags            uint32
	PrimaryPicType   StdVideoH264PictureType
	FrameNum         uint32
	PicOrderCnt      int32
	LongTermPicNum   uint16
	LongTermFrameIdx uint16
	TemporalID       uint8
}

// StdVideoEncodeH264SliceHeader is StdVideoEncodeH264SliceHeader.
type StdVideoEncodeH264SliceHeader struct {
	Flags                      uint32
	FirstMbInSlice             uint32
	SliceType                  StdVideoH264SliceType
	SliceAlphaC0OffsetDiv2     int8
	SliceBetaOffsetDiv2        int8
	SliceQpDelta               int8
	Reserved1                  uint8
	CabacInitIdc               StdVideoH264CabacInitIdc
	DisableDeblockingFilterIdc StdVideoH264DisableDeblockingFilterIdc
	PWeightTable               unsafe.Pointer
}
