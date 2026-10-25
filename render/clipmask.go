package render

import (
	"image"
	"image/draw"
	"math"

	"golang.org/x/image/vector"
)

// The clip-path machinery: a CSS clip shape becomes a Path (inset with
// rounded corners, circle and ellipse as four-arc cubics, polygon, or
// a path() string), and the path's coverage multiplies a layer's
// pixels — the same alpha-mask step a mask-image goes through.

// kappa is the cubic Bézier circle constant: the control-point handle
// for a quarter arc.
const kappa = 0.5522847498307936

// RoundedRectPath appends the rounded rect r (logical pixels, radii
// clockwise from top-left) to p as one closed subpath.
func RoundedRectPath(p *Path, r Rect, radii Corners) {
	tl := clampRadius(radii.TopLeft, r.W, r.H)
	tr := clampRadius(radii.TopRight, r.W, r.H)
	br := clampRadius(radii.BottomRight, r.W, r.H)
	bl := clampRadius(radii.BottomLeft, r.W, r.H)
	x0, y0 := float64(r.X), float64(r.Y)
	x1, y1 := float64(r.X+r.W), float64(r.Y+r.H)
	p.MoveTo(x0+tl, y0)
	p.LineTo(x1-tr, y0)
	if k := kappa * tr; k > 0 {
		p.CubeTo(x1-tr+k, y0, x1, y0+tr-k, x1, y0+tr)
	}
	p.LineTo(x1, y1-br)
	if k := kappa * br; k > 0 {
		p.CubeTo(x1, y1-br+k, x1-br+k, y1, x1-br, y1)
	}
	p.LineTo(x0+bl, y1)
	if k := kappa * bl; k > 0 {
		p.CubeTo(x0+bl-k, y1, x0, y1-bl+k, x0, y1-bl)
	}
	p.LineTo(x0, y0+tl)
	if k := kappa * tl; k > 0 {
		p.CubeTo(x0, y0+tl-k, x0+tl-k, y0, x0+tl, y0)
	}
	p.Close()
}

// clampRadius bounds a corner radius by half of each side it spans.
func clampRadius(rad, w, h int) float64 {
	return math.Min(float64(rad), float64(min(w, h))/2)
}

// EllipsePath appends the ellipse centered at (cx, cy) with radii rx
// and ry (logical pixels) to p as one closed subpath.
func EllipsePath(p *Path, cx, cy, rx, ry float64) {
	if rx <= 0 || ry <= 0 {
		return
	}
	kx, ky := kappa*rx, kappa*ry
	p.MoveTo(cx+rx, cy)
	p.CubeTo(cx+rx, cy+ky, cx+kx, cy+ry, cx, cy+ry)
	p.CubeTo(cx-kx, cy+ry, cx-rx, cy+ky, cx-rx, cy)
	p.CubeTo(cx-rx, cy-ky, cx-kx, cy-ry, cx, cy-ry)
	p.CubeTo(cx+kx, cy-ry, cx+rx, cy-ky, cx+rx, cy)
	p.Close()
}

// PathCoverage rasterizes p's fill coverage over box (device pixels,
// p scaled by the device scale) into a fresh alpha image; nil when
// nothing covers.
func PathCoverage(p *Path, box Rect, scale float64) *image.Alpha {
	if p == nil || p.Empty() || box.Empty() {
		return nil
	}
	z := vector.NewRasterizer(box.W, box.H)
	mx := func(x float64) float32 { return float32(x*scale - float64(box.X)) }
	my := func(y float64) float32 { return float32(y*scale - float64(box.Y)) }
	walkPath(p,
		func(x, y float64) { z.MoveTo(mx(x), my(y)) },
		func(x, y float64) { z.LineTo(mx(x), my(y)) },
		func(x1, y1, x2, y2, x, y float64) {
			z.CubeTo(mx(x1), my(y1), mx(x2), my(y2), mx(x), my(y))
		},
		func() { z.ClosePath() })
	mask := image.NewAlpha(image.Rect(0, 0, box.W, box.H))
	z.DrawOp = draw.Src
	z.Draw(mask, mask.Bounds(), image.Opaque, image.Point{})
	return mask
}

// walkPath replays p's segments through the move, line, cube and
// close callbacks.
func walkPath(p *Path, move func(float64, float64), line func(float64, float64), cube func(x1, y1, x2, y2, x, y float64), close func()) {
	open := false
	for _, op := range p.ops {
		switch op.kind {
		case pathMove:
			if open {
				close()
			}
			move(op.pts[0][0], op.pts[0][1])
			open = true
		case pathLine:
			line(op.pts[0][0], op.pts[0][1])
		case pathCube:
			cube(op.pts[0][0], op.pts[0][1], op.pts[1][0], op.pts[1][1], op.pts[2][0], op.pts[2][1])
		case pathClose:
			if open {
				close()
				open = false
			}
		}
	}
	if open {
		close()
	}
}

// ApplyPathMask multiplies l's pixels inside region by the path's
// coverage, scaled by the layer's device scale: the clip-path and the
// alpha mask both land here.
func (l *Layer) ApplyPathMask(p *Path, region Rect) {
	if l.cv.rec != nil {
		l.cv.record(op{kind: opPathMask, layer: l, path: p, r: region})
		return
	}
	region = region.Intersect(l.cv.Rect())
	if region.Empty() {
		return
	}
	scale := float64(l.cv.num) / float64(l.cv.denom)
	box := p.bounds(scale).Intersect(region)
	var mask *image.Alpha
	if !box.Empty() {
		mask = PathCoverage(p, box, scale)
	}
	for y := region.Y; y < region.Y+region.H; y++ {
		for x := region.X; x < region.X+region.W; x++ {
			var cov uint32
			if mask != nil && x >= box.X && x < box.X+box.W && y >= box.Y && y < box.Y+box.H {
				cov = uint32(mask.Pix[(y-box.Y)*mask.Stride+x-box.X])
			}
			if cov == 255 {
				continue
			}
			col := l.cv.get(x, y)
			if cov == 0 {
				l.cv.set(x, y, 0)
				continue
			}
			l.cv.set(x, y, coverageScale(col, cov))
		}
	}
}

// ApplyMaskLayer multiplies l's pixels inside region by the mask
// layer's pixels: the alpha channel for mask-mode alpha, the
// luminance (WCAG weights over the straight channels) for
// mask-mode luminance. The mask is positioned at its own coordinates.
func (l *Layer) ApplyMaskLayer(mask *Layer, region Rect, luminance bool) {
	if mask == nil {
		return
	}
	if l.cv.rec != nil {
		l.cv.record(op{kind: opMaskLayer, layer: l, other: mask, r: region, flag: luminance})
		return
	}
	region = region.Intersect(l.cv.Rect()).Intersect(mask.cv.Rect())
	if region.Empty() {
		return
	}
	for y := region.Y; y < region.Y+region.H; y++ {
		for x := region.X; x < region.X+region.W; x++ {
			m := mask.cv.get(x, y)
			var cov uint32
			if luminance {
				s := m.Straight()
				cov = (uint32(s[0])*77 + uint32(s[1])*151 + uint32(s[2])*28) / 256 * uint32(s[3]) / 255
			} else {
				cov = uint32(m.A())
			}
			if cov == 255 {
				continue
			}
			col := l.cv.get(x, y)
			if cov == 0 {
				l.cv.set(x, y, 0)
				continue
			}
			l.cv.set(x, y, coverageScale(col, cov))
		}
	}
}
