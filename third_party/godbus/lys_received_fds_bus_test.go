//go:build !darwin

package dbus_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stubbedev/lys-lib/dbustest"
	dbus "github.com/stubbedev/lys-lib/third_party/godbus"
)

// TestADictOfDescriptorsArrives is the received-descriptors patch on a
// bus: any peer sending an a{sh} with descriptors took down the process
// of every connection its message reached.
func TestADictOfDescriptorsArrives(t *testing.T) {
	server, client := pairedConns(t)
	if err := client.AddMatchSignal(dbus.WithMatchInterface(handIface)); err != nil {
		t.Fatal(err)
	}
	ch := make(chan *dbus.Signal, 1)
	client.Signal(ch)
	pipe := newHandPipe(t)
	want := inode(t, int(pipe.fd()))
	before := openDescriptors(t)
	if err := server.Emit(handPath, handIface+".Dict", map[string]dbus.UnixFD{"a": pipe.fd(), "b": pipe.fd()}, map[string][]dbus.UnixFD{"c": {pipe.fd()}}); err != nil {
		t.Fatal(err)
	}
	sig := awaitPlain(t, ch)
	if len(sig.Body) != 2 {
		t.Fatalf("received %#v", sig.Body)
	}
	dict, isDict := sig.Body[0].(map[string]dbus.UnixFD)
	nested, isNested := sig.Body[1].(map[string][]dbus.UnixFD)
	if !isDict || !isNested || len(dict) != 2 || len(nested["c"]) != 1 {
		t.Fatalf("received %#v, want the two dicts of descriptors", sig.Body)
	}
	for _, fd := range []dbus.UnixFD{dict["a"], dict["b"], nested["c"][0]} {
		if got := inode(t, int(fd)); got != want {
			t.Errorf("descriptor %d is inode %d, want the pipe's %d", fd, got, want)
		}
	}
	if got := openDescriptors(t); got != before+3 {
		t.Errorf("%d descriptors open, want the %d before and the three received", got, before)
	}
	if err := sig.CloseFDs(); err != nil {
		t.Error(err)
	}
	pipe.wantEveryCopyClosed(t, "the receiver closed the signal's descriptors")
}

type handTaker struct{ taken chan dbus.UnixFD }

func (h handTaker) Take(fd dbus.UnixFD) *dbus.Error {
	h.taken <- fd
	return nil
}

// A call no method runs for leaves its descriptors with nobody, so they
// are closed: upstream kept them open, and any peer could fill a
// service's descriptor table by calling what it does not export.
func TestTheDescriptorsOfACallNoMethodRunsForAreClosed(t *testing.T) {
	server, client := pairedConns(t)
	taker := handTaker{taken: make(chan dbus.UnixFD, 1)}
	if err := server.Export(taker, handPath, handIface); err != nil {
		t.Fatal(err)
	}
	object := client.Object(server.Names()[0], handPath)
	for name, call := range map[string]func(fd dbus.UnixFD) *dbus.Call{
		"an unknown method":    func(fd dbus.UnixFD) *dbus.Call { return object.Call(handIface+".Missing", 0, fd) },
		"an unknown interface": func(fd dbus.UnixFD) *dbus.Call { return object.Call("dev.stubbe.lys.Other.Take", 0, fd) },
		"an unknown object": func(fd dbus.UnixFD) *dbus.Call {
			return client.Object(server.Names()[0], "/dev/stubbe/lys/Nowhere").Call(handIface+".Take", 0, fd)
		},
		"arguments of other types": func(fd dbus.UnixFD) *dbus.Call { return object.Call(handIface+".Take", 0, "s", fd) },
		"a ping":                   func(fd dbus.UnixFD) *dbus.Call { return object.Call("org.freedesktop.DBus.Peer.Ping", 0, fd) },
	} {
		pipe := newHandPipe(t)
		_ = call(pipe.fd())
		pipe.wantEveryCopyClosed(t, name)
	}

	pipe := newHandPipe(t)
	if err := object.Call(handIface+".Take", 0, pipe.fd()).Err; err != nil {
		t.Fatal(err)
	}
	got := <-taker.taken
	if inode(t, int(got)) != inode(t, int(pipe.fd())) {
		t.Errorf("the method was handed descriptor %d, which is not the pipe", got)
	}
	if err := os.NewFile(uintptr(got), "taken").Close(); err != nil {
		t.Errorf("the method closing the descriptor it took: %v", err)
	}
	pipe.wantEveryCopyClosed(t, "the method closed the descriptor it took")
}

// Upstream wrote the address it found in the runtime directory into
// DBUS_SESSION_BUS_ADDRESS: connecting to the bus changed the
// environment of the process and of every child it started afterwards.
func TestFindingTheSessionBusLeavesTheEnvironmentAsItWas(t *testing.T) {
	sock := dbustest.SocketPath(t)
	dbustest.StartAt(t, sock)
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "")
	t.Setenv("XDG_RUNTIME_DIR", filepath.Dir(sock))
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if got := os.Getenv("DBUS_SESSION_BUS_ADDRESS"); got != "" {
		t.Errorf("connecting set DBUS_SESSION_BUS_ADDRESS to %q", got)
	}
}
