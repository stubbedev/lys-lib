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
// the consumer imports it. Between frames it belongs to the consumer.
type Target struct {
	s      surface
	memory vk.DeviceMemory
	buf    dmabuf.Buffer
	// held is whether the device holds t, between acquire and release.
	held bool
	// offscreen marks a target no consumer imports (NewOffscreen).
	offscreen bool
}

// NewOffscreen allocates a width x height target in device memory no
// other process imports: a frame rendered for its pixels (ReadPixels),
// a screenshot or a test.
func (d *Device) NewOffscreen(width, height int) (*Target, error) {
	if width <= 0 || height <= 0 {
		return nil, fmt.Errorf("gpu: target size %dx%d is not positive", width, height)
	}
	s, err := d.newSurface(width, height)
	if err != nil {
		return nil, err
	}
	return &Target{s: *s, offscreen: true, buf: dmabuf.Buffer{Width: width, Height: height}}, nil
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
	if !d.exports {
		return nil, ErrNoDmabuf
	}
	if len(modifiers) == 0 {
		return nil, errors.New("gpu: no modifier to allocate a target in")
	}
	vf, err := vkFormatOf(format)
	if err != nil {
		return nil, err
	}
	if vf != surfaceFormat {
		return nil, fmt.Errorf("gpu: DRM format %#08x renders as Vulkan format %d, surfaces are %d", format, vf, surfaceFormat)
	}
	t := &Target{s: surface{d: d, w: width, h: height, layout: vk.ImageLayoutUndefined}}
	if err := t.create(width, height, modifiers); err != nil {
		t.Close()
		return nil, err
	}
	t.buf.Format = format
	return t, nil
}

func (t *Target) create(width, height int, modifiers []uint64) error {
	d := t.s.d
	modList := vk.ImageDrmFormatModifierListCreateInfoEXT{
		SType: vk.StructureTypeImageDrmFormatModifierListCreateInfoExt, DrmFormatModifierCount: uint32(len(modifiers)), PDrmFormatModifiers: &modifiers[0],
	}
	ext := vk.ExternalMemoryImageCreateInfo{
		SType: vk.StructureTypeExternalMemoryImageCreateInfo, PNext: (*uintptr)(unsafe.Pointer(&modList)),
		HandleTypes: vk.ExternalMemoryHandleTypeFlags(vk.ExternalMemoryHandleTypeDmaBufBitExt),
	}
	ici := vk.ImageCreateInfo{
		SType: vk.StructureTypeImageCreateInfo, PNext: (*uintptr)(unsafe.Pointer(&ext)),
		ImageType: vk.ImageType2d, Format: surfaceFormat,
		Extent: vk.Extent3D{Width: uint32(width), Height: uint32(height), Depth: 1}, MipLevels: 1, ArrayLayers: 1,
		Samples: vk.SampleCount1Bit, Tiling: vk.ImageTilingDrmFormatModifierExt,
		Usage:       vk.ImageUsageFlags(vk.ImageUsageTransferDstBit | vk.ImageUsageTransferSrcBit | vk.ImageUsageColorAttachmentBit | vk.ImageUsageSampledBit),
		SharingMode: vk.SharingModeExclusive, InitialLayout: vk.ImageLayoutUndefined,
	}
	r := d.cmds.CreateImage(d.device, &ici, nil, &t.s.image)
	runtime.KeepAlive(&modList)
	runtime.KeepAlive(&ext)
	runtime.KeepAlive(modifiers)
	if r != vk.Success {
		return fmt.Errorf("gpu: vkCreateImage %dx%d in %d modifiers: %d", width, height, len(modifiers), r)
	}
	var req vk.MemoryRequirements
	d.cmds.GetImageMemoryRequirements(d.device, t.s.image, &req)
	mt, err := d.memoryType(req.MemoryTypeBits, vk.MemoryPropertyFlags(vk.MemoryPropertyDeviceLocalBit))
	if err != nil {
		return err
	}
	ded := vk.MemoryDedicatedAllocateInfo{SType: vk.StructureTypeMemoryDedicatedAllocateInfo, Image: t.s.image}
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
	if r := d.cmds.BindImageMemory(d.device, t.s.image, t.memory, 0); r != vk.Success {
		return fmt.Errorf("gpu: vkBindImageMemory: %d", r)
	}
	gfi := vk.MemoryGetFdInfoKHR{SType: vk.StructureTypeMemoryGetFdInfoKhr, Memory: t.memory, HandleType: vk.ExternalMemoryHandleTypeDmaBufBitExt}
	var fd int32
	if r := d.cmds.GetMemoryFdKHR(d.device, &gfi, &fd); r != vk.Success {
		return fmt.Errorf("gpu: vkGetMemoryFdKHR: %d", r)
	}
	mod := vk.ImageDrmFormatModifierPropertiesEXT{SType: vk.StructureTypeImageDrmFormatModifierPropertiesExt}
	if r := d.cmds.GetImageDrmFormatModifierPropertiesEXT(d.device, t.s.image, &mod); r != vk.Success {
		_ = syscall.Close(int(fd))
		return fmt.Errorf("gpu: vkGetImageDrmFormatModifierPropertiesEXT: %d", r)
	}
	sub := vk.ImageSubresource{AspectMask: vk.ImageAspectFlags(vk.ImageAspectMemoryPlane0BitExt)}
	var lay vk.SubresourceLayout
	d.cmds.GetImageSubresourceLayout(d.device, t.s.image, &sub, &lay)
	t.buf = dmabuf.Buffer{
		Width: width, Height: height, Modifier: mod.DrmFormatModifier,
		Planes: []dmabuf.Plane{{FD: int(fd), Offset: uint32(lay.Offset), Stride: uint32(lay.RowPitch)}},
	}
	return nil
}

// Dmabuf describes the target's memory for its consumer. Its fd is the
// target's, open until Close; an offscreen target has none.
func (t *Target) Dmabuf() dmabuf.Buffer { return t.buf }

// Size is the target's size in pixels.
func (t *Target) Size() image.Point { return image.Pt(t.buf.Width, t.buf.Height) }

// Close releases the target and its dmabuf fd.
func (t *Target) Close() {
	d := t.s.d
	for _, p := range t.buf.Planes {
		_ = syscall.Close(p.FD)
	}
	t.buf.Planes = nil
	t.s.close()
	if t.s.image != 0 {
		d.cmds.DestroyImage(d.device, t.s.image, nil)
		t.s.image = 0
	}
	if t.memory != 0 {
		d.cmds.FreeMemory(d.device, t.memory, nil)
		t.memory = 0
	}
}

// acquire records taking t back from its consumer for drawing; its
// contents stand, except before its first frame.
func (t *Target) acquire(cb vk.CommandBuffer) {
	if t.held {
		return
	}
	t.held = true
	if t.offscreen || t.s.layout == vk.ImageLayoutUndefined {
		return
	}
	t.s.barrier(cb, vk.ImageLayoutGeneral, vk.QueueFamilyForeignExt, t.s.d.family)
}

// release records handing t to its consumer outside this device: a
// queue-family release to the foreign family, its writes made visible.
func (t *Target) release(cb vk.CommandBuffer) {
	t.held = false
	if t.offscreen {
		return
	}
	t.s.barrier(cb, vk.ImageLayoutGeneral, t.s.d.family, vk.QueueFamilyForeignExt)
}

// clear fills t with the premultiplied color rgba and hands it to its
// consumer.
func (t *Target) clear(rgba [4]float32) error {
	return t.s.d.submit(func(cb vk.CommandBuffer) {
		t.acquire(cb)
		t.s.to(cb, vk.ImageLayoutTransferDstOptimal)
		var col vk.ClearColorValue
		copy(col[:], unsafe.Slice((*byte)(unsafe.Pointer(&rgba[0])), len(col)))
		t.s.d.cmds.CmdClearColorImage(cb, t.s.image, vk.ImageLayoutTransferDstOptimal, &col, 1, &colorRange)
		t.release(cb)
	})
}

// ReadPixels copies the target back into memory as premultiplied
// RGBA: tests and screenshots.
func (t *Target) ReadPixels() (*image.RGBA, error) {
	d := t.s.d
	w, h := t.buf.Width, t.buf.Height
	staging, err := d.newBuffer(w*h*4, vk.BufferUsageTransferDstBit)
	if err != nil {
		return nil, err
	}
	defer staging.close()
	err = d.submit(func(cb vk.CommandBuffer) {
		t.acquire(cb)
		t.s.download(cb, image.Rect(0, 0, w, h), staging, 0)
		t.release(cb)
	})
	if err != nil {
		return nil, err
	}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	src := staging.mapped
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = src[i+2], src[i+1], src[i], src[i+3]
	}
	return img, nil
}

// Modifiers are the DRM modifiers the device renders format into as a
// single-plane color attachment: what to intersect with the ones a
// consumer imports before NewTarget.
func (d *Device) Modifiers(format uint32) ([]uint64, error) {
	if !d.exports {
		return nil, ErrNoDmabuf
	}
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
