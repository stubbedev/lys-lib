// The CSS box primitives: per-corner rounded fills, rounded border
// rings with per-side widths and colors, angled multi-stop linear
// gradients, and box shadows with offset, spread, blur, and inset —
// everything a CSS box paints behind its content. Every primitive is a
// per-pixel signed-distance coverage pass at the device scale and
// blends through the single blend site, so PushAlpha and
// PushBrightness apply by construction.
package render

import (
	"math"
)

// Corners is a per-corner radius set in logical pixels, clockwise from
// the top-left.
type Corners struct {
	TopLeft, TopRight, BottomRight, BottomLeft int
}

// UniformCorners returns r on every corner.
func UniformCorners(r int) Corners { return Corners{r, r, r, r} }

// Insets is a per-side length set in logical pixels, in CSS order.
type Insets struct {
	Top, Right, Bottom, Left int
}

// UniformInsets returns v on every side.
func UniformInsets(v int) Insets { return Insets{v, v, v, v} }

// Zero reports whether every side is zero.
func (i Insets) Zero() bool { return i == Insets{} }

// Shrink pulls r in by the insets, clamping at an empty rect.
func (i Insets) Shrink(r Rect) Rect {
	return Rect{
		X: r.X + i.Left,
		Y: r.Y + i.Top,
		W: max(0, r.W-i.Left-i.Right),
		H: max(0, r.H-i.Top-i.Bottom),
	}
}

// Grow pushes r out by the insets.
func (i Insets) Grow(r Rect) Rect {
	return Rect{X: r.X - i.Left, Y: r.Y - i.Top, W: r.W + i.Left + i.Right, H: r.H + i.Top + i.Bottom}
}

// GradientStop is one color stop of a linear gradient: Pos is the
// fraction of the gradient line, 0 at its start and 1 at its end.
type GradientStop struct {
	Pos   float64
	Color Color
}

// BoxShadow is one CSS box-shadow layer: an offset, a blur radius (the
// Gaussian spans two sigmas of it, CSS's convention), a spread, and
// whether it paints inside the box instead of outside.
type BoxShadow struct {
	X, Y, Blur, Spread int
	Color              Color
	Inset              bool
}

// Extent returns how far an outer shadow paints beyond its box, per
// side; an inset shadow never paints outside. Callers owe these pixels
// damage.
func (s BoxShadow) Extent() Insets {
	if s.Inset || s.Color.A() == 0 {
		return Insets{}
	}
	reach := s.Spread + s.Blur
	return Insets{
		Top:    max(0, reach-s.Y),
		Right:  max(0, reach+s.X),
		Bottom: max(0, reach+s.Y),
		Left:   max(0, reach-s.X),
	}
}

// frect is a float rect in device pixels.
type frect struct{ x0, y0, x1, y1 float64 }

// fcorners are device-pixel radii, clockwise from top-left.
type fcorners [4]float64

// devBox maps a logical rect and radii to device space, scaling the
// radii down uniformly when adjacent ones overlap (the CSS rule).
func (c *Canvas) devBox(r Rect, radii Corners) (frect, fcorners) {
	d := c.MapRect(r)
	f := float64(c.num) / float64(c.denom)
	b := frect{float64(d.X), float64(d.Y), float64(d.X + d.W), float64(d.Y + d.H)}
	rc := fcorners{
		math.Max(0, float64(radii.TopLeft)*f),
		math.Max(0, float64(radii.TopRight)*f),
		math.Max(0, float64(radii.BottomRight)*f),
		math.Max(0, float64(radii.BottomLeft)*f),
	}
	return b, clampRadii(b, rc)
}

// clampRadii applies CSS's overlap rule: when two radii on one side sum
// past its length, every radius shrinks by the same factor.
func clampRadii(b frect, rc fcorners) fcorners {
	w, h := b.x1-b.x0, b.y1-b.y0
	scale := 1.0
	check := func(sum, side float64) {
		if sum > side && sum > 0 {
			scale = math.Min(scale, side/sum)
		}
	}
	check(rc[0]+rc[1], w)
	check(rc[3]+rc[2], w)
	check(rc[0]+rc[3], h)
	check(rc[1]+rc[2], h)
	if scale < 1 {
		for i := range rc {
			rc[i] *= scale
		}
	}
	return rc
}

// sdBox is the signed distance from (px, py) to a rounded rect with
// per-corner radii: negative inside, positive outside, in device pixels.
func sdBox(px, py float64, b frect, rc fcorners) float64 {
	cx, cy := (b.x0+b.x1)/2, (b.y0+b.y1)/2
	var rad float64
	switch {
	case px < cx && py < cy:
		rad = rc[0]
	case px >= cx && py < cy:
		rad = rc[1]
	case px >= cx:
		rad = rc[2]
	default:
		rad = rc[3]
	}
	hw := (b.x1-b.x0)/2 - rad
	hh := (b.y1-b.y0)/2 - rad
	qx := math.Abs(px-cx) - hw
	qy := math.Abs(py-cy) - hh
	return math.Hypot(math.Max(qx, 0), math.Max(qy, 0)) + math.Min(math.Max(qx, qy), 0) - rad
}

// coverage maps a signed distance to anti-aliased pixel coverage.
func coverage(d float64) float64 { return math.Min(1, math.Max(0, 0.5-d)) }

// span returns the device pixel span of b, clipped.
func (c *Canvas) span(b frect) Rect {
	r := Rect{
		X: int(math.Floor(b.x0)), Y: int(math.Floor(b.y0)),
		W: int(math.Ceil(b.x1)) - int(math.Floor(b.x0)),
		H: int(math.Ceil(b.y1)) - int(math.Floor(b.y0)),
	}
	return c.clip.Intersect(r)
}

// blendCov blends col at coverage cov in [0, 1].
func (c *Canvas) blendCov(x, y int, col Color, cov float64) {
	if cov <= 0 {
		return
	}
	if cov >= 1 {
		c.blend(x, y, col)
		return
	}
	c.blend(x, y, coverageScale(col, uint32(math.Round(cov*255))))
}

// RoundedRectCorners blends col over r with each corner rounded by its
// own radius, anti-aliased. Radii that overlap scale down together, per
// CSS.
func (c *Canvas) RoundedRectCorners(r Rect, radii Corners, col Color) {
	if col == 0 {
		return
	}
	b, rc := c.devBox(r, radii)
	sp := c.span(b)
	for y := sp.Y; y < sp.Y+sp.H; y++ {
		for x := sp.X; x < sp.X+sp.W; x++ {
			c.blendCov(x, y, col, coverage(sdBox(float64(x)+0.5, float64(y)+0.5, b, rc)))
		}
	}
}

// RoundedBorder strokes the ring between r's rounded outline and its
// inner edge, inset per side by widths: a CSS border. The inner corner
// radii shrink by the adjacent widths.
func (c *Canvas) RoundedBorder(r Rect, radii Corners, widths Insets, col Color) {
	c.RoundedBorderSides(r, radii, widths, [4]Color{col, col, col, col})
}

// RoundedBorderSides is RoundedBorder with a color per side (top,
// right, bottom, left). A ring pixel takes the color of the side it is
// relatively closest to, which splits the corners along their
// diagonals the way CSS borders join.
func (c *Canvas) RoundedBorderSides(r Rect, radii Corners, widths Insets, cols [4]Color) {
	if widths.Zero() {
		return
	}
	b, rc := c.devBox(r, radii)
	f := float64(c.num) / float64(c.denom)
	w := [4]float64{
		float64(widths.Top) * f, float64(widths.Right) * f,
		float64(widths.Bottom) * f, float64(widths.Left) * f,
	}
	in := frect{b.x0 + w[3], b.y0 + w[0], b.x1 - w[1], b.y1 - w[2]}
	irc := fcorners{
		math.Max(0, rc[0]-math.Max(w[3], w[0])),
		math.Max(0, rc[1]-math.Max(w[0], w[1])),
		math.Max(0, rc[2]-math.Max(w[1], w[2])),
		math.Max(0, rc[3]-math.Max(w[2], w[3])),
	}
	innerEmpty := in.x1 <= in.x0 || in.y1 <= in.y0
	sp := c.span(b)
	for y := sp.Y; y < sp.Y+sp.H; y++ {
		for x := sp.X; x < sp.X+sp.W; x++ {
			px, py := float64(x)+0.5, float64(y)+0.5
			cov := coverage(sdBox(px, py, b, rc))
			if cov <= 0 {
				continue
			}
			if !innerEmpty {
				cov = math.Min(cov, 1-coverage(sdBox(px, py, in, irc)))
			}
			if cov <= 0 {
				continue
			}
			c.blendCov(x, y, cols[borderSide(px, py, b, w)], cov)
		}
	}
}

// borderSide picks the side a ring pixel belongs to: the one whose
// width-normalized distance from its edge is smallest, skipping
// zero-width sides.
func borderSide(px, py float64, b frect, w [4]float64) int {
	d := [4]float64{py - b.y0, b.x1 - px, b.y1 - py, px - b.x0}
	best, bestV := 0, math.Inf(1)
	for i := range 4 {
		if w[i] <= 0 {
			continue
		}
		if v := d[i] / w[i]; v < bestV {
			best, bestV = i, v
		}
	}
	return best
}

// FillGradient paints a CSS linear gradient over r, clipped to its
// rounded outline. angle is in degrees, CSS convention: 0 runs bottom
// to top, 90 left to right; the gradient line spans the box's corners
// along that direction. Colors interpolate premultiplied between the
// stops, which must be sorted by position; outside the first and last
// stop the end colors extend.
func (c *Canvas) FillGradient(r Rect, radii Corners, angle float64, stops []GradientStop) {
	if len(stops) == 0 {
		return
	}
	b, rc := c.devBox(r, radii)
	rad := angle * math.Pi / 180
	dx, dy := math.Sin(rad), -math.Cos(rad)
	w, h := b.x1-b.x0, b.y1-b.y0
	length := math.Abs(w*dx) + math.Abs(h*dy)
	if length == 0 {
		return
	}
	cx, cy := (b.x0+b.x1)/2, (b.y0+b.y1)/2
	sp := c.span(b)
	for y := sp.Y; y < sp.Y+sp.H; y++ {
		for x := sp.X; x < sp.X+sp.W; x++ {
			px, py := float64(x)+0.5, float64(y)+0.5
			cov := coverage(sdBox(px, py, b, rc))
			if cov <= 0 {
				continue
			}
			t := ((px-cx)*dx+(py-cy)*dy)/length + 0.5
			c.blendCov(x, y, gradientAt(stops, t), cov)
		}
	}
}

// gradientAt samples the stop list at t.
func gradientAt(stops []GradientStop, t float64) Color {
	if t <= stops[0].Pos {
		return stops[0].Color
	}
	for i := 1; i < len(stops); i++ {
		if t <= stops[i].Pos {
			a, b := stops[i-1], stops[i]
			span := b.Pos - a.Pos
			if span <= 0 {
				return b.Color
			}
			return lerp(a.Color, b.Color, (t-a.Pos)/span)
		}
	}
	return stops[len(stops)-1].Color
}

// BoxShadow paints one CSS box-shadow layer for the box r with the
// given corner radii. An outer shadow is the box offset and grown by
// the spread, blurred, and clipped to outside the box itself (so a
// translucent box never shows its own shadow through); an inset shadow
// fills the box's inside except the offset, spread-shrunk hole,
// blurred at the hole's edge. The blur is Gaussian with sigma half the
// blur radius, evaluated per pixel from the silhouette's signed
// distance.
func (c *Canvas) BoxShadow(r Rect, radii Corners, s BoxShadow) {
	if s.Color.A() == 0 || c.clip.Empty() {
		return
	}
	box, rc := c.devBox(r, radii)
	f := float64(c.num) / float64(c.denom)
	ox, oy := float64(s.X)*f, float64(s.Y)*f
	spread := float64(s.Spread) * f
	sigma := float64(s.Blur) * f / 2
	shape := func(sign float64) (frect, fcorners) {
		g := spread * sign
		sb := frect{box.x0 + ox - g, box.y0 + oy - g, box.x1 + ox + g, box.y1 + oy + g}
		var src fcorners
		for i := range rc {
			if rc[i] > 0 {
				src[i] = math.Max(0, rc[i]+g)
			}
		}
		if sb.x1 < sb.x0 {
			m := (sb.x0 + sb.x1) / 2
			sb.x0, sb.x1 = m, m
		}
		if sb.y1 < sb.y0 {
			m := (sb.y0 + sb.y1) / 2
			sb.y0, sb.y1 = m, m
		}
		return sb, clampRadii(sb, src)
	}
	// blurCov is the coverage of a blurred silhouette at signed distance
	// d: a Gaussian-convolved edge, 50% on the edge itself.
	blurCov := func(d float64) float64 {
		if sigma <= 0.5 {
			return coverage(d)
		}
		return 0.5 * math.Erfc(d/(sigma*math.Sqrt2))
	}
	if !s.Inset {
		sb, src := shape(1)
		reach := 3 * sigma
		sp := c.span(frect{sb.x0 - reach, sb.y0 - reach, sb.x1 + reach, sb.y1 + reach})
		for y := sp.Y; y < sp.Y+sp.H; y++ {
			for x := sp.X; x < sp.X+sp.W; x++ {
				px, py := float64(x)+0.5, float64(y)+0.5
				outside := 1 - coverage(sdBox(px, py, box, rc))
				if outside <= 0 {
					continue
				}
				c.blendCov(x, y, s.Color, blurCov(sdBox(px, py, sb, src))*outside)
			}
		}
		return
	}
	hole, hrc := shape(-1)
	sp := c.span(box)
	for y := sp.Y; y < sp.Y+sp.H; y++ {
		for x := sp.X; x < sp.X+sp.W; x++ {
			px, py := float64(x)+0.5, float64(y)+0.5
			inside := coverage(sdBox(px, py, box, rc))
			if inside <= 0 {
				continue
			}
			c.blendCov(x, y, s.Color, (1-blurCov(sdBox(px, py, hole, hrc)))*inside)
		}
	}
}

// PushBrightness multiplies the color channels of everything painted
// until the matching PopBrightness by f: CSS's filter: brightness().
// Nested pushes multiply. Channels clamp at the pixel's alpha, so the
// result stays a valid premultiplied color. It returns the previous
// factor; restore it with PopBrightness.
func (c *Canvas) PushBrightness(f float64) float64 {
	prev := c.bright
	if prev == 0 {
		prev = 1
	}
	c.bright = prev * math.Max(0, f)
	return prev
}

// PopBrightness restores a factor returned by PushBrightness.
func (c *Canvas) PopBrightness(prev float64) { c.bright = prev }

// brighten scales a premultiplied color's RGB by f, clamped to alpha.
func brighten(col Color, f float64) Color {
	a := float64(col.A())
	ch := func(v uint8) uint32 { return uint32(math.Min(a, math.Round(float64(v)*f))) }
	return Color(uint32(col.A())<<24 | ch(col.R())<<16 | ch(col.G())<<8 | ch(col.B()))
}
