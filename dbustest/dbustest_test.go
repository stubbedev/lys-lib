package dbustest

import (
	"errors"
	"os/exec"
	"strings"
	"testing"

	dbus "github.com/stubbedev/lys-lib/third_party/godbus"
)

type echo struct{}

func (echo) Echo(s string) (string, *dbus.Error) { return s, nil }

// The name is long on purpose: t.TempDir() embeds it, and a socket
// under that path would overflow sun_path.
func TestAPrivateBusStartsEvenWhenTheTestNameIsFarLongerThanAUnixSocketPathMayBeWithoutBeingTruncated(t *testing.T) {
	bus := Start(t)
	if bus.Address == "" {
		t.Fatal("no address")
	}
	_ = bus.Conn(t)
}

func TestPrivateBusCarriesCallsBetweenPeers(t *testing.T) {
	bus := Start(t)
	server := bus.Conn(t)
	if err := server.Export(echo{}, "/test", "com.example.Echo"); err != nil {
		t.Fatal(err)
	}
	if reply, err := server.RequestName("com.example.Echo", dbus.NameFlagDoNotQueue); err != nil || reply != dbus.RequestNameReplyPrimaryOwner {
		t.Fatalf("RequestName = %v, %v", reply, err)
	}
	client := bus.Conn(t)
	var got string
	if err := client.Object("com.example.Echo", "/test").Call("com.example.Echo.Echo", 0, "hi").Store(&got); err != nil {
		t.Fatal(err)
	}
	if got != "hi" {
		t.Errorf("echo = %q", got)
	}
	// An unowned name is an error, not a hang.
	if err := client.Object("com.example.Missing", "/test").Call("com.example.Echo.Echo", 0, "hi").Err; err == nil {
		t.Error("call to an unowned name: want an error")
	}
}

// A bus stopped and started again at the same socket is a new bus at
// the same address: connections to the old one are gone, a fresh dial
// of the path reaches the new one, and the cleanup's second Stop is
// harmless.
func TestABusRestartsAtTheSameSocket(t *testing.T) {
	sock := SocketPath(t)
	first := StartAt(t, sock)
	old := first.Conn(t)
	first.Stop(t)
	if err := old.BusObject().Call("org.freedesktop.DBus.Peer.Ping", 0).Err; err == nil {
		t.Error("a connection to the stopped bus still answers")
	}
	second := StartAt(t, sock)
	if second.Address == first.Address {
		t.Errorf("the restarted bus kept the address %s, guid included", first.Address)
	}
	conn, err := dbus.Connect("unix:path=" + sock)
	if err != nil {
		t.Fatalf("dial the restarted bus at %s: %v", sock, err)
	}
	defer func() { _ = conn.Close() }()
	if err := conn.BusObject().Call("org.freedesktop.DBus.Peer.Ping", 0).Err; err != nil {
		t.Errorf("ping the restarted bus: %v", err)
	}
}

// A missing dbus-daemon is an error that names it, which StartAt turns
// into a failed test rather than a skip.
func TestLaunchWithoutADaemonFails(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	_, err := launch(t.TempDir(), SocketPath(t))
	if !errors.Is(err, exec.ErrNotFound) || !strings.Contains(err.Error(), "dbus-daemon") {
		t.Fatalf("launch without dbus-daemon = %v, want a not-found error naming it", err)
	}
}

func TestVarIsOrderedAcrossGoroutines(t *testing.T) {
	var v Var[int]
	if v.Load() != 0 {
		t.Fatal("the zero Var holds the zero value")
	}
	done := make(chan struct{})
	for range 8 {
		go func() {
			v.Update(func(n int) int { return n + 1 })
			done <- struct{}{}
		}()
	}
	for range 8 {
		<-done
	}
	if got := v.Load(); got != 8 {
		t.Fatalf("after 8 updates = %d, want 8 (an update was lost)", got)
	}
	v.Store(3)
	if got := v.Load(); got != 3 {
		t.Fatalf("after Store(3) = %d", got)
	}
}
