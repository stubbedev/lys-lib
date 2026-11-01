package wl

import (
	"encoding/binary"
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
