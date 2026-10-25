package render

import "image"

// bounds is the device rect o's call can touch on c, before the clip:
// what a player draws it within and what culls it when it misses the
// clip. Layer lifecycle and in-place layer calls report their region.
func (c *Canvas) bounds(o *op) Rect {
	switch o.kind {
	case opClear, opFill:
		return o.r
	case opCanvas:
		return Rect{X: o.i[0], Y: o.i[1], W: o.cv.w, H: o.cv.h}
	case opLayerAt:
		return Rect{X: o.i[0], Y: o.i[1], W: o.layer.cv.w, H: o.layer.cv.h}
	case opRoundedRect, opImageCover, opImageRect, opGrain, opBorder, opRRect, opGradient, opGradientTiles, opImageTiles:
		return c.MapRect(o.r)
	case opLine:
		return c.lineSpan(o.i[0], o.i[1], o.i[2], o.i[3], o.i[4])
	case opShadow:
		in := shadowInset(o.i[1], c.num, c.denom)
		return Insets{in, in, in, in}.Grow(c.MapRect(o.r))
	case opImage:
		return c.MapRect(imageAt(o.img, o.i[0], o.i[1]))
	case opImageDevice:
		return imageAt(o.img, o.i[0], o.i[1])
	case opIcon:
		if o.aff == Identity {
			return c.MapRect(imageAt(o.icon.img, o.i[0], o.i[1]))
		}
		return o.icon.xformSpan(o.i[0], o.i[1], o.aff, c.num, c.denom)
	case opArc:
		return c.arcSpan(o.f[0], o.f[1], o.f[2], o.f[3])
	case opBoxShadow:
		_, _, _, _, full := c.boxShadowRaster(o.r, o.radii, o.sh)
		return full
	case opPath:
		return o.path.bounds(float64(c.num) / float64(c.denom))
	case opGlyph:
		return glyphBounds(o.face, o.gid, o.f[0], o.f[1], o.f[2], o.col)
	case opComposite:
		return o.aff.MapBounds(o.r)
	case opProjective:
		return o.m3.MapBounds(o.r)
	}
	return o.r
}

// imageAt is img's rect with its top-left at (x, y).
func imageAt(img image.Image, x, y int) Rect {
	b := img.Bounds()
	return Rect{X: x, Y: y, W: b.Dx(), H: b.Dy()}
}
