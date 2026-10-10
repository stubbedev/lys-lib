// Package dmabuf describes Linux dma-buf frames: GPU (or udmabuf)
// memory handed to the compositor by file descriptor through
// zwp_linux_dmabuf_v1, with no copy. capture fills them with screen
// contents; widget.GPUArea presents them.
package dmabuf

import "fmt"

// DRM fourcc formats and modifiers gelm names; any other DRM code
// works the same way.
const (
	// FormatXRGB8888 is DRM_FORMAT_XRGB8888 ('XR24').
	FormatXRGB8888 uint32 = 0x34325258
	// FormatARGB8888 is DRM_FORMAT_ARGB8888 ('AR24').
	FormatARGB8888 uint32 = 0x34325241
	// ModifierLinear is DRM_FORMAT_MOD_LINEAR.
	ModifierLinear uint64 = 0
	// ModifierInvalid is DRM_FORMAT_MOD_INVALID: an implicit,
	// driver-chosen layout.
	ModifierInvalid uint64 = 0x00ffffffffffffff
)

const maxPlanes = 4

// Plane is one plane of a dmabuf: its file descriptor and the plane's
// byte offset and stride within it.
type Plane struct {
	FD     int
	Offset uint32
	Stride uint32
}

// Buffer describes a dmabuf. The caller allocates it (gbm, a DRM dumb
// buffer, udmabuf) and keeps the plane fds open for as long as the
// compositor-side buffer lives; importing dups nothing. The planes must
// be the ones Format calls for.
type Buffer struct {
	Width, Height int
	// Format is the DRM fourcc.
	Format uint32
	// Modifier is the DRM format modifier.
	Modifier uint64
	Planes   []Plane
}

// Validate reports a buffer the compositor would reject as a protocol
// error: a non-positive size, no planes or more than four, or a plane
// without a stride or fd.
func (b Buffer) Validate() error {
	if b.Width <= 0 || b.Height <= 0 {
		return fmt.Errorf("dmabuf: size %dx%d is not positive", b.Width, b.Height)
	}
	if len(b.Planes) == 0 || len(b.Planes) > maxPlanes {
		return fmt.Errorf("dmabuf: %d planes, want 1 to %d", len(b.Planes), maxPlanes)
	}
	for i, p := range b.Planes {
		if p.FD < 0 || p.Stride == 0 {
			return fmt.Errorf("dmabuf: plane %d has fd %d and stride %d", i, p.FD, p.Stride)
		}
	}
	return nil
}
