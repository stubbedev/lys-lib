// Package va drives VA-API's encoders without cgo: it loads libva and
// libva-drm at run time through purego, opens a display on a DRM render
// node, and wraps the calls H.264 and H.265 encoding makes. It is the
// gpu encoder's second backend, for GPUs whose Vulkan driver encodes
// no video (Intel Xe2 on Mesa 26).
package va

import (
	"errors"
	"fmt"
	"runtime"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
	"golang.org/x/sys/unix"
)

// ErrNoLibrary reports that libva or libva-drm is not installed.
var ErrNoLibrary = errors.New("va: no libva")

// lib is libva's entry points, loaded once.
var lib struct {
	once sync.Once
	err  error

	getDisplayDRM     func(fd int32) uintptr
	initialize        func(dpy uintptr, major, minor *int32) int32
	terminate         func(dpy uintptr) int32
	errorStr          func(status int32) string
	queryVendorString func(dpy uintptr) string
	maxNumEntrypoints func(dpy uintptr) int32
	queryEntrypoints  func(dpy uintptr, profile int32, entrypoints *int32, num *int32) int32
	getConfigAttribs  func(dpy uintptr, profile, entrypoint int32, attribs *ConfigAttrib, num int32) int32
	createConfig      func(dpy uintptr, profile, entrypoint int32, attribs *ConfigAttrib, num int32, config *uint32) int32
	destroyConfig     func(dpy uintptr, config uint32) int32
	createSurfaces    func(dpy uintptr, format, width, height uint32, surfaces *uint32, num uint32, attribs *SurfaceAttrib, numAttribs uint32) int32
	destroySurfaces   func(dpy uintptr, surfaces *uint32, num int32) int32
	createContext     func(dpy uintptr, config uint32, width, height, flag int32, targets *uint32, num int32, context *uint32) int32
	destroyContext    func(dpy uintptr, context uint32) int32
	createBuffer      func(dpy uintptr, context uint32, typ int32, size, num uint32, data unsafe.Pointer, buf *uint32) int32
	destroyBuffer     func(dpy uintptr, buf uint32) int32
	mapBuffer         func(dpy uintptr, buf uint32, data *unsafe.Pointer) int32
	unmapBuffer       func(dpy uintptr, buf uint32) int32
	beginPicture      func(dpy uintptr, context, target uint32) int32
	renderPicture     func(dpy uintptr, context uint32, bufs *uint32, num int32) int32
	endPicture        func(dpy uintptr, context uint32) int32
	syncSurface       func(dpy uintptr, surface uint32) int32
	querySurfAttribs  func(dpy uintptr, config uint32, attribs *SurfaceAttrib, num *uint32) int32
	createImage       func(dpy uintptr, format *ImageFormat, width, height int32, image *Image) int32
	getImage          func(dpy uintptr, surface uint32, x, y int32, width, height uint32, image uint32) int32
	destroyImage      func(dpy uintptr, image uint32) int32
	putImage          func(dpy uintptr, surface, image uint32, srcX, srcY int32, srcW, srcH uint32, dstX, dstY int32, dstW, dstH uint32) int32
	exportSurface     func(dpy uintptr, surface, memType, flags uint32, desc unsafe.Pointer) int32
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
			fn     any
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
			{va, "vaSyncSurface", &lib.syncSurface},
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
			purego.RegisterFunc(f.fn, sym)
		}
	})
	return lib.err
}

// Error is a VA call that failed: the call and libva's status.
type Error struct {
	Op     string
	Status int32
}

func (e *Error) Error() string {
	return fmt.Sprintf("va: %s: %s (%d)", e.Op, lib.errorStr(e.Status), e.Status)
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
	dpy    uintptr
	fd     int
	vendor string
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
	dpy := lib.getDisplayDRM(int32(fd))
	if dpy == 0 {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("va: no display on %s", path)
	}
	var major, minor int32
	if err := check("vaInitialize "+path, lib.initialize(dpy, &major, &minor)); err != nil {
		_ = unix.Close(fd)
		return nil, err
	}
	return &Display{dpy: dpy, fd: fd, vendor: lib.queryVendorString(dpy)}, nil
}

// Vendor is the driver's vendor string.
func (d *Display) Vendor() string { return d.vendor }

// Close terminates the display and closes its node.
func (d *Display) Close() {
	if d.dpy == 0 {
		return
	}
	_ = lib.terminate(d.dpy)
	_ = unix.Close(d.fd)
	d.dpy = 0
}

// Entrypoints are the entrypoints the driver offers for profile; an
// unsupported profile has none.
func (d *Display) Entrypoints(profile int32) []int32 {
	eps := make([]int32, lib.maxNumEntrypoints(d.dpy))
	var n int32
	if len(eps) == 0 || lib.queryEntrypoints(d.dpy, profile, &eps[0], &n) != StatusSuccess {
		return nil
	}
	return eps[:n]
}

// ConfigAttributes reads the driver's value for each of attribs'
// types, in place: AttribNotSupported where it has none.
func (d *Display) ConfigAttributes(profile, entrypoint int32, attribs []ConfigAttrib) error {
	return check("vaGetConfigAttributes", lib.getConfigAttribs(d.dpy, profile, entrypoint, &attribs[0], int32(len(attribs))))
}

// CreateConfig makes an encode configuration.
func (d *Display) CreateConfig(profile, entrypoint int32, attribs []ConfigAttrib) (uint32, error) {
	var id uint32
	var p *ConfigAttrib
	if len(attribs) > 0 {
		p = &attribs[0]
	}
	return id, check("vaCreateConfig", lib.createConfig(d.dpy, profile, entrypoint, p, int32(len(attribs)), &id))
}

// DestroyConfig releases a configuration.
func (d *Display) DestroyConfig(id uint32) { _ = lib.destroyConfig(d.dpy, id) }

// CreateSurfaces makes n driver-allocated surfaces of format.
func (d *Display) CreateSurfaces(format uint32, width, height, n int) ([]uint32, error) {
	ids := make([]uint32, n)
	return ids, check("vaCreateSurfaces", lib.createSurfaces(d.dpy, format, uint32(width), uint32(height), &ids[0], uint32(n), nil, 0))
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
	var id uint32
	err := check("vaCreateSurfaces (PRIME 2)", lib.createSurfaces(d.dpy, RTFormatYUV420, desc.Width, desc.Height, &id, 1, &attribs[0], 2))
	return id, err
}

// SurfaceSizes are the smallest and largest surfaces config takes,
// zero where the driver does not say.
func (d *Display) SurfaceSizes(config uint32) (minW, minH, maxW, maxH int, err error) {
	var n uint32
	if err := check("vaQuerySurfaceAttributes", lib.querySurfAttribs(d.dpy, config, nil, &n)); err != nil {
		return 0, 0, 0, 0, err
	}
	attribs := make([]SurfaceAttrib, max(n, 1))
	if err := check("vaQuerySurfaceAttributes", lib.querySurfAttribs(d.dpy, config, &attribs[0], &n)); err != nil {
		return 0, 0, 0, 0, err
	}
	for _, a := range attribs[:n] {
		v := int(int32(a.Value.Value))
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
	err := check("vaExportSurfaceHandle", lib.exportSurface(d.dpy, id, SurfaceAttribMemTypeDRMPrime2, ExportSurfaceReadOnly|ExportSurfaceComposedLayers, unsafe.Pointer(desc)))
	return *desc, err
}

// nv12Image is a width x height NV12 image and its mapping for the
// CPU; the caller unmaps img.Buf and destroys the image.
func (d *Display) nv12Image(width, height int) (Image, []byte, error) {
	format := ImageFormat{Fourcc: FourccNV12, ByteOrder: 1, BitsPerPixel: 12}
	var img Image
	if err := check("vaCreateImage", lib.createImage(d.dpy, &format, int32(width), int32(height), &img)); err != nil {
		return Image{}, nil, err
	}
	p, err := d.MapBuffer(img.Buf)
	if err != nil {
		_ = lib.destroyImage(d.dpy, img.ImageID)
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
	defer lib.destroyImage(d.dpy, img.ImageID)
	if err := check("vaGetImage", lib.getImage(d.dpy, id, 0, 0, uint32(width), uint32(height), img.ImageID)); err != nil {
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
	defer lib.destroyImage(d.dpy, img.ImageID)
	for i, r := range nv12Rows(img, height) {
		copy(data[r:][:width], pix[i*width:])
	}
	if err := d.UnmapBuffer(img.Buf); err != nil {
		return err
	}
	return check("vaPutImage", lib.putImage(d.dpy, id, img.ImageID, 0, 0, uint32(width), uint32(height), 0, 0, uint32(width), uint32(height)))
}

// DestroySurfaces releases surfaces.
func (d *Display) DestroySurfaces(ids []uint32) {
	if len(ids) > 0 {
		_ = lib.destroySurfaces(d.dpy, &ids[0], int32(len(ids)))
	}
}

// CreateContext makes an encode context for config over targets.
func (d *Display) CreateContext(config uint32, width, height int, targets []uint32) (uint32, error) {
	var id uint32
	return id, check("vaCreateContext", lib.createContext(d.dpy, config, int32(width), int32(height), 1, &targets[0], int32(len(targets)), &id))
}

// DestroyContext releases a context.
func (d *Display) DestroyContext(id uint32) { _ = lib.destroyContext(d.dpy, id) }

// CreateBuffer makes a buffer of typ for context holding size bytes,
// copied from data unless nil.
func (d *Display) CreateBuffer(context uint32, typ int32, size int, data unsafe.Pointer) (uint32, error) {
	var id uint32
	return id, check("vaCreateBuffer", lib.createBuffer(d.dpy, context, typ, uint32(size), 1, data, &id))
}

// CreateMiscBuffer makes a miscellaneous encode parameter buffer for
// context: typ's header, then the size bytes of param.
func (d *Display) CreateMiscBuffer(context uint32, typ int32, param unsafe.Pointer, size int) (uint32, error) {
	data := make([]byte, 4+size)
	*(*int32)(unsafe.Pointer(&data[0])) = typ
	copy(data[4:], unsafe.Slice((*byte)(param), size))
	return d.CreateBuffer(context, EncMiscParameterBufferType, len(data), unsafe.Pointer(&data[0]))
}

// DestroyBuffer releases a buffer.
func (d *Display) DestroyBuffer(id uint32) { _ = lib.destroyBuffer(d.dpy, id) }

// MapBuffer maps a buffer for the CPU.
func (d *Display) MapBuffer(id uint32) (unsafe.Pointer, error) {
	var p unsafe.Pointer
	return p, check("vaMapBuffer", lib.mapBuffer(d.dpy, id, &p))
}

// UnmapBuffer unmaps a mapped buffer.
func (d *Display) UnmapBuffer(id uint32) error {
	return check("vaUnmapBuffer", lib.unmapBuffer(d.dpy, id))
}

// ReadCoded copies out what a coded buffer holds once its picture is
// done: its segments joined, an error when the picture overflowed it.
func (d *Display) ReadCoded(id uint32) ([]byte, error) {
	p, err := d.MapBuffer(id)
	if err != nil {
		return nil, err
	}
	var out []byte
	for seg := (*CodedBufferSegment)(p); seg != nil; seg = (*CodedBufferSegment)(seg.Next) {
		if seg.Status&CodedBufStatusSliceOverflowMask != 0 {
			_ = d.UnmapBuffer(id)
			return nil, fmt.Errorf("va: the picture overflowed its %d-byte coded buffer segment", seg.Size)
		}
		if seg.Size > 0 {
			out = append(out, unsafe.Slice((*byte)(seg.Buf), seg.Size)...)
		}
	}
	return out, d.UnmapBuffer(id)
}

// Encode renders bufs into a picture of target in context: the
// begin, render, end sequence one frame takes.
func (d *Display) Encode(context, target uint32, bufs []uint32) error {
	if err := check("vaBeginPicture", lib.beginPicture(d.dpy, context, target)); err != nil {
		return err
	}
	if err := check("vaRenderPicture", lib.renderPicture(d.dpy, context, &bufs[0], int32(len(bufs)))); err != nil {
		return err
	}
	return check("vaEndPicture", lib.endPicture(d.dpy, context))
}

// SyncSurface waits until the work on surface is done.
func (d *Display) SyncSurface(id uint32) error {
	return check("vaSyncSurface", lib.syncSurface(d.dpy, id))
}
