package vk

import (
	"testing"
	"unsafe"
)

// The H.264 codec structures match the C headers' layouts (sizes and
// offsets printed by a C compiler from vulkan-headers 1.4.363 on
// x86-64; aarch64 lays them out the same).
func TestStdVideoH264Layouts(t *testing.T) {
	var (
		vui  StdVideoH264SequenceParameterSetVui
		sps  StdVideoH264SequenceParameterSet
		pps  StdVideoH264PictureParameterSet
		pic  StdVideoEncodeH264PictureInfo
		ref  StdVideoEncodeH264ReferenceInfo
		sh   StdVideoEncodeH264SliceHeader
		list StdVideoEncodeH264ReferenceListsInfo
	)
	for _, c := range []struct {
		name      string
		got, want uintptr
	}{
		{"vui size", unsafe.Sizeof(vui), 40},
		{"vui.NumUnitsInTick", unsafe.Offsetof(vui.NumUnitsInTick), 16},
		{"vui.MaxNumReorderFrames", unsafe.Offsetof(vui.MaxNumReorderFrames), 24},
		{"vui.PHrdParameters", unsafe.Offsetof(vui.PHrdParameters), 32},
		{"sps size", unsafe.Sizeof(sps), 88},
		{"sps.PicOrderCntType", unsafe.Offsetof(sps.PicOrderCntType), 20},
		{"sps.Log2MaxPicOrderCntLsbMinus4", unsafe.Offsetof(sps.Log2MaxPicOrderCntLsbMinus4), 32},
		{"sps.PicWidthInMbsMinus1", unsafe.Offsetof(sps.PicWidthInMbsMinus1), 36},
		{"sps.Reserved2", unsafe.Offsetof(sps.Reserved2), 60},
		{"sps.POffsetForRefFrame", unsafe.Offsetof(sps.POffsetForRefFrame), 64},
		{"sps.PSequenceParameterSetVui", unsafe.Offsetof(sps.PSequenceParameterSetVui), 80},
		{"pps size", unsafe.Sizeof(pps), 24},
		{"pps.WeightedBipredIdc", unsafe.Offsetof(pps.WeightedBipredIdc), 8},
		{"pps.PicInitQpMinus26", unsafe.Offsetof(pps.PicInitQpMinus26), 12},
		{"pps.PScalingLists", unsafe.Offsetof(pps.PScalingLists), 16},
		{"picture size", unsafe.Sizeof(pic), 32},
		{"picture.IdrPicID", unsafe.Offsetof(pic.IdrPicID), 6},
		{"picture.PrimaryPicType", unsafe.Offsetof(pic.PrimaryPicType), 8},
		{"picture.PicOrderCnt", unsafe.Offsetof(pic.PicOrderCnt), 16},
		{"picture.TemporalID", unsafe.Offsetof(pic.TemporalID), 20},
		{"picture.PRefLists", unsafe.Offsetof(pic.PRefLists), 24},
		{"reference size", unsafe.Sizeof(ref), 24},
		{"reference.LongTermPicNum", unsafe.Offsetof(ref.LongTermPicNum), 16},
		{"reference.TemporalID", unsafe.Offsetof(ref.TemporalID), 20},
		{"slice header size", unsafe.Sizeof(sh), 32},
		{"slice header.SliceType", unsafe.Offsetof(sh.SliceType), 8},
		{"slice header.SliceQpDelta", unsafe.Offsetof(sh.SliceQpDelta), 14},
		{"slice header.CabacInitIdc", unsafe.Offsetof(sh.CabacInitIdc), 16},
		{"slice header.PWeightTable", unsafe.Offsetof(sh.PWeightTable), 24},
		{"lists size", unsafe.Sizeof(list), 104},
		{"lists.RefPicList0", unsafe.Offsetof(list.RefPicList0), 6},
		{"lists.RefPicList1", unsafe.Offsetof(list.RefPicList1), 38},
		{"lists.RefList0ModOpCount", unsafe.Offsetof(list.RefList0ModOpCount), 70},
		{"lists.Reserved1", unsafe.Offsetof(list.Reserved1), 73},
		{"lists.PRefList0ModOperations", unsafe.Offsetof(list.PRefList0ModOperations), 80},
		{"lists.PRefPicMarkingOperations", unsafe.Offsetof(list.PRefPicMarkingOperations), 96},
	} {
		if c.got != c.want {
			t.Errorf("%s = %d, want %d", c.name, c.got, c.want)
		}
	}
}

// The structures video encoding and synchronization2 pass are the
// C headers' sizes (printed by a C compiler from vulkan-headers
// 1.4.363 on x86-64): VkFlags64 flag types (the *Flags2 masks) are 64
// bits and the H.264 encode flags 32, which vk-gen had swapped.
func TestVideoStructSizes(t *testing.T) {
	for _, c := range []struct {
		name      string
		got, want uintptr
	}{
		{"ImageMemoryBarrier2", unsafe.Sizeof(ImageMemoryBarrier2{}), 96},
		{"MemoryBarrier2", unsafe.Sizeof(MemoryBarrier2{}), 48},
		{"BufferMemoryBarrier2", unsafe.Sizeof(BufferMemoryBarrier2{}), 80},
		{"DependencyInfo", unsafe.Sizeof(DependencyInfo{}), 64},
		{"SemaphoreSubmitInfo", unsafe.Sizeof(SemaphoreSubmitInfo{}), 48},
		{"CommandBufferSubmitInfo", unsafe.Sizeof(CommandBufferSubmitInfo{}), 32},
		{"SubmitInfo2", unsafe.Sizeof(SubmitInfo2{}), 64},
		{"VideoProfileInfoKHR", unsafe.Sizeof(VideoProfileInfoKHR{}), 32},
		{"VideoProfileListInfoKHR", unsafe.Sizeof(VideoProfileListInfoKHR{}), 32},
		{"VideoCapabilitiesKHR", unsafe.Sizeof(VideoCapabilitiesKHR{}), 336},
		{"VideoEncodeCapabilitiesKHR", unsafe.Sizeof(VideoEncodeCapabilitiesKHR{}), 56},
		{"VideoEncodeH264CapabilitiesKHR", unsafe.Sizeof(VideoEncodeH264CapabilitiesKHR{}), 72},
		{"VideoEncodeH264ProfileInfoKHR", unsafe.Sizeof(VideoEncodeH264ProfileInfoKHR{}), 24},
		{"VideoEncodeUsageInfoKHR", unsafe.Sizeof(VideoEncodeUsageInfoKHR{}), 32},
		{"VideoSessionCreateInfoKHR", unsafe.Sizeof(VideoSessionCreateInfoKHR{}), 64},
		{"VideoSessionMemoryRequirementsKHR", unsafe.Sizeof(VideoSessionMemoryRequirementsKHR{}), 48},
		{"BindVideoSessionMemoryInfoKHR", unsafe.Sizeof(BindVideoSessionMemoryInfoKHR{}), 48},
		{"VideoEncodeH264SessionParametersAddInfoKHR", unsafe.Sizeof(VideoEncodeH264SessionParametersAddInfoKHR{}), 48},
		{"VideoEncodeH264SessionParametersCreateInfoKHR", unsafe.Sizeof(VideoEncodeH264SessionParametersCreateInfoKHR{}), 32},
		{"VideoSessionParametersCreateInfoKHR", unsafe.Sizeof(VideoSessionParametersCreateInfoKHR{}), 40},
		{"VideoEncodeSessionParametersGetInfoKHR", unsafe.Sizeof(VideoEncodeSessionParametersGetInfoKHR{}), 24},
		{"VideoEncodeH264SessionParametersGetInfoKHR", unsafe.Sizeof(VideoEncodeH264SessionParametersGetInfoKHR{}), 32},
		{"VideoEncodeSessionParametersFeedbackInfoKHR", unsafe.Sizeof(VideoEncodeSessionParametersFeedbackInfoKHR{}), 24},
		{"VideoEncodeH264SessionParametersFeedbackInfoKHR", unsafe.Sizeof(VideoEncodeH264SessionParametersFeedbackInfoKHR{}), 24},
		{"PhysicalDeviceVideoFormatInfoKHR", unsafe.Sizeof(PhysicalDeviceVideoFormatInfoKHR{}), 24},
		{"VideoFormatPropertiesKHR", unsafe.Sizeof(VideoFormatPropertiesKHR{}), 56},
		{"QueryPoolVideoEncodeFeedbackCreateInfoKHR", unsafe.Sizeof(QueryPoolVideoEncodeFeedbackCreateInfoKHR{}), 24},
		{"VideoEncodeRateControlInfoKHR", unsafe.Sizeof(VideoEncodeRateControlInfoKHR{}), 48},
		{"VideoEncodeRateControlLayerInfoKHR", unsafe.Sizeof(VideoEncodeRateControlLayerInfoKHR{}), 40},
		{"VideoEncodeH264RateControlInfoKHR", unsafe.Sizeof(VideoEncodeH264RateControlInfoKHR{}), 40},
		{"VideoCodingControlInfoKHR", unsafe.Sizeof(VideoCodingControlInfoKHR{}), 24},
		{"VideoBeginCodingInfoKHR", unsafe.Sizeof(VideoBeginCodingInfoKHR{}), 56},
		{"VideoEndCodingInfoKHR", unsafe.Sizeof(VideoEndCodingInfoKHR{}), 24},
		{"VideoPictureResourceInfoKHR", unsafe.Sizeof(VideoPictureResourceInfoKHR{}), 48},
		{"VideoReferenceSlotInfoKHR", unsafe.Sizeof(VideoReferenceSlotInfoKHR{}), 32},
		{"VideoEncodeInfoKHR", unsafe.Sizeof(VideoEncodeInfoKHR{}), 128},
		{"VideoEncodeH264PictureInfoKHR", unsafe.Sizeof(VideoEncodeH264PictureInfoKHR{}), 48},
		{"VideoEncodeH264NaluSliceInfoKHR", unsafe.Sizeof(VideoEncodeH264NaluSliceInfoKHR{}), 32},
		{"VideoEncodeH264DpbSlotInfoKHR", unsafe.Sizeof(VideoEncodeH264DpbSlotInfoKHR{}), 24},
		{"QueueFamilyVideoPropertiesKHR", unsafe.Sizeof(QueueFamilyVideoPropertiesKHR{}), 24},
		{"QueueFamilyProperties2", unsafe.Sizeof(QueueFamilyProperties2{}), 40},
		{"PhysicalDeviceVulkan13Features", unsafe.Sizeof(PhysicalDeviceVulkan13Features{}), 80},
		{"PhysicalDeviceFeatures2", unsafe.Sizeof(PhysicalDeviceFeatures2{}), 240},
		{"ImageCreateInfo", unsafe.Sizeof(ImageCreateInfo{}), 88},
		{"ImageViewCreateInfo", unsafe.Sizeof(ImageViewCreateInfo{}), 80},
		{"BufferImageCopy", unsafe.Sizeof(BufferImageCopy{}), 56},
		{"ComputePipelineCreateInfo", unsafe.Sizeof(ComputePipelineCreateInfo{}), 96},
	} {
		if c.got != c.want {
			t.Errorf("%s is %d bytes, want %d", c.name, c.got, c.want)
		}
	}
}
