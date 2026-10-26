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
		ApplyFilters(l, l.cv.Rect(), &f, FilterShape{})
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
		ApplyFilters(l, l.cv.Rect(), &f, FilterShape{})
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
	ApplyFilters(l, l.cv.Rect(), &f, FilterShape{})
	if c := l.cv.get(2, 2); !nearC(c.R(), 54) || !nearC(c.G(), 54) {
		t.Errorf("chain = %02x%02x%02x, want saturated gray", c.R(), c.G(), c.B())
	}
	l = filterLayer(t, RGB(100, 0, 0), 4, 4)
	f = Filters{}
	f.Append(FilterOp{Kind: FilterBrightness, Amount: 2})
	f.Append(FilterOp{Kind: FilterGrayscale, Amount: 1})
	ApplyFilters(l, l.cv.Rect(), &f, FilterShape{})
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
	ApplyFilters(l, l.cv.Rect(), &f, FilterShape{})
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
	ApplyFilters(l, l.cv.Rect(), &f, FilterShape{})
	if c := l.cv.get(8, 8); c != RGB(255, 255, 255) {
		t.Errorf("drop-shadow painted over the silhouette: %08x", uint32(c))
	}
	if c := l.cv.get(8, 14); c.G() < 200 {
		t.Errorf("the shadow tint did not land at the offset: %08x", uint32(c))
	}
}

func nearC(v uint8, w int) bool { return abs64(float64(v)-float64(w)) <= 1 }

// A heavy blur runs downsampled (blurScale) and must still match the
// exact full-resolution passes: uniform areas stay uniform, and the
// blurred edge of a square differs by a few levels at most (#195).
func TestFilterHeavyBlurMatchesFullResolution(t *testing.T) {
	if blurScale(2) != 1 || blurScale(8) != 2 || blurScale(40) != 4 {
		t.Fatalf("blurScale(2, 8, 40) = %d, %d, %d; want 1, 2, 4", blurScale(2), blurScale(8), blurScale(40))
	}
	const w, h, radius = 96, 96, 16.0
	paint := func() *Layer {
		l := filterLayer(t, RGB(20, 40, 60), w, h)
		for y := 30; y < 66; y++ {
			for x := 30; x < 66; x++ {
				l.cv.set(x, y, RGB(240, 200, 40))
			}
		}
		return l
	}
	got := paint()
	var f Filters
	f.Append(FilterOp{Kind: FilterBlur, Amount: radius})
	ApplyFilters(got, got.cv.Rect(), &f, FilterShape{})

	want := paint()
	n := w * h
	buf := make([]Color, n)
	readRegion(want.cv, want.cv.Rect(), buf)
	want.blurPasses(buf, w, h, blurBoxes(radius/2))
	writeRegion(want.cv, want.cv.Rect(), buf)

	// Within the blur's reach of the region's edge the two differ the
	// most (outside reads transparent, coarser at the reduced
	// resolution); a backdrop filter masks that band away. Inside it
	// they must agree closely.
	reach := int(3 * radius / 2)
	worst := 0
	for y := reach; y < h-reach; y++ {
		for x := reach; x < w-reach; x++ {
			a, b := got.cv.get(x, y), want.cv.get(x, y)
			for _, s := range []uint{24, 16, 8, 0} {
				worst = max(worst, abs(int(uint8(a>>s))-int(uint8(b>>s))))
			}
		}
	}
	if worst > 3 {
		t.Errorf("downsampled blur differs from the full-resolution one by up to %d levels inside the edge band, want at most 3", worst)
	}
}

// Filter lengths are CSS pixels: on a 2x layer a blur reaches twice as
// many device pixels as on a 1x one (#223).
func TestFilterBlurScalesWithTheDevice(t *testing.T) {
	reach := func(num int) int {
		w := 64 * num
		cv := NewScaled(make([]byte, Stride(w)*w), Stride(w), w, w, num, 1)
		l := cv.Layer(nil, cv.Rect())
		sq := 24 * num
		for y := sq; y < 2*sq; y++ {
			for x := sq; x < 2*sq; x++ {
				l.cv.set(x, y, RGB(255, 255, 255))
			}
		}
		var f Filters
		f.Append(FilterOp{Kind: FilterBlur, Amount: 4})
		ApplyFilters(l, l.cv.Rect(), &f, FilterShape{})
		r := 0
		for x := range sq {
			if l.cv.get(sq-1-x, sq+sq/2).A() > 2 {
				r = x + 1
			}
		}
		return r
	}
	r1, r2 := reach(1), reach(2)
	if r2 < 2*r1-2 || r2 > 2*r1+2 {
		t.Errorf("blur reach %d device px at 1x, %d at 2x; want twice", r1, r2)
	}
}

// -lys-refract bends only the bezel: inside the shape within the bezel
// of its edge pixels sample the backdrop displaced outward; past the
// bezel and outside the shape they stay as they were (#197).
func TestFilterRefractBendsTheBezel(t *testing.T) {
	const w, h = 80, 60
	stripes := func() *Layer {
		l := filterLayer(t, 0, w, h)
		for y := range h {
			for x := range w {
				l.cv.set(x, y, RGB(uint8(x*3), uint8(y*4), 200))
			}
		}
		return l
	}
	before := stripes()
	l := stripes()
	var f Filters
	f.Append(FilterOp{Kind: FilterRefract, Bezel: 10, Amount: 6})
	shape := FilterShape{Box: Rect{X: 20, Y: 15, W: 40, H: 30}, Radii: UniformCorners(8)}
	ApplyFilters(l, l.cv.Rect(), &f, shape)
	same := func(x, y int) bool { return l.cv.get(x, y) == before.cv.get(x, y) }
	if !same(5, 5) || !same(70, 50) {
		t.Error("refract changed pixels outside the shape")
	}
	if !same(40, 30) {
		t.Error("refract changed the middle, past the bezel")
	}
	// On the left edge the backdrop comes from further left: a smaller
	// red ramp value than the pixel's own.
	if got, own := l.cv.get(21, 30).R(), before.cv.get(21, 30).R(); got >= own {
		t.Errorf("left rim red %d, want below its own %d (sampled from outside)", got, own)
	}
}
