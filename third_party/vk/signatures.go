//go:build !(js && wasm)

// Copyright 2025 The GoGPU Authors
// SPDX-License-Identifier: MIT

// Package vk provides Pure Go Vulkan bindings using goffi.
//
// This file contains CallInterface signatures that are reused across
// multiple Vulkan functions with identical parameter ffi.

package vk

import (
	"github.com/stubbedev/lys-ui/third_party/vk/internal/ffi"
)

// Signature templates - reused across functions with identical signatures.
// Vulkan has ~700 functions but only ~30 unique signatures.
var (
	// === Result-returning signatures ===

	// VkResult(ptr, ptr, ptr) - vkCreateInstance, vkCreateDevice, etc.
	SigResultPtrPtrPtr ffi.CallInterface

	// VkResult(ptr, ptr) - vkBeginCommandBuffer, etc.
	SigResultPtrPtr ffi.CallInterface

	// VkResult(handle, ptr) - vkEndCommandBuffer (returns result)
	SigResultHandlePtr ffi.CallInterface

	// VkResult(handle) - vkEndCommandBuffer
	SigResultHandle ffi.CallInterface

	// VkResult(handle, u32, ptr, ptr) - vkEnumeratePhysicalDevices
	SigResultHandleU32PtrPtr ffi.CallInterface

	// VkResult(handle, ptr, ptr, ptr, ptr) - vkCreateGraphicsPipelines
	SigResultHandlePtrPtrPtrPtr ffi.CallInterface

	// VkResult(handle, u64, u32, ptr) - vkWaitForFences
	SigResultHandleU64U32Ptr ffi.CallInterface

	// VkResult(handle, ptr, ptr) - vkCreateCommandPool, vkCreateFence, etc.
	SigResultHandlePtrPtr ffi.CallInterface

	// VkResult(handle, ptr, ptr, ptr) - vkCreateSwapchainKHR, etc.
	SigResultHandlePtrPtrPtr ffi.CallInterface

	// VkResult(handle, handle, ptr) - vkResetCommandPool
	SigResultHandleHandlePtr ffi.CallInterface

	// VkResult(handle, u32, u32, ptr, ptr) - vkAllocateDescriptorSets
	SigResultHandleU32U32PtrPtr ffi.CallInterface

	// === Void-returning signatures ===

	// void(handle, ptr) - vkDestroyInstance, vkDestroyDevice, etc.
	SigVoidHandlePtr ffi.CallInterface

	// void(handle, handle, ptr) - vkDestroyPipeline, vkDestroyCommandPool, etc.
	SigVoidHandleHandlePtr ffi.CallInterface

	// void(handle, ptr, ptr) - vkGetPhysicalDeviceProperties, etc.
	SigVoidHandlePtrPtr ffi.CallInterface

	// void(handle, u32, ptr) - vkGetPhysicalDeviceQueueFamilyProperties
	SigVoidHandleU32Ptr ffi.CallInterface

	// void(handle) - vkCmdEndRendering
	SigVoidHandle ffi.CallInterface

	// void(handle, u32, u32) - vkCmdBindPipeline (handle, enum, handle)
	SigVoidHandleU32Handle ffi.CallInterface

	// void(handle, u32, u32, u32, u32) - vkCmdDraw
	SigVoidHandleU32x4 ffi.CallInterface

	// void(handle, u32, u32, u32, i32, u32) - vkCmdDrawIndexed
	SigVoidHandleU32x3I32U32 ffi.CallInterface

	// void(handle, u32, u32, ptr, ptr) - vkCmdBindVertexBuffers
	SigVoidHandleU32U32PtrPtr ffi.CallInterface

	// void(handle, handle, u64, u32) - vkCmdBindIndexBuffer
	SigVoidHandleHandleU64U32 ffi.CallInterface

	// void(handle, u32, u32, ptr) - vkCmdSetViewport, vkCmdSetScissor
	SigVoidHandleU32U32Ptr ffi.CallInterface

	// void(handle, ptr) - vkCmdSetBlendConstants
	SigVoidHandleFloatPtr ffi.CallInterface

	// void(handle, u32, u32, u32) - vkCmdSetStencilReference
	SigVoidHandleU32x3 ffi.CallInterface

	// void(handle, u32, handle, u32) - vkCmdWriteTimestamp
	SigVoidHandleU32HandleU32 ffi.CallInterface

	// void(handle, handle, u64, u32) - vkCmdDrawIndirect
	SigVoidHandleHandleU64U32Count ffi.CallInterface

	// void(handle, u32, u32, u32) - vkCmdDispatch
	SigVoidHandleU32U32U32 ffi.CallInterface

	// void(handle, handle, u64) - vkCmdDispatchIndirect
	SigVoidHandleHandleU64 ffi.CallInterface

	// void(handle, ptr) - vkCmdBeginRendering
	SigVoidHandlePtrRendering ffi.CallInterface

	// void(handle, u32, u32, u32, u32, u32, ptr, u32, ptr) - vkCmdBindDescriptorSets
	SigVoidCmdBindDescriptorSets ffi.CallInterface

	// void(handle, ...) - vkCmdPipelineBarrier (11 args)
	SigVoidCmdPipelineBarrier ffi.CallInterface

	// void(handle, handle, u64, u64, u32) - vkCmdFillBuffer
	SigVoidCmdFillBuffer ffi.CallInterface

	// void(handle, handle, handle, u32, ptr) - vkCmdCopyBuffer
	SigVoidCmdCopyBuffer ffi.CallInterface

	// void(handle, handle, handle, u32, u32, ptr) - vkCmdCopyBufferToImage
	SigVoidCmdCopyBufferToImage ffi.CallInterface

	// void(handle, handle, u32, handle, u32, ptr) - vkCmdCopyImageToBuffer
	SigVoidCmdCopyImageToBuffer ffi.CallInterface

	// void(handle, handle, u32, handle, u32, u32, ptr) - vkCmdCopyImage
	SigVoidCmdCopyImage ffi.CallInterface

	// void(device, u32, u32, ptr) - vkUpdateDescriptorSets
	SigVoidDeviceUpdateDescriptorSets ffi.CallInterface

	// void(device, u32, ptr) - vkGetDeviceQueue
	SigVoidDeviceU32Ptr ffi.CallInterface

	// VkResult(device, ptr) - vkQueueSubmit2
	SigResultDevicePtr ffi.CallInterface

	// VkResult(handle, u32, ptr, ptr, ptr) - vkQueueSubmit2
	SigResultHandleU32PtrPtrPtr ffi.CallInterface

	// Additional Result-returning signatures

	// VkResult(ptr) - vkEnumerateInstanceVersion
	SigResultPtr ffi.CallInterface

	// VkResult(handle, handle, u32) - vkResetCommandPool, vkResetDescriptorPool
	SigResultHandleHandleU32 ffi.CallInterface

	// VkResult(handle, u32) - vkResetCommandBuffer
	SigResultHandleU32 ffi.CallInterface

	// VkResult(handle, handle) - vkGetFenceStatus, vkSetEvent, vkResetEvent
	SigResultHandleHandle ffi.CallInterface

	// VkResult(handle, u32, ptr) - vkFlushMappedMemoryRanges
	SigResultHandleU32Ptr ffi.CallInterface

	// VkResult(handle, handle, handle, u64) - vkBindBufferMemory, vkBindImageMemory
	SigResultHandle4 ffi.CallInterface

	// VkResult(handle, handle, u64, u64, u32, ptr) - vkMapMemory
	SigResultMapMemory ffi.CallInterface

	// VkResult(handle, u32, ptr, handle) - vkQueueSubmit
	SigResultHandleU32PtrHandle ffi.CallInterface

	// VkResult(handle, u32, ptr, u32, u64) - vkWaitForFences
	SigResultWaitForFences ffi.CallInterface

	// VkResult(handle, handle, ptr, ptr) - vkGetSwapchainImagesKHR
	SigResultHandleHandlePtrPtr ffi.CallInterface

	// VkResult(handle, handle, u64, handle, handle, ptr) - vkAcquireNextImageKHR
	SigResultAcquireNextImage ffi.CallInterface

	// VkResult(handle, u32, handle, ptr) - vkGetPhysicalDeviceSurfaceSupportKHR
	SigResultHandleU32HandlePtr ffi.CallInterface

	// Additional Void-returning signatures

	// void(handle, handle) - vkUnmapMemory
	SigVoidHandleHandle ffi.CallInterface

	// void(handle, handle, handle) - vkCmdBindPipeline
	SigVoidHandleHandleHandle ffi.CallInterface

	// void(handle, u32, u32) - vkCmdSetStencilCompareMask, etc.
	SigVoidHandleU32U32 ffi.CallInterface

	// void(handle, u32) - vkCmdSetAttachmentFeedbackLoopEnableEXT
	SigVoidHandleU32 ffi.CallInterface

	// void(handle, handle, u32, u32) - vkCmdBeginQuery, vkResetQueryPool
	SigVoidHandleHandleU32U32 ffi.CallInterface

	// void(handle, handle, u32) - vkCmdSetEvent, vkCmdResetEvent
	SigVoidHandleHandleU32 ffi.CallInterface

	// void(handle, handle, handle, u32, u32) - vkCmdDrawIndirect
	SigVoidHandle3U32U32 ffi.CallInterface

	// void(handle, ptr, u32) - vkCmdBeginRenderPass
	SigVoidHandlePtrU32 ffi.CallInterface

	// void(handle, handle, u32, ptr) - vkFreeCommandBuffers
	SigVoidHandleHandleU32Ptr ffi.CallInterface

	// void(handle, f32) - vkCmdSetBlendConstants (receives ptr to float array)
	SigVoidHandleF32 ffi.CallInterface

	// void(handle, handle, u64, u32, u32) - vkCmdDrawIndirect
	SigVoidHandleHandleU64U32U32 ffi.CallInterface

	// VkResult(handle, handle, u32, ptr) - vkFreeDescriptorSets, vkFreeCommandBuffers
	SigResultHandleHandleU32Ptr ffi.CallInterface

	// VkResult(handle, handle, u32, ptr, ptr, ptr) - vkCreateGraphicsPipelines, vkCreateComputePipelines
	SigResultCreatePipelines ffi.CallInterface

	// void(handle, handle, u32, u32, handle, u64, u64, u32) - vkCmdCopyQueryPoolResults
	SigVoidCmdCopyQueryPoolResults ffi.CallInterface

	// VkResult(handle, ptr, u64) - vkWaitSemaphores
	SigResultHandlePtrU64 ffi.CallInterface

	// === Ray tracing signatures ===

	// void(handle, u32, ptr, ptr) - vkCmdBuildAccelerationStructuresKHR
	SigVoidCmdBuildAccelerationStructures ffi.CallInterface

	// void(handle, u32, ptr, u32, handle, u32) - vkCmdWriteAccelerationStructuresPropertiesKHR
	SigVoidCmdWriteASProperties ffi.CallInterface

	// VkDeviceAddress(handle, ptr) - vkGetBufferDeviceAddress, vkGetAccelerationStructureDeviceAddressKHR
	// Returns uint64 (device address), not VkResult.
	SigU64HandlePtr ffi.CallInterface

	// void(handle, handle, ptr, ptr, ptr) - vkGetAccelerationStructureBuildSizesKHR
	SigVoidHandleHandlePtrPtrPtr ffi.CallInterface
)

// InitSignatures prepares all CallInterface templates.
// Must be called once after loading Vulkan library.
func InitSignatures() error {
	var err error

	// Helper for common pointer type slice
	ptr := ffi.PointerTypeDescriptor
	u32 := ffi.UInt32TypeDescriptor
	u64 := ffi.UInt64TypeDescriptor
	i32 := ffi.SInt32TypeDescriptor
	voidRet := ffi.VoidTypeDescriptor
	resultRet := ffi.SInt32TypeDescriptor // VkResult is int32

	// === Result-returning signatures ===

	// VkResult(ptr, ptr, ptr)
	err = ffi.PrepareCallInterface(&SigResultPtrPtrPtr, ffi.DefaultCall, resultRet,
		[]*ffi.TypeDescriptor{ptr, ptr, ptr})
	if err != nil {
		return err
	}

	// VkResult(ptr, ptr)
	err = ffi.PrepareCallInterface(&SigResultPtrPtr, ffi.DefaultCall, resultRet,
		[]*ffi.TypeDescriptor{ptr, ptr})
	if err != nil {
		return err
	}

	// VkResult(handle, ptr)
	err = ffi.PrepareCallInterface(&SigResultHandlePtr, ffi.DefaultCall, resultRet,
		[]*ffi.TypeDescriptor{u64, ptr})
	if err != nil {
		return err
	}

	// VkResult(handle)
	err = ffi.PrepareCallInterface(&SigResultHandle, ffi.DefaultCall, resultRet,
		[]*ffi.TypeDescriptor{u64})
	if err != nil {
		return err
	}

	// VkResult(handle, u32, ptr, ptr)
	err = ffi.PrepareCallInterface(&SigResultHandleU32PtrPtr, ffi.DefaultCall, resultRet,
		[]*ffi.TypeDescriptor{u64, u32, ptr, ptr})
	if err != nil {
		return err
	}

	// VkResult(handle, ptr, ptr, ptr, ptr)
	err = ffi.PrepareCallInterface(&SigResultHandlePtrPtrPtrPtr, ffi.DefaultCall, resultRet,
		[]*ffi.TypeDescriptor{u64, ptr, ptr, ptr, ptr})
	if err != nil {
		return err
	}

	// VkResult(handle, u64, u32, ptr)
	err = ffi.PrepareCallInterface(&SigResultHandleU64U32Ptr, ffi.DefaultCall, resultRet,
		[]*ffi.TypeDescriptor{u64, u64, u32, ptr})
	if err != nil {
		return err
	}

	// VkResult(handle, ptr, ptr)
	err = ffi.PrepareCallInterface(&SigResultHandlePtrPtr, ffi.DefaultCall, resultRet,
		[]*ffi.TypeDescriptor{u64, ptr, ptr})
	if err != nil {
		return err
	}

	// VkResult(handle, ptr, ptr, ptr)
	err = ffi.PrepareCallInterface(&SigResultHandlePtrPtrPtr, ffi.DefaultCall, resultRet,
		[]*ffi.TypeDescriptor{u64, ptr, ptr, ptr})
	if err != nil {
		return err
	}

	// VkResult(handle, handle, ptr)
	err = ffi.PrepareCallInterface(&SigResultHandleHandlePtr, ffi.DefaultCall, resultRet,
		[]*ffi.TypeDescriptor{u64, u64, ptr})
	if err != nil {
		return err
	}

	// VkResult(handle, u32, u32, ptr, ptr)
	err = ffi.PrepareCallInterface(&SigResultHandleU32U32PtrPtr, ffi.DefaultCall, resultRet,
		[]*ffi.TypeDescriptor{u64, u32, u32, ptr, ptr})
	if err != nil {
		return err
	}

	// VkResult(handle, u32, ptr, ptr, ptr)
	err = ffi.PrepareCallInterface(&SigResultHandleU32PtrPtrPtr, ffi.DefaultCall, resultRet,
		[]*ffi.TypeDescriptor{u64, u32, ptr, ptr, ptr})
	if err != nil {
		return err
	}

	// VkResult(device, ptr)
	err = ffi.PrepareCallInterface(&SigResultDevicePtr, ffi.DefaultCall, resultRet,
		[]*ffi.TypeDescriptor{u64, ptr})
	if err != nil {
		return err
	}

	// === Void-returning signatures ===

	// void(handle, ptr)
	err = ffi.PrepareCallInterface(&SigVoidHandlePtr, ffi.DefaultCall, voidRet,
		[]*ffi.TypeDescriptor{u64, ptr})
	if err != nil {
		return err
	}

	// void(handle, handle, ptr)
	err = ffi.PrepareCallInterface(&SigVoidHandleHandlePtr, ffi.DefaultCall, voidRet,
		[]*ffi.TypeDescriptor{u64, u64, ptr})
	if err != nil {
		return err
	}

	// void(handle, ptr, ptr)
	err = ffi.PrepareCallInterface(&SigVoidHandlePtrPtr, ffi.DefaultCall, voidRet,
		[]*ffi.TypeDescriptor{u64, ptr, ptr})
	if err != nil {
		return err
	}

	// void(handle, u32, ptr)
	err = ffi.PrepareCallInterface(&SigVoidHandleU32Ptr, ffi.DefaultCall, voidRet,
		[]*ffi.TypeDescriptor{u64, u32, ptr})
	if err != nil {
		return err
	}

	// void(handle)
	err = ffi.PrepareCallInterface(&SigVoidHandle, ffi.DefaultCall, voidRet,
		[]*ffi.TypeDescriptor{u64})
	if err != nil {
		return err
	}

	// void(handle, u32, handle)
	err = ffi.PrepareCallInterface(&SigVoidHandleU32Handle, ffi.DefaultCall, voidRet,
		[]*ffi.TypeDescriptor{u64, u32, u64})
	if err != nil {
		return err
	}

	// void(handle, u32, u32, u32, u32)
	err = ffi.PrepareCallInterface(&SigVoidHandleU32x4, ffi.DefaultCall, voidRet,
		[]*ffi.TypeDescriptor{u64, u32, u32, u32, u32})
	if err != nil {
		return err
	}

	// void(handle, u32, u32, u32, i32, u32)
	err = ffi.PrepareCallInterface(&SigVoidHandleU32x3I32U32, ffi.DefaultCall, voidRet,
		[]*ffi.TypeDescriptor{u64, u32, u32, u32, i32, u32})
	if err != nil {
		return err
	}

	// void(handle, u32, u32, ptr, ptr)
	err = ffi.PrepareCallInterface(&SigVoidHandleU32U32PtrPtr, ffi.DefaultCall, voidRet,
		[]*ffi.TypeDescriptor{u64, u32, u32, ptr, ptr})
	if err != nil {
		return err
	}

	// void(handle, handle, u64, u32)
	err = ffi.PrepareCallInterface(&SigVoidHandleHandleU64U32, ffi.DefaultCall, voidRet,
		[]*ffi.TypeDescriptor{u64, u64, u64, u32})
	if err != nil {
		return err
	}

	// void(handle, u32, u32, ptr)
	err = ffi.PrepareCallInterface(&SigVoidHandleU32U32Ptr, ffi.DefaultCall, voidRet,
		[]*ffi.TypeDescriptor{u64, u32, u32, ptr})
	if err != nil {
		return err
	}

	// void(handle, ptr) for floats
	err = ffi.PrepareCallInterface(&SigVoidHandleFloatPtr, ffi.DefaultCall, voidRet,
		[]*ffi.TypeDescriptor{u64, ptr})
	if err != nil {
		return err
	}

	// void(handle, u32, u32, u32)
	err = ffi.PrepareCallInterface(&SigVoidHandleU32x3, ffi.DefaultCall, voidRet,
		[]*ffi.TypeDescriptor{u64, u32, u32, u32})
	if err != nil {
		return err
	}

	// void(handle, u32, handle, u32) for CmdWriteTimestamp
	err = ffi.PrepareCallInterface(&SigVoidHandleU32HandleU32, ffi.DefaultCall, voidRet,
		[]*ffi.TypeDescriptor{u64, u32, u64, u32})
	if err != nil {
		return err
	}

	// void(handle, handle, u64, u32) for draw indirect
	err = ffi.PrepareCallInterface(&SigVoidHandleHandleU64U32Count, ffi.DefaultCall, voidRet,
		[]*ffi.TypeDescriptor{u64, u64, u64, u32})
	if err != nil {
		return err
	}

	// void(handle, u32, u32, u32) for dispatch
	err = ffi.PrepareCallInterface(&SigVoidHandleU32U32U32, ffi.DefaultCall, voidRet,
		[]*ffi.TypeDescriptor{u64, u32, u32, u32})
	if err != nil {
		return err
	}

	// void(handle, handle, u64)
	err = ffi.PrepareCallInterface(&SigVoidHandleHandleU64, ffi.DefaultCall, voidRet,
		[]*ffi.TypeDescriptor{u64, u64, u64})
	if err != nil {
		return err
	}

	// void(handle, ptr) for rendering
	err = ffi.PrepareCallInterface(&SigVoidHandlePtrRendering, ffi.DefaultCall, voidRet,
		[]*ffi.TypeDescriptor{u64, ptr})
	if err != nil {
		return err
	}

	// void(handle, u32, u32, u32, u32, u32, ptr, u32, ptr) - vkCmdBindDescriptorSets
	err = ffi.PrepareCallInterface(&SigVoidCmdBindDescriptorSets, ffi.DefaultCall, voidRet,
		[]*ffi.TypeDescriptor{u64, u32, u64, u32, u32, ptr, u32, ptr})
	if err != nil {
		return err
	}

	// void(handle, u32, u32, u32, u32, u32, u32, ptr, u32, ptr, u32, ptr) - vkCmdPipelineBarrier
	err = ffi.PrepareCallInterface(&SigVoidCmdPipelineBarrier, ffi.DefaultCall, voidRet,
		[]*ffi.TypeDescriptor{u64, u32, u32, u32, u32, ptr, u32, ptr, u32, ptr})
	if err != nil {
		return err
	}

	// void(handle, handle, u64, u64, u32) - vkCmdFillBuffer
	err = ffi.PrepareCallInterface(&SigVoidCmdFillBuffer, ffi.DefaultCall, voidRet,
		[]*ffi.TypeDescriptor{u64, u64, u64, u64, u32})
	if err != nil {
		return err
	}

	// void(handle, handle, handle, u32, ptr) - vkCmdCopyBuffer
	err = ffi.PrepareCallInterface(&SigVoidCmdCopyBuffer, ffi.DefaultCall, voidRet,
		[]*ffi.TypeDescriptor{u64, u64, u64, u32, ptr})
	if err != nil {
		return err
	}

	// void(handle, handle, handle, u32, u32, ptr) - vkCmdCopyBufferToImage
	err = ffi.PrepareCallInterface(&SigVoidCmdCopyBufferToImage, ffi.DefaultCall, voidRet,
		[]*ffi.TypeDescriptor{u64, u64, u64, u32, u32, ptr})
	if err != nil {
		return err
	}

	// void(handle, handle, u32, handle, u32, ptr) - vkCmdCopyImageToBuffer
	err = ffi.PrepareCallInterface(&SigVoidCmdCopyImageToBuffer, ffi.DefaultCall, voidRet,
		[]*ffi.TypeDescriptor{u64, u64, u32, u64, u32, ptr})
	if err != nil {
		return err
	}

	// void(handle, handle, u32, handle, u32, u32, ptr) - vkCmdCopyImage
	err = ffi.PrepareCallInterface(&SigVoidCmdCopyImage, ffi.DefaultCall, voidRet,
		[]*ffi.TypeDescriptor{u64, u64, u32, u64, u32, u32, ptr})
	if err != nil {
		return err
	}

	// void(device, u32, u32, ptr) - vkUpdateDescriptorSets
	err = ffi.PrepareCallInterface(&SigVoidDeviceUpdateDescriptorSets, ffi.DefaultCall, voidRet,
		[]*ffi.TypeDescriptor{u64, u32, ptr, u32, ptr})
	if err != nil {
		return err
	}

	// void(device, u32, ptr) - vkGetDeviceQueue
	err = ffi.PrepareCallInterface(&SigVoidDeviceU32Ptr, ffi.DefaultCall, voidRet,
		[]*ffi.TypeDescriptor{u64, u32, u32, ptr})
	if err != nil {
		return err
	}

	// === Additional Result-returning signatures ===

	// VkResult(ptr)
	err = ffi.PrepareCallInterface(&SigResultPtr, ffi.DefaultCall, resultRet,
		[]*ffi.TypeDescriptor{ptr})
	if err != nil {
		return err
	}

	// VkResult(handle, handle, u32)
	err = ffi.PrepareCallInterface(&SigResultHandleHandleU32, ffi.DefaultCall, resultRet,
		[]*ffi.TypeDescriptor{u64, u64, u32})
	if err != nil {
		return err
	}

	// VkResult(handle, u32)
	err = ffi.PrepareCallInterface(&SigResultHandleU32, ffi.DefaultCall, resultRet,
		[]*ffi.TypeDescriptor{u64, u32})
	if err != nil {
		return err
	}

	// VkResult(handle, handle)
	err = ffi.PrepareCallInterface(&SigResultHandleHandle, ffi.DefaultCall, resultRet,
		[]*ffi.TypeDescriptor{u64, u64})
	if err != nil {
		return err
	}

	// VkResult(handle, u32, ptr)
	err = ffi.PrepareCallInterface(&SigResultHandleU32Ptr, ffi.DefaultCall, resultRet,
		[]*ffi.TypeDescriptor{u64, u32, ptr})
	if err != nil {
		return err
	}

	// VkResult(handle, handle, handle, u64) - vkBindBufferMemory
	err = ffi.PrepareCallInterface(&SigResultHandle4, ffi.DefaultCall, resultRet,
		[]*ffi.TypeDescriptor{u64, u64, u64, u64})
	if err != nil {
		return err
	}

	// VkResult(handle, handle, u64, u64, u32, ptr) - vkMapMemory
	err = ffi.PrepareCallInterface(&SigResultMapMemory, ffi.DefaultCall, resultRet,
		[]*ffi.TypeDescriptor{u64, u64, u64, u64, u32, ptr})
	if err != nil {
		return err
	}

	// VkResult(handle, u32, ptr, handle) - vkQueueSubmit
	err = ffi.PrepareCallInterface(&SigResultHandleU32PtrHandle, ffi.DefaultCall, resultRet,
		[]*ffi.TypeDescriptor{u64, u32, ptr, u64})
	if err != nil {
		return err
	}

	// VkResult(handle, u32, ptr, u32, u64) - vkWaitForFences
	err = ffi.PrepareCallInterface(&SigResultWaitForFences, ffi.DefaultCall, resultRet,
		[]*ffi.TypeDescriptor{u64, u32, ptr, u32, u64})
	if err != nil {
		return err
	}

	// VkResult(handle, handle, ptr, ptr)
	err = ffi.PrepareCallInterface(&SigResultHandleHandlePtrPtr, ffi.DefaultCall, resultRet,
		[]*ffi.TypeDescriptor{u64, u64, ptr, ptr})
	if err != nil {
		return err
	}

	// VkResult(handle, handle, u64, handle, handle, ptr) - vkAcquireNextImageKHR
	err = ffi.PrepareCallInterface(&SigResultAcquireNextImage, ffi.DefaultCall, resultRet,
		[]*ffi.TypeDescriptor{u64, u64, u64, u64, u64, ptr})
	if err != nil {
		return err
	}

	// VkResult(handle, u32, handle, ptr) - vkGetPhysicalDeviceSurfaceSupportKHR
	err = ffi.PrepareCallInterface(&SigResultHandleU32HandlePtr, ffi.DefaultCall, resultRet,
		[]*ffi.TypeDescriptor{u64, u32, u64, ptr})
	if err != nil {
		return err
	}

	// === Additional Void-returning signatures ===

	// void(handle, handle)
	err = ffi.PrepareCallInterface(&SigVoidHandleHandle, ffi.DefaultCall, voidRet,
		[]*ffi.TypeDescriptor{u64, u64})
	if err != nil {
		return err
	}

	// void(handle, handle, handle)
	err = ffi.PrepareCallInterface(&SigVoidHandleHandleHandle, ffi.DefaultCall, voidRet,
		[]*ffi.TypeDescriptor{u64, u64, u64})
	if err != nil {
		return err
	}

	// void(handle, u32, u32)
	err = ffi.PrepareCallInterface(&SigVoidHandleU32U32, ffi.DefaultCall, voidRet,
		[]*ffi.TypeDescriptor{u64, u32, u32})
	if err != nil {
		return err
	}

	// void(handle, u32)
	err = ffi.PrepareCallInterface(&SigVoidHandleU32, ffi.DefaultCall, voidRet,
		[]*ffi.TypeDescriptor{u64, u32})
	if err != nil {
		return err
	}

	// void(handle, handle, u32, u32)
	err = ffi.PrepareCallInterface(&SigVoidHandleHandleU32U32, ffi.DefaultCall, voidRet,
		[]*ffi.TypeDescriptor{u64, u64, u32, u32})
	if err != nil {
		return err
	}

	// void(handle, handle, u32)
	err = ffi.PrepareCallInterface(&SigVoidHandleHandleU32, ffi.DefaultCall, voidRet,
		[]*ffi.TypeDescriptor{u64, u64, u32})
	if err != nil {
		return err
	}

	// void(handle, handle, handle, u32, u32)
	err = ffi.PrepareCallInterface(&SigVoidHandle3U32U32, ffi.DefaultCall, voidRet,
		[]*ffi.TypeDescriptor{u64, u64, u64, u32, u32})
	if err != nil {
		return err
	}

	// void(handle, ptr, u32)
	err = ffi.PrepareCallInterface(&SigVoidHandlePtrU32, ffi.DefaultCall, voidRet,
		[]*ffi.TypeDescriptor{u64, ptr, u32})
	if err != nil {
		return err
	}

	// void(handle, handle, u32, ptr)
	err = ffi.PrepareCallInterface(&SigVoidHandleHandleU32Ptr, ffi.DefaultCall, voidRet,
		[]*ffi.TypeDescriptor{u64, u64, u32, ptr})
	if err != nil {
		return err
	}

	// void(handle, f32) - for blend constants (ptr)
	f32 := ffi.FloatTypeDescriptor
	err = ffi.PrepareCallInterface(&SigVoidHandleF32, ffi.DefaultCall, voidRet,
		[]*ffi.TypeDescriptor{u64, f32})
	if err != nil {
		return err
	}

	// void(handle, handle, u64, u32, u32)
	err = ffi.PrepareCallInterface(&SigVoidHandleHandleU64U32U32, ffi.DefaultCall, voidRet,
		[]*ffi.TypeDescriptor{u64, u64, u64, u32, u32})
	if err != nil {
		return err
	}

	// VkResult(handle, handle, u32, ptr) - vkFreeDescriptorSets
	err = ffi.PrepareCallInterface(&SigResultHandleHandleU32Ptr, ffi.DefaultCall, resultRet,
		[]*ffi.TypeDescriptor{u64, u64, u32, ptr})
	if err != nil {
		return err
	}

	// VkResult(handle, handle, u32, ptr, ptr, ptr) - vkCreateGraphicsPipelines
	err = ffi.PrepareCallInterface(&SigResultCreatePipelines, ffi.DefaultCall, resultRet,
		[]*ffi.TypeDescriptor{u64, u64, u32, ptr, ptr, ptr})
	if err != nil {
		return err
	}

	// void(handle, handle, u32, u32, handle, u64, u64, u32) - vkCmdCopyQueryPoolResults
	err = ffi.PrepareCallInterface(&SigVoidCmdCopyQueryPoolResults, ffi.DefaultCall, voidRet,
		[]*ffi.TypeDescriptor{u64, u64, u32, u32, u64, u64, u64, u32})
	if err != nil {
		return err
	}

	// VkResult(handle, ptr, u64) - vkWaitSemaphores
	err = ffi.PrepareCallInterface(&SigResultHandlePtrU64, ffi.DefaultCall, resultRet,
		[]*ffi.TypeDescriptor{u64, ptr, u64})
	if err != nil {
		return err
	}

	// === Ray tracing signatures ===

	// void(handle, u32, ptr, ptr) - vkCmdBuildAccelerationStructuresKHR
	err = ffi.PrepareCallInterface(&SigVoidCmdBuildAccelerationStructures, ffi.DefaultCall, voidRet,
		[]*ffi.TypeDescriptor{u64, u32, ptr, ptr})
	if err != nil {
		return err
	}

	// void(handle, u32, ptr, u32, handle, u32) - vkCmdWriteAccelerationStructuresPropertiesKHR
	err = ffi.PrepareCallInterface(&SigVoidCmdWriteASProperties, ffi.DefaultCall, voidRet,
		[]*ffi.TypeDescriptor{u64, u32, ptr, u32, u64, u32})
	if err != nil {
		return err
	}

	// VkDeviceAddress(handle, ptr) - returns uint64
	err = ffi.PrepareCallInterface(&SigU64HandlePtr, ffi.DefaultCall, u64,
		[]*ffi.TypeDescriptor{u64, ptr})
	if err != nil {
		return err
	}

	// void(handle, handle, ptr, ptr, ptr) - vkGetAccelerationStructureBuildSizesKHR
	err = ffi.PrepareCallInterface(&SigVoidHandleHandlePtrPtrPtr, ffi.DefaultCall, voidRet,
		[]*ffi.TypeDescriptor{u64, u64, ptr, ptr, ptr})
	if err != nil {
		return err
	}

	return nil
}
