//go:build !(js && wasm)

package vk

import (
	"sync"
	"unsafe"

	"github.com/stubbedev/lys-ui/third_party/vk/internal/ffi"
)

// The call interfaces vk-gen has no template for, prepared on first use.
var (
	lysuiSigsOnce             sync.Once
	lysuiSigsErr              error
	sigVoidHandleHandlePtrPtr ffi.CallInterface
	sigVoidCmdClearColorImage ffi.CallInterface
	sigResultHandleU32I32Ptr  ffi.CallInterface
	sigVoidCmdPushConstants   ffi.CallInterface
)

func lysuiSigs() error {
	lysuiSigsOnce.Do(func() {
		u64, u32, i32, ptr := ffi.UInt64TypeDescriptor, ffi.UInt32TypeDescriptor, ffi.SInt32TypeDescriptor, ffi.PointerTypeDescriptor
		if lysuiSigsErr = ffi.PrepareCallInterface(&sigVoidHandleHandlePtrPtr, ffi.DefaultCall, ffi.VoidTypeDescriptor,
			[]*ffi.TypeDescriptor{u64, u64, ptr, ptr}); lysuiSigsErr != nil {
			return
		}
		if lysuiSigsErr = ffi.PrepareCallInterface(&sigVoidCmdClearColorImage, ffi.DefaultCall, ffi.VoidTypeDescriptor,
			[]*ffi.TypeDescriptor{u64, u64, u32, ptr, u32, ptr}); lysuiSigsErr != nil {
			return
		}
		if lysuiSigsErr = ffi.PrepareCallInterface(&sigResultHandleU32I32Ptr, ffi.DefaultCall, ffi.SInt32TypeDescriptor,
			[]*ffi.TypeDescriptor{u64, u32, i32, ptr}); lysuiSigsErr != nil {
			return
		}
		lysuiSigsErr = ffi.PrepareCallInterface(&sigVoidCmdPushConstants, ffi.DefaultCall, ffi.VoidTypeDescriptor,
			[]*ffi.TypeDescriptor{u64, u64, u32, u32, u32, ptr})
	})
	return lysuiSigsErr
}

// GetImageSubresourceLayout wraps vkGetImageSubresourceLayout.
func (c *Commands) GetImageSubresourceLayout(device Device, image Image, pSubresource *ImageSubresource, pLayout *SubresourceLayout) {
	if c.getImageSubresourceLayout == nil || lysuiSigs() != nil {
		return
	}
	args := [4]unsafe.Pointer{unsafe.Pointer(&device), unsafe.Pointer(&image), unsafe.Pointer(&pSubresource), unsafe.Pointer(&pLayout)}
	_, _ = ffi.CallFunction(&sigVoidHandleHandlePtrPtr, c.getImageSubresourceLayout, nil, args[:])
}

// CmdClearColorImage wraps vkCmdClearColorImage.
func (c *Commands) CmdClearColorImage(commandBuffer CommandBuffer, image Image, imageLayout ImageLayout, pColor *ClearColorValue, rangeCount uint32, pRanges *ImageSubresourceRange) {
	if c.cmdClearColorImage == nil || lysuiSigs() != nil {
		return
	}
	layout := uint32(imageLayout)
	args := [6]unsafe.Pointer{unsafe.Pointer(&commandBuffer), unsafe.Pointer(&image), unsafe.Pointer(&layout), unsafe.Pointer(&pColor), unsafe.Pointer(&rangeCount), unsafe.Pointer(&pRanges)}
	_, _ = ffi.CallFunction(&sigVoidCmdClearColorImage, c.cmdClearColorImage, nil, args[:])
}

// GetMemoryFdPropertiesKHR wraps vkGetMemoryFdPropertiesKHR: the
// memory types an imported fd (a client's dmabuf) can bind as.
func (c *Commands) GetMemoryFdPropertiesKHR(device Device, handleType ExternalMemoryHandleTypeFlagBits, fd int32, pMemoryFdProperties *MemoryFdPropertiesKHR) Result {
	if c.getMemoryFdPropertiesKHR == nil {
		return ErrorExtensionNotPresent
	}
	if err := lysuiSigs(); err != nil {
		return ErrorInitializationFailed
	}
	var result int32
	ht := uint32(handleType)
	args := [4]unsafe.Pointer{unsafe.Pointer(&device), unsafe.Pointer(&ht), unsafe.Pointer(&fd), unsafe.Pointer(&pMemoryFdProperties)}
	if _, err := ffi.CallFunction(&sigResultHandleU32I32Ptr, c.getMemoryFdPropertiesKHR, unsafe.Pointer(&result), args[:]); err != nil {
		return ErrorInitializationFailed
	}
	return Result(result)
}

// CmdPushConstants wraps vkCmdPushConstants.
func (c *Commands) CmdPushConstants(commandBuffer CommandBuffer, layout PipelineLayout, stageFlags ShaderStageFlags, offset, size uint32, pValues unsafe.Pointer) {
	if c.cmdPushConstants == nil || lysuiSigs() != nil {
		return
	}
	stages := uint32(stageFlags)
	args := [6]unsafe.Pointer{unsafe.Pointer(&commandBuffer), unsafe.Pointer(&layout), unsafe.Pointer(&stages), unsafe.Pointer(&offset), unsafe.Pointer(&size), unsafe.Pointer(&pValues)}
	_, _ = ffi.CallFunction(&sigVoidCmdPushConstants, c.cmdPushConstants, nil, args[:])
}
