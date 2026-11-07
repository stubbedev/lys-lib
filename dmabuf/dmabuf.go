// Package dmabuf describes Linux dma-buf frames: GPU (or udmabuf)
// memory handed to the compositor by file descriptor through
// zwp_linux_dmabuf_v1, with no copy. capture fills them with screen
// contents; widget.GPUArea presents them.
package dmabuf

import (
	"errors"
	"fmt"
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

// ErrNoSyncFileImport reports a kernel without
// DMA_BUF_IOCTL_IMPORT_SYNC_FILE, added in Linux 6.0.
var ErrNoSyncFileImport = errors.New("dmabuf: the kernel attaches no sync_file to a dmabuf")

// dmaBufIoctlImportSyncFile is DMA_BUF_IOCTL_IMPORT_SYNC_FILE and
// dmaBufSyncWrite its flag for a fence that writers and readers wait on.
const (
	dmaBufIoctlImportSyncFile = 0x40086203
	dmaBufSyncWrite           = 2
)

// ImportSyncFile attaches the fence in syncFile to the dmabuf fd as its
// writer's: whoever uses the dmabuf with implicit synchronization (a
// compositor, a display) waits for it. syncFile stays the caller's.
func ImportSyncFile(fd, syncFile int) error {
	arg := struct {
		flags uint32
		fd    int32
	}{dmaBufSyncWrite, int32(syncFile)} //nolint:gosec // a file descriptor fits an int32
	for {
		_, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), dmaBufIoctlImportSyncFile, uintptr(unsafe.Pointer(&arg))) //nolint:gosec // the kernel reads the struct during the call
		switch errno {
		case 0:
			return nil
		case unix.EINTR, unix.EAGAIN:
			continue
		case unix.ENOTTY:
			return ErrNoSyncFileImport
		}
		return fmt.Errorf("dmabuf: DMA_BUF_IOCTL_IMPORT_SYNC_FILE of sync_file %d onto dmabuf %d: %w", syncFile, fd, errno)
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
func BeginCPUWrite(fd int) error { return cpuSync(fd, dmaBufSyncStart|dmaBufSyncWrite, "start") }

// EndCPUWrite ends a CPU write BeginCPUWrite started, flushing it for
// the device that reads the buffer next.
func EndCPUWrite(fd int) error { return cpuSync(fd, dmaBufSyncEnd|dmaBufSyncWrite, "end") }

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
