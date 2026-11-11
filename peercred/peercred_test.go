package peercred

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-lib/peercred/peercredtest"
	"github.com/stubbedev/lys-lib/sockdir"
	"github.com/stubbedev/lys-lib/testhome"
)

func TestMain(m *testing.M) {
	os.Exit(testhome.Main(func() int { return peercredtest.Main(m.Run) }))
}

func TestTheTestsReachNoHostSession(t *testing.T) { testhome.RequireNoHostSession(t) }

func connect(t *testing.T) *net.UnixConn {
	t.Helper()
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: filepath.Join(sockdir.Dir(t), "s"), Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	client, err := net.DialUnix("unix", nil, l.Addr().(*net.UnixAddr))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	server, err := l.AcceptUnix()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	return server
}

func pidOf(t *testing.T, fd int) int {
	t.Helper()
	info, err := os.ReadFile("/proc/self/fdinfo/" + strconv.Itoa(fd))
	if err != nil {
		t.Fatal(err)
	}
	for line := range strings.SplitSeq(string(info), "\n") {
		if v, ok := strings.CutPrefix(line, "Pid:"); ok {
			pid, err := strconv.Atoi(strings.TrimSpace(v))
			if err != nil {
				t.Fatalf("fdinfo %q: %v", line, err)
			}
			return pid
		}
	}
	t.Fatalf("descriptor %d is no pidfd: %s", fd, info)
	return 0
}

func TestOfIsTheConnectingProcess(t *testing.T) {
	got, err := Of(connect(t))
	if err != nil {
		t.Fatal(err)
	}
	want := Credentials{UID: uint32(os.Getuid()), GID: uint32(os.Getgid()), PID: int32(os.Getpid())}
	if got != want {
		t.Errorf("Of = %+v, want %+v", got, want)
	}
}

func TestOfAClosedConnectionFails(t *testing.T) {
	server := connect(t)
	_ = server.Close()
	if got, err := Of(server); err == nil {
		t.Errorf("Of a closed connection = %+v, want an error", got)
	}
	if pidfd, err := PIDFDOf(server); err == nil || errors.Is(err, ErrNoPIDFD) {
		t.Errorf("PIDFDOf a closed connection = %v, %v; want an error that is not ErrNoPIDFD", pidfd, err)
	}
}

func TestPIDFDNamesThePeerUntilItExits(t *testing.T) {
	peer := peercredtest.Start(t)
	cred, err := Of(peer.Conn)
	if err != nil {
		t.Fatal(err)
	}
	if want := (Credentials{UID: uint32(os.Getuid()), GID: uint32(os.Getgid()), PID: int32(peer.PID)}); cred != want {
		t.Fatalf("Of = %+v, want the peer process %+v, not the test's own pid %d", cred, want, os.Getpid())
	}
	pidfd, err := PIDFDOf(peer.Conn)
	if errors.Is(err, ErrNoPIDFD) {
		t.Skipf("this kernel predates Linux 6.5: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	if got := pidfd.Credentials(); got != cred {
		t.Errorf("the pidfd's credentials %+v, want the peer's %+v", got, cred)
	}
	fd, err := pidfd.FD()
	if err != nil {
		t.Fatal(err)
	}
	if got := pidOf(t, fd); got != peer.PID {
		t.Errorf("the pidfd names pid %d, want the peer %d", got, peer.PID)
	}
	if flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0); err != nil || flags&unix.FD_CLOEXEC == 0 {
		t.Errorf("the pidfd's descriptor flags = %#x, %v; want close-on-exec", flags, err)
	}
	if exited, err := pidfd.Exited(); err != nil || exited {
		t.Errorf("Exited of the running peer = %v, %v", exited, err)
	}
	peer.Reap(t)
	if exited, err := pidfd.Exited(); err != nil || !exited {
		t.Errorf("Exited of the reaped peer = %v, %v", exited, err)
	}
	if err := pidfd.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := pidfd.FD(); !errors.Is(err, os.ErrClosed) {
		t.Errorf("FD of a closed pidfd = %v, want os.ErrClosed", err)
	}
	if exited, err := pidfd.Exited(); !errors.Is(err, os.ErrClosed) {
		t.Errorf("Exited of a closed pidfd = %v, %v; want os.ErrClosed", exited, err)
	}
}

func TestAKernelWithoutPeerPIDFDIsErrNoPIDFD(t *testing.T) {
	if err := noPIDFD(fmt.Errorf("getsockopt: %w", unix.ENOPROTOOPT)); !errors.Is(err, ErrNoPIDFD) {
		t.Errorf("ENOPROTOOPT = %v, want ErrNoPIDFD", err)
	}
	for _, errno := range []unix.Errno{unix.ESRCH, unix.ENODATA, unix.EBADF, unix.EMFILE} {
		if err := noPIDFD(errno); errors.Is(err, ErrNoPIDFD) || !errors.Is(err, errno) {
			t.Errorf("%v = %v, want it kept: the kernel has SO_PEERPIDFD", errno, err)
		}
	}
}
