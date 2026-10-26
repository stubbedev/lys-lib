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

// Max is the widest of the four sides.
func (i Insets) Max() int { return max(i.Top, i.Right, i.Bottom, i.Left) }

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

// GradientStop is one color stop of a gradient: Pos is the fraction
// of the gradient line (or ray, or turn), 0 at its start and 1 at its
// end.
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

// frect is a float32 rect in device pixels.
type frect struct{ x0, y0, x1, y1 float32 }

// fcorners are device-pixel radii, clockwise from top-left.
type fcorners [4]float32

// devScale is the device scale num/denom as the float32 every
// device-space length multiplies by.
func devScale(num, denom int) float32 { return float32(num) / float32(denom) }

// devBox maps a logical rect and radii to device space, scaling the
// radii down uniformly when adjacent ones overlap (the CSS rule).
func (c *Canvas) devBox(r Rect, radii Corners) (frect, fcorners) {
	d := c.MapRect(r)
	f := devScale(c.num, c.denom)
	b := frect{float32(d.X), float32(d.Y), float32(d.X + d.W), float32(d.Y + d.H)}
	rc := fcorners{
		max(0, float32(radii.TopLeft)*f),
		max(0, float32(radii.TopRight)*f),
		max(0, float32(radii.BottomRight)*f),
		max(0, float32(radii.BottomLeft)*f),
	}
	return b, clampRadii(b, rc)
}

// DeviceBox is the rounded box r with radii as the shape painters
// rasterize it at device scale num/denom: its device rect as x0, y0,
// x1, y1 and its radii clockwise from top-left, scaled down together
// where adjacent ones overlap.
func DeviceBox(r Rect, radii Corners, num, denom int) (box, rc [4]float32) {
	c := Canvas{num: num, denom: denom}
	b, r32 := c.devBox(r, radii)
	return b.array(), r32
}

// clampRadii applies CSS's overlap rule: when two radii on one side sum
// past its length, every radius shrinks by the same factor.
func clampRadii(b frect, rc fcorners) fcorners {
	w, h := b.x1-b.x0, b.y1-b.y0
	scale := float32(1)
	check := func(sum, side float32) {
		if sum > side && sum > 0 {
			scale = min(scale, side/sum)
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

// sdBoxF is the signed distance from (px, py) to the rounded rect b
// (x0, y0, x1, y1) with per-corner radii rc: negative inside, positive
// outside, in device pixels. It works from the point's distances to its
// nearest edges, exact in half pixels, so a pixel the same distance
// from an edge gets the same value wherever the box sits and however
// long it is. Each step rounds on its own, in the shader's order.
func sdBoxF(px, py float32, b, rc [4]float32) float32 {
	left, top := px < (b[0]+b[2])*0.5, py < (b[1]+b[3])*0.5
	ex, ey := b[2]-px, b[3]-py
	if left {
		ex = px - b[0]
	}
	if top {
		ey = py - b[1]
	}
	var rad float32
	switch {
	case left && top:
		rad = rc[0]
	case top:
		rad = rc[1]
	case !left:
		rad = rc[2]
	default:
		rad = rc[3]
	}
	qx, qy := rad-ex, rad-ey
	return lengthF(max(qx, 0), max(qy, 0)) + min(max(qx, qy), 0) - rad
}

// lengthF is sqrt(x*x + y*y), each product rounded on its own and the
// root correctly rounded.
func lengthF(x, y float32) float32 {
	return float32(math.Sqrt(float64(float32(x*x) + float32(y*y)))) // each conversion rounds its product, so neither fuses into an FMA
}

// sdBox is sdBoxF over an frect and its corners.
func sdBox(px, py float32, b frect, rc fcorners) float32 {
	return sdBoxF(px, py, b.array(), rc)
}

// solidRun returns the pixel-center x range [xa, xb] of row center py
// where the rounded rect covers fully: at least half a pixel inside
// every straight edge and outside the corner squares, where sdBox is at
// most -0.5 and coverage exactly 1. ok is false for a row with no such
// range. The painters fill the run without evaluating the distance
// field, which is what a large rounded surface's interior costs.
func solidRun(b frect, rc fcorners, py float32) (xa, xb float32, ok bool) {
	if py-b.y0 < 0.5 || b.y1-py < 0.5 {
		return 0, 0, false
	}
	xa, xb = b.x0+0.5, b.x1-0.5
	if py < b.y0+rc[0] {
		xa = max(xa, b.x0+rc[0])
	}
	if py < b.y0+rc[1] {
		xb = min(xb, b.x1-rc[1])
	}
	if py > b.y1-rc[2] {
		xb = min(xb, b.x1-rc[2])
	}
	if py > b.y1-rc[3] {
		xa = max(xa, b.x0+rc[3])
	}
	return xa, xb, xa <= xb
}

// coverage maps a signed distance to anti-aliased pixel coverage.
func coverage(d float32) float32 { return min(1, max(0, 0.5-d)) }

// span returns the device pixel span of b, clipped.
func (c *Canvas) span(b frect) Rect {
	x0, y0 := int(math.Floor(float64(b.x0))), int(math.Floor(float64(b.y0)))
	r := Rect{X: x0, Y: y0, W: int(math.Ceil(float64(b.x1))) - x0, H: int(math.Ceil(float64(b.y1))) - y0}
	return c.clip.Intersect(r)
}

// blendCovF blends col at coverage cov in [0, 1], the coverage
// quantized as floor(cov*255 + 0.5), as the shader does.
func blendCovF(c *Canvas, x, y int, col Color, cov float32) {
	if cov <= 0 {
		return
	}
	if cov >= 1 {
		c.blend(x, y, col)
		return
	}
	c.blend(x, y, coverageScale(col, cov255(cov)))
}

// cov255 quantizes coverage in [0, 1] to 0..255, rounding half up.
func cov255(cov float32) uint32 {
	return uint32(math.Floor(float64(float32(cov*255) + 0.5))) // the conversion rounds the product, so it cannot fuse into an FMA
}

// RoundedRectCorners blends col over r with each corner rounded by its
// own radius, anti-aliased. Radii that overlap scale down together, per
// CSS.
func (c *Canvas) RoundedRectCorners(r Rect, radii Corners, col Color) {
	if c.rec != nil {
		c.record(op{kind: opRRect, r: r, radii: radii, col: col})
		return
	}
	if col == 0 {
		return
	}
	b, rc := c.devBox(r, radii)
	b32, rc32 := b.array(), rc
	sp := c.span(b)
	for y := sp.Y; y < sp.Y+sp.H; y++ {
		py := float32(y) + 0.5
		xs, xe := sp.X+sp.W, sp.X+sp.W
		if xa, xb, solid := solidRun(b, rc, py); solid {
			xs = min(max(sp.X, int(math.Ceil(float64(xa-0.5)))), sp.X+sp.W)
			xe = min(max(xs, int(math.Floor(float64(xb-0.5)))+1), sp.X+sp.W)
		}
		for x := sp.X; x < xs; x++ {
			blendCovF(c, x, y, col, coverage(sdBoxF(float32(x)+0.5, py, b32, rc32)))
		}
		c.blendRun(xs, xe, y, col)
		for x := xe; x < sp.X+sp.W; x++ {
			blendCovF(c, x, y, col, coverage(sdBoxF(float32(x)+0.5, py, b32, rc32)))
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
	if c.rec != nil {
		c.record(op{kind: opBorder, r: r, radii: radii, ins: widths, cols: cols})
		return
	}
	if widths.Zero() {
		return
	}
	g := c.borderRing(r, radii, widths)
	b, rc, w, in, irc := g.outer, g.radii, g.widths, g.inner, g.iradii
	innerEmpty := in.x1 <= in.x0 || in.y1 <= in.y0
	b32, rc32, in32, irc32, inv := b.array(), rc, in.array(), irc, SideReciprocals(w)
	// ring is a pixel's coverage and side, in float32 as the shader
	// computes them.
	ring := func(x, y int) (float32, int) {
		px, py := float32(x)+0.5, float32(y)+0.5
		cov := coverage(sdBoxF(px, py, b32, rc32))
		if cov > 0 && !innerEmpty {
			cov = min(cov, 1-coverage(sdBoxF(px, py, in32, irc32)))
		}
		if cov <= 0 {
			return 0, 0
		}
		return cov, borderSide(px, py, b32, inv)
	}
	sp := c.span(b)
	// Across the top and bottom bands, clear of the corners' radii
	// and of the side widths a pixel could be closer to, coverage and
	// side vary only by row: one pixel stands for the run.
	runX0 := min(max(sp.X, int(math.Ceil(float64(b.x0+max(rc[0], rc[3], w[3]))))+1), sp.X+sp.W)
	runX1 := max(min(sp.X+sp.W, int(math.Floor(float64(b.x1-max(rc[1], rc[2], w[1]))))-1), runX0)
	// Down the side columns, rows clear of the corners and of the
	// bands, past every width a pixel there could be closer to, vary
	// only by column: one row's cells stand for them all.
	reachY := max(rc[0], rc[1], rc[2], rc[3], w[0], w[1], w[2], w[3]) + 2
	colY0, colY1 := b.y0+reachY, b.y1-reachY
	var cols1, cols2 []ringCell
	for y := sp.Y; y < sp.Y+sp.H; y++ {
		py := float32(y) + 0.5
		if !innerEmpty && (py < in.y0 || py > in.y1) && runX1 > runX0 {
			for x := sp.X; x < runX0; x++ {
				c.ringPixel(x, y, ring, cols)
			}
			if cov, side := ring(runX0, y); cov > 0 {
				col := cols[side]
				if cov < 1 {
					col = coverageScale(col, cov255(cov))
				}
				c.blendRun(runX0, runX1, y, col)
			}
			for x := runX1; x < sp.X+sp.W; x++ {
				c.ringPixel(x, y, ring, cols)
			}
			continue
		}
		// The inner shape's solid run is the ring's hole: coverage 0,
		// skipped whole.
		hs, he := sp.X+sp.W, sp.X+sp.W
		if xa, xb, hole := solidRun(in, irc, py); hole && !innerEmpty {
			hs = min(max(sp.X, int(math.Ceil(float64(xa-0.5)))), sp.X+sp.W)
			he = min(max(hs, int(math.Floor(float64(xb-0.5)))+1), sp.X+sp.W)
		}
		if py > colY0 && py < colY1 {
			if cols1 == nil {
				n1, n2 := max(0, hs-sp.X), max(0, sp.X+sp.W-he)
				if cap(c.ringCells) < n1+n2 {
					c.ringCells = make([]ringCell, n1+n2)
				}
				cols1, cols2 = c.ringCells[:n1], c.ringCells[n1:n1+n2]
				ringColumn(cols1, sp.X, y, ring)
				ringColumn(cols2, he, y, ring)
			}
			c.ringColumnRow(sp.X, y, cols1, cols)
			c.ringColumnRow(he, y, cols2, cols)
			continue
		}
		for x := sp.X; x < hs; x++ {
			c.ringPixel(x, y, ring, cols)
		}
		for x := he; x < sp.X+sp.W; x++ {
			c.ringPixel(x, y, ring, cols)
		}
	}
}

// ring is a border's geometry in device pixels: the outer box and
// radii, the side widths (top, right, bottom, left), and the inner box
// and radii, each inner radius shrunk by its corner's wider side.
type ring struct {
	outer, inner  frect
	radii, iradii fcorners
	widths        [4]float32
}

// borderRing is the ring RoundedBorderSides strokes.
func (c *Canvas) borderRing(r Rect, radii Corners, widths Insets) ring {
	b, rc := c.devBox(r, radii)
	f := devScale(c.num, c.denom)
	w := [4]float32{
		float32(widths.Top) * f, float32(widths.Right) * f,
		float32(widths.Bottom) * f, float32(widths.Left) * f,
	}
	return ring{
		outer: b, radii: rc, widths: w,
		inner: frect{b.x0 + w[3], b.y0 + w[0], b.x1 - w[1], b.y1 - w[2]},
		iradii: fcorners{
			max(0, rc[0]-max(w[3], w[0])),
			max(0, rc[1]-max(w[0], w[1])),
			max(0, rc[2]-max(w[1], w[2])),
			max(0, rc[3]-max(w[2], w[3])),
		},
	}
}

// BorderRing is RoundedBorderSides' ring at device scale num/denom:
// the outer box (x0, y0, x1, y1) and radii, the inner box and radii,
// and the side widths, top, right, bottom, left.
func BorderRing(r Rect, radii Corners, widths Insets, num, denom int) (outer, oradii, inner, iradii, w [4]float32) {
	c := Canvas{num: num, denom: denom}
	g := c.borderRing(r, radii, widths)
	return g.outer.array(), g.radii, g.inner.array(), g.iradii, g.widths
}

// array is b as x0, y0, x1, y1.
func (b frect) array() [4]float32 { return [4]float32{b.x0, b.y0, b.x1, b.y1} }

// ringCell is a ring column's coverage and side.
type ringCell struct {
	cov  float32
	side int
}

// ringColumn fills out with the ring's cells from x0 on, on a row
// between the corners and clear of the top and bottom bands, where
// they hold for every such row.
func ringColumn(out []ringCell, x0, y int, ring func(x, y int) (float32, int)) {
	for i := range out {
		out[i].cov, out[i].side = ring(x0+i, y)
	}
}

// ringColumnRow blends a row of ring cells from x0 on.
func (c *Canvas) ringColumnRow(x0, y int, cells []ringCell, cols [4]Color) {
	for i, cell := range cells {
		if cell.cov > 0 {
			blendCovF(c, x0+i, y, cols[cell.side], cell.cov)
		}
	}
}

// ringPixel blends one border ring pixel by its coverage and side.
func (c *Canvas) ringPixel(x, y int, ring func(x, y int) (float32, int), cols [4]Color) {
	if cov, side := ring(x, y); cov > 0 {
		blendCovF(c, x, y, cols[side], cov)
	}
}

// borderSide picks the side a ring pixel belongs to: the one whose
// width-normalized distance from its edge (the distance times the
// side's reciprocal width, inv) is smallest, skipping zero-width sides,
// whose inv is 0.
func borderSide(px, py float32, b, inv [4]float32) int {
	d := [4]float32{py - b[1], b[2] - px, b[3] - py, px - b[0]}
	best, bestV := 0, float32(math.Inf(1))
	for i := range 4 {
		if inv[i] == 0 {
			continue
		}
		if v := d[i] * inv[i]; v < bestV {
			best, bestV = i, v
		}
	}
	return best
}

// SideReciprocals are a border's reciprocal side widths (top, right,
// bottom, left), 0 for a side of no width: what borderSide, on the CPU
// and in the shader, scales each side's distance by.
func SideReciprocals(w [4]float32) [4]float32 {
	var inv [4]float32
	for i, v := range w {
		if v > 0 {
			inv[i] = Reciprocal(v)
		}
	}
	return inv
}

// gradientAt samples the stop list at t, in float32 as At computes t:
// between two stops each channel lerps and rounds half up.
func gradientAt(stops []GradientStop, t float32) Color {
	if t <= float32(stops[0].Pos) {
		return stops[0].Color
	}
	for i := 1; i < len(stops); i++ {
		a, b := stops[i-1], stops[i]
		bp := float32(b.Pos)
		if t > bp {
			continue
		}
		ap := float32(a.Pos)
		if bp-ap <= 0 {
			return b.Color
		}
		return lerp32(a.Color, b.Color, (t-ap)*Reciprocal(bp-ap))
	}
	return stops[len(stops)-1].Color
}

// boxShadowRaster is BoxShadow's cached raster for the box r, the
// box's device rect, the raster's core size, and the device rect the
// shadow covers once the core is stretched to the box.
func (c *Canvas) boxShadowRaster(r Rect, radii Corners, s BoxShadow) (Rect, *ShadowMask, int, int, Rect) {
	dev := c.MapRect(r)
	kw, kh := shadowCore(dev, radii, s, c.num, c.denom)
	ras := boxShadows.get(boxShadowKey{
		w: kw, h: kh, radii: radii,
		x: s.X, y: s.Y, blur: s.Blur, spread: s.Spread, inset: s.Inset,
		num: c.num, denom: c.denom,
	}, func() *ShadowMask { return buildBoxShadow(kw, kh, radii, s, c.num, c.denom) })
	full := Rect{X: dev.X + ras.x0, Y: dev.Y + ras.y0, W: ras.w + dev.W - kw, H: ras.h + dev.H - kh}
	return dev, ras, kw, kh, full
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
	if c.rec != nil {
		c.record(op{kind: opBoxShadow, r: r, radii: radii, sh: s})
		return
	}
	if s.Color.A() == 0 || c.clip.Empty() {
		return
	}
	dev, ras, kw, kh, full := c.boxShadowRaster(r, radii, s)
	// The raster is the core box's; a longer box repeats the core's
	// middle row and column, where every edge is straight.
	midX, midY := kw/2, kh/2
	area := c.clip.Intersect(full)
	if area.Empty() {
		return
	}
	stretch := func(b, full, core, mid int) int {
		switch {
		case b < mid:
			return b
		case b >= mid+full-core:
			return b - (full - core)
		}
		return mid
	}
	// Box-relative columns of the stretched middle, clipped to the area.
	m0 := min(max(dev.X+midX, area.X), area.X+area.W)
	m1 := min(max(dev.X+midX+dev.W-kw, m0), area.X+area.W)
	for y := area.Y; y < area.Y+area.H; y++ {
		sy := stretch(y-dev.Y, dev.H, kh, midY) - ras.y0
		row := ras.cov[sy*ras.w : (sy+1)*ras.w]
		at := func(x int) int { return stretch(x-dev.X, dev.W, kw, midX) - ras.x0 }
		c.blendMask(area.X, y, row[at(area.X):at(area.X)+m0-area.X], s.Color)
		if q := uint32(row[midX-ras.x0]); q != 0 && m1 > m0 {
			c.blendRun(m0, m1, y, coverageScale(s.Color, q))
		}
		if end := area.X + area.W; end > m1 {
			c.blendMask(m1, y, row[at(m1):at(m1)+end-m1], s.Color)
		}
	}
}

// shadowCore is the box a shadow's raster is built for: dev itself,
// or, past the extent its corners reach (radius, offset, spread and
// blur), a core just long enough to hold both corners and one straight
// middle row or column. Overlapping radii scale with the box (CSS), so
// a box whose radii overlap keeps its own size.
func shadowCore(dev Rect, radii Corners, s BoxShadow, num, denom int) (w, h int) {
	f := devScale(num, denom)
	tl, tr := float32(radii.TopLeft)*f, float32(radii.TopRight)*f
	br, bl := float32(radii.BottomRight)*f, float32(radii.BottomLeft)*f
	if tl+tr > float32(dev.W) || bl+br > float32(dev.W) || tl+bl > float32(dev.H) || tr+br > float32(dev.H) {
		return dev.W, dev.H
	}
	reach := max(tl, tr, br, bl) + float32(float32(max(abs(s.X), abs(s.Y))+abs(s.Spread))*f) + float32(1.5*float32(s.Blur)*f)
	core := 2*(int(math.Ceil(float64(reach)))+2) + 1
	return min(dev.W, core), min(dev.H, core)
}

// boxShadowKey identifies a CSS box-shadow's coverage: everything but
// the color and the box's position, which the field does not depend on
// (device boxes sit on whole pixels).
type boxShadowKey struct {
	w, h               int
	radii              Corners
	x, y, blur, spread int
	inset              bool
	num, denom         int
}

// ShadowMask is a box-shadow's coverage field over its core box's
// span, whose top-left sits at (x0, y0) from the box's, immutable once
// made: a GPU renderer keys its atlas by it.
type ShadowMask struct {
	covRaster
	x0, y0 int
}

// Origin is the mask's top-left from its core box's.
func (r *ShadowMask) Origin() (x0, y0 int) { return r.x0, r.y0 }

// BoxShadowMask is how BoxShadow paints s for the box r at device scale
// num/denom: the cached mask of a core box coreW x coreH, the box's
// device rect, and the core's size. A shadow pixel (x, y) takes the
// mask's coverage at (stretch(x-box.X, box.W, coreW), stretch(y-box.Y,
// box.H, coreH)) less Origin, where stretch maps the box's columns onto
// the core's, repeating its middle one across a longer box: b below
// core/2 stays b, b from core/2+full-core on drops full-core, and the
// rest is core/2.
func BoxShadowMask(r Rect, radii Corners, s BoxShadow, num, denom int) (m *ShadowMask, box Rect, coreW, coreH int) {
	c := Canvas{num: num, denom: denom}
	box, m, coreW, coreH, _ = c.boxShadowRaster(r, radii, s)
	return m, box, coreW, coreH
}

func (r *ShadowMask) size() int { return len(r.cov) }

var boxShadows rasterCache[boxShadowKey, *ShadowMask]

// buildBoxShadow rasterizes one shadow layer's coverage for a w x h
// device box at the origin: an outer shadow is the box offset and
// grown by the spread, blurred, and clipped to outside the box itself;
// an inset shadow fills the box's inside except the offset,
// spread-shrunk hole, blurred at the hole's edge. The blur is Gaussian
// with sigma half the blur radius, evaluated per pixel from the
// silhouette's signed distance.
func buildBoxShadow(w, h int, radii Corners, s BoxShadow, num, denom int) *ShadowMask {
	f := devScale(num, denom)
	box := frect{0, 0, float32(w), float32(h)}
	rc := clampRadii(box, fcorners{
		max(0, float32(radii.TopLeft)*f),
		max(0, float32(radii.TopRight)*f),
		max(0, float32(radii.BottomRight)*f),
		max(0, float32(radii.BottomLeft)*f),
	})
	ox, oy := float32(s.X)*f, float32(s.Y)*f
	spread := float32(s.Spread) * f
	sigma := float32(s.Blur) * f / 2
	shape := func(sign float32) (frect, fcorners) {
		g := spread * sign
		sb := frect{box.x0 + ox - g, box.y0 + oy - g, box.x1 + ox + g, box.y1 + oy + g}
		var src fcorners
		for i := range rc {
			if rc[i] > 0 {
				src[i] = max(0, rc[i]+g)
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
	blurCov := func(d float32) float32 {
		if sigma <= 0.5 {
			return coverage(d)
		}
		return float32(0.5 * math.Erfc(float64(d)/(float64(sigma)*math.Sqrt2)))
	}
	reach := 3 * sigma
	span := box
	if !s.Inset {
		sb, _ := shape(1)
		span = frect{sb.x0 - reach, sb.y0 - reach, sb.x1 + reach, sb.y1 + reach}
	}
	x0, y0 := int(math.Floor(float64(span.x0))), int(math.Floor(float64(span.y0)))
	ras := &ShadowMask{x0: x0, y0: y0}
	ras.w, ras.h = int(math.Ceil(float64(span.x1)))-x0, int(math.Ceil(float64(span.y1)))-y0
	ras.cov = make([]uint8, max(0, ras.w*ras.h))
	put := func(x, y int, cov float32) {
		if cov > 0 {
			ras.cov[(y-y0)*ras.w+(x-x0)] = uint8(cov255(min(cov, 1)))
		}
	}
	if !s.Inset {
		sb, src := shape(1)
		for y := y0; y < y0+ras.h; y++ {
			py := float32(y) + 0.5
			// The box's own solid run is fully covered: no shadow there.
			xa, xb, solid := solidRun(box, rc, py)
			for x := x0; x < x0+ras.w; x++ {
				px := float32(x) + 0.5
				if solid && px >= xa && px <= xb {
					continue
				}
				if outside := 1 - coverage(sdBox(px, py, box, rc)); outside > 0 {
					put(x, y, blurCov(sdBox(px, py, sb, src))*outside)
				}
			}
		}
		return ras
	}
	hole, hrc := shape(-1)
	// Deeper than the blur's 3-sigma reach inside the hole, the shadow
	// rounds to nothing: the hole shrunk by the reach is skipped whole.
	deep := frect{hole.x0 + reach, hole.y0 + reach, hole.x1 - reach, hole.y1 - reach}
	var drc fcorners
	for i := range hrc {
		drc[i] = max(0, hrc[i]-reach)
	}
	deepOK := deep.x1 > deep.x0 && deep.y1 > deep.y0
	for y := y0; y < y0+ras.h; y++ {
		py := float32(y) + 0.5
		xa, xb, skip := solidRun(deep, drc, py)
		skip = skip && deepOK
		for x := x0; x < x0+ras.w; x++ {
			px := float32(x) + 0.5
			if skip && px >= xa && px <= xb {
				continue
			}
			if inside := coverage(sdBox(px, py, box, rc)); inside > 0 {
				put(x, y, (1-blurCov(sdBox(px, py, hole, hrc)))*inside)
			}
		}
	}
	return ras
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

// brighten scales a premultiplied color's RGB by f, clamped to alpha,
// in float32 as the shader does: each channel floor(v*f + 0.5).
func brighten(col Color, f float64) Color {
	a, f32 := uint32(col.A()), float32(f)
	ch := func(v uint8) uint32 {
		return min(a, uint32(math.Floor(float64(float32(float32(v)*f32)+0.5))))
	}
	return Color(uint32(col.A())<<24 | ch(col.R())<<16 | ch(col.G())<<8 | ch(col.B()))
}

// RoundedRectRegion decomposes the rounded rect r into the rects a
// rectangle-only region (a wl_region) can carry: per pixel row, the
// pixels whose centers lie inside the shape, with consecutive rows of
// the same span merged. Radii that overlap scale down together, per
// CSS. A plain rect is one rect.
func RoundedRectRegion(r Rect, radii Corners) []Rect {
	if r.Empty() {
		return nil
	}
	b := frect{float32(r.X), float32(r.Y), float32(r.X + r.W), float32(r.Y + r.H)}
	rc := clampRadii(b, fcorners{
		max(0, float32(radii.TopLeft)), max(0, float32(radii.TopRight)),
		max(0, float32(radii.BottomRight)), max(0, float32(radii.BottomLeft)),
	})
	// inset is how far a corner of radius rad pulls the edge in at the
	// row whose center sits dy into the corner's square.
	inset := func(rad, dy float32) float32 {
		d := rad - dy
		return rad - float32(math.Sqrt(float64(max(0, float32(rad*rad)-float32(d*d)))))
	}
	var out []Rect
	for y := r.Y; y < r.Y+r.H; y++ {
		cy := float32(y) + 0.5
		var left, right float32
		if dy := cy - b.y0; dy < rc[0] {
			left = inset(rc[0], dy)
		}
		if dy := b.y1 - cy; dy < rc[3] {
			left = max(left, inset(rc[3], dy))
		}
		if dy := cy - b.y0; dy < rc[1] {
			right = inset(rc[1], dy)
		}
		if dy := b.y1 - cy; dy < rc[2] {
			right = max(right, inset(rc[2], dy))
		}
		// A column is in when its center is: x + 0.5 >= x0 + left.
		x0 := r.X + int(math.Ceil(float64(left-0.5)))
		x1 := r.X + r.W - int(math.Ceil(float64(right-0.5)))
		if x1 <= x0 {
			continue
		}
		if n := len(out); n > 0 && out[n-1].X == x0 && out[n-1].X+out[n-1].W == x1 && out[n-1].Y+out[n-1].H == y {
			out[n-1].H++
			continue
		}
		out = append(out, Rect{X: x0, Y: y, W: x1 - x0, H: 1})
	}
	return out
}

// PaintGrain blends a fine luminance noise over the rounded rect r:
// each device pixel lightens or darkens by a hashed amount up to
// opacity, the etching of frosted glass. The noise is anchored at the
// logical origin (ox, oy) - the surface it belongs to - so it moves
// with the surface and repaints identically: a pure function of the
// pixel's offset from the origin, deterministic on every machine.
func (c *Canvas) PaintGrain(r Rect, radii Corners, opacity float64, ox, oy int) {
	if c.rec != nil {
		c.record(op{kind: opGrain, r: r, radii: radii, f: [6]float64{opacity}, i: [5]int{ox, oy}})
		return
	}
	if opacity <= 0 || c.clip.Empty() {
		return
	}
	b, rc := c.devBox(r, radii)
	b32, rc32 := b.array(), rc
	o := c.MapRect(Rect{X: ox, Y: oy})
	strength := GrainStrength(opacity)
	sp := c.span(b)
	for y := sp.Y; y < sp.Y+sp.H; y++ {
		py := float32(y) + 0.5
		xa, xb, solid := solidRun(b, rc, py)
		for x := sp.X; x < sp.X+sp.W; x++ {
			px := float32(x) + 0.5
			cov := uint32(255)
			if !solid || px < xa || px > xb {
				cov = cov255(coverage(sdBoxF(px, py, b32, rc32)))
			}
			n := grainNoise(x-o.X, y-o.Y)
			// Signed around the midpoint: above lightens, below darkens.
			ink, amt := RGB(255, 255, 255), n-128
			if n < 128 {
				ink, amt = RGB(0, 0, 0), 127-n
			}
			if a := amt * 2 * strength / 255 * cov / 255; a > 0 {
				c.blend(x, y, coverageScale(ink, a))
			}
		}
	}
}

// GrainStrength is PaintGrain's opacity as the 0..255 factor its noise
// scales by.
func GrainStrength(opacity float64) uint32 {
	return uint32(math.Round(math.Min(opacity, 1) * 255))
}

// grainNoise is a hashed byte for a pixel offset: white noise without
// a texture to store, the same for the same offset everywhere. The
// shader's grainNoise repeats it.
func grainNoise(x, y int) uint32 {
	h := uint32(x)*0x27d4eb2d ^ uint32(y)*0x165667b1
	h ^= h >> 15
	h *= 0x85ebca6b
	h ^= h >> 13
	return h & 0xff
}
