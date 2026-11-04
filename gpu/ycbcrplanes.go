package gpu

import (
	"errors"
	"fmt"
	"runtime"
	"slices"
	"unsafe"

	"github.com/stubbedev/lys-lib/dmabuf"
	"github.com/stubbedev/lys-lib/fourcc"
	"github.com/stubbedev/lys-lib/third_party/vk"
)

// A driver may list a YUV format without the chroma features a sampler
// conversion needs (Intel's lists P010 so), or not at all. What it can
// still do is sample each plane as a plain image over the same memory,
// as Mesa lowers YUV for GLES, and ycbcrplanes.frag then converts.

// planeImage is one RGB-format image over a dmabuf's plane, sized down by
// its subsampling.
type planeImage struct {
	plane      int
	vf         vk.Format
	hsub, vsub int
}

// planeSample is where one of Y, Cb and Cr is read from.
type planeSample struct {
	image   int
	channel vk.ComponentSwizzle
}

// yuvImageLayout is how a YUV format reads as plain images.
type yuvImageLayout struct {
	images     []planeImage
	y, cb, cr  planeSample
	bits       int
	hsub, vsub int
}

func biplanar(r, rg vk.Format, bits, hsub, vsub int, swapped bool) yuvImageLayout {
	l := yuvImageLayout{
		images: []planeImage{{0, r, 1, 1}, {1, rg, hsub, vsub}},
		y:      planeSample{0, vk.ComponentSwizzleR}, cb: planeSample{1, vk.ComponentSwizzleR}, cr: planeSample{1, vk.ComponentSwizzleG},
		bits: bits, hsub: hsub, vsub: vsub,
	}
	if swapped {
		l.cb, l.cr = l.cr, l.cb
	}
	return l
}

func triplanar(r vk.Format, bits, hsub, vsub int, swapped bool) yuvImageLayout {
	l := yuvImageLayout{
		images: []planeImage{{0, r, 1, 1}, {1, r, hsub, vsub}, {2, r, hsub, vsub}},
		y:      planeSample{0, vk.ComponentSwizzleR}, cb: planeSample{1, vk.ComponentSwizzleR}, cr: planeSample{2, vk.ComponentSwizzleR},
		bits: bits, hsub: hsub, vsub: vsub,
	}
	if swapped {
		l.cb, l.cr = l.cr, l.cb
	}
	return l
}

// packed422 reads two luma samples a macropixel from a two-channel image
// of it, and chroma from a four-channel image half as wide.
func packed422(rg, rgba vk.Format, bits int, y, cb, cr vk.ComponentSwizzle) yuvImageLayout {
	return yuvImageLayout{
		images: []planeImage{{0, rg, 1, 1}, {0, rgba, 2, 1}},
		y:      planeSample{0, y}, cb: planeSample{1, cb}, cr: planeSample{1, cr},
		bits: bits, hsub: 2, vsub: 1,
	}
}

var yuvImageLayouts = func() map[fourcc.Format]yuvImageLayout {
	const (
		r8, rg8, rgba8    = vk.FormatR8Unorm, vk.FormatR8g8Unorm, vk.FormatR8g8b8a8Unorm
		r16, rg16, rgba16 = vk.FormatR16Unorm, vk.FormatR16g16Unorm, vk.FormatR16g16b16a16Unorm
		sr, sg, sb, sa    = vk.ComponentSwizzleR, vk.ComponentSwizzleG, vk.ComponentSwizzleB, vk.ComponentSwizzleA
	)
	return map[fourcc.Format]yuvImageLayout{
		fourcc.NV12:   biplanar(r8, rg8, 8, 2, 2, false),
		fourcc.NV21:   biplanar(r8, rg8, 8, 2, 2, true),
		fourcc.NV16:   biplanar(r8, rg8, 8, 2, 1, false),
		fourcc.NV61:   biplanar(r8, rg8, 8, 2, 1, true),
		fourcc.NV24:   biplanar(r8, rg8, 8, 1, 1, false),
		fourcc.NV42:   biplanar(r8, rg8, 8, 1, 1, true),
		fourcc.P010:   biplanar(r16, rg16, 16, 2, 2, false),
		fourcc.P012:   biplanar(r16, rg16, 16, 2, 2, false),
		fourcc.P016:   biplanar(r16, rg16, 16, 2, 2, false),
		fourcc.P210:   biplanar(r16, rg16, 16, 2, 1, false),
		fourcc.YUV420: triplanar(r8, 8, 2, 2, false),
		fourcc.YVU420: triplanar(r8, 8, 2, 2, true),
		fourcc.YUV422: triplanar(r8, 8, 2, 1, false),
		fourcc.YVU422: triplanar(r8, 8, 2, 1, true),
		fourcc.YUV444: triplanar(r8, 8, 1, 1, false),
		fourcc.YVU444: triplanar(r8, 8, 1, 1, true),
		fourcc.Q410:   triplanar(r16, 16, 1, 1, false),
		fourcc.Q401:   triplanar(r16, 16, 1, 1, true),
		fourcc.YUYV:   packed422(rg8, rgba8, 8, sr, sg, sa),
		fourcc.YVYU:   packed422(rg8, rgba8, 8, sr, sa, sg),
		fourcc.UYVY:   packed422(rg8, rgba8, 8, sg, sr, sb),
		fourcc.VYUY:   packed422(rg8, rgba8, 8, sg, sb, sr),
		fourcc.Y210:   packed422(rg16, rgba16, 16, sr, sg, sa),
		fourcc.Y212:   packed422(rg16, rgba16, 16, sr, sg, sa),
		fourcc.Y216:   packed422(rg16, rgba16, 16, sr, sg, sa),
	}
}()

// chromaFeatures are what a sampler conversion needs one of.
const chromaFeatures = vk.FormatFeatureFlags(vk.FormatFeatureCositedChromaSamplesBit | vk.FormatFeatureMidpointChromaSamplesBit)

// driverConverts reports whether the device's sampler conversion samples
// format in modifier: the features its modifier lists, and an import.
func (d *Device) driverConverts(format fourcc.Format, modifier fourcc.Modifier, features vk.FormatFeatureFlags, create vk.ImageCreateFlags) bool {
	pf := pixelFormats[format]
	if !pf.ycbcr || !d.ycbcr || pf.core13 && !d.vulkan13 {
		return false
	}
	want := features | textureImport.features
	for _, m := range d.formatModifiers(pf.vk) {
		if fourcc.Modifier(m.DrmFormatModifier) != modifier {
			continue
		}
		if m.DrmFormatModifierTilingFeatures&want != want || m.DrmFormatModifierTilingFeatures&chromaFeatures == 0 {
			return false
		}
		_, ok := d.external(pf.vk, modifier, textureImport.usage, create, vk.ExternalMemoryFeatureImportableBit)
		return ok
	}
	return false
}

// planesModifiers are the modifiers every image of format's layout
// samples and imports in.
func (d *Device) planesModifiers(l yuvImageLayout) []fourcc.Modifier {
	var out []fourcc.Modifier
	for i, img := range l.images {
		var mods []fourcc.Modifier
		for _, m := range d.formatModifiers(img.vf) {
			mod := fourcc.Modifier(m.DrmFormatModifier)
			if m.DrmFormatModifierPlaneCount != 1 || m.DrmFormatModifierTilingFeatures&textureImport.features != textureImport.features {
				continue
			}
			if _, ok := d.external(img.vf, mod, textureImport.usage, 0, vk.ExternalMemoryFeatureImportableBit); ok {
				mods = append(mods, mod)
			}
		}
		if i == 0 {
			out = mods
			continue
		}
		out = slices.DeleteFunc(out, func(m fourcc.Modifier) bool { return !slices.Contains(mods, m) })
	}
	return out
}

type planesKey struct {
	base  yuv444Key
	shift [2]bool
}

// planesConstants are ycbcrplanes.frag's specialization constants.
type planesConstants struct {
	yuv444Constants
	chromaShift [2]float32
}

type planesKind struct {
	constants planesConstants
	sets      setPools
	layout    vk.PipelineLayout
	pipes     map[vk.Format]vk.Pipeline
	colored   coloredPipes
}

// cosited reports chroma sited with the first luma sample along an axis
// that is subsampled: the sample then lies a quarter chroma texel before
// where a texture read at the luma coordinate would take it.
func cosited(s, def Siting, sub int) bool {
	if s == SitingDefault {
		s = def
	}
	return sub > 1 && s == SitedAt0
}

func (d *Device) planesKindOf(l yuvImageLayout, c YCbCr) (*planesKind, error) {
	if c.Chroma.X > SitedAtHalf || c.Chroma.Y > SitedAtHalf {
		return nil, fmt.Errorf("gpu: chroma siting %+v is not one Vulkan samples", c.Chroma)
	}
	k := planesKey{yuv444Key{c.Matrix, c.Range, l.bits}, [2]bool{cosited(c.Chroma.X, SitedAt0, l.hsub), cosited(c.Chroma.Y, SitedAtHalf, l.vsub)}}
	base, err := k.base.constants()
	if err != nil {
		return nil, err
	}
	if err := d.compInit(); err != nil {
		return nil, err
	}
	if kind, ok := d.comp.planes[k]; ok {
		return kind, nil
	}
	kind := &planesKind{constants: planesConstants{yuv444Constants: base}, pipes: map[vk.Format]vk.Pipeline{}}
	for i, shift := range k.shift {
		if shift {
			kind.constants.chromaShift[i] = 0.25
		}
	}
	if err := d.samplerLayouts(nil, 3, 3, &kind.sets, &kind.layout); err != nil {
		kind.destroy(d)
		return nil, err
	}
	d.comp.planes[k] = kind
	return kind, nil
}

func (kind *planesKind) pipeline(d *Device, target vk.Format) (vk.Pipeline, error) {
	if p, ok := kind.pipes[target]; ok {
		return p, nil
	}
	ct, err := d.compTargetOf(target)
	if err != nil {
		return 0, err
	}
	build := func() (vk.Pipeline, error) {
		p, err := d.constantsPipelineOf(ct, d.comp.frags[fragYCbCrPlanes], kind.layout, unsafe.Pointer(&kind.constants), unsafe.Sizeof(kind.constants))
		runtime.KeepAlive(kind)
		return p, err
	}
	return d.lazyPipeline(pipelineKey{owner: kind, target: target}, 0, build, func(p vk.Pipeline) { kind.pipes[target] = p })
}

func (kind *planesKind) destroy(d *Device) {
	kind.colored.destroy(d)
	for _, p := range kind.pipes {
		d.cmds.DestroyPipeline(d.device, p, nil)
	}
	if kind.layout != 0 {
		d.cmds.DestroyPipelineLayout(d.device, kind.layout, nil)
	}
	kind.sets.destroy(d)
}

// constantsPipeline is a compositing pipeline into target whose fragment
// shader takes its float constants, in order, from size bytes at data.
func (d *Device) constantsPipelineOf(ct *compTarget, frag vk.ShaderModule, layout vk.PipelineLayout, data unsafe.Pointer, size uintptr) (vk.Pipeline, error) {
	const float = unsafe.Sizeof(float32(0))
	entries := make([]vk.SpecializationMapEntry, size/float)
	for i := range entries {
		entries[i] = vk.SpecializationMapEntry{ConstantID: uint32(i), Offset: uint32(uintptr(i) * float), Size: float}
	}
	spec := vk.SpecializationInfo{MapEntryCount: count(entries), PMapEntries: &entries[0], DataSize: size, PData: (*uintptr)(data)}
	p, err := d.specializedPipeline([2]vk.ShaderModule{d.comp.vert, frag}, layout, ct.pass, blendOver, &spec)
	runtime.KeepAlive(entries)
	return p, err
}

// importPlanes samples b through plain images over its planes.
func (d *Device) importPlanes(b dmabuf.Buffer, l yuvImageLayout, c YCbCr) (*Texture, error) {
	t := &Texture{d: d, format: b.Format, pf: pixelFormats[b.Format], w: b.Width, h: b.Height, foreign: true, layout: vk.ImageLayoutGeneral}
	if len(b.Planes) != t.pf.planes {
		return nil, fmt.Errorf("gpu: DRM format %s has %d planes, the dmabuf %d", b.Format, t.pf.planes, len(b.Planes))
	}
	var err error
	if t.planes, err = d.planesKindOf(l, c); err != nil {
		return nil, err
	}
	if err := t.createPlanes(b, l); err != nil {
		t.release()
		return nil, err
	}
	d.textures[t] = struct{}{}
	return t, nil
}

func (t *Texture) createPlanes(b dmabuf.Buffer, l yuvImageLayout) error {
	d := t.d
	for _, spec := range l.images {
		features, err := d.planeImageFeatures(spec.vf, b.Modifier)
		if err != nil {
			return fmt.Errorf("gpu: %s importing a %s dmabuf: %w", d.name, b.Format, err)
		}
		if features&vk.FormatFeatureFlags(vk.FormatFeatureSampledImageFilterLinearBit) == 0 {
			t.nearest = true
		}
		sub := b
		sub.Width, sub.Height = (b.Width+spec.hsub-1)/spec.hsub, (b.Height+spec.vsub-1)/spec.vsub
		sub.Planes = b.Planes[spec.plane : spec.plane+1]
		im := imported{pf: pixelFormat{vk: spec.vf}}
		if err := im.create(d, sub, textureImport.usage, 0); err != nil {
			im.close(d)
			return err
		}
		t.planeImages = append(t.planeImages, im.image)
		t.memory = append(t.memory, im.memory...)
	}
	for i, s := range [3]planeSample{l.y, l.cb, l.cr} {
		vci := vk.ImageViewCreateInfo{
			SType: vk.StructureTypeImageViewCreateInfo, Image: t.planeImages[s.image], ViewType: vk.ImageViewType2d, Format: l.images[s.image].vf,
			Components: vk.ComponentMapping{R: s.channel}, SubresourceRange: colorRange,
		}
		if r := d.cmds.CreateImageView(d.device, &vci, nil, &t.planeViews[i]); r != vk.Success {
			return fmt.Errorf("gpu: vkCreateImageView (YUV plane): %w", resultError(r))
		}
	}
	return nil
}

func (d *Device) planeImageFeatures(vf vk.Format, modifier fourcc.Modifier) (vk.FormatFeatureFlags, error) {
	for _, m := range d.formatModifiers(vf) {
		if fourcc.Modifier(m.DrmFormatModifier) == modifier && m.DrmFormatModifierPlaneCount == 1 && m.DrmFormatModifierTilingFeatures&textureImport.features == textureImport.features {
			return m.DrmFormatModifierTilingFeatures, nil
		}
	}
	return 0, fmt.Errorf("no plane format %d in modifier %s", vf, modifier)
}

// setPlanes is t's descriptor set, luma through filter f and chroma
// bilinearly: it lies between luma samples.
func (t *Texture) setPlanes(f filter) (textureSet, error) {
	d := t.d
	s, err := d.allocBareSet(&t.planes.sets)
	if err != nil {
		return textureSet{}, err
	}
	chroma := d.comp.samplers[filterLinear]
	if t.nearest {
		chroma = d.comp.samplers[filterNearest]
	}
	infos := [3]vk.DescriptorImageInfo{
		{Sampler: d.comp.samplers[f], ImageView: t.planeViews[0], ImageLayout: vk.ImageLayoutShaderReadOnlyOptimal},
		{Sampler: chroma, ImageView: t.planeViews[1], ImageLayout: vk.ImageLayoutShaderReadOnlyOptimal},
		{Sampler: chroma, ImageView: t.planeViews[2], ImageLayout: vk.ImageLayoutShaderReadOnlyOptimal},
	}
	var writes [3]vk.WriteDescriptorSet
	for i := range writes {
		writes[i] = vk.WriteDescriptorSet{SType: vk.StructureTypeWriteDescriptorSet, DstSet: s.set, DstBinding: uint32(i), DescriptorCount: 1, DescriptorType: vk.DescriptorTypeCombinedImageSampler, PImageInfo: &infos[i]}
	}
	d.cmds.UpdateDescriptorSets(d.device, count(writes[:]), &writes[0], 0, nil)
	runtime.KeepAlive(&infos)
	return s, nil
}

// checkImportable is what every import of b needs.
func (d *Device) checkImportable(b dmabuf.Buffer) error {
	if !d.exports {
		return ErrNoDmabuf
	}
	if err := b.Validate(); err != nil {
		return err
	}
	if err := d.checkSize(b.Width, b.Height); err != nil {
		return err
	}
	if b.Modifier == fourcc.Invalid {
		return errors.New("gpu: a dmabuf with the implicit modifier does not import into Vulkan")
	}
	return nil
}

// importCreateFlags is the disjoint flag a dmabuf with a memory object
// per plane needs, and the features that come with it.
func importCreateFlags(planes []dmabuf.Plane) (vk.FormatFeatureFlags, vk.ImageCreateFlags, error) {
	disjoint, err := disjointPlanes(planes)
	if err != nil {
		return 0, 0, err
	}
	if !disjoint {
		return 0, 0, nil
	}
	return vk.FormatFeatureFlags(vk.FormatFeatureDisjointBit), vk.ImageCreateFlags(vk.ImageCreateDisjointBit), nil
}
