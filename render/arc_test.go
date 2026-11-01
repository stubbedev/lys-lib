package render

import (
	"math"
	"testing"
)

func arcCanvas(t *testing.T) (*Canvas, []byte) {
	t.Helper()
	const w = 40
	buf := make([]byte, Stride(w)*w)
	return New(buf, Stride(w), w, w), buf
}

func alphaAt(buf []byte, x, y int) uint8 { return buf[y*Stride(40)+x*4+3] }

// A quarter arc from twelve o'clock clockwise paints the top and right
// of the ring and leaves the bottom and left empty; the round caps
// reach past the ends; the full circle paints all four sides.
func TestArcPaintsTheSweepWithRoundCaps(t *testing.T) {
	c, buf := arcCanvas(t)
	white := RGB(255, 255, 255)
	c.Arc(20, 20, 15, 4, -math.Pi/2, math.Pi/2, white)
	if alphaAt(buf, 20, 5) == 0 || alphaAt(buf, 35, 20) == 0 {
		t.Fatal("the quarter arc is missing its ends")
	}
	if alphaAt(buf, 31, 9) == 0 {
		t.Error("the middle of the quarter arc is empty")
	}
	if alphaAt(buf, 20, 35) != 0 || alphaAt(buf, 5, 20) != 0 {
		t.Error("the quarter arc painted the bottom or left")
	}
	// The cap at twelve o'clock rounds a little to the left of the end.
	if alphaAt(buf, 19, 5) == 0 {
		t.Error("no round cap at the start")
	}
	if alphaAt(buf, 20, 20) != 0 {
		t.Error("the arc filled its center")
	}

	c2, buf2 := arcCanvas(t)
	c2.Arc(20, 20, 15, 4, 0, 2*math.Pi, white)
	for _, p := range [][2]int{{20, 5}, {35, 20}, {20, 35}, {5, 20}} {
		if alphaAt(buf2, p[0], p[1]) == 0 {
			t.Errorf("the full circle missed %v", p)
		}
	}
}

func TestArcIgnoresDegenerateInput(t *testing.T) {
	c, buf := arcCanvas(t)
	white := RGB(255, 255, 255)
	c.Arc(20, 20, 15, 0, 0, math.Pi, white)
	c.Arc(20, 20, 0, 4, 0, math.Pi, white)
	c.Arc(20, 20, 15, 4, 0, 0, white)
	for i := 3; i < len(buf); i += 4 {
		if buf[i] != 0 {
			t.Fatal("a degenerate arc painted")
		}
	}
}

// Skipping what a ring cannot reach changes no pixel: every arc paints
// what visiting its whole box does, at every scale, clipped or not.
func TestArcPaintsWhatItsWholeBoxWould(t *testing.T) {
	const w, h = 96, 80
	white := RGBA(255, 255, 255, 200)
	for _, sc := range [][2]int{{1, 1}, {3, 2}, {2, 1}} {
		for _, a := range [][6]float64{
			{24, 20, 15, 4, -math.Pi / 2, math.Pi / 2},
			{24, 20, 15, 4, 0, 2 * math.Pi},
			{24, 20, 2, 9, 1, 4},
			{24, 20, 0.5, 30, 0, 6},
			{0, 0, 18, 3, 0, 1.5},
			{40, 30, 60, 1, 0.3, 5},
			{20.25, 17.75, 9.5, 0.5, 2, 3},
			{24, 20, 15, 4, 6, 0.01},
		} {
			got := NewScaled(make([]byte, Stride(w)*h), Stride(w), w, h, sc[0], sc[1])
			got.Arc(a[0], a[1], a[2], a[3], a[4], a[5], white)
			want := NewScaled(make([]byte, Stride(w)*h), Stride(w), w, h, sc[0], sc[1])
			box := want.clip.Intersect(want.arcSpan(a[0], a[1], a[2], a[3]))
			g := ArcOf(a[0], a[1], a[2], a[3], a[4], a[5], sc[0], sc[1])
			for y := box.Y; y < box.Y+box.H; y++ {
				for x := box.X; x < box.X+box.W; x++ {
					blendCovF(want, x, y, white, g.Coverage(x, y))
				}
			}
			for y := range h {
				for x := range w {
					if got.get(x, y) != want.get(x, y) {
						t.Fatalf("scale %v arc %v: pixel (%d,%d) is %v, want %v", sc, a, x, y, got.get(x, y), want.get(x, y))
					}
				}
			}
		}
	}
}

func BenchmarkArcRing(b *testing.B) {
	cv := New(make([]byte, Stride(256)*256), Stride(256), 256, 256)
	white := RGBA(255, 255, 255, 255)
	b.ReportAllocs()
	for b.Loop() {
		cv.Arc(128, 128, 110, 6, -math.Pi/2, 3*math.Pi/2, white)
	}
}
