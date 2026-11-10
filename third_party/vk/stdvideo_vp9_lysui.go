package vk

// The VP9 structures of vulkan_video_codec_vp9std.h and
// vulkan_video_codec_vp9std_decode.h, laid out as the C headers lay them
// (vk-gen carries them as opaque byte arrays). C bitfields are a uint32
// of the flag bits below; C enums are their Go types.

// StdVideoVP9ColorConfigFlags, StdVideoVP9LoopFilterFlags,
// StdVideoVP9SegmentationFlags and StdVideoDecodeVP9PictureInfoFlags
// bits.
const (
	StdVideoVP9ColorRange                         uint32 = 1 << 0
	StdVideoVP9LoopFilterDeltaEnabled             uint32 = 1 << 0
	StdVideoVP9LoopFilterDeltaUpdate              uint32 = 1 << 1
	StdVideoVP9SegmentationUpdateMap              uint32 = 1 << 0
	StdVideoVP9SegmentationTemporalUpdate         uint32 = 1 << 1
	StdVideoVP9SegmentationUpdateData             uint32 = 1 << 2
	StdVideoVP9SegmentationAbsOrDeltaUpdate       uint32 = 1 << 3
	StdVideoDecodeVP9PictureErrorResilientMode    uint32 = 1 << 0
	StdVideoDecodeVP9PictureIntraOnly             uint32 = 1 << 1
	StdVideoDecodeVP9PictureAllowHighPrecisionMv  uint32 = 1 << 2
	StdVideoDecodeVP9PictureRefreshFrameContext   uint32 = 1 << 3
	StdVideoDecodeVP9PictureFrameParallelDecoding uint32 = 1 << 4
	StdVideoDecodeVP9PictureSegmentationEnabled   uint32 = 1 << 5
	StdVideoDecodeVP9PictureShowFrame             uint32 = 1 << 6
	StdVideoDecodeVP9PictureUsePrevFrameMvs       uint32 = 1 << 7
)

// StdVideoVP9ColorConfig is StdVideoVP9ColorConfig.
type StdVideoVP9ColorConfig struct {
	Flags        uint32
	BitDepth     uint8
	SubsamplingX uint8
	SubsamplingY uint8
	Reserved1    uint8
	ColorSpace   int32
}

// StdVideoVP9LoopFilter is StdVideoVP9LoopFilter: UpdateRefDelta and
// UpdateModeDelta have bit i set for each delta i coded.
type StdVideoVP9LoopFilter struct {
	Flags                uint32
	LoopFilterLevel      uint8
	LoopFilterSharpness  uint8
	UpdateRefDelta       uint8
	LoopFilterRefDeltas  [4]int8
	UpdateModeDelta      uint8
	LoopFilterModeDeltas [2]int8
}

// StdVideoVP9Segmentation is StdVideoVP9Segmentation: FeatureEnabled
// has bit j set for each of a segment's features j.
type StdVideoVP9Segmentation struct {
	Flags                 uint32
	SegmentationTreeProbs [7]uint8
	SegmentationPredProb  [3]uint8
	FeatureEnabled        [8]uint8
	FeatureData           [8][4]int16
}

// StdVideoDecodeVP9PictureInfo is StdVideoDecodeVP9PictureInfo:
// RefFrameSignBiasMask has bit i set for reference name i.
type StdVideoDecodeVP9PictureInfo struct {
	Flags                uint32
	Profile              StdVideoVP9Profile
	FrameType            int32
	FrameContextIdx      uint8
	ResetFrameContext    uint8
	RefreshFrameFlags    uint8
	RefFrameSignBiasMask uint8
	InterpolationFilter  int32
	BaseQIdx             uint8
	DeltaQYDc            int8
	DeltaQUVDc           int8
	DeltaQUVAc           int8
	TileColsLog2         uint8
	TileRowsLog2         uint8
	Reserved1            [3]uint16
	PColorConfig         *StdVideoVP9ColorConfig
	PLoopFilter          *StdVideoVP9LoopFilter
	PSegmentation        *StdVideoVP9Segmentation
}
