//go:build !(js && wasm)

// Copyright 2025 The GoGPU Authors
// SPDX-License-Identifier: MIT

package vk

// This file contains manual function pointer loading for Vulkan commands.
// The Commands struct fields are defined in commands_gen.go (auto-generated).
//
// # Function Loading Hierarchy
//
// Vulkan functions are loaded in three stages:
//
//  1. LoadGlobal() — Functions callable without instance (pre-instance)
//     - vkCreateInstance
//     - vkEnumerateInstanceVersion
//     - vkEnumerateInstanceLayerProperties
//     - vkEnumerateInstanceExtensionProperties
//
//  2. LoadInstance(instance) — Instance-level functions
//     - Core: vkDestroyInstance, vkEnumeratePhysicalDevices, vkCreateDevice
//     - WSI:  vkCreateWin32SurfaceKHR, vkGetPhysicalDeviceSurfaceSupportKHR, etc.
//     - vkGetDeviceProcAddr too, which LoadDevice looks device
//       functions up through
//
//  3. LoadDevice(device) — Device-level functions
//     - Memory: vkAllocateMemory, vkFreeMemory, vkMapMemory
//     - Buffers: vkCreateBuffer, vkDestroyBuffer
//     - Images: vkCreateImage, vkCreateImageView
//     - Pipelines: vkCreateGraphicsPipelines, vkCreateComputePipelines
//     - Commands: vkBeginCommandBuffer, vkCmdDraw, etc.
//     - Swapchain: vkCreateSwapchainKHR, vkAcquireNextImageKHR, vkQueuePresentKHR
//
// # Intel Driver Notes
//
// Intel Iris Xe drivers require special handling:
//   - vkGetInstanceProcAddr(NULL, "vkGetDeviceProcAddr") returns NULL,
//     so each instance's own is used

import (
	"fmt"
	"unsafe"
)

// NewCommands creates a new Commands instance.
// Function pointers must be loaded via LoadGlobal() and LoadInstance() before use.
func NewCommands() *Commands {
	return &Commands{}
}

// LoadGlobal loads global Vulkan function pointers.
// These are functions that can be called without an instance (like vkCreateInstance).
func (c *Commands) LoadGlobal() error {
	// Load vkCreateInstance
	c.createInstance = GetInstanceProcAddr(0, "vkCreateInstance")
	if c.createInstance == nil {
		return fmt.Errorf("failed to load vkCreateInstance")
	}

	// Load vkEnumerateInstanceVersion
	c.enumerateInstanceVersion = GetInstanceProcAddr(0, "vkEnumerateInstanceVersion")

	// Load vkEnumerateInstanceLayerProperties
	c.enumerateInstanceLayerProperties = GetInstanceProcAddr(0, "vkEnumerateInstanceLayerProperties")

	// Load vkEnumerateInstanceExtensionProperties
	c.enumerateInstanceExtensionProperties = GetInstanceProcAddr(0, "vkEnumerateInstanceExtensionProperties")

	return nil
}

// LoadInstance loads instance-level Vulkan function pointers.
// Must be called after vkCreateInstance succeeds.
func (c *Commands) LoadInstance(instance Instance) error {
	if instance == 0 {
		return fmt.Errorf("invalid instance handle")
	}

	// Load all instance-level functions
	c.destroyInstance = GetInstanceProcAddr(instance, "vkDestroyInstance")
	c.enumeratePhysicalDevices = GetInstanceProcAddr(instance, "vkEnumeratePhysicalDevices")
	c.getPhysicalDeviceProperties = GetInstanceProcAddr(instance, "vkGetPhysicalDeviceProperties")
	c.getPhysicalDeviceQueueFamilyProperties = GetInstanceProcAddr(instance, "vkGetPhysicalDeviceQueueFamilyProperties")
	c.getPhysicalDeviceMemoryProperties = GetInstanceProcAddr(instance, "vkGetPhysicalDeviceMemoryProperties")
	c.getPhysicalDeviceFeatures = GetInstanceProcAddr(instance, "vkGetPhysicalDeviceFeatures")
	c.getPhysicalDeviceFormatProperties = GetInstanceProcAddr(instance, "vkGetPhysicalDeviceFormatProperties")
	c.getPhysicalDeviceImageFormatProperties = GetInstanceProcAddr(instance, "vkGetPhysicalDeviceImageFormatProperties")
	c.createDevice = GetInstanceProcAddr(instance, "vkCreateDevice")
	c.getDeviceProcAddr = GetInstanceProcAddr(instance, "vkGetDeviceProcAddr")
	c.enumerateDeviceLayerProperties = GetInstanceProcAddr(instance, "vkEnumerateDeviceLayerProperties")
	c.enumerateDeviceExtensionProperties = GetInstanceProcAddr(instance, "vkEnumerateDeviceExtensionProperties")
	c.getPhysicalDeviceSparseImageFormatProperties = GetInstanceProcAddr(instance, "vkGetPhysicalDeviceSparseImageFormatProperties")

	// Load WSI (Window System Integration) functions
	c.destroySurfaceKHR = GetInstanceProcAddr(instance, "vkDestroySurfaceKHR")
	c.getPhysicalDeviceSurfaceSupportKHR = GetInstanceProcAddr(instance, "vkGetPhysicalDeviceSurfaceSupportKHR")
	c.getPhysicalDeviceSurfaceCapabilitiesKHR = GetInstanceProcAddr(instance, "vkGetPhysicalDeviceSurfaceCapabilitiesKHR")
	c.getPhysicalDeviceSurfaceFormatsKHR = GetInstanceProcAddr(instance, "vkGetPhysicalDeviceSurfaceFormatsKHR")
	c.getPhysicalDeviceSurfacePresentModesKHR = GetInstanceProcAddr(instance, "vkGetPhysicalDeviceSurfacePresentModesKHR")

	// Platform-specific surface creation (nil on unsupported platforms)
	c.createWin32SurfaceKHR = GetInstanceProcAddr(instance, "vkCreateWin32SurfaceKHR")
	c.createXlibSurfaceKHR = GetInstanceProcAddr(instance, "vkCreateXlibSurfaceKHR")
	c.createXcbSurfaceKHR = GetInstanceProcAddr(instance, "vkCreateXcbSurfaceKHR")
	c.createWaylandSurfaceKHR = GetInstanceProcAddr(instance, "vkCreateWaylandSurfaceKHR")
	c.createAndroidSurfaceKHR = GetInstanceProcAddr(instance, "vkCreateAndroidSurfaceKHR")
	c.createMetalSurfaceEXT = GetInstanceProcAddr(instance, "vkCreateMetalSurfaceEXT")

	// Vulkan 1.1+ instance functions
	c.getPhysicalDeviceFeatures2 = GetInstanceProcAddr(instance, "vkGetPhysicalDeviceFeatures2")
	c.getPhysicalDeviceProperties2 = GetInstanceProcAddr(instance, "vkGetPhysicalDeviceProperties2")
	c.getPhysicalDeviceFormatProperties2 = GetInstanceProcAddr(instance, "vkGetPhysicalDeviceFormatProperties2")
	c.getPhysicalDeviceQueueFamilyProperties2 = GetInstanceProcAddr(instance, "vkGetPhysicalDeviceQueueFamilyProperties2")
	c.getPhysicalDeviceImageFormatProperties2 = GetInstanceProcAddr(instance, "vkGetPhysicalDeviceImageFormatProperties2")

	// VK_KHR_video_queue and VK_KHR_video_encode_queue: what a device
	// encodes, in which formats and at which quality levels.
	c.getPhysicalDeviceVideoCapabilitiesKHR = GetInstanceProcAddr(instance, "vkGetPhysicalDeviceVideoCapabilitiesKHR")
	c.getPhysicalDeviceVideoFormatPropertiesKHR = GetInstanceProcAddr(instance, "vkGetPhysicalDeviceVideoFormatPropertiesKHR")
	c.getPhysicalDeviceVideoEncodeQualityLevelPropertiesKHR = GetInstanceProcAddr(instance, "vkGetPhysicalDeviceVideoEncodeQualityLevelPropertiesKHR")

	// VK_EXT_debug_utils (instance extension — MUST use GetInstanceProcAddr).
	// Loading via GetDeviceProcAddr bypasses the validation layer's handle
	// wrapping on NVIDIA drivers, causing "Invalid VkDescriptorPool" errors.
	// See: https://github.com/gogpu/gogpu/issues/98
	c.setDebugUtilsObjectNameEXT = GetInstanceProcAddr(instance, "vkSetDebugUtilsObjectNameEXT")
	c.createDebugUtilsMessengerEXT = GetInstanceProcAddr(instance, "vkCreateDebugUtilsMessengerEXT")
	c.destroyDebugUtilsMessengerEXT = GetInstanceProcAddr(instance, "vkDestroyDebugUtilsMessengerEXT")

	// Verify critical functions loaded
	if c.destroyInstance == nil || c.enumeratePhysicalDevices == nil || c.createDevice == nil {
		return fmt.Errorf("failed to load critical instance functions")
	}

	// The WSI query functions (VK_KHR_surface) are optional: an
	// instance that renders offscreen or into exported memory enables no
	// surface extension. Surface users check HasWSIQueries.

	return nil
}

// LoadDevice loads device-level Vulkan function pointers.
// Must be called after vkCreateDevice succeeds.
func (c *Commands) LoadDevice(device Device) error {
	if device == 0 {
		return fmt.Errorf("invalid device handle")
	}

	// Load device-level functions via vkGetDeviceProcAddr
	// For now, use GetDeviceProcAddr from loader.go
	c.destroyDevice = c.deviceProcAddr(device, "vkDestroyDevice")
	c.getDeviceQueue = c.deviceProcAddr(device, "vkGetDeviceQueue")
	c.queueSubmit = c.deviceProcAddr(device, "vkQueueSubmit")
	c.queueWaitIdle = c.deviceProcAddr(device, "vkQueueWaitIdle")
	c.deviceWaitIdle = c.deviceProcAddr(device, "vkDeviceWaitIdle")
	c.allocateMemory = c.deviceProcAddr(device, "vkAllocateMemory")
	c.freeMemory = c.deviceProcAddr(device, "vkFreeMemory")
	c.mapMemory = c.deviceProcAddr(device, "vkMapMemory")
	c.unmapMemory = c.deviceProcAddr(device, "vkUnmapMemory")
	c.flushMappedMemoryRanges = c.deviceProcAddr(device, "vkFlushMappedMemoryRanges")
	c.invalidateMappedMemoryRanges = c.deviceProcAddr(device, "vkInvalidateMappedMemoryRanges")
	c.getDeviceMemoryCommitment = c.deviceProcAddr(device, "vkGetDeviceMemoryCommitment")
	c.getBufferMemoryRequirements = c.deviceProcAddr(device, "vkGetBufferMemoryRequirements")
	c.bindBufferMemory = c.deviceProcAddr(device, "vkBindBufferMemory")
	c.getImageMemoryRequirements = c.deviceProcAddr(device, "vkGetImageMemoryRequirements")
	c.bindImageMemory = c.deviceProcAddr(device, "vkBindImageMemory")
	c.getImageSparseMemoryRequirements = c.deviceProcAddr(device, "vkGetImageSparseMemoryRequirements")
	c.queueBindSparse = c.deviceProcAddr(device, "vkQueueBindSparse")
	c.createFence = c.deviceProcAddr(device, "vkCreateFence")
	c.destroyFence = c.deviceProcAddr(device, "vkDestroyFence")
	c.resetFences = c.deviceProcAddr(device, "vkResetFences")
	c.getFenceStatus = c.deviceProcAddr(device, "vkGetFenceStatus")
	c.waitForFences = c.deviceProcAddr(device, "vkWaitForFences")
	c.createSemaphore = c.deviceProcAddr(device, "vkCreateSemaphore")
	c.destroySemaphore = c.deviceProcAddr(device, "vkDestroySemaphore")
	c.createEvent = c.deviceProcAddr(device, "vkCreateEvent")
	c.destroyEvent = c.deviceProcAddr(device, "vkDestroyEvent")
	c.getEventStatus = c.deviceProcAddr(device, "vkGetEventStatus")
	c.setEvent = c.deviceProcAddr(device, "vkSetEvent")
	c.resetEvent = c.deviceProcAddr(device, "vkResetEvent")
	c.createQueryPool = c.deviceProcAddr(device, "vkCreateQueryPool")
	c.destroyQueryPool = c.deviceProcAddr(device, "vkDestroyQueryPool")
	c.getQueryPoolResults = c.deviceProcAddr(device, "vkGetQueryPoolResults")
	c.resetQueryPool = c.deviceProcAddr(device, "vkResetQueryPool")
	c.createBuffer = c.deviceProcAddr(device, "vkCreateBuffer")
	c.destroyBuffer = c.deviceProcAddr(device, "vkDestroyBuffer")
	c.createBufferView = c.deviceProcAddr(device, "vkCreateBufferView")
	c.destroyBufferView = c.deviceProcAddr(device, "vkDestroyBufferView")
	c.createImage = c.deviceProcAddr(device, "vkCreateImage")
	c.destroyImage = c.deviceProcAddr(device, "vkDestroyImage")
	c.getImageSubresourceLayout = c.deviceProcAddr(device, "vkGetImageSubresourceLayout")
	// VK_KHR_external_memory_fd and VK_EXT_image_drm_format_modifier:
	// exporting an image's memory as a dmabuf and reading its layout.
	c.getMemoryFdKHR = c.deviceProcAddr(device, "vkGetMemoryFdKHR")
	c.getMemoryFdPropertiesKHR = c.deviceProcAddr(device, "vkGetMemoryFdPropertiesKHR")
	c.getImageDrmFormatModifierPropertiesEXT = c.deviceProcAddr(device, "vkGetImageDrmFormatModifierPropertiesEXT")
	c.createImageView = c.deviceProcAddr(device, "vkCreateImageView")
	c.destroyImageView = c.deviceProcAddr(device, "vkDestroyImageView")
	c.createShaderModule = c.deviceProcAddr(device, "vkCreateShaderModule")
	c.destroyShaderModule = c.deviceProcAddr(device, "vkDestroyShaderModule")
	c.createPipelineCache = c.deviceProcAddr(device, "vkCreatePipelineCache")
	c.destroyPipelineCache = c.deviceProcAddr(device, "vkDestroyPipelineCache")
	c.getPipelineCacheData = c.deviceProcAddr(device, "vkGetPipelineCacheData")
	c.mergePipelineCaches = c.deviceProcAddr(device, "vkMergePipelineCaches")
	c.createGraphicsPipelines = c.deviceProcAddr(device, "vkCreateGraphicsPipelines")
	c.createComputePipelines = c.deviceProcAddr(device, "vkCreateComputePipelines")
	c.destroyPipeline = c.deviceProcAddr(device, "vkDestroyPipeline")
	c.createPipelineLayout = c.deviceProcAddr(device, "vkCreatePipelineLayout")
	c.destroyPipelineLayout = c.deviceProcAddr(device, "vkDestroyPipelineLayout")
	c.createSampler = c.deviceProcAddr(device, "vkCreateSampler")
	c.destroySampler = c.deviceProcAddr(device, "vkDestroySampler")
	c.createDescriptorSetLayout = c.deviceProcAddr(device, "vkCreateDescriptorSetLayout")
	c.destroyDescriptorSetLayout = c.deviceProcAddr(device, "vkDestroyDescriptorSetLayout")
	c.createDescriptorPool = c.deviceProcAddr(device, "vkCreateDescriptorPool")
	c.destroyDescriptorPool = c.deviceProcAddr(device, "vkDestroyDescriptorPool")
	c.resetDescriptorPool = c.deviceProcAddr(device, "vkResetDescriptorPool")
	c.allocateDescriptorSets = c.deviceProcAddr(device, "vkAllocateDescriptorSets")
	c.freeDescriptorSets = c.deviceProcAddr(device, "vkFreeDescriptorSets")
	c.updateDescriptorSets = c.deviceProcAddr(device, "vkUpdateDescriptorSets")
	c.createFramebuffer = c.deviceProcAddr(device, "vkCreateFramebuffer")
	c.destroyFramebuffer = c.deviceProcAddr(device, "vkDestroyFramebuffer")
	c.createRenderPass = c.deviceProcAddr(device, "vkCreateRenderPass")
	c.destroyRenderPass = c.deviceProcAddr(device, "vkDestroyRenderPass")
	c.getRenderAreaGranularity = c.deviceProcAddr(device, "vkGetRenderAreaGranularity")
	c.createCommandPool = c.deviceProcAddr(device, "vkCreateCommandPool")
	c.destroyCommandPool = c.deviceProcAddr(device, "vkDestroyCommandPool")
	c.resetCommandPool = c.deviceProcAddr(device, "vkResetCommandPool")
	c.allocateCommandBuffers = c.deviceProcAddr(device, "vkAllocateCommandBuffers")
	c.freeCommandBuffers = c.deviceProcAddr(device, "vkFreeCommandBuffers")
	c.beginCommandBuffer = c.deviceProcAddr(device, "vkBeginCommandBuffer")
	c.endCommandBuffer = c.deviceProcAddr(device, "vkEndCommandBuffer")
	c.resetCommandBuffer = c.deviceProcAddr(device, "vkResetCommandBuffer")
	c.cmdBindPipeline = c.deviceProcAddr(device, "vkCmdBindPipeline")
	c.cmdSetViewport = c.deviceProcAddr(device, "vkCmdSetViewport")
	c.cmdSetScissor = c.deviceProcAddr(device, "vkCmdSetScissor")
	c.cmdSetLineWidth = c.deviceProcAddr(device, "vkCmdSetLineWidth")
	c.cmdSetDepthBias = c.deviceProcAddr(device, "vkCmdSetDepthBias")
	c.cmdSetBlendConstants = c.deviceProcAddr(device, "vkCmdSetBlendConstants")
	c.cmdSetDepthBounds = c.deviceProcAddr(device, "vkCmdSetDepthBounds")
	c.cmdSetStencilCompareMask = c.deviceProcAddr(device, "vkCmdSetStencilCompareMask")
	c.cmdSetStencilWriteMask = c.deviceProcAddr(device, "vkCmdSetStencilWriteMask")
	c.cmdSetStencilReference = c.deviceProcAddr(device, "vkCmdSetStencilReference")
	c.cmdBindDescriptorSets = c.deviceProcAddr(device, "vkCmdBindDescriptorSets")
	c.cmdBindIndexBuffer = c.deviceProcAddr(device, "vkCmdBindIndexBuffer")
	c.cmdBindVertexBuffers = c.deviceProcAddr(device, "vkCmdBindVertexBuffers")
	c.cmdDraw = c.deviceProcAddr(device, "vkCmdDraw")
	c.cmdDrawIndexed = c.deviceProcAddr(device, "vkCmdDrawIndexed")
	c.cmdDrawIndirect = c.deviceProcAddr(device, "vkCmdDrawIndirect")
	c.cmdDrawIndexedIndirect = c.deviceProcAddr(device, "vkCmdDrawIndexedIndirect")
	c.cmdDispatch = c.deviceProcAddr(device, "vkCmdDispatch")
	c.cmdDispatchIndirect = c.deviceProcAddr(device, "vkCmdDispatchIndirect")
	c.cmdCopyBuffer = c.deviceProcAddr(device, "vkCmdCopyBuffer")
	c.cmdCopyImage = c.deviceProcAddr(device, "vkCmdCopyImage")
	c.cmdBlitImage = c.deviceProcAddr(device, "vkCmdBlitImage")
	c.cmdCopyBufferToImage = c.deviceProcAddr(device, "vkCmdCopyBufferToImage")
	c.cmdCopyImageToBuffer = c.deviceProcAddr(device, "vkCmdCopyImageToBuffer")
	c.cmdUpdateBuffer = c.deviceProcAddr(device, "vkCmdUpdateBuffer")
	c.cmdFillBuffer = c.deviceProcAddr(device, "vkCmdFillBuffer")
	c.cmdClearColorImage = c.deviceProcAddr(device, "vkCmdClearColorImage")
	c.cmdClearDepthStencilImage = c.deviceProcAddr(device, "vkCmdClearDepthStencilImage")
	c.cmdClearAttachments = c.deviceProcAddr(device, "vkCmdClearAttachments")
	c.cmdResolveImage = c.deviceProcAddr(device, "vkCmdResolveImage")
	c.cmdSetEvent = c.deviceProcAddr(device, "vkCmdSetEvent")
	c.cmdResetEvent = c.deviceProcAddr(device, "vkCmdResetEvent")
	c.cmdWaitEvents = c.deviceProcAddr(device, "vkCmdWaitEvents")
	c.cmdPipelineBarrier = c.deviceProcAddr(device, "vkCmdPipelineBarrier")
	c.cmdBeginQuery = c.deviceProcAddr(device, "vkCmdBeginQuery")
	c.cmdEndQuery = c.deviceProcAddr(device, "vkCmdEndQuery")
	c.cmdResetQueryPool = c.deviceProcAddr(device, "vkCmdResetQueryPool")
	c.cmdWriteTimestamp = c.deviceProcAddr(device, "vkCmdWriteTimestamp")
	c.cmdCopyQueryPoolResults = c.deviceProcAddr(device, "vkCmdCopyQueryPoolResults")
	c.cmdPushConstants = c.deviceProcAddr(device, "vkCmdPushConstants")
	c.cmdBeginRenderPass = c.deviceProcAddr(device, "vkCmdBeginRenderPass")
	c.cmdNextSubpass = c.deviceProcAddr(device, "vkCmdNextSubpass")
	c.cmdEndRenderPass = c.deviceProcAddr(device, "vkCmdEndRenderPass")
	c.cmdExecuteCommands = c.deviceProcAddr(device, "vkCmdExecuteCommands")

	// Vulkan 1.2+ timeline semaphore functions
	c.getSemaphoreCounterValue = c.deviceProcAddr(device, "vkGetSemaphoreCounterValue")
	c.waitSemaphores = c.deviceProcAddr(device, "vkWaitSemaphores")
	c.signalSemaphore = c.deviceProcAddr(device, "vkSignalSemaphore")

	// Vulkan 1.3 (VK_KHR_synchronization2), which video coding needs.
	c.cmdPipelineBarrier2 = c.deviceProcAddr(device, "vkCmdPipelineBarrier2")
	c.queueSubmit2 = c.deviceProcAddr(device, "vkQueueSubmit2")

	// VK_KHR_video_queue and VK_KHR_video_encode_queue.
	c.createVideoSessionKHR = c.deviceProcAddr(device, "vkCreateVideoSessionKHR")
	c.destroyVideoSessionKHR = c.deviceProcAddr(device, "vkDestroyVideoSessionKHR")
	c.getVideoSessionMemoryRequirementsKHR = c.deviceProcAddr(device, "vkGetVideoSessionMemoryRequirementsKHR")
	c.bindVideoSessionMemoryKHR = c.deviceProcAddr(device, "vkBindVideoSessionMemoryKHR")
	c.createVideoSessionParametersKHR = c.deviceProcAddr(device, "vkCreateVideoSessionParametersKHR")
	c.destroyVideoSessionParametersKHR = c.deviceProcAddr(device, "vkDestroyVideoSessionParametersKHR")
	c.getEncodedVideoSessionParametersKHR = c.deviceProcAddr(device, "vkGetEncodedVideoSessionParametersKHR")
	c.cmdBeginVideoCodingKHR = c.deviceProcAddr(device, "vkCmdBeginVideoCodingKHR")
	c.cmdEndVideoCodingKHR = c.deviceProcAddr(device, "vkCmdEndVideoCodingKHR")
	c.cmdControlVideoCodingKHR = c.deviceProcAddr(device, "vkCmdControlVideoCodingKHR")
	c.cmdEncodeVideoKHR = c.deviceProcAddr(device, "vkCmdEncodeVideoKHR")

	// Swapchain functions (WSI)
	c.createSwapchainKHR = c.deviceProcAddr(device, "vkCreateSwapchainKHR")
	c.destroySwapchainKHR = c.deviceProcAddr(device, "vkDestroySwapchainKHR")
	c.getSwapchainImagesKHR = c.deviceProcAddr(device, "vkGetSwapchainImagesKHR")
	c.acquireNextImageKHR = c.deviceProcAddr(device, "vkAcquireNextImageKHR")
	c.queuePresentKHR = c.deviceProcAddr(device, "vkQueuePresentKHR")

	// Verify critical functions loaded
	if c.destroyDevice == nil || c.getDeviceQueue == nil || c.queueSubmit == nil {
		return fmt.Errorf("failed to load critical device functions")
	}

	return nil
}

// HasTimelineSemaphore returns true if timeline semaphore functions were loaded.
// These are Vulkan 1.2 core functions and should be available on all conformant drivers.
func (c *Commands) HasTimelineSemaphore() bool {
	return c.getSemaphoreCounterValue != nil &&
		c.waitSemaphores != nil &&
		c.signalSemaphore != nil
}

// HasPhysicalDeviceFeatures2 returns true if vkGetPhysicalDeviceFeatures2 is available.
// This is a Vulkan 1.1 core function used to query extended feature support via PNext chains.
func (c *Commands) HasPhysicalDeviceFeatures2() bool {
	return c.getPhysicalDeviceFeatures2 != nil
}

// HasCreateWin32SurfaceKHR returns true if vkCreateWin32SurfaceKHR is available.
func (c *Commands) HasCreateWin32SurfaceKHR() bool {
	return c.createWin32SurfaceKHR != nil
}

// HasCreateXlibSurfaceKHR returns true if vkCreateXlibSurfaceKHR is available.
func (c *Commands) HasCreateXlibSurfaceKHR() bool {
	return c.createXlibSurfaceKHR != nil
}

// HasCreateWaylandSurfaceKHR returns true if vkCreateWaylandSurfaceKHR is available.
func (c *Commands) HasCreateWaylandSurfaceKHR() bool {
	return c.createWaylandSurfaceKHR != nil
}

// HasCreateAndroidSurfaceKHR returns true if vkCreateAndroidSurfaceKHR is available.
func (c *Commands) HasCreateAndroidSurfaceKHR() bool {
	return c.createAndroidSurfaceKHR != nil
}

// HasWSIQueries reports whether the VK_KHR_surface commands used by the
// backend are available. Platform surface creation is checked separately.
func (c *Commands) HasWSIQueries() bool {
	return c.destroySurfaceKHR != nil &&
		c.getPhysicalDeviceSurfaceSupportKHR != nil &&
		c.getPhysicalDeviceSurfaceCapabilitiesKHR != nil &&
		c.getPhysicalDeviceSurfaceFormatsKHR != nil &&
		c.getPhysicalDeviceSurfacePresentModesKHR != nil
}

// HasCreateMetalSurfaceEXT returns true if vkCreateMetalSurfaceEXT is available.
func (c *Commands) HasCreateMetalSurfaceEXT() bool {
	return c.createMetalSurfaceEXT != nil
}

// HasDebugUtils returns true if vkSetDebugUtilsObjectNameEXT is available.
// When true, Vulkan objects can be labeled with human-readable names for
// validation layer messages and GPU debugging tools (RenderDoc, NSight).
func (c *Commands) HasDebugUtils() bool {
	return c.setDebugUtilsObjectNameEXT != nil
}

// DebugFunctionPointer returns the address of the specified Vulkan function.
// This is only for debugging purposes.
func (c *Commands) DebugFunctionPointer(name string) unsafe.Pointer {
	switch name {
	case "vkCreateGraphicsPipelines":
		return c.createGraphicsPipelines
	case "vkCreateComputePipelines":
		return c.createComputePipelines
	case "vkCreateRenderPass":
		return c.createRenderPass
	default:
		return nil
	}
}
