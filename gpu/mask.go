package gpu

import (
	_ "embed" // the mask's GLSL below
	"fmt"
	"image"
	"image/color"
	"math"
)

// MaskGLSL is the GLSL of Mask.Coverage, maskCoverage, and of
// GradientAt's texture coordinate, gradientUV, valid as GLSL 4.50 and
// as GLSL ES 1.00, for renderers outside this package to share.
//
//go:embed shaders/mask.glsl
var MaskGLSL string

// RoundRect is a rectangle in target pixels whose corners are rounded
// by Radius, clamped to half its shorter side.
type RoundRect struct {
	Rect   image.Rectangle
	Radius float32
}

// Falloff softens Keep's edge: coverage falls from 1 at the edge to 0
// Range pixels outside it, as (1 - d/Range)^Power, a drop shadow's
// shape. The zero Falloff is an anti-aliased edge.
type Falloff struct {
	Range, Power float32
}

// Mask limits a draw to the pixels inside Keep and outside Cut, each
// edge anti-aliased by its signed distance at the pixel centre, Keep's
// softened by Falloff. An empty Keep keeps everything; an empty Cut
// cuts nothing.
type Mask struct {
	Keep, Cut RoundRect
	Falloff   Falloff
}

// Check reports a radius or falloff range that is negative, infinite
// or NaN, and a falloff with a range whose power is not positive.
func (m Mask) Check() error {
	for _, v := range [3]float32{m.Keep.Radius, m.Cut.Radius, m.Falloff.Range} {
		if !(v >= 0) || math.IsInf(float64(v), 0) {
			return fmt.Errorf("gpu: mask radius or falloff %v is not zero or more", v)
		}
	}
	if m.Falloff.Range > 0 && (!(m.Falloff.Power > 0) || math.IsInf(float64(m.Falloff.Power), 0)) {
		return fmt.Errorf("gpu: falloff power %v is not positive", m.Falloff.Power)
	}
	return nil
}

func roundRectDistance(px, py float32, r image.Rectangle, radius float32) float32 {
	hx, hy := float32(r.Dx())*0.5, float32(r.Dy())*0.5
	rad := min(radius, min(hx, hy))
	cx, cy := float32(r.Min.X+r.Max.X)*0.5, float32(r.Min.Y+r.Max.Y)*0.5
	qx := float32(math.Abs(float64(px-cx))) - hx + rad
	qy := float32(math.Abs(float64(py-cy))) - hy + rad
	mx, my := max(qx, 0), max(qy, 0)
	return float32(math.Sqrt(float64(mx*mx+my*my))) + min(max(qx, qy), 0) - rad
}

func edgeCoverage(d float32) float32 {
	return min(max(0.5-d, 0), 1)
}

func (f Falloff) coverage(d float32) float32 {
	if f.Range <= 0 {
		return edgeCoverage(d)
	}
	return float32(math.Pow(float64(min(max(1-d/f.Range, 0), 1)), float64(f.Power)))
}

// Coverage is the fraction of the pixel whose centre is (px, py) that
// the mask keeps, as the shaders compute it.
func (m Mask) Coverage(px, py float32) float32 {
	k := float32(1)
	if !m.Keep.Rect.Empty() {
		k = m.Falloff.coverage(roundRectDistance(px, py, m.Keep.Rect, m.Keep.Radius))
	}
	if !m.Cut.Rect.Empty() {
		k *= 1 - edgeCoverage(roundRectDistance(px, py, m.Cut.Rect, m.Cut.Radius))
	}
	return k
}

// GradientAt is the premultiplied colour FillGradient gives the pixel
// whose centre is (px, py), for stops spread from from to to, as the
// shader computes it.
func GradientAt(from, to [2]float32, stops []color.RGBA, px, py float32) color.RGBA {
	lx, ly := to[0]-from[0], to[1]-from[1]
	t := min(max(((px-from[0])*lx+(py-from[1])*ly)/(lx*lx+ly*ly), 0), 1)
	u := t * float32(len(stops)-1)
	i := min(int(u), len(stops)-2)
	f := u - float32(i)
	a, b := stops[i], stops[i+1]
	mix := func(x, y uint8) uint8 { return uint8(math.Round(float64(float32(x) + (float32(y)-float32(x))*f))) }
	return color.RGBA{mix(a.R, b.R), mix(a.G, b.G), mix(a.B, b.B), mix(a.A, b.A)}
}
