package vk

// The H.265 codec structures of vulkan_video_codec_h265std.h and
// vulkan_video_codec_h265std_encode.h, laid out as the C headers lay
// them (vk-gen carries them as opaque byte arrays), field for field in
// the headers' order. C bitfields are a uint32 of the flag bits below.

// The H.265 flag bits the encoder sets, by their bitfield's position.
const (
	StdVideoH265VpsTemporalIDNesting           uint32 = 1 << 0
	StdVideoH265VpsSubLayerOrderingInfoPresent uint32 = 1 << 1
	StdVideoH265PtlProgressiveSource           uint32 = 1 << 1
	StdVideoH265PtlFrameOnlyConstraint         uint32 = 1 << 4
	StdVideoH265SpsTemporalIDNesting           uint32 = 1 << 0
	StdVideoH265SpsConformanceWindow           uint32 = 1 << 2
	StdVideoH265SpsSubLayerOrderingInfoPresent uint32 = 1 << 3
	StdVideoH265SpsAmpEnabled                  uint32 = 1 << 6
	StdVideoH265SpsSampleAdaptiveOffset        uint32 = 1 << 7
	StdVideoH265SpsVuiParametersPresent        uint32 = 1 << 13
	StdVideoH265VuiVideoSignalTypePresent      uint32 = 1 << 3
	StdVideoH265VuiColourDescriptionPresent    uint32 = 1 << 5
	StdVideoH265PpsCuQpDeltaEnabled            uint32 = 1 << 6
	StdVideoH265PpsLoopFilterAcrossSlices      uint32 = 1 << 15
	StdVideoEncodeH265PictureIsReference       uint32 = 1 << 0
	StdVideoEncodeH265PictureIrap              uint32 = 1 << 1
	StdVideoEncodeH265PictureOutput            uint32 = 1 << 5
	StdVideoEncodeH265PictureShortTermRpsSps   uint32 = 1 << 7
	StdVideoEncodeH265SliceFirstInPic          uint32 = 1 << 0
	StdVideoEncodeH265SliceSaoLuma             uint32 = 1 << 2
	StdVideoEncodeH265SliceSaoChroma           uint32 = 1 << 3
	StdVideoEncodeH265SliceLoopFilterAcross    uint32 = 1 << 11
)

// The H.265 enumerants the encoder names.
const (
	StdVideoH265ProfileIdcMain     StdVideoH265ProfileIdc      = 1
	StdVideoH265ChromaFormatIdc420 StdVideoH265ChromaFormatIdc = 1
	StdVideoH265SliceTypeP         StdVideoH265SliceType       = 1
	StdVideoH265SliceTypeI         StdVideoH265SliceType       = 2
	StdVideoH265PictureTypeP       StdVideoH265PictureType     = 0
	StdVideoH265PictureTypeIdr     StdVideoH265PictureType     = 3
	StdVideoH265NoReferencePicture uint8                       = 0xff
)

// StdVideoH265DecPicBufMgr is StdVideoH265DecPicBufMgr.
type StdVideoH265DecPicBufMgr struct {
	MaxLatencyIncreasePlus1  [7]uint32
	MaxDecPicBufferingMinus1 [7]uint8
	MaxNumReorderPics        [7]uint8
}

// StdVideoH265SubLayerHrdParameters is StdVideoH265SubLayerHrdParameters.
type StdVideoH265SubLayerHrdParameters struct {
	BitRateValueMinus1   [32]uint32
	CpbSizeValueMinus1   [32]uint32
	CpbSizeDuValueMinus1 [32]uint32
	BitRateDuValueMinus1 [32]uint32
	CbrFlag              uint32
}

// StdVideoH265HrdParameters is StdVideoH265HrdParameters.
type StdVideoH265HrdParameters struct {
	Flags                                  uint32
	TickDivisorMinus2                      uint8
	DuCpbRemovalDelayIncrementLengthMinus1 uint8
	DpbOutputDelayDuLengthMinus1           uint8
	BitRateScale                           uint8
	CpbSizeScale                           uint8
	CpbSizeDuScale                         uint8
	InitialCpbRemovalDelayLengthMinus1     uint8
	AuCpbRemovalDelayLengthMinus1          uint8
	DpbOutputDelayLengthMinus1             uint8
	CpbCntMinus1                           [7]uint8
	ElementalDurationInTcMinus1            [7]uint16
	Reserved                               [3]uint16
	PSubLayerHrdParametersNal              *StdVideoH265SubLayerHrdParameters
	PSubLayerHrdParametersVcl              *StdVideoH265SubLayerHrdParameters
}

// StdVideoH265ProfileTierLevel is StdVideoH265ProfileTierLevel.
type StdVideoH265ProfileTierLevel struct {
	Flags             uint32
	GeneralProfileIdc StdVideoH265ProfileIdc
	GeneralLevelIdc   StdVideoH265LevelIdc
}

// StdVideoH265VideoParameterSet is StdVideoH265VideoParameterSet.
type StdVideoH265VideoParameterSet struct {
	Flags                       uint32
	VpsVideoParameterSetID      uint8
	VpsMaxSubLayersMinus1       uint8
	Reserved1                   uint8
	Reserved2                   uint8
	VpsNumUnitsInTick           uint32
	VpsTimeScale                uint32
	VpsNumTicksPocDiffOneMinus1 uint32
	Reserved3                   uint32
	PDecPicBufMgr               *StdVideoH265DecPicBufMgr
	PHrdParameters              *StdVideoH265HrdParameters
	PProfileTierLevel           *StdVideoH265ProfileTierLevel
}

// StdVideoH265ScalingLists is StdVideoH265ScalingLists.
type StdVideoH265ScalingLists struct {
	ScalingList4x4         [6][16]uint8
	ScalingList8x8         [6][64]uint8
	ScalingList16x16       [6][64]uint8
	ScalingList32x32       [2][64]uint8
	ScalingListDCCoef16x16 [6]uint8
	ScalingListDCCoef32x32 [2]uint8
}

// StdVideoH265SequenceParameterSetVui is StdVideoH265SequenceParameterSetVui.
type StdVideoH265SequenceParameterSetVui struct {
	Flags                          uint32
	AspectRatioIdc                 StdVideoH265AspectRatioIdc
	SarWidth                       uint16
	SarHeight                      uint16
	VideoFormat                    uint8
	ColourPrimaries                uint8
	TransferCharacteristics        uint8
	MatrixCoeffs                   uint8
	ChromaSampleLocTypeTopField    uint8
	ChromaSampleLocTypeBottomField uint8
	Reserved1                      uint8
	Reserved2                      uint8
	DefDispWinLeftOffset           uint16
	DefDispWinRightOffset          uint16
	DefDispWinTopOffset            uint16
	DefDispWinBottomOffset         uint16
	VuiNumUnitsInTick              uint32
	VuiTimeScale                   uint32
	VuiNumTicksPocDiffOneMinus1    uint32
	MinSpatialSegmentationIdc      uint16
	Reserved3                      uint16
	MaxBytesPerPicDenom            uint8
	MaxBitsPerMinCuDenom           uint8
	Log2MaxMvLengthHorizontal      uint8
	Log2MaxMvLengthVertical        uint8
	PHrdParameters                 *StdVideoH265HrdParameters
}

// StdVideoH265PredictorPaletteEntries is StdVideoH265PredictorPaletteEntries.
type StdVideoH265PredictorPaletteEntries struct {
	PredictorPaletteEntries [3][128]uint16
}

// StdVideoH265ShortTermRefPicSet is StdVideoH265ShortTermRefPicSet.
type StdVideoH265ShortTermRefPicSet struct {
	Flags               uint32
	DeltaIdxMinus1      uint32
	UseDeltaFlag        uint16
	AbsDeltaRpsMinus1   uint16
	UsedByCurrPicFlag   uint16
	UsedByCurrPicS0Flag uint16
	UsedByCurrPicS1Flag uint16
	Reserved1           uint16
	Reserved2           uint8
	Reserved3           uint8
	NumNegativePics     uint8
	NumPositivePics     uint8
	DeltaPocS0Minus1    [16]uint16
	DeltaPocS1Minus1    [16]uint16
}

// StdVideoH265LongTermRefPicsSps is StdVideoH265LongTermRefPicsSps.
type StdVideoH265LongTermRefPicsSps struct {
	UsedByCurrPicLtSpsFlag uint32
	LtRefPicPocLsbSps      [32]uint32
}

// StdVideoH265SequenceParameterSet is StdVideoH265SequenceParameterSet.
type StdVideoH265SequenceParameterSet struct {
	Flags                                    uint32
	ChromaFormatIdc                          StdVideoH265ChromaFormatIdc
	PicWidthInLumaSamples                    uint32
	PicHeightInLumaSamples                   uint32
	SpsVideoParameterSetID                   uint8
	SpsMaxSubLayersMinus1                    uint8
	SpsSeqParameterSetID                     uint8
	BitDepthLumaMinus8                       uint8
	BitDepthChromaMinus8                     uint8
	Log2MaxPicOrderCntLsbMinus4              uint8
	Log2MinLumaCodingBlockSizeMinus3         uint8
	Log2DiffMaxMinLumaCodingBlockSize        uint8
	Log2MinLumaTransformBlockSizeMinus2      uint8
	Log2DiffMaxMinLumaTransformBlockSize     uint8
	MaxTransformHierarchyDepthInter          uint8
	MaxTransformHierarchyDepthIntra          uint8
	NumShortTermRefPicSets                   uint8
	NumLongTermRefPicsSps                    uint8
	PcmSampleBitDepthLumaMinus1              uint8
	PcmSampleBitDepthChromaMinus1            uint8
	Log2MinPcmLumaCodingBlockSizeMinus3      uint8
	Log2DiffMaxMinPcmLumaCodingBlockSize     uint8
	Reserved1                                uint8
	Reserved2                                uint8
	PaletteMaxSize                           uint8
	DeltaPaletteMaxPredictorSize             uint8
	MotionVectorResolutionControlIdc         uint8
	SpsNumPalettePredictorInitializersMinus1 uint8
	ConfWinLeftOffset                        uint32
	ConfWinRightOffset                       uint32
	ConfWinTopOffset                         uint32
	ConfWinBottomOffset                      uint32
	PProfileTierLevel                        *StdVideoH265ProfileTierLevel
	PDecPicBufMgr                            *StdVideoH265DecPicBufMgr
	PScalingLists                            *StdVideoH265ScalingLists
	PShortTermRefPicSet                      *StdVideoH265ShortTermRefPicSet
	PLongTermRefPicsSps                      *StdVideoH265LongTermRefPicsSps
	PSequenceParameterSetVui                 *StdVideoH265SequenceParameterSetVui
	PPredictorPaletteEntries                 *StdVideoH265PredictorPaletteEntries
}

// StdVideoH265PictureParameterSet is StdVideoH265PictureParameterSet.
type StdVideoH265PictureParameterSet struct {
	Flags                               uint32
	PpsPicParameterSetID                uint8
	PpsSeqParameterSetID                uint8
	SpsVideoParameterSetID              uint8
	NumExtraSliceHeaderBits             uint8
	NumRefIdxL0DefaultActiveMinus1      uint8
	NumRefIdxL1DefaultActiveMinus1      uint8
	InitQpMinus26                       int8
	DiffCuQpDeltaDepth                  uint8
	PpsCbQpOffset                       int8
	PpsCrQpOffset                       int8
	PpsBetaOffsetDiv2                   int8
	PpsTcOffsetDiv2                     int8
	Log2ParallelMergeLevelMinus2        uint8
	Log2MaxTransformSkipBlockSizeMinus2 uint8
	DiffCuChromaQpOffsetDepth           uint8
	ChromaQpOffsetListLenMinus1         uint8
	CbQpOffsetList                      [6]int8
	CrQpOffsetList                      [6]int8
	Log2SaoOffsetScaleLuma              uint8
	Log2SaoOffsetScaleChroma            uint8
	PpsActYQpOffsetPlus5                int8
	PpsActCbQpOffsetPlus5               int8
	PpsActCrQpOffsetPlus3               int8
	PpsNumPalettePredictorInitializers  uint8
	LumaBitDepthEntryMinus8             uint8
	ChromaBitDepthEntryMinus8           uint8
	NumTileColumnsMinus1                uint8
	NumTileRowsMinus1                   uint8
	Reserved1                           uint8
	Reserved2                           uint8
	ColumnWidthMinus1                   [19]uint16
	RowHeightMinus1                     [21]uint16
	Reserved3                           uint32
	PScalingLists                       *StdVideoH265ScalingLists
	PPredictorPaletteEntries            *StdVideoH265PredictorPaletteEntries
}

// StdVideoEncodeH265WeightTableFlags is StdVideoEncodeH265WeightTableFlags.
type StdVideoEncodeH265WeightTableFlags struct {
	LumaWeightL0Flag   uint16
	ChromaWeightL0Flag uint16
	LumaWeightL1Flag   uint16
	ChromaWeightL1Flag uint16
}

// StdVideoEncodeH265WeightTable is StdVideoEncodeH265WeightTable.
type StdVideoEncodeH265WeightTable struct {
	Flags                      StdVideoEncodeH265WeightTableFlags
	LumaLog2WeightDenom        uint8
	DeltaChromaLog2WeightDenom int8
	DeltaLumaWeightL0          [15]int8
	LumaOffsetL0               [15]int8
	DeltaChromaWeightL0        [15][2]int8
	DeltaChromaOffsetL0        [15][2]int8
	DeltaLumaWeightL1          [15]int8
	LumaOffsetL1               [15]int8
	DeltaChromaWeightL1        [15][2]int8
	DeltaChromaOffsetL1        [15][2]int8
}

// StdVideoEncodeH265SliceSegmentHeader is StdVideoEncodeH265SliceSegmentHeader.
type StdVideoEncodeH265SliceSegmentHeader struct {
	Flags               uint32
	SliceType           StdVideoH265SliceType
	SliceSegmentAddress uint32
	CollocatedRefIdx    uint8
	MaxNumMergeCand     uint8
	SliceCbQpOffset     int8
	SliceCrQpOffset     int8
	SliceBetaOffsetDiv2 int8
	SliceTcOffsetDiv2   int8
	SliceActYQpOffset   int8
	SliceActCbQpOffset  int8
	SliceActCrQpOffset  int8
	SliceQpDelta        int8
	Reserved1           uint16
	PWeightTable        *StdVideoEncodeH265WeightTable
}

// StdVideoEncodeH265ReferenceListsInfo is StdVideoEncodeH265ReferenceListsInfo.
type StdVideoEncodeH265ReferenceListsInfo struct {
	Flags                   uint32
	NumRefIdxL0ActiveMinus1 uint8
	NumRefIdxL1ActiveMinus1 uint8
	RefPicList0             [15]uint8
	RefPicList1             [15]uint8
	ListEntryL0             [15]uint8
	ListEntryL1             [15]uint8
}

// StdVideoEncodeH265LongTermRefPics is StdVideoEncodeH265LongTermRefPics.
type StdVideoEncodeH265LongTermRefPics struct {
	NumLongTermSps         uint8
	NumLongTermPics        uint8
	LtIdxSps               [32]uint8
	PocLsbLt               [16]uint8
	UsedByCurrPicLtFlag    uint16
	DeltaPocMsbPresentFlag [48]uint8
	DeltaPocMsbCycleLt     [48]uint8
}

// StdVideoEncodeH265PictureInfo is StdVideoEncodeH265PictureInfo.
type StdVideoEncodeH265PictureInfo struct {
	Flags                  uint32
	PicType                StdVideoH265PictureType
	SpsVideoParameterSetID uint8
	PpsSeqParameterSetID   uint8
	PpsPicParameterSetID   uint8
	ShortTermRefPicSetIdx  uint8
	PicOrderCntVal         int32
	TemporalId             uint8
	Reserved1              [7]uint8
	PRefLists              *StdVideoEncodeH265ReferenceListsInfo
	PShortTermRefPicSet    *StdVideoH265ShortTermRefPicSet
	PLongTermRefPics       *StdVideoEncodeH265LongTermRefPics
}

// StdVideoEncodeH265ReferenceInfo is StdVideoEncodeH265ReferenceInfo.
type StdVideoEncodeH265ReferenceInfo struct {
	Flags          uint32
	PicType        StdVideoH265PictureType
	PicOrderCntVal int32
	TemporalId     uint8
}
