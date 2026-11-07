package dmabuf

import (
	"errors"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-lib/udmabuf"
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

// A CPU write to a udmabuf is bracketed by the sync ioctl; an fd that is
// no dmabuf says which step failed on which fd.
func TestCPUWritesSyncAndNameWhatFailed(t *testing.T) {
	var p [2]int
	if err := unix.Pipe2(p[:], unix.O_CLOEXEC); err != nil {
		t.Fatal(err)
	}
	defer unix.Close(p[0])
	defer unix.Close(p[1])
	if err := BeginCPUWrite(p[0]); err == nil || !strings.Contains(err.Error(), "SYNC start") {
		t.Errorf("begin on a pipe: %v", err)
	}
	if err := EndCPUWrite(p[0]); err == nil || !strings.Contains(err.Error(), "SYNC end") {
		t.Errorf("end on a pipe: %v", err)
	}
	b, err := udmabuf.New(4096)
	if err != nil {
		t.Skipf("no udmabuf to sync: %v", err)
	}
	defer b.Close()
	if err := BeginCPUWrite(b.FD); err != nil {
		t.Fatal(err)
	}
	b.Mem[0] = 1
	if err := EndCPUWrite(b.FD); err != nil {
		t.Fatal(err)
	}
}
