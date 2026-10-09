package capture

import (
	"errors"
	"fmt"
	"math"
	"os"

	wlos "github.com/stubbedev/gelm/third_party/neurlang-wayland/os"
	"github.com/stubbedev/gelm/third_party/neurlang-wayland/wl"
)

// shmBuffer is one memfd-backed wl_buffer the compositor copies a
// frame into: the file, the client's mapping of it, and the buffer
// object carved from a single-buffer pool.
type shmBuffer struct {
	wl     *wl.Buffer
	file   *os.File
	data   []byte
	width  int
	height int
	stride int
	format Format
}

// newShmBuffer allocates a width x height buffer of stride-byte rows.
// The pool is sized stride*height, not width*4*height: compositors pad
// strides, and a pool sized to the unpadded width is too small for the
// wl_buffer created with the padded stride (a protocol error).
func newShmBuffer(shm *wl.Shm, width, height, stride int, format Format) (*shmBuffer, error) {
	if shm == nil {
		return nil, fmt.Errorf("%w: %s", ErrUnsupported, ifaceShm)
	}
	if width <= 0 || height <= 0 || stride < width*max(format.BytesPerPixel(), 1) {
		return nil, fmt.Errorf("capture: bad buffer geometry %dx%d stride %d", width, height, stride)
	}
	size := stride * height
	if size > math.MaxInt32 {
		return nil, fmt.Errorf("capture: %dx%d buffer exceeds the wayland int32 range", width, height)
	}
	file, err := wlos.CreateAnonymousFile(int64(size))
	if err != nil {
		return nil, fmt.Errorf("capture: memfd: %w", err)
	}
	data, err := wlos.Mmap(int(file.Fd()), 0, size, wlos.ProtRead|wlos.ProtWrite, wlos.MapShared)
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("capture: mmap: %w", err)
	}
	b := &shmBuffer{file: file, data: data, width: width, height: height, stride: stride, format: format}
	pool, err := shm.CreatePool(file.Fd(), int32(size))
	if err != nil {
		b.unmap()
		return nil, fmt.Errorf("capture: wl_shm.create_pool: %w", err)
	}
	buf, err := pool.CreateBuffer(0, int32(width), int32(height), int32(stride), uint32(format))
	// The buffer keeps the pool's memory alive compositor-side; the
	// pool object itself is not needed past this point.
	_ = pool.Destroy()
	if err != nil {
		b.unmap()
		return nil, fmt.Errorf("capture: wl_shm_pool.create_buffer: %w", err)
	}
	b.wl = buf
	return b, nil
}

// matches reports whether the buffer can take a frame of this
// geometry and format.
func (b *shmBuffer) matches(width, height, stride int, format Format) bool {
	return b.width == width && b.height == height && b.stride == stride && b.format == format
}

// frame copies the buffer's pixels into a Frame. dst is reused when it
// is large enough.
func (b *shmBuffer) frame(dst []byte) *Frame {
	n := b.stride * b.height
	if cap(dst) < n {
		dst = make([]byte, n)
	}
	dst = dst[:n]
	copy(dst, b.data)
	return &Frame{Width: b.width, Height: b.height, Stride: b.stride, Format: b.format, Data: dst}
}

// readInto copies the buffer's pixels into dst, returning the bytes
// copied.
func (b *shmBuffer) readInto(dst []byte) int { return copy(dst, b.data) }

// release destroys the wl_buffer and drops the client mapping.
func (b *shmBuffer) release() {
	if b.wl != nil {
		_ = b.wl.Destroy()
		b.wl = nil
	}
	b.unmap()
}

func (b *shmBuffer) unmap() {
	if b.data != nil {
		_ = wlos.Munmap(b.data)
		b.data = nil
	}
	if b.file != nil {
		_ = b.file.Close()
		b.file = nil
	}
}

// errNoBuffer reports a copy the compositor advertised no usable
// buffer for.
var errNoBuffer = errors.New("capture: the compositor advertised no shm buffer for the frame")
