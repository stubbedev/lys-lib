package render

import (
	"testing"
)

func TestRoundedRectCorners(t *testing.T) {
	cv, data := newTestCanvas(20, 20)
	red := RGB(0xff, 0, 0)
	cv.RoundedRectCorners(Rect{X: 0, Y: 0, W: 20, H: 20}, Corners{TopLeft: 8}, red)
	if got := pxAt(data, Stride(20), 0, 0); got != 0 {
		t.Errorf("rounded top-left corner pixel = %08x, want empty", uint32(got))
	}
	for _, p := range [][2]int{{19, 0}, {19, 19}, {0, 19}, {10, 10}} {
		if got := pxAt(data, Stride(20), p[0], p[1]); got != red {
			t.Errorf("square corner / center %v = %08x, want red", p, uint32(got))
		}
	}
	// Overlapping radii scale down together instead of bulging.
	cv2, data2 := newTestCanvas(10, 10)
	cv2.RoundedRectCorners(Rect{W: 10, H: 10}, UniformCorners(100), red)
	if got := pxAt(data2, Stride(10), 5, 5); got != red {
		t.Errorf("huge radii center = %08x, want a filled pill", uint32(got))
	}
	if got := pxAt(data2, Stride(10), 0, 0); got != 0 {
		t.Errorf("huge radii corner = %08x, want empty", uint32(got))
	}
}

func TestRoundedBorderSides(t *testing.T) {
	cv, data := newTestCanvas(20, 20)
	red, blue := RGB(0xff, 0, 0), RGB(0, 0, 0xff)
	cv.RoundedBorderSides(Rect{W: 20, H: 20}, Corners{}, Insets{Top: 2, Bottom: 3}, [4]Color{red, 0, blue, 0})
	if got := pxAt(data, Stride(20), 10, 0); got != red {
		t.Errorf("top ring = %08x, want red", uint32(got))
	}
	if got := pxAt(data, Stride(20), 10, 1); got != red {
		t.Errorf("top ring row 2 = %08x, want red", uint32(got))
	}
	if got := pxAt(data, Stride(20), 10, 2); got != 0 {
		t.Errorf("inside below the top border = %08x, want empty", uint32(got))
	}
	if got := pxAt(data, Stride(20), 10, 17); got != blue {
		t.Errorf("bottom ring = %08x, want blue", uint32(got))
	}
	if got := pxAt(data, Stride(20), 0, 10); got != 0 {
		t.Errorf("zero-width left side painted %08x", uint32(got))
	}
	// A uniform ring with round corners leaves the corner itself empty
	// and the inside untouched.
	cv2, data2 := newTestCanvas(20, 20)
	cv2.RoundedBorder(Rect{W: 20, H: 20}, UniformCorners(6), UniformInsets(1), red)
	if got := pxAt(data2, Stride(20), 0, 0); got != 0 {
		t.Errorf("rounded ring corner = %08x, want empty", uint32(got))
	}
	if got := pxAt(data2, Stride(20), 10, 0); got != red {
		t.Errorf("rounded ring edge = %08x, want red", uint32(got))
	}
	if got := pxAt(data2, Stride(20), 10, 10); got != 0 {
		t.Errorf("ring interior = %08x, want empty", uint32(got))
	}
	cv3, data3 := newTestCanvas(4, 4)
	cv3.RoundedBorder(Rect{W: 4, H: 4}, Corners{}, Insets{}, red)
	if pxAt(data3, Stride(4), 1, 1) != 0 {
		t.Error("zero widths painted")
	}
}

func TestPaintGradientLinear(t *testing.T) {
	cv, data := newTestCanvas(100, 10)
	black, white := RGB(0, 0, 0), RGB(0xff, 0xff, 0xff)
	cv.PaintGradient(Rect{W: 100, H: 10}, Corners{}, Linear(90, GradientStop{0, black}, GradientStop{1, white}))
	left, mid, right := pxAt(data, Stride(100), 0, 5), pxAt(data, Stride(100), 50, 5), pxAt(data, Stride(100), 99, 5)
	if left.R() > 5 || right.R() < 250 || mid.R() < 120 || mid.R() > 135 {
		t.Errorf("90deg ramp: left %d mid %d right %d", left.R(), mid.R(), right.R())
	}
	// 180deg runs top to bottom: every column in a row is equal.
	cv2, data2 := newTestCanvas(10, 100)
	cv2.PaintGradient(Rect{W: 10, H: 100}, Corners{}, Linear(180, GradientStop{0, black}, GradientStop{0.5, white}, GradientStop{1, black}))
	if a, b := pxAt(data2, Stride(10), 0, 50), pxAt(data2, Stride(10), 9, 50); a != b || a.R() < 245 {
		t.Errorf("180deg middle stop: %08x %08x", uint32(a), uint32(b))
	}
	if top := pxAt(data2, Stride(10), 5, 0); top.R() > 10 {
		t.Errorf("180deg top = %d, want black", top.R())
	}
}

func TestBoxShadowOuterAndInset(t *testing.T) {
	black := RGB(0, 0, 0)
	cv, data := newTestCanvas(40, 40)
	box := Rect{X: 10, Y: 10, W: 20, H: 20}
	cv.BoxShadow(box, Corners{}, BoxShadow{Y: 4, Blur: 0, Color: black})
	// Offset down 4: a solid strip below the box, nothing above, and
	// nothing under the box itself.
	if got := pxAt(data, Stride(40), 20, 32); got != black {
		t.Errorf("below the box = %08x, want the shadow", uint32(got))
	}
	if got := pxAt(data, Stride(40), 20, 8); got != 0 {
		t.Errorf("above the box = %08x, want empty", uint32(got))
	}
	if got := pxAt(data, Stride(40), 20, 20); got != 0 {
		t.Errorf("under the box = %08x, want clipped out", uint32(got))
	}

	cv2, data2 := newTestCanvas(40, 40)
	cv2.BoxShadow(box, Corners{}, BoxShadow{Blur: 8, Spread: 2, Color: black})
	near, far := pxAt(data2, Stride(40), 20, 8), pxAt(data2, Stride(40), 20, 1)
	if near.A() == 0 || far.A() >= near.A() {
		t.Errorf("blur falloff: near %d far %d", near.A(), far.A())
	}

	// Inset `0 2px 0 0`: a 2px band along the top inside edge only.
	cv3, data3 := newTestCanvas(40, 40)
	cv3.BoxShadow(box, Corners{}, BoxShadow{Y: 2, Color: black, Inset: true})
	if got := pxAt(data3, Stride(40), 20, 10); got != black {
		t.Errorf("inset top band = %08x", uint32(got))
	}
	if got := pxAt(data3, Stride(40), 20, 13); got != 0 {
		t.Errorf("inset below the band = %08x", uint32(got))
	}
	if got := pxAt(data3, Stride(40), 20, 9); got != 0 {
		t.Errorf("inset painted outside the box: %08x", uint32(got))
	}
	// Inset spread `0 0 0 1px`: a 1px ring on every inner edge.
	cv4, data4 := newTestCanvas(40, 40)
	cv4.BoxShadow(box, Corners{}, BoxShadow{Spread: 1, Color: black, Inset: true})
	if pxAt(data4, Stride(40), 10, 20) != black || pxAt(data4, Stride(40), 29, 20) != black || pxAt(data4, Stride(40), 20, 20) != 0 {
		t.Error("inset spread ring")
	}
}

func TestBoxShadowExtent(t *testing.T) {
	got := BoxShadow{X: 2, Y: -3, Blur: 4, Spread: 1, Color: RGB(0, 0, 0)}.Extent()
	if got != (Insets{Top: 8, Right: 7, Bottom: 2, Left: 3}) {
		t.Errorf("extent = %+v", got)
	}
	if (BoxShadow{Blur: 4, Color: RGB(0, 0, 0), Inset: true}).Extent() != (Insets{}) {
		t.Error("an inset shadow has no outer extent")
	}
	if (BoxShadow{Blur: 4}).Extent() != (Insets{}) {
		t.Error("a transparent shadow has no extent")
	}
}

func TestPushBrightness(t *testing.T) {
	cv, data := newTestCanvas(2, 1)
	prev := cv.PushBrightness(2)
	cv.FillRect(Rect{W: 1, H: 1}, RGB(0x40, 0x80, 0xc0))
	cv.PopBrightness(prev)
	cv.FillRect(Rect{X: 1, W: 1, H: 1}, RGB(0x40, 0x80, 0xc0))
	if got := pxAt(data, Stride(2), 0, 0); got != RGB(0x80, 0xff, 0xff) {
		t.Errorf("brightness 2 = %08x, want channels doubled and clamped", uint32(got))
	}
	if got := pxAt(data, Stride(2), 1, 0); got != RGB(0x40, 0x80, 0xc0) {
		t.Errorf("after pop = %08x, want unmodulated", uint32(got))
	}
	// Premultiplied stays valid: a translucent color never exceeds its
	// alpha.
	cv2, data2 := newTestCanvas(1, 1)
	cv2.PushBrightness(4)
	cv2.FillRect(Rect{W: 1, H: 1}, RGBA(0xff, 0xff, 0xff, 0x80))
	if got := pxAt(data2, Stride(1), 0, 0); got.R() > got.A() {
		t.Errorf("brightened translucent = %08x, channel above alpha", uint32(got))
	}
}

func TestInsetsShrinkGrow(t *testing.T) {
	r := Rect{X: 10, Y: 10, W: 20, H: 10}
	in := Insets{Top: 1, Right: 2, Bottom: 3, Left: 4}
	if got := in.Shrink(r); got != (Rect{X: 14, Y: 11, W: 14, H: 6}) {
		t.Errorf("shrink = %+v", got)
	}
	if got := in.Grow(in.Shrink(r)); got != r {
		t.Errorf("grow(shrink) = %+v, want %+v", got, r)
	}
	if got := UniformInsets(20).Shrink(r); got.W != 0 || got.H != 0 {
		t.Errorf("over-shrink = %+v, want empty", got)
	}
}
