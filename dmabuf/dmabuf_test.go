package dmabuf

import (
	"errors"
	"testing"

	"golang.org/x/sys/unix"
)

// An fd whose driver has no such ioctl answers as a kernel before Linux 6.0
// does, which is what callers fall back on; anything else is an error of its own.
func TestImportSyncFileReportsAKernelWithoutIt(t *testing.T) {
	var p [2]int
	if err := unix.Pipe2(p[:], unix.O_CLOEXEC); err != nil {
		t.Fatal(err)
	}
	defer unix.Close(p[0])
	defer unix.Close(p[1])
	if err := ImportSyncFile(p[0], p[1]); !errors.Is(err, ErrNoSyncFileImport) {
		t.Errorf("on a pipe: %v, want ErrNoSyncFileImport", err)
	}
	if err := ImportSyncFile(-1, p[1]); err == nil || errors.Is(err, ErrNoSyncFileImport) {
		t.Errorf("on a closed fd: %v, want an error of its own", err)
	}
}
