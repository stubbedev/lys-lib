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
// format in for use and exports as a dmabuf. Modifiers with an
// auxiliary plane (Intel CCS, AMD DCC) are left out: an exported image
// is one plane.
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
		if m.DrmFormatModifierPlaneCount != 1 || m.DrmFormatModifierTilingFeatures&use.features != use.features {
			continue
		}
		if _, ok := d.exportable(vf, m.DrmFormatModifier, use.usage); ok {
			out = append(out, m.DrmFormatModifier)
		}
	}
	return out, nil
}

// exportable reports whether an image of vf in modifier mod, made for
// usage, exports its memory as a dmabuf, and the largest such image:
// what a modifier's format features alone do not promise.
func (d *Device) exportable(vf vk.Format, mod uint64, usage vk.ImageUsageFlags) (vk.Extent3D, bool) {
	modInfo := vk.PhysicalDeviceImageDrmFormatModifierInfoEXT{
		SType: vk.StructureTypePhysicalDeviceImageDrmFormatModifierInfoExt, DrmFormatModifier: mod, SharingMode: vk.SharingModeExclusive,
	}
	ext := vk.PhysicalDeviceExternalImageFormatInfo{
		SType: vk.StructureTypePhysicalDeviceExternalImageFormatInfo, PNext: (*uintptr)(unsafe.Pointer(&modInfo)),
		HandleType: vk.ExternalMemoryHandleTypeDmaBufBitExt,
	}
	info := vk.PhysicalDeviceImageFormatInfo2{
		SType: vk.StructureTypePhysicalDeviceImageFormatInfo2, PNext: (*uintptr)(unsafe.Pointer(&ext)),
		Format: vf, Type: vk.ImageType2d, Tiling: vk.ImageTilingDrmFormatModifierExt, Usage: usage,
	}
	extProps := vk.ExternalImageFormatProperties{SType: vk.StructureTypeExternalImageFormatProperties}
	props := vk.ImageFormatProperties2{SType: vk.StructureTypeImageFormatProperties2, PNext: (*uintptr)(unsafe.Pointer(&extProps))}
	r := d.cmds.GetPhysicalDeviceImageFormatProperties2(d.phys, &info, &props)
	runtime.KeepAlive(&modInfo)
	runtime.KeepAlive(&ext)
	runtime.KeepAlive(&extProps)
	exportable := extProps.ExternalMemoryProperties.ExternalMemoryFeatures&vk.ExternalMemoryFeatureFlags(vk.ExternalMemoryFeatureExportableBit) != 0
	return props.ImageFormatProperties.MaxExtent, r == vk.Success && exportable
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
	mods := slices.DeleteFunc(slices.Clone(offered), func(m uint64) bool {
		if !slices.Contains(supported, m) {
			return true
		}
		limit, _ := d.exportable(vf, m, use.usage)
		return width > int(limit.Width) || height > int(limit.Height)
	})
	if len(mods) == 0 {
		return exported{}, fmt.Errorf("gpu: none of the modifiers %#x is one %s %s DRM format %#08x in at %dx%d (%#x)", offered, d.name, use.verb, format, width, height, supported)
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
	ded := vk.MemoryDedicatedAllocateInfo{SType: vk.StructureTypeMemoryDedicatedAllocateInfo, Image: e.image}
	exp := vk.ExportMemoryAllocateInfo{
		SType: vk.StructureTypeExportMemoryAllocateInfo, PNext: (*uintptr)(unsafe.Pointer(&ded)),
		HandleTypes: vk.ExternalMemoryHandleTypeFlags(vk.ExternalMemoryHandleTypeDmaBufBitExt),
	}
	mem, err := d.allocate(req, vk.MemoryPropertyDeviceLocalBit, unsafe.Pointer(&exp))
	runtime.KeepAlive(&ded)
	runtime.KeepAlive(&exp)
	if err != nil {
		return err
	}
	e.memory = mem
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
	d *Device
	e exported
	// sampled is the view the encoder reads the buffer through, made
	// on its first frame.
	sampled vk.ImageView
	closed  bool
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

// view is the buffer's sampled view.
func (b *Buffer) view() (vk.ImageView, error) {
	if b.sampled != 0 {
		return b.sampled, nil
	}
	vf, err := vkFormatOf(b.e.buf.Format)
	if err != nil {
		return 0, err
	}
	vci := vk.ImageViewCreateInfo{SType: vk.StructureTypeImageViewCreateInfo, Image: b.e.image, ViewType: vk.ImageViewType2d, Format: vf, SubresourceRange: colorRange}
	if r := b.d.cmds.CreateImageView(b.d.device, &vci, nil, &b.sampled); r != vk.Success {
		return 0, fmt.Errorf("gpu: vkCreateImageView (buffer): %d", r)
	}
	return b.sampled, nil
}

// Close releases the buffer and its fd; closing it again, or after its
// device, does nothing.
func (b *Buffer) Close() {
	if b.closed {
		return
	}
	b.closed = true
	delete(b.d.buffers, b)
	if b.sampled != 0 {
		b.d.cmds.DestroyImageView(b.d.device, b.sampled, nil)
		b.sampled = 0
	}
	b.e.close(b.d)
}
