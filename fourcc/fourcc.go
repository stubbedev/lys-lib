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
	ARGB8888    Format = 'A' | 'R'<<8 | '2'<<16 | '4'<<24
	XRGB8888    Format = 'X' | 'R'<<8 | '2'<<16 | '4'<<24
	ABGR8888    Format = 'A' | 'B'<<8 | '2'<<16 | '4'<<24
	XBGR8888    Format = 'X' | 'B'<<8 | '2'<<16 | '4'<<24
	ARGB2101010 Format = 'A' | 'R'<<8 | '3'<<16 | '0'<<24
	XRGB2101010 Format = 'X' | 'R'<<8 | '3'<<16 | '0'<<24
	ABGR2101010 Format = 'A' | 'B'<<8 | '3'<<16 | '0'<<24
	XBGR2101010 Format = 'X' | 'B'<<8 | '3'<<16 | '0'<<24
	NV12        Format = 'N' | 'V'<<8 | '1'<<16 | '2'<<24
	NV21        Format = 'N' | 'V'<<8 | '2'<<16 | '1'<<24
	NV16        Format = 'N' | 'V'<<8 | '1'<<16 | '6'<<24
	P010        Format = 'P' | '0'<<8 | '1'<<16 | '0'<<24
	YUV420      Format = 'Y' | 'U'<<8 | '1'<<16 | '2'<<24
	YVU420      Format = 'Y' | 'V'<<8 | '1'<<16 | '2'<<24

	Linear  Modifier = 0
	Invalid Modifier = 0x00ffffffffffffff
)

var withoutAlpha = map[Format]Format{
	ARGB8888:    XRGB8888,
	ABGR8888:    XBGR8888,
	ARGB2101010: XRGB2101010,
	ABGR2101010: XBGR2101010,
}

// YUV reports whether f is one of the YUV formats named here.
func (f Format) YUV() bool {
	switch f {
	case NV12, NV21, NV16, P010, YUV420, YVU420:
		return true
	}
	return false
}

// HasAlpha reports whether f is one of the RGB formats named here with
// an alpha channel.
func (f Format) HasAlpha() bool {
	_, ok := withoutAlpha[f]
	return ok
}

// Opaque is f with its alpha channel replaced by padding, such as
// XRGB8888 for ARGB8888, or f itself when it has no alpha variant here.
func (f Format) Opaque() Format {
	if x, ok := withoutAlpha[f]; ok {
		return x
	}
	return f
}

// WithAlpha is f with its padding as an alpha channel, such as
// ARGB8888 for XRGB8888, or f itself when it has no opaque variant
// here.
func (f Format) WithAlpha() Format {
	for alpha, opaque := range withoutAlpha {
		if opaque == f {
			return alpha
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
