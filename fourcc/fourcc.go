// Package fourcc names DRM pixel formats and format modifiers, as shared by
// the kernel, GBM, EGL and linux-dmabuf.
package fourcc

import (
	"encoding/binary"
	"fmt"
)

// Format is a DRM fourcc pixel format, DRM_FORMAT_*.
type Format uint32

// Modifier is a DRM format modifier, DRM_FORMAT_MOD_*: the buffer's
// tiling and compression layout.
type Modifier uint64

// The pixel formats lys handles, DRM_FORMAT_* by their fourcc codes,
// and the modifiers DRM_FORMAT_MOD_LINEAR and DRM_FORMAT_MOD_INVALID.
const (
	ARGB8888      Format = 'A' | 'R'<<8 | '2'<<16 | '4'<<24
	XRGB8888      Format = 'X' | 'R'<<8 | '2'<<16 | '4'<<24
	ABGR8888      Format = 'A' | 'B'<<8 | '2'<<16 | '4'<<24
	XBGR8888      Format = 'X' | 'B'<<8 | '2'<<16 | '4'<<24
	ARGB2101010   Format = 'A' | 'R'<<8 | '3'<<16 | '0'<<24
	XRGB2101010   Format = 'X' | 'R'<<8 | '3'<<16 | '0'<<24
	ABGR2101010   Format = 'A' | 'B'<<8 | '3'<<16 | '0'<<24
	XBGR2101010   Format = 'X' | 'B'<<8 | '3'<<16 | '0'<<24
	ABGR16161616F Format = 'A' | 'B'<<8 | '4'<<16 | 'H'<<24
	XBGR16161616F Format = 'X' | 'B'<<8 | '4'<<16 | 'H'<<24
	NV12          Format = 'N' | 'V'<<8 | '1'<<16 | '2'<<24
	NV21          Format = 'N' | 'V'<<8 | '2'<<16 | '1'<<24
	NV16          Format = 'N' | 'V'<<8 | '1'<<16 | '6'<<24
	NV61          Format = 'N' | 'V'<<8 | '6'<<16 | '1'<<24
	NV24          Format = 'N' | 'V'<<8 | '2'<<16 | '4'<<24
	NV42          Format = 'N' | 'V'<<8 | '4'<<16 | '2'<<24
	P010          Format = 'P' | '0'<<8 | '1'<<16 | '0'<<24
	P012          Format = 'P' | '0'<<8 | '1'<<16 | '2'<<24
	P016          Format = 'P' | '0'<<8 | '1'<<16 | '6'<<24
	P210          Format = 'P' | '2'<<8 | '1'<<16 | '0'<<24
	YUV420        Format = 'Y' | 'U'<<8 | '1'<<16 | '2'<<24
	YVU420        Format = 'Y' | 'V'<<8 | '1'<<16 | '2'<<24
	YUV422        Format = 'Y' | 'U'<<8 | '1'<<16 | '6'<<24
	YVU422        Format = 'Y' | 'V'<<8 | '1'<<16 | '6'<<24
	YUV444        Format = 'Y' | 'U'<<8 | '2'<<16 | '4'<<24
	YVU444        Format = 'Y' | 'V'<<8 | '2'<<16 | '4'<<24
	Q410          Format = 'Q' | '4'<<8 | '1'<<16 | '0'<<24
	Q401          Format = 'Q' | '4'<<8 | '0'<<16 | '1'<<24
	YUYV          Format = 'Y' | 'U'<<8 | 'Y'<<16 | 'V'<<24
	YVYU          Format = 'Y' | 'V'<<8 | 'Y'<<16 | 'U'<<24
	UYVY          Format = 'U' | 'Y'<<8 | 'V'<<16 | 'Y'<<24
	VYUY          Format = 'V' | 'Y'<<8 | 'U'<<16 | 'Y'<<24
	Y210          Format = 'Y' | '2'<<8 | '1'<<16 | '0'<<24
	Y212          Format = 'Y' | '2'<<8 | '1'<<16 | '2'<<24
	Y216          Format = 'Y' | '2'<<8 | '1'<<16 | '6'<<24
	AYUV          Format = 'A' | 'Y'<<8 | 'U'<<16 | 'V'<<24
	XYUV8888      Format = 'X' | 'Y'<<8 | 'U'<<16 | 'V'<<24
	AVUY8888      Format = 'A' | 'V'<<8 | 'U'<<16 | 'Y'<<24
	XVUY8888      Format = 'X' | 'V'<<8 | 'U'<<16 | 'Y'<<24
	Y410          Format = 'Y' | '4'<<8 | '1'<<16 | '0'<<24
	XVYU2101010   Format = 'X' | 'V'<<8 | '3'<<16 | '0'<<24
	Y412          Format = 'Y' | '4'<<8 | '1'<<16 | '2'<<24
	XVYU121616    Format = 'X' | 'V'<<8 | '3'<<16 | '6'<<24
	Y416          Format = 'Y' | '4'<<8 | '1'<<16 | '6'<<24
	XVYU16161616  Format = 'X' | 'V'<<8 | '4'<<16 | '8'<<24
	XVUY2101010   Format = 'X' | 'Y'<<8 | '3'<<16 | '0'<<24
	VUY888        Format = 'V' | 'U'<<8 | '2'<<16 | '4'<<24

	Linear  Modifier = 0
	Invalid Modifier = 0x00ffffffffffffff
)

var alphaVariants = [...]struct{ alpha, opaque Format }{
	{ARGB8888, XRGB8888},
	{ABGR8888, XBGR8888},
	{ARGB2101010, XRGB2101010},
	{ABGR2101010, XBGR2101010},
}

var (
	packedRows           = []int{1}
	fullChromaRows       = []int{1, 1}
	halfChromaRows       = []int{1, 2}
	fullChromaPlanarRows = []int{1, 1, 1}
	halfChromaPlanarRows = []int{1, 2, 2}
)

// yuvPlanes is a YUV format's color planes, as how many luma rows share
// one row of the plane; nil for a format that is not YUV.
func yuvPlanes(f Format) []int {
	switch f {
	case NV12, NV21, P010, P012, P016:
		return halfChromaRows
	case NV16, NV61, NV24, NV42, P210:
		return fullChromaRows
	case YUV420, YVU420:
		return halfChromaPlanarRows
	case YUV422, YVU422, YUV444, YVU444, Q410, Q401:
		return fullChromaPlanarRows
	case YUYV, YVYU, UYVY, VYUY, Y210, Y212, Y216,
		AYUV, XYUV8888, AVUY8888, XVUY8888,
		Y410, XVYU2101010, Y412, XVYU121616, Y416, XVYU16161616,
		XVUY2101010, VUY888:
		return packedRows
	}
	return nil
}

// YUV reports whether f is one of the YUV formats named here.
func (f Format) YUV() bool { return yuvPlanes(f) != nil }

// Planes is how many color planes a buffer of f has, or 0 when f is not
// named here. A modifier may add metadata planes beyond them.
func (f Format) Planes() int {
	for _, v := range alphaVariants {
		if v.alpha == f || v.opaque == f {
			return 1
		}
	}
	return len(yuvPlanes(f))
}

// PlaneRows is how many rows plane has in a buffer of f height rows
// tall: height for a plane that is not subsampled, a metadata plane or a
// format not named here.
func (f Format) PlaneRows(plane, height int) int {
	subs := yuvPlanes(f)
	if plane >= len(subs) {
		return height
	}
	return (height + subs[plane] - 1) / subs[plane]
}

// HasAlpha reports whether f is one of the RGB formats named here with
// an alpha channel.
func (f Format) HasAlpha() bool { return f.Opaque() != f }

// Opaque is f with its alpha channel replaced by padding, such as
// XRGB8888 for ARGB8888, or f itself when it has no alpha variant here.
func (f Format) Opaque() Format {
	for _, v := range alphaVariants {
		if v.alpha == f {
			return v.opaque
		}
	}
	return f
}

// WithAlpha is f with its padding as an alpha channel, such as
// ARGB8888 for XRGB8888, or f itself when it has no opaque variant
// here.
func (f Format) WithAlpha() Format {
	for _, v := range alphaVariants {
		if v.opaque == f {
			return v.alpha
		}
	}
	return f
}

// String is f's four characters, such as "XR24", or its hex value
// when they are not printable.
func (f Format) String() string {
	var b [4]byte
	binary.LittleEndian.PutUint32(b[:], uint32(f))
	for _, c := range b {
		if c < ' ' || c > '~' {
			return fmt.Sprintf("%#08x", uint32(f))
		}
	}
	return string(b[:])
}

// String is "linear", "invalid" or m's hex value.
func (m Modifier) String() string {
	switch m {
	case Linear:
		return "linear"
	case Invalid:
		return "invalid"
	}
	return fmt.Sprintf("%#016x", uint64(m))
}
