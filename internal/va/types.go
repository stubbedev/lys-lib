package va

import "unsafe"

// The structures libva 2.24's headers declare (va.h, va_drmcommon.h,
// va_enc_h264.h, va_enc_hevc.h) that encoding passes, laid out as the
// headers lay them out, field for field: a union of bitfields is its
// uint32 value (set through the Bits below), one holding a pointer its
// uint64. TestLayouts pins every size and offset against a C
// compiler's.

// ConfigAttrib is VAConfigAttrib.
type ConfigAttrib struct {
	Type  int32
	Value uint32
}

// GenericValue is VAGenericValue.
type GenericValue struct {
	Type  int32
	Value uint64
}

// SurfaceAttrib is VASurfaceAttrib.
type SurfaceAttrib struct {
	Type  int32
	Flags uint32
	Value GenericValue
}

// DRMPRIMESurfaceDescriptorObjects is an anonymous member of VADRMPRIMESurfaceDescriptorObjects.
type DRMPRIMESurfaceDescriptorObjects struct {
	Fd                int32
	Size              uint32
	DrmFormatModifier uint64
}

// DRMPRIMESurfaceDescriptorLayers is an anonymous member of VADRMPRIMESurfaceDescriptorLayers.
type DRMPRIMESurfaceDescriptorLayers struct {
	DrmFormat   uint32
	NumPlanes   uint32
	ObjectIndex [4]uint32
	Offset      [4]uint32
	Pitch       [4]uint32
}

// DRMPRIMESurfaceDescriptor is VADRMPRIMESurfaceDescriptor.
type DRMPRIMESurfaceDescriptor struct {
	Fourcc     uint32
	Width      uint32
	Height     uint32
	NumObjects uint32
	Objects    [4]DRMPRIMESurfaceDescriptorObjects
	NumLayers  uint32
	Layers     [4]DRMPRIMESurfaceDescriptorLayers
}

// CodedBufferSegment is VACodedBufferSegment.
type CodedBufferSegment struct {
	Size       uint32
	BitOffset  uint32
	Status     uint32
	Reserved   uint32
	Buf        unsafe.Pointer
	Next       unsafe.Pointer
	VaReserved [4]uint32
}

// EncPackedHeaderParameterBuffer is VAEncPackedHeaderParameterBuffer.
type EncPackedHeaderParameterBuffer struct {
	Type              uint32
	BitLength         uint32
	HasEmulationBytes uint8
	VaReserved        [4]uint32
}

// EncMiscParameterBuffer is VAEncMiscParameterBuffer's header: its
// parameter structure follows it in the buffer.
type EncMiscParameterBuffer struct {
	Type int32
}

// EncMiscParameterRateControl is VAEncMiscParameterRateControl.
type EncMiscParameterRateControl struct {
	BitsPerSecond    uint32
	TargetPercentage uint32
	WindowSize       uint32
	InitialQp        uint32
	MinQp            uint32
	BasicUnitSize    uint32
	RcFlags          uint32
	ICQQualityFactor uint32
	MaxQp            uint32
	QualityFactor    uint32
	TargetFrameSize  uint32
	VaReserved       [4]uint32
}

// EncMiscParameterFrameRate is VAEncMiscParameterFrameRate.
type EncMiscParameterFrameRate struct {
	Framerate      uint32
	FramerateFlags uint32
	VaReserved     [4]uint32
}

// EncMiscParameterHRD is VAEncMiscParameterHRD.
type EncMiscParameterHRD struct {
	InitialBufferFullness uint32
	BufferSize            uint32
	VaReserved            [4]uint32
}

// PictureH264 is VAPictureH264.
type PictureH264 struct {
	PictureID           uint32
	FrameIdx            uint32
	Flags               uint32
	TopFieldOrderCnt    int32
	BottomFieldOrderCnt int32
	VaReserved          [4]uint32
}

// EncSequenceParameterBufferH264 is VAEncSequenceParameterBufferH264.
type EncSequenceParameterBufferH264 struct {
	SeqParameterSetID              uint8
	LevelIdc                       uint8
	IntraPeriod                    uint32
	IntraIdrPeriod                 uint32
	IpPeriod                       uint32
	BitsPerSecond                  uint32
	MaxNumRefFrames                uint32
	PictureWidthInMbs              uint16
	PictureHeightInMbs             uint16
	SeqFields                      uint32
	BitDepthLumaMinus8             uint8
	BitDepthChromaMinus8           uint8
	NumRefFramesInPicOrderCntCycle uint8
	OffsetForNonRefPic             int32
	OffsetForTopToBottomField      int32
	OffsetForRefFrame              [256]int32
	FrameCroppingFlag              uint8
	FrameCropLeftOffset            uint32
	FrameCropRightOffset           uint32
	FrameCropTopOffset             uint32
	FrameCropBottomOffset          uint32
	VuiParametersPresentFlag       uint8
	VuiFields                      uint32
	AspectRatioIdc                 uint8
	SarWidth                       uint32
	SarHeight                      uint32
	NumUnitsInTick                 uint32
	TimeScale                      uint32
	VaReserved                     [4]uint32
}

// EncPictureParameterBufferH264 is VAEncPictureParameterBufferH264.
type EncPictureParameterBufferH264 struct {
	CurrPic                   PictureH264
	ReferenceFrames           [16]PictureH264
	CodedBuf                  uint32
	PicParameterSetID         uint8
	SeqParameterSetID         uint8
	LastPicture               uint8
	FrameNum                  uint16
	PicInitQp                 uint8
	NumRefIdxL0ActiveMinus1   uint8
	NumRefIdxL1ActiveMinus1   uint8
	ChromaQpIndexOffset       int8
	SecondChromaQpIndexOffset int8
	PicFields                 uint32
	VaReserved                [4]uint32
}

// EncSliceParameterBufferH264 is VAEncSliceParameterBufferH264.
type EncSliceParameterBufferH264 struct {
	MacroblockAddress           uint32
	NumMacroblocks              uint32
	MacroblockInfo              uint32
	SliceType                   uint8
	PicParameterSetID           uint8
	IdrPicID                    uint16
	PicOrderCntLsb              uint16
	DeltaPicOrderCntBottom      int32
	DeltaPicOrderCnt            [2]int32
	DirectSpatialMvPredFlag     uint8
	NumRefIdxActiveOverrideFlag uint8
	NumRefIdxL0ActiveMinus1     uint8
	NumRefIdxL1ActiveMinus1     uint8
	RefPicList0                 [32]PictureH264
	RefPicList1                 [32]PictureH264
	LumaLog2WeightDenom         uint8
	ChromaLog2WeightDenom       uint8
	LumaWeightL0Flag            uint8
	LumaWeightL0                [32]int16
	LumaOffsetL0                [32]int16
	ChromaWeightL0Flag          uint8
	ChromaWeightL0              [32][2]int16
	ChromaOffsetL0              [32][2]int16
	LumaWeightL1Flag            uint8
	LumaWeightL1                [32]int16
	LumaOffsetL1                [32]int16
	ChromaWeightL1Flag          uint8
	ChromaWeightL1              [32][2]int16
	ChromaOffsetL1              [32][2]int16
	CabacInitIdc                uint8
	SliceQpDelta                int8
	DisableDeblockingFilterIdc  uint8
	SliceAlphaC0OffsetDiv2      int8
	SliceBetaOffsetDiv2         int8
	VaReserved                  [4]uint32
}

// PictureHEVC is VAPictureHEVC.
type PictureHEVC struct {
	PictureID   uint32
	PicOrderCnt int32
	Flags       uint32
	VaReserved  [4]uint32
}

// EncSequenceParameterBufferHEVC is VAEncSequenceParameterBufferHEVC.
type EncSequenceParameterBufferHEVC struct {
	GeneralProfileIdc                   uint8
	GeneralLevelIdc                     uint8
	GeneralTierFlag                     uint8
	IntraPeriod                         uint32
	IntraIdrPeriod                      uint32
	IpPeriod                            uint32
	BitsPerSecond                       uint32
	PicWidthInLumaSamples               uint16
	PicHeightInLumaSamples              uint16
	SeqFields                           uint32
	Log2MinLumaCodingBlockSizeMinus3    uint8
	Log2DiffMaxMinLumaCodingBlockSize   uint8
	Log2MinTransformBlockSizeMinus2     uint8
	Log2DiffMaxMinTransformBlockSize    uint8
	MaxTransformHierarchyDepthInter     uint8
	MaxTransformHierarchyDepthIntra     uint8
	PcmSampleBitDepthLumaMinus1         uint32
	PcmSampleBitDepthChromaMinus1       uint32
	Log2MinPcmLumaCodingBlockSizeMinus3 uint32
	Log2MaxPcmLumaCodingBlockSizeMinus3 uint32
	VuiParametersPresentFlag            uint8
	VuiFields                           uint32
	AspectRatioIdc                      uint8
	SarWidth                            uint32
	SarHeight                           uint32
	VuiNumUnitsInTick                   uint32
	VuiTimeScale                        uint32
	MinSpatialSegmentationIdc           uint16
	MaxBytesPerPicDenom                 uint8
	MaxBitsPerMinCuDenom                uint8
	SccFields                           uint16
	VaReserved                          [7]uint32
}

// EncPictureParameterBufferHEVC is VAEncPictureParameterBufferHEVC.
type EncPictureParameterBufferHEVC struct {
	DecodedCurrPic                 PictureHEVC
	ReferenceFrames                [15]PictureHEVC
	CodedBuf                       uint32
	CollocatedRefPicIndex          uint8
	LastPicture                    uint8
	PicInitQp                      uint8
	DiffCuQpDeltaDepth             uint8
	PpsCbQpOffset                  int8
	PpsCrQpOffset                  int8
	NumTileColumnsMinus1           uint8
	NumTileRowsMinus1              uint8
	ColumnWidthMinus1              [19]uint8
	RowHeightMinus1                [21]uint8
	Log2ParallelMergeLevelMinus2   uint8
	CtuMaxBitsizeAllowed           uint8
	NumRefIdxL0DefaultActiveMinus1 uint8
	NumRefIdxL1DefaultActiveMinus1 uint8
	SlicePicParameterSetID         uint8
	NalUnitType                    uint8
	PicFields                      uint32
	HierarchicalLevelPlus1         uint8
	VaByteReserved                 uint8
	SccFields                      uint16
	VaReserved                     [15]uint32
}

// EncSliceParameterBufferHEVC is VAEncSliceParameterBufferHEVC.
type EncSliceParameterBufferHEVC struct {
	SliceSegmentAddress        uint32
	NumCtuInSlice              uint32
	SliceType                  uint8
	SlicePicParameterSetID     uint8
	NumRefIdxL0ActiveMinus1    uint8
	NumRefIdxL1ActiveMinus1    uint8
	RefPicList0                [15]PictureHEVC
	RefPicList1                [15]PictureHEVC
	LumaLog2WeightDenom        uint8
	DeltaChromaLog2WeightDenom int8
	DeltaLumaWeightL0          [15]int8
	LumaOffsetL0               [15]int8
	DeltaChromaWeightL0        [15][2]int8
	ChromaOffsetL0             [15][2]int8
	DeltaLumaWeightL1          [15]int8
	LumaOffsetL1               [15]int8
	DeltaChromaWeightL1        [15][2]int8
	ChromaOffsetL1             [15][2]int8
	MaxNumMergeCand            uint8
	SliceQpDelta               int8
	SliceCbQpOffset            int8
	SliceCrQpOffset            int8
	SliceBetaOffsetDiv2        int8
	SliceTcOffsetDiv2          int8
	SliceFields                uint32
	PredWeightTableBitOffset   uint32
	PredWeightTableBitLength   uint32
	VaReserved                 [6]uint32
}
