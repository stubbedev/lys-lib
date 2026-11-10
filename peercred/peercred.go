// Package peercred reads who is at the other end of a Unix socket, as
// the kernel recorded it when the peer connected: its credentials, and
// where the kernel gives one a pidfd, which keeps naming that process
// after its pid has been reused.
package peercred

import (
	"errors"
	"fmt"
	"net"
	"os"

	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-lib/intconv"
)

// Credentials are the ids of the process at a socket's other end, from
// SO_PEERCRED.
type Credentials struct {
	UID uint32
	GID uint32
	PID int32
}

// Of reads the credentials of the process at conn's other end.
func Of(conn *net.UnixConn) (Credentials, error) {
	var cred Credentials
	err := control(conn, func(fd int) (err error) {
		cred, err = credentials(fd)
		return err
	})
	return cred, err
}

func credentials(fd int) (Credentials, error) {
	cred, err := unix.GetsockoptUcred(fd, unix.SOL_SOCKET, unix.SO_PEERCRED)
	if err != nil {
		return Credentials{}, fmt.Errorf("SO_PEERCRED: %w", err)
	}
	return Credentials{UID: cred.Uid, GID: cred.Gid, PID: cred.Pid}, nil
}

// ErrNoPIDFD is what PIDFDOf wraps on a kernel without SO_PEERPIDFD
// (before Linux 6.5): the peer is known by its Credentials alone.
var ErrNoPIDFD = errors.New("the kernel has no SO_PEERPIDFD")

// PIDFD is a pidfd of the process at a socket's other end, with that
// process's credentials. Unlike its pid it names that one process for
// as long as it is held, whatever the pid is reused for.
type PIDFD struct {
	file *os.File
	cred Credentials
}

// PIDFDOf opens a pidfd of the process at conn's other end, for the
// caller to Close. A kernel without SO_PEERPIDFD is ErrNoPIDFD: there
// Of still says who the peer is.
func PIDFDOf(conn *net.UnixConn) (*PIDFD, error) {
	var (
		pidfd int
		cred  Credentials
	)
	if err := control(conn, func(fd int) (err error) {
		if cred, err = credentials(fd); err != nil {
			return err
		}
		if pidfd, err = unix.GetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_PEERPIDFD); err != nil {
			return fmt.Errorf("SO_PEERPIDFD: %w", noPIDFD(err))
		}
		return nil
	}); err != nil {
		return nil, err
	}
	return &PIDFD{file: os.NewFile(intconv.Must[uintptr](pidfd), "pidfd"), cred: cred}, nil
}

func noPIDFD(err error) error {
	if errors.Is(err, unix.ENOPROTOOPT) {
		return ErrNoPIDFD
	}
	return err
}

func control(conn *net.UnixConn, read func(fd int) error) error {
	raw, err := conn.SyscallConn()
	if err != nil {
		return fmt.Errorf("peercred: %w", err)
	}
	var readErr error
	if err := raw.Control(func(fd uintptr) { readErr = read(intconv.Must[int](fd)) }); err != nil {
		return fmt.Errorf("peercred: %w", err)
	}
	if readErr != nil {
		return fmt.Errorf("peercred: %w", readErr)
	}
	return nil
}

// Credentials are the ids of the process the pidfd names, as Of reads
// them.
func (p *PIDFD) Credentials() Credentials { return p.cred }

// FD is the descriptor, for a call that takes a pidfd; it is valid
// until Close.
func (p *PIDFD) FD() (int, error) {
	fd, err := intconv.Exact[int](p.file.Fd())
	if err != nil {
		return -1, fmt.Errorf("peercred: the pidfd: %w", os.ErrClosed)
	}
	return fd, nil
}

// Exited reports whether the process has ended. While it has not, its
// pid is still its own: what was read by that pid before an Exited
// that reports false was read of this process.
func (p *PIDFD) Exited() (bool, error) {
	fd, err := p.FD()
	if err != nil {
		return false, err
	}
	fds := [1]unix.PollFd{{Fd: intconv.Must[int32](fd), Events: unix.POLLIN}}
	for {
		n, err := unix.Poll(fds[:], 0)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return false, fmt.Errorf("peercred: poll the pidfd: %w", err)
		}
		return n > 0, nil
	}
}

// Close releases the descriptor.
func (p *PIDFD) Close() error {
	if err := p.file.Close(); err != nil {
		return fmt.Errorf("peercred: close the pidfd: %w", err)
	}
	return nil
}
