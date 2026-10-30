package vk

// Core 1.1 structure types vk-gen leaves out (it emits only the
// extension aliases): external memory, dedicated allocation and the
// format property queries.
const (
	StructureTypeExternalMemoryImageCreateInfo         StructureType = 1000072001
	StructureTypeExportMemoryAllocateInfo              StructureType = 1000072002
	StructureTypeMemoryDedicatedAllocateInfo           StructureType = 1000127001
	StructureTypeFormatProperties2                     StructureType = 1000059002
	StructureTypeImageFormatProperties2                StructureType = 1000059003
	StructureTypePhysicalDeviceImageFormatInfo2        StructureType = 1000059004
	StructureTypePhysicalDeviceExternalImageFormatInfo StructureType = 1000071000
	StructureTypeExternalImageFormatProperties         StructureType = 1000071001
)

// Core 1.1 format features vk-gen leaves out (VK_KHR_maintenance1 made
// them core): an image of the format can be a transfer's source or
// destination.
const (
	FormatFeatureTransferSrcBit FormatFeatureFlagBits = 1 << 14
	FormatFeatureTransferDstBit FormatFeatureFlagBits = 1 << 15
)

// Core 1.1 and 1.3 values vk-gen leaves out, which video encoding
// names: the queue family query that carries a family's codecs, the
// two-plane 4:2:0 format (NV12) and its plane aspects, the 1.3 feature
// block, and VK_KHR_synchronization2's structures, core in 1.3.
const (
	StructureTypeQueueFamilyProperties2         StructureType = 1000059005
	StructureTypePhysicalDeviceVulkan13Features StructureType = 53
	StructureTypeMemoryBarrier2                 StructureType = 1000314000
	StructureTypeBufferMemoryBarrier2           StructureType = 1000314001
	StructureTypeImageMemoryBarrier2            StructureType = 1000314002
	StructureTypeDependencyInfo                 StructureType = 1000314003
	StructureTypeSubmitInfo2                    StructureType = 1000314004
	StructureTypeSemaphoreSubmitInfo            StructureType = 1000314005
	StructureTypeCommandBufferSubmitInfo        StructureType = 1000314006

	FormatG8B8r82plane420Unorm Format = 1000156003

	ImageAspectPlane0Bit ImageAspectFlagBits = 1 << 4
	ImageAspectPlane1Bit ImageAspectFlagBits = 1 << 5
)

// Core 1.1 values vk-gen leaves out that importing dmabufs names: the
// memory requirement and bind structures, per plane for a disjoint
// image, and the disjoint format feature and create flag.
const (
	StructureTypeBindImageMemoryInfo              StructureType = 1000157001
	StructureTypeImageMemoryRequirementsInfo2     StructureType = 1000146001
	StructureTypeMemoryRequirements2              StructureType = 1000146003
	StructureTypeBindImagePlaneMemoryInfo         StructureType = 1000156002
	StructureTypeImagePlaneMemoryRequirementsInfo StructureType = 1000156003

	FormatFeatureDisjointBit FormatFeatureFlagBits = 1 << 22
	ImageCreateDisjointBit   ImageCreateFlagBits   = 1 << 9
)

// Core 1.1 external semaphore structure types vk-gen leaves out, for
// sync_file export and the query of what a device exports.
const (
	StructureTypePhysicalDeviceExternalSemaphoreInfo StructureType = 1000076000
	StructureTypeExternalSemaphoreProperties         StructureType = 1000076001
	StructureTypeExportSemaphoreCreateInfo           StructureType = 1000077000
)

// Core 1.1 values vk-gen leaves out that sampling YUV buffers names:
// the sampler YCbCr conversion structures and feature block, the
// 10-bit two-plane 4:2:0 format (P010) and the chroma format features.
const (
	StructureTypeSamplerYcbcrConversionCreateInfo            StructureType = 1000156000
	StructureTypeSamplerYcbcrConversionInfo                  StructureType = 1000156001
	StructureTypeSamplerYcbcrConversionImageFormatProperties StructureType = 1000156005
	StructureTypePhysicalDeviceVulkan11Features              StructureType = 49

	FormatG10x6B10x6r10x62plane420Unorm3pack16 Format = 1000156013

	FormatFeatureMidpointChromaSamplesBit                   FormatFeatureFlagBits = 1 << 17
	FormatFeatureSampledImageYcbcrConversionLinearFilterBit FormatFeatureFlagBits = 1 << 18
	FormatFeatureCositedChromaSamplesBit                    FormatFeatureFlagBits = 1 << 23
)

// The core 1.1 structure type importing a dmabuf as a buffer names,
// left out of const_gen.go.
const StructureTypeExternalMemoryBufferCreateInfo StructureType = 1000072000
