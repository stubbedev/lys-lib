package gpu

import (
	"errors"
	"fmt"
	"math/bits"
	"runtime"
	"slices"
	"unsafe"

	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-lib/dmabuf"
	"github.com/stubbedev/lys-lib/fourcc"
	"github.com/stubbedev/lys-lib/intconv"
	"github.com/stubbedev/lys-lib/third_party/vk"
)

type importUse struct {
	features vk.FormatFeatureFlags
	usage    vk.ImageUsageFlags
	formats  func(d *Device) []fourcc.Format
	verb     string
}

var (
	textureImport = importUse{
		features: vk.FormatFeatureFlags(vk.FormatFeatureSampledImageBit),
		usage:    vk.ImageUsageFlags(vk.ImageUsageSampledBit),
		formats:  (*Device).importFormats,
		verb:     "samples",
	}
	targetImport = importUse{
		features: renderUse.features,
		usage:    renderUse.usage,
		formats:  func(*Device) []fourcc.Format { return textureFormats },
		verb:     "renders into",
	}
)

func (d *Device) importFormats() []fourcc.Format {
	if d.ycbcr {
		return append(slices.Clone(textureFormats), ycbcrFormats...)
	}
	return textureFormats
}

func (d *Device) importFormatOf(format fourcc.Format, use importUse) (pixelFormat, error) {
	if formats := use.formats(d); !slices.Contains(formats, format) {
		return pixelFormat{}, fmt.Errorf("gpu: %s %s no imported DRM format %s (%v)", d.name, use.verb, format, formats)
	}
	return pixelFormats[format], nil
}

// TextureModifiers are the DRM modifiers ImportTexture takes format
// in: what a compositor offers its clients through linux-dmabuf.
func (d *Device) TextureModifiers(format fourcc.Format) ([]fourcc.Modifier, error) {
	return d.importModifiers(format, textureImport)
}

// TargetModifiers are the DRM modifiers ImportTarget takes format in:
// what to allocate a scanout buffer the device renders into in.
func (d *Device) TargetModifiers(format fourcc.Format) ([]fourcc.Modifier, error) {
	return d.importModifiers(format, targetImport)
}

func (d *Device) importModifiers(format fourcc.Format, use importUse) ([]fourcc.Modifier, error) {
	if !d.exports {
		return nil, ErrNoDmabuf
	}
	pf, err := d.importFormatOf(format, use)
	if err != nil {
		return nil, err
	}
	var out []fourcc.Modifier
	for _, m := range d.formatModifiers(pf.vk) {
		mod := fourcc.Modifier(m.DrmFormatModifier)
		if m.DrmFormatModifierTilingFeatures&use.features != use.features {
			continue
		}
		if _, ok := d.external(pf.vk, mod, use.usage, 0, vk.ExternalMemoryFeatureImportableBit); ok {
			out = append(out, mod)
		}
	}
	return out, nil
}

type imported struct {
	image  vk.Image
	memory []vk.DeviceMemory
	pf     pixelFormat
}

func (im *imported) close(d *Device) {
	if im.image != 0 {
		d.cmds.DestroyImage(d.device, im.image, nil)
	}
	d.freeMemory(im.memory)
	*im = imported{}
}

func (d *Device) importImage(b dmabuf.Buffer, use importUse) (imported, vk.FormatFeatureFlags, error) {
	if !d.exports {
		return imported{}, 0, ErrNoDmabuf
	}
	if err := b.Validate(); err != nil {
		return imported{}, 0, err
	}
	pf, err := d.importFormatOf(b.Format, use)
	if err != nil {
		return imported{}, 0, err
	}
	if err := d.checkSize(b.Width, b.Height); err != nil {
		return imported{}, 0, err
	}
	if b.Modifier == fourcc.Invalid {
		return imported{}, 0, errors.New("gpu: a dmabuf with the implicit modifier does not import into Vulkan")
	}
	disjoint, err := disjointPlanes(b.Planes)
	if err != nil {
		return imported{}, 0, err
	}
	features, create := use.features, vk.ImageCreateFlags(0)
	if disjoint {
		features |= vk.FormatFeatureFlags(vk.FormatFeatureDisjointBit)
		create = vk.ImageCreateFlags(vk.ImageCreateDisjointBit)
	}
	have, err := d.checkImport(b, pf, use, features, create)
	if err != nil {
		return imported{}, 0, err
	}
	im := imported{pf: pf}
	if err := im.create(d, b, use.usage, create); err != nil {
		im.close(d)
		return imported{}, 0, err
	}
	return im, have, nil
}

func (d *Device) checkImport(b dmabuf.Buffer, pf pixelFormat, use importUse, features vk.FormatFeatureFlags, create vk.ImageCreateFlags) (vk.FormatFeatureFlags, error) {
	i := slices.IndexFunc(d.formatModifiers(pf.vk), func(m vk.DrmFormatModifierPropertiesEXT) bool {
		return fourcc.Modifier(m.DrmFormatModifier) == b.Modifier
	})
	if i < 0 {
		return 0, fmt.Errorf("gpu: %s has DRM format %s in no modifier %s", d.name, b.Format, b.Modifier)
	}
	m := d.formatModifiers(pf.vk)[i]
	switch {
	case int(m.DrmFormatModifierPlaneCount) != len(b.Planes):
		return 0, fmt.Errorf("gpu: DRM format %s in modifier %s has %d planes, the dmabuf %d", b.Format, b.Modifier, m.DrmFormatModifierPlaneCount, len(b.Planes))
	case m.DrmFormatModifierTilingFeatures&features != features:
		return 0, fmt.Errorf("gpu: %s %s no DRM format %s in modifier %s", d.name, use.verb, b.Format, b.Modifier)
	}
	limit, ok := d.external(pf.vk, b.Modifier, use.usage, create, vk.ExternalMemoryFeatureImportableBit)
	if !ok || b.Width > int(limit.Width) || b.Height > int(limit.Height) {
		return 0, fmt.Errorf("gpu: %s imports no %dx%d DRM format %s dmabuf in modifier %s it %s", d.name, b.Width, b.Height, b.Format, b.Modifier, use.verb)
	}
	return m.DrmFormatModifierTilingFeatures, nil
}

func disjointPlanes(planes []dmabuf.Plane) (bool, error) {
	var first unix.Stat_t
	for i, p := range planes {
		var st unix.Stat_t
		if err := unix.Fstat(p.FD, &st); err != nil {
			return false, fmt.Errorf("gpu: dmabuf plane %d: %w", i, err)
		}
		if i == 0 {
			first = st
		} else if st.Dev != first.Dev || st.Ino != first.Ino {
			return true, nil
		}
	}
	return false, nil
}

func (im *imported) create(d *Device, b dmabuf.Buffer, usage vk.ImageUsageFlags, create vk.ImageCreateFlags) error {
	layouts := make([]vk.SubresourceLayout, len(b.Planes))
	for i, p := range b.Planes {
		layouts[i] = vk.SubresourceLayout{Offset: vk.DeviceSize(p.Offset), RowPitch: vk.DeviceSize(p.Stride)}
	}
	explicit := vk.ImageDrmFormatModifierExplicitCreateInfoEXT{
		SType: vk.StructureTypeImageDrmFormatModifierExplicitCreateInfoExt, DrmFormatModifier: uint64(b.Modifier),
		DrmFormatModifierPlaneCount: count(layouts), PPlaneLayouts: &layouts[0],
	}
	ext := vk.ExternalMemoryImageCreateInfo{
		SType: vk.StructureTypeExternalMemoryImageCreateInfo, PNext: (*uintptr)(unsafe.Pointer(&explicit)),
		HandleTypes: vk.ExternalMemoryHandleTypeFlags(vk.ExternalMemoryHandleTypeDmaBufBitExt),
	}
	ici := vk.ImageCreateInfo{
		SType: vk.StructureTypeImageCreateInfo, PNext: (*uintptr)(unsafe.Pointer(&ext)), Flags: create,
		ImageType: vk.ImageType2d, Format: im.pf.vk, Extent: extent3D(b.Width, b.Height), MipLevels: 1, ArrayLayers: 1,
		Samples: vk.SampleCount1Bit, Tiling: vk.ImageTilingDrmFormatModifierExt,
		Usage: usage, SharingMode: vk.SharingModeExclusive, InitialLayout: vk.ImageLayoutUndefined,
	}
	r := d.cmds.CreateImage(d.device, &ici, nil, &im.image)
	runtime.KeepAlive(&explicit)
	runtime.KeepAlive(&ext)
	runtime.KeepAlive(layouts)
	if r != vk.Success {
		return fmt.Errorf("gpu: vkCreateImage importing a %dx%d %s dmabuf in modifier %s: %d", b.Width, b.Height, b.Format, b.Modifier, r)
	}
	if create == 0 {
		var req vk.MemoryRequirements
		d.cmds.GetImageMemoryRequirements(d.device, im.image, &req)
		mem, err := d.importMemory(b.Planes[0].FD, req, im.image)
		if err != nil {
			return err
		}
		im.memory = append(im.memory, mem)
		if r := d.cmds.BindImageMemory(d.device, im.image, mem, 0); r != vk.Success {
			return fmt.Errorf("gpu: vkBindImageMemory (import): %d", r)
		}
		return nil
	}
	return im.bindPlanes(d, b)
}

func (im *imported) bindPlanes(d *Device, b dmabuf.Buffer) error {
	planes := make([]vk.BindImagePlaneMemoryInfo, len(b.Planes))
	binds := make([]vk.BindImageMemoryInfo, len(b.Planes))
	for i, p := range b.Planes {
		aspect := vk.ImageAspectMemoryPlane0BitExt << i
		pi := vk.ImagePlaneMemoryRequirementsInfo{SType: vk.StructureTypeImagePlaneMemoryRequirementsInfo, PlaneAspect: aspect}
		ri := vk.ImageMemoryRequirementsInfo2{SType: vk.StructureTypeImageMemoryRequirementsInfo2, PNext: (*uintptr)(unsafe.Pointer(&pi)), Image: im.image}
		req := vk.MemoryRequirements2{SType: vk.StructureTypeMemoryRequirements2}
		d.cmds.GetImageMemoryRequirements2(d.device, &ri, &req)
		runtime.KeepAlive(&pi)
		mem, err := d.importMemory(p.FD, req.MemoryRequirements, 0)
		if err != nil {
			return err
		}
		im.memory = append(im.memory, mem)
		planes[i] = vk.BindImagePlaneMemoryInfo{SType: vk.StructureTypeBindImagePlaneMemoryInfo, PlaneAspect: aspect}
		binds[i] = vk.BindImageMemoryInfo{SType: vk.StructureTypeBindImageMemoryInfo, PNext: (*uintptr)(unsafe.Pointer(&planes[i])), Image: im.image, Memory: mem}
	}
	r := d.cmds.BindImageMemory2(d.device, count(binds), &binds[0])
	runtime.KeepAlive(planes)
	if r != vk.Success {
		return fmt.Errorf("gpu: vkBindImageMemory2 (disjoint import): %d", r)
	}
	return nil
}

func (d *Device) importMemory(fd int, req vk.MemoryRequirements, dedicated vk.Image) (vk.DeviceMemory, error) {
	size, err := unix.Seek(fd, 0, unix.SEEK_END)
	if err != nil {
		return 0, fmt.Errorf("gpu: size of dmabuf %d: %w", fd, err)
	}
	if need, err := intconv.Exact[int64](req.Size); err != nil || size < need {
		return 0, fmt.Errorf("gpu: dmabuf %d holds %d bytes, the image needs %d", fd, size, req.Size)
	}
	props := vk.MemoryFdPropertiesKHR{SType: vk.StructureTypeMemoryFdPropertiesKhr}
	if r := d.cmds.GetMemoryFdPropertiesKHR(d.device, vk.ExternalMemoryHandleTypeDmaBufBitExt, intconv.Must[int32](fd), &props); r != vk.Success {
		return 0, fmt.Errorf("gpu: vkGetMemoryFdPropertiesKHR on dmabuf %d: %d", fd, r)
	}
	types := req.MemoryTypeBits & props.MemoryTypeBits
	if types == 0 {
		return 0, fmt.Errorf("%w: the image binds %#x, dmabuf %d %#x", errNoMemoryType, req.MemoryTypeBits, fd, props.MemoryTypeBits)
	}
	dup, err := unix.FcntlInt(uintptr(fd), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return 0, fmt.Errorf("gpu: dup dmabuf %d: %w", fd, err)
	}
	ded := vk.MemoryDedicatedAllocateInfo{SType: vk.StructureTypeMemoryDedicatedAllocateInfo, Image: dedicated}
	imp := vk.ImportMemoryFdInfoKHR{SType: vk.StructureTypeImportMemoryFdInfoKhr, HandleType: vk.ExternalMemoryHandleTypeDmaBufBitExt, Fd: intconv.Must[int32](dup)}
	if dedicated != 0 {
		imp.PNext = (*uintptr)(unsafe.Pointer(&ded))
	}
	mai := vk.MemoryAllocateInfo{
		SType: vk.StructureTypeMemoryAllocateInfo, PNext: (*uintptr)(unsafe.Pointer(&imp)),
		AllocationSize: intconv.Must[vk.DeviceSize](size), MemoryTypeIndex: intconv.Must[uint32](bits.TrailingZeros32(types)),
	}
	var mem vk.DeviceMemory
	r := d.cmds.AllocateMemory(d.device, &mai, nil, &mem)
	runtime.KeepAlive(&ded)
	runtime.KeepAlive(&imp)
	if r != vk.Success {
		_ = unix.Close(dup)
		return 0, fmt.Errorf("gpu: vkAllocateMemory importing dmabuf %d: %d", fd, r)
	}
	return mem, nil
}

// ImportTexture imports b, a client's dmabuf in one of TextureFormats,
// or NV12 or P010 where the device converts YCbCr (as BT.601 limited
// range; ImportTextureYCbCr takes the buffer's hints), and a modifier
// of TextureModifiers, as a texture a Pass samples. Its producer
// writes it; Update refuses it. b's fds stay the caller's: the texture
// holds the memory itself until Close.
func (d *Device) ImportTexture(b dmabuf.Buffer) (*Texture, error) {
	return d.ImportTextureYCbCr(b, YCbCr{})
}

// ImportTextureYCbCr is ImportTexture sampling a YUV buffer (NV12 or
// P010) through c; an RGB buffer ignores c.
func (d *Device) ImportTextureYCbCr(b dmabuf.Buffer, c YCbCr) (*Texture, error) {
	im, features, err := d.importImage(b, textureImport)
	if err != nil {
		return nil, err
	}
	t := &Texture{d: d, format: b.Format, pf: im.pf, w: b.Width, h: b.Height, image: im.image, memory: im.memory, foreign: true, layout: vk.ImageLayoutGeneral}
	if im.pf.ycbcr {
		if t.ycbcr, err = d.ycbcrKindOf(im.pf.vk, c, features); err != nil {
			t.release()
			return nil, err
		}
	}
	if err := t.createView(); err != nil {
		t.release()
		return nil, err
	}
	d.textures[t] = struct{}{}
	return t, nil
}

// ImportTarget imports b, a dmabuf in one of TextureFormats and a
// modifier of TargetModifiers (a scanout buffer GBM allocated), as a
// target a Pass draws into, its contents kept between passes. Like an
// exported target it is handed back to its consumer after each pass.
// Its Dmabuf has no planes: b's fds stay the caller's.
func (d *Device) ImportTarget(b dmabuf.Buffer) (*Target, error) {
	im, _, err := d.importImage(b, targetImport)
	if err != nil {
		return nil, err
	}
	t := &Target{
		s:      surface{d: d, w: b.Width, h: b.Height, vf: im.pf.vk, layout: vk.ImageLayoutGeneral, image: im.image},
		memory: im.memory,
		buf:    dmabuf.Buffer{Width: b.Width, Height: b.Height, Format: b.Format, Modifier: b.Modifier},
	}
	d.targets[t] = struct{}{}
	return t, nil
}
