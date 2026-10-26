package render

import "testing"

func TestDrawCanvasDevice(t *testing.T) {
	src := New(make([]byte, Stride(4)*4), Stride(4), 4, 4)
	red := RGB(0xff, 0, 0)
	src.Clear(Rect{W: 4, H: 4}, red)

	dst := New(make([]byte, Stride(6)*6), Stride(6), 6, 6)
	dst.Clear(Rect{W: 6, H: 6}, RGB(0, 0xff, 0))
	dst.DrawCanvasDevice(src, 2, 1)

	if got := dst.get(3, 2); got != red {
		t.Errorf("blitted pixel = %#x, want the source red %#x", got, red)
	}
	if got := dst.get(1, 2); got != RGB(0, 0xff, 0) {
		t.Errorf("pixel left of the blit = %#x, want the destination green", got)
	}
	if got := dst.get(5, 4); got != red {
		t.Errorf("bottom-right blit pixel = %#x, want red: the source edge lands inside", got)
	}

	// The blit honors the clip: nothing is written outside it.
	dst2 := New(make([]byte, Stride(6)*6), Stride(6), 6, 6)
	prev := dst2.PushClipDevice(Rect{X: 2, Y: 1, W: 1, H: 6})
	dst2.DrawCanvasDevice(src, 2, 1)
	dst2.PopClip(prev)
	if got := dst2.get(4, 2); got != 0 {
		t.Errorf("clipped-out pixel = %#x, want untouched", got)
	}
	if got := dst2.get(2, 2); got != red {
		t.Errorf("pixel inside the clip = %#x, want red", got)
	}
}
