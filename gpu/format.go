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

// lookupPixelFormat is how Vulkan holds a DRM format; false for a
// format it has none for.
func lookupPixelFormat(format fourcc.Format) (pixelFormat, bool) {
	switch format {
	case fourcc.ARGB8888:
		return pixelFormat{vk: vk.FormatB8g8r8a8Unorm, planes: 1, size: 4}, true
	case fourcc.XRGB8888:
		return pixelFormat{vk: vk.FormatB8g8r8a8Unorm, planes: 1, opaque: true, size: 4}, true
	case fourcc.ABGR8888:
		return pixelFormat{vk: vk.FormatR8g8b8a8Unorm, planes: 1, size: 4}, true
	case fourcc.XBGR8888:
		return pixelFormat{vk: vk.FormatR8g8b8a8Unorm, planes: 1, opaque: true, size: 4}, true
	case fourcc.ARGB2101010:
		return pixelFormat{vk: vk.FormatA2r10g10b10UnormPack32, planes: 1, size: 4}, true
	case fourcc.XRGB2101010:
		return pixelFormat{vk: vk.FormatA2r10g10b10UnormPack32, planes: 1, opaque: true, size: 4}, true
	case fourcc.ABGR2101010:
		return pixelFormat{vk: vk.FormatA2b10g10r10UnormPack32, planes: 1, size: 4}, true
	case fourcc.XBGR2101010:
		return pixelFormat{vk: vk.FormatA2b10g10r10UnormPack32, planes: 1, opaque: true, size: 4}, true
	case fourcc.ABGR16161616F:
		return pixelFormat{vk: vk.FormatR16g16b16a16Sfloat, planes: 1, size: 8}, true
	case fourcc.XBGR16161616F:
		return pixelFormat{vk: vk.FormatR16g16b16a16Sfloat, planes: 1, opaque: true, size: 8}, true
	case fourcc.NV12:
		return pixelFormat{vk: nv12, planes: 2, ycbcr: true}, true
	case fourcc.NV21:
		return pixelFormat{vk: nv12, planes: 2, ycbcr: true, swapped: true}, true
	case fourcc.NV16:
		return pixelFormat{vk: vk.FormatG8B8r82plane422Unorm, planes: 2, ycbcr: true}, true
	case fourcc.NV61:
		return pixelFormat{vk: vk.FormatG8B8r82plane422Unorm, planes: 2, ycbcr: true, swapped: true}, true
	case fourcc.NV24:
		return pixelFormat{vk: vk.FormatG8B8r82plane444Unorm, planes: 2, ycbcr: true, core13: true}, true
	case fourcc.NV42:
		return pixelFormat{vk: vk.FormatG8B8r82plane444Unorm, planes: 2, ycbcr: true, swapped: true, core13: true}, true
	case fourcc.P010:
		return pixelFormat{vk: vk.FormatG10x6B10x6r10x62plane420Unorm3pack16, planes: 2, ycbcr: true}, true
	case fourcc.P012:
		return pixelFormat{vk: vk.FormatG12x4B12x4r12x42plane420Unorm3pack16, planes: 2, ycbcr: true}, true
	case fourcc.P016:
		return pixelFormat{vk: vk.FormatG16B16r162plane420Unorm, planes: 2, ycbcr: true}, true
	case fourcc.P210:
		return pixelFormat{vk: vk.FormatG10x6B10x6r10x62plane422Unorm3pack16, planes: 2, ycbcr: true}, true
	case fourcc.YUV420:
		return pixelFormat{vk: vk.FormatG8B8R83plane420Unorm, planes: 3, ycbcr: true}, true
	case fourcc.YVU420:
		return pixelFormat{vk: vk.FormatG8B8R83plane420Unorm, planes: 3, ycbcr: true, swapped: true}, true
	case fourcc.YUV422:
		return pixelFormat{vk: vk.FormatG8B8R83plane422Unorm, planes: 3, ycbcr: true}, true
	case fourcc.YVU422:
		return pixelFormat{vk: vk.FormatG8B8R83plane422Unorm, planes: 3, ycbcr: true, swapped: true}, true
	case fourcc.YUV444:
		return pixelFormat{vk: vk.FormatG8B8R83plane444Unorm, planes: 3, ycbcr: true}, true
	case fourcc.YVU444:
		return pixelFormat{vk: vk.FormatG8B8R83plane444Unorm, planes: 3, ycbcr: true, swapped: true}, true
	case fourcc.Q410:
		return pixelFormat{vk: vk.FormatG10x6B10x6R10x63plane444Unorm3pack16, planes: 3, ycbcr: true}, true
	case fourcc.Q401:
		return pixelFormat{vk: vk.FormatG10x6B10x6R10x63plane444Unorm3pack16, planes: 3, ycbcr: true, swapped: true}, true
	case fourcc.YUYV:
		return pixelFormat{vk: vk.FormatG8b8g8r8422Unorm, planes: 1, ycbcr: true}, true
	case fourcc.YVYU:
		return pixelFormat{vk: vk.FormatG8b8g8r8422Unorm, planes: 1, ycbcr: true, swapped: true}, true
	case fourcc.UYVY:
		return pixelFormat{vk: vk.FormatB8g8r8g8422Unorm, planes: 1, ycbcr: true}, true
	case fourcc.VYUY:
		return pixelFormat{vk: vk.FormatB8g8r8g8422Unorm, planes: 1, ycbcr: true, swapped: true}, true
	case fourcc.Y210:
		return pixelFormat{vk: vk.FormatG10x6b10x6g10x6r10x6422Unorm4pack16, planes: 1, ycbcr: true}, true
	case fourcc.Y212:
		return pixelFormat{vk: vk.FormatG12x4b12x4g12x4r12x4422Unorm4pack16, planes: 1, ycbcr: true}, true
	case fourcc.Y216:
		return pixelFormat{vk: vk.FormatG16b16g16r16422Unorm, planes: 1, ycbcr: true}, true
	case fourcc.AYUV:
		return pixelFormat{vk: vk.FormatR8g8b8a8Unorm, planes: 1, yuvBits: 8, view: viewCrCbY}, true
	case fourcc.XYUV8888:
		return pixelFormat{vk: vk.FormatR8g8b8a8Unorm, planes: 1, yuvBits: 8, view: viewCrCbY, opaque: true}, true
	case fourcc.AVUY8888:
		return pixelFormat{vk: vk.FormatR8g8b8a8Unorm, planes: 1, yuvBits: 8}, true
	case fourcc.XVUY8888:
		return pixelFormat{vk: vk.FormatR8g8b8a8Unorm, planes: 1, yuvBits: 8, opaque: true}, true
	case fourcc.VUY888:
		return pixelFormat{vk: vk.FormatR8g8b8Unorm, planes: 1, yuvBits: 8}, true
	case fourcc.Y410:
		return pixelFormat{vk: vk.FormatA2b10g10r10UnormPack32, planes: 1, yuvBits: 10, view: viewCbYCr}, true
	case fourcc.XVYU2101010:
		return pixelFormat{vk: vk.FormatA2b10g10r10UnormPack32, planes: 1, yuvBits: 10, view: viewCbYCr, opaque: true}, true
	case fourcc.XVUY2101010:
		return pixelFormat{vk: vk.FormatA2b10g10r10UnormPack32, planes: 1, yuvBits: 10, opaque: true}, true
	case fourcc.Y412:
		return pixelFormat{vk: vk.FormatR16g16b16a16Unorm, planes: 1, yuvBits: 16, view: viewCbYCr}, true
	case fourcc.XVYU121616:
		return pixelFormat{vk: vk.FormatR16g16b16a16Unorm, planes: 1, yuvBits: 16, view: viewCbYCr, opaque: true}, true
	case fourcc.Y416:
		return pixelFormat{vk: vk.FormatR16g16b16a16Unorm, planes: 1, yuvBits: 16, view: viewCbYCr}, true
	case fourcc.XVYU16161616:
		return pixelFormat{vk: vk.FormatR16g16b16a16Unorm, planes: 1, yuvBits: 16, view: viewCbYCr, opaque: true}, true
	}
	return pixelFormat{}, false
}

func pixelFormatOf(format fourcc.Format) (pixelFormat, error) {
	pf, ok := lookupPixelFormat(format)
	if !ok {
		return pixelFormat{}, fmt.Errorf("gpu: no Vulkan format for DRM format %s", format)
	}
	return pf, nil
}

var textureFormats = []fourcc.Format{
	fourcc.ARGB8888, fourcc.XRGB8888, fourcc.ABGR8888, fourcc.XBGR8888,
	fourcc.ARGB2101010, fourcc.XRGB2101010, fourcc.ABGR2101010, fourcc.XBGR2101010,
}

// floatFormats are the half-float formats a client's dmabuf may hold: light
// above SDR white, and below zero for colours outside the primaries.
var floatFormats = []fourcc.Format{fourcc.ABGR16161616F, fourcc.XBGR16161616F}

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
