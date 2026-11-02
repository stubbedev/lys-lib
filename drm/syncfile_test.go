package drm

import (
	"errors"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-lib/udmabuf"
)

// fakeSyncFile answers SYNC_IOC_FILE_INFO as drivers/dma-buf/sync_file.c
// does: no room only counts, too little room is EINVAL, otherwise the
// fences are filled and the status is the worst of theirs.
type fakeSyncFile struct {
	fences []syncFenceInfo
	fail   error
	calls  int
}

func (f *fakeSyncFile) call(_ int, info *syncFileInfo, fences []syncFenceInfo) error {
	f.calls++
	if f.fail != nil {
		return f.fail
	}
	status := int32(1)
	for i := range f.fences {
		if f.fences[i].status <= 0 && status > 0 {
			status = f.fences[i].status
		}
	}
	*info = syncFileInfo{numFences: uint32(len(f.fences)), status: status}
	if len(fences) == 0 {
		return nil
	}
	if len(fences) < len(f.fences) {
		*info = syncFileInfo{}
		return unix.EINVAL
	}
	copy(fences, f.fences)
	return nil
}

func signalled(timestamps ...uint64) []syncFenceInfo {
	fences := make([]syncFenceInfo, len(timestamps))
	for i, ts := range timestamps {
		fences[i] = syncFenceInfo{status: fenceSignalled, timestamp: ts}
	}
	return fences
}

func TestSyncFileSignalTimeDecisions(t *testing.T) {
	pending := append(signalled(5), syncFenceInfo{status: 0})
	errored := append(signalled(5), syncFenceInfo{status: -5, timestamp: 9})
	large := signalled(1, 2, 3, 4, 90, 6)
	largePending := append(signalled(1, 2, 3, 4, 5), syncFenceInfo{status: 0})
	for _, c := range []struct {
		name   string
		fences []syncFenceInfo
		when   time.Duration
		ok     bool
		calls  int
	}{
		{"no fences", nil, 0, false, 1},
		{"one signalled", signalled(7), 7, true, 1},
		{"latest of a full capacity", signalled(3, 9, 1, 4), 9, true, 1},
		{"one unsignalled", pending, 0, false, 1},
		{"one errored", errored, 0, false, 1},
		{"above capacity", large, 90, true, 3},
		{"above capacity, unsignalled", largePending, 0, false, 2},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := &fakeSyncFile{fences: c.fences}
			when, ok, err := syncFileSignalTime(3, f.call)
			if err != nil || when != c.when || ok != c.ok {
				t.Errorf("= %v, %v, %v; want %v, %v", when, ok, err, c.when, c.ok)
			}
			if f.calls != c.calls {
				t.Errorf("%d ioctls, want %d", f.calls, c.calls)
			}
		})
	}
}

func TestSyncFileSignalTimeReportsErrors(t *testing.T) {
	for _, c := range []struct {
		name  string
		fail  error
		calls int
	}{
		{"not a sync file", unix.ENOTTY, 1},
		{"a real EINVAL survives the fallback", unix.EINVAL, 2},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := &fakeSyncFile{fail: c.fail}
			_, ok, err := syncFileSignalTime(3, f.call)
			if !errors.Is(err, c.fail) || ok {
				t.Errorf("= %v, %v; want %v", ok, err, c.fail)
			}
			if f.calls != c.calls {
				t.Errorf("%d ioctls, want %d", f.calls, c.calls)
			}
		})
	}
}

// A leftover from a larger file in the pooled memory must not count.
func TestSyncFileSignalTimeReadsOnlyThisFilesFences(t *testing.T) {
	large := &fakeSyncFile{fences: signalled(1, 2, 3, 99)}
	if _, _, err := syncFileSignalTime(3, large.call); err != nil {
		t.Fatal(err)
	}
	small := &fakeSyncFile{fences: signalled(5)}
	for range 64 {
		if when, ok, err := syncFileSignalTime(3, small.call); err != nil || !ok || when != 5 {
			t.Fatalf("= %v, %v, %v", when, ok, err)
		}
	}
}

func TestSyncFileQueryMatchesTheKernelsLayout(t *testing.T) {
	if got := unsafe.Sizeof(syncFileInfo{}); got != syncFileInfoSize {
		t.Errorf("sync_file_info is %d bytes", got)
	}
	if got := unsafe.Sizeof(syncFenceInfo{}); got != syncFenceInfoSize {
		t.Errorf("sync_fence_info is %d bytes", got)
	}
}

func TestSyncFileSignalTimeAllocatesNothingWithinCapacity(t *testing.T) {
	f := &fakeSyncFile{fences: signalled(7)}
	call := f.call
	if allocs := testing.AllocsPerRun(1000, func() {
		if _, ok, err := syncFileSignalTime(3, call); !ok || err != nil {
			t.Fatal(ok, err)
		}
	}); allocs != 0 {
		t.Errorf("%v allocations a call", allocs)
	}
}

func BenchmarkSyncFileSignalTime(b *testing.B) {
	f := &fakeSyncFile{fences: signalled(7)}
	call := f.call
	b.ReportAllocs()
	for b.Loop() {
		if _, ok, err := syncFileSignalTime(3, call); !ok || err != nil {
			b.Fatal(ok, err)
		}
	}
}

// The ioctl this package states, against a kernel's real sync file: a
// udmabuf with no fences exports the signalled stub fence.
func realSyncFile(tb testing.TB) int {
	tb.Helper()
	buf, err := udmabuf.New(4096)
	if err != nil {
		tb.Skipf("no udmabuf: %v", err)
	}
	tb.Cleanup(func() { _ = buf.Close() })
	const exportSyncFile = ioctlReadWrite<<ioctlDirShift | 8<<ioctlSizeShift | 'b'<<ioctlTypeShift | 2
	arg := struct {
		flags uint32
		fd    int32
	}{flags: 1}
	if _, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(buf.FD), exportSyncFile, uintptr(unsafe.Pointer(&arg))); errno != 0 {
		tb.Fatalf("DMA_BUF_IOCTL_EXPORT_SYNC_FILE: %v", errno)
	}
	tb.Cleanup(func() { _ = unix.Close(int(arg.fd)) })
	return int(arg.fd)
}

func TestSyncFileSignalTimeOnARealSyncFile(t *testing.T) {
	fd := realSyncFile(t)
	calls := 0
	counted := func(fd int, info *syncFileInfo, fences []syncFenceInfo) error {
		calls++
		return kernelSyncFileInfo(fd, info, fences)
	}
	when, ok, err := syncFileSignalTime(fd, counted)
	if err != nil || !ok {
		t.Fatalf("= %v, %v, %v; want a signal time", when, ok, err)
	}
	if calls != 1 {
		t.Errorf("%d ioctls, want 1", calls)
	}
	var now unix.Timespec
	if err := unix.ClockGettime(unix.CLOCK_MONOTONIC, &now); err != nil {
		t.Fatal(err)
	}
	if when <= 0 || when > time.Duration(now.Nano()) {
		t.Errorf("signalled at %v, now %v", when, time.Duration(now.Nano()))
	}
	if signalledNow, err := SyncFileSignalled(fd); err != nil || !signalledNow {
		t.Errorf("SyncFileSignalled = %v, %v", signalledNow, err)
	}
	var counts syncFileInfo
	if err := kernelSyncFileInfo(fd, &counts, nil); err != nil || counts.numFences != 1 || counts.status != fenceSignalled {
		t.Errorf("the count: %d fences, status %d, %v", counts.numFences, counts.status, err)
	}
	if allocs := testing.AllocsPerRun(1000, func() {
		if _, ok, err := SyncFileSignalTime(fd); !ok || err != nil {
			t.Fatal(ok, err)
		}
	}); allocs != 0 {
		t.Errorf("%v allocations a call on a real sync file", allocs)
	}
}

func BenchmarkSyncFileSignalTimeReal(b *testing.B) {
	fd := realSyncFile(b)
	b.ReportAllocs()
	for b.Loop() {
		if _, ok, err := SyncFileSignalTime(fd); !ok || err != nil {
			b.Fatal(ok, err)
		}
	}
}

// The two-call path, which a file of more fences than the capacity takes.
func BenchmarkSyncFileSignalTimeRealCounted(b *testing.B) {
	fd := realSyncFile(b)
	var info syncFileInfo
	b.ReportAllocs()
	for b.Loop() {
		if _, ok, err := syncFileSignalTimeCounted(fd, &info, kernelSyncFileInfo); !ok || err != nil {
			b.Fatal(ok, err)
		}
	}
}
