package vk

import (
	"testing"
	"unsafe"
)

// The H.264 decode structures match the C headers' layouts
// (vulkan-headers 1.4.363 on x86-64; aarch64 lays them out the same).
func TestStdVideoDecodeH264Layouts(t *testing.T) {
	var (
		pic   StdVideoDecodeH264PictureInfo
		ref   StdVideoDecodeH264ReferenceInfo
		lists StdVideoH264ScalingLists
	)
	for _, c := range []struct {
		name      string
		got, want uintptr
	}{
		{"picture size", unsafe.Sizeof(pic), 20},
		{"picture.FrameNum", unsafe.Offsetof(pic.FrameNum), 8},
		{"picture.IdrPicID", unsafe.Offsetof(pic.IdrPicID), 10},
		{"picture.PicOrderCnt", unsafe.Offsetof(pic.PicOrderCnt), 12},
		{"reference size", unsafe.Sizeof(ref), 16},
		{"reference.FrameNum", unsafe.Offsetof(ref.FrameNum), 4},
		{"reference.PicOrderCnt", unsafe.Offsetof(ref.PicOrderCnt), 8},
		{"scaling lists size", unsafe.Sizeof(lists), 484},
		{"scaling lists.ScalingList4x4", unsafe.Offsetof(lists.ScalingList4x4), 4},
		{"scaling lists.ScalingList8x8", unsafe.Offsetof(lists.ScalingList8x8), 100},
	} {
		if c.got != c.want {
			t.Errorf("%s = %d, want %d", c.name, c.got, c.want)
		}
	}
}

// The decode structures the decoder passes are the C headers' sizes:
// the H.264 picture layout is a 32-bit enum, which vk-gen had made 64.
func TestVideoDecodeStructSizes(t *testing.T) {
	for _, c := range []struct {
		name      string
		got, want uintptr
	}{
		{"VideoDecodeH264ProfileInfoKHR", unsafe.Sizeof(VideoDecodeH264ProfileInfoKHR{}), 24},
		{"VideoDecodeH264CapabilitiesKHR", unsafe.Sizeof(VideoDecodeH264CapabilitiesKHR{}), 32},
		{"VideoDecodeH264PictureInfoKHR", unsafe.Sizeof(VideoDecodeH264PictureInfoKHR{}), 40},
		{"VideoDecodeH264DpbSlotInfoKHR", unsafe.Sizeof(VideoDecodeH264DpbSlotInfoKHR{}), 24},
		{"VideoDecodeH264SessionParametersAddInfoKHR", unsafe.Sizeof(VideoDecodeH264SessionParametersAddInfoKHR{}), 48},
		{"VideoDecodeInfoKHR", unsafe.Sizeof(VideoDecodeInfoKHR{}), 120},
		{"VideoDecodeCapabilitiesKHR", unsafe.Sizeof(VideoDecodeCapabilitiesKHR{}), 24},
		{"ImageViewUsageCreateInfo", unsafe.Sizeof(ImageViewUsageCreateInfo{}), 24},
	} {
		if c.got != c.want {
			t.Errorf("%s = %d bytes, want %d", c.name, c.got, c.want)
		}
	}
}

// The H.265 decode structures match the C headers' layouts.
func TestStdVideoDecodeH265Layouts(t *testing.T) {
	var (
		pic StdVideoDecodeH265PictureInfo
		ref StdVideoDecodeH265ReferenceInfo
	)
	for _, c := range []struct {
		name      string
		got, want uintptr
	}{
		{"picture size", unsafe.Sizeof(pic), 40},
		{"picture.PicOrderCntVal", unsafe.Offsetof(pic.PicOrderCntVal), 8},
		{"picture.NumBitsForSTRefPicSetInSlice", unsafe.Offsetof(pic.NumBitsForSTRefPicSetInSlice), 12},
		{"picture.RefPicSetStCurrBefore", unsafe.Offsetof(pic.RefPicSetStCurrBefore), 16},
		{"picture.RefPicSetLtCurr", unsafe.Offsetof(pic.RefPicSetLtCurr), 32},
		{"reference size", unsafe.Sizeof(ref), 8},
		{"VideoDecodeH265PictureInfoKHR", unsafe.Sizeof(VideoDecodeH265PictureInfoKHR{}), 40},
		{"VideoDecodeH265SessionParametersAddInfoKHR", unsafe.Sizeof(VideoDecodeH265SessionParametersAddInfoKHR{}), 64},
	} {
		if c.got != c.want {
			t.Errorf("%s = %d, want %d", c.name, c.got, c.want)
		}
	}
}
