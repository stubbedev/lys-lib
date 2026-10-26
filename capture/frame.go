package capture

import (
	"errors"
	"fmt"
	"image"

	"github.com/neurlang/wayland/wl"
)

// Format is a wl_shm pixel format code (the wl_shm.format enum: DRM
// fourcc values, except the two legacy codes for ARGB8888 and
// XRGB8888).
type Format uint32

// The formats a Frame converts to an image: the packed 32-bit formats
// (8-bit channels, and the 2:10:10:10 variants 10-bit outputs hand
// out) and the 24-bit ones GPU renderers offer; all little endian.
const (
	FormatARGB8888    Format = wl.ShmFormatArgb8888
	FormatXRGB8888    Format = wl.ShmFormatXrgb8888
	FormatABGR8888    Format = wl.ShmFormatAbgr8888
	FormatXBGR8888    Format = wl.ShmFormatXbgr8888
	FormatARGB2101010 Format = wl.ShmFormatArgb2101010
	FormatXRGB2101010 Format = wl.ShmFormatXrgb2101010
	FormatABGR2101010 Format = wl.ShmFormatAbgr2101010
	FormatXBGR2101010 Format = wl.ShmFormatXbgr2101010
	FormatRGB888      Format = wl.ShmFormatRgb888
	FormatBGR888      Format = wl.ShmFormatBgr888
)

// DRM fourccs of the two formats wl_shm spells with legacy codes.
const (
	fourccARGB8888 = 0x34325241 // 'AR24'
	fourccXRGB8888 = 0x34325258 // 'XR24'
)

// FormatFromFourcc maps a DRM fourcc (a dmabuf format) to the wl_shm
// format code of the same memory layout.
func FormatFromFourcc(fourcc uint32) Format {
	switch fourcc {
	case fourccARGB8888:
		return FormatARGB8888
	case fourccXRGB8888:
		return FormatXRGB8888
	}
	return Format(fourcc)
}

// Fourcc is the DRM fourcc of the format, the inverse of
// FormatFromFourcc.
func (f Format) Fourcc() uint32 {
	switch f {
	case FormatARGB8888:
		return fourccARGB8888
	case FormatXRGB8888:
		return fourccXRGB8888
	}
	return uint32(f)
}

// BytesPerPixel is the pixel size of a supported format, 0 for the
// rest.
func (f Format) BytesPerPixel() int {
	switch f {
	case FormatARGB8888, FormatXRGB8888, FormatABGR8888, FormatXBGR8888,
		FormatARGB2101010, FormatXRGB2101010, FormatABGR2101010, FormatXBGR2101010:
		return 4
	case FormatRGB888, FormatBGR888:
		return 3
	}
	return 0
}

// Supported reports whether Frame.Image can convert the format.
func (f Format) Supported() bool { return f.BytesPerPixel() != 0 }

// hasAlpha reports whether the format's top bits carry alpha rather
// than padding.
func (f Format) hasAlpha() bool {
	switch f {
	case FormatARGB8888, FormatABGR8888, FormatARGB2101010, FormatABGR2101010:
		return true
	}
	return false
}

func (f Format) String() string {
	b := f.Fourcc()
	s := []byte{byte(b), byte(b >> 8), byte(b >> 16), byte(b >> 24)}
	for _, c := range s {
		if c < 0x20 || c > 0x7e {
			return fmt.Sprintf("format(%#x)", uint32(f))
		}
	}
	return string(s)
}

// ErrFormat reports a frame in a pixel format Image cannot convert.
var ErrFormat = errors.New("capture: unsupported pixel format")

// Transform is a wl_output.transform value: how the compositor rotates
// (counter-clockwise, in 90 degree steps) and flips buffer contents
// when presenting them.
type Transform uint32

// The eight wl_output transforms.
const (
	TransformNormal     Transform = wl.OutputTransformNormal
	Transform90         Transform = wl.OutputTransform90
	Transform180        Transform = wl.OutputTransform180
	Transform270        Transform = wl.OutputTransform270
	TransformFlipped    Transform = wl.OutputTransformFlipped
	TransformFlipped90  Transform = wl.OutputTransformFlipped90
	TransformFlipped180 Transform = wl.OutputTransformFlipped180
	TransformFlipped270 Transform = wl.OutputTransformFlipped270
)

// SwapsAxes reports whether the transform exchanges width and height
// (the 90 and 270 degree variants).
func (t Transform) SwapsAxes() bool {
	switch t {
	case Transform90, Transform270, TransformFlipped90, TransformFlipped270:
		return true
	}
	return false
}

// Rect is a damaged region in buffer pixels.
type Rect struct {
	X, Y, Width, Height int
}

// Frame is one captured image, copied out of the shared memory buffer
// the compositor wrote: Height rows of Stride bytes in Format.
type Frame struct {
	Width, Height int
	Stride        int
	Format        Format
	Data          []byte
	// YInvert is set when the rows arrive bottom-up (the screencopy
	// y_invert flag); Image flips them back.
	YInvert bool
	// Transform is the buffer transform the compositor reported for
	// the frame (ext-image-copy-capture) or TransformNormal.
	Transform Transform
	// Damage lists the regions that changed since the previous frame
	// of the same source, when the compositor reported any; empty
	// means treat the whole frame as damaged.
	Damage []Rect
}

// Image converts the frame to an RGBA image in presentation row order
// (YInvert undone; the Transform is left to the caller, who knows
// whether it wants buffer or presentation orientation). Formats
// without alpha convert fully opaque; alpha formats are premultiplied
// already, which is what image.RGBA stores.
func (f *Frame) Image() (*image.RGBA, error) {
	bpp := f.Format.BytesPerPixel()
	if bpp == 0 {
		return nil, fmt.Errorf("%w: %s", ErrFormat, f.Format)
	}
	row := f.Width * bpp
	if f.Width <= 0 || f.Height <= 0 || f.Stride < row || len(f.Data) < f.Stride*(f.Height-1)+row {
		return nil, fmt.Errorf("capture: frame %dx%d stride %d does not fit %d bytes", f.Width, f.Height, f.Stride, len(f.Data))
	}
	img := image.NewRGBA(image.Rect(0, 0, f.Width, f.Height))
	for y := range f.Height {
		srcY := y
		if f.YInvert {
			srcY = f.Height - 1 - y
		}
		src := f.Data[srcY*f.Stride : srcY*f.Stride+row]
		dst := img.Pix[y*img.Stride : y*img.Stride+f.Width*4]
		if bpp == 3 {
			convertRow24(dst, src, f.Format)
		} else {
			convertRow32(dst, src, f.Format)
		}
	}
	return img, nil
}

// convertRow32 unpacks one row of 32-bit little-endian pixels into
// RGBA bytes.
func convertRow32(dst, src []byte, f Format) {
	alpha := f.hasAlpha()
	for i := 0; i+3 < len(src); i += 4 {
		px := uint32(src[i]) | uint32(src[i+1])<<8 | uint32(src[i+2])<<16 | uint32(src[i+3])<<24
		var r, g, b, a uint8
		switch f {
		case FormatARGB8888, FormatXRGB8888:
			r, g, b, a = uint8(px>>16), uint8(px>>8), uint8(px), uint8(px>>24)
		case FormatABGR8888, FormatXBGR8888:
			r, g, b, a = uint8(px), uint8(px>>8), uint8(px>>16), uint8(px>>24)
		case FormatARGB2101010, FormatXRGB2101010:
			r, g, b, a = ten(px>>20), ten(px>>10), ten(px), two(px>>30)
		case FormatABGR2101010, FormatXBGR2101010:
			r, g, b, a = ten(px), ten(px>>10), ten(px>>20), two(px>>30)
		}
		if !alpha {
			a = 0xff
		}
		dst[i], dst[i+1], dst[i+2], dst[i+3] = r, g, b, a
	}
}

// convertRow24 unpacks one row of 24-bit little-endian pixels into
// opaque RGBA bytes: RGB888 ([23:0] R:G:B) sits in memory as B, G, R;
// BGR888 as R, G, B.
func convertRow24(dst, src []byte, f Format) {
	for i, j := 0, 0; i+2 < len(src); i, j = i+3, j+4 {
		if f == FormatRGB888 {
			dst[j], dst[j+1], dst[j+2] = src[i+2], src[i+1], src[i]
		} else {
			dst[j], dst[j+1], dst[j+2] = src[i], src[i+1], src[i+2]
		}
		dst[j+3] = 0xff
	}
}

// ten narrows a 10-bit channel (the low bits of v) to 8 bits.
func ten(v uint32) uint8 { return uint8((v & 0x3ff) >> 2) }

// two widens a 2-bit alpha channel to 8 bits.
func two(v uint32) uint8 { return uint8((v & 0x3) * 0x55) }
