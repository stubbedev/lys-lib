package render

import (
	"image"
	"image/draw"
	"math"

	"golang.org/x/image/vector"
)

// Path is an outline in logical pixels for FillPath: straight and
// cubic Bézier segments in subpaths, each closed when filled (cairo's
// move_to / line_to / curve_to / close_path).
type Path struct {
	ops []pathOp
}

type pathKind uint8

const (
	pathMove pathKind = iota
	pathLine
	pathCube
	pathClose
)

// pathOp is one segment: pts holds the end point, or a cubic's two
// control points then its end point.
type pathOp struct {
	kind pathKind
	pts  [3][2]float64
}

// MoveTo starts a subpath at (x, y).
func (p *Path) MoveTo(x, y float64) {
	p.ops = append(p.ops, pathOp{kind: pathMove, pts: [3][2]float64{{x, y}}})
}

// LineTo adds a straight segment to (x, y).
func (p *Path) LineTo(x, y float64) {
	p.ops = append(p.ops, pathOp{kind: pathLine, pts: [3][2]float64{{x, y}}})
}

// CubeTo adds a cubic Bézier to (x, y) through the control points
// (x1, y1) and (x2, y2).
func (p *Path) CubeTo(x1, y1, x2, y2, x, y float64) {
	p.ops = append(p.ops, pathOp{kind: pathCube, pts: [3][2]float64{{x1, y1}, {x2, y2}, {x, y}}})
}

// Close closes the current subpath back to its start.
func (p *Path) Close() { p.ops = append(p.ops, pathOp{kind: pathClose}) }

// Empty reports a path with no segments.
func (p *Path) Empty() bool { return len(p.ops) == 0 }

// bounds is the device-pixel box holding every point (a cubic lies
// inside its control points' hull), nil-sized for an empty path.
func (p *Path) bounds(scale float64) Rect {
	minX, minY := math.Inf(1), math.Inf(1)
	maxX, maxY := math.Inf(-1), math.Inf(-1)
	for _, op := range p.ops {
		n := 1
		switch op.kind {
		case pathClose:
			continue
		case pathCube:
			n = 3
		}
		for _, pt := range op.pts[:n] {
			minX, maxX = math.Min(minX, pt[0]*scale), math.Max(maxX, pt[0]*scale)
			minY, maxY = math.Min(minY, pt[1]*scale), math.Max(maxY, pt[1]*scale)
		}
	}
	if minX > maxX {
		return Rect{}
	}
	x, y := int(math.Floor(minX)), int(math.Floor(minY))
	return Rect{X: x, Y: y, W: int(math.Ceil(maxX)) - x, H: int(math.Ceil(maxY)) - y}
}

// FillPath fills the path's subpaths with col, anti-aliased, under the
// clip and the pushed opacity. Overlapping subpaths fill once.
func (c *Canvas) FillPath(p *Path, col Color) {
	if p == nil || p.Empty() || col == 0 || c.clip.Empty() {
		return
	}
	scale := float64(c.num) / float64(c.denom)
	box := c.clip.Intersect(p.bounds(scale))
	if box.Empty() {
		return
	}
	z := vector.NewRasterizer(box.W, box.H)
	pt := func(x, y float64) (float32, float32) {
		return float32(x*scale - float64(box.X)), float32(y*scale - float64(box.Y))
	}
	open := false
	for _, op := range p.ops {
		switch op.kind {
		case pathMove:
			if open {
				z.ClosePath()
			}
			z.MoveTo(pt(op.pts[0][0], op.pts[0][1]))
			open = true
		case pathLine:
			z.LineTo(pt(op.pts[0][0], op.pts[0][1]))
		case pathCube:
			bx, by := pt(op.pts[0][0], op.pts[0][1])
			cx, cy := pt(op.pts[1][0], op.pts[1][1])
			dx, dy := pt(op.pts[2][0], op.pts[2][1])
			z.CubeTo(bx, by, cx, cy, dx, dy)
		case pathClose:
			if open {
				z.ClosePath()
				open = false
			}
		}
	}
	if open {
		z.ClosePath()
	}
	mask := image.NewAlpha(image.Rect(0, 0, box.W, box.H))
	z.DrawOp = draw.Src
	z.Draw(mask, mask.Bounds(), image.Opaque, image.Point{})
	for y := range box.H {
		row := mask.Pix[y*mask.Stride : y*mask.Stride+box.W]
		for x, a := range row {
			if a != 0 {
				c.blendCov(box.X+x, box.Y+y, col, float64(a)/255)
			}
		}
	}
}
