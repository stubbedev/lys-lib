package pwtest_test

import (
	"sync"
	"testing"

	"github.com/stubbedev/lys-lib/pipewire"
	"github.com/stubbedev/lys-lib/pipewire/pwtest"
)

// once is a source of one frame of 0x55.
type once struct{ sent bool }

func (o *once) Fill(dst []byte) ([]pipewire.Rect, bool) {
	if o.sent {
		return nil, false
	}
	o.sent = true
	for i := range dst {
		dst[i] = 0x55
	}
	return nil, true
}

// arrow is a pointer of a 2 x 2 image that moves when told to.
type arrow struct {
	mu    sync.Mutex
	state pipewire.CursorState
	wake  func()
}

func (a *arrow) CursorState() pipewire.CursorState {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.state
}

func (a *arrow) CursorBitmap(uint64, []byte, int) (pipewire.CursorBitmap, bool) {
	return pipewire.CursorBitmap{Format: pipewire.VideoBGRA, Width: 2, Height: 2, Stride: 8}, true
}

func (a *arrow) NotifyCursor(wake func()) { a.wake = wake }

func (a *arrow) moveTo(x, y int32) {
	a.mu.Lock()
	a.state.X, a.state.Y = x, y
	a.mu.Unlock()
	a.wake()
}

// The reference consumer asks for the cursor meta and reports it: the
// bitmap with the first frame, then a move in a buffer without a frame.
func TestTheConsumerReadsTheCursorMeta(t *testing.T) {
	pwtest.Start(t)
	cur := &arrow{state: pipewire.CursorState{Visible: true, X: 4, Y: 5, HotspotX: 1, Shape: 1}}
	p, err := pipewire.Start(pipewire.Config{
		Application: "pwtest", Name: "pwtest-producer", Width: 64, Height: 32, Stride: 256,
		Format: pipewire.VideoBGRx, FPS: 120, Source: &once{}, Cursor: cur,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	c := pwtest.Consume(t, "pwtest-producer")
	var first pwtest.Buffer
	for first.Size == 0 {
		first = c.Buffer(t)
	}
	if first.Corrupted || first.Cursor == nil || first.Cursor.ID == 0 || first.Cursor.X != 4 || first.Cursor.HotspotX != 1 {
		t.Fatalf("the first frame %+v cursor %+v", first, first.Cursor)
	}
	if bm := first.Cursor.Bitmap; bm == nil || bm.Width != 2 || bm.Height != 2 || bm.Stride != 8 || bm.Format != uint32(pipewire.VideoBGRA) {
		t.Fatalf("the first frame's bitmap %+v", bm)
	}
	cur.moveTo(9, 10)
	for {
		b := c.Buffer(t)
		if b.Cursor == nil || b.Cursor.X != 9 {
			continue
		}
		if b.Size != 0 || !b.Corrupted || b.Cursor.Y != 10 || b.Cursor.Bitmap != nil {
			t.Fatalf("the move came as %+v cursor %+v", b, b.Cursor)
		}
		return
	}
}
