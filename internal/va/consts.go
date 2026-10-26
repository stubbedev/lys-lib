package va

// The values of libva's enumerants and flags encoding names, as
// libva 2.24's headers define them.
const (
	ProfileH264ConstrainedBaseline        int32  = 13
	ProfileH264Main                       int32  = 6
	ProfileH264High                       int32  = 7
	ProfileHEVCMain                       int32  = 17
	EntrypointEncSlice                    int32  = 6
	EntrypointEncSliceLP                  int32  = 8
	ConfigAttribRTFormat                  int32  = 0
	ConfigAttribRateControl               int32  = 5
	ConfigAttribEncPackedHeaders          int32  = 10
	ConfigAttribEncMaxRefFrames           int32  = 13
	ConfigAttribPredictionDirection       int32  = 39
	ConfigAttribEncHEVCFeatures           int32  = 50
	ConfigAttribEncHEVCBlockSizes         int32  = 51
	PredictionDirectionBiNotEmpty         uint32 = 4
	FeatureNotSupported                   uint32 = 0
	FeatureRequired                       uint32 = 2
	AttribNotSupported                    uint32 = 2147483648
	RTFormatYUV420                        uint32 = 1
	RCCBR                                 uint32 = 2
	RCVBR                                 uint32 = 4
	RCCQP                                 uint32 = 16
	EncPackedHeaderSequence               uint32 = 1
	EncPackedHeaderSlice                  uint32 = 4
	PackedHeaderSequence                  uint32 = 1
	PackedHeaderSlice                     uint32 = 3
	EncCodedBufferType                    int32  = 21
	EncSequenceParameterBufferType        int32  = 22
	EncPictureParameterBufferType         int32  = 23
	EncSliceParameterBufferType           int32  = 24
	EncPackedHeaderParameterBufferType    int32  = 25
	EncPackedHeaderDataBufferType         int32  = 26
	EncMiscParameterBufferType            int32  = 27
	EncMiscParameterTypeFrameRate         int32  = 0
	EncMiscParameterTypeRateControl       int32  = 1
	EncMiscParameterTypeHRD               int32  = 5
	SurfaceAttribMinWidth                 int32  = 2
	SurfaceAttribMaxWidth                 int32  = 3
	SurfaceAttribMinHeight                int32  = 4
	SurfaceAttribMaxHeight                int32  = 5
	SurfaceAttribMemoryType               int32  = 6
	SurfaceAttribExternalBufferDescriptor int32  = 7
	SurfaceAttribMemTypeDRMPrime2         uint32 = 1073741824
	SurfaceAttribSettable                 uint32 = 2
	ExportSurfaceReadOnly                 uint32 = 1
	ExportSurfaceComposedLayers           uint32 = 8
	GenericValueTypeInteger               int32  = 1
	GenericValueTypePointer               int32  = 3
	FourccNV12                            uint32 = 842094158
	InvalidID                             uint32 = 4294967295
	PictureH264Invalid                    uint32 = 1
	PictureH264ShortTermReference         uint32 = 8
	PictureHEVCInvalid                    uint32 = 1
	PictureHEVCRPSStCurrBefore            uint32 = 16
	CodedBufStatusSliceOverflowMask       uint32 = 512
	StatusSuccess                         int32  = 0
)

// Bits is a bitfield inside one of libva's unions: its first bit and
// its width.
type Bits struct{ shift, width uint8 }

// Put is v in the field, to OR into the union's value.
func (b Bits) Put(v uint32) uint32 { return (v & (1<<b.width - 1)) << b.shift }

// Get is the field's value in the union's value u.
func (b Bits) Get(u uint32) uint32 { return u >> b.shift & (1<<b.width - 1) }

// The union bitfields encoding sets, as libva 2.24's headers place
// them.
var (
	H264SeqChromaFormatIdc                          = Bits{0, 2}
	H264SeqFrameMbsOnlyFlag                         = Bits{2, 1}
	H264SeqMbAdaptiveFrameFieldFlag                 = Bits{3, 1}
	H264SeqSeqScalingMatrixPresentFlag              = Bits{4, 1}
	H264SeqDirect8x8InferenceFlag                   = Bits{5, 1}
	H264SeqLog2MaxFrameNumMinus4                    = Bits{6, 4}
	H264SeqPicOrderCntType                          = Bits{10, 2}
	H264SeqLog2MaxPicOrderCntLsbMinus4              = Bits{12, 4}
	H264SeqDeltaPicOrderAlwaysZeroFlag              = Bits{16, 1}
	H264VuiAspectRatioInfoPresentFlag               = Bits{0, 1}
	H264VuiTimingInfoPresentFlag                    = Bits{1, 1}
	H264VuiBitstreamRestrictionFlag                 = Bits{2, 1}
	H264VuiLog2MaxMvLengthHorizontal                = Bits{3, 5}
	H264VuiLog2MaxMvLengthVertical                  = Bits{8, 5}
	H264VuiFixedFrameRateFlag                       = Bits{13, 1}
	H264VuiLowDelayHrdFlag                          = Bits{14, 1}
	H264VuiMotionVectorsOverPicBoundariesFlag       = Bits{15, 1}
	H264PicIdrPicFlag                               = Bits{0, 1}
	H264PicReferencePicFlag                         = Bits{1, 2}
	H264PicEntropyCodingModeFlag                    = Bits{3, 1}
	H264PicWeightedPredFlag                         = Bits{4, 1}
	H264PicWeightedBipredIdc                        = Bits{5, 2}
	H264PicConstrainedIntraPredFlag                 = Bits{7, 1}
	H264PicTransform8x8ModeFlag                     = Bits{8, 1}
	H264PicDeblockingFilterControlPresentFlag       = Bits{9, 1}
	H264PicRedundantPicCntPresentFlag               = Bits{10, 1}
	H264PicPicOrderPresentFlag                      = Bits{11, 1}
	H264PicPicScalingMatrixPresentFlag              = Bits{12, 1}
	HEVCSeqChromaFormatIdc                          = Bits{0, 2}
	HEVCSeqSeparateColourPlaneFlag                  = Bits{2, 1}
	HEVCSeqBitDepthLumaMinus8                       = Bits{3, 3}
	HEVCSeqBitDepthChromaMinus8                     = Bits{6, 3}
	HEVCSeqScalingListEnabledFlag                   = Bits{9, 1}
	HEVCSeqStrongIntraSmoothingEnabledFlag          = Bits{10, 1}
	HEVCSeqAmpEnabledFlag                           = Bits{11, 1}
	HEVCSeqSampleAdaptiveOffsetEnabledFlag          = Bits{12, 1}
	HEVCSeqPcmEnabledFlag                           = Bits{13, 1}
	HEVCSeqPcmLoopFilterDisabledFlag                = Bits{14, 1}
	HEVCSeqSpsTemporalMvpEnabledFlag                = Bits{15, 1}
	HEVCSeqLowDelaySeq                              = Bits{16, 1}
	HEVCSeqHierachicalFlag                          = Bits{17, 1}
	HEVCVuiAspectRatioInfoPresentFlag               = Bits{0, 1}
	HEVCVuiNeutralChromaIndicationFlag              = Bits{1, 1}
	HEVCVuiFieldSeqFlag                             = Bits{2, 1}
	HEVCVuiVuiTimingInfoPresentFlag                 = Bits{3, 1}
	HEVCVuiBitstreamRestrictionFlag                 = Bits{4, 1}
	HEVCVuiTilesFixedStructureFlag                  = Bits{5, 1}
	HEVCVuiMotionVectorsOverPicBoundariesFlag       = Bits{6, 1}
	HEVCVuiRestrictedRefPicListsFlag                = Bits{7, 1}
	HEVCVuiLog2MaxMvLengthHorizontal                = Bits{8, 5}
	HEVCVuiLog2MaxMvLengthVertical                  = Bits{13, 5}
	HEVCSccPaletteModeEnabledFlag                   = Bits{0, 1}
	HEVCPicIdrPicFlag                               = Bits{0, 1}
	HEVCPicCodingType                               = Bits{1, 3}
	HEVCPicReferencePicFlag                         = Bits{4, 1}
	HEVCPicDependentSliceSegmentsEnabledFlag        = Bits{5, 1}
	HEVCPicSignDataHidingEnabledFlag                = Bits{6, 1}
	HEVCPicConstrainedIntraPredFlag                 = Bits{7, 1}
	HEVCPicTransformSkipEnabledFlag                 = Bits{8, 1}
	HEVCPicCuQpDeltaEnabledFlag                     = Bits{9, 1}
	HEVCPicWeightedPredFlag                         = Bits{10, 1}
	HEVCPicWeightedBipredFlag                       = Bits{11, 1}
	HEVCPicTransquantBypassEnabledFlag              = Bits{12, 1}
	HEVCPicTilesEnabledFlag                         = Bits{13, 1}
	HEVCPicEntropyCodingSyncEnabledFlag             = Bits{14, 1}
	HEVCPicLoopFilterAcrossTilesEnabledFlag         = Bits{15, 1}
	HEVCPicPpsLoopFilterAcrossSlicesEnabledFlag     = Bits{16, 1}
	HEVCPicScalingListDataPresentFlag               = Bits{17, 1}
	HEVCPicScreenContentFlag                        = Bits{18, 1}
	HEVCPicEnableGpuWeightedPrediction              = Bits{19, 1}
	HEVCPicNoOutputOfPriorPicsFlag                  = Bits{20, 1}
	HEVCSliceLastSliceOfPicFlag                     = Bits{0, 1}
	HEVCSliceDependentSliceSegmentFlag              = Bits{1, 1}
	HEVCSliceColourPlaneID                          = Bits{2, 2}
	HEVCSliceSliceTemporalMvpEnabledFlag            = Bits{4, 1}
	HEVCSliceSliceSaoLumaFlag                       = Bits{5, 1}
	HEVCSliceSliceSaoChromaFlag                     = Bits{6, 1}
	HEVCSliceNumRefIdxActiveOverrideFlag            = Bits{7, 1}
	HEVCSliceMvdL1ZeroFlag                          = Bits{8, 1}
	HEVCSliceCabacInitFlag                          = Bits{9, 1}
	HEVCSliceSliceDeblockingFilterDisabledFlag      = Bits{10, 2}
	HEVCSliceSliceLoopFilterAcrossSlicesEnabledFlag = Bits{12, 1}
	HEVCSliceCollocatedFromL0Flag                   = Bits{13, 1}
	HEVCFeatureAmp                                  = Bits{4, 2}
	HEVCFeatureSao                                  = Bits{6, 2}
	HEVCFeatureTemporalMvp                          = Bits{10, 2}
	HEVCFeatureCuQpDelta                            = Bits{22, 2}
	HEVCBlockLog2MaxCodingTreeBlockSizeMinus3       = Bits{0, 2}
	HEVCBlockLog2MinLumaCodingBlockSizeMinus3       = Bits{4, 2}
	HEVCBlockLog2MaxLumaTransformBlockSizeMinus2    = Bits{6, 2}
	HEVCBlockLog2MinLumaTransformBlockSizeMinus2    = Bits{8, 2}
	HEVCBlockMaxMaxTransformHierarchyDepthInter     = Bits{10, 2}
	HEVCBlockMaxMaxTransformHierarchyDepthIntra     = Bits{14, 2}
	RCFlagsReset                                    = Bits{0, 1}
	RCFlagsDisableFrameSkip                         = Bits{1, 1}
	RCFlagsDisableBitStuffing                       = Bits{2, 1}
	RCFlagsMbRateControl                            = Bits{3, 4}
	RCFlagsTemporalID                               = Bits{7, 8}
	RCFlagsCfsIFrames                               = Bits{15, 1}
	RCFlagsEnableParallelBrc                        = Bits{16, 1}
	RCFlagsEnableDynamicScaling                     = Bits{17, 1}
	RCFlagsFrameToleranceMode                       = Bits{18, 2}
)
