package render

import (
	"image"
	"math"
)

// Tile is one image layer's tiling (the CSS background and mask
// painting model): the rect of the tile at the layer's position in
// logical pixels, the gap between neighboring tiles along each axis,
// and whether the tiles repeat there. A non-repeating axis paints the
// one tile.
type Tile struct {
	X, Y, W, H       float64
	GapX, GapY       float64
	RepeatX, RepeatY bool
}

// PaintImageTiles paints img over every tile t lays out, each tile the
// image scaled to the tile's size and nearest sampled, all clipped to
// clip's rounded outline (radii in logical pixels).
func (c *Canvas) PaintImageTiles(img image.Image, t Tile, clip Rect, radii Corners) {
	if c.rec != nil {
		c.record(op{kind: opImageTiles, img: img, tile: t, r: clip, radii: radii})
		return
	}
	ib := img.Bounds()
	if ib.Empty() {
		return
	}
	iw, ih := float64(ib.Dx()), float64(ib.Dy())
	c.paintTiles(t, clip, radii, func(u, v, tw, th float64) Color {
		sx := min(max(ib.Min.X+int(u/tw*iw), ib.Min.X), ib.Max.X-1)
		sy := min(max(ib.Min.Y+int(v/th*ih), ib.Min.Y), ib.Max.Y-1)
		r, g, b, a := img.At(sx, sy).RGBA()
		return Color(a>>8<<24 | r>>8<<16 | g>>8<<8 | b>>8)
	})
}

// PaintGradientTiles paints g over every tile t lays out, each tile
// its own gradient box, clipped to clip's rounded outline.
func (c *Canvas) PaintGradientTiles(g Gradient, t Tile, clip Rect, radii Corners) {
	if c.rec != nil {
		c.record(op{kind: opGradientTiles, grad: c.keepGradient(&g), tile: t, r: clip, radii: radii})
		return
	}
	if len(g.Stops) == 0 {
		return
	}
	s := float64(c.num) / float64(c.denom)
	m, ok := g.mapping(frect{0, 0, t.W * s, t.H * s}, s)
	if !ok {
		return
	}
	c.paintTiles(t, clip, radii, func(u, v, _, _ float64) Color {
		return sampleStops(g.Stops, g.Extend, m.at(u, v))
	})
}

// paintTiles runs sample over the device pixels of clip's rounded
// outline that a tile covers, with the pixel center's offset into its
// tile and the tile's device size.
func (c *Canvas) paintTiles(t Tile, clip Rect, radii Corners, sample func(u, v, tw, th float64) Color) {
	if t.W <= 0 || t.H <= 0 {
		return
	}
	b, rc := c.devBox(clip, radii)
	s := float64(c.num) / float64(c.denom)
	x0, y0, tw, th := t.X*s, t.Y*s, t.W*s, t.H*s
	sp := c.span(b)
	for y := sp.Y; y < sp.Y+sp.H; y++ {
		py := float64(y) + 0.5
		v, inY := tileOffset(py-y0, th, t.GapY*s, t.RepeatY)
		if !inY {
			continue
		}
		for x := sp.X; x < sp.X+sp.W; x++ {
			px := float64(x) + 0.5
			u, inX := tileOffset(px-x0, tw, t.GapX*s, t.RepeatX)
			if !inX {
				continue
			}
			if cov := coverage(sdBox(px, py, b, rc)); cov > 0 {
				c.blendCov(x, y, sample(u, v, tw, th), cov)
			}
		}
	}
}

// tileOffset maps a distance d from the positioned tile's start onto
// the offset within the tile it falls in: false in a gap between
// repeated tiles, or outside the lone tile of a non-repeating axis.
func tileOffset(d, size, gap float64, repeat bool) (float64, bool) {
	if !repeat {
		return d, d >= 0 && d < size
	}
	period := size + gap
	m := math.Mod(d, period)
	if m < 0 {
		m += period
	}
	return m, m < size
}
