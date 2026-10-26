package gpu

import (
	"fmt"

	"github.com/stubbedev/lys-ui/third_party/vk"
)

// commandPool makes a pool on family whose buffers reset one by one.
func (d *Device) commandPool(family uint32) (vk.CommandPool, error) {
	pci := vk.CommandPoolCreateInfo{
		SType: vk.StructureTypeCommandPoolCreateInfo, Flags: vk.CommandPoolCreateFlags(vk.CommandPoolCreateResetCommandBufferBit), QueueFamilyIndex: family,
	}
	var p vk.CommandPool
	if r := d.cmds.CreateCommandPool(d.device, &pci, nil, &p); r != vk.Success {
		return 0, fmt.Errorf("gpu: vkCreateCommandPool on family %d: %d", family, r)
	}
	return p, nil
}

// commandBuffer allocates a primary command buffer from pool.
func (d *Device) commandBuffer(pool vk.CommandPool) (vk.CommandBuffer, error) {
	cai := vk.CommandBufferAllocateInfo{SType: vk.StructureTypeCommandBufferAllocateInfo, CommandPool: pool, Level: vk.CommandBufferLevelPrimary, CommandBufferCount: 1}
	var cb vk.CommandBuffer
	if r := d.cmds.AllocateCommandBuffers(d.device, &cai, &cb); r != vk.Success {
		return 0, fmt.Errorf("gpu: vkAllocateCommandBuffers: %d", r)
	}
	return cb, nil
}

// fence makes an unsignalled fence.
func (d *Device) fence() (vk.Fence, error) {
	fci := vk.FenceCreateInfo{SType: vk.StructureTypeFenceCreateInfo}
	var f vk.Fence
	if r := d.cmds.CreateFence(d.device, &fci, nil, &f); r != vk.Success {
		return 0, fmt.Errorf("gpu: vkCreateFence: %d", r)
	}
	return f, nil
}

func resetFence(d *Device, f vk.Fence) error {
	if r := d.cmds.ResetFences(d.device, 1, &f); r != vk.Success {
		return fmt.Errorf("gpu: vkResetFences: %d", r)
	}
	return nil
}

// begin starts recording cb, once, over whatever it held.
func begin(d *Device, cb vk.CommandBuffer) error {
	cbi := vk.CommandBufferBeginInfo{SType: vk.StructureTypeCommandBufferBeginInfo, Flags: vk.CommandBufferUsageFlags(vk.CommandBufferUsageOneTimeSubmitBit)}
	if r := d.cmds.BeginCommandBuffer(cb, &cbi); r != vk.Success {
		return fmt.Errorf("gpu: vkBeginCommandBuffer: %d", r)
	}
	return nil
}

// imageBarrier is a synchronization2 barrier moving range of img from
// one layout to another, its stages and accesses none: the caller sets
// those it orders.
func imageBarrier(img vk.Image, rng vk.ImageSubresourceRange, from, to vk.ImageLayout) vk.ImageMemoryBarrier2 {
	return vk.ImageMemoryBarrier2{
		SType: vk.StructureTypeImageMemoryBarrier2, OldLayout: from, NewLayout: to,
		SrcQueueFamilyIndex: vk.QueueFamilyIgnored, DstQueueFamilyIndex: vk.QueueFamilyIgnored, Image: img, SubresourceRange: rng,
	}
}

// barrier2 records a memory barrier (nil for none) and image barriers.
func barrier2(d *Device, cb vk.CommandBuffer, mem *vk.MemoryBarrier2, images ...vk.ImageMemoryBarrier2) {
	di := vk.DependencyInfo{SType: vk.StructureTypeDependencyInfo}
	if mem != nil {
		di.MemoryBarrierCount, di.PMemoryBarriers = 1, mem
	}
	if len(images) > 0 {
		di.ImageMemoryBarrierCount, di.PImageMemoryBarriers = uint32(len(images)), &images[0]
	}
	d.cmds.CmdPipelineBarrier2(cb, &di)
}

// submit2 submits cb to queue after wait and before signal (each nil
// for none), signalling done when it finishes.
func submit2(d *Device, queue vk.Queue, cb vk.CommandBuffer, wait, signal *vk.SemaphoreSubmitInfo, done vk.Fence) error {
	cbi := vk.CommandBufferSubmitInfo{SType: vk.StructureTypeCommandBufferSubmitInfo, CommandBuffer: cb}
	si := vk.SubmitInfo2{SType: vk.StructureTypeSubmitInfo2, CommandBufferInfoCount: 1, PCommandBufferInfos: &cbi}
	if wait != nil {
		si.WaitSemaphoreInfoCount, si.PWaitSemaphoreInfos = 1, wait
	}
	if signal != nil {
		si.SignalSemaphoreInfoCount, si.PSignalSemaphoreInfos = 1, signal
	}
	if r := d.cmds.QueueSubmit2(queue, 1, &si, done); r != vk.Success {
		return fmt.Errorf("gpu: vkQueueSubmit2: %d", r)
	}
	return nil
}
