package render

import "math"

// Arc strokes a circular arc of the logical circle centered at (cx, cy)
// with the given radius (to the stroke's middle) and stroke width,
// clockwise from start through sweep radians (0 is three o'clock, -π/2
// is twelve). Caps are round, like cairo's LINE_CAP_ROUND; a sweep of
// 2π or more is the whole circle. Anti-aliased by per-pixel signed
// distance at the device scale.
func (c *Canvas) Arc(cx, cy, radius, width, start, sweep float64, col Color) {
	if width <= 0 || radius <= 0 || sweep <= 0 || col == 0 || c.clip.Empty() {
		return
	}
	dev := float64(c.num) / float64(c.denom)
	dcx, dcy, r, half := cx*dev, cy*dev, radius*dev, width*dev/2
	outer := r + half + 1
	box := c.clip.Intersect(Rect{
		X: int(math.Floor(dcx - outer)), Y: int(math.Floor(dcy - outer)),
		W: int(math.Ceil(2 * outer)), H: int(math.Ceil(2 * outer)),
	})
	full := sweep >= 2*math.Pi
	// The cap centers: the stroke's middle at both ends.
	sx, sy := dcx+r*math.Cos(start), dcy+r*math.Sin(start)
	ex, ey := dcx+r*math.Cos(start+sweep), dcy+r*math.Sin(start+sweep)
	for y := box.Y; y < box.Y+box.H; y++ {
		for x := box.X; x < box.X+box.W; x++ {
			px, py := float64(x)+0.5, float64(y)+0.5
			var d float64
			if full || angleWithin(math.Atan2(py-dcy, px-dcx), start, sweep) {
				d = math.Abs(math.Hypot(px-dcx, py-dcy)-r) - half
			} else {
				d = math.Min(math.Hypot(px-sx, py-sy), math.Hypot(px-ex, py-ey)) - half
			}
			c.blendCov(x, y, col, coverage(d))
		}
	}
}

// angleWithin reports whether angle a lies on the clockwise sweep from
// start.
func angleWithin(a, start, sweep float64) bool {
	rel := math.Mod(a-start, 2*math.Pi)
	if rel < 0 {
		rel += 2 * math.Pi
	}
	return rel <= sweep
}
