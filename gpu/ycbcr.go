package gpu

import (
	"fmt"
	"runtime"
	"unsafe"

	"github.com/stubbedev/lys-lib/fourcc"
	"github.com/stubbedev/lys-lib/third_party/vk"
)

// ycbcrFormats are the formats a device that converts YCbCr can sample.
// DRM's other YUV formats have no Vulkan format with their layout: the
// 10-bit packed NV15, NV20, NV30, P030, P230 and T430, the 4x subsampled
// YUV410 and YUV411 and their swaps, the low-bit-aligned S0xx planes and
// the compressed-only VUY101010. yuv444Formats sample without one.
var ycbcrFormats = []fourcc.Format{
	fourcc.NV12, fourcc.NV21, fourcc.NV16, fourcc.NV61, fourcc.NV24, fourcc.NV42,
	fourcc.P010, fourcc.P012, fourcc.P016, fourcc.P210,
	fourcc.YUV420, fourcc.YVU420, fourcc.YUV422, fourcc.YVU422, fourcc.YUV444, fourcc.YVU444,
	fourcc.Q410, fourcc.Q401,
	fourcc.YUYV, fourcc.YVYU, fourcc.UYVY, fourcc.VYUY, fourcc.Y210, fourcc.Y212, fourcc.Y216,
}

// yuv444Formats are the packed 4:4:4 formats: Vulkan has no YCbCr format
// for them, and ycbcr444.frag converts what an RGB format samples.
var yuv444Formats = []fourcc.Format{
	fourcc.AYUV, fourcc.XYUV8888, fourcc.AVUY8888, fourcc.XVUY8888, fourcc.VUY888,
	fourcc.Y410, fourcc.XVYU2101010, fourcc.XVUY2101010,
	fourcc.Y412, fourcc.XVYU121616, fourcc.Y416, fourcc.XVYU16161616,
}

// YCbCr is how a YUV buffer's samples become RGB: the colour hints a
// client gives with it (wp_color_representation, or a video's own).
// The zero value is BT.601 in limited range, chroma sited as MPEG-2
// and H.264 site 4:2:0 chroma.
type YCbCr struct {
	Matrix YCbCrMatrix
	Range  YCbCrRange
	Chroma ChromaSiting
}

// YCbCrMatrix is the matrix coefficients a YUV buffer was encoded with.
type YCbCrMatrix uint8

// The matrices.
const (
	BT601 YCbCrMatrix = iota
	BT709
	BT2020
)

// YCbCrRange is the range a YUV buffer's samples span.
type YCbCrRange uint8

// The ranges: limited (16 to 235 for 8-bit luma) and full.
const (
	LimitedRange YCbCrRange = iota
	FullRange
)

// ChromaSiting is where a subsampled chroma sample lies along each
// axis, relative to the luma samples it covers.
type ChromaSiting struct {
	X, Y Siting
}

// Siting is a chroma sample's position along one axis.
type Siting uint8

// The sitings: the axis's default (with the first luma sample along X,
// between the two along Y, or the other where the device samples only
// that), with the first luma sample, or midway between the two.
const (
	SitingDefault Siting = iota
	SitedAt0
	SitedAtHalf
)

type ycbcrKey struct {
	vf     vk.Format
	swap   bool
	model  vk.SamplerYcbcrModelConversion
	rng    vk.SamplerYcbcrRange
	x, y   vk.ChromaLocation
	filter vk.Filter
}

type ycbcrKind struct {
	conv    vk.SamplerYcbcrConversion
	sampler vk.Sampler
	sets    setPools
	layout  vk.PipelineLayout
	pipes   map[vk.Format]vk.Pipeline
}

func (c YCbCr) key(pf pixelFormat, features vk.FormatFeatureFlags) (ycbcrKey, error) {
	k := ycbcrKey{vf: pf.vk, swap: pf.swapped, filter: vk.FilterNearest}
	switch c.Matrix {
	case BT601:
		k.model = vk.SamplerYcbcrModelConversionYcbcr601
	case BT709:
		k.model = vk.SamplerYcbcrModelConversionYcbcr709
	case BT2020:
		k.model = vk.SamplerYcbcrModelConversionYcbcr2020
	default:
		return ycbcrKey{}, fmt.Errorf("gpu: YCbCr matrix %d is none of BT.601, BT.709, BT.2020", c.Matrix)
	}
	switch c.Range {
	case LimitedRange:
		k.rng = vk.SamplerYcbcrRangeItuNarrow
	case FullRange:
		k.rng = vk.SamplerYcbcrRangeItuFull
	default:
		return ycbcrKey{}, fmt.Errorf("gpu: YCbCr range %d is neither limited nor full", c.Range)
	}
	var err error
	if k.x, err = location(c.Chroma.X, SitedAt0, features); err != nil {
		return ycbcrKey{}, err
	}
	if k.y, err = location(c.Chroma.Y, SitedAtHalf, features); err != nil {
		return ycbcrKey{}, err
	}
	if features&vk.FormatFeatureFlags(vk.FormatFeatureSampledImageYcbcrConversionLinearFilterBit) != 0 {
		k.filter = vk.FilterLinear
	}
	return k, nil
}

func location(s, def Siting, features vk.FormatFeatureFlags) (vk.ChromaLocation, error) {
	if s == SitingDefault {
		if loc, err := location(def, def, features); err == nil {
			return loc, nil
		}
		s = SitedAt0 + SitedAtHalf - def
	}
	var loc vk.ChromaLocation
	var need vk.FormatFeatureFlagBits
	switch s {
	case SitedAt0:
		loc, need = vk.ChromaLocationCositedEven, vk.FormatFeatureCositedChromaSamplesBit
	case SitedAtHalf:
		loc, need = vk.ChromaLocationMidpoint, vk.FormatFeatureMidpointChromaSamplesBit
	default:
		return 0, fmt.Errorf("gpu: chroma siting %d is not one Vulkan samples", s)
	}
	if features&flags[vk.FormatFeatureFlags](need) == 0 {
		return 0, fmt.Errorf("gpu: the device samples no chroma sited %d in this layout", s)
	}
	return loc, nil
}

func (d *Device) probeYCbCr(p vk.PhysicalDevice) bool {
	f11 := vk.PhysicalDeviceVulkan11Features{SType: vk.StructureTypePhysicalDeviceVulkan11Features}
	f2 := vk.PhysicalDeviceFeatures2{SType: vk.StructureTypePhysicalDeviceFeatures2, PNext: (*uintptr)(unsafe.Pointer(&f11))}
	d.cmds.GetPhysicalDeviceFeatures2(p, &f2)
	runtime.KeepAlive(&f11)
	return f11.SamplerYcbcrConversion != 0
}

func (d *Device) ycbcrKindOf(pf pixelFormat, c YCbCr, features vk.FormatFeatureFlags) (*ycbcrKind, error) {
	k, err := c.key(pf, features)
	if err != nil {
		return nil, err
	}
	if err := d.compInit(); err != nil {
		return nil, err
	}
	if kind, ok := d.comp.ycbcr[k]; ok {
		return kind, nil
	}
	kind := &ycbcrKind{pipes: map[vk.Format]vk.Pipeline{}}
	if err := kind.create(d, k); err != nil {
		kind.destroy(d)
		return nil, err
	}
	d.comp.ycbcr[k] = kind
	return kind, nil
}

func (kind *ycbcrKind) create(d *Device, k ycbcrKey) error {
	cci := vk.SamplerYcbcrConversionCreateInfo{
		SType: vk.StructureTypeSamplerYcbcrConversionCreateInfo, Format: k.vf, YcbcrModel: k.model, YcbcrRange: k.rng,
		XChromaOffset: k.x, YChromaOffset: k.y, ChromaFilter: k.filter,
	}
	if k.swap {
		cci.Components = vk.ComponentMapping{R: vk.ComponentSwizzleB, B: vk.ComponentSwizzleR}
	}
	if r := d.cmds.CreateSamplerYcbcrConversion(d.device, &cci, nil, &kind.conv); r != vk.Success {
		return fmt.Errorf("gpu: vkCreateSamplerYcbcrConversion: %w", resultError(r))
	}
	info := vk.SamplerYcbcrConversionInfo{SType: vk.StructureTypeSamplerYcbcrConversionInfo, Conversion: kind.conv}
	sci := vk.SamplerCreateInfo{
		SType: vk.StructureTypeSamplerCreateInfo, PNext: (*uintptr)(unsafe.Pointer(&info)), MagFilter: k.filter, MinFilter: k.filter,
		MipmapMode:   vk.SamplerMipmapModeNearest,
		AddressModeU: vk.SamplerAddressModeClampToEdge, AddressModeV: vk.SamplerAddressModeClampToEdge, AddressModeW: vk.SamplerAddressModeClampToEdge,
	}
	r := d.cmds.CreateSampler(d.device, &sci, nil, &kind.sampler)
	runtime.KeepAlive(&info)
	if r != vk.Success {
		return fmt.Errorf("gpu: vkCreateSampler (YCbCr): %w", resultError(r))
	}
	return d.samplerLayouts(&kind.sampler, 1, 3, &kind.sets, &kind.layout)
}

func (kind *ycbcrKind) pipeline(d *Device, target vk.Format) (vk.Pipeline, error) {
	if p, ok := kind.pipes[target]; ok {
		return p, nil
	}
	ct, err := d.compTargetOf(target)
	if err != nil {
		return 0, err
	}
	p, err := d.graphicsPipeline([2]vk.ShaderModule{d.comp.vert, d.comp.frags[0]}, kind.layout, ct.pass, blendOver)
	if err != nil {
		return 0, err
	}
	kind.pipes[target] = p
	return p, nil
}

func (kind *ycbcrKind) destroy(d *Device) {
	for _, p := range kind.pipes {
		d.cmds.DestroyPipeline(d.device, p, nil)
	}
	if kind.layout != 0 {
		d.cmds.DestroyPipelineLayout(d.device, kind.layout, nil)
	}
	kind.sets.destroy(d)
	if kind.sampler != 0 {
		d.cmds.DestroySampler(d.device, kind.sampler, nil)
	}
	if kind.conv != 0 {
		d.cmds.DestroySamplerYcbcrConversion(d.device, kind.conv, nil)
	}
}
