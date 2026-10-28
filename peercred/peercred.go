// Package peercred reads the credentials of the process at the other
// end of a Unix socket, as the kernel recorded them when it connected.
package peercred

import (
	"fmt"
	"net"

	"golang.org/x/sys/unix"
)

// UID is the uid of the process at conn's other end, from SO_PEERCRED.
func UID(conn *net.UnixConn) (uint32, error) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return 0, fmt.Errorf("peercred: %w", err)
	}
	var cred *unix.Ucred
	var credErr error
	if err := raw.Control(func(fd uintptr) {
		cred, credErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil {
		return 0, fmt.Errorf("peercred: %w", err)
	}
	if credErr != nil {
		return 0, fmt.Errorf("peercred: SO_PEERCRED: %w", credErr)
	}
	return cred.Uid, nil
}
