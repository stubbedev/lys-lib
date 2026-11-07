package wl

import "testing"

type countingDone struct{ n int }

func (c *countingDone) HandleCallbackDone(CallbackDoneEvent) { c.n++ }

func TestAProxyTheClientDestroyedIsReusedOnlyOnceItsIDIsDeleted(t *testing.T) {
	ctx, _ := newSocketContext(t)
	ctx.newDisplay()
	gone := NewCallback(ctx)
	var h countingDone
	gone.AddDoneHandler(&h)
	gone.AddDoneHandler(&countingDone{})
	gone.Unregister()
	if again := NewCallback(ctx); again == gone {
		t.Fatal("a proxy whose id the server has not deleted was reused")
	}
	idReaper{ctx}.HandleDisplayDeleteId(DisplayDeleteIdEvent{Id: uint32(gone.Id())})
	if _, ok := ctx.zombies[gone.Id()]; ok {
		t.Fatal("delete_id left the destroyed proxy a zombie")
	}
	reused := NewCallback(ctx)
	if reused != gone {
		t.Fatal("a proxy whose id was deleted was not reused")
	}
	if reused.done != nil || len(reused.more) != 0 {
		t.Fatal("a reused proxy kept its handlers")
	}
	if ctx.LookupProxy(reused.Id()) != reused {
		t.Fatal("a reused proxy is not registered under its new id")
	}
	if h.n != 0 {
		t.Fatalf("the old handler ran %d times", h.n)
	}
}

func TestAProxyTheServerDestroyedIsReused(t *testing.T) {
	ctx, _ := newSocketContext(t)
	ctx.newDisplay()
	gone := NewCallback(ctx)
	idReaper{ctx}.HandleDisplayDeleteId(DisplayDeleteIdEvent{Id: uint32(gone.Id())})
	if NewCallback(ctx) != gone {
		t.Fatal("a proxy the server destroyed was not reused")
	}
}

func TestAClosedContextKeepsNoRetiredProxy(t *testing.T) {
	ctx, _ := newSocketContext(t)
	ctx.newDisplay()
	gone := NewCallback(ctx)
	if err := ctx.Close(); err != nil {
		t.Fatal(err)
	}
	ctx.retire(gone)
	if ctx.free != nil {
		t.Fatal("a closed context kept a retired proxy")
	}
}
