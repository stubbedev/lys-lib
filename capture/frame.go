package capture

import (
	"encoding/binary"
	"errors"
	"fmt"
	"image"

	"github.com/stubbedev/lys-lib/fourcc"
	"github.com/stubbedev/lys-lib/intconv"
	"github.com/stubbedev/lys-lib/third_party/wayland/wl"
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

// FormatFromFourcc maps a DRM format (a dmabuf's) to the wl_shm
// format code of the same memory layout.
func FormatFromFourcc(drm fourcc.Format) Format {
	switch drm {
	case fourcc.ARGB8888:
		return FormatARGB8888
	case fourcc.XRGB8888:
		return FormatXRGB8888
	}
	return Format(drm)
}

// Fourcc is the DRM format of the format, the inverse of
// FormatFromFourcc.
func (f Format) Fourcc() fourcc.Format {
	switch f {
	case FormatARGB8888:
		return fourcc.ARGB8888
	case FormatXRGB8888:
		return fourcc.XRGB8888
	}
	return fourcc.Format(f)
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
	s := binary.LittleEndian.AppendUint32(nil, uint32(f.Fourcc()))
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

// Image converts the frame to a new RGBA image in presentation row
// order (YInvert undone; the Transform is left to the caller, who knows
// whether it wants buffer or presentation orientation). Formats
// without alpha convert fully opaque; alpha formats are premultiplied
// already, which is what image.RGBA stores.
func (f *Frame) Image() (*image.RGBA, error) {
	if err := f.check(); err != nil {
		return nil, err
	}
	img := image.NewRGBA(image.Rect(0, 0, f.Width, f.Height))
	f.convert(img)
	return img, nil
}

// ImageInto converts the frame into dst as Image does, for a caller
// that owns the pixels' memory (reused across frames, or off the Go
// heap). dst must be exactly the frame's size; its stride and origin
// are its own.
func (f *Frame) ImageInto(dst *image.RGBA) error {
	if err := f.check(); err != nil {
		return err
	}
	if dst.Rect.Dx() != f.Width || dst.Rect.Dy() != f.Height {
		return fmt.Errorf("capture: converting a %dx%d frame into a %dx%d image", f.Width, f.Height, dst.Rect.Dx(), dst.Rect.Dy())
	}
	f.convert(dst)
	return nil
}

// convert writes a checked frame's pixels into dst, which is its size.
func (f *Frame) convert(dst *image.RGBA) {
	bpp := f.Format.BytesPerPixel()
	row := f.Width * bpp
	for y := range f.Height {
		srcY := y
		if f.YInvert {
			srcY = f.Height - 1 - y
		}
		src := f.Data[srcY*f.Stride : srcY*f.Stride+row]
		at := dst.PixOffset(dst.Rect.Min.X, dst.Rect.Min.Y+y)
		out := dst.Pix[at : at+f.Width*4]
		if bpp == 3 {
			convertRow24(out, src, f.Format)
		} else {
			convertRow32(out, src, f.Format)
		}
	}
}

// check reports a frame whose format or geometry its data cannot hold.
func (f *Frame) check() error {
	bpp := f.Format.BytesPerPixel()
	if bpp == 0 {
		return fmt.Errorf("%w: %s", ErrFormat, f.Format)
	}
	row := f.Width * bpp
	if f.Width <= 0 || f.Height <= 0 || f.Stride < row || len(f.Data) < f.Stride*(f.Height-1)+row {
		return fmt.Errorf("capture: frame %dx%d stride %d does not fit %d bytes", f.Width, f.Height, f.Stride, len(f.Data))
	}
	return nil
}

func convertRow32(dst, src []byte, f Format) {
	var opaque uint32
	if !f.hasAlpha() {
		opaque = 0xff << 24
	}
	switch f {
	case FormatABGR8888:
		copy(dst, src)
	case FormatXBGR8888:
		for len(src) >= 4 && len(dst) >= 4 {
			binary.LittleEndian.PutUint32(dst, binary.LittleEndian.Uint32(src)|opaque)
			dst, src = dst[4:], src[4:]
		}
	case FormatARGB8888, FormatXRGB8888:
		opaque2 := uint64(opaque)<<32 | uint64(opaque)
		for len(src) >= 8 && len(dst) >= 8 {
			binary.LittleEndian.PutUint64(dst, swapRB(binary.LittleEndian.Uint64(src))|opaque2)
			dst, src = dst[8:], src[8:]
		}
		if len(src) >= 4 && len(dst) >= 4 {
			binary.LittleEndian.PutUint32(dst, intconv.Must[uint32](swapRB(uint64(binary.LittleEndian.Uint32(src))))|opaque)
		}
	case FormatARGB2101010, FormatXRGB2101010:
		for len(src) >= 4 && len(dst) >= 4 {
			px := binary.LittleEndian.Uint32(src)
			binary.LittleEndian.PutUint32(dst, rgba(ten(px>>20), ten(px>>10), ten(px), two(px>>30))|opaque)
			dst, src = dst[4:], src[4:]
		}
	case FormatABGR2101010, FormatXBGR2101010:
		for len(src) >= 4 && len(dst) >= 4 {
			px := binary.LittleEndian.Uint32(src)
			binary.LittleEndian.PutUint32(dst, rgba(ten(px), ten(px>>10), ten(px>>20), two(px>>30))|opaque)
			dst, src = dst[4:], src[4:]
		}
	}
}

func swapRB(px uint64) uint64 {
	const rb, ga = 0x000000ff_000000ff, 0xff00ff00_ff00ff00
	return px>>16&rb | px&ga | px&rb<<16
}

func rgba(r, g, b, a uint8) uint32 {
	return uint32(r) | uint32(g)<<8 | uint32(b)<<16 | uint32(a)<<24
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
