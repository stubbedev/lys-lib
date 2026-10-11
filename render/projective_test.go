package render

import (
	"math"
	"testing"
)

// The projective transform set: the homography algebra, the 3-D
// function matrices restricted to the z=0 plane (perspective()'s
// shrink under translateZ, rotateY's flip), and the canvas's
// perspective-correct composite.

func TestM3ApplyInvertRoundTrip(t *testing.T) {
	m := M3{1.2, 0.3, 10, -0.2, 0.9, -4, 0.001, -0.002, 1}
	inv, ok := m.Invert()
	if !ok {
		t.Fatal("an invertible map failed to invert")
	}
	x, y := 33.0, 17.0
	bx, by := m.Apply(x, y)
	rx, ry := inv.Apply(bx, by)
	if math.Abs(rx-x) > 1e-6 || math.Abs(ry-y) > 1e-6 {
		t.Errorf("round trip: (%v, %v) -> (%v, %v)", x, y, rx, ry)
	}
	if _, ok := (M3{1, 0, 0, 0, 1, 0, 0, 0, 0}).Invert(); ok {
		t.Error("a collapsed plane inverted")
	}
}

func TestM3MulIdentity(t *testing.T) {
	m := M3{0.5, 0.1, 3, 0, 2, -1, 0.01, 0, 1}
	if m.Mul(Identity3) != m || Identity3.Mul(m) != m {
		t.Error("identity is not the unit")
	}
}

func TestPerspectiveTranslateZShrinks(t *testing.T) {
	// translateZ(t) under perspective d: w = 1 - t/d, so the plane
	// scales by d/(d-t) about the origin — a zoom in for t > 0.
	d, tz := 500.0, 125.0
	m := Perspective4(d).Mul(TranslateZ4(tz)).Plane3()
	x, y := m.Apply(100, 0)
	if math.Abs(x-100*d/(d-tz)) > 1e-9 || math.Abs(y) > 1e-9 {
		t.Errorf("perspective translateZ maps (100,0) to (%v, %v), want (%v, 0)", x, y, 100*d/(d-tz))
	}
	// At z = 0 exactly, perspective() alone is the identity: a flat
	// plane projects onto itself.
	if p := Perspective4(d).Plane3(); p != Identity3 {
		t.Errorf("perspective on the z=0 plane is not the identity: %v", p)
	}
}

func TestRotateYFlipsBackface(t *testing.T) {
	// rotateY(180deg) mirrors x; its plane determinant goes negative —
	// the flat-pipeline backface reading.
	m := RotateY4(180).Plane3()
	x, _ := m.Apply(10, 0)
	if x > -9.9 {
		t.Errorf("rotateY(180) did not mirror x: %v", x)
	}
	det := m[0]*m[4] - m[1]*m[3]
	if det > 0 {
		t.Errorf("rotateY(180) determinant %v, want negative", det)
	}
	if m := RotateY4(45).Plane3(); m[0]*m[4]-m[1]*m[3] <= 0 {
		t.Error("rotateY(45) claimed a backface")
	}
}

func TestRotateXForeShortens(t *testing.T) {
	// rotateX(90deg) on the plane: every y maps to the same z, so the
	// homography collapses the y axis (a zero row); the determinant
	// reads zero, and 45 degrees halves it through the perspective.
	m := RotateX4(45).Mul(Perspective4(400)).Plane3()
	_, y1 := m.Apply(0, 100)
	if y1 <= 50 || y1 >= 100 {
		t.Errorf("rotateX(45) under perspective maps y=100 to %v, want a foreshortened (50,100)", y1)
	}
}

func TestCompositeProjectiveDrawsZoomed(t *testing.T) {
	// A white 4x4 layer composited under a translateZ zoom covers a
	// larger square about the origin; a point map pins the growth.
	cv, _ := newTestCanvas(16, 16)
	l := filterLayer(t, RGB(255, 255, 255), 4, 4)
	m := Perspective4(32).Mul(TranslateZ4(16)).Plane3()
	cv.CompositeProjective(l, Rect{W: 4, H: 4}, m, 1)
	if a := cv.get(2, 2).A(); a < 250 {
		t.Errorf("the zoomed center faded: alpha %d", a)
	}
	if a := cv.get(6, 6).A(); a == 0 {
		t.Error("the zoom did not grow the footprint")
	}
	if a := cv.get(14, 14).A(); a > 8 {
		t.Errorf("the zoom overreached the far corner: alpha %d", a)
	}
}

func TestSnapshotSamplesBackdrop(t *testing.T) {
	// Snapshot copies the canvas region: the snapshot layer holds the
	// backdrop's own pixels, and filtering it does not touch the
	// canvas until the composite back.
	cv, _ := newTestCanvas(8, 8)
	cv.Clear(cv.Rect(), RGB(10, 20, 30))
	l := cv.Snapshot(nil, Rect{W: 8, H: 8})
	if got := l.cv.get(3, 3); got != RGB(10, 20, 30) {
		t.Errorf("snapshot pixel = %08x", uint32(got))
	}
	var f Filters
	f.Append(FilterOp{Kind: FilterBrightness, Amount: 0})
	ApplyFilters(l, l.cv.Rect(), &f)
	if l.cv.get(3, 3).R() != 0 {
		t.Error("filtering the snapshot left color")
	}
	if cv.get(3, 3) != RGB(10, 20, 30) {
		t.Error("filtering the snapshot touched the canvas")
	}
}
