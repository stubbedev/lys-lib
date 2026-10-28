// Package dbustest runs private dbus-daemons for tests, so services that
// export or call D-Bus objects are exercised over a real bus without
// touching the user's session or system bus. Each bus is configured from
// a file the helper writes itself, so a test needs dbus-daemon on PATH
// and nothing in /etc, where a distro's session.conf may be absent
// (NixOS, minimal containers).
package dbustest

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stubbedev/lys-lib/sockdir"
	dbus "github.com/stubbedev/lys-lib/third_party/godbus"
)

// config is a session-bus policy that lets every connection own any
// name and talk to anyone, like the stock session.conf.
const config = `<!DOCTYPE busconfig PUBLIC "-//freedesktop//DTD D-BUS Bus Configuration 1.0//EN"
 "http://www.freedesktop.org/standards/dbus/1.0/busconfig.dtd">
<busconfig>
  <type>session</type>
  <listen>unix:path=%SOCKET%</listen>
  <auth>EXTERNAL</auth>
  <policy context="default">
    <allow send_destination="*" eavesdrop="true"/>
    <allow eavesdrop="true"/>
    <allow own="*"/>
  </policy>
</busconfig>
`

const startTimeout = 5 * time.Second

// Bus is one private bus.
type Bus struct {
	// Address is the bus address, for dbus.Connect or
	// DBUS_SESSION_BUS_ADDRESS.
	Address string
	daemon  *exec.Cmd
	stop    sync.Once
}

// Start launches a dbus-daemon on a fresh socket (SocketPath) for the
// test and stops it at cleanup. A missing dbus-daemon fails the test:
// the dev shell provides one.
func Start(t testing.TB) *Bus {
	t.Helper()
	return StartAt(t, SocketPath(t))
}

// SocketPath is a fresh bus socket path, removed with the test, in a
// directory short enough for AF_UNIX's 108-byte sun_path (sockdir). A
// bus stopped there can be followed by another at the same path, which
// a client dialing that fixed address sees as the bus restarting.
func SocketPath(t testing.TB) string {
	t.Helper()
	return filepath.Join(sockdir.Dir(t), "bus")
}

// StartAt launches a dbus-daemon bound to sock for the test and stops
// it at cleanup. The bus accepts connections once StartAt returns.
func StartAt(t testing.TB, sock string) *Bus {
	t.Helper()
	b, err := launch(t.TempDir(), sock)
	if err != nil {
		t.Fatalf("dbustest: %v", err)
	}
	t.Cleanup(func() { b.Stop(t) })
	return b
}

// launch runs a dbus-daemon bound to sock, its config written to dir,
// and returns once the daemon prints its address: it does so only
// after binding the socket.
func launch(dir, sock string) (*Bus, error) {
	daemon, err := exec.LookPath("dbus-daemon")
	if err != nil {
		return nil, fmt.Errorf("dbus-daemon not on PATH: %w", err)
	}
	confPath := filepath.Join(dir, "bus.conf")
	conf := strings.Replace(config, "%SOCKET%", dbus.EscapeBusAddressValue(sock), 1)
	if err := os.WriteFile(confPath, []byte(conf), 0o600); err != nil {
		return nil, fmt.Errorf("write bus config %s: %w", confPath, err)
	}
	cmd := exec.Command(daemon, "--config-file="+confPath, "--nofork", "--print-address=1") //nolint:gosec // the test daemon from PATH, a config this helper wrote
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("dbus-daemon stdout: %w", err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", daemon, err)
	}
	addresses := make(chan string, 1)
	go func() {
		defer close(addresses)
		if line, err := bufio.NewReader(stdout).ReadString('\n'); err == nil {
			addresses <- strings.TrimSpace(line)
		}
	}()
	var failure string
	select {
	case addr, ok := <-addresses:
		if ok && addr != "" {
			return &Bus{Address: addr, daemon: cmd}, nil
		}
		failure = "printed no address"
	case <-time.After(startTimeout):
		failure = "printed no address within " + startTimeout.String()
	}
	_ = cmd.Process.Kill()
	waitErr := cmd.Wait()
	return nil, fmt.Errorf("dbus-daemon on %s %s (%v): %s", sock, failure, waitErr, strings.TrimSpace(stderr.String()))
}

// Stop terminates the daemon and waits for it to exit, so a bus started
// next at the same socket does not race a dying one; connections to it
// see the bus go away. Stopping again does nothing.
func (b *Bus) Stop(t testing.TB) {
	t.Helper()
	b.stop.Do(func() {
		if err := b.daemon.Process.Signal(syscall.SIGTERM); err != nil {
			t.Errorf("dbustest: stop dbus-daemon at %s: %v", b.Address, err)
		}
		var exit *exec.ExitError
		if err := b.daemon.Wait(); err != nil && !errors.As(err, &exit) {
			t.Errorf("dbustest: wait for dbus-daemon at %s: %v", b.Address, err)
		}
	})
}

// Conn opens a new authenticated connection to the bus, closed at
// cleanup. Each call is a distinct peer with its own unique name.
func (b *Bus) Conn(t testing.TB) *dbus.Conn {
	t.Helper()
	conn, err := dbus.Connect(b.Address)
	if err != nil {
		t.Fatalf("dbustest: connect %s: %v", b.Address, err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// UseAsSessionBus points DBUS_SESSION_BUS_ADDRESS at the bus for the
// test, for code that dials the session bus itself.
func (b *Bus) UseAsSessionBus(t testing.TB) {
	t.Helper()
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", b.Address)
}

// UseAsSystemBus points DBUS_SYSTEM_BUS_ADDRESS at the bus for the
// test, for code that dials the system bus itself.
func (b *Bus) UseAsSystemBus(t testing.TB) {
	t.Helper()
	t.Setenv("DBUS_SYSTEM_BUS_ADDRESS", b.Address)
}

// Session starts a private bus and makes it the test's session bus,
// for daemon and CLI halves that both dial the session bus.
func Session(t testing.TB) *Bus {
	t.Helper()
	b := Start(t)
	b.UseAsSessionBus(t)
	return b
}

// SessionConn opens a connection to the session bus the test pointed
// at a private bus (Session or UseAsSessionBus), closed at cleanup.
func SessionConn(t testing.TB) *dbus.Conn {
	t.Helper()
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		t.Fatalf("dbustest: connect session bus: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}
