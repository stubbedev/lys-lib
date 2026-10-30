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
	size   int
}

var pixelFormats = map[fourcc.Format]pixelFormat{
	fourcc.ARGB8888:    {vk: vk.FormatB8g8r8a8Unorm, planes: 1, size: 4},
	fourcc.XRGB8888:    {vk: vk.FormatB8g8r8a8Unorm, planes: 1, opaque: true, size: 4},
	fourcc.ABGR8888:    {vk: vk.FormatR8g8b8a8Unorm, planes: 1, size: 4},
	fourcc.XBGR8888:    {vk: vk.FormatR8g8b8a8Unorm, planes: 1, opaque: true, size: 4},
	fourcc.ARGB2101010: {vk: vk.FormatA2r10g10b10UnormPack32, planes: 1, size: 4},
	fourcc.XRGB2101010: {vk: vk.FormatA2r10g10b10UnormPack32, planes: 1, opaque: true, size: 4},
	fourcc.ABGR2101010: {vk: vk.FormatA2b10g10r10UnormPack32, planes: 1, size: 4},
	fourcc.XBGR2101010: {vk: vk.FormatA2b10g10r10UnormPack32, planes: 1, opaque: true, size: 4},
	fourcc.NV12:        {vk: nv12, planes: 2, ycbcr: true, size: 1},
	fourcc.P010:        {vk: vk.FormatG10x6B10x6r10x62plane420Unorm3pack16, planes: 2, ycbcr: true, size: 2},
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
	if pf.opaque {
		return vk.ComponentMapping{A: vk.ComponentSwizzleOne}
	}
	return vk.ComponentMapping{}
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
