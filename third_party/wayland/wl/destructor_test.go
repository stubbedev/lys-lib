package wl

import (
	"encoding/binary"
	"errors"
	"syscall"
	"testing"
)

func TestDestructorRequestUnregistersTheProxy(t *testing.T) {
	ctx, _ := newSocketContext(t)
	ctx.newDisplay()
	src := NewDataSource(ctx)
	if err := src.Offer("text/plain"); err != nil {
		t.Fatal(err)
	}
	if ctx.LookupProxy(src.Id()) == nil {
		t.Fatal("an ordinary request unregistered the proxy")
	}
	if err := src.Destroy(); err != nil {
		t.Fatal(err)
	}
	if ctx.LookupProxy(src.Id()) != nil {
		t.Fatal("a destroyed proxy is still registered, keeping its handlers alive")
	}
	if _, ok := ctx.zombies[src.Id()]; !ok {
		t.Fatal("a destroyed fd-receiving proxy is not kept until its id is deleted")
	}
}

func TestDeleteIDForgetsAServerDestroyedObject(t *testing.T) {
	ctx, sock := newSocketContext(t)
	ctx.newDisplay()
	gone, kept := NewCallback(ctx), NewCallback(ctx)
	msg := make([]byte, 12)
	binary.NativeEndian.PutUint32(msg[0:], 1)
	binary.NativeEndian.PutUint32(msg[4:], uint32(len(msg))<<16|1)
	binary.NativeEndian.PutUint32(msg[8:], uint32(gone.Id()))
	if _, err := syscall.Write(sock, msg); err != nil {
		t.Fatal(err)
	}
	if err := ctx.Run(); err != nil {
		t.Fatal(err)
	}
	if ctx.LookupProxy(gone.Id()) != nil {
		t.Fatal("an object the server destroyed stays registered, keeping its handlers alive")
	}
	if ctx.LookupProxy(kept.Id()) == nil {
		t.Fatal("delete_id forgot another object")
	}
}

func sendEvent(t *testing.T, sock int, id ProxyId) {
	t.Helper()
	msg := make([]byte, 8)
	binary.NativeEndian.PutUint32(msg[0:], uint32(id))
	binary.NativeEndian.PutUint32(msg[4:], uint32(len(msg))<<16)
	if _, err := syscall.Write(sock, msg); err != nil {
		t.Fatal(err)
	}
}

func TestAnEventInFlightForADestroyedObjectIsIgnored(t *testing.T) {
	ctx, sock := newSocketContext(t)
	ctx.newDisplay()
	gone := NewCallback(ctx)
	gone.Unregister()
	sendEvent(t, sock, gone.Id())
	if err := ctx.Run(); err != nil {
		t.Fatalf("an event the compositor sent before it saw the destroy: %v", err)
	}
}

func TestRunTillOutlivesAnEventForADestroyedObject(t *testing.T) {
	ctx, sock := newSocketContext(t)
	ctx.newDisplay()
	gone, wanted := NewCallback(ctx), NewCallback(ctx)
	gone.Unregister()
	sendEvent(t, sock, gone.Id())
	sendEvent(t, sock, wanted.Id())
	if err := ctx.RunTill(wanted); err != nil {
		t.Fatalf("waiting past an event for a destroyed object: %v", err)
	}
}

func TestAnEventForAnUnknownObjectIsStillAnError(t *testing.T) {
	ctx, sock := newSocketContext(t)
	ctx.newDisplay()
	sendEvent(t, sock, 12345)
	if err := ctx.Run(); !errors.Is(err, ErrContextRunProxyNil) {
		t.Fatalf("an event for an id nothing ever held: %v, want %v", err, ErrContextRunProxyNil)
	}
}

func TestAnEventAfterDeleteIDIsAnError(t *testing.T) {
	ctx, sock := newSocketContext(t)
	ctx.newDisplay()
	gone := NewCallback(ctx)
	gone.Unregister()
	idReaper{ctx}.HandleDisplayDeleteId(DisplayDeleteIdEvent{Id: uint32(gone.Id())})
	sendEvent(t, sock, gone.Id())
	if err := ctx.Run(); !errors.Is(err, ErrContextRunProxyNil) {
		t.Fatalf("an event for a retired id: %v, want %v", err, ErrContextRunProxyNil)
	}
}
