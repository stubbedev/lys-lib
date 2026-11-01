package gpu

import (
	"encoding/binary"
	"fmt"
	"slices"

	"github.com/stubbedev/lys-lib/fourcc"
	"github.com/stubbedev/lys-lib/third_party/vk"
)

type pixelFormat struct {
	vk     vk.Format
	planes int
	opaque bool
	ycbcr  bool
	// swapped marks a YCbCr format that holds Cr where its Vulkan format
	// holds Cb: Vulkan has no format of its own for it, and the
	// conversion's swizzle reads it.
	swapped bool
	// core13 marks a format Vulkan 1.3 added to core.
	core13 bool
	// yuvBits is the depth of a packed 4:4:4 YUV format's samples, which
	// an RGB format holds; view moves them to R (Y), G (Cb) and B (Cr).
	yuvBits int
	view    vk.ComponentMapping
	size    int
}

// The views of packed 4:4:4 formats whose samples are not already in Y,
// Cb, Cr order.
var (
	viewCrCbY = vk.ComponentMapping{R: vk.ComponentSwizzleB, B: vk.ComponentSwizzleR}
	viewCbYCr = vk.ComponentMapping{R: vk.ComponentSwizzleG, G: vk.ComponentSwizzleR}
)

var pixelFormats = map[fourcc.Format]pixelFormat{
	fourcc.ARGB8888:    {vk: vk.FormatB8g8r8a8Unorm, planes: 1, size: 4},
	fourcc.XRGB8888:    {vk: vk.FormatB8g8r8a8Unorm, planes: 1, opaque: true, size: 4},
	fourcc.ABGR8888:    {vk: vk.FormatR8g8b8a8Unorm, planes: 1, size: 4},
	fourcc.XBGR8888:    {vk: vk.FormatR8g8b8a8Unorm, planes: 1, opaque: true, size: 4},
	fourcc.ARGB2101010: {vk: vk.FormatA2r10g10b10UnormPack32, planes: 1, size: 4},
	fourcc.XRGB2101010: {vk: vk.FormatA2r10g10b10UnormPack32, planes: 1, opaque: true, size: 4},
	fourcc.ABGR2101010: {vk: vk.FormatA2b10g10r10UnormPack32, planes: 1, size: 4},
	fourcc.XBGR2101010: {vk: vk.FormatA2b10g10r10UnormPack32, planes: 1, opaque: true, size: 4},
	fourcc.NV12:        {vk: nv12, planes: 2, ycbcr: true},
	fourcc.NV21:        {vk: nv12, planes: 2, ycbcr: true, swapped: true},
	fourcc.NV16:        {vk: vk.FormatG8B8r82plane422Unorm, planes: 2, ycbcr: true},
	fourcc.NV61:        {vk: vk.FormatG8B8r82plane422Unorm, planes: 2, ycbcr: true, swapped: true},
	fourcc.NV24:        {vk: vk.FormatG8B8r82plane444Unorm, planes: 2, ycbcr: true, core13: true},
	fourcc.NV42:        {vk: vk.FormatG8B8r82plane444Unorm, planes: 2, ycbcr: true, swapped: true, core13: true},
	fourcc.P010:        {vk: vk.FormatG10x6B10x6r10x62plane420Unorm3pack16, planes: 2, ycbcr: true},
	fourcc.P012:        {vk: vk.FormatG12x4B12x4r12x42plane420Unorm3pack16, planes: 2, ycbcr: true},
	fourcc.P016:        {vk: vk.FormatG16B16r162plane420Unorm, planes: 2, ycbcr: true},
	fourcc.P210:        {vk: vk.FormatG10x6B10x6r10x62plane422Unorm3pack16, planes: 2, ycbcr: true},
	fourcc.YUV420:      {vk: vk.FormatG8B8R83plane420Unorm, planes: 3, ycbcr: true},
	fourcc.YVU420:      {vk: vk.FormatG8B8R83plane420Unorm, planes: 3, ycbcr: true, swapped: true},
	fourcc.YUV422:      {vk: vk.FormatG8B8R83plane422Unorm, planes: 3, ycbcr: true},
	fourcc.YVU422:      {vk: vk.FormatG8B8R83plane422Unorm, planes: 3, ycbcr: true, swapped: true},
	fourcc.YUV444:      {vk: vk.FormatG8B8R83plane444Unorm, planes: 3, ycbcr: true},
	fourcc.YVU444:      {vk: vk.FormatG8B8R83plane444Unorm, planes: 3, ycbcr: true, swapped: true},
	fourcc.Q410:        {vk: vk.FormatG10x6B10x6R10x63plane444Unorm3pack16, planes: 3, ycbcr: true},
	fourcc.Q401:        {vk: vk.FormatG10x6B10x6R10x63plane444Unorm3pack16, planes: 3, ycbcr: true, swapped: true},
	fourcc.YUYV:        {vk: vk.FormatG8b8g8r8422Unorm, planes: 1, ycbcr: true},
	fourcc.YVYU:        {vk: vk.FormatG8b8g8r8422Unorm, planes: 1, ycbcr: true, swapped: true},
	fourcc.UYVY:        {vk: vk.FormatB8g8r8g8422Unorm, planes: 1, ycbcr: true},
	fourcc.VYUY:        {vk: vk.FormatB8g8r8g8422Unorm, planes: 1, ycbcr: true, swapped: true},
	fourcc.Y210:        {vk: vk.FormatG10x6b10x6g10x6r10x6422Unorm4pack16, planes: 1, ycbcr: true},
	fourcc.Y212:        {vk: vk.FormatG12x4b12x4g12x4r12x4422Unorm4pack16, planes: 1, ycbcr: true},
	fourcc.Y216:        {vk: vk.FormatG16b16g16r16422Unorm, planes: 1, ycbcr: true},

	fourcc.AYUV:         {vk: vk.FormatR8g8b8a8Unorm, planes: 1, yuvBits: 8, view: viewCrCbY},
	fourcc.XYUV8888:     {vk: vk.FormatR8g8b8a8Unorm, planes: 1, yuvBits: 8, view: viewCrCbY, opaque: true},
	fourcc.AVUY8888:     {vk: vk.FormatR8g8b8a8Unorm, planes: 1, yuvBits: 8},
	fourcc.XVUY8888:     {vk: vk.FormatR8g8b8a8Unorm, planes: 1, yuvBits: 8, opaque: true},
	fourcc.VUY888:       {vk: vk.FormatR8g8b8Unorm, planes: 1, yuvBits: 8},
	fourcc.Y410:         {vk: vk.FormatA2b10g10r10UnormPack32, planes: 1, yuvBits: 10, view: viewCbYCr},
	fourcc.XVYU2101010:  {vk: vk.FormatA2b10g10r10UnormPack32, planes: 1, yuvBits: 10, view: viewCbYCr, opaque: true},
	fourcc.XVUY2101010:  {vk: vk.FormatA2b10g10r10UnormPack32, planes: 1, yuvBits: 10, opaque: true},
	fourcc.Y412:         {vk: vk.FormatR16g16b16a16Unorm, planes: 1, yuvBits: 16, view: viewCbYCr},
	fourcc.XVYU121616:   {vk: vk.FormatR16g16b16a16Unorm, planes: 1, yuvBits: 16, view: viewCbYCr, opaque: true},
	fourcc.Y416:         {vk: vk.FormatR16g16b16a16Unorm, planes: 1, yuvBits: 16, view: viewCbYCr},
	fourcc.XVYU16161616: {vk: vk.FormatR16g16b16a16Unorm, planes: 1, yuvBits: 16, view: viewCbYCr, opaque: true},
}

func pixelFormatOf(format fourcc.Format) (pixelFormat, error) {
	pf, ok := pixelFormats[format]
	if !ok {
		return pixelFormat{}, fmt.Errorf("gpu: no Vulkan format for DRM format %s", format)
	}
	return pf, nil
}

var textureFormats = []fourcc.Format{
	fourcc.ARGB8888, fourcc.XRGB8888, fourcc.ABGR8888, fourcc.XBGR8888,
	fourcc.ARGB2101010, fourcc.XRGB2101010, fourcc.ABGR2101010, fourcc.XBGR2101010,
}

// TextureFormats are the formats NewTexture takes.
func (d *Device) TextureFormats() []fourcc.Format { return slices.Clone(textureFormats) }

func (pf pixelFormat) components() vk.ComponentMapping {
	m := pf.view
	if pf.opaque {
		m.A = vk.ComponentSwizzleOne
	}
	return m
}

func decode(vf vk.Format, px [4]byte) (r, g, b, a uint8) {
	switch vf {
	case vk.FormatR8g8b8a8Unorm:
		return px[0], px[1], px[2], px[3]
	case vk.FormatA2r10g10b10UnormPack32, vk.FormatA2b10g10r10UnormPack32:
		v := binary.LittleEndian.Uint32(px[:])
		r, g, b, a = tenToEight(v>>20), tenToEight(v>>10), tenToEight(v), uint8(v>>30)*0x55
		if vf == vk.FormatA2b10g10r10UnormPack32 {
			r, b = b, r
		}
		return r, g, b, a
	}
	return px[2], px[1], px[0], px[3]
}

func tenToEight(v uint32) uint8 {
	return uint8(((v&0x3ff)*0xff + 0x3ff/2) / 0x3ff)
}
