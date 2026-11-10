package vk

// The AV1 structures of vulkan_video_codec_av1std.h and
// vulkan_video_codec_av1std_decode.h, laid out as the C headers lay them
// (vk-gen carries them as opaque byte arrays). C bitfields are a uint32
// of the flag bits below; C enums are their Go types.

// StdVideoAV1 enumerants the decoder names.
const (
	StdVideoAV1ProfileMain         StdVideoAV1Profile = 0
	StdVideoAV1ProfileHigh         StdVideoAV1Profile = 1
	StdVideoAV1ProfileProfessional StdVideoAV1Profile = 2
)

// StdVideoAV1ColorConfigFlags bits.
const (
	StdVideoAV1ColorMonoChrome                uint32 = 1 << 0
	StdVideoAV1ColorRange                     uint32 = 1 << 1
	StdVideoAV1ColorSeparateUVDeltaQ          uint32 = 1 << 2
	StdVideoAV1ColorDescriptionPresent        uint32 = 1 << 3
	StdVideoAV1TimingEqualPictureInterval     uint32 = 1 << 0
	StdVideoAV1LoopFilterDeltaEnabled         uint32 = 1 << 0
	StdVideoAV1LoopFilterDeltaUpdate          uint32 = 1 << 1
	StdVideoAV1QuantizationUsingQmatrix       uint32 = 1 << 0
	StdVideoAV1QuantizationDiffUVDelta        uint32 = 1 << 1
	StdVideoAV1TileInfoUniformTileSpacing     uint32 = 1 << 0
	StdVideoAV1FilmGrainChromaScalingFromLuma uint32 = 1 << 0
	StdVideoAV1FilmGrainOverlap               uint32 = 1 << 1
	StdVideoAV1FilmGrainClipToRestrictedRange uint32 = 1 << 2
	StdVideoAV1FilmGrainUpdateGrain           uint32 = 1 << 3
)

// StdVideoAV1SequenceHeaderFlags bits.
const (
	StdVideoAV1SeqStillPicture               uint32 = 1 << 0
	StdVideoAV1SeqReducedStillPictureHeader  uint32 = 1 << 1
	StdVideoAV1SeqUse128x128Superblock       uint32 = 1 << 2
	StdVideoAV1SeqEnableFilterIntra          uint32 = 1 << 3
	StdVideoAV1SeqEnableIntraEdgeFilter      uint32 = 1 << 4
	StdVideoAV1SeqEnableInterintraCompound   uint32 = 1 << 5
	StdVideoAV1SeqEnableMaskedCompound       uint32 = 1 << 6
	StdVideoAV1SeqEnableWarpedMotion         uint32 = 1 << 7
	StdVideoAV1SeqEnableDualFilter           uint32 = 1 << 8
	StdVideoAV1SeqEnableOrderHint            uint32 = 1 << 9
	StdVideoAV1SeqEnableJntComp              uint32 = 1 << 10
	StdVideoAV1SeqEnableRefFrameMvs          uint32 = 1 << 11
	StdVideoAV1SeqFrameIDNumbersPresent      uint32 = 1 << 12
	StdVideoAV1SeqEnableSuperres             uint32 = 1 << 13
	StdVideoAV1SeqEnableCdef                 uint32 = 1 << 14
	StdVideoAV1SeqEnableRestoration          uint32 = 1 << 15
	StdVideoAV1SeqFilmGrainParamsPresent     uint32 = 1 << 16
	StdVideoAV1SeqTimingInfoPresent          uint32 = 1 << 17
	StdVideoAV1SeqInitialDisplayDelayPresent uint32 = 1 << 18
)

// StdVideoDecodeAV1PictureInfoFlags and
// StdVideoDecodeAV1ReferenceInfoFlags bits.
const (
	StdVideoDecodeAV1PictureErrorResilientMode          uint32 = 1 << 0
	StdVideoDecodeAV1PictureDisableCdfUpdate            uint32 = 1 << 1
	StdVideoDecodeAV1PictureUseSuperres                 uint32 = 1 << 2
	StdVideoDecodeAV1PictureRenderAndFrameSizeDifferent uint32 = 1 << 3
	StdVideoDecodeAV1PictureAllowScreenContentTools     uint32 = 1 << 4
	StdVideoDecodeAV1PictureIsFilterSwitchable          uint32 = 1 << 5
	StdVideoDecodeAV1PictureForceIntegerMv              uint32 = 1 << 6
	StdVideoDecodeAV1PictureFrameSizeOverride           uint32 = 1 << 7
	StdVideoDecodeAV1PictureBufferRemovalTimePresent    uint32 = 1 << 8
	StdVideoDecodeAV1PictureAllowIntrabc                uint32 = 1 << 9
	StdVideoDecodeAV1PictureFrameRefsShortSignaling     uint32 = 1 << 10
	StdVideoDecodeAV1PictureAllowHighPrecisionMv        uint32 = 1 << 11
	StdVideoDecodeAV1PictureIsMotionModeSwitchable      uint32 = 1 << 12
	StdVideoDecodeAV1PictureUseRefFrameMvs              uint32 = 1 << 13
	StdVideoDecodeAV1PictureDisableFrameEndUpdateCdf    uint32 = 1 << 14
	StdVideoDecodeAV1PictureAllowWarpedMotion           uint32 = 1 << 15
	StdVideoDecodeAV1PictureReducedTxSet                uint32 = 1 << 16
	StdVideoDecodeAV1PictureReferenceSelect             uint32 = 1 << 17
	StdVideoDecodeAV1PictureSkipModePresent             uint32 = 1 << 18
	StdVideoDecodeAV1PictureDeltaQPresent               uint32 = 1 << 19
	StdVideoDecodeAV1PictureDeltaLfPresent              uint32 = 1 << 20
	StdVideoDecodeAV1PictureDeltaLfMulti                uint32 = 1 << 21
	StdVideoDecodeAV1PictureSegmentationEnabled         uint32 = 1 << 22
	StdVideoDecodeAV1PictureSegmentationUpdateMap       uint32 = 1 << 23
	StdVideoDecodeAV1PictureSegmentationTemporalUpdate  uint32 = 1 << 24
	StdVideoDecodeAV1PictureSegmentationUpdateData      uint32 = 1 << 25
	StdVideoDecodeAV1PictureUsesLr                      uint32 = 1 << 26
	StdVideoDecodeAV1PictureUsesChromaLr                uint32 = 1 << 27
	StdVideoDecodeAV1PictureApplyGrain                  uint32 = 1 << 28
	StdVideoDecodeAV1ReferenceDisableFrameEndUpdateCdf  uint32 = 1 << 0
	StdVideoDecodeAV1ReferenceSegmentationEnabled       uint32 = 1 << 1
)

// StdVideoAV1ColorConfig is StdVideoAV1ColorConfig.
type StdVideoAV1ColorConfig struct {
	Flags                   uint32
	BitDepth                uint8
	SubsamplingX            uint8
	SubsamplingY            uint8
	Reserved1               uint8
	ColorPrimaries          int32
	TransferCharacteristics int32
	MatrixCoefficients      int32
	ChromaSamplePosition    int32
}

// StdVideoAV1TimingInfo is StdVideoAV1TimingInfo.
type StdVideoAV1TimingInfo struct {
	Flags                    uint32
	NumUnitsInDisplayTick    uint32
	TimeScale                uint32
	NumTicksPerPictureMinus1 uint32
}

// StdVideoAV1SequenceHeader is StdVideoAV1SequenceHeader.
type StdVideoAV1SequenceHeader struct {
	Flags                         uint32
	SeqProfile                    StdVideoAV1Profile
	FrameWidthBitsMinus1          uint8
	FrameHeightBitsMinus1         uint8
	MaxFrameWidthMinus1           uint16
	MaxFrameHeightMinus1          uint16
	DeltaFrameIDLengthMinus2      uint8
	AdditionalFrameIDLengthMinus1 uint8
	OrderHintBitsMinus1           uint8
	SeqForceIntegerMv             uint8
	SeqForceScreenContentTools    uint8
	Reserved1                     [5]uint8
	PColorConfig                  *StdVideoAV1ColorConfig
	PTimingInfo                   *StdVideoAV1TimingInfo
}

// StdVideoAV1LoopFilter is StdVideoAV1LoopFilter.
type StdVideoAV1LoopFilter struct {
	Flags                uint32
	LoopFilterLevel      [4]uint8
	LoopFilterSharpness  uint8
	UpdateRefDelta       uint8
	LoopFilterRefDeltas  [8]int8
	UpdateModeDelta      uint8
	LoopFilterModeDeltas [2]int8
}

// StdVideoAV1Quantization is StdVideoAV1Quantization.
type StdVideoAV1Quantization struct {
	Flags     uint32
	BaseQIdx  uint8
	DeltaQYDc int8
	DeltaQUDc int8
	DeltaQUAc int8
	DeltaQVDc int8
	DeltaQVAc int8
	QmY       uint8
	QmU       uint8
	QmV       uint8
}

// StdVideoAV1Segmentation is StdVideoAV1Segmentation: FeatureEnabled
// has bit j set for each of a segment's features j.
type StdVideoAV1Segmentation struct {
	FeatureEnabled [8]uint8
	FeatureData    [8][8]int16
}

// StdVideoAV1TileInfo is StdVideoAV1TileInfo.
type StdVideoAV1TileInfo struct {
	Flags               uint32
	TileCols            uint8
	TileRows            uint8
	ContextUpdateTileID uint16
	TileSizeBytesMinus1 uint8
	Reserved1           [7]uint8
	PMiColStarts        *uint16
	PMiRowStarts        *uint16
	PWidthInSbsMinus1   *uint16
	PHeightInSbsMinus1  *uint16
}

// StdVideoAV1CDEF is StdVideoAV1CDEF.
type StdVideoAV1CDEF struct {
	CdefDampingMinus3 uint8
	CdefBits          uint8
	CdefYPriStrength  [8]uint8
	CdefYSecStrength  [8]uint8
	CdefUVPriStrength [8]uint8
	CdefUVSecStrength [8]uint8
}

// StdVideoAV1LoopRestoration is StdVideoAV1LoopRestoration.
type StdVideoAV1LoopRestoration struct {
	FrameRestorationType [3]int32
	LoopRestorationSize  [3]uint16
}

// StdVideoAV1GlobalMotion is StdVideoAV1GlobalMotion.
type StdVideoAV1GlobalMotion struct {
	GmType   [8]uint8
	GmParams [8][6]int32
}

// StdVideoAV1FilmGrain is StdVideoAV1FilmGrain.
type StdVideoAV1FilmGrain struct {
	Flags                 uint32
	GrainScalingMinus8    uint8
	ArCoeffLag            uint8
	ArCoeffShiftMinus6    uint8
	GrainScaleShift       uint8
	GrainSeed             uint16
	FilmGrainParamsRefIdx uint8
	NumYPoints            uint8
	PointYValue           [14]uint8
	PointYScaling         [14]uint8
	NumCbPoints           uint8
	PointCbValue          [10]uint8
	PointCbScaling        [10]uint8
	NumCrPoints           uint8
	PointCrValue          [10]uint8
	PointCrScaling        [10]uint8
	ArCoeffsYPlus128      [24]int8
	ArCoeffsCbPlus128     [25]int8
	ArCoeffsCrPlus128     [25]int8
	CbMult                uint8
	CbLumaMult            uint8
	CbOffset              uint16
	CrMult                uint8
	CrLumaMult            uint8
	CrOffset              uint16
}

// StdVideoDecodeAV1PictureInfo is StdVideoDecodeAV1PictureInfo.
type StdVideoDecodeAV1PictureInfo struct {
	Flags               uint32
	FrameType           int32
	CurrentFrameID      uint32
	OrderHint           uint8
	PrimaryRefFrame     uint8
	RefreshFrameFlags   uint8
	Reserved1           uint8
	InterpolationFilter int32
	TxMode              int32
	DeltaQRes           uint8
	DeltaLfRes          uint8
	SkipModeFrame       [2]uint8
	CodedDenom          uint8
	Reserved2           [3]uint8
	OrderHints          [8]uint8
	ExpectedFrameID     [8]uint32
	PTileInfo           *StdVideoAV1TileInfo
	PQuantization       *StdVideoAV1Quantization
	PSegmentation       *StdVideoAV1Segmentation
	PLoopFilter         *StdVideoAV1LoopFilter
	PCDEF               *StdVideoAV1CDEF
	PLoopRestoration    *StdVideoAV1LoopRestoration
	PGlobalMotion       *StdVideoAV1GlobalMotion
	PFilmGrain          *StdVideoAV1FilmGrain
}

// StdVideoDecodeAV1ReferenceInfo is StdVideoDecodeAV1ReferenceInfo:
// RefFrameSignBias has bit i set for reference name i.
type StdVideoDecodeAV1ReferenceInfo struct {
	Flags            uint32
	FrameType        uint8
	RefFrameSignBias uint8
	OrderHint        uint8
	SavedOrderHints  [8]uint8
}
