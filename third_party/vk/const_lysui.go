package vk

// Core 1.1 structure types vk-gen leaves out (it emits only the
// extension aliases): external memory and dedicated allocation.
const (
	StructureTypeExternalMemoryImageCreateInfo StructureType = 1000072001
	StructureTypeExportMemoryAllocateInfo      StructureType = 1000072002
	StructureTypeMemoryDedicatedAllocateInfo   StructureType = 1000127001
	StructureTypeFormatProperties2             StructureType = 1000059002
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
