//go:build unix

package dbus

import "golang.org/x/sys/unix"

// dupFD is a duplicate of fd that an exec closes, or -1 when the
// process can hold no more descriptors.
func dupFD(fd UnixFD) UnixFD {
	if fd < 0 {
		return fd
	}
	n, err := unix.FcntlInt(uintptr(fd), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return -1
	}
	return UnixFD(n)
}

func closeFD(fd UnixFD) error { return unix.Close(int(fd)) }
