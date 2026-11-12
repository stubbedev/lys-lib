package native_test

import (
	"context"
	"errors"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stubbedev/lys-lib/internal/goroutine"
	"github.com/stubbedev/lys-lib/pulse/native"
	"github.com/stubbedev/lys-lib/pulse/pulsetest"
)

const closeWait = 5 * time.Second

// goroutineRuns reports whether the goroutine with id still exists.
func goroutineRuns(id uint64) bool {
	buf := make([]byte, 1<<20)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			buf = buf[:n]
			break
		}
		buf = make([]byte, 2*len(buf))
	}
	prefix := "goroutine " + strconv.FormatUint(id, 10) + " "
	for stack := range strings.SplitSeq(string(buf), "\n\n") {
		if strings.HasPrefix(stack, prefix) {
			return true
		}
	}
	return false
}

func receive[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(closeWait):
		t.Fatalf("%s did not happen within %s", what, closeWait)
		var zero T
		return zero
	}
}

func subscribeToSinks(t *testing.T, c *native.Conn) {
	t.Helper()
	if err := c.Subscribe(context.Background(), native.MaskSink); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
}

func TestCloseWaitsForTheEventHandler(t *testing.T) {
	srv := pulsetest.New(t)
	entered := make(chan uint64, 1)
	release := make(chan struct{})
	var returned atomic.Bool
	c := dial(t, srv, native.Options{OnSubscribe: func(native.SubscribeEvent) {
		entered <- goroutine.ID()
		<-release
		returned.Store(true)
	}})
	subscribeToSinks(t, c)
	srv.PutSink(speakers())
	handler := receive(t, entered, "the event handler")

	closed := make(chan error, 1)
	go func() { closed <- c.Close() }()
	receive(t, c.Done(), "the connection ending")
	select {
	case <-closed:
		t.Fatal("Close returned while the event handler was still running")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	if err := receive(t, closed, "Close"); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !returned.Load() {
		t.Error("Close returned before the event handler did")
	}
	if goroutineRuns(handler) {
		t.Error("the event goroutine outlived Close")
	}
}

func TestAnEventHandlerMayCloseItsConnection(t *testing.T) {
	srv := pulsetest.New(t)
	conns := make(chan *native.Conn, 1)
	handler := make(chan uint64, 1)
	errs := make(chan error, 1)
	c := dial(t, srv, native.Options{OnSubscribe: func(native.SubscribeEvent) {
		c := <-conns
		handler <- goroutine.ID()
		if err := c.Close(); err != nil {
			errs <- err
			return
		}
		_, err := c.Sinks(context.Background())
		errs <- err
	}})
	conns <- c
	subscribeToSinks(t, c)
	srv.PutSink(speakers())
	id := receive(t, handler, "the event handler")
	if err := receive(t, errs, "Close from the event handler"); !errors.Is(err, native.ErrClosed) {
		t.Fatalf("a request after Close in the handler = %v, want ErrClosed", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if goroutineRuns(id) {
		t.Error("the event goroutine outlived Close")
	}
}

func TestCloseFailsAHandlersRequestInFlight(t *testing.T) {
	addr, swallowed := swallowingServer(t)
	conns := make(chan *native.Conn, 1)
	errs := make(chan error, 1)
	c, err := native.Dial(context.Background(), native.Options{Server: addr, Timeout: time.Hour, OnSubscribe: func(native.SubscribeEvent) {
		_, err := (<-conns).ServerInfo(context.Background())
		errs <- err
	}})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	conns <- c
	if cmd := receive(t, swallowed, "the handler's request"); cmd != native.CmdGetServerInfo {
		t.Fatalf("the server got %s, want %s", native.CommandName(cmd), native.CommandName(native.CmdGetServerInfo))
	}
	closed := make(chan error, 1)
	go func() { closed <- c.Close() }()
	if err := receive(t, closed, "Close with a handler's request in flight"); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case err := <-errs:
		if !errors.Is(err, native.ErrClosed) {
			t.Fatalf("the handler's request = %v, want ErrClosed", err)
		}
	default:
		t.Fatal("Close returned before the handler's request did")
	}
}

func TestARecordDataHandlerMayCloseItsConnection(t *testing.T) {
	srv := pulsetest.New(t)
	srv.PutSource(monitor())
	c := dial(t, srv, native.Options{})
	errs := make(chan error, 1)
	params := native.RecordParams{
		SampleSpec: native.SampleSpec{Format: native.SampleS16LE, Channels: 1, Rate: 44100},
		ChannelMap: native.ChannelMap{native.ChannelMono},
		Source:     "alsa_output.speakers.monitor",
	}
	if _, err := c.CreateRecordStream(context.Background(), params, func([]byte) { errs <- c.Close() }); err != nil {
		t.Fatal(err)
	}
	srv.WaitRecords(t, 1)[0].Push([]byte{1, 2})
	if err := receive(t, errs, "Close from the record data handler"); err != nil {
		t.Fatalf("Close: %v", err)
	}
	receive(t, c.Done(), "the connection ending")
}
