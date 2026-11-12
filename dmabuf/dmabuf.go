// Package dmabuf describes Linux dma-buf frames: GPU (or udmabuf)
// memory handed to the compositor by file descriptor through
// zwp_linux_dmabuf_v1, with no copy. capture fills them with screen
// contents; widget.GPUArea presents them.
package dmabuf

import (
	"errors"
	"fmt"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-lib/fourcc"
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
	Format        fourcc.Format
	Modifier      fourcc.Modifier
	Planes        []Plane
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

// Access is what a user of a dmabuf does with it, for implicit
// synchronization: a reader waits for the buffer's writers, a writer for
// its readers and writers.
type Access uint32

// The accesses, as DMA_BUF_SYNC_READ and DMA_BUF_SYNC_WRITE.
const (
	Read  Access = 1
	Write Access = 2
)

func (a Access) String() string {
	switch a {
	case Read:
		return "read"
	case Write:
		return "write"
	}
	return fmt.Sprintf("access %d", uint32(a))
}

// ErrNoSyncFileImport reports a kernel without
// DMA_BUF_IOCTL_IMPORT_SYNC_FILE, added in Linux 6.0.
var ErrNoSyncFileImport = errors.New("dmabuf: the kernel attaches no sync_file to a dmabuf")

// ErrNoSyncFileExport reports a kernel without
// DMA_BUF_IOCTL_EXPORT_SYNC_FILE, added in Linux 6.0.
var ErrNoSyncFileExport = errors.New("dmabuf: the kernel exports no sync_file from a dmabuf")

// DMA_BUF_IOCTL_EXPORT_SYNC_FILE and DMA_BUF_IOCTL_IMPORT_SYNC_FILE.
const (
	dmaBufIoctlExportSyncFile = 0xc0086202
	dmaBufIoctlImportSyncFile = 0x40086203
)

// syncFileArg is struct dma_buf_export_sync_file, and its import twin.
type syncFileArg struct {
	flags uint32
	fd    int32
}

// ImportSyncFile attaches the fence in syncFile to the dmabuf fd as its
// writer's: whoever uses the dmabuf with implicit synchronization (a
// compositor, a display) waits for it. syncFile stays the caller's.
func ImportSyncFile(fd, syncFile int) error {
	arg := syncFileArg{uint32(Write), int32(syncFile)} //nolint:gosec // a file descriptor fits an int32
	err := syncFileIoctl(fd, dmaBufIoctlImportSyncFile, &arg)
	if errors.Is(err, unix.ENOTTY) {
		return ErrNoSyncFileImport
	}
	if err != nil {
		return fmt.Errorf("dmabuf: DMA_BUF_IOCTL_IMPORT_SYNC_FILE of sync_file %d onto dmabuf %d: %w", syncFile, fd, err)
	}
	return nil
}

// ExportSyncFile returns a sync_file, the caller's to close, holding the
// fences on the dmabuf fd that a use of it for a waits for: its writers'
// for a Read, every reader's and writer's for a Write. One with no such
// fence is a sync_file already signalled.
func ExportSyncFile(fd int, a Access) (int, error) {
	arg := syncFileArg{uint32(a), -1}
	err := syncFileIoctl(fd, dmaBufIoctlExportSyncFile, &arg)
	if errors.Is(err, unix.ENOTTY) {
		return -1, ErrNoSyncFileExport
	}
	if err != nil {
		return -1, fmt.Errorf("dmabuf: DMA_BUF_IOCTL_EXPORT_SYNC_FILE for a %s of dmabuf %d: %w", a, fd, err)
	}
	return int(arg.fd), nil
}

func syncFileIoctl(fd int, req uintptr, arg *syncFileArg) error {
	for {
		_, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), req, uintptr(unsafe.Pointer(arg))) //nolint:gosec // the kernel reads and fills the struct during the call
		switch errno {
		case 0:
			return nil
		case unix.EINTR, unix.EAGAIN:
			continue
		}
		return errno
	}
}

// ErrWaitTimeout reports a Wait whose fences did not signal in time.
var ErrWaitTimeout = errors.New("dmabuf: the fences did not signal in time")

// Wait waits here, timeout at most, for the fences on the dmabuf fd
// that a use of it for a waits for, as ExportSyncFile names them: by
// polling the dmabuf, which every kernel with dmabufs answers.
func Wait(fd int, a Access, timeout time.Duration) error {
	events := int16(unix.POLLIN)
	if a == Write {
		events = unix.POLLOUT
	}
	deadline := time.Now().Add(timeout)
	pfd := []unix.PollFd{{Fd: int32(fd), Events: events}} //nolint:gosec // a file descriptor fits an int32
	for {
		n, err := unix.Poll(pfd, int(max(time.Until(deadline), 0).Milliseconds()))
		switch {
		case errors.Is(err, unix.EINTR):
			continue
		case err != nil:
			return fmt.Errorf("dmabuf: poll dmabuf %d for a %s: %w", fd, a, err)
		case n == 0:
			return fmt.Errorf("%w: dmabuf %d for a %s, %v", ErrWaitTimeout, fd, a, timeout)
		case pfd[0].Revents&(unix.POLLERR|unix.POLLNVAL) != 0:
			return fmt.Errorf("dmabuf: poll dmabuf %d for a %s: revents %#x", fd, a, pfd[0].Revents)
		}
		return nil
	}
}

// DMA_BUF_IOCTL_SYNC and its flags.
const (
	dmaBufIoctlSync = 0x40086200
	dmaBufSyncStart = 0
	dmaBufSyncEnd   = 4
)

// BeginCPUWrite starts a CPU write through a mapping of the dmabuf fd
// (DMA_BUF_IOCTL_SYNC): it waits for the devices still reading the
// buffer and, on a system whose caches do not snoop, readies the
// mapping. EndCPUWrite closes the write.
func BeginCPUWrite(fd int) error { return cpuSync(fd, dmaBufSyncStart|uint64(Write), "start") }

// EndCPUWrite ends a CPU write BeginCPUWrite started, flushing it for
// the device that reads the buffer next.
func EndCPUWrite(fd int) error { return cpuSync(fd, dmaBufSyncEnd|uint64(Write), "end") }

func cpuSync(fd int, flags uint64, what string) error {
	for {
		_, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), dmaBufIoctlSync, uintptr(unsafe.Pointer(&flags))) //nolint:gosec // the kernel reads the flags during the call
		switch errno {
		case 0:
			return nil
		case unix.EINTR, unix.EAGAIN:
			continue
		}
		return fmt.Errorf("dmabuf: DMA_BUF_IOCTL_SYNC %s of a CPU write to dmabuf %d: %w", what, fd, errno)
	}
}
