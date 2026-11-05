package capture

import (
	"slices"
	"testing"

	"github.com/stubbedev/lys-lib/third_party/wayland/wl"
	"github.com/stubbedev/lys-lib/wlr"
)

func TestABufferIsStaleByTheFramesPublishedSinceItWasFilled(t *testing.T) {
	var b bufferDamage
	a, c := &wl.Buffer{}, &wl.Buffer{}
	if _, whole := b.stale(a, 1); !whole {
		t.Fatal("a new buffer is not damaged whole")
	}
	b.filled(a, 1, []Rect{{Width: 8, Height: 8}}, true)
	b.filled(c, 1, []Rect{{X: 1, Width: 1, Height: 1}}, true)
	b.filled(c, 1, []Rect{{X: 2, Width: 1, Height: 1}}, true)
	if rects, whole := b.stale(a, 1); whole || !slices.Equal(rects, []Rect{{X: 1, Width: 1, Height: 1}, {X: 2, Width: 1, Height: 1}}) {
		t.Errorf("stale by %v, whole %v", rects, whole)
	}
	if rects, whole := b.stale(c, 1); whole || len(rects) != 0 {
		t.Errorf("the buffer just filled is stale by %v, whole %v", rects, whole)
	}
	if _, whole := b.stale(a, 2); !whole {
		t.Error("a buffer filled under other constraints is not damaged whole")
	}
	b.filled(c, 1, nil, true)
	if _, whole := b.stale(a, 1); !whole {
		t.Error("a frame damaged whole left another buffer partly stale")
	}
	b.filled(a, 1, nil, false)
	if _, whole := b.stale(a, 1); !whole {
		t.Error("a failed frame left its buffer trusted")
	}
	b.filled(c, 1, nil, true)
	for i := range maxStaleRects + 4 {
		b.filled(a, 1, []Rect{{X: i, Width: 1, Height: 1 + i}}, true)
	}
	if rects, whole := b.stale(c, 1); whole || len(rects) > maxStaleRects || !slices.Contains(rects, Rect{Width: maxStaleRects, Height: maxStaleRects}) {
		t.Errorf("many stale rects merged into %v, whole %v", rects, whole)
	}
}

func TestASessionForgetsTheOldestOfTooManyBuffers(t *testing.T) {
	var b bufferDamage
	bufs := make([]*wl.Buffer, maxStaleBuffers+1)
	for i := range bufs {
		bufs[i] = &wl.Buffer{}
		b.filled(bufs[i], 1, []Rect{{Width: 1, Height: 1}}, true)
	}
	if _, whole := b.stale(bufs[0], 1); !whole {
		t.Error("the evicted buffer is not damaged whole")
	}
	if _, whole := b.stale(bufs[maxStaleBuffers], 1); whole {
		t.Error("the newest buffer is damaged whole")
	}
}

func TestASteadyStreamFrameAllocatesNothing(t *testing.T) {
	s := &Stream{ring: make([]*shmBuffer, streamRing), slots: make([]streamSlot, streamRing)}
	var b bufferDamage
	bufs := make([]*wl.Buffer, streamRing)
	for i := range bufs {
		bufs[i] = &wl.Buffer{}
	}
	var seq uint64
	frame := func() {
		slot := int(seq % streamRing)
		fs := &s.slots[slot].events
		fs.reset()
		b.stale(bufs[slot], 1)
		fs.HandleImageCopyCaptureFrameV1Damage(wlr.ImageCopyCaptureFrameV1DamageEvent{X: int32(seq % 64), Y: 1, Width: 2, Height: 2})
		fs.HandleImageCopyCaptureFrameV1Damage(wlr.ImageCopyCaptureFrameV1DamageEvent{X: 5, Y: int32(seq % 32), Width: 3, Height: 1})
		fs.HandleImageCopyCaptureFrameV1Ready(wlr.ImageCopyCaptureFrameV1ReadyEvent{})
		b.filled(bufs[slot], 1, fs.info.Damage, fs.ready)
		s.publish(slot, seq)
		seq++
	}
	for range 2 * streamRing {
		frame()
	}
	if n := testing.AllocsPerRun(100, frame); n != 0 {
		t.Errorf("a steady stream frame allocated %v times", n)
	}
	if got := s.Latest(); got == nil || got.Seq != seq-1 || len(got.Damage) != 2 {
		t.Errorf("the latest frame is %+v", got)
	}
}
