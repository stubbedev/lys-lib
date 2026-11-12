// Package va drives VA-API's encoders without cgo: it loads libva and
// libva-drm at run time through purego, opens a display on a DRM render
// node, and wraps the calls H.264 and H.265 encoding makes. It is the
// gpu encoder's second backend, for GPUs whose Vulkan driver encodes
// no video (Intel Xe2 on Mesa 26).
package va

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"runtime"
	"sync"
	"time"
	"unsafe"

	"github.com/ebitengine/purego"
	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-lib/clib"
	"github.com/stubbedev/lys-lib/intconv"
)

// ErrNoLibrary reports that libva or libva-drm is not installed.
var ErrNoLibrary = errors.New("va: no libva")

// ErrTimeout reports work the driver had not finished within the limit
// a wait gave it.
var ErrTimeout = errors.New("va: the driver did not finish in time")

// lib is libva's entry points, loaded once: their addresses, which a
// Display's calls take.
var lib struct {
	once sync.Once
	err  error

	getDisplayDRM     uintptr
	initialize        uintptr
	terminate         uintptr
	errorStr          uintptr
	queryVendorString uintptr
	maxNumEntrypoints uintptr
	queryEntrypoints  uintptr
	getConfigAttribs  uintptr
	createConfig      uintptr
	destroyConfig     uintptr
	createSurfaces    uintptr
	destroySurfaces   uintptr
	createContext     uintptr
	destroyContext    uintptr
	createBuffer      uintptr
	destroyBuffer     uintptr
	mapBuffer         uintptr
	unmapBuffer       uintptr
	beginPicture      uintptr
	renderPicture     uintptr
	endPicture        uintptr
	syncSurface2      uintptr
	querySurfAttribs  uintptr
	createImage       uintptr
	getImage          uintptr
	destroyImage      uintptr
	putImage          uintptr
	exportSurface     uintptr
}

// load opens libva and libva-drm and resolves the entry points.
func load() error {
	lib.once.Do(func() {
		va, err := purego.Dlopen("libva.so.2", purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			lib.err = fmt.Errorf("%w: %w", ErrNoLibrary, err)
			return
		}
		drm, err := purego.Dlopen("libva-drm.so.2", purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			lib.err = fmt.Errorf("%w: libva-drm: %w", ErrNoLibrary, err)
			return
		}
		for _, f := range []struct {
			handle uintptr
			name   string
			sym    *uintptr
		}{
			{drm, "vaGetDisplayDRM", &lib.getDisplayDRM},
			{va, "vaInitialize", &lib.initialize},
			{va, "vaTerminate", &lib.terminate},
			{va, "vaErrorStr", &lib.errorStr},
			{va, "vaQueryVendorString", &lib.queryVendorString},
			{va, "vaMaxNumEntrypoints", &lib.maxNumEntrypoints},
			{va, "vaQueryConfigEntrypoints", &lib.queryEntrypoints},
			{va, "vaGetConfigAttributes", &lib.getConfigAttribs},
			{va, "vaCreateConfig", &lib.createConfig},
			{va, "vaDestroyConfig", &lib.destroyConfig},
			{va, "vaCreateSurfaces", &lib.createSurfaces},
			{va, "vaDestroySurfaces", &lib.destroySurfaces},
			{va, "vaCreateContext", &lib.createContext},
			{va, "vaDestroyContext", &lib.destroyContext},
			{va, "vaCreateBuffer", &lib.createBuffer},
			{va, "vaDestroyBuffer", &lib.destroyBuffer},
			{va, "vaMapBuffer", &lib.mapBuffer},
			{va, "vaUnmapBuffer", &lib.unmapBuffer},
			{va, "vaBeginPicture", &lib.beginPicture},
			{va, "vaRenderPicture", &lib.renderPicture},
			{va, "vaEndPicture", &lib.endPicture},
			{va, "vaSyncSurface2", &lib.syncSurface2},
			{va, "vaQuerySurfaceAttributes", &lib.querySurfAttribs},
			{va, "vaCreateImage", &lib.createImage},
			{va, "vaGetImage", &lib.getImage},
			{va, "vaDestroyImage", &lib.destroyImage},
			{va, "vaPutImage", &lib.putImage},
			{va, "vaExportSurfaceHandle", &lib.exportSurface},
		} {
			sym, err := purego.Dlsym(f.handle, f.name)
			if err != nil {
				lib.err = fmt.Errorf("%w: %s: %w", ErrNoLibrary, f.name, err)
				return
			}
			*f.sym = sym
		}
	})
	return lib.err
}

type calls struct{ args [11]uintptr }

func (c *calls) syscall(fn uintptr, n int) uintptr {
	r, _, _ := purego.SyscallN(fn, c.args[:n]...)
	return r
}

//go:uintptrescapes
//go:noinline
func (c *calls) call1(fn, a0 uintptr) uintptr {
	c.args[0] = a0
	return c.syscall(fn, 1)
}

//go:uintptrescapes
//go:noinline
func (c *calls) call2(fn, a0, a1 uintptr) uintptr {
	c.args[0], c.args[1] = a0, a1
	return c.syscall(fn, 2)
}

//go:uintptrescapes
//go:noinline
func (c *calls) call3(fn, a0, a1, a2 uintptr) uintptr {
	c.args[0], c.args[1], c.args[2] = a0, a1, a2
	return c.syscall(fn, 3)
}

//go:uintptrescapes
//go:noinline
func (c *calls) call4(fn, a0, a1, a2, a3 uintptr) uintptr {
	c.args[0], c.args[1], c.args[2], c.args[3] = a0, a1, a2, a3
	return c.syscall(fn, 4)
}

//go:uintptrescapes
//go:noinline
func (c *calls) call5(fn, a0, a1, a2, a3, a4 uintptr) uintptr {
	c.args[0], c.args[1], c.args[2], c.args[3], c.args[4] = a0, a1, a2, a3, a4
	return c.syscall(fn, 5)
}

//go:uintptrescapes
//go:noinline
func (c *calls) call6(fn, a0, a1, a2, a3, a4, a5 uintptr) uintptr {
	c.args[0], c.args[1], c.args[2], c.args[3], c.args[4], c.args[5] = a0, a1, a2, a3, a4, a5
	return c.syscall(fn, 6)
}

//go:uintptrescapes
//go:noinline
func (c *calls) call7(fn, a0, a1, a2, a3, a4, a5, a6 uintptr) uintptr {
	c.args[0], c.args[1], c.args[2], c.args[3], c.args[4], c.args[5], c.args[6] = a0, a1, a2, a3, a4, a5, a6
	return c.syscall(fn, 7)
}

//go:uintptrescapes
//go:noinline
func (c *calls) call8(fn, a0, a1, a2, a3, a4, a5, a6, a7 uintptr) uintptr {
	c.args[0], c.args[1], c.args[2], c.args[3], c.args[4], c.args[5], c.args[6], c.args[7] = a0, a1, a2, a3, a4, a5, a6, a7
	return c.syscall(fn, 8)
}

//go:uintptrescapes
//go:noinline
func (c *calls) call11(fn, a0, a1, a2, a3, a4, a5, a6, a7, a8, a9, a10 uintptr) uintptr {
	c.args = [11]uintptr{a0, a1, a2, a3, a4, a5, a6, a7, a8, a9, a10}
	return c.syscall(fn, 11)
}

func cint(v int32) uintptr { return uintptr(intconv.Uint32Bits(v)) }

func status(r uintptr) int32 { return intconv.Int32Bits(uint32(r & math.MaxUint32)) }

func cstring(r uintptr) string { return clib.GoString(*(**byte)(unsafe.Pointer(&r))) }

// Error is a VA call that failed: the call and libva's status.
type Error struct {
	Op     string
	Status int32
}

func (e *Error) Error() string {
	return fmt.Sprintf("va: %s: %s (%d)", e.Op, cstring(new(calls).call1(lib.errorStr, cint(e.Status))), e.Status)
}

func check(op string, status int32) error {
	if status == StatusSuccess {
		return nil
	}
	return &Error{Op: op, Status: status}
}

// Display is an initialized VA display on a DRM render node. Like the
// gpu Device it belongs to one goroutine.
type Display struct {
	calls
	dpy    uintptr
	fd     int
	vendor string
	// misc is the miscellaneous parameter buffer being made, and id and
	// mapped the out-parameters of the calls, kept between calls so
	// that making a buffer, mapping it or reading it allocates nothing.
	misc   []byte
	id     uint32
	mapped unsafe.Pointer
}

// Open opens a VA display on the DRM node at path: ErrNoLibrary
// without libva, an error naming the node when no VA driver serves it.
func Open(path string) (*Display, error) {
	if err := load(); err != nil {
		return nil, err
	}
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("va: open %s: %w", path, err)
	}
	cfd, err := intconv.Exact[int32](fd)
	if err != nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("va: fd of %s: %w", path, err)
	}
	d := &Display{fd: fd}
	if d.dpy = d.call1(lib.getDisplayDRM, cint(cfd)); d.dpy == 0 {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("va: no display on %s", path)
	}
	var major, minor int32
	if err := check("vaInitialize "+path, status(d.call3(lib.initialize, d.dpy, uintptr(unsafe.Pointer(&major)), uintptr(unsafe.Pointer(&minor))))); err != nil {
		_ = unix.Close(fd)
		return nil, err
	}
	d.vendor = cstring(d.call1(lib.queryVendorString, d.dpy))
	return d, nil
}

// Vendor is the driver's vendor string.
func (d *Display) Vendor() string { return d.vendor }

// Close terminates the display and closes its node.
func (d *Display) Close() {
	if d.dpy == 0 {
		return
	}
	_ = d.call1(lib.terminate, d.dpy)
	_ = unix.Close(d.fd)
	d.dpy = 0
}

// Entrypoints are the entrypoints the driver offers for profile; an
// unsupported profile has none.
func (d *Display) Entrypoints(profile int32) []int32 {
	eps := make([]int32, max(status(d.call1(lib.maxNumEntrypoints, d.dpy)), 0))
	var n int32
	if len(eps) == 0 || status(d.call4(lib.queryEntrypoints, d.dpy, cint(profile), uintptr(unsafe.Pointer(&eps[0])), uintptr(unsafe.Pointer(&n)))) != StatusSuccess {
		return nil
	}
	return eps[:n]
}

// ConfigAttributes reads the driver's value for each of attribs'
// types, in place: AttribNotSupported where it has none.
func (d *Display) ConfigAttributes(profile, entrypoint int32, attribs []ConfigAttrib) error {
	n, err := intconv.Exact[int32](len(attribs))
	if err != nil {
		return fmt.Errorf("va: %d config attributes: %w", len(attribs), err)
	}
	return check("vaGetConfigAttributes", status(d.call5(lib.getConfigAttribs, d.dpy, cint(profile), cint(entrypoint), uintptr(unsafe.Pointer(&attribs[0])), cint(n))))
}

// CreateConfig makes an encode configuration.
func (d *Display) CreateConfig(profile, entrypoint int32, attribs []ConfigAttrib) (uint32, error) {
	var p *ConfigAttrib
	if len(attribs) > 0 {
		p = &attribs[0]
	}
	n, err := intconv.Exact[int32](len(attribs))
	if err != nil {
		return 0, fmt.Errorf("va: %d config attributes: %w", len(attribs), err)
	}
	err = check("vaCreateConfig", status(d.call6(lib.createConfig, d.dpy, cint(profile), cint(entrypoint), uintptr(unsafe.Pointer(p)), cint(n), uintptr(unsafe.Pointer(&d.id)))))
	return d.id, err
}

// DestroyConfig releases a configuration.
func (d *Display) DestroyConfig(id uint32) { _ = d.call2(lib.destroyConfig, d.dpy, uintptr(id)) }

// CreateSurfaces makes n driver-allocated surfaces of format.
func (d *Display) CreateSurfaces(format uint32, width, height, n int) ([]uint32, error) {
	w, h, err := size[uint32](width, height)
	if err != nil {
		return nil, err
	}
	count, err := intconv.Exact[uint32](n)
	if err != nil {
		return nil, fmt.Errorf("va: %d surfaces: %w", n, err)
	}
	ids := make([]uint32, n)
	return ids, check("vaCreateSurfaces", status(d.call8(lib.createSurfaces, d.dpy, uintptr(format), uintptr(w), uintptr(h), uintptr(unsafe.Pointer(&ids[0])), uintptr(count), 0, 0)))
}

// ImportSurface wraps a dmabuf desc describes as a surface, with no
// copy: the PRIME 2 import.
func (d *Display) ImportSurface(desc *DRMPRIMESurfaceDescriptor) (uint32, error) {
	var pin runtime.Pinner
	defer pin.Unpin()
	pin.Pin(desc)
	attribs := [2]SurfaceAttrib{
		{Type: SurfaceAttribMemoryType, Flags: SurfaceAttribSettable, Value: GenericValue{Type: GenericValueTypeInteger, Value: uint64(SurfaceAttribMemTypeDRMPrime2)}},
		{Type: SurfaceAttribExternalBufferDescriptor, Flags: SurfaceAttribSettable, Value: GenericValue{Type: GenericValueTypePointer, Value: uint64(uintptr(unsafe.Pointer(desc)))}},
	}
	err := check("vaCreateSurfaces (PRIME 2)", status(d.call8(lib.createSurfaces, d.dpy, uintptr(RTFormatYUV420), uintptr(desc.Width), uintptr(desc.Height), uintptr(unsafe.Pointer(&d.id)), 1, uintptr(unsafe.Pointer(&attribs[0])), 2)))
	return d.id, err
}

// SurfaceSizes are the smallest and largest surfaces config takes,
// zero where the driver does not say.
func (d *Display) SurfaceSizes(config uint32) (minW, minH, maxW, maxH int, err error) {
	var n uint32
	if err := check("vaQuerySurfaceAttributes", status(d.call4(lib.querySurfAttribs, d.dpy, uintptr(config), 0, uintptr(unsafe.Pointer(&n))))); err != nil {
		return 0, 0, 0, 0, err
	}
	attribs := make([]SurfaceAttrib, max(n, 1))
	if err := check("vaQuerySurfaceAttributes", status(d.call4(lib.querySurfAttribs, d.dpy, uintptr(config), uintptr(unsafe.Pointer(&attribs[0])), uintptr(unsafe.Pointer(&n))))); err != nil {
		return 0, 0, 0, 0, err
	}
	for _, a := range attribs[:n] {
		v := int(a.Value.integer())
		switch a.Type {
		case SurfaceAttribMinWidth:
			minW = v
		case SurfaceAttribMinHeight:
			minH = v
		case SurfaceAttribMaxWidth:
			maxW = v
		case SurfaceAttribMaxHeight:
			maxH = v
		}
	}
	return minW, minH, maxW, maxH, nil
}

// ExportSurface describes a surface as a PRIME 2 dmabuf, composed in
// one layer: how the driver lays its own surfaces out. The fds are the
// caller's to close.
func (d *Display) ExportSurface(id uint32) (DRMPRIMESurfaceDescriptor, error) {
	var pin runtime.Pinner
	defer pin.Unpin()
	desc := new(DRMPRIMESurfaceDescriptor)
	pin.Pin(desc)
	err := check("vaExportSurfaceHandle", status(d.call5(lib.exportSurface, d.dpy, uintptr(id), uintptr(SurfaceAttribMemTypeDRMPrime2), uintptr(ExportSurfaceReadOnly|ExportSurfaceComposedLayers), uintptr(unsafe.Pointer(desc)))))
	return *desc, err
}

// nv12Image is a width x height NV12 image and its mapping for the
// CPU; the caller unmaps img.Buf and destroys the image.
func (d *Display) nv12Image(width, height int) (Image, []byte, error) {
	format := ImageFormat{Fourcc: FourccNV12, ByteOrder: 1, BitsPerPixel: 12}
	w, h, err := size[int32](width, height)
	if err != nil {
		return Image{}, nil, err
	}
	var img Image
	if err := check("vaCreateImage", status(d.call5(lib.createImage, d.dpy, uintptr(unsafe.Pointer(&format)), cint(w), cint(h), uintptr(unsafe.Pointer(&img))))); err != nil {
		return Image{}, nil, err
	}
	p, err := d.MapBuffer(img.Buf)
	if err != nil {
		d.destroyImage(img.ImageID)
		return Image{}, nil, err
	}
	return img, unsafe.Slice((*byte)(p), img.DataSize), nil
}

// nv12Rows are the offsets in an NV12 image of its luma rows, then its
// chroma rows.
func nv12Rows(img Image, height int) []int {
	rows := make([]int, 0, height*3/2)
	for y := range height {
		rows = append(rows, int(img.Offsets[0])+y*int(img.Pitches[0]))
	}
	for y := range height / 2 {
		rows = append(rows, int(img.Offsets[1])+y*int(img.Pitches[1]))
	}
	return rows
}

// ReadNV12 copies what an NV12 surface holds: its luma rows, then its
// interleaved chroma rows, tight.
func (d *Display) ReadNV12(id uint32, width, height int) ([]byte, error) {
	img, data, err := d.nv12Image(width, height)
	if err != nil {
		return nil, err
	}
	defer d.destroyImage(img.ImageID)
	w, h, err := size[uint32](width, height)
	if err != nil {
		_ = d.UnmapBuffer(img.Buf)
		return nil, err
	}
	if err := check("vaGetImage", status(d.call7(lib.getImage, d.dpy, uintptr(id), 0, 0, uintptr(w), uintptr(h), uintptr(img.ImageID)))); err != nil {
		_ = d.UnmapBuffer(img.Buf)
		return nil, err
	}
	out := make([]byte, 0, width*height*3/2)
	for _, r := range nv12Rows(img, height) {
		out = append(out, data[r:][:width]...)
	}
	return out, d.UnmapBuffer(img.Buf)
}

// WriteNV12 copies pix (luma rows, then interleaved chroma rows, tight)
// into an NV12 surface.
func (d *Display) WriteNV12(id uint32, width, height int, pix []byte) error {
	img, data, err := d.nv12Image(width, height)
	if err != nil {
		return err
	}
	defer d.destroyImage(img.ImageID)
	for i, r := range nv12Rows(img, height) {
		copy(data[r:][:width], pix[i*width:])
	}
	if err := d.UnmapBuffer(img.Buf); err != nil {
		return err
	}
	w, h, err := size[uint32](width, height)
	if err != nil {
		return err
	}
	return check("vaPutImage", status(d.call11(lib.putImage, d.dpy, uintptr(id), uintptr(img.ImageID), 0, 0, uintptr(w), uintptr(h), 0, 0, uintptr(w), uintptr(h))))
}

func (d *Display) destroyImage(id uint32) { _ = d.call2(lib.destroyImage, d.dpy, uintptr(id)) }

// DestroySurfaces releases surfaces, as many a call as vaDestroySurfaces
// counts.
func (d *Display) DestroySurfaces(ids []uint32) {
	for len(ids) > 0 {
		n := min(len(ids), math.MaxInt32)
		_ = d.call3(lib.destroySurfaces, d.dpy, uintptr(unsafe.Pointer(&ids[0])), uintptr(n))
		ids = ids[n:]
	}
}

// CreateContext makes an encode context for config over targets.
func (d *Display) CreateContext(config uint32, width, height int, targets []uint32) (uint32, error) {
	w, h, err := size[int32](width, height)
	if err != nil {
		return 0, err
	}
	n, err := intconv.Exact[int32](len(targets))
	if err != nil {
		return 0, fmt.Errorf("va: a context over %d surfaces: %w", len(targets), err)
	}
	err = check("vaCreateContext", status(d.call8(lib.createContext, d.dpy, uintptr(config), cint(w), cint(h), 1, uintptr(unsafe.Pointer(&targets[0])), cint(n), uintptr(unsafe.Pointer(&d.id)))))
	return d.id, err
}

// DestroyContext releases a context.
func (d *Display) DestroyContext(id uint32) { _ = d.call2(lib.destroyContext, d.dpy, uintptr(id)) }

// CreateBuffer makes a buffer of typ for context holding size bytes,
// copied from data unless nil.
func (d *Display) CreateBuffer(context uint32, typ int32, size int, data unsafe.Pointer) (uint32, error) {
	n, err := intconv.Exact[uint32](size)
	if err != nil {
		return 0, fmt.Errorf("va: a %d-byte buffer: %w", size, err)
	}
	err = check("vaCreateBuffer", status(d.call7(lib.createBuffer, d.dpy, uintptr(context), cint(typ), uintptr(n), 1, uintptr(data), uintptr(unsafe.Pointer(&d.id)))))
	return d.id, err
}

// CreateMiscBuffer makes a miscellaneous encode parameter buffer for
// context: typ's header, then the size bytes of param.
func (d *Display) CreateMiscBuffer(context uint32, typ int32, param unsafe.Pointer, size int) (uint32, error) {
	d.misc = appendMisc(d.misc[:0], typ, unsafe.Slice((*byte)(param), size))
	return d.CreateBuffer(context, EncMiscParameterBufferType, len(d.misc), unsafe.Pointer(&d.misc[0]))
}

// appendMisc appends a VAEncMiscParameterBuffer to dst: its type, in
// the machine's byte order, then param.
func appendMisc(dst []byte, typ int32, param []byte) []byte {
	dst = binary.NativeEndian.AppendUint32(dst, uint32(typ)) //nolint:gosec // the type's bits, whatever its sign
	return append(dst, param...)
}

// DestroyBuffer releases a buffer.
func (d *Display) DestroyBuffer(id uint32) { _ = d.call2(lib.destroyBuffer, d.dpy, uintptr(id)) }

// MapBuffer maps a buffer for the CPU.
func (d *Display) MapBuffer(id uint32) (unsafe.Pointer, error) {
	err := check("vaMapBuffer", status(d.call3(lib.mapBuffer, d.dpy, uintptr(id), uintptr(unsafe.Pointer(&d.mapped)))))
	return d.mapped, err
}

// UnmapBuffer unmaps a mapped buffer.
func (d *Display) UnmapBuffer(id uint32) error {
	return check("vaUnmapBuffer", status(d.call2(lib.unmapBuffer, d.dpy, uintptr(id))))
}

// ReadCoded appends to dst what a coded buffer holds once its picture
// is done: its segments joined, an error when the picture overflowed
// it. With room in dst it allocates nothing.
func (d *Display) ReadCoded(dst []byte, id uint32) ([]byte, error) {
	p, err := d.MapBuffer(id)
	if err != nil {
		return dst, err
	}
	out, err := appendCoded(dst, (*CodedBufferSegment)(p))
	if err != nil {
		_ = d.UnmapBuffer(id)
		return dst, err
	}
	return out, d.UnmapBuffer(id)
}

// appendCoded appends the bytes of the coded buffer segments from seg
// on to dst, which an error returns as it was.
func appendCoded(dst []byte, seg *CodedBufferSegment) ([]byte, error) {
	out := dst
	for ; seg != nil; seg = (*CodedBufferSegment)(seg.Next) {
		if seg.Status&CodedBufStatusSliceOverflowMask != 0 {
			return dst, fmt.Errorf("va: the picture overflowed its %d-byte coded buffer segment", seg.Size)
		}
		if seg.Size > 0 {
			out = append(out, unsafe.Slice((*byte)(seg.Buf), seg.Size)...)
		}
	}
	return out, nil
}

// Encode renders bufs into a picture of target in context: the
// begin, render, end sequence one frame takes.
func (d *Display) Encode(context, target uint32, bufs []uint32) error {
	if err := check("vaBeginPicture", status(d.call3(lib.beginPicture, d.dpy, uintptr(context), uintptr(target)))); err != nil {
		return err
	}
	n, err := intconv.Exact[int32](len(bufs))
	if err != nil {
		return fmt.Errorf("va: render %d buffers: %w", len(bufs), err)
	}
	if err := check("vaRenderPicture", status(d.call4(lib.renderPicture, d.dpy, uintptr(context), uintptr(unsafe.Pointer(&bufs[0])), cint(n)))); err != nil {
		return err
	}
	return check("vaEndPicture", status(d.call2(lib.endPicture, d.dpy, uintptr(context))))
}

// SyncSurface waits until the work on surface is done, limit at most:
// past it the wait is ErrTimeout instead of a frozen caller. It asks
// the driver whether the surface is busy, without waiting there, and
// sleeps between asking, from pollFirst doubling up to pollMax.
func (d *Display) SyncSurface(id uint32, limit time.Duration) error {
	deadline := time.Now().Add(limit)
	for pause := pollFirst; ; pause = min(2*pause, pollMax) {
		st := status(d.call3(lib.syncSurface2, d.dpy, uintptr(id), 0))
		if st != StatusErrorTimedOut {
			return check("vaSyncSurface2", st)
		}
		left := time.Until(deadline)
		if left <= 0 {
			return fmt.Errorf("%w: surface %d, %v at most", ErrTimeout, id, limit)
		}
		time.Sleep(min(pause, left))
	}
}

const (
	pollFirst = 50 * time.Microsecond
	pollMax   = time.Millisecond
)

// size is a width x height size in the integer type a libva call takes
// it in, or an error naming it when the type cannot hold it.
func size[T int32 | uint32](width, height int) (w, h T, err error) {
	w, errW := intconv.Exact[T](width)
	h, errH := intconv.Exact[T](height)
	if err := errors.Join(errW, errH); err != nil {
		return 0, 0, fmt.Errorf("va: size %dx%d: %w", width, height, err)
	}
	return w, h, nil
}

// integer is the value's integer member: the union's first four bytes,
// where VAGenericValue keeps its int i.
func (v *GenericValue) integer() int32 { return *(*int32)(unsafe.Pointer(&v.Value)) }
