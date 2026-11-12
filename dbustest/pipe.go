package dbustest

import (
	"errors"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-lib/intconv"
	dbus "github.com/stubbedev/lys-lib/third_party/godbus"
)

const hangupWait = 5 * time.Second

// Pipe proves that a descriptor a test sends over D-Bus is closed by
// everyone it reaches: Write is the write end of a pipe, to send, and
// its read end hangs up only once no copy of Write is open anywhere in
// the process.
type Pipe struct {
	Write  dbus.UnixFD
	read   int
	closed bool
}

// NewPipe opens a Pipe, closed at cleanup.
func NewPipe(t testing.TB) *Pipe {
	t.Helper()
	var fds [2]int
	if err := unix.Pipe2(fds[:], unix.O_CLOEXEC); err != nil {
		t.Fatalf("dbustest: open a pipe: %v", err)
	}
	p := &Pipe{Write: intconv.Must[dbus.UnixFD](fds[1]), read: fds[0]}
	t.Cleanup(func() {
		p.closeWrite(t)
		if err := unix.Close(p.read); err != nil {
			t.Errorf("dbustest: close the read end %d: %v", p.read, err)
		}
	})
	return p
}

func (p *Pipe) closeWrite(t testing.TB) {
	t.Helper()
	if p.closed {
		return
	}
	p.closed = true
	if err := unix.Close(int(p.Write)); err != nil {
		t.Fatalf("dbustest: close the write end %d: %v", p.Write, err)
	}
}

// RequireEveryCopyClosed closes Write and fails the test unless every
// copy of it the bus handed out in the process is closed as well within
// a liveness bound.
func (p *Pipe) RequireEveryCopyClosed(t testing.TB) {
	t.Helper()
	p.closeWrite(t)
	fds := []unix.PollFd{{Fd: intconv.Must[int32](p.read), Events: unix.POLLIN}}
	deadline := time.Now().Add(hangupWait)
	n, err := unix.Poll(fds, int(hangupWait.Milliseconds()))
	for errors.Is(err, unix.EINTR) && time.Now().Before(deadline) {
		n, err = unix.Poll(fds, int(time.Until(deadline).Milliseconds()))
	}
	if err != nil || n != 1 || fds[0].Revents&unix.POLLHUP == 0 {
		t.Fatalf("dbustest: a copy of descriptor %d is still open %v after the test closed its own: poll = %d, %v, events %#x", p.Write, hangupWait, n, err, fds[0].Revents)
	}
}
