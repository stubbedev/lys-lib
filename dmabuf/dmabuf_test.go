package dmabuf

import (
	"errors"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-lib/drm"
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

// A dmabuf no one has used exports a sync_file already signalled for
// either use, and Wait returns at once; an fd whose driver has no such
// ioctl answers as a kernel before Linux 6.0 does, and anything else is an
// error naming the use and the fd.
func TestExportSyncFileOfAnIdleDmabuf(t *testing.T) {
	var p [2]int
	if err := unix.Pipe2(p[:], unix.O_CLOEXEC); err != nil {
		t.Fatal(err)
	}
	defer unix.Close(p[0])
	defer unix.Close(p[1])
	if _, err := ExportSyncFile(p[0], Read); !errors.Is(err, ErrNoSyncFileExport) {
		t.Errorf("on a pipe: %v, want ErrNoSyncFileExport", err)
	}
	if _, err := ExportSyncFile(-1, Write); err == nil || errors.Is(err, ErrNoSyncFileExport) || !strings.Contains(err.Error(), "for a write of dmabuf -1") {
		t.Errorf("on a closed fd: %v, want an error of its own naming the write and the fd", err)
	}
	b, err := udmabuf.New(4096)
	if err != nil {
		t.Skipf("no udmabuf to export from: %v", err)
	}
	defer b.Close()
	for _, a := range []Access{Read, Write} {
		fd, err := ExportSyncFile(b.FD, a)
		if err != nil {
			t.Fatalf("%s: %v", a, err)
		}
		if signalled, err := drm.SyncFileSignalled(fd); err != nil || !signalled {
			t.Errorf("%s: the sync_file is signalled %v (%v), want true", a, signalled, err)
		}
		if err := unix.Close(fd); err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		if err := Wait(b.FD, a, time.Second); err != nil {
			t.Errorf("%s: Wait: %v", a, err)
		}
		if waited := time.Since(start); waited > 100*time.Millisecond {
			t.Errorf("%s: Wait on an idle dmabuf took %v", a, waited)
		}
	}
	if Read.String() != "read" || Write.String() != "write" || Access(7).String() != "access 7" {
		t.Errorf("accesses are named %q, %q and %q", Read, Write, Access(7))
	}
}
