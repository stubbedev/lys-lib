//go:build !darwin

package dbus_test

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/stubbedev/lys-lib/dbustest"
	dbus "github.com/stubbedev/lys-lib/third_party/godbus"
)

func busID(t *testing.T, conn *dbus.Conn) string {
	t.Helper()
	var id string
	if err := conn.BusObject().Call("org.freedesktop.DBus.GetId", 0).Store(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// TestTheSessionBusIsFoundInTheRuntimeDirectory is the runtime
// directory patch: with DBUS_SESSION_BUS_ADDRESS unset, upstream looks
// for the bus in /run/user/<uid> whatever XDG_RUNTIME_DIR says, so a
// process given a runtime directory of its own still joined the bus of
// the user's login session, and missed the one in its own directory.
func TestTheSessionBusIsFoundInTheRuntimeDirectory(t *testing.T) {
	sock := dbustest.SocketPath(t)
	bus := dbustest.StartAt(t, sock)
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "")
	t.Setenv("XDG_RUNTIME_DIR", filepath.Dir(sock))
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		t.Fatalf("the session bus at $XDG_RUNTIME_DIR/bus was not found: %v", err)
	}
	defer conn.Close()
	if got, want := busID(t, conn), busID(t, bus.Conn(t)); got != want {
		t.Errorf("connected to bus %s, want the one in the runtime directory, %s", got, want)
	}
}

func TestARuntimeDirectoryWithoutABusHasNoSessionBus(t *testing.T) {
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "")
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	conn, err := dbus.ConnectSessionBus()
	if err == nil {
		_ = conn.Close()
		t.Fatal("a session bus was found outside the runtime directory, which holds none")
	}
	if !errors.Is(err, dbus.ErrNoSessionBus) {
		t.Errorf("error %v, want ErrNoSessionBus", err)
	}
}
