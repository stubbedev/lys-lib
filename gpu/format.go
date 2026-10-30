package gpu

import (
	"fmt"
	"slices"

	"github.com/stubbedev/lys-lib/fourcc"
	"github.com/stubbedev/lys-lib/third_party/vk"
)

type pixelFormat struct {
	vk     vk.Format
	planes int
	opaque bool
	size   int
}

var pixelFormats = map[fourcc.Format]pixelFormat{
	fourcc.ARGB8888: {vk: vk.FormatB8g8r8a8Unorm, planes: 1, size: 4},
	fourcc.XRGB8888: {vk: vk.FormatB8g8r8a8Unorm, planes: 1, opaque: true, size: 4},
	fourcc.ABGR8888: {vk: vk.FormatR8g8b8a8Unorm, planes: 1, size: 4},
	fourcc.XBGR8888: {vk: vk.FormatR8g8b8a8Unorm, planes: 1, opaque: true, size: 4},
	fourcc.NV12:     {vk: nv12, planes: 2, size: 1},
}

func pixelFormatOf(format fourcc.Format) (pixelFormat, error) {
	pf, ok := pixelFormats[format]
	if !ok {
		return pixelFormat{}, fmt.Errorf("gpu: no Vulkan format for DRM format %s", format)
	}
	return pf, nil
}

var textureFormats = []fourcc.Format{fourcc.ARGB8888, fourcc.XRGB8888, fourcc.ABGR8888, fourcc.XBGR8888}

// TextureFormats are the formats NewTexture takes.
func (d *Device) TextureFormats() []fourcc.Format { return slices.Clone(textureFormats) }

func (pf pixelFormat) components() vk.ComponentMapping {
	if pf.opaque {
		return vk.ComponentMapping{A: vk.ComponentSwizzleOne}
	}
	return vk.ComponentMapping{}
}
