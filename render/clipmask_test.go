package render

import "testing"

// The clip-path geometry set: each shape's coverage raster probed at
// points solidly inside, solidly outside, and across the edge, plus
// the SVG path data parser the path() form feeds.

// maskedLayer builds a layer of solid white with p applied as its
// clip mask over the given region.
func maskedLayer(t *testing.T, p *Path, region Rect) *Layer {
	t.Helper()
	l := filterLayer(t, RGB(255, 255, 255), region.W, region.H)
	l.ApplyPathMask(p, region)
	return l
}

func TestClipPathInsetCutsCorners(t *testing.T) {
	// inset(0 round 12) on a 24x24 box: the center stays, the corners
	// go, the edge midpoints stay.
	p := &Path{}
	RoundedRectPath(p, Rect{W: 24, H: 24}, Corners{TopLeft: 12, TopRight: 12, BottomRight: 12, BottomLeft: 12})
	l := maskedLayer(t, p, Rect{W: 24, H: 24})
	for _, tc := range []struct {
		x, y int
		want uint8
	}{
		{12, 12, 255},
		{1, 1, 0},
		{22, 22, 0},
		{12, 1, 255},
		{1, 12, 255},
	} {
		if got := l.cv.get(tc.x, tc.y).A(); got != tc.want {
			t.Errorf("inset round corner at (%d,%d): alpha %d, want %d", tc.x, tc.y, got, tc.want)
		}
	}
}

func TestClipPathInsetShrinks(t *testing.T) {
	// inset(6px): the ring between 0 and 6 disappears.
	p := &Path{}
	RoundedRectPath(p, Rect{X: 6, Y: 6, W: 12, H: 12}, Corners{})
	l := maskedLayer(t, p, Rect{W: 24, H: 24})
	if a := l.cv.get(3, 3).A(); a != 0 {
		t.Errorf("inside the inset ring: alpha %d", a)
	}
	if a := l.cv.get(12, 12).A(); a != 255 {
		t.Errorf("the inset box hollow: alpha %d", a)
	}
}

func TestClipPathCircleCentered(t *testing.T) {
	// circle(50%) on a 20x20 box: radius 10 at the center; the corner
	// (1,1) is outside (distance ~12.7), (10,10) inside, (10,2) just
	// inside the top edge.
	p := &Path{}
	EllipsePath(p, 10, 10, 10, 10)
	l := maskedLayer(t, p, Rect{W: 20, H: 20})
	for _, tc := range []struct {
		x, y  int
		solid bool
	}{
		{10, 10, true},
		{10, 2, true},
		{1, 1, false},
		{19, 19, false},
	} {
		a := l.cv.get(tc.x, tc.y).A()
		if tc.solid && a < 250 {
			t.Errorf("circle interior at (%d,%d): alpha %d", tc.x, tc.y, a)
		}
		if !tc.solid && a > 5 {
			t.Errorf("circle exterior at (%d,%d): alpha %d", tc.x, tc.y, a)
		}
	}
}

func TestClipPathPolygonTriangle(t *testing.T) {
	// polygon(0 0, 100% 0, 0 100%): the upper-left triangle of a
	// 20x20 box.
	p := &Path{}
	p.MoveTo(0, 0)
	p.LineTo(20, 0)
	p.LineTo(0, 20)
	p.Close()
	l := maskedLayer(t, p, Rect{W: 20, H: 20})
	if a := l.cv.get(2, 2).A(); a < 250 {
		t.Errorf("triangle interior: alpha %d", a)
	}
	if a := l.cv.get(17, 17).A(); a > 5 {
		t.Errorf("triangle exterior: alpha %d", a)
	}
	// The hypotenuse cuts (12,10): x > 20-y means outside.
	if a := l.cv.get(12, 7).A(); a > 200 {
		t.Errorf("below the hypotenuse stayed solid: alpha %d", a)
	}
}

func TestParsePathDataCommands(t *testing.T) {
	// A rounded square written in every line/cubic form: explicit C,
	// S with reflection, H/V, and a relative arc converted to cubics.
	p, ok := ParsePathData("M 4 4 H 16 C 18 4 20 6 20 8 V 16 S 16 20 4 20 l -4 -4 z")
	if !ok {
		t.Fatal("parse failed")
	}
	cov := PathCoverage(p, Rect{X: 0, Y: 0, W: 24, H: 24}, 1)
	if cov == nil {
		t.Fatal("no coverage rasterized")
	}
	at := func(x, y int) uint8 { return cov.Pix[(y-cov.Rect.Min.Y)*cov.Stride+x] }
	if at(12, 12) < 250 {
		t.Errorf("the shape's interior is not solid: %d", at(12, 12))
	}
	if at(1, 1) > 5 {
		t.Errorf("outside the shape: %d", at(1, 1))
	}
	if _, ok := ParsePathData("M 4 4 X 10"); ok {
		t.Error("an unknown command parsed")
	}
	if _, ok := ParsePathData(""); ok {
		t.Error("an empty string parsed")
	}
}

func TestParsePathDataArcApproximatesCircle(t *testing.T) {
	// Two arcs of radius 10 around the center: the raster must land
	// within a quarter pixel of the analytic circle at the cardinal
	// and diagonal probes.
	p, ok := ParsePathData("M 10 0 A 10 10 0 0 1 10 20 A 10 10 0 0 1 10 0")
	if !ok {
		t.Fatal("parse failed")
	}
	cov := PathCoverage(p, Rect{W: 22, H: 22}, 1)
	if cov == nil {
		t.Fatal("no coverage")
	}
	at := func(x, y int) uint8 { return cov.Pix[y*cov.Stride+x] }
	// Pixel centers sit at (+0.5, +0.5); the circle of radius 10
	// about (10,10) leaves (10,10) deep inside and (20,10) — center
	// (20.5, 10.5), just past the rim — near half coverage.
	if a := at(10, 10); a < 250 {
		t.Errorf("the arc's interior is not solid: %d", a)
	}
	if a := at(18, 4); a < 40 || a > 215 {
		t.Errorf("on the arc: %d, want a partial edge pixel", a)
	}
	if a := at(20, 20); a > 5 {
		t.Errorf("outside the arc: %d", a)
	}
}

func TestApplyMaskLayerAlphaAndLuminance(t *testing.T) {
	// A mask layer of a half-luma gray at full alpha: the alpha mode
	// keeps everything, the luminance mode halves.
	cv, _ := newTestCanvas(4, 4)
	l := filterLayer(t, RGB(255, 255, 255), 4, 4)
	mask := cv.Layer(nil, Rect{W: 4, H: 4})
	mask.Canvas().ClearDevice(Rect{W: 4, H: 4}, RGB(128, 128, 128))
	l.ApplyMaskLayer(mask, Rect{W: 4, H: 4}, false)
	if a := l.cv.get(2, 2).A(); a != 255 {
		t.Errorf("alpha mode read luminance: %d", a)
	}
	l = filterLayer(t, RGB(255, 255, 255), 4, 4)
	l.ApplyMaskLayer(mask, Rect{W: 4, H: 4}, true)
	if a := l.cv.get(2, 2).A(); a < 124 || a > 132 {
		t.Errorf("luminance mode: alpha %d, want ~128", a)
	}
}
