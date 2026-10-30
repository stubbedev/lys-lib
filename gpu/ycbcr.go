package gpu

import (
	"fmt"
	"runtime"
	"unsafe"

	"github.com/stubbedev/lys-lib/fourcc"
	"github.com/stubbedev/lys-lib/third_party/vk"
)

var ycbcrFormats = []fourcc.Format{fourcc.NV12, fourcc.P010}

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

func (c YCbCr) key(vf vk.Format, features vk.FormatFeatureFlags) (ycbcrKey, error) {
	k := ycbcrKey{vf: vf, filter: vk.FilterNearest}
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

func (d *Device) ycbcrKindOf(vf vk.Format, c YCbCr, features vk.FormatFeatureFlags) (*ycbcrKind, error) {
	k, err := c.key(vf, features)
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
	if r := d.cmds.CreateSamplerYcbcrConversion(d.device, &cci, nil, &kind.conv); r != vk.Success {
		return fmt.Errorf("gpu: vkCreateSamplerYcbcrConversion: %d", r)
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
		return fmt.Errorf("gpu: vkCreateSampler (YCbCr): %d", r)
	}
	return d.samplerLayouts(&kind.sampler, 3, &kind.sets, &kind.layout)
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
