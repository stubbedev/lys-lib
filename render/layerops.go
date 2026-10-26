package render

// The effect-pipeline layer operations: sampling what is already on
// the canvas (a backdrop-filter's input), blending a layer back with
// a mix-blend-mode, and compositing one through a projective map.

// Snapshot copies c's device region into a layer shaped like c,
// reusing old's buffer when it fits, so a backdrop-filter held across
// frames allocates once. The snapshot is the input backdrop-filter
// filters: what has been painted under the region so far this frame.
func (c *Canvas) Snapshot(old *Layer, region Rect) *Layer {
	l := c.Layer(old, region)
	l.copyFrom(c, region)
	return l
}

// copyFrom copies c's device region into the layer's, row by row.
func (l *Layer) copyFrom(c *Canvas, region Rect) {
	if c.rec != nil {
		c.record(op{kind: opSnapshot, layer: l, r: region})
		return
	}
	for y := region.Y; y < region.Y+region.H; y++ {
		o := y*c.stride + region.X*4
		copy(l.cv.data[o:o+region.W*4], c.data[o:o+region.W*4])
	}
	l.cv.touched += region.W * region.H
}

// DrawLayer draws l's device rect over c one to one through the blend
// mode: every pixel of src blends over the canvas's pixel at the same
// coordinates, clipped to c's clip and modulated by c's opacity.
// BlendNormal is the plain source-over copy.
func (c *Canvas) DrawLayer(l *Layer, src Rect, mode BlendMode) {
	if l == nil {
		return
	}
	if c.rec != nil {
		c.record(op{kind: opDrawLayer, layer: l, r: src, mode: mode})
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
			c.set(x, y, blendPixel(mode, col, c.get(x, y)))
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
	if c.rec != nil {
		c.record(op{kind: opProjective, layer: l, r: src, m3: m, f: [6]float64{alpha}})
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
	mp, ok := MappedOf(m)
	if !ok {
		return
	}
	c.blendMapped(dst, src, &mp, l.cv.get)
}
