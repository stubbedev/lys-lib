package dbusx_test

import (
	"context"
	"errors"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stubbedev/lys-lib/dbustest"
	"github.com/stubbedev/lys-lib/dbusx"
	"github.com/stubbedev/lys-lib/sockdir"
	dbus "github.com/stubbedev/lys-lib/third_party/godbus"
)

const dialWait = 5 * time.Second

func silentSystemBus(t *testing.T) {
	t.Helper()
	sock := filepath.Join(sockdir.Dir(t), "silent")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	t.Setenv("DBUS_SYSTEM_BUS_ADDRESS", "unix:path="+sock)
}

func dialSystem(t *testing.T, ctx context.Context, timeout time.Duration) (*dbus.Conn, error) {
	t.Helper()
	type dialed struct {
		conn *dbus.Conn
		err  error
	}
	done := make(chan dialed, 1)
	go func() {
		conn, err := dbusx.DialSystem(ctx, timeout)
		done <- dialed{conn, err}
	}()
	select {
	case d := <-done:
		if d.conn != nil {
			t.Cleanup(func() { _ = d.conn.Close() })
		}
		return d.conn, d.err
	case <-time.After(dialWait):
		t.Fatalf("DialSystem did not return within %s", dialWait)
		return nil, nil
	}
}

func TestDialSystemConnectionOutlivesTheDial(t *testing.T) {
	dbustest.Start(t).UseAsSystemBus(t)
	ctx, cancel := context.WithCancel(t.Context())
	conn, err := dialSystem(t, ctx, dialWait)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	if owner, err := dbusx.NameOwner(t.Context(), conn, "org.freedesktop.DBus"); err != nil || owner == "" {
		t.Errorf("the connection after its dial's context ended: owner %q, %v", owner, err)
	}
}

func TestDialSystemGivesUpOnASilentBusAfterItsTimeout(t *testing.T) {
	silentSystemBus(t)
	conn, err := dialSystem(t, context.Background(), 20*time.Millisecond)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("DialSystem on a bus that never answers = %v, %v; want its timeout", conn, err)
	}
}

func TestDialSystemGivesUpWithItsContext(t *testing.T) {
	silentSystemBus(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	conn, err := dialSystem(t, ctx, dialWait)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("DialSystem with its context ended = %v, %v; want the context's end", conn, err)
	}
}

func TestDialSystemWithoutABusNamesTheSystemBus(t *testing.T) {
	t.Setenv("DBUS_SYSTEM_BUS_ADDRESS", "unix:path="+filepath.Join(sockdir.Dir(t), "absent"))
	conn, err := dialSystem(t, t.Context(), dialWait)
	if err == nil || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("DialSystem with no bus = %v, %v; want the dial's own failure", conn, err)
	}
	if want := "connect to the system bus: "; !strings.HasPrefix(err.Error(), want) {
		t.Errorf("error %q, want it to start %q", err, want)
	}
}
