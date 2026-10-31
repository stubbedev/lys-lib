package gpu

import (
	_ "embed" // the mask's GLSL below
	"fmt"
	"image"
	"math"
)

// MaskGLSL is the GLSL of Mask.Coverage, valid as GLSL 4.50 and as
// GLSL ES 1.00, for renderers outside this package to share.
//
//go:embed shaders/mask.glsl
var MaskGLSL string

// RoundRect is a rectangle in target pixels whose corners are rounded
// by Radius, clamped to half its shorter side.
type RoundRect struct {
	Rect   image.Rectangle
	Radius float32
}

// Mask limits a draw to the pixels inside Keep and outside Cut, each
// anti-aliased by its signed distance at the pixel centre. An empty
// Keep keeps everything; an empty Cut cuts nothing.
type Mask struct {
	Keep, Cut RoundRect
}

// Check reports a radius that is negative, infinite or NaN.
func (m Mask) Check() error {
	for _, r := range [2]RoundRect{m.Keep, m.Cut} {
		if !(r.Radius >= 0) || math.IsInf(float64(r.Radius), 0) {
			return fmt.Errorf("gpu: mask radius %v is not zero or more", r.Radius)
		}
	}
	return nil
}

func roundRectCoverage(px, py float32, r image.Rectangle, radius float32) float32 {
	hx, hy := float32(r.Dx())*0.5, float32(r.Dy())*0.5
	rad := min(radius, min(hx, hy))
	cx, cy := float32(r.Min.X+r.Max.X)*0.5, float32(r.Min.Y+r.Max.Y)*0.5
	qx := float32(math.Abs(float64(px-cx))) - hx + rad
	qy := float32(math.Abs(float64(py-cy))) - hy + rad
	mx, my := max(qx, 0), max(qy, 0)
	d := float32(math.Sqrt(float64(mx*mx+my*my))) + min(max(qx, qy), 0) - rad
	return min(max(0.5-d, 0), 1)
}

// Coverage is the fraction of the pixel whose centre is (px, py) that
// the mask keeps, as the shaders compute it.
func (m Mask) Coverage(px, py float32) float32 {
	k, c := float32(1), float32(0)
	if !m.Keep.Rect.Empty() {
		k = roundRectCoverage(px, py, m.Keep.Rect, m.Keep.Radius)
	}
	if !m.Cut.Rect.Empty() {
		c = roundRectCoverage(px, py, m.Cut.Rect, m.Cut.Radius)
	}
	return k * (1 - c)
}
