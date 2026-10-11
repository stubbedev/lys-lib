package render

import "testing"

// The filter-effects math set: every color-matrix function probed on
// known straight colors against the spec's matrices, blur's energy
// and skirt, drop-shadow's silhouette, and the composed-matrix chain.

func filterLayer(t *testing.T, col Color, w, h int) *Layer {
	t.Helper()
	cv, _ := newTestCanvas(w, h)
	cv.Clear(cv.Rect(), col)
	l := &Layer{}
	return cv.Snapshot(l, cv.Rect())
}

func TestFilterGrayscaleLuminanceWeights(t *testing.T) {
	// grayscale(1) of pure red, green and blue lands on the 0.2126 /
	// 0.7152 / 0.0722 weights, rounded; the channels come out equal.
	for _, tc := range []struct {
		in   Color
		want uint8
	}{{RGB(255, 0, 0), 54}, {RGB(0, 255, 0), 182}, {RGB(0, 0, 255), 18}} {
		l := filterLayer(t, tc.in, 4, 4)
		var f Filters
		f.Append(FilterOp{Kind: FilterGrayscale, Amount: 1})
		ApplyFilters(l, l.cv.Rect(), &f)
		c := l.cv.get(2, 2)
		if c.R() != c.G() || c.G() != c.B() || abs64(float64(c.R())-float64(tc.want)) > 1 {
			t.Errorf("grayscale(%08x) = %02x%02x%02x, want ~%02x", uint32(tc.in), c.R(), c.G(), c.B(), tc.want)
		}
	}
}

func TestFilterMatrixFunctions(t *testing.T) {
	cases := []struct {
		name  string
		op    FilterOp
		in    Color
		check func(c Color) bool
	}{
		{
			"brightness halves red",
			FilterOp{Kind: FilterBrightness, Amount: 0.5},
			RGB(200, 100, 50),
			func(c Color) bool { return nearC(c.R(), 100) && nearC(c.G(), 50) && nearC(c.B(), 25) },
		},
		{
			"contrast 2 spreads around mid",
			FilterOp{Kind: FilterContrast, Amount: 2},
			RGB(200, 100, 50),
			func(c Color) bool { return nearC(c.R(), 255) && nearC(c.G(), 73) && nearC(c.B(), 0) },
		},
		{
			"invert swaps",
			FilterOp{Kind: FilterInvert, Amount: 1},
			RGB(200, 100, 50),
			func(c Color) bool { return nearC(c.R(), 55) && nearC(c.G(), 155) && nearC(c.B(), 205) },
		},
		{
			"opacity halves alpha",
			FilterOp{Kind: FilterOpacity, Amount: 0.5},
			RGBA(200, 100, 50, 255),
			func(c Color) bool {
				return nearC(c.A(), 128) && nearC(c.R(), 100) && nearC(c.G(), 50) && nearC(c.B(), 25)
			},
		},
		{
			"saturate 0 is grayscale",
			FilterOp{Kind: FilterSaturate, Amount: 0},
			RGB(255, 0, 0),
			func(c Color) bool { return nearC(c.R(), 54) && nearC(c.G(), 54) && nearC(c.B(), 54) },
		},
		{
			"sepia red",
			FilterOp{Kind: FilterSepia, Amount: 1},
			RGB(255, 0, 0),
			func(c Color) bool { return nearC(c.R(), 100) && nearC(c.G(), 89) && nearC(c.B(), 69) },
		},
		{
			"hue-rotate 120 of red (the spec's linear approximation)",
			FilterOp{Kind: FilterHueRotate, Amount: 120},
			RGB(255, 0, 0),
			func(c Color) bool { return nearC(c.R(), 0) && nearC(c.G(), 113) && nearC(c.B(), 0) },
		},
	}
	for _, tc := range cases {
		l := filterLayer(t, tc.in, 4, 4)
		var f Filters
		f.Append(tc.op)
		ApplyFilters(l, l.cv.Rect(), &f)
		if got := l.cv.get(2, 2); !tc.check(got) {
			t.Errorf("%s: %08x -> %08x", tc.name, uint32(tc.in), uint32(got))
		}
	}
}

func TestFilterChainComposes(t *testing.T) {
	// brightness(2) then grayscale(1): the brightness saturates red
	// first (the spec clamps per function), the gray weights then land
	// on the saturated value.
	l := filterLayer(t, RGB(255, 0, 0), 4, 4)
	var f Filters
	f.Append(FilterOp{Kind: FilterBrightness, Amount: 2})
	f.Append(FilterOp{Kind: FilterGrayscale, Amount: 1})
	ApplyFilters(l, l.cv.Rect(), &f)
	if c := l.cv.get(2, 2); !nearC(c.R(), 54) || !nearC(c.G(), 54) {
		t.Errorf("chain = %02x%02x%02x, want saturated gray", c.R(), c.G(), c.B())
	}
	l = filterLayer(t, RGB(100, 0, 0), 4, 4)
	f = Filters{}
	f.Append(FilterOp{Kind: FilterBrightness, Amount: 2})
	f.Append(FilterOp{Kind: FilterGrayscale, Amount: 1})
	ApplyFilters(l, l.cv.Rect(), &f)
	// Unsaturated: 100 -> 200, then the red weight .2126: ~43.
	if c := l.cv.get(2, 2); !nearC(c.R(), 43) {
		t.Errorf("chain unsaturated = %02x, want ~43", c.R())
	}
}

func TestFilterBrightnessOnlyFast(t *testing.T) {
	var f Filters
	if _, ok := f.BrightnessOnly(); ok {
		t.Error("an empty list claimed brightness-only")
	}
	f.Append(FilterOp{Kind: FilterBrightness, Amount: 2})
	f.Append(FilterOp{Kind: FilterBrightness, Amount: 0.5})
	if b, ok := f.BrightnessOnly(); !ok || b != 1 {
		t.Errorf("brightness-only product = %v %v, want 1 true", b, ok)
	}
	f.Append(FilterOp{Kind: FilterBlur, Amount: 2})
	if _, ok := f.BrightnessOnly(); ok {
		t.Error("a mixed list claimed brightness-only")
	}
}

func TestFilterBlurSpreadsAndFades(t *testing.T) {
	// A white square blurred over transparent: the center dims, the
	// skirt beyond the 3-sigma reach stays clean, and the total
	// premultiplied energy is roughly kept.
	l := filterLayer(t, 0, 24, 24)
	for y := 8; y < 16; y++ {
		for x := 8; x < 16; x++ {
			l.cv.set(x, y, RGB(255, 255, 255))
		}
	}
	before := layerEnergy(l)
	var f Filters
	f.Append(FilterOp{Kind: FilterBlur, Amount: 4})
	ApplyFilters(l, l.cv.Rect(), &f)
	c := l.cv.get(11, 11)
	if c.A() == 255 {
		t.Error("blur left the center fully opaque")
	}
	if a := l.cv.get(11, 2).A(); a > 8 {
		t.Errorf("blur reached the far edge: alpha %d", a)
	}
	after := layerEnergy(l)
	if float64(after) < float64(before)*0.85 || float64(after) > float64(before)*1.15 {
		t.Errorf("blur energy %d -> %d, outside 15%%", before, after)
	}
}

func layerEnergy(l *Layer) int {
	n := 0
	r := l.cv.Rect()
	for y := r.Y; y < r.Y+r.H; y++ {
		for x := r.X; x < r.X+r.W; x++ {
			n += int(l.cv.get(x, y).A())
		}
	}
	return n
}

func TestFilterDropShadowUnderSilhouette(t *testing.T) {
	// drop-shadow paints a blurred tint at the offset, under the
	// original pixels: inside the box the original stays opaque, at
	// the offset below it the shadow tint shows.
	l := filterLayer(t, 0, 24, 24)
	for y := 6; y < 12; y++ {
		for x := 6; x < 18; x++ {
			l.cv.set(x, y, RGB(255, 255, 255))
		}
	}
	var f Filters
	f.Append(FilterOp{Kind: FilterDropShadow, Shadow: DropShadow{DX: 0, DY: 6, Blur: 0, Color: RGB(0, 255, 0)}})
	ApplyFilters(l, l.cv.Rect(), &f)
	if c := l.cv.get(8, 8); c != RGB(255, 255, 255) {
		t.Errorf("drop-shadow painted over the silhouette: %08x", uint32(c))
	}
	if c := l.cv.get(8, 14); c.G() < 200 {
		t.Errorf("the shadow tint did not land at the offset: %08x", uint32(c))
	}
}

func nearC(v uint8, w int) bool { return abs64(float64(v)-float64(w)) <= 1 }
