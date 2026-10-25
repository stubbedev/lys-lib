package gpu

import (
	"errors"
	"fmt"
	"image"
	"runtime"
	"syscall"
	"unsafe"

	"github.com/stubbedev/lys-ui/dmabuf"
	"github.com/stubbedev/lys-ui/third_party/vk"
)

// Target is an image the GPU renders into whose memory is a dmabuf: a
// window's frame handed to the compositor, or an output's scanout
// buffer. Its layout (the DRM modifier) is one the caller offered, as
// the consumer imports it.
type Target struct {
	d      *Device
	image  vk.Image
	memory vk.DeviceMemory
	format vk.Format
	buf    dmabuf.Buffer
	// layout is the image's current layout, which a recording picks up
	// and leaves the image in.
	layout vk.ImageLayout
}

// vkFormatOf maps a DRM fourcc to the Vulkan format with the same byte
// order in memory.
func vkFormatOf(fourcc uint32) (vk.Format, error) {
	switch fourcc {
	case dmabuf.FormatARGB8888, dmabuf.FormatXRGB8888:
		return vk.FormatB8g8r8a8Unorm, nil
	}
	return 0, fmt.Errorf("gpu: no Vulkan format for DRM format %#08x", fourcc)
}

// NewTarget allocates a width x height image in format (a DRM fourcc,
// dmabuf.FormatARGB8888) whose layout the driver picks among
// modifiers, the ones the consumer imports.
func (d *Device) NewTarget(width, height int, format uint32, modifiers []uint64) (*Target, error) {
	if width <= 0 || height <= 0 {
		return nil, fmt.Errorf("gpu: target size %dx%d is not positive", width, height)
	}
	if len(modifiers) == 0 {
		return nil, errors.New("gpu: no modifier to allocate a target in")
	}
	vf, err := vkFormatOf(format)
	if err != nil {
		return nil, err
	}
	t := &Target{d: d, format: vf, layout: vk.ImageLayoutUndefined}
	if err := t.create(width, height, modifiers); err != nil {
		t.Close()
		return nil, err
	}
	t.buf.Format = format
	return t, nil
}

func (t *Target) create(width, height int, modifiers []uint64) error {
	d := t.d
	modList := vk.ImageDrmFormatModifierListCreateInfoEXT{
		SType: vk.StructureTypeImageDrmFormatModifierListCreateInfoExt, DrmFormatModifierCount: uint32(len(modifiers)), PDrmFormatModifiers: &modifiers[0],
	}
	ext := vk.ExternalMemoryImageCreateInfo{
		SType: vk.StructureTypeExternalMemoryImageCreateInfo, PNext: (*uintptr)(unsafe.Pointer(&modList)),
		HandleTypes: vk.ExternalMemoryHandleTypeFlags(vk.ExternalMemoryHandleTypeDmaBufBitExt),
	}
	ici := vk.ImageCreateInfo{
		SType: vk.StructureTypeImageCreateInfo, PNext: (*uintptr)(unsafe.Pointer(&ext)),
		ImageType: vk.ImageType2d, Format: t.format,
		Extent: vk.Extent3D{Width: uint32(width), Height: uint32(height), Depth: 1}, MipLevels: 1, ArrayLayers: 1,
		Samples: vk.SampleCount1Bit, Tiling: vk.ImageTilingDrmFormatModifierExt,
		Usage:       vk.ImageUsageFlags(vk.ImageUsageTransferDstBit | vk.ImageUsageTransferSrcBit | vk.ImageUsageColorAttachmentBit | vk.ImageUsageSampledBit),
		SharingMode: vk.SharingModeExclusive, InitialLayout: vk.ImageLayoutUndefined,
	}
	r := d.cmds.CreateImage(d.device, &ici, nil, &t.image)
	runtime.KeepAlive(&modList)
	runtime.KeepAlive(&ext)
	runtime.KeepAlive(modifiers)
	if r != vk.Success {
		return fmt.Errorf("gpu: vkCreateImage %dx%d in %d modifiers: %d", width, height, len(modifiers), r)
	}
	var req vk.MemoryRequirements
	d.cmds.GetImageMemoryRequirements(d.device, t.image, &req)
	mt, err := d.memoryType(req.MemoryTypeBits, vk.MemoryPropertyFlags(vk.MemoryPropertyDeviceLocalBit))
	if err != nil {
		return err
	}
	ded := vk.MemoryDedicatedAllocateInfo{SType: vk.StructureTypeMemoryDedicatedAllocateInfo, Image: t.image}
	exp := vk.ExportMemoryAllocateInfo{
		SType: vk.StructureTypeExportMemoryAllocateInfo, PNext: (*uintptr)(unsafe.Pointer(&ded)),
		HandleTypes: vk.ExternalMemoryHandleTypeFlags(vk.ExternalMemoryHandleTypeDmaBufBitExt),
	}
	mai := vk.MemoryAllocateInfo{SType: vk.StructureTypeMemoryAllocateInfo, PNext: (*uintptr)(unsafe.Pointer(&exp)), AllocationSize: req.Size, MemoryTypeIndex: mt}
	r = d.cmds.AllocateMemory(d.device, &mai, nil, &t.memory)
	runtime.KeepAlive(&ded)
	runtime.KeepAlive(&exp)
	if r != vk.Success {
		return fmt.Errorf("gpu: vkAllocateMemory %d bytes: %d", req.Size, r)
	}
	if r := d.cmds.BindImageMemory(d.device, t.image, t.memory, 0); r != vk.Success {
		return fmt.Errorf("gpu: vkBindImageMemory: %d", r)
	}
	gfi := vk.MemoryGetFdInfoKHR{SType: vk.StructureTypeMemoryGetFdInfoKhr, Memory: t.memory, HandleType: vk.ExternalMemoryHandleTypeDmaBufBitExt}
	var fd int32
	if r := d.cmds.GetMemoryFdKHR(d.device, &gfi, &fd); r != vk.Success {
		return fmt.Errorf("gpu: vkGetMemoryFdKHR: %d", r)
	}
	mod := vk.ImageDrmFormatModifierPropertiesEXT{SType: vk.StructureTypeImageDrmFormatModifierPropertiesExt}
	if r := d.cmds.GetImageDrmFormatModifierPropertiesEXT(d.device, t.image, &mod); r != vk.Success {
		_ = syscall.Close(int(fd))
		return fmt.Errorf("gpu: vkGetImageDrmFormatModifierPropertiesEXT: %d", r)
	}
	sub := vk.ImageSubresource{AspectMask: vk.ImageAspectFlags(vk.ImageAspectMemoryPlane0BitExt)}
	var lay vk.SubresourceLayout
	d.cmds.GetImageSubresourceLayout(d.device, t.image, &sub, &lay)
	t.buf = dmabuf.Buffer{
		Width: width, Height: height, Modifier: mod.DrmFormatModifier,
		Planes: []dmabuf.Plane{{FD: int(fd), Offset: uint32(lay.Offset), Stride: uint32(lay.RowPitch)}},
	}
	return nil
}

// Dmabuf describes the target's memory for its consumer. Its fd is the
// target's, open until Close.
func (t *Target) Dmabuf() dmabuf.Buffer { return t.buf }

// Size is the target's size in pixels.
func (t *Target) Size() image.Point { return image.Pt(t.buf.Width, t.buf.Height) }

// Close releases the target and its dmabuf fd.
func (t *Target) Close() {
	d := t.d
	for _, p := range t.buf.Planes {
		_ = syscall.Close(p.FD)
	}
	t.buf.Planes = nil
	if t.image != 0 {
		d.cmds.DestroyImage(d.device, t.image, nil)
		t.image = 0
	}
	if t.memory != 0 {
		d.cmds.FreeMemory(d.device, t.memory, nil)
		t.memory = 0
	}
}

// colorRange is a target's whole color subresource.
var colorRange = vk.ImageSubresourceRange{AspectMask: vk.ImageAspectFlags(vk.ImageAspectColorBit), LevelCount: 1, LayerCount: 1}

// transition records a barrier moving t to layout, after src work
// (stage and access) and before dst work.
func (t *Target) transition(cb vk.CommandBuffer, layout vk.ImageLayout, srcStage, dstStage vk.PipelineStageFlagBits, srcAccess, dstAccess vk.AccessFlagBits) {
	b := vk.ImageMemoryBarrier{
		SType: vk.StructureTypeImageMemoryBarrier, SrcAccessMask: vk.AccessFlags(srcAccess), DstAccessMask: vk.AccessFlags(dstAccess),
		OldLayout: t.layout, NewLayout: layout,
		SrcQueueFamilyIndex: vk.QueueFamilyIgnored, DstQueueFamilyIndex: vk.QueueFamilyIgnored, Image: t.image, SubresourceRange: colorRange,
	}
	t.d.cmds.CmdPipelineBarrier(cb, vk.PipelineStageFlags(srcStage), vk.PipelineStageFlags(dstStage), 0, 0, nil, 0, nil, 1, &b)
	t.layout = layout
}

// release records handing t to its consumer outside this device: a
// queue-family release to the foreign family, its writes made visible.
func (t *Target) release(cb vk.CommandBuffer, srcStage vk.PipelineStageFlagBits, srcAccess vk.AccessFlagBits) {
	b := vk.ImageMemoryBarrier{
		SType: vk.StructureTypeImageMemoryBarrier, SrcAccessMask: vk.AccessFlags(srcAccess),
		OldLayout: t.layout, NewLayout: vk.ImageLayoutGeneral,
		SrcQueueFamilyIndex: t.d.family, DstQueueFamilyIndex: vk.QueueFamilyForeignExt, Image: t.image, SubresourceRange: colorRange,
	}
	t.d.cmds.CmdPipelineBarrier(cb, vk.PipelineStageFlags(srcStage), vk.PipelineStageFlags(vk.PipelineStageBottomOfPipeBit), 0, 0, nil, 0, nil, 1, &b)
	t.layout = vk.ImageLayoutGeneral
}

// clear fills t with the premultiplied color rgba and hands it to its
// consumer.
func (t *Target) clear(rgba [4]float32) error {
	return t.d.submit(func(cb vk.CommandBuffer) {
		t.transition(cb, vk.ImageLayoutTransferDstOptimal, vk.PipelineStageTopOfPipeBit, vk.PipelineStageTransferBit, 0, vk.AccessTransferWriteBit)
		var col vk.ClearColorValue
		copy(col[:], unsafe.Slice((*byte)(unsafe.Pointer(&rgba[0])), len(col)))
		t.d.cmds.CmdClearColorImage(cb, t.image, vk.ImageLayoutTransferDstOptimal, &col, 1, &colorRange)
		t.release(cb, vk.PipelineStageTransferBit, vk.AccessTransferWriteBit)
	})
}

// ReadPixels copies the target back into memory as premultiplied
// RGBA: tests and screenshots.
func (t *Target) ReadPixels() (*image.RGBA, error) {
	d := t.d
	w, h := t.buf.Width, t.buf.Height
	size := vk.DeviceSize(w * h * 4)
	bci := vk.BufferCreateInfo{SType: vk.StructureTypeBufferCreateInfo, Size: size, Usage: vk.BufferUsageFlags(vk.BufferUsageTransferDstBit), SharingMode: vk.SharingModeExclusive}
	var buf vk.Buffer
	if r := d.cmds.CreateBuffer(d.device, &bci, nil, &buf); r != vk.Success {
		return nil, fmt.Errorf("gpu: vkCreateBuffer %d bytes: %d", size, r)
	}
	defer d.cmds.DestroyBuffer(d.device, buf, nil)
	var req vk.MemoryRequirements
	d.cmds.GetBufferMemoryRequirements(d.device, buf, &req)
	mt, err := d.memoryType(req.MemoryTypeBits, vk.MemoryPropertyFlags(vk.MemoryPropertyHostVisibleBit|vk.MemoryPropertyHostCoherentBit))
	if err != nil {
		return nil, err
	}
	mai := vk.MemoryAllocateInfo{SType: vk.StructureTypeMemoryAllocateInfo, AllocationSize: req.Size, MemoryTypeIndex: mt}
	var mem vk.DeviceMemory
	if r := d.cmds.AllocateMemory(d.device, &mai, nil, &mem); r != vk.Success {
		return nil, fmt.Errorf("gpu: vkAllocateMemory %d bytes: %d", req.Size, r)
	}
	defer d.cmds.FreeMemory(d.device, mem, nil)
	if r := d.cmds.BindBufferMemory(d.device, buf, mem, 0); r != vk.Success {
		return nil, fmt.Errorf("gpu: vkBindBufferMemory: %d", r)
	}
	err = d.submit(func(cb vk.CommandBuffer) {
		t.transition(cb, vk.ImageLayoutTransferSrcOptimal, vk.PipelineStageBottomOfPipeBit, vk.PipelineStageTransferBit, 0, vk.AccessTransferReadBit)
		region := vk.BufferImageCopy{
			ImageSubresource: vk.ImageSubresourceLayers{AspectMask: vk.ImageAspectFlags(vk.ImageAspectColorBit), LayerCount: 1},
			ImageExtent:      vk.Extent3D{Width: uint32(w), Height: uint32(h), Depth: 1},
		}
		d.cmds.CmdCopyImageToBuffer(cb, t.image, vk.ImageLayoutTransferSrcOptimal, buf, 1, &region)
		t.release(cb, vk.PipelineStageTransferBit, vk.AccessTransferReadBit)
	})
	if err != nil {
		return nil, err
	}
	var ptr unsafe.Pointer
	if r := d.cmds.MapMemory(d.device, mem, 0, size, 0, uintptr(unsafe.Pointer(&ptr))); r != vk.Success {
		return nil, fmt.Errorf("gpu: vkMapMemory: %d", r)
	}
	defer d.cmds.UnmapMemory(d.device, mem)
	src := unsafe.Slice((*byte)(ptr), int(size))
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < len(src); i += 4 {
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = src[i+2], src[i+1], src[i], src[i+3]
	}
	return img, nil
}

// Modifiers are the DRM modifiers the device renders format into as a
// single-plane color attachment: what to intersect with the ones a
// consumer imports before NewTarget.
func (d *Device) Modifiers(format uint32) ([]uint64, error) {
	vf, err := vkFormatOf(format)
	if err != nil {
		return nil, err
	}
	list := vk.DrmFormatModifierPropertiesListEXT{SType: vk.StructureTypeDrmFormatModifierPropertiesListExt}
	props := vk.FormatProperties2{SType: vk.StructureTypeFormatProperties2, PNext: (*uintptr)(unsafe.Pointer(&list))}
	d.cmds.GetPhysicalDeviceFormatProperties2(d.phys, vf, &props)
	all := make([]vk.DrmFormatModifierPropertiesEXT, list.DrmFormatModifierCount)
	if len(all) > 0 {
		list.PDrmFormatModifierProperties = &all[0]
		d.cmds.GetPhysicalDeviceFormatProperties2(d.phys, vf, &props)
	}
	runtime.KeepAlive(&list)
	want := vk.FormatFeatureFlags(vk.FormatFeatureColorAttachmentBit | vk.FormatFeatureColorAttachmentBlendBit)
	var out []uint64
	for _, m := range all[:list.DrmFormatModifierCount] {
		if m.DrmFormatModifierPlaneCount == 1 && m.DrmFormatModifierTilingFeatures&want == want {
			out = append(out, m.DrmFormatModifier)
		}
	}
	return out, nil
}
