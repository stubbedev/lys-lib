// Package peercredtest gives a test a socket peer that is another
// process, with a pid and a lifetime of its own: what telling a peer
// from the test itself, or outliving it, needs.
package peercredtest

import (
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stubbedev/lys-lib/sockdir"
)

// socketEnv names, to the process Start runs, the socket to be the
// peer of.
const socketEnv = "LYS_PEERCREDTEST_SOCKET"

// connectTimeout bounds the wait for the peer to connect.
const connectTimeout = 10 * time.Second

// Run is the TestMain of a package whose tests call Start:
//
//	func TestMain(m *testing.M) { peercredtest.Run(m) }
//
// It runs the tests and exits with their code, except in the process
// Start runs, where it is the peer. See Main.
func Run(m *testing.M) { os.Exit(Main(m.Run)) }

// Main is Run for a TestMain with work of its own around the tests,
// such as testhome's isolation:
//
//	func TestMain(m *testing.M) {
//		os.Exit(testhome.Main(func() int { return peercredtest.Main(m.Run) }))
//	}
//
// It calls run and returns its code, except in the process Start runs,
// where it is the peer: it connects to the test's socket, stays until
// its standard input closes, and returns the peer's exit code.
func Main(run func() int) int {
	path := os.Getenv(socketEnv)
	if path == "" {
		return run()
	}
	if err := stay(path); err != nil {
		fmt.Fprintln(os.Stderr, "peercredtest:", err)
		return 1
	}
	return 0
}

func stay(path string) error {
	conn, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	_, err = io.Copy(io.Discard, os.Stdin)
	return err
}

// Peer is a process of its own connected to the test.
type Peer struct {
	// Conn is the test's end of the connection the peer made.
	Conn *net.UnixConn
	// PID is the peer's process id.
	PID int

	cmd   *exec.Cmd
	stdin io.Closer
	done  bool
}

// Start runs the test binary again as a peer (Main) and returns once
// it has connected. The peer ends, and is reaped, with the test at the
// latest.
func Start(t testing.TB) *Peer {
	t.Helper()
	if os.Getenv(socketEnv) != "" {
		t.Fatal("peercredtest: this is the peer process running tests: the package's TestMain calls neither peercredtest.Run nor peercredtest.Main")
	}
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: filepath.Join(sockdir.Dir(t), "peer"), Net: "unix"})
	if err != nil {
		t.Fatalf("peercredtest: %v", err)
	}
	defer func() { _ = l.Close() }()
	if err := l.SetDeadline(time.Now().Add(connectTimeout)); err != nil {
		t.Fatalf("peercredtest: %v", err)
	}
	cmd := exec.Command(os.Args[0]) //nolint:gosec // the test binary itself
	cmd.Env = append(os.Environ(), socketEnv+"="+l.Addr().String())
	cmd.Stderr = os.Stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("peercredtest: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("peercredtest: start the peer: %v", err)
	}
	p := &Peer{PID: cmd.Process.Pid, cmd: cmd, stdin: stdin}
	t.Cleanup(func() { p.Reap(t) })
	if p.Conn, err = l.AcceptUnix(); err != nil {
		t.Fatalf("peercredtest: the peer did not connect: %v", err)
	}
	t.Cleanup(func() { _ = p.Conn.Close() })
	return p
}

// End tells the peer to exit and returns at once: until Reap the peer
// is running or, once it has exited, a zombie whose pid is still its
// own.
func (p *Peer) End() { _ = p.stdin.Close() }

// Reap ends the peer and waits for it, after which its pid is free for
// another process. Reaping again does nothing.
func (p *Peer) Reap(t testing.TB) {
	t.Helper()
	if p.done {
		return
	}
	p.done = true
	p.End()
	if err := p.cmd.Wait(); err != nil {
		t.Errorf("peercredtest: the peer: %v", err)
	}
}
