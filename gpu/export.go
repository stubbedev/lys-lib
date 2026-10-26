package gpu

import (
	"errors"
	"fmt"
	"runtime"
	"slices"
	"syscall"
	"unsafe"

	"github.com/stubbedev/lys-ui/dmabuf"
	"github.com/stubbedev/lys-ui/third_party/vk"
)

// exportUse is what an image exported as a dmabuf is made for: the
// format features its modifier must offer, the usage it is created
// with, and the verb an error names it by.
type exportUse struct {
	features vk.FormatFeatureFlags
	usage    vk.ImageUsageFlags
	verb     string
}

var (
	// renderUse is a Target: drawn into, copied and sampled.
	renderUse = exportUse{
		features: vk.FormatFeatureFlags(vk.FormatFeatureColorAttachmentBit | vk.FormatFeatureColorAttachmentBlendBit),
		usage:    vk.ImageUsageFlags(vk.ImageUsageTransferDstBit | vk.ImageUsageTransferSrcBit | vk.ImageUsageColorAttachmentBit | vk.ImageUsageSampledBit),
		verb:     "renders",
	}
	// shareUse is a Buffer: written by a transfer and read, never
	// drawn into here.
	shareUse = exportUse{
		features: vk.FormatFeatureFlags(vk.FormatFeatureTransferDstBit | vk.FormatFeatureSampledImageBit),
		usage:    vk.ImageUsageFlags(vk.ImageUsageTransferDstBit | vk.ImageUsageSampledBit),
		verb:     "exports",
	}
)

// modifiers are the single-plane DRM modifiers the device offers
// format in for use.
func (d *Device) modifiers(format uint32, use exportUse) ([]uint64, error) {
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
	var out []uint64
	for _, m := range all[:list.DrmFormatModifierCount] {
		if m.DrmFormatModifierPlaneCount == 1 && m.DrmFormatModifierTilingFeatures&use.features == use.features {
			out = append(out, m.DrmFormatModifier)
		}
	}
	return out, nil
}

// exported is an image whose memory is a dmabuf, and that dmabuf.
type exported struct {
	image  vk.Image
	memory vk.DeviceMemory
	buf    dmabuf.Buffer
}

// export allocates a width x height image in format for use whose
// layout the driver picks among the offered modifiers the device has
// for use; none in common is an error naming both lists.
func (d *Device) export(width, height int, format uint32, offered []uint64, use exportUse) (exported, error) {
	if width <= 0 || height <= 0 {
		return exported{}, fmt.Errorf("gpu: image size %dx%d is not positive", width, height)
	}
	if !d.exports {
		return exported{}, ErrNoDmabuf
	}
	if len(offered) == 0 {
		return exported{}, errors.New("gpu: no modifier to allocate an image in")
	}
	vf, err := vkFormatOf(format)
	if err != nil {
		return exported{}, err
	}
	supported, err := d.modifiers(format, use)
	if err != nil {
		return exported{}, err
	}
	mods := slices.DeleteFunc(slices.Clone(offered), func(m uint64) bool { return !slices.Contains(supported, m) })
	if len(mods) == 0 {
		return exported{}, fmt.Errorf("gpu: none of the modifiers %#x is one %s %s DRM format %#08x in (%#x)", offered, d.name, use.verb, format, supported)
	}
	var e exported
	if err := e.create(d, width, height, vf, mods, use.usage); err != nil {
		e.close(d)
		return exported{}, err
	}
	e.buf.Format = format
	return e, nil
}

// create makes the image in one of mods, its exportable memory and the
// dmabuf describing it.
func (e *exported) create(d *Device, width, height int, vf vk.Format, mods []uint64, usage vk.ImageUsageFlags) error {
	modList := vk.ImageDrmFormatModifierListCreateInfoEXT{
		SType: vk.StructureTypeImageDrmFormatModifierListCreateInfoExt, DrmFormatModifierCount: uint32(len(mods)), PDrmFormatModifiers: &mods[0],
	}
	ext := vk.ExternalMemoryImageCreateInfo{
		SType: vk.StructureTypeExternalMemoryImageCreateInfo, PNext: (*uintptr)(unsafe.Pointer(&modList)),
		HandleTypes: vk.ExternalMemoryHandleTypeFlags(vk.ExternalMemoryHandleTypeDmaBufBitExt),
	}
	ici := vk.ImageCreateInfo{
		SType: vk.StructureTypeImageCreateInfo, PNext: (*uintptr)(unsafe.Pointer(&ext)),
		ImageType: vk.ImageType2d, Format: vf,
		Extent: vk.Extent3D{Width: uint32(width), Height: uint32(height), Depth: 1}, MipLevels: 1, ArrayLayers: 1,
		Samples: vk.SampleCount1Bit, Tiling: vk.ImageTilingDrmFormatModifierExt,
		Usage: usage, SharingMode: vk.SharingModeExclusive, InitialLayout: vk.ImageLayoutUndefined,
	}
	r := d.cmds.CreateImage(d.device, &ici, nil, &e.image)
	runtime.KeepAlive(&modList)
	runtime.KeepAlive(&ext)
	runtime.KeepAlive(mods)
	if r != vk.Success {
		return fmt.Errorf("gpu: vkCreateImage %dx%d in %d modifiers: %d", width, height, len(mods), r)
	}
	var req vk.MemoryRequirements
	d.cmds.GetImageMemoryRequirements(d.device, e.image, &req)
	mt, err := d.memoryType(req.MemoryTypeBits, vk.MemoryPropertyFlags(vk.MemoryPropertyDeviceLocalBit))
	if err != nil {
		return err
	}
	ded := vk.MemoryDedicatedAllocateInfo{SType: vk.StructureTypeMemoryDedicatedAllocateInfo, Image: e.image}
	exp := vk.ExportMemoryAllocateInfo{
		SType: vk.StructureTypeExportMemoryAllocateInfo, PNext: (*uintptr)(unsafe.Pointer(&ded)),
		HandleTypes: vk.ExternalMemoryHandleTypeFlags(vk.ExternalMemoryHandleTypeDmaBufBitExt),
	}
	mai := vk.MemoryAllocateInfo{SType: vk.StructureTypeMemoryAllocateInfo, PNext: (*uintptr)(unsafe.Pointer(&exp)), AllocationSize: req.Size, MemoryTypeIndex: mt}
	r = d.cmds.AllocateMemory(d.device, &mai, nil, &e.memory)
	runtime.KeepAlive(&ded)
	runtime.KeepAlive(&exp)
	if r != vk.Success {
		return fmt.Errorf("gpu: vkAllocateMemory %d bytes: %d", req.Size, r)
	}
	if r := d.cmds.BindImageMemory(d.device, e.image, e.memory, 0); r != vk.Success {
		return fmt.Errorf("gpu: vkBindImageMemory: %d", r)
	}
	gfi := vk.MemoryGetFdInfoKHR{SType: vk.StructureTypeMemoryGetFdInfoKhr, Memory: e.memory, HandleType: vk.ExternalMemoryHandleTypeDmaBufBitExt}
	var fd int32
	if r := d.cmds.GetMemoryFdKHR(d.device, &gfi, &fd); r != vk.Success {
		return fmt.Errorf("gpu: vkGetMemoryFdKHR: %d", r)
	}
	mod := vk.ImageDrmFormatModifierPropertiesEXT{SType: vk.StructureTypeImageDrmFormatModifierPropertiesExt}
	if r := d.cmds.GetImageDrmFormatModifierPropertiesEXT(d.device, e.image, &mod); r != vk.Success {
		_ = syscall.Close(int(fd))
		return fmt.Errorf("gpu: vkGetImageDrmFormatModifierPropertiesEXT: %d", r)
	}
	sub := vk.ImageSubresource{AspectMask: vk.ImageAspectFlags(vk.ImageAspectMemoryPlane0BitExt)}
	var lay vk.SubresourceLayout
	d.cmds.GetImageSubresourceLayout(d.device, e.image, &sub, &lay)
	e.buf = dmabuf.Buffer{
		Width: width, Height: height, Modifier: mod.DrmFormatModifier,
		Planes: []dmabuf.Plane{{FD: int(fd), Offset: uint32(lay.Offset), Stride: uint32(lay.RowPitch)}},
	}
	return nil
}

// close releases the image, its memory and the dmabuf's fds.
func (e *exported) close(d *Device) {
	for _, p := range e.buf.Planes {
		_ = syscall.Close(p.FD)
	}
	e.buf.Planes = nil
	if e.image != 0 {
		d.cmds.DestroyImage(d.device, e.image, nil)
		e.image = 0
	}
	if e.memory != 0 {
		d.cmds.FreeMemory(d.device, e.memory, nil)
		e.memory = 0
	}
}

// Buffer is an image whose memory is a dmabuf this device allocates
// for others to write and read, a screencast frame a compositor copies
// into, and never draws into itself: it asks only for transfer and
// sampling, so layouts a device cannot render to (NVIDIA's linear) are
// on offer (ExportModifiers).
type Buffer struct {
	d      *Device
	e      exported
	closed bool
}

// ExportModifiers are the single-plane DRM modifiers the device
// allocates format in for a Buffer: what to intersect with the ones a
// consumer imports before NewBuffer.
func (d *Device) ExportModifiers(format uint32) ([]uint64, error) {
	return d.modifiers(format, shareUse)
}

// NewBuffer allocates a width x height Buffer in format (a DRM fourcc:
// ARGB8888 or XRGB8888) whose layout the driver picks among offered,
// the modifiers the consumer imports, that the device exports format
// in (ExportModifiers). None in common, as with only the implicit
// dmabuf.ModifierInvalid on offer, is an error naming both lists.
func (d *Device) NewBuffer(width, height int, format uint32, offered []uint64) (*Buffer, error) {
	e, err := d.export(width, height, format, offered, shareUse)
	if err != nil {
		return nil, err
	}
	b := &Buffer{d: d, e: e}
	d.buffers[b] = struct{}{}
	return b, nil
}

// Dmabuf describes the buffer's memory. Its fd is the buffer's, open
// until Close.
func (b *Buffer) Dmabuf() dmabuf.Buffer { return b.e.buf }

// Close releases the buffer and its fd; closing it again, or after its
// device, does nothing.
func (b *Buffer) Close() {
	if b.closed {
		return
	}
	b.closed = true
	delete(b.d.buffers, b)
	b.e.close(b.d)
}
