//go:build linux

package os

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

const anonymousFileName = "lys-anonymous"

// CreateAnonymousFile returns a size-byte shared memory file that has no
// path and needs no writable directory.
func CreateAnonymousFile(size int64) (*os.File, error) {
	fd, err := unix.MemfdCreate(anonymousFileName, unix.MFD_CLOEXEC)
	if err != nil {
		return nil, fmt.Errorf("memfd_create: %w", err)
	}
	if err := unix.Ftruncate(fd, size); err != nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("size the memfd to %d bytes: %w", size, err)
	}
	return os.NewFile(uintptr(fd), "memfd:"+anonymousFileName), nil
}
