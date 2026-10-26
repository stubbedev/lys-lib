package render

import (
	"testing"

	"golang.org/x/image/font/gofont/goregular"
)

// TestMapRectRoundsOutward pins the logical-to-device mapping: origins
// floor, far edges ceil, so a logical rect fully covers the device area
// it spans at fractional factors.
func TestMapRectRoundsOutward(t *testing.T) {
	tests := []struct {
		name       string
		r          Rect
		num, denom int
		want       Rect
	}{
		{"identity at 1x", Rect{X: 10, Y: 10, W: 25, H: 20}, 120, 120, Rect{X: 10, Y: 10, W: 25, H: 20}},
		{"2x exact", Rect{X: 10, Y: 10, W: 25, H: 20}, 240, 120, Rect{X: 20, Y: 20, W: 50, H: 40}},
		{"1.25 floors origin, ceils far edge", Rect{X: 10, Y: 10, W: 25, H: 20}, 150, 120, Rect{X: 12, Y: 12, W: 32, H: 26}},
		{"negative origins floor", Rect{X: -10, Y: -10, W: 20, H: 20}, 240, 120, Rect{X: -20, Y: -20, W: 40, H: 40}},
		{"1.25 negative origin", Rect{X: -9, Y: 0, W: 9, H: 8}, 150, 120, Rect{X: -12, Y: 0, W: 12, H: 10}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := MapRect(tc.r, tc.num, tc.denom); got != tc.want {
				t.Errorf("MapRect(%+v, %d/%d) = %+v, want %+v", tc.r, tc.num, tc.denom, got, tc.want)
			}
		})
	}
}

// TestFillRectDeviceScale checks a logical fill lands exactly on the
// mapped device rect at 2x, including partial device pixels.
func TestFillRectDeviceScale(t *testing.T) {
	col := RGB(1, 2, 3)
	cv := NewScaled(make([]byte, Stride(120)*80), Stride(120), 120, 80, 240, 120)
	cv.FillRect(Rect{X: 10, Y: 10, W: 25, H: 20}, col)

	// Inside the mapped rect (20..69, 20..59): every pixel filled.
	for _, p := range [][2]int{{20, 20}, {69, 59}, {50, 40}} {
		if got := cv.get(p[0], p[1]); got != col {
			t.Fatalf("pixel %v = %v, want filled", p, got)
		}
	}
	// One pixel outside on each edge: untouched.
	for _, p := range [][2]int{{19, 20}, {70, 40}, {20, 19}, {50, 60}} {
		if got := cv.get(p[0], p[1]); got == col {
			t.Fatalf("pixel %v is filled but lies outside the mapped rect", p)
		}
	}
}

// TestFillRectFractionalScale checks the outward rounding at 1.25: the
// logical rect 10,10 25x20 maps to device 12,12 32x25.
func TestFillRectFractionalScale(t *testing.T) {
	col := RGB(4, 5, 6)
	cv := NewScaled(make([]byte, Stride(60)*50), Stride(60), 60, 50, 150, 120)
	cv.FillRect(Rect{X: 10, Y: 10, W: 25, H: 20}, col)

	for _, p := range [][2]int{{12, 12}, {43, 36}} {
		if got := cv.get(p[0], p[1]); got != col {
			t.Fatalf("pixel %v = %v, want filled (mapped rect 12,12 32x26)", p, got)
		}
	}
	for _, p := range [][2]int{{11, 12}, {44, 20}, {20, 11}, {30, 38}} {
		if got := cv.get(p[0], p[1]); got == col {
			t.Fatalf("pixel %v is filled but lies outside the 1.25x mapping", p)
		}
	}
}

// TestTextRasterizesAtDeviceScale pins the crispness contract: the same
// run, shaped once at its logical size, produces glyph ink twice as tall
// on a 2x canvas as on a 1x one. A rasterizer that ignored the device
// scale would leave both identical.
func TestTextRasterizesAtDeviceScale(t *testing.T) {
	tf, err := LoadFont(goregular.TTF)
	if err != nil {
		t.Fatal(err)
	}
	shape := tf.Shape("H", 20) // shape once, at the logical size
	col := RGB(255, 255, 255)

	inkHeight := func(cv *Canvas) int {
		tf.Draw(cv, shape, 5, 40, col)
		minY, maxY := -1, -1
		for y := range cv.h {
			for x := range cv.w {
				if cv.get(x, y).A() > 0 {
					if minY < 0 {
						minY = y
					}
					maxY = y
				}
			}
		}
		if minY < 0 {
			t.Fatal("glyph painted nothing")
		}
		return maxY - minY + 1
	}

	one := NewScaled(make([]byte, Stride(100)*100), Stride(100), 100, 100, 120, 120)
	two := NewScaled(make([]byte, Stride(200)*200), Stride(200), 200, 200, 240, 120)
	h1, h2 := inkHeight(one), inkHeight(two)
	if h2 < h1*18/10 {
		t.Errorf("glyph ink height at 2x = %d px vs %d px at 1x, want roughly double (rasterization ignored the device scale)", h2, h1)
	}
}

// TestTextLogicalBaselineAtDeviceScale checks the logical anchor maps
// exactly: a baseline at logical y=40 lands near device y=80 minus the
// cap height at 2x, not at the 1x position.
func TestTextLogicalBaselineAtDeviceScale(t *testing.T) {
	tf, err := LoadFont(goregular.TTF)
	if err != nil {
		t.Fatal(err)
	}
	cv := NewScaled(make([]byte, Stride(200)*200), Stride(200), 200, 200, 240, 120)
	tf.Draw(cv, tf.Shape("H", 20), 5, 40, RGB(255, 255, 255))
	minY := -1
	for y := range cv.h {
		for x := range cv.w {
			if cv.get(x, y).A() > 0 {
				minY = y
				break
			}
		}
		if minY >= 0 {
			break
		}
	}
	if minY < 40 || minY > 84 {
		// The cap top sits above the baseline by roughly the cap height
		// (~14.5 logical px at 20px Go Regular): (40-15)*2 = 70ish; a
		// wide band pins the mapping without pinning font metrics.
		t.Errorf("first ink row at device y=%d, want ~2x the logical anchor band", minY)
	}
}

func TestClearDeviceVsClear(t *testing.T) {
	col := RGB(9, 9, 9)
	cv := NewScaled(make([]byte, Stride(50)*50), Stride(50), 50, 50, 150, 120)
	// Clear takes logical coordinates; at 1.25 the logical rect
	// 8,8 8x8 covers device 10..20 (10,10 to 19,19).
	cv.Clear(Rect{X: 8, Y: 8, W: 8, H: 8}, col)
	for _, p := range [][2]int{{10, 10}, {19, 19}} {
		if cv.get(p[0], p[1]) != col {
			t.Fatalf("pixel %v not cleared by the logical Clear", p)
		}
	}
	if cv.get(9, 10) == col || cv.get(20, 10) == col {
		t.Fatal("logical Clear cleared outside its mapped device rect")
	}

	// ClearDevice is the explicit device-space bridge: exact pixels.
	cv.ClearDevice(Rect{X: 0, Y: 0, W: 3, H: 3}, col)
	for _, p := range [][2]int{{0, 0}, {2, 2}} {
		if cv.get(p[0], p[1]) != col {
			t.Fatalf("pixel %v not cleared by ClearDevice", p)
		}
	}
	if cv.get(3, 3) == col {
		t.Fatal("ClearDevice cleared beyond the given device rect")
	}
}

func TestBorderAndRoundedRectAtDeviceScale(t *testing.T) {
	col := RGB(1, 1, 1)
	cv := NewScaled(make([]byte, Stride(100)*100), Stride(100), 100, 100, 240, 120)

	// A 1px logical border maps to 2 device px at 2x: the rect edge
	// bands are filled, the interior is not. The mapped rect is
	// 20,20 80x80, so the right band is x 98-99 and the bottom band
	// y 98-99.
	cv.BorderRect(Rect{X: 10, Y: 10, W: 40, H: 40}, 1, col)
	for _, p := range [][2]int{{20, 20}, {21, 20}, {20, 21}, {99, 20}, {20, 99}, {98, 55}} {
		if cv.get(p[0], p[1]) != col {
			t.Fatalf("border pixel %v unfilled at 2x", p)
		}
	}
	if cv.get(50, 50) == col {
		t.Fatal("border filled the interior at 2x")
	}

	// Rounded fills stay inside the mapped rect at fractional factors.
	// The logical rect 10,10 20x20 maps to device 12,12 26x26 at 1.25;
	// edge midlines fill, the cut corners and the outside do not.
	cv2 := NewScaled(make([]byte, Stride(60)*60), Stride(60), 60, 60, 150, 120)
	cv2.RoundedRect(Rect{X: 10, Y: 10, W: 20, H: 20}, 5, col)
	for _, p := range [][2]int{{12, 24}, {24, 12}, {24, 24}, {37, 24}, {24, 37}} {
		if cv2.get(p[0], p[1]) != col {
			t.Fatalf("rounded-rect pixel %v unfilled at 1.25x", p)
		}
	}
	for _, p := range [][2]int{{12, 12}, {37, 37}, {45, 24}} {
		if cv2.get(p[0], p[1]) == col {
			t.Fatalf("rounded-rect pixel %v filled outside the shape at 1.25x", p)
		}
	}
}

func TestNewScaledRejectsInvalidScale(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("NewScaled accepted a zero denominator")
		}
	}()
	NewScaled(make([]byte, 16), 16, 1, 1, 120, 0)
}
