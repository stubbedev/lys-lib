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

type dialedBus struct {
	name    string
	env     string
	dial    func(context.Context, time.Duration) (*dbus.Conn, error)
	private func(*dbustest.Bus, testing.TB)
}

var dialedBuses = []dialedBus{
	{"system", "DBUS_SYSTEM_BUS_ADDRESS", dbusx.DialSystem, (*dbustest.Bus).UseAsSystemBus},
	{"session", "DBUS_SESSION_BUS_ADDRESS", dbusx.DialSession, (*dbustest.Bus).UseAsSessionBus},
}

func eachBus(t *testing.T, test func(t *testing.T, bus dialedBus)) {
	t.Helper()
	for _, bus := range dialedBuses {
		t.Run(bus.name, func(t *testing.T) { test(t, bus) })
	}
}

func (b dialedBus) silent(t *testing.T) {
	t.Helper()
	sock := filepath.Join(sockdir.Dir(t), "silent")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	t.Setenv(b.env, "unix:path="+sock)
}

func (b dialedBus) connect(t *testing.T, ctx context.Context, timeout time.Duration) (*dbus.Conn, error) {
	t.Helper()
	type dialed struct {
		conn *dbus.Conn
		err  error
	}
	done := make(chan dialed, 1)
	go func() {
		conn, err := b.dial(ctx, timeout)
		done <- dialed{conn, err}
	}()
	select {
	case d := <-done:
		if d.conn != nil {
			t.Cleanup(func() { _ = d.conn.Close() })
		}
		return d.conn, d.err
	case <-time.After(dialWait):
		t.Fatalf("the dial of the %s bus did not return within %s", b.name, dialWait)
		return nil, nil
	}
}

func TestADialedConnectionOutlivesTheDial(t *testing.T) {
	eachBus(t, func(t *testing.T, bus dialedBus) {
		bus.private(dbustest.Start(t), t)
		ctx, cancel := context.WithCancel(t.Context())
		conn, err := bus.connect(t, ctx, dialWait)
		if err != nil {
			t.Fatal(err)
		}
		cancel()
		if owner, err := dbusx.NameOwner(t.Context(), conn, "org.freedesktop.DBus"); err != nil || owner == "" {
			t.Errorf("the connection after its dial's context ended: owner %q, %v", owner, err)
		}
	})
}

func TestADialGivesUpOnASilentBusAfterItsTimeout(t *testing.T) {
	eachBus(t, func(t *testing.T, bus dialedBus) {
		bus.silent(t)
		conn, err := bus.connect(t, context.Background(), 20*time.Millisecond)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("the dial of a bus that never answers = %v, %v; want its timeout", conn, err)
		}
	})
}

func TestADialGivesUpWithItsContext(t *testing.T) {
	eachBus(t, func(t *testing.T, bus dialedBus) {
		bus.silent(t)
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		conn, err := bus.connect(t, ctx, dialWait)
		if !errors.Is(err, context.Canceled) {
			t.Errorf("the dial with its context ended = %v, %v; want the context's end", conn, err)
		}
	})
}

func TestADialWithoutABusNamesTheBus(t *testing.T) {
	eachBus(t, func(t *testing.T, bus dialedBus) {
		t.Setenv(bus.env, "unix:path="+filepath.Join(sockdir.Dir(t), "absent"))
		conn, err := bus.connect(t, t.Context(), dialWait)
		if err == nil || errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("the dial with no bus = %v, %v; want the dial's own failure", conn, err)
		}
		if want := "connect to the " + bus.name + " bus: "; !strings.HasPrefix(err.Error(), want) {
			t.Errorf("error %q, want it to start %q", err, want)
		}
	})
}
