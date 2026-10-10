package wlr_test

import (
	"encoding/binary"
	"testing"

	"github.com/stubbedev/gelm/internal/headlesstest"
	"github.com/stubbedev/gelm/third_party/neurlang-wayland/wl"
	"github.com/stubbedev/gelm/wlr"
)

type releases struct{ n int }

func (r *releases) HandleBufferRelease(wl.BufferReleaseEvent) { r.n++ }

type created struct{ buf *wlr.Buffer }

func (c *created) HandleZwpBufferParamsV1Created(ev wlr.ZwpBufferParamsV1CreatedEvent) {
	c.buf = ev.Buffer
}

func TestServerCreatedBufferTakesReleaseHandlers(t *testing.T) {
	ln, _ := headlesstest.LoopbackDisplay(t, "wayland-wlr-test")
	t.Cleanup(func() { ln.Close() })
	go func() {
		if conn, err := ln.AcceptUnix(); err == nil {
			t.Cleanup(func() { conn.Close() })
		}
	}()
	disp, err := wl.Connect("")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = disp.Context().Close() })
	params := wlr.NewZwpBufferParamsV1(disp.Context())
	got := &created{}
	params.AddCreatedHandler(got)
	id := make([]byte, 4)
	binary.NativeEndian.PutUint32(id, 0xff000001)
	params.Dispatch(&wl.Event{Opcode: 0, Data: id})
	if got.buf == nil {
		t.Fatal("the created event carried no buffer")
	}
	r := &releases{}
	got.buf.AddReleaseHandler(r)
	got.buf.Dispatch(&wl.Event{Opcode: 0})
	if r.n != 1 {
		t.Errorf("%d release events reached the handler, want 1", r.n)
	}
}
