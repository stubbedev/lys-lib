package gpu

import (
	"errors"
	"fmt"
	"image"
	"runtime"
	"slices"
	"syscall"
	"unsafe"

	"github.com/stubbedev/lys-lib/dmabuf"
	"github.com/stubbedev/lys-lib/fourcc"
	"github.com/stubbedev/lys-lib/intconv"
	"github.com/stubbedev/lys-lib/third_party/vk"
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
	// shareUse is a Buffer: written by a transfer, sampled and read
	// back, never drawn into here.
	shareUse = exportUse{
		features: vk.FormatFeatureFlags(vk.FormatFeatureTransferDstBit | vk.FormatFeatureTransferSrcBit | vk.FormatFeatureSampledImageBit),
		usage:    vk.ImageUsageFlags(vk.ImageUsageTransferDstBit | vk.ImageUsageTransferSrcBit | vk.ImageUsageSampledBit),
		verb:     "exports",
	}
	// encodeUse is an NV12 picture another API's encoder reads: written
	// by a transfer, never otherwise used here.
	encodeUse = exportUse{
		features: vk.FormatFeatureFlags(vk.FormatFeatureTransferDstBit),
		usage:    vk.ImageUsageFlags(vk.ImageUsageTransferDstBit),
		verb:     "encodes from",
	}
)

// modifiers are the DRM modifiers the device offers format in for use
// and exports as a dmabuf, with the format's own planes. Modifiers with
// an auxiliary plane (Intel CCS, AMD DCC) are left out: an exported
// image carries only its format's planes.
func (d *Device) modifiers(format fourcc.Format, use exportUse) ([]fourcc.Modifier, error) {
	if !d.exports {
		return nil, ErrNoDmabuf
	}
	vf, planes, err := exportFormatOf(format)
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
	var out []fourcc.Modifier
	for _, m := range all[:list.DrmFormatModifierCount] {
		if int(m.DrmFormatModifierPlaneCount) != planes || m.DrmFormatModifierTilingFeatures&use.features != use.features {
			continue
		}
		mod := fourcc.Modifier(m.DrmFormatModifier)
		if _, ok := d.exportable(vf, mod, use.usage); ok {
			out = append(out, mod)
		}
	}
	return out, nil
}

// exportable reports whether an image of vf in modifier mod, made for
// usage, exports its memory as a dmabuf, and the largest such image:
// what a modifier's format features alone do not promise.
func (d *Device) exportable(vf vk.Format, mod fourcc.Modifier, usage vk.ImageUsageFlags) (vk.Extent3D, bool) {
	modInfo := vk.PhysicalDeviceImageDrmFormatModifierInfoEXT{
		SType: vk.StructureTypePhysicalDeviceImageDrmFormatModifierInfoExt, DrmFormatModifier: uint64(mod), SharingMode: vk.SharingModeExclusive,
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

// errPlanesOverlap reports a driver that lays an exported image's
// memory planes over each other (lavapipe's NV12 in Mesa 26.2, which
// gives every memory plane the first one's layout, mesa#16701): no
// consumer could read that dmabuf.
var errPlanesOverlap = errors.New("gpu: the driver lays an image's planes over each other")

// exported is an image whose memory is a dmabuf, and that dmabuf.
type exported struct {
	image  vk.Image
	memory vk.DeviceMemory
	buf    dmabuf.Buffer
}

// export allocates a width x height image in format for use whose
// layout the driver picks among the offered modifiers the device has
// for use; none in common is an error naming both lists.
func (d *Device) export(width, height int, format fourcc.Format, offered []fourcc.Modifier, use exportUse) (exported, error) {
	if err := d.checkSize(width, height); err != nil {
		return exported{}, err
	}
	if !d.exports {
		return exported{}, ErrNoDmabuf
	}
	if len(offered) == 0 {
		return exported{}, errors.New("gpu: no modifier to allocate an image in")
	}
	vf, planes, err := exportFormatOf(format)
	if err != nil {
		return exported{}, err
	}
	supported, err := d.modifiers(format, use)
	if err != nil {
		return exported{}, err
	}
	mods := slices.DeleteFunc(slices.Clone(offered), func(m fourcc.Modifier) bool {
		if !slices.Contains(supported, m) {
			return true
		}
		limit, _ := d.exportable(vf, m, use.usage)
		return width > int(limit.Width) || height > int(limit.Height)
	})
	if len(mods) == 0 {
		return exported{}, fmt.Errorf("gpu: none of the modifiers %v is one %s %s DRM format %s in at %dx%d (%v)", offered, d.name, use.verb, format, width, height, supported)
	}
	var e exported
	if err := e.create(d, width, height, vf, planes, mods, use.usage); err != nil {
		e.close(d)
		return exported{}, err
	}
	e.buf.Format = format
	return e, nil
}

// create makes the image in one of mods, its exportable memory and the
// dmabuf describing its planes, each plane with an fd of its own.
func (e *exported) create(d *Device, width, height int, vf vk.Format, planes int, offered []fourcc.Modifier, usage vk.ImageUsageFlags) error {
	mods := make([]uint64, len(offered))
	for i, m := range offered {
		mods[i] = uint64(m)
	}
	modList := vk.ImageDrmFormatModifierListCreateInfoEXT{
		SType: vk.StructureTypeImageDrmFormatModifierListCreateInfoExt, DrmFormatModifierCount: count(mods), PDrmFormatModifiers: &mods[0],
	}
	ext := vk.ExternalMemoryImageCreateInfo{
		SType: vk.StructureTypeExternalMemoryImageCreateInfo, PNext: (*uintptr)(unsafe.Pointer(&modList)),
		HandleTypes: vk.ExternalMemoryHandleTypeFlags(vk.ExternalMemoryHandleTypeDmaBufBitExt),
	}
	ici := vk.ImageCreateInfo{
		SType: vk.StructureTypeImageCreateInfo, PNext: (*uintptr)(unsafe.Pointer(&ext)),
		ImageType: vk.ImageType2d, Format: vf,
		Extent: extent3D(width, height), MipLevels: 1, ArrayLayers: 1,
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
	e.buf = dmabuf.Buffer{Width: width, Height: height, Modifier: fourcc.Modifier(mod.DrmFormatModifier)}
	var end vk.DeviceSize
	for i := range planes {
		sub := vk.ImageSubresource{AspectMask: vk.ImageAspectFlags(vk.ImageAspectMemoryPlane0BitExt << i)}
		var lay vk.SubresourceLayout
		d.cmds.GetImageSubresourceLayout(d.device, e.image, &sub, &lay)
		if lay.Offset < end {
			return fmt.Errorf("%w: %s lays memory plane %d of a modifier %s image at %d, inside the plane before (to %d)", errPlanesOverlap, d.name, i, e.buf.Modifier, lay.Offset, end)
		}
		end = lay.Offset + lay.Size
		offset, errOffset := intconv.Exact[uint32](lay.Offset)
		stride, errStride := intconv.Exact[uint32](lay.RowPitch)
		if err := errors.Join(errOffset, errStride); err != nil {
			if i == 0 {
				_ = syscall.Close(int(fd))
			}
			return fmt.Errorf("gpu: %s lays memory plane %d at offset %d, stride %d, past a dmabuf plane's 32 bits: %w", d.name, i, lay.Offset, lay.RowPitch, err)
		}
		planeFD := int(fd)
		if i > 0 {
			dup, err := syscall.Dup(int(fd))
			if err != nil {
				return fmt.Errorf("gpu: dup the dmabuf for plane %d: %w", i, err)
			}
			planeFD = dup
		}
		e.buf.Planes = append(e.buf.Planes, dmabuf.Plane{FD: planeFD, Offset: offset, Stride: stride})
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
func (d *Device) ExportModifiers(format fourcc.Format) ([]fourcc.Modifier, error) {
	if _, err := vkFormatOf(format); err != nil {
		return nil, err
	}
	return d.modifiers(format, shareUse)
}

// NewBuffer allocates a width x height Buffer in format
// (fourcc.ARGB8888 or XRGB8888) whose layout the driver picks among
// offered, the modifiers the consumer imports, that the device exports
// format in (ExportModifiers). None in common, as with only the
// implicit fourcc.Invalid on offer, is an error naming both lists.
func (d *Device) NewBuffer(width, height int, format fourcc.Format, offered []fourcc.Modifier) (*Buffer, error) {
	if _, err := vkFormatOf(format); err != nil {
		return nil, err
	}
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

// ReadPixels copies what the buffer holds (the frame a compositor last
// copied into it) back into memory as RGBA, opaque when its format
// has no alpha: screenshots and tests.
func (b *Buffer) ReadPixels() (*image.RGBA, error) {
	if b.closed {
		return nil, errors.New("gpu: read back a closed buffer")
	}
	d := b.d
	w, h := b.e.buf.Width, b.e.buf.Height
	return d.readPixels(w, h, b.e.buf.Format == fourcc.XRGB8888, func(cb vk.CommandBuffer, staging *buffer) {
		fullBarrier(d, cb, b.e.image, vk.ImageLayoutGeneral, vk.ImageLayoutTransferSrcOptimal, vk.QueueFamilyForeignExt, d.family)
		region := vk.BufferImageCopy{
			ImageSubresource: vk.ImageSubresourceLayers{AspectMask: vk.ImageAspectFlags(vk.ImageAspectColorBit), LayerCount: 1},
			ImageExtent:      extent3D(w, h),
		}
		d.cmds.CmdCopyImageToBuffer(cb, b.e.image, vk.ImageLayoutTransferSrcOptimal, staging.buf, 1, &region)
		fullBarrier(d, cb, b.e.image, vk.ImageLayoutTransferSrcOptimal, vk.ImageLayoutGeneral, d.family, vk.QueueFamilyForeignExt)
	})
}

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
