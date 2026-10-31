package gpu

import (
	_ "embed" // the mask's GLSL below
	"fmt"
	"image"
	"image/color"
	"math"
)

// MaskGLSL is the GLSL of Mask.Coverage, maskCoverage, of GradientAt's
// texture coordinate, gradientUV, and, when SAMPLE names the sampling
// function (texture or texture2D), of KawaseAt's kawaseDown and
// kawaseUp, valid as GLSL 4.50 and as GLSL ES 1.00, for renderers
// outside this package to share.
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

// Bilinear samples pix, w x h premultiplied BGRA pixels stride bytes a
// row, at texture coordinate (u, v) as a linear sampler clamped to its
// edges does, returning B, G, R, A in 0..255.
func Bilinear(pix []byte, stride, w, h int, u, v float32) [4]float32 {
	x, y := u*float32(w)-0.5, v*float32(h)-0.5
	x0, y0 := int(math.Floor(float64(x))), int(math.Floor(float64(y)))
	fx, fy := x-float32(x0), y-float32(y0)
	at := func(i, j int) []byte {
		i, j = min(max(i, 0), w-1), min(max(j, 0), h-1)
		return pix[j*stride+i*4:]
	}
	var out [4]float32
	a, b, c, d := at(x0, y0), at(x0+1, y0), at(x0, y0+1), at(x0+1, y0+1)
	for k := range out {
		top := float32(a[k]) + (float32(b[k])-float32(a[k]))*fx
		bottom := float32(c[k]) + (float32(d[k])-float32(c[k]))*fx
		out[k] = top + (bottom-top)*fy
	}
	return out
}

// KawaseAt is Kawase's colour at texture coordinate (u, v) of a
// texture sample samples, with taps h source texels apart in texture
// coordinates.
func KawaseAt(sample func(u, v float32) [4]float32, up bool, u, v, hx, hy float32) [4]float32 {
	var sum [4]float32
	add := func(du, dv, weight float32) {
		s := sample(u+du, v+dv)
		for k := range sum {
			sum[k] += s[k] * weight
		}
	}
	total := float32(8)
	if up {
		total = 12
		for _, t := range [8][3]float32{{-2 * hx, 0, 1}, {-hx, hy, 2}, {0, 2 * hy, 1}, {hx, hy, 2}, {2 * hx, 0, 1}, {hx, -hy, 2}, {0, -2 * hy, 1}, {-hx, -hy, 2}} {
			add(t[0], t[1], t[2])
		}
	} else {
		add(0, 0, 4)
		for _, t := range [4][2]float32{{-hx, -hy}, {hx, hy}, {hx, -hy}, {-hx, hy}} {
			add(t[0], t[1], 1)
		}
	}
	for k := range sum {
		sum[k] /= total
	}
	return sum
}
