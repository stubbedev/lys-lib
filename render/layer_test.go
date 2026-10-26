package render

import (
	"math"
	"testing"
)

func approxAffine(a, b Affine) bool {
	near := func(x, y float64) bool { return math.Abs(x-y) < 1e-9 }
	return near(a.A, b.A) && near(a.B, b.B) && near(a.C, b.C) && near(a.D, b.D) && near(a.E, b.E) && near(a.F, b.F)
}

func TestAffine(t *testing.T) {
	// Mul applies the right operand first, like a snapshot chain.
	m := Translate(10, 0).Mul(Scale(2, 2))
	if x, y := m.Apply(1, 1); x != 12 || y != 2 {
		t.Errorf("translate∘scale (1,1) = (%v,%v), want (12,2)", x, y)
	}
	if x, y := Scale(2, 2).Mul(Translate(10, 0)).Apply(1, 1); x != 22 || y != 2 {
		t.Errorf("scale∘translate (1,1) = (%v,%v), want (22,2)", x, y)
	}
	// Rotation is clockwise on a y-down screen: +x turns into +y.
	if x, y := Rotate(90).Apply(1, 0); math.Abs(x) > 1e-12 || math.Abs(y-1) > 1e-12 {
		t.Errorf("rotate(90) (1,0) = (%v,%v), want (0,1)", x, y)
	}
	// About keeps the pivot fixed.
	if x, y := Scale(3, 3).About(5, 5).Apply(5, 5); x != 5 || y != 5 {
		t.Errorf("scale about (5,5) moved the pivot to (%v,%v)", x, y)
	}
	for _, a := range []Affine{Identity, Translate(3, -4), Scale(2, 0.5), Rotate(33).Mul(Scale(1.5, 2)).About(7, 9)} {
		inv, ok := a.Invert()
		if !ok || !approxAffine(a.Mul(inv), Identity) {
			t.Errorf("%+v: inverse %+v (ok %v) does not undo it", a, inv, ok)
		}
	}
	if _, ok := Scale(0, 1).Invert(); ok {
		t.Error("a collapsed axis inverted")
	}
	if got := Scale(2, 2).About(10, 10).MapBounds(Rect{X: 5, Y: 5, W: 10, H: 10}); got != (Rect{X: 0, Y: 0, W: 20, H: 20}) {
		t.Errorf("MapBounds of a scale = %+v", got)
	}
	if got := Rotate(45).MapBounds(Rect{W: 10, H: 10}); got != (Rect{X: -8, Y: 0, W: 16, H: 15}) {
		t.Errorf("MapBounds of a rotation = %+v, want the rotated square's cover", got)
	}
	if got := Identity.MapBounds(Rect{}); !got.Empty() {
		t.Errorf("an empty rect mapped to %+v", got)
	}
}

// layerCanvas is a w x h canvas cleared to bg.
func layerCanvas(w, h int, bg Color) *Canvas {
	c := New(make([]byte, Stride(w)*h), Stride(w), w, h)
	c.ClearDevice(c.Rect(), bg)
	return c
}

func TestLayerReuseAndClear(t *testing.T) {
	c := layerCanvas(8, 8, RGB(0, 0, 0))
	l := c.Layer(nil, Rect{X: 1, Y: 1, W: 4, H: 4})
	l.Canvas().FillRect(Rect{W: 8, H: 8}, RGB(255, 0, 0))
	if got := l.Canvas().get(0, 0); got != 0 {
		t.Errorf("drawing escaped the region: (0,0) = %v", got)
	}
	if got := l.Canvas().get(2, 2); got != RGB(255, 0, 0) {
		t.Errorf("drawing missed the region: (2,2) = %v", got)
	}
	again := c.Layer(l, Rect{X: 1, Y: 1, W: 4, H: 4})
	if again != l {
		t.Error("a same-size layer was reallocated")
	}
	if got := again.Canvas().get(2, 2); got != 0 {
		t.Errorf("the region was not cleared: %v", got)
	}
	if other := layerCanvas(9, 8, 0).Layer(l, Rect{W: 2, H: 2}); other == l {
		t.Error("a layer of the wrong size was reused")
	}
}

func TestCompositeTranslationCopies(t *testing.T) {
	c := layerCanvas(10, 10, RGB(0, 0, 0))
	src := Rect{X: 2, Y: 2, W: 3, H: 3}
	l := c.Layer(nil, src)
	l.Canvas().FillRect(src, RGB(0, 255, 0))
	l.Canvas().FillRect(Rect{X: 2, Y: 2, W: 1, H: 1}, RGB(255, 0, 0))

	c.Composite(l, src, Translate(4, 1), 1)

	if got := c.get(6, 3); got != RGB(255, 0, 0) {
		t.Errorf("the marked corner did not land at (6,3): %v", got)
	}
	if got := c.get(8, 5); got != RGB(0, 255, 0) {
		t.Errorf("(8,5) = %v, want the copied green", got)
	}
	for _, p := range [][2]int{{5, 3}, {9, 3}, {6, 2}, {6, 6}, {2, 2}} {
		if got := c.get(p[0], p[1]); got != RGB(0, 0, 0) {
			t.Errorf("(%d,%d) = %v outside the copy, want the background", p[0], p[1], got)
		}
	}
}

func TestCompositeAlphaAndClip(t *testing.T) {
	c := layerCanvas(6, 6, RGB(0, 0, 0))
	src := Rect{W: 6, H: 6}
	l := c.Layer(nil, src)
	l.Canvas().FillRect(src, RGB(255, 255, 255))
	prev := c.PushClip(Rect{W: 3, H: 6})
	c.Composite(l, src, Identity, 0.5)
	c.PopClip(prev)
	if got := c.get(1, 1); got.R() < 120 || got.R() > 135 {
		t.Errorf("half alpha over black = %v, want mid gray", got)
	}
	if got := c.get(4, 1); got != RGB(0, 0, 0) {
		t.Errorf("drawn outside the clip: %v", got)
	}
	c.Composite(l, src, Identity, 0)
	c.Composite(l, src, Scale(0, 1), 1)
	if got := c.get(4, 1); got != RGB(0, 0, 0) {
		t.Errorf("a zero alpha or a collapsed map drew: %v", got)
	}
}

func TestCompositeTransforms(t *testing.T) {
	// A 4x4 block whose top-left pixel is red, the rest blue.
	src := Rect{X: 4, Y: 4, W: 4, H: 4}
	build := func() (*Canvas, *Layer) {
		c := layerCanvas(16, 16, RGB(0, 0, 0))
		l := c.Layer(nil, src)
		l.Canvas().FillRect(src, RGB(0, 0, 255))
		l.Canvas().FillRect(Rect{X: 4, Y: 4, W: 1, H: 1}, RGB(255, 0, 0))
		return c, l
	}

	// Doubling about the block's center covers 2..10 and keeps the red
	// corner top-left.
	c, l := build()
	c.Composite(l, src, Scale(2, 2).About(6, 6), 1)
	// Bilinear filtering softens the edges; the corner stays red-dominant.
	if got := c.get(3, 3); got.R() <= got.B() {
		t.Errorf("scaled corner (3,3) = %v, want red over blue", got)
	}
	if got := c.get(8, 8); got.B() < 200 {
		t.Errorf("scaled body (8,8) = %v, want blue", got)
	}
	if got := c.get(11, 11); got != RGB(0, 0, 0) {
		t.Errorf("(11,11) = %v past the scaled block, want background", got)
	}

	// A quarter turn about the center moves the red corner top-right.
	c, l = build()
	c.Composite(l, src, Rotate(90).About(6, 6), 1)
	if got := c.get(7, 4); got.R() < 200 {
		t.Errorf("rotated corner (7,4) = %v, want red", got)
	}
	if got := c.get(4, 4); got.B() < 200 || got.R() > 50 {
		t.Errorf("(4,4) after the turn = %v, want blue", got)
	}
}

func TestLayerCrossFade(t *testing.T) {
	c := layerCanvas(4, 1, 0)
	region := Rect{W: 4, H: 1}
	a := c.Layer(nil, region)
	b := c.Layer(nil, region)
	a.Canvas().FillRect(Rect{W: 2, H: 1}, RGB(200, 0, 0))
	b.Canvas().FillRect(Rect{X: 1, W: 3, H: 1}, RGB(0, 0, 100))
	a.CrossFade(b, region, 0.5)
	// Pixel 0: red fading to nothing; 1: red to blue; 2: nothing to blue.
	if got := a.Canvas().get(0, 0); !near8(got.A(), 128) || !near8(got.R(), 100) {
		t.Errorf("red half gone = %v", got)
	}
	if got := a.Canvas().get(1, 0); got.A() != 255 || !near8(got.R(), 100) || !near8(got.B(), 50) {
		t.Errorf("red-to-blue midpoint = %v", got)
	}
	if got := a.Canvas().get(2, 0); !near8(got.A(), 128) || !near8(got.B(), 50) {
		t.Errorf("blue half in = %v", got)
	}
	a.CrossFade(b, region, 1)
	if got := a.Canvas().get(0, 0); got != 0 {
		t.Errorf("at t=1 the old pixel survives: %v", got)
	}
}

func near8(got, want uint8) bool { return got+1 >= want && got <= want+1 }
