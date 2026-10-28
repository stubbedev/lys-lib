package dbus_test

import (
	"fmt"
	"io"
	"os"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/stubbedev/lys-lib/dbustest"
	dbus "github.com/stubbedev/lys-lib/third_party/godbus"
)

// pairedConns is two peers on a private bus.
func pairedConns(t *testing.T) (server, client *dbus.Conn) {
	t.Helper()
	bus := dbustest.Start(t)
	return bus.Conn(t), bus.Conn(t)
}

type fileReplier struct{ f *os.File }

func (r fileReplier) Take() (*os.File, *dbus.Error) { return r.f, nil }

// TestAFileReplyIsSentThenClosed is the third lys-lib patch: a method
// replying an *os.File sends its descriptor as 'h' and godbus closes
// the file after the reply.
func TestAFileReplyIsSentThenClosed(t *testing.T) {
	if dbus.SignatureOf((*os.File)(nil)).String() != "h" {
		t.Fatalf("*os.File signature = %s", dbus.SignatureOf((*os.File)(nil)))
	}
	server, client := pairedConns(t)
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if err := server.Export(fileReplier{w}, "/f", "a.b"); err != nil {
		t.Fatal(err)
	}
	var fd dbus.UnixFD
	if err := client.Object(server.Names()[0], "/f").Call("a.b.Take", 0).Store(&fd); err != nil {
		t.Fatal(err)
	}
	got := os.NewFile(uintptr(fd), "got")
	if _, err := got.Write([]byte("through")); err != nil {
		t.Fatal(err)
	}
	_ = got.Close()
	// The server's copy is closed: with the received one closed too, the
	// read end sees EOF after the payload (not a read that waits on).
	_ = r.SetReadDeadline(time.Now().Add(2 * time.Second))
	b, err := io.ReadAll(r)
	if err != nil || string(b) != "through" {
		t.Errorf("read %q, %v", b, err)
	}
	if err := w.Close(); err == nil {
		t.Error("the replied file was left open")
	}
}

// recorder logs the calls it serves; Slow takes its time first, or
// waits for Fast when release is set.
type recorder struct {
	mu      sync.Mutex
	got     []string
	release chan struct{}
}

func (r *recorder) log(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.got = append(r.got, name)
}

func (r *recorder) seen() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.got)
}

func (r *recorder) Slow() *dbus.Error {
	if r.release != nil {
		<-r.release
	} else {
		time.Sleep(50 * time.Millisecond)
	}
	r.log("slow")
	return nil
}

func (r *recorder) Fast() *dbus.Error {
	r.log("fast")
	if r.release != nil {
		close(r.release)
	}
	return nil
}

// waitSeen waits until r served n calls.
func waitSeen(t *testing.T, r *recorder, n int) []string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if got := r.seen(); len(got) >= n {
			return got
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("served %v, want %d calls", r.seen(), n)
	return nil
}

// TestOrderedCallsRunInArrivalOrder is the fourth lys-lib patch:
// calls to members DispatchInOrder names run one at a time in arrival
// order, even when the caller does not wait for replies; calls to
// other members still run concurrently, so one that blocks does not
// hold back the next.
func TestOrderedCallsRunInArrivalOrder(t *testing.T) {
	server, client := pairedConns(t)
	ordered := &recorder{}
	if err := server.Export(ordered, "/o", "a.ordered"); err != nil {
		t.Fatal(err)
	}
	server.DispatchInOrder("a.ordered", "Slow", "Fast")
	free := &recorder{release: make(chan struct{})}
	if err := server.Export(free, "/f", "a.free"); err != nil {
		t.Fatal(err)
	}
	o := client.Object(server.Names()[0], "/o")
	for range 3 {
		o.Go("a.ordered.Slow", dbus.FlagNoReplyExpected, nil)
		o.Go("a.ordered.Fast", dbus.FlagNoReplyExpected, nil)
	}
	if got := waitSeen(t, ordered, 6); !slices.Equal(got, []string{"slow", "fast", "slow", "fast", "slow", "fast"}) {
		t.Errorf("ordered calls ran %v, want them in arrival order", got)
	}

	f := client.Object(server.Names()[0], "/f")
	f.Go("a.free.Slow", dbus.FlagNoReplyExpected, nil)
	f.Go("a.free.Fast", dbus.FlagNoReplyExpected, nil)
	if got := waitSeen(t, free, 2); !slices.Equal(got, []string{"fast", "slow"}) {
		t.Errorf("unordered calls ran %v, want the blocked Slow overtaken", got)
	}
}

type pinger struct{}

func (pinger) Ping() *dbus.Error { return nil }

// TestConcurrentExportsAndCalls is the fifth lys-lib patch: Export
// and unexport from many goroutines while calls and introspection look
// paths up. The handler's object table was read without its lock and
// an object's first interface was added in two steps, so under -race
// this reports a data race, and without it a call could miss an object
// mid-export.
func TestConcurrentExportsAndCalls(t *testing.T) {
	server, client := pairedConns(t)
	var wg sync.WaitGroup
	for g := range 8 {
		wg.Go(func() {
			for i := range 50 {
				path := dbus.ObjectPath(fmt.Sprintf("/race/g%d/o%d", g, i%5))
				if err := server.Export(pinger{}, path, "a.ping"); err != nil {
					t.Error(err)
					return
				}
				if err := server.Export(pinger{}, path, "a.other"); err != nil {
					t.Error(err)
					return
				}
				obj := client.Object(server.Names()[0], path)
				_ = obj.Call("org.freedesktop.DBus.Introspectable.Introspect", 0).Err
				_ = server.Export(nil, path, "a.ping")
				_ = server.Export(nil, path, "a.other")
			}
		})
	}
	wg.Wait()
	if left := dbus.ExportedObjects(server); left != 0 {
		t.Errorf("%d objects left after every interface was unexported", left)
	}
}
