package render

import (
	"bytes"
	"math/rand/v2"
	"testing"
)

// borderPerPixel is RoundedBorderSides as it was, every ring pixel
// through the coverage and side maths: the reference the run fills
// must match.
func borderPerPixel(c *Canvas, r Rect, radii Corners, widths Insets, cols [4]Color) {
	g := c.borderRing(r, radii, widths)
	b, rc, in, irc, inv := to32(g.outer.array()), to32(g.radii), to32(g.inner.array()), to32(g.iradii), SideReciprocals(g.widths)
	innerEmpty := g.inner.x1 <= g.inner.x0 || g.inner.y1 <= g.inner.y0
	sp := c.span(g.outer)
	for y := sp.Y; y < sp.Y+sp.H; y++ {
		py := float32(y) + 0.5
		for x := sp.X; x < sp.X+sp.W; x++ {
			px := float32(x) + 0.5
			cov := coverage(sdBoxF(px, py, b, rc))
			if cov > 0 && !innerEmpty {
				cov = min(cov, 1-coverage(sdBoxF(px, py, in, irc)))
			}
			if cov > 0 {
				blendCovF(c, x, y, cols[borderSide(px, py, b, inv)], cov)
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

// roundedPerPixel is RoundedRect as every pixel through the distance
// field: the reference its run fill must match.
func roundedPerPixel(c *Canvas, r Rect, radius int, col Color) {
	full := c.MapRect(r)
	box := c.clip.Intersect(full)
	rad := RoundedRadius(full, radius, c.num, c.denom)
	for y := box.Y; y < box.Y+box.H; y++ {
		for x := box.X; x < box.X+box.W; x++ {
			cov := 0.5 - sdRoundRect(float32(x)+0.5, float32(y)+0.5, full, rad)
			if cov < 0 {
				continue
			}
			c.blend(x, y, coverageScale(col, cov255(min(cov, 1))))
		}
	}
}

// RoundedRect's run fill paints what the per-pixel field paints, for
// any rect, radius, color, clip and device scale.
func TestRoundedRectRunsMatchPerPixel(t *testing.T) {
	rng := rand.New(rand.NewPCG(3, 4))
	const w, h = 160, 120
	for i := range 600 {
		num, denom := []int{1, 5, 2, 3}[i%4], []int{1, 4, 1, 2}[i%4]
		r := Rect{X: rng.IntN(30) - 5, Y: rng.IntN(30) - 5, W: 1 + rng.IntN(70), H: 1 + rng.IntN(60)}
		radius := rng.IntN(40)
		col := []Color{RGBA(200, 0, 0, 255), RGBA(0, 160, 0, 200), RGBA(0, 0, 0, 0), RGBA(9, 9, 220, 3)}[i%4]
		clip := Rect{X: rng.IntN(40), Y: rng.IntN(40), W: 20 + rng.IntN(120), H: 20 + rng.IntN(80)}
		got := NewScaled(make([]byte, Stride(w)*h), Stride(w), w, h, num, denom)
		want := NewScaled(make([]byte, Stride(w)*h), Stride(w), w, h, num, denom)
		got.PushClipDevice(clip)
		want.PushClipDevice(clip)
		got.RoundedRect(r, radius, col)
		roundedPerPixel(want, r, radius, col)
		if !bytes.Equal(got.data, want.data) {
			t.Fatalf("case %d (%v radius %d at %d/%d, clip %v): run fill differs from per-pixel", i, r, radius, num, denom, clip)
		}
	}
}
