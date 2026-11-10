package vk

import (
	"testing"
	"unsafe"
)

// The VP9 structures match the C headers' layouts (vulkan-headers
// 1.4.363 on x86-64; aarch64 lays them out the same).
func TestStdVideoVP9Layouts(t *testing.T) {
	var (
		color StdVideoVP9ColorConfig
		lf    StdVideoVP9LoopFilter
		seg   StdVideoVP9Segmentation
		pic   StdVideoDecodeVP9PictureInfo
		info  VideoDecodeVP9PictureInfoKHR
	)
	for _, c := range []struct {
		name      string
		got, want uintptr
	}{
		{"color config size", unsafe.Sizeof(color), 12},
		{"color config.BitDepth", unsafe.Offsetof(color.BitDepth), 4},
		{"color config.ColorSpace", unsafe.Offsetof(color.ColorSpace), 8},
		{"loop filter size", unsafe.Sizeof(lf), 16},
		{"loop filter.LoopFilterRefDeltas", unsafe.Offsetof(lf.LoopFilterRefDeltas), 7},
		{"loop filter.UpdateModeDelta", unsafe.Offsetof(lf.UpdateModeDelta), 11},
		{"loop filter.LoopFilterModeDeltas", unsafe.Offsetof(lf.LoopFilterModeDeltas), 12},
		{"segmentation size", unsafe.Sizeof(seg), 88},
		{"segmentation.SegmentationPredProb", unsafe.Offsetof(seg.SegmentationPredProb), 11},
		{"segmentation.FeatureEnabled", unsafe.Offsetof(seg.FeatureEnabled), 14},
		{"segmentation.FeatureData", unsafe.Offsetof(seg.FeatureData), 22},
		{"picture size", unsafe.Sizeof(pic), 56},
		{"picture.Profile", unsafe.Offsetof(pic.Profile), 4},
		{"picture.FrameType", unsafe.Offsetof(pic.FrameType), 8},
		{"picture.FrameContextIdx", unsafe.Offsetof(pic.FrameContextIdx), 12},
		{"picture.InterpolationFilter", unsafe.Offsetof(pic.InterpolationFilter), 16},
		{"picture.BaseQIdx", unsafe.Offsetof(pic.BaseQIdx), 20},
		{"picture.TileColsLog2", unsafe.Offsetof(pic.TileColsLog2), 24},
		{"picture.Reserved1", unsafe.Offsetof(pic.Reserved1), 26},
		{"picture.PColorConfig", unsafe.Offsetof(pic.PColorConfig), 32},
		{"picture.PSegmentation", unsafe.Offsetof(pic.PSegmentation), 48},
		{"VideoDecodeVP9PictureInfoKHR size", unsafe.Sizeof(info), 48},
		{"VideoDecodeVP9PictureInfoKHR.ReferenceNameSlotIndices", unsafe.Offsetof(info.ReferenceNameSlotIndices), 24},
		{"VideoDecodeVP9PictureInfoKHR.UncompressedHeaderOffset", unsafe.Offsetof(info.UncompressedHeaderOffset), 36},
		{"VideoDecodeVP9PictureInfoKHR.TilesOffset", unsafe.Offsetof(info.TilesOffset), 44},
		{"VideoDecodeVP9ProfileInfoKHR size", unsafe.Sizeof(VideoDecodeVP9ProfileInfoKHR{}), 24},
		{"VideoDecodeVP9CapabilitiesKHR size", unsafe.Sizeof(VideoDecodeVP9CapabilitiesKHR{}), 24},
		{"PhysicalDeviceVideoDecodeVP9FeaturesKHR size", unsafe.Sizeof(PhysicalDeviceVideoDecodeVP9FeaturesKHR{}), 24},
	} {
		if c.got != c.want {
			t.Errorf("%s = %d, want %d", c.name, c.got, c.want)
		}
	}
}
