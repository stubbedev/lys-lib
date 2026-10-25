package vk

// Core 1.1 structure types vk-gen leaves out (it emits only the
// extension aliases): external memory and dedicated allocation.
const (
	StructureTypeExternalMemoryImageCreateInfo StructureType = 1000072001
	StructureTypeExportMemoryAllocateInfo      StructureType = 1000072002
	StructureTypeMemoryDedicatedAllocateInfo   StructureType = 1000127001
	StructureTypeFormatProperties2             StructureType = 1000059002
)
