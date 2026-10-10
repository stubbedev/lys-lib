// Package udmabuf allocates linear, CPU-visible dmabufs through
// /dev/udmabuf: a sealed memfd wrapped as a dmabuf, so tests and demos
// can hand the compositor dmabufs they fill and read without a GPU
// library.
package udmabuf

import (
	"errors"
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/unix"
)

type createRequest struct {
	memfd  uint32
	flags  uint32
	offset uint64
	size   uint64
}

const ioctlCreate = 0x40187542

// Buffer is an allocated udmabuf: FD is the dmabuf, Mem maps its
// memory read-write.
type Buffer struct {
	FD    int
	Mem   []byte
	memfd int
}

// New allocates size bytes, rounded up to whole pages. It fails when
// /dev/udmabuf is missing or not accessible.
func New(size int) (*Buffer, error) {
	size = (size + os.Getpagesize() - 1) &^ (os.Getpagesize() - 1)
	dev, err := os.OpenFile("/dev/udmabuf", os.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("udmabuf: %w", err)
	}
	defer dev.Close()
	memfd, err := unix.MemfdCreate("gelm-udmabuf", unix.MFD_ALLOW_SEALING|unix.MFD_CLOEXEC)
	if err != nil {
		return nil, fmt.Errorf("udmabuf: memfd: %w", err)
	}
	b := &Buffer{FD: -1, memfd: memfd}
	if err := b.init(dev, size); err != nil {
		return nil, errors.Join(err, b.Close())
	}
	return b, nil
}

func (b *Buffer) init(dev *os.File, size int) error {
	if err := unix.Ftruncate(b.memfd, int64(size)); err != nil {
		return fmt.Errorf("udmabuf: size the memfd: %w", err)
	}
	if _, err := unix.FcntlInt(uintptr(b.memfd), unix.F_ADD_SEALS, unix.F_SEAL_SHRINK); err != nil {
		return fmt.Errorf("udmabuf: seal the memfd: %w", err)
	}
	req := createRequest{memfd: uint32(b.memfd), flags: unix.O_CLOEXEC, size: uint64(size)}
	fd, _, errno := unix.Syscall(unix.SYS_IOCTL, dev.Fd(), ioctlCreate, uintptr(unsafe.Pointer(&req))) //nolint:gosec // UDMABUF_CREATE takes its request struct by pointer, and req outlives the syscall
	if errno != 0 {
		return fmt.Errorf("udmabuf: UDMABUF_CREATE: %w", errno)
	}
	b.FD = int(fd)
	mem, err := unix.Mmap(b.memfd, 0, size, unix.PROT_READ|unix.PROT_WRITE, unix.MAP_SHARED)
	if err != nil {
		return fmt.Errorf("udmabuf: map: %w", err)
	}
	b.Mem = mem
	return nil
}

// Close unmaps the memory and closes both descriptors.
func (b *Buffer) Close() error {
	var errs []error
	if b.Mem != nil {
		errs = append(errs, unix.Munmap(b.Mem))
		b.Mem = nil
	}
	if b.FD >= 0 {
		errs = append(errs, unix.Close(b.FD))
		b.FD = -1
	}
	if b.memfd >= 0 {
		errs = append(errs, unix.Close(b.memfd))
		b.memfd = -1
	}
	return errors.Join(errs...)
}
