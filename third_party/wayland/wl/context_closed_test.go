package wl

import (
	"errors"
	"sync"
	"syscall"
	"testing"
)

func TestARequestOnAClosedContextFailsWithoutPanicking(t *testing.T) {
	ctx, _ := newSocketContext(t)
	display := ctx.newDisplay()
	live := NewCallback(ctx)
	if err := ctx.Close(); err != nil {
		t.Fatal(err)
	}
	cb, err := display.Sync()
	if !errors.Is(err, ErrContextClosed) || !errors.Is(err, ErrContextConnNil) {
		t.Fatalf("sync on a closed context returned %v, want %v", err, ErrContextClosed)
	}
	if cb.Context() != ctx {
		t.Fatal("a proxy made on a closed context does not name it")
	}
	if ctx.LookupProxy(cb.Id()) != nil {
		t.Fatal("a closed context tracks a proxy made after it closed")
	}
	if _, err := NewSurface(ctx).Frame(); !errors.Is(err, ErrContextClosed) {
		t.Fatalf("frame on a closed context returned %v, want %v", err, ErrContextClosed)
	}
	live.Unregister()
	cb.Unregister()
	ctx.RegisterMapped(NewCallback(ctx), 0xff000000)
	idReaper{ctx}.HandleDisplayDeleteId(DisplayDeleteIdEvent{Id: uint32(live.Id())})
	if ctx.isZombie(live.Id()) || ctx.LookupProxy(0xff000000) != nil {
		t.Fatal("a closed context tracks proxies again")
	}
	if err := ctx.Close(); err != nil {
		t.Fatalf("a second close: %v", err)
	}
}

func TestClosingAContextWhileAnotherGoroutineMakesProxiesFailsTheirRequests(t *testing.T) {
	ctx, peer := newSocketContext(t)
	display := ctx.newDisplay()
	var wg sync.WaitGroup
	wg.Go(func() {
		buf := make([]byte, 4096)
		for {
			if n, err := syscall.Read(peer, buf); n <= 0 || err != nil {
				return
			}
		}
	})
	errs := make(chan error, 1)
	wg.Go(func() {
		for {
			if _, err := display.Sync(); err != nil {
				errs <- err
				return
			}
		}
	})
	if err := ctx.Close(); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	if err := <-errs; !errors.Is(err, ErrContextClosed) {
		t.Fatalf("sync racing close returned %v, want %v", err, ErrContextClosed)
	}
}
