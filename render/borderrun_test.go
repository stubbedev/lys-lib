package render

import (
	"bytes"
	"math"
	"math/rand/v2"
	"testing"
)

// borderPerPixel is RoundedBorderSides as it was, every ring pixel
// through the coverage and side maths: the reference the run fills
// must match.
func borderPerPixel(c *Canvas, r Rect, radii Corners, widths Insets, cols [4]Color) {
	b, rc := c.devBox(r, radii)
	f := float64(c.num) / float64(c.denom)
	w := [4]float64{float64(widths.Top) * f, float64(widths.Right) * f, float64(widths.Bottom) * f, float64(widths.Left) * f}
	in := frect{b.x0 + w[3], b.y0 + w[0], b.x1 - w[1], b.y1 - w[2]}
	irc := fcorners{
		math.Max(0, rc[0]-math.Max(w[3], w[0])), math.Max(0, rc[1]-math.Max(w[0], w[1])),
		math.Max(0, rc[2]-math.Max(w[1], w[2])), math.Max(0, rc[3]-math.Max(w[2], w[3])),
	}
	innerEmpty := in.x1 <= in.x0 || in.y1 <= in.y0
	sp := c.span(b)
	for y := sp.Y; y < sp.Y+sp.H; y++ {
		py := float64(y) + 0.5
		for x := sp.X; x < sp.X+sp.W; x++ {
			px := float64(x) + 0.5
			cov := coverage(sdBox(px, py, b, rc))
			if cov > 0 && !innerEmpty {
				cov = math.Min(cov, 1-coverage(sdBox(px, py, in, irc)))
			}
			if cov > 0 {
				c.blendCov(x, y, cols[borderSide(px, py, b, w)], cov)
			}
		}
	}
}

// Filling a border's straight bands by run paints what the per-pixel
// maths paints, for any box, radii, widths, colors and device scale.
func TestBorderRunsMatchPerPixel(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	const w, h = 160, 120
	for i := range 400 {
		num, denom := []int{1, 5, 2}[i%3], []int{1, 4, 1}[i%3]
		r := Rect{X: rng.IntN(20), Y: rng.IntN(20), W: 10 + rng.IntN(60), H: 10 + rng.IntN(50)}
		radii := Corners{rng.IntN(16), rng.IntN(16), rng.IntN(16), rng.IntN(16)}
		widths := Insets{rng.IntN(5), rng.IntN(5), rng.IntN(5), rng.IntN(5)}
		cols := [4]Color{RGBA(200, 0, 0, 255), RGBA(0, 160, 0, 200), RGBA(0, 0, 220, 128), RGBA(90, 90, 0, 255)}
		got := NewScaled(make([]byte, Stride(w)*h), Stride(w), w, h, num, denom)
		want := NewScaled(make([]byte, Stride(w)*h), Stride(w), w, h, num, denom)
		got.RoundedBorderSides(r, radii, widths, cols)
		if !widths.Zero() {
			borderPerPixel(want, r, radii, widths, cols)
		}
		if !bytes.Equal(got.data, want.data) {
			t.Fatalf("case %d (%v radii %v widths %v at %d/%d): run fill differs from per-pixel", i, r, radii, widths, num, denom)
		}
	}
}
