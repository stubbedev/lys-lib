package render

import "math"

// GradientKind is a gradient's geometry.
type GradientKind uint8

const (
	// GradientLinear runs along a line at an angle (CSS linear-gradient).
	GradientLinear GradientKind = iota
	// GradientRadial grows from a center out to an ellipse or circle
	// (CSS radial-gradient).
	GradientRadial
	// GradientConic sweeps around a center (CSS conic-gradient).
	GradientConic
)

// Extend says what lies beyond a gradient's first and last stops.
type Extend uint8

const (
	// ExtendPad continues the end colors (CSS's plain gradients).
	ExtendPad Extend = iota
	// ExtendRepeat repeats the stop range (CSS's repeating-*).
	ExtendRepeat
	// ExtendReflect mirrors the stop range back and forth (COLR).
	ExtendReflect
)

// RadialSize is a radial gradient's ending-shape size, the CSS
// keywords plus explicit radii.
type RadialSize uint8

const (
	// FarthestCorner reaches the farthest corner (the CSS default).
	FarthestCorner RadialSize = iota
	// ClosestSide touches the nearest side.
	ClosestSide
	// ClosestCorner reaches the nearest corner.
	ClosestCorner
	// FarthestSide touches the farthest side.
	FarthestSide
	// RadiusExplicit uses Gradient.RadiusX and RadiusY.
	RadiusExplicit
)

// Gradient is one gradient fill: its geometry, its stops (sorted by
// position; positions may repeat for a hard stop), and its extend
// mode. The zero value is a padded linear gradient pointing up; build
// one with Linear, Radial or Conic.
type Gradient struct {
	Kind   GradientKind
	Stops  []GradientStop
	Extend Extend
	// Angle is the linear direction, or the conic start, in degrees on
	// CSS's compass: 0 up, 90 right, clockwise.
	Angle float64
	// CenterX and CenterY place a radial or conic center as fractions
	// of the box (0.5 is its middle).
	CenterX, CenterY float64
	// Circle makes a radial gradient's ending shape a circle instead
	// of an ellipse.
	Circle bool
	// Size picks the radial ending shape's size; RadiusX and RadiusY
	// are its explicit radii in logical pixels under RadiusExplicit (a
	// circle takes RadiusX).
	Size             RadialSize
	RadiusX, RadiusY float64
}

// Linear is a linear gradient at angle (CSS degrees).
func Linear(angle float64, stops ...GradientStop) Gradient {
	return Gradient{Kind: GradientLinear, Angle: angle, Stops: stops}
}

// Radial is a centered farthest-corner ellipse.
func Radial(stops ...GradientStop) Gradient {
	return Gradient{Kind: GradientRadial, CenterX: 0.5, CenterY: 0.5, Stops: stops}
}

// Conic is a centered conic gradient starting at from (CSS degrees).
func Conic(from float64, stops ...GradientStop) Gradient {
	return Gradient{Kind: GradientConic, Angle: from, CenterX: 0.5, CenterY: 0.5, Stops: stops}
}

// Repeating returns g repeating its stop range (CSS repeating-*).
func (g Gradient) Repeating() Gradient {
	g.Extend = ExtendRepeat
	return g
}

// PaintGradient fills r with g, clipped to its rounded outline (radii
// in logical pixels). Colors interpolate premultiplied between the
// stops. A linear gradient's line spans the box's corners along its
// angle, CSS's rule; radial and conic geometry sizes against the box.
func (c *Canvas) PaintGradient(r Rect, radii Corners, g Gradient) {
	if len(g.Stops) == 0 {
		return
	}
	b, rc := c.devBox(r, radii)
	m, ok := g.mapping(b, float64(c.num)/float64(c.denom))
	if !ok {
		return
	}
	sp := c.span(b)
	for y := sp.Y; y < sp.Y+sp.H; y++ {
		for x := sp.X; x < sp.X+sp.W; x++ {
			px, py := float64(x)+0.5, float64(y)+0.5
			cov := coverage(sdBox(px, py, b, rc))
			if cov <= 0 {
				continue
			}
			c.blendCov(x, y, sampleStops(g.Stops, g.Extend, m.at(px, py)), cov)
		}
	}
}

// gradientMap maps device pixel centers to gradient positions for one
// box: a plain value, so the per-pixel loop allocates nothing.
type gradientMap struct {
	kind           GradientKind
	cx, cy         float64 // the center (radial, conic, linear's box middle)
	dx, dy, length float64 // linear direction and line length
	rx, ry         float64 // radial radii
	from           float64 // conic start
}

// mapping resolves g's geometry over the device box b (scale is device
// pixels per logical pixel); false when it is degenerate.
func (g Gradient) mapping(b frect, scale float64) (gradientMap, bool) {
	w, h := b.x1-b.x0, b.y1-b.y0
	m := gradientMap{kind: g.Kind, cx: b.x0 + g.CenterX*w, cy: b.y0 + g.CenterY*h, from: g.Angle}
	switch g.Kind {
	case GradientRadial:
		m.rx, m.ry = g.radii(b, m.cx, m.cy, scale)
		return m, m.rx > 0 && m.ry > 0
	case GradientConic:
		return m, true
	}
	rad := g.Angle * math.Pi / 180
	m.dx, m.dy = math.Sin(rad), -math.Cos(rad)
	m.length = math.Abs(w*m.dx) + math.Abs(h*m.dy)
	m.cx, m.cy = (b.x0+b.x1)/2, (b.y0+b.y1)/2
	return m, m.length != 0
}

// at is the gradient position of the device point (px, py).
func (m gradientMap) at(px, py float64) float64 {
	switch m.kind {
	case GradientRadial:
		dx, dy := (px-m.cx)/m.rx, (py-m.cy)/m.ry
		return math.Sqrt(dx*dx + dy*dy)
	case GradientConic:
		a := math.Atan2(px-m.cx, -(py-m.cy)) * 180 / math.Pi // clockwise from up
		return math.Mod(math.Mod(a-m.from, 360)+360, 360) / 360
	}
	return ((px-m.cx)*m.dx+(py-m.cy)*m.dy)/m.length + 0.5
}

// radii resolves a radial gradient's ending-shape radii in device
// pixels for center (cx, cy) in box b, per CSS's size keywords.
func (g Gradient) radii(b frect, cx, cy, scale float64) (rx, ry float64) {
	sideX := [2]float64{math.Abs(cx - b.x0), math.Abs(b.x1 - cx)}
	sideY := [2]float64{math.Abs(cy - b.y0), math.Abs(b.y1 - cy)}
	var sx, sy float64
	switch g.Size {
	case RadiusExplicit:
		rx, ry = g.RadiusX*scale, g.RadiusY*scale
		if g.Circle {
			ry = rx
		}
		return rx, ry
	case ClosestSide, ClosestCorner:
		sx, sy = min(sideX[0], sideX[1]), min(sideY[0], sideY[1])
	default: // FarthestSide, FarthestCorner
		sx, sy = max(sideX[0], sideX[1]), max(sideY[0], sideY[1])
	}
	corner := g.Size == ClosestCorner || g.Size == FarthestCorner
	if g.Circle {
		r := min(sx, sy)
		switch {
		case corner:
			r = math.Hypot(sx, sy)
		case g.Size == FarthestSide:
			r = max(sx, sy)
		}
		return r, r
	}
	if corner {
		// The ellipse keeps the side ellipse's aspect and passes
		// through the corner.
		return sx * math.Sqrt2, sy * math.Sqrt2
	}
	return sx, sy
}

// extendT folds a gradient position into the stop range [lo, hi] per
// the extend mode; padding leaves it for the sampler to clamp.
func extendT(t, lo, hi float64, ext Extend) float64 {
	span := hi - lo
	if span <= 0 {
		return t
	}
	switch ext {
	case ExtendRepeat:
		return lo + math.Mod(math.Mod(t-lo, span)+span, span)
	case ExtendReflect:
		u := math.Mod(math.Mod(t-lo, 2*span)+2*span, 2*span)
		if u > span {
			u = 2*span - u
		}
		return lo + u
	}
	return t
}

// sampleStops is the color at position t of sorted stops under ext.
func sampleStops(stops []GradientStop, ext Extend, t float64) Color {
	return gradientAt(stops, extendT(t, stops[0].Pos, stops[len(stops)-1].Pos, ext))
}
