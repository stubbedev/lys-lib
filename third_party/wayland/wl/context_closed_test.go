package wl

import (
	"errors"
	"os"
	"sync"
	"syscall"
	"testing"
	"time"
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

func TestWaitingForEventsWhileAnotherGoroutineClosesFailsAsClosed(t *testing.T) {
	ctx, _ := newSocketContext(t)
	if _, err := ctx.ReadReady(); err != nil {
		t.Fatal(err)
	}
	if err := ctx.SetReadDeadline(time.Unix(1, 0)); err != nil {
		t.Fatal(err)
	}
	waiting := make(chan struct{})
	errs := make(chan error, 1)
	go func() {
		close(waiting)
		for {
			if err := ctx.WaitReadable(); !errors.Is(err, os.ErrDeadlineExceeded) {
				errs <- err
				return
			}
		}
	}()
	<-waiting
	if err := ctx.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-errs; !errors.Is(err, ErrContextClosed) || !errors.Is(err, ErrContextConnNil) {
		t.Fatalf("a wait racing close returned %v, want %v", err, ErrContextClosed)
	}
	if err := ctx.WaitReadable(); !errors.Is(err, ErrContextClosed) {
		t.Fatalf("a wait on a closed context returned %v, want %v", err, ErrContextClosed)
	}
	if err := ctx.WaitWritable(); !errors.Is(err, ErrContextClosed) {
		t.Fatalf("a wait for room on a closed context returned %v, want %v", err, ErrContextClosed)
	}
	if _, err := ctx.ReadReady(); !errors.Is(err, ErrContextClosed) {
		t.Fatalf("a read on a closed context returned %v, want %v", err, ErrContextClosed)
	}
	if err := ctx.SetReadDeadline(time.Now()); !errors.Is(err, ErrContextClosed) {
		t.Fatalf("a deadline on a closed context returned %v, want %v", err, ErrContextClosed)
	}
}

func TestReadingEventsWhileAnotherGoroutineClosesFailsAsClosed(t *testing.T) {
	ctx, _ := newSocketContext(t)
	reading := make(chan struct{})
	errs := make(chan error, 1)
	go func() {
		close(reading)
		for {
			if err := ctx.Run(); err != nil {
				errs <- err
				return
			}
		}
	}()
	<-reading
	if err := ctx.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-errs; !errors.Is(err, ErrContextClosed) {
		t.Fatalf("a read racing close returned %v, want %v", err, ErrContextClosed)
	}
	if err := ctx.Run(); !errors.Is(err, ErrContextClosed) {
		t.Fatalf("a read on a closed context returned %v, want %v", err, ErrContextClosed)
	}
}
