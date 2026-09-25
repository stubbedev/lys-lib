// Package render holds pure drawing and damage primitives. Nothing in here
// touches Wayland; everything is unit-testable.
package render

// Rect is an axis-aligned rectangle in buffer pixel coordinates. A Rect
// with a non-positive width or height is empty and covers no pixels.
type Rect struct {
	X, Y, W, H int
}

// Empty reports whether r covers no pixels.
func (r Rect) Empty() bool {
	return r.W <= 0 || r.H <= 0
}

// Contains reports whether the pixel (x, y) lies inside r. Empty rects
// contain nothing.
func (r Rect) Contains(x, y int) bool {
	if r.Empty() {
		return false
	}
	return x >= r.X && x < r.X+r.W && y >= r.Y && y < r.Y+r.H
}

// Intersect returns the overlap of r and o. Disjoint or empty inputs yield
// an empty Rect.
func (r Rect) Intersect(o Rect) Rect {
	x1 := max(r.X, o.X)
	y1 := max(r.Y, o.Y)
	x2 := min(r.X+r.W, o.X+o.W)
	y2 := min(r.Y+r.H, o.Y+o.H)
	if x2 <= x1 || y2 <= y1 {
		return Rect{}
	}
	return Rect{X: x1, Y: y1, W: x2 - x1, H: y2 - y1}
}

// Union returns the bounding box of r and o. Empty rectangles drop out, so
// the Union of two empty rects is empty.
func (r Rect) Union(o Rect) Rect {
	if r.Empty() {
		return o
	}
	if o.Empty() {
		return r
	}
	x1 := min(r.X, o.X)
	y1 := min(r.Y, o.Y)
	x2 := max(r.X+r.W, o.X+o.W)
	y2 := max(r.Y+r.H, o.Y+o.H)
	return Rect{X: x1, Y: y1, W: x2 - x1, H: y2 - y1}
}

// Subtract returns r with o cut out, as up to four non-overlapping rects
// that tile the remainder exactly. Order is unspecified. A non-intersecting
// or empty o yields [r]; an o that covers r yields nil.
func (r Rect) Subtract(o Rect) []Rect {
	clip := r.Intersect(o)
	if clip.Empty() {
		return []Rect{r}
	}
	var out []Rect
	if clip.Y > r.Y {
		out = append(out, Rect{X: r.X, Y: r.Y, W: r.W, H: clip.Y - r.Y})
	}
	if clip.X > r.X {
		out = append(out, Rect{X: r.X, Y: clip.Y, W: clip.X - r.X, H: clip.H})
	}
	if r.X+r.W > clip.X+clip.W {
		out = append(out, Rect{X: clip.X + clip.W, Y: clip.Y, W: r.X + r.W - clip.X - clip.W, H: clip.H})
	}
	if r.Y+r.H > clip.Y+clip.H {
		out = append(out, Rect{X: r.X, Y: clip.Y + clip.H, W: r.W, H: r.Y + r.H - clip.Y - clip.H})
	}
	return out
}

// UnionAll returns the bounding box of all rects. Nil and empty inputs
// yield an empty Rect.
func UnionAll(rects []Rect) Rect {
	var out Rect
	for _, r := range rects {
		out = out.Union(r)
	}
	return out
}
