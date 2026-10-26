package render

import (
	"image"
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

// TileGeometry is a tiling laid out in device pixels, in the float32 a
// GPU shader evaluates it in: the clip's box and corner radii, and per
// axis the positioned tile's start and size, the repeat period (tile
// and gap) with its reciprocal, and whether the axis repeats.
type TileGeometry struct {
	Box, Radii                               [4]float32
	X0, Y0, W, H                             float32
	PeriodX, PeriodY, InvPeriodX, InvPeriodY float32
	RepeatX, RepeatY                         bool
}

// TileGeometryOf lays t out over clip's rounded outline (radii in
// logical pixels) at device scale num/denom; false for an empty tile.
func TileGeometryOf(t Tile, clip Rect, radii Corners, num, denom int) (TileGeometry, bool) {
	if t.W <= 0 || t.H <= 0 {
		return TileGeometry{}, false
	}
	c := Canvas{num: num, denom: denom}
	b, rc := c.devBox(clip, radii)
	s := devScale(num, denom)
	w, h := float32(t.W)*s, float32(t.H)*s
	px, py := w+float32(float32(t.GapX)*s), h+float32(float32(t.GapY)*s)
	return TileGeometry{
		Box: b.array(), Radii: rc,
		X0: float32(t.X) * s, Y0: float32(t.Y) * s, W: w, H: h,
		PeriodX: px, PeriodY: py, InvPeriodX: Reciprocal(px), InvPeriodY: Reciprocal(py),
		RepeatX: t.RepeatX, RepeatY: t.RepeatY,
	}, true
}

// At is the offset (u, v) of device pixel center (px, py) within the
// tile it falls in, and whether one covers it: false in a gap between
// repeated tiles, or outside the lone tile of a non-repeating axis.
func (g TileGeometry) At(px, py float32) (u, v float32, ok bool) {
	u, inX := tileAxis(px-g.X0, g.W, g.PeriodX, g.InvPeriodX, g.RepeatX)
	v, inY := tileAxis(py-g.Y0, g.H, g.PeriodY, g.InvPeriodY, g.RepeatY)
	return u, v, inX && inY
}

// tileAxis maps a distance d from the positioned tile's start onto the
// offset within the tile it falls in, folding repeats with modFloor.
func tileAxis(d, size, period, inv float32, repeat bool) (float32, bool) {
	if repeat {
		d = modFloor(d, period, inv)
	}
	return d, d >= 0 && d < size
}

// TexelScale is the factor that maps a tile offset onto one of n
// texels across a tile size device pixels long: n over size, rounded
// once to float32, so both renderers truncate the same product.
func TexelScale(n int, size float32) float32 { return float32(n) * Reciprocal(size) }

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
	g, ok := TileGeometryOf(t, clip, radii, c.num, c.denom)
	if !ok {
		return
	}
	iw, ih := ib.Dx(), ib.Dy()
	kx, ky := TexelScale(iw, g.W), TexelScale(ih, g.H)
	c.paintTiles(g, func(u, v float32) Color {
		sx := ib.Min.X + min(max(int(u*kx), 0), iw-1)
		sy := ib.Min.Y + min(max(int(v*ky), 0), ih-1)
		r, gr, b, a := img.At(sx, sy).RGBA()
		return Color(a>>8<<24 | r>>8<<16 | gr>>8<<8 | b>>8)
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
	m, ok := g.TileMap(t, c.num, c.denom)
	if !ok {
		return
	}
	tg, ok := TileGeometryOf(t, clip, radii, c.num, c.denom)
	if !ok {
		return
	}
	c.paintTiles(tg, func(u, v float32) Color {
		return sampleStops(g.Stops, g.Extend, m.At(u, v))
	})
}

// TileMap is g's mapping over one tile of t at device scale num/denom:
// a box at the origin, sampled at a pixel's offset into its tile;
// false when it is degenerate.
func (g Gradient) TileMap(t Tile, num, denom int) (GradientMap, bool) {
	s := devScale(num, denom)
	return g.mapping(frect{0, 0, float32(t.W) * s, float32(t.H) * s}, s)
}

// paintTiles runs sample over the device pixels of g's rounded clip
// that a tile covers, with the pixel center's offset into its tile.
func (c *Canvas) paintTiles(g TileGeometry, sample func(u, v float32) Color) {
	b := g.Box
	sp := c.span(frect{b[0], b[1], b[2], b[3]})
	for y := sp.Y; y < sp.Y+sp.H; y++ {
		py := float32(y) + 0.5
		for x := sp.X; x < sp.X+sp.W; x++ {
			px := float32(x) + 0.5
			u, v, ok := g.At(px, py)
			if !ok {
				continue
			}
			if cov := coverage(sdBoxF(px, py, g.Box, g.Radii)); cov > 0 {
				blendCovF(c, x, y, sample(u, v), cov)
			}
		}
	}
}
