package render

import "math"

// The effect-pipeline layer operations: sampling what is already on
// the canvas (a backdrop-filter's input), blending a layer back with
// a mix-blend-mode, and compositing one through a projective map.

// Snapshot copies c's device region into a layer shaped like c,
// reusing old's buffer when it fits, so a backdrop-filter held across
// frames allocates once. The snapshot is the input backdrop-filter
// filters: what has been painted under the region so far this frame.
func (c *Canvas) Snapshot(old *Layer, region Rect) *Layer {
	l := c.Layer(old, region)
	for y := region.Y; y < region.Y+region.H; y++ {
		for x := region.X; x < region.X+region.W; x++ {
			l.cv.set(x, y, c.get(x, y))
		}
	}
	return l
}

// DrawLayer draws l's device rect over c one to one through the blend
// mode: every pixel of src blends over the canvas's pixel at the same
// coordinates, clipped to c's clip and modulated by c's opacity.
// BlendNormal is the plain source-over copy.
func (c *Canvas) DrawLayer(l *Layer, src Rect, mode BlendMode) {
	if l == nil {
		return
	}
	src = src.Intersect(l.cv.Rect()).Intersect(c.clip)
	if src.Empty() {
		return
	}
	for y := src.Y; y < src.Y+src.H; y++ {
		for x := src.X; x < src.X+src.W; x++ {
			col := l.cv.get(x, y)
			if col == 0 {
				continue
			}
			switch c.alphaScale {
			case 0:
				return
			case 255:
			default:
				col = modulate(col, c.alphaScale)
			}
			if mode == BlendNormal {
				c.set(x, y, col.over(c.get(x, y)))
				continue
			}
			out := blendPixel(mode, col, c.get(x, y))
			if c.bright != 0 && c.bright != 1 {
				out = brighten(out, c.bright)
			}
			c.set(x, y, out)
		}
	}
}

// CompositeProjective draws the src device rect of l onto c through
// the projective map m (layer device pixels to canvas device pixels),
// inverse-mapping each destination pixel, sampling bilinearly, and
// blending like Composite. Pixels mapping behind the viewer (w <= 0)
// are skipped — the classic near-plane clip a one-plane raster owes.
func (c *Canvas) CompositeProjective(l *Layer, src Rect, m M3, alpha float64) {
	if l == nil || src.Empty() {
		return
	}
	src = src.Intersect(l.cv.Rect())
	prev := c.PushAlpha(alpha)
	defer c.PopAlpha(prev)
	if c.alphaScale == 0 {
		return
	}
	dst := m.MapBounds(src).Intersect(c.clip).Intersect(c.Rect())
	if dst.Empty() {
		return
	}
	inv, ok := m.Invert()
	if !ok {
		return
	}
	for y := dst.Y; y < dst.Y+dst.H; y++ {
		for x := dst.X; x < dst.X+dst.W; x++ {
			sx, sy := inv.Apply(float64(x)+0.5, float64(y)+0.5)
			if w := m[6]*sx + m[7]*sy + m[8]; w <= 1e-9 || math.IsNaN(w) {
				continue
			}
			if col := l.sample(src, sx-0.5, sy-0.5); col != 0 {
				c.blend(x, y, col)
			}
		}
	}
}
