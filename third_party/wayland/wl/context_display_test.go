package wl

import (
	"syscall"
	"testing"
	"time"
)

// The display proxy gets id 1 from Register like any other proxy, but the
// compositor already owns that id and no request ever carries it as a
// new_id. If Register counted it as unsent, the first request that does
// introduce an id -- wl_display.get_registry, id 2 -- would wait for id 1
// forever, and a client would sit on a fresh connection without a window.
func TestFirstRequestAfterConnectDoesNotWaitForDisplay(t *testing.T) {
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Close(fds[0])
	defer syscall.Close(fds[1])
	ctx := &Context{sockFD: fds[0], objects: make(map[ProxyId]Proxy)}
	display := NewDisplay(ctx)

	done := make(chan error, 1)
	go func() {
		_, err := display.GetRegistry()
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("wl_display.get_registry never reached the wire: it waits for id 1 (the display), which no request ever carries")
	}
}
