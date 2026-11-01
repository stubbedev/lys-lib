package wl

import "testing"

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
