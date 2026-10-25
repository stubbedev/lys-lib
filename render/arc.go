package render

import "math"

// Arc strokes a circular arc of the logical circle centered at (cx, cy)
// with the given radius (to the stroke's middle) and stroke width,
// clockwise from start through sweep radians (0 is three o'clock, -π/2
// is twelve). Caps are round, like cairo's LINE_CAP_ROUND; a sweep of
// 2π or more is the whole circle. Anti-aliased by per-pixel signed
// distance at the device scale.
func (c *Canvas) Arc(cx, cy, radius, width, start, sweep float64, col Color) {
	if c.rec != nil {
		c.record(op{kind: opArc, f: [6]float64{cx, cy, radius, width, start, sweep}, col: col})
		return
	}
	if width <= 0 || radius <= 0 || sweep <= 0 || col == 0 || c.clip.Empty() {
		return
	}
	box := c.clip.Intersect(c.arcSpan(cx, cy, radius, width))
	g := ArcOf(cx, cy, radius, width, start, sweep, c.num, c.denom)
	for y := box.Y; y < box.Y+box.H; y++ {
		for x := box.X; x < box.X+box.W; x++ {
			blendCovF(c, x, y, col, g.Coverage(x, y))
		}
	}
}

// ArcGeometry is Arc's stroke in device pixels, in the float32 a GPU
// shader evaluates it in: the center, radius and half width, the start
// angle and sweep (Full for a whole turn), and the cap centers, the
// stroke's middle at both ends.
type ArcGeometry struct {
	CX, CY, R, Half, Start, Sweep, SX, SY, EX, EY float32
	Full                                          bool
}

// ArcOf is the stroke Arc paints at device scale num/denom.
func ArcOf(cx, cy, radius, width, start, sweep float64, num, denom int) ArcGeometry {
	dev := float64(num) / float64(denom)
	dcx, dcy, r := cx*dev, cy*dev, radius*dev
	return ArcGeometry{
		CX: float32(dcx), CY: float32(dcy), R: float32(r), Half: float32(width * dev / 2),
		Start: float32(start), Sweep: float32(sweep), Full: sweep >= 2*math.Pi,
		SX: float32(dcx + r*math.Cos(start)), SY: float32(dcy + r*math.Sin(start)),
		EX: float32(dcx + r*math.Cos(start+sweep)), EY: float32(dcy + r*math.Sin(start+sweep)),
	}
}

// The turn and its reciprocal, as the arc folds angles by them.
var (
	turn32    = float32(2 * math.Pi)
	invTurn32 = Reciprocal(turn32)
)

// Coverage is the stroke's coverage of pixel (x, y): within the sweep,
// the center's distance to the circle; past it, to the nearer cap.
// Angles come from atan2f, each step rounding as the shader's does.
func (g ArcGeometry) Coverage(x, y int) float32 {
	px, py := float32(x)+0.5, float32(y)+0.5
	dx, dy := px-g.CX, py-g.CY
	var d float32
	if g.Full || modFloor(atan2f(dy, dx)-g.Start, turn32, invTurn32) <= g.Sweep {
		d = abs(lengthF(dx, dy)-g.R) - g.Half
	} else {
		d = min(lengthF(px-g.SX, py-g.SY), lengthF(px-g.EX, py-g.EY)) - g.Half
	}
	return coverage(d)
}

// arcSpan is the device rect Arc visits: the stroked circle's box and
// a pixel.
func (c *Canvas) arcSpan(cx, cy, radius, width float64) Rect {
	dev := float64(c.num) / float64(c.denom)
	dcx, dcy := cx*dev, cy*dev
	outer := radius*dev + width*dev/2 + 1
	return Rect{
		X: int(math.Floor(dcx - outer)), Y: int(math.Floor(dcy - outer)),
		W: int(math.Ceil(2 * outer)), H: int(math.Ceil(2 * outer)),
	}
}
