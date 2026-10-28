package peercred

import (
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/stubbedev/lys-lib/sockdir"
)

func connect(t *testing.T) *net.UnixConn {
	t.Helper()
	addr := &net.UnixAddr{Name: filepath.Join(sockdir.Dir(t), "s"), Net: "unix"}
	l, err := net.ListenUnix("unix", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	client, err := net.DialUnix("unix", nil, addr)
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

func TestUIDIsTheConnectingProcesss(t *testing.T) {
	uid, err := UID(connect(t))
	if err != nil {
		t.Fatal(err)
	}
	if want := os.Getuid(); int(uid) != want {
		t.Errorf("UID = %d, want %d", uid, want)
	}
}

func TestUIDOfAClosedConnectionFails(t *testing.T) {
	server := connect(t)
	_ = server.Close()
	if uid, err := UID(server); err == nil {
		t.Errorf("UID of a closed connection = %d, want an error", uid)
	}
}
