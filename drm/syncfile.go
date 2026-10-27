package drm

import (
	"fmt"
	"runtime"
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

func SyncFileSignalTime(fd int) (time.Duration, bool, error) {
	var info syncFileInfo
	if err := syncFileIoctl(fd, unsafe.Pointer(&info)); err != nil { //nolint:gosec // the kernel fills this struct during the call
		return 0, false, fmt.Errorf("drm: SYNC_IOC_FILE_INFO on fd %d: %w", fd, err)
	}
	if info.status != fenceSignalled || info.numFences == 0 {
		return 0, false, nil
	}
	fences := make([]syncFenceInfo, info.numFences)
	info.fences = uint64(uintptr(unsafe.Pointer(&fences[0]))) //nolint:gosec // the kernel fills the fence array during the call, kept alive below
	err := syncFileIoctl(fd, unsafe.Pointer(&info))           //nolint:gosec // the kernel fills this struct during the call
	runtime.KeepAlive(fences)
	if err != nil {
		return 0, false, fmt.Errorf("drm: SYNC_IOC_FILE_INFO on fd %d with %d fences: %w", fd, len(fences), err)
	}
	var last uint64
	for _, f := range fences {
		if f.status != fenceSignalled {
			return 0, false, nil
		}
		last = max(last, f.timestamp)
	}
	return time.Duration(last), true, nil
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
