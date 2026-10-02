package render

import "testing"

func TestOverlaysDeferWhileArmed(t *testing.T) {
	cv := New(make([]byte, Stride(10)*10), Stride(10), 10, 10)
	var order []string
	cv.Overlay(func(*Canvas) { order = append(order, "inline") })
	cv.BeginOverlays()
	prev := cv.PushClip(Rect{W: 2, H: 2})
	alpha := cv.PushAlpha(0.5)
	cv.Overlay(func(c *Canvas) {
		order = append(order, "deferred")
		if c.clip != (Rect{W: 10, H: 10}) {
			t.Errorf("deferred clip = %+v, want the frame's", c.clip)
		}
		if c.alpha != 0.5 {
			t.Errorf("deferred alpha = %v, want the queued 0.5", c.alpha)
		}
		c.Overlay(func(*Canvas) { order = append(order, "nested") })
	})
	order = append(order, "tree")
	cv.PopAlpha(alpha)
	cv.PopClip(prev)
	cv.FlushOverlays()
	want := []string{"inline", "tree", "deferred", "nested"}
	if len(order) != len(want) {
		t.Fatalf("order = %v, want %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("order = %v, want %v", order, want)
		}
	}
	if cv.alpha != 1 || cv.clip != (Rect{W: 10, H: 10}) {
		t.Errorf("flush left alpha %v clip %+v", cv.alpha, cv.clip)
	}
	// Disarmed again: paints run in place.
	ran := false
	cv.Overlay(func(*Canvas) { ran = true })
	if !ran {
		t.Error("an overlay after the flush was deferred")
	}
}

func TestFocusRingDrawsOnceAfterItsWidget(t *testing.T) {
	cv := New(make([]byte, Stride(4)*4), Stride(4), 4, 4)
	type w struct{ n int }
	focused, other := &w{1}, &w{2}
	var order []string
	cv.MarkFocus(focused, func(*Canvas) { order = append(order, "ring") })
	cv.Painted(other)
	order = append(order, "focused")
	cv.Painted(focused)
	cv.Painted(focused)
	order = append(order, "later")
	cv.FinishFocus()
	if len(order) != 3 || order[0] != "focused" || order[1] != "ring" || order[2] != "later" {
		t.Errorf("order = %v, want the ring once, right after its widget", order)
	}
	// Unreached, the ring still draws, at the end.
	order = nil
	cv.MarkFocus(focused, func(*Canvas) { order = append(order, "ring") })
	cv.Painted(other)
	cv.FinishFocus()
	if len(order) != 1 {
		t.Errorf("an unreached ring drew %d times, want once at the end", len(order))
	}
	// Disarmed: nothing draws.
	cv.Painted(focused)
	cv.FinishFocus()
	if len(order) != 1 {
		t.Error("a finished frame drew the ring again")
	}
}
