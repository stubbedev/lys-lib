//go:build !darwin

package dbus_test

import (
	"context"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stubbedev/lys-lib/dbustest"
	dbus "github.com/stubbedev/lys-lib/third_party/godbus"
)

// goroutineStacks is the stack of every goroutine now, by its header
// line's id.
func goroutineStacks() map[string]string {
	buf := make([]byte, 1<<20)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			buf = buf[:n]
			break
		}
		buf = make([]byte, 2*len(buf))
	}
	stacks := map[string]string{}
	for stack := range strings.SplitSeq(string(buf), "\n\n") {
		id, _, _ := strings.Cut(strings.TrimPrefix(stack, "goroutine "), " ")
		stacks[id] = stack
	}
	return stacks
}

// startedSince are the goroutines running this package's code now that
// were not among before.
func startedSince(before map[string]string) []string {
	var started []string
	for id, stack := range goroutineStacks() {
		if _, old := before[id]; !old && strings.Contains(stack, "third_party/godbus.") {
			started = append(started, stack)
		}
	}
	return started
}

// leftRunning are the goroutines running this package's code, started
// since before, that have not ended within handWait: one that Close
// waited for has left the connection's count and is returning, and one
// of a peer is sending the reply of a method the test let go.
func leftRunning(before map[string]string) []string {
	deadline := time.Now().Add(handWait)
	for {
		left := startedSince(before)
		if len(left) == 0 || time.Now().After(deadline) {
			return left
		}
		runtime.Gosched()
	}
}

type slowHand struct{ entered, release chan struct{} }

func (h slowHand) Take() *dbus.Error {
	h.entered <- struct{}{}
	<-h.release
	return nil
}

func (h slowHand) Quick() *dbus.Error { return nil }

// TestCloseWaitsForTheGoroutinesOfTheConnection is the Close patch
// (lys_close.go): upstream's Close returned while the read loop, the
// context watcher and the watchers of pending calls were still winding
// down. Here the read loop cannot finish before the test lets it: it
// is failing a pending call whose Done channel is full.
func TestCloseWaitsForTheGoroutinesOfTheConnection(t *testing.T) {
	bus := dbustest.Start(t)
	peer := bus.Conn(t)
	hand := slowHand{entered: make(chan struct{}, 1), release: make(chan struct{})}
	if err := peer.Export(hand, handPath, handIface); err != nil {
		t.Fatal(err)
	}
	before := goroutineStacks()

	conn, err := dbus.Connect(bus.Address)
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.Export(hand, handPath, handIface); err != nil {
		t.Fatal(err)
	}
	conn.DispatchInOrder(handIface, "Quick")
	signals := make(chan *dbus.Signal)
	conn.Signal(signals)
	if err := conn.AddMatchSignal(dbus.WithMatchInterface(handIface)); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if err := peer.Emit(handPath, handIface+".Plain", "undelivered"); err != nil {
			t.Fatal(err)
		}
	}
	for _, method := range []string{"Quick", "Missing"} {
		_ = peer.Object(conn.Names()[0], handPath).Call(handIface+"."+method, 0)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	waiting := conn.Object(peer.Names()[0], handPath).GoWithContext(ctx, handIface+".Take", 0, make(chan *dbus.Call, 1))
	<-hand.entered
	full := make(chan *dbus.Call, 1)
	full <- nil
	held := conn.Object(peer.Names()[0], handPath).Go(handIface+".Take", 0, full)
	<-hand.entered
	if len(startedSince(before)) == 0 {
		t.Fatal("the connection runs no goroutine the test can see")
	}

	closed := make(chan error, 1)
	go func() { closed <- conn.Close() }()
	select {
	case err := <-closed:
		t.Fatalf("Close returned (%v) while the read loop was still failing a pending call", err)
	case <-time.After(50 * time.Millisecond):
	}
	if got := <-full; got != nil {
		t.Fatalf("the full channel held %v", got)
	}
	select {
	case err := <-closed:
		if err != nil {
			t.Errorf("Close = %v", err)
		}
	case <-time.After(handWait):
		t.Fatal("Close did not return once the read loop could finish")
	}
	if got := <-full; got != held || held.Err == nil {
		t.Errorf("the held call ended as %v with %v, want it failed by the connection closing", got, held.Err)
	}
	if got := <-waiting.Done; got.Err == nil {
		t.Error("the call still waiting for its reply ended without an error")
	}
	close(hand.release)
	for _, stack := range leftRunning(before) {
		t.Errorf("a goroutine of the connection is still running after Close returned and the peer's methods were let go:\n%s", stack)
	}
}

type closingHand struct {
	conn   *dbus.Conn
	closed chan error
}

func (h closingHand) Quit() *dbus.Error {
	h.closed <- h.conn.Close()
	return nil
}

// A method of the connection may close it: Close does not wait for the
// goroutine that is running the application's own code.
func TestAMethodMayCloseItsOwnConnection(t *testing.T) {
	for _, ordered := range []bool{false, true} {
		server, client := pairedConns(t)
		hand := closingHand{conn: server, closed: make(chan error, 1)}
		if err := server.Export(hand, handPath, handIface); err != nil {
			t.Fatal(err)
		}
		if ordered {
			server.DispatchInOrder(handIface, "Quit")
		}
		_ = client.Object(server.Names()[0], handPath).Call(handIface+".Quit", 0)
		select {
		case err := <-hand.closed:
			if err != nil {
				t.Errorf("ordered %v: Close from a method = %v", ordered, err)
			}
		case <-time.After(handWait):
			t.Fatalf("ordered %v: Close called from a method of the connection never returned", ordered)
		}
	}
}

// Closing through the connection's context, as dbusx's bounded dial
// does when it gives up, ends everything the connection started too,
// and a Close after it has nothing left to wait for.
func TestAConnectionClosedByItsContextLeavesNoGoroutine(t *testing.T) {
	bus := dbustest.Start(t)
	before := goroutineStacks()
	ctx, cancel := context.WithCancel(context.Background())
	conn, err := dbus.Connect(bus.Address, dbus.WithContext(ctx))
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	<-conn.Context().Done()
	if err := conn.Close(); err != nil {
		t.Errorf("Close = %v", err)
	}
	for _, stack := range leftRunning(before) {
		t.Errorf("a goroutine of the connection is still running after Close returned:\n%s", stack)
	}
}
