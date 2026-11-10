package gpu

import (
	"fmt"

	"github.com/stubbedev/lys-lib/third_party/vk"
)

// commandPool makes a pool on family whose buffers reset one by one.
func (d *Device) commandPool(family uint32) (vk.CommandPool, error) {
	pci := vk.CommandPoolCreateInfo{
		SType: vk.StructureTypeCommandPoolCreateInfo, Flags: vk.CommandPoolCreateFlags(vk.CommandPoolCreateResetCommandBufferBit), QueueFamilyIndex: family,
	}
	var p vk.CommandPool
	if r := d.cmds.CreateCommandPool(d.device, &pci, nil, &p); r != vk.Success {
		return 0, fmt.Errorf("gpu: vkCreateCommandPool on family %d: %w", family, resultError(r))
	}
	return p, nil
}

// commandBuffer allocates a primary command buffer from pool.
func (d *Device) commandBuffer(pool vk.CommandPool) (vk.CommandBuffer, error) {
	a := &d.args
	a.allocCB = vk.CommandBufferAllocateInfo{SType: vk.StructureTypeCommandBufferAllocateInfo, CommandPool: pool, Level: vk.CommandBufferLevelPrimary, CommandBufferCount: 1}
	if r := d.cmds.AllocateCommandBuffers(d.device, &a.allocCB, &a.cb); r != vk.Success {
		return 0, fmt.Errorf("gpu: vkAllocateCommandBuffers: %w", resultError(r))
	}
	return a.cb, nil
}

// fence makes an unsignalled fence.
func (d *Device) fence() (vk.Fence, error) {
	a := &d.args
	a.createFence = vk.FenceCreateInfo{SType: vk.StructureTypeFenceCreateInfo}
	if r := d.cmds.CreateFence(d.device, &a.createFence, nil, &a.fence); r != vk.Success {
		return 0, fmt.Errorf("gpu: vkCreateFence: %w", resultError(r))
	}
	return a.fence, nil
}

func resetFence(d *Device, f vk.Fence) error {
	d.args.fence = f
	if r := d.cmds.ResetFences(d.device, 1, &d.args.fence); r != vk.Success {
		return fmt.Errorf("gpu: vkResetFences: %w", resultError(r))
	}
	return nil
}

// begin starts recording cb, once, over whatever it held.
func begin(d *Device, cb vk.CommandBuffer) error {
	d.args.begin = vk.CommandBufferBeginInfo{SType: vk.StructureTypeCommandBufferBeginInfo, Flags: vk.CommandBufferUsageFlags(vk.CommandBufferUsageOneTimeSubmitBit)}
	if r := d.cmds.BeginCommandBuffer(cb, &d.args.begin); r != vk.Success {
		return fmt.Errorf("gpu: vkBeginCommandBuffer: %w", resultError(r))
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
// What a Vulkan command points at escapes to the heap, so it copies
// them into the device's arguments: the caller's stay on its stack, and
// a barrier allocates nothing.
func barrier2(d *Device, cb vk.CommandBuffer, mem *vk.MemoryBarrier2, images ...vk.ImageMemoryBarrier2) {
	a := &d.args
	a.dependency = vk.DependencyInfo{SType: vk.StructureTypeDependencyInfo}
	if mem != nil {
		a.memory2 = *mem
		a.dependency.MemoryBarrierCount, a.dependency.PMemoryBarriers = 1, &a.memory2
	}
	if len(images) > 0 {
		a.images2 = append(a.images2[:0], images...)
		a.dependency.ImageMemoryBarrierCount, a.dependency.PImageMemoryBarriers = count(a.images2), &a.images2[0]
	}
	d.cmds.CmdPipelineBarrier2(cb, &a.dependency)
}

// submit2 submits cb to queue after wait and before signal (each nil
// for none), signalling done (0 for none) when it finishes. Like
// barrier2 it copies what it is handed into the device's arguments.
func submit2(d *Device, queue vk.Queue, cb vk.CommandBuffer, wait, signal *vk.SemaphoreSubmitInfo, done vk.Fence) error {
	a := &d.args
	a.buffer2 = vk.CommandBufferSubmitInfo{SType: vk.StructureTypeCommandBufferSubmitInfo, CommandBuffer: cb}
	a.submit2 = vk.SubmitInfo2{SType: vk.StructureTypeSubmitInfo2, CommandBufferInfoCount: 1, PCommandBufferInfos: &a.buffer2}
	if wait != nil {
		a.wait2 = *wait
		a.submit2.WaitSemaphoreInfoCount, a.submit2.PWaitSemaphoreInfos = 1, &a.wait2
	}
	if signal != nil {
		a.signal2 = *signal
		a.submit2.SignalSemaphoreInfoCount, a.submit2.PSignalSemaphoreInfos = 1, &a.signal2
	}
	if err := d.checkLost(); err != nil {
		return err
	}
	r := d.takeFault()
	if r == vk.Success {
		r = d.cmds.QueueSubmit2(queue, 1, &a.submit2, done)
	}
	return d.submitResult("vkQueueSubmit2", r)
}
