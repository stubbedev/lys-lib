package drm

import (
	"errors"
	"fmt"
	"runtime"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

const (
	syncFileInfoSize  = 56
	syncFenceInfoSize = 80
	ioctlSyncFileInfo = ioctlReadWrite<<ioctlDirShift | syncFileInfoSize<<ioctlSizeShift | '>'<<ioctlTypeShift | 4
	fenceSignalled    = 1
)

type syncFileInfo struct {
	name      [32]byte
	status    int32
	flags     uint32
	numFences uint32
	pad       uint32
	fences    uint64
}

type syncFenceInfo struct {
	objName    [32]byte
	driverName [32]byte
	status     int32
	flags      uint32
	timestamp  uint64
}

// syncFenceCapacity is how many fences the first SYNC_IOC_FILE_INFO has room
// for. A semaphore's sync file holds one fence and a dma-buf's holds one per
// writer, so four leaves the second call to the rare merged file.
const syncFenceCapacity = 4

// syncFileQuery is one call's memory. Its address reaches the kernel as an
// integer inside syncFileInfo, which the runtime would not adjust if a
// goroutine stack moved, so it lives on the heap, which does not move.
type syncFileQuery struct {
	info   syncFileInfo
	fences [syncFenceCapacity]syncFenceInfo
}

var syncFileQueries = sync.Pool{New: func() any { return new(syncFileQuery) }}

// syncFileInfoCall makes SYNC_IOC_FILE_INFO with room for fences: with none
// it only counts them, and with fewer than the file holds it fails EINVAL.
type syncFileInfoCall func(fd int, info *syncFileInfo, fences []syncFenceInfo) error

func kernelSyncFileInfo(fd int, info *syncFileInfo, fences []syncFenceInfo) error {
	*info = syncFileInfo{numFences: uint32(len(fences))} //nolint:gosec // at most what a sync file reported
	if len(fences) > 0 {
		info.fences = uint64(uintptr(unsafe.Pointer(&fences[0]))) //nolint:gosec // heap memory the kernel fills during the call, kept alive below
	}
	err := syncFileIoctl(fd, unsafe.Pointer(info)) //nolint:gosec // the kernel fills this struct during the call
	runtime.KeepAlive(fences)
	return err
}

// SyncFileSignalTime reads when the sync file fd signaled, with
// SYNC_IOC_FILE_INFO: the latest signal timestamp of its fences, on
// CLOCK_MONOTONIC. It reports false while any fence is unsignaled or
// the file holds none. A file of up to syncFenceCapacity fences takes one
// ioctl and no allocation.
func SyncFileSignalTime(fd int) (time.Duration, bool, error) {
	return syncFileSignalTime(fd, kernelSyncFileInfo)
}

func syncFileSignalTime(fd int, call syncFileInfoCall) (time.Duration, bool, error) {
	q, _ := syncFileQueries.Get().(*syncFileQuery)
	defer syncFileQueries.Put(q)
	err := call(fd, &q.info, q.fences[:])
	if err == nil {
		when, ok := latestSignal(&q.info, q.fences[:q.info.numFences])
		return when, ok, nil
	}
	if !errors.Is(err, unix.EINVAL) {
		return 0, false, fmt.Errorf("drm: SYNC_IOC_FILE_INFO on fd %d: %w", fd, err)
	}
	return syncFileSignalTimeCounted(fd, &q.info, call)
}

// syncFileSignalTimeCounted is the path for a file of more fences than the
// first call had room for, or for a real EINVAL, which the count reports.
func syncFileSignalTimeCounted(fd int, info *syncFileInfo, call syncFileInfoCall) (time.Duration, bool, error) {
	if err := call(fd, info, nil); err != nil {
		return 0, false, fmt.Errorf("drm: SYNC_IOC_FILE_INFO on fd %d: %w", fd, err)
	}
	if info.status != fenceSignalled || info.numFences == 0 {
		return 0, false, nil
	}
	fences := make([]syncFenceInfo, info.numFences)
	if err := call(fd, info, fences); err != nil {
		return 0, false, fmt.Errorf("drm: SYNC_IOC_FILE_INFO on fd %d with %d fences: %w", fd, len(fences), err)
	}
	when, ok := latestSignal(info, fences)
	return when, ok, nil
}

// latestSignal is the time the last of the fences signalled, false while any
// has not or there are none.
func latestSignal(info *syncFileInfo, fences []syncFenceInfo) (time.Duration, bool) {
	if info.status != fenceSignalled || len(fences) == 0 {
		return 0, false
	}
	var last uint64
	for i := range fences {
		if fences[i].status != fenceSignalled {
			return 0, false
		}
		last = max(last, fences[i].timestamp)
	}
	return time.Duration(last), true
}

// SyncFileSignalled reports whether every fence in the sync file fd has
// signalled, in one SYNC_IOC_FILE_INFO that does not read the fences.
func SyncFileSignalled(fd int) (bool, error) {
	var info syncFileInfo
	if err := syncFileIoctl(fd, unsafe.Pointer(&info)); err != nil { //nolint:gosec // the kernel fills this struct during the call
		return false, fmt.Errorf("drm: SYNC_IOC_FILE_INFO on fd %d: %w", fd, err)
	}
	return info.status == fenceSignalled, nil
}

func syncFileIoctl(fd int, arg unsafe.Pointer) error {
	for {
		_, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), ioctlSyncFileInfo, uintptr(arg))
		switch errno {
		case 0:
			return nil
		case unix.EINTR, unix.EAGAIN:
			continue
		}
		return errno
	}
}
