// Package fourcc names DRM pixel formats and format modifiers, as shared by
// the kernel, GBM, EGL and linux-dmabuf.
package fourcc

import (
	"encoding/binary"
	"fmt"
)

type Format uint32

type Modifier uint64

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

func (f Format) YUV() bool {
	switch f {
	case NV12, NV21, NV16, P010, YUV420, YVU420:
		return true
	}
	return false
}

func (f Format) HasAlpha() bool {
	_, ok := withoutAlpha[f]
	return ok
}

func (f Format) Opaque() Format {
	if x, ok := withoutAlpha[f]; ok {
		return x
	}
	return f
}

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

func (m Modifier) String() string {
	switch m {
	case Linear:
		return "linear"
	case Invalid:
		return "invalid"
	}
	return fmt.Sprintf("%#016x", uint64(m))
}
