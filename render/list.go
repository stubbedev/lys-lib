package render

import (
	"fmt"
	"image"

	"github.com/go-text/typesetting/font"
)

// List is a frame recorded instead of rasterized: every drawing call a
// recording canvas (NewRecorder) and the layers made from it received,
// in order, with the clip, opacity and brightness in force. Play hands
// the calls to a Player: Paint rasterizes them onto a canvas exactly as
// painting directly would have, and a GPU renderer draws them on the
// GPU.
//
// A List refers to what its calls were given (images, paths, filter
// lists, retained canvases) rather than copying it; it is played before
// they change, within the frame that recorded it.
type List struct {
	ops []op
	// stops holds the recorded gradients' stops, copied so a caller's
	// stop slice need not outlive the call.
	stops []GradientStop
	// free holds the released recorded scratch layers, reused by the
	// next ScratchLayer.
	free []*Layer
}

// State is the canvas state a recorded call ran under. Layer is the
// recorded layer whose canvas the call drew on, nil for the frame's
// canvas. Bounds is the device rect the call can touch, within Clip;
// for a layer's lifecycle or an in-place layer call, its region.
type State struct {
	Layer         *Layer
	Clip          Rect
	Bounds        Rect
	Alpha, Bright float64
}

// Player receives a List's calls, each a Canvas (or Layer) method's
// arguments after the state it ran under. Layers are the recorded
// ones; a player keeps what stands for each (a raster buffer, a GPU
// image) with SetBacking, so a layer kept across frames keeps it.
type Player interface {
	ClearDevice(s State, r Rect, col Color)
	FillRectDevice(s State, r Rect, col Color)
	DrawCanvasDevice(s State, src *Canvas, x, y int)
	DrawLayerAt(s State, src *Layer, x, y int)
	RoundedRect(s State, r Rect, radius int, col Color)
	Line(s State, x0, y0, x1, y1, width int, col Color)
	Shadow(s State, r Rect, cornerRadius, blur int, col Color)
	DrawImage(s State, img image.Image, x, y int)
	DrawImageCover(s State, img image.Image, r Rect, radii Corners)
	DrawImageDevice(s State, img image.Image, x, y int)
	DrawImageRect(s State, img image.Image, r Rect)
	DrawIcon(s State, ic *Icon, x, y int, m Affine)
	Arc(s State, cx, cy, radius, width, start, sweep float64, col Color)
	BoxShadow(s State, r Rect, radii Corners, sh BoxShadow)
	PaintGrain(s State, r Rect, radii Corners, opacity float64, ox, oy int)
	RoundedBorderSides(s State, r Rect, radii Corners, widths Insets, cols [4]Color)
	RoundedRectCorners(s State, r Rect, radii Corners, col Color)
	PaintGradient(s State, r Rect, radii Corners, g Gradient)
	PaintGradientTiles(s State, g Gradient, t Tile, clip Rect, radii Corners)
	PaintImageTiles(s State, img image.Image, t Tile, clip Rect, radii Corners)
	FillPath(s State, p *Path, col Color)
	Glyph(s State, face *Typeface, gid font.GID, scale, penX, penY float64, col Color)
	// Layer readies l, the size and scale of the canvas s names, with
	// region transparent; ScratchLayer is Layer for a layer Release
	// ends.
	Layer(s State, l *Layer, region Rect)
	ScratchLayer(s State, l *Layer, region Rect)
	// Snapshot copies the region of the canvas s names into l.
	Snapshot(s State, l *Layer, region Rect)
	Release(l *Layer)
	Composite(s State, l *Layer, src Rect, m Affine, alpha float64)
	CompositeProjective(s State, l *Layer, src Rect, m M3, alpha float64)
	DrawLayer(s State, l *Layer, src Rect, mode BlendMode)
	CrossFade(l, other *Layer, region Rect, t float64)
	ApplyMaskLayer(l, mask *Layer, region Rect, luminance bool)
	ApplyPathMask(l *Layer, p *Path, region Rect)
	ApplyFilters(l *Layer, region Rect, f *Filters, shape FilterShape)
}

// opKind names a recorded call.
type opKind uint8

const (
	opClear opKind = iota
	opFill
	opCanvas
	opLayerAt
	opRoundedRect
	opLine
	opShadow
	opImage
	opImageCover
	opImageDevice
	opImageRect
	opIcon
	opArc
	opBoxShadow
	opGrain
	opBorder
	opRRect
	opGradient
	opGradientTiles
	opImageTiles
	opPath
	opGlyph
	// The kinds from opLayer on manage or rework layers rather than
	// draw, and are recorded whatever their bounds.
	opLayer
	opScratch
	opSnapshot
	opRelease
	opComposite
	opProjective
	opDrawLayer
	opCrossFade
	opMaskLayer
	opPathMask
	opFilters
	// numOpKinds counts the kinds above.
	numOpKinds
)

// op is one recorded call and the state it ran under; only the fields
// its kind passes on are set.
type op struct {
	kind  opKind
	state State
	r     Rect
	col   Color
	cols  [4]Color
	radii Corners
	ins   Insets
	i     [5]int
	f     [6]float64
	img   image.Image
	cv    *Canvas
	icon  *Icon
	path  *Path
	grad  Gradient
	tile  Tile
	sh    BoxShadow
	aff   Affine
	m3    M3
	mode  BlendMode
	filt  *Filters
	shape FilterShape
	layer *Layer
	other *Layer
	flag  bool
	face  *Typeface
	gid   font.GID
}

// NewRecorder returns a canvas that records into a fresh List instead
// of painting: a width x height device canvas at scale num/denom, as
// NewScaled's. Its layers record into the same List.
func NewRecorder(width, height, num, denom int) *Canvas {
	c := &Canvas{rec: &List{}}
	return c.ResetRecorder(width, height, num, denom)
}

// ResetRecorder readies a recording canvas for the next frame, as
// Reset does a raster one: its List empties and the canvas takes the
// given size and scale.
func (c *Canvas) ResetRecorder(width, height, num, denom int) *Canvas {
	if c.rec == nil {
		panic("render: ResetRecorder of a raster canvas")
	}
	c.rec.ops, c.rec.stops = c.rec.ops[:0], c.rec.stops[:0]
	c.resetFrame(width, height, num, denom)
	return c
}

// Recording is the List c records into, nil for a raster canvas.
func (c *Canvas) Recording() *List { return c.rec }

// Len is the number of recorded calls.
func (l *List) Len() int { return len(l.ops) }

// record appends o under c's state; c must be recording. A drawing
// call whose bounds miss the clip is dropped: it would paint nothing.
func (c *Canvas) record(o op) {
	o.state = State{Layer: c.owner, Clip: c.clip, Alpha: c.alpha, Bright: c.bright}
	o.state.Bounds = c.clip.Intersect(c.bounds(&o))
	if o.kind < opLayer && o.state.Bounds.Empty() {
		return
	}
	c.rec.ops = append(c.rec.ops, o)
}

// keepGradient is *g with its stops copied into c's List, so the
// caller's stop slice need not outlive the call (nor, through escape
// analysis, leave the stack on the raster path).
func (c *Canvas) keepGradient(g *Gradient) Gradient {
	n := len(c.rec.stops)
	c.rec.stops = append(c.rec.stops, g.Stops...)
	return Gradient{
		Kind: g.Kind, Stops: c.rec.stops[n:len(c.rec.stops):len(c.rec.stops)], Extend: g.Extend,
		Angle: g.Angle, CenterX: g.CenterX, CenterY: g.CenterY,
		Circle: g.Circle, Size: g.Size, RadiusX: g.RadiusX, RadiusY: g.RadiusY,
	}
}

// recordLayer is Layer or, scratch, ScratchLayer on a recording canvas:
// old when it is a recorded layer, else a new one or a released scratch
// one, sized like c and drawing into c's List.
func (c *Canvas) recordLayer(old *Layer, region Rect, scratch bool) *Layer {
	l := old
	switch {
	case scratch && len(c.rec.free) > 0:
		l = c.rec.free[len(c.rec.free)-1]
		c.rec.free = c.rec.free[:len(c.rec.free)-1]
	case scratch || l == nil || l.cv.rec == nil:
		l = &Layer{cv: &Canvas{}}
		l.cv.owner = l
	}
	l.scratch = scratch
	l.cv.rec = c.rec
	l.cv.resetFrame(c.w, c.h, c.num, c.denom)
	l.cv.clip = l.cv.Rect().Intersect(region)
	kind := opLayer
	if scratch {
		kind = opScratch
	}
	c.record(op{kind: kind, layer: l, r: region})
	return l
}

// Backing is what the player that last readied l keeps for it: a
// raster layer for Paint, a GPU image for a GPU player.
func (l *Layer) Backing() any { return l.backing }

// SetBacking stores what stands for the recorded layer l.
func (l *Layer) SetBacking(b any) { l.backing = b }

// Play hands l's calls to p in recorded order.
func (l *List) Play(p Player) {
	for i := range l.ops {
		o := &l.ops[i]
		s := o.state
		switch o.kind {
		case opClear:
			p.ClearDevice(s, o.r, o.col)
		case opFill:
			p.FillRectDevice(s, o.r, o.col)
		case opCanvas:
			p.DrawCanvasDevice(s, o.cv, o.i[0], o.i[1])
		case opLayerAt:
			p.DrawLayerAt(s, o.layer, o.i[0], o.i[1])
		case opRoundedRect:
			p.RoundedRect(s, o.r, o.i[0], o.col)
		case opLine:
			p.Line(s, o.i[0], o.i[1], o.i[2], o.i[3], o.i[4], o.col)
		case opShadow:
			p.Shadow(s, o.r, o.i[0], o.i[1], o.col)
		case opImage:
			p.DrawImage(s, o.img, o.i[0], o.i[1])
		case opImageCover:
			p.DrawImageCover(s, o.img, o.r, o.radii)
		case opImageDevice:
			p.DrawImageDevice(s, o.img, o.i[0], o.i[1])
		case opImageRect:
			p.DrawImageRect(s, o.img, o.r)
		case opIcon:
			p.DrawIcon(s, o.icon, o.i[0], o.i[1], o.aff)
		case opArc:
			p.Arc(s, o.f[0], o.f[1], o.f[2], o.f[3], o.f[4], o.f[5], o.col)
		case opBoxShadow:
			p.BoxShadow(s, o.r, o.radii, o.sh)
		case opGrain:
			p.PaintGrain(s, o.r, o.radii, o.f[0], o.i[0], o.i[1])
		case opBorder:
			p.RoundedBorderSides(s, o.r, o.radii, o.ins, o.cols)
		case opRRect:
			p.RoundedRectCorners(s, o.r, o.radii, o.col)
		case opGradient:
			p.PaintGradient(s, o.r, o.radii, o.grad)
		case opGradientTiles:
			p.PaintGradientTiles(s, o.grad, o.tile, o.r, o.radii)
		case opImageTiles:
			p.PaintImageTiles(s, o.img, o.tile, o.r, o.radii)
		case opPath:
			p.FillPath(s, o.path, o.col)
		case opGlyph:
			p.Glyph(s, o.face, o.gid, o.f[0], o.f[1], o.f[2], o.col)
		case opLayer:
			p.Layer(s, o.layer, o.r)
		case opScratch:
			p.ScratchLayer(s, o.layer, o.r)
		case opSnapshot:
			p.Snapshot(s, o.layer, o.r)
		case opRelease:
			p.Release(o.layer)
		case opComposite:
			p.Composite(s, o.layer, o.r, o.aff, o.f[0])
		case opProjective:
			p.CompositeProjective(s, o.layer, o.r, o.m3, o.f[0])
		case opDrawLayer:
			p.DrawLayer(s, o.layer, o.r, o.mode)
		case opCrossFade:
			p.CrossFade(o.layer, o.other, o.r, o.f[0])
		case opMaskLayer:
			p.ApplyMaskLayer(o.layer, o.other, o.r, o.flag)
		case opPathMask:
			p.ApplyPathMask(o.layer, o.path, o.r)
		case opFilters:
			p.ApplyFilters(o.layer, o.r, o.filt, o.shape)
		default:
			panic(fmt.Sprintf("render: play of unknown op %d", o.kind))
		}
	}
}

// Paint rasterizes l onto dst, a raster canvas of the recording's size
// and scale: the pixels painting directly onto dst would have made.
func (l *List) Paint(dst *Canvas) {
	if dst.rec != nil {
		panic("render: Paint onto a recording canvas")
	}
	l.Play(rasterPlayer{dst: dst})
}

// rasterPlayer paints a List onto a raster canvas, each recorded layer
// backed by a raster one.
type rasterPlayer struct{ dst *Canvas }

// on is the raster canvas s names, under s's clip and opacity.
func (p rasterPlayer) on(s State) *Canvas {
	c := p.dst
	if s.Layer != nil {
		c = rasterOf(s.Layer).cv
	}
	c.clip, c.alpha, c.bright = s.Clip, s.Alpha, s.Bright
	c.rescaleAlpha()
	return c
}

// rasterOf is the raster layer backing the recorded l.
func rasterOf(l *Layer) *Layer {
	r, ok := l.backing.(*Layer)
	if !ok {
		panic(fmt.Sprintf("render: a recorded layer backed by %T, not a raster layer", l.backing))
	}
	return r
}

func (p rasterPlayer) ClearDevice(s State, r Rect, col Color) { p.on(s).ClearDevice(r, col) }

func (p rasterPlayer) FillRectDevice(s State, r Rect, col Color) { p.on(s).FillRectDevice(r, col) }

func (p rasterPlayer) DrawCanvasDevice(s State, src *Canvas, x, y int) {
	p.on(s).DrawCanvasDevice(src, x, y)
}

func (p rasterPlayer) DrawLayerAt(s State, src *Layer, x, y int) {
	p.on(s).DrawCanvasDevice(rasterOf(src).cv, x, y)
}

func (p rasterPlayer) RoundedRect(s State, r Rect, radius int, col Color) {
	p.on(s).RoundedRect(r, radius, col)
}

func (p rasterPlayer) Line(s State, x0, y0, x1, y1, width int, col Color) {
	p.on(s).Line(x0, y0, x1, y1, width, col)
}

func (p rasterPlayer) Shadow(s State, r Rect, cornerRadius, blur int, col Color) {
	p.on(s).Shadow(r, cornerRadius, blur, col)
}

func (p rasterPlayer) DrawImage(s State, img image.Image, x, y int) { p.on(s).DrawImage(img, x, y) }

func (p rasterPlayer) DrawImageCover(s State, img image.Image, r Rect, radii Corners) {
	p.on(s).DrawImageCover(img, r, radii)
}

func (p rasterPlayer) DrawImageDevice(s State, img image.Image, x, y int) {
	p.on(s).DrawImageDevice(img, x, y)
}

func (p rasterPlayer) DrawImageRect(s State, img image.Image, r Rect) {
	p.on(s).DrawImageRect(img, r)
}

func (p rasterPlayer) DrawIcon(s State, ic *Icon, x, y int, m Affine) {
	ic.DrawXformed(p.on(s), x, y, m)
}

func (p rasterPlayer) Arc(s State, cx, cy, radius, width, start, sweep float64, col Color) {
	p.on(s).Arc(cx, cy, radius, width, start, sweep, col)
}

func (p rasterPlayer) BoxShadow(s State, r Rect, radii Corners, sh BoxShadow) {
	p.on(s).BoxShadow(r, radii, sh)
}

func (p rasterPlayer) PaintGrain(s State, r Rect, radii Corners, opacity float64, ox, oy int) {
	p.on(s).PaintGrain(r, radii, opacity, ox, oy)
}

func (p rasterPlayer) RoundedBorderSides(s State, r Rect, radii Corners, widths Insets, cols [4]Color) {
	p.on(s).RoundedBorderSides(r, radii, widths, cols)
}

func (p rasterPlayer) RoundedRectCorners(s State, r Rect, radii Corners, col Color) {
	p.on(s).RoundedRectCorners(r, radii, col)
}

func (p rasterPlayer) PaintGradient(s State, r Rect, radii Corners, g Gradient) {
	p.on(s).PaintGradient(r, radii, g)
}

func (p rasterPlayer) PaintGradientTiles(s State, g Gradient, t Tile, clip Rect, radii Corners) {
	p.on(s).PaintGradientTiles(g, t, clip, radii)
}

func (p rasterPlayer) PaintImageTiles(s State, img image.Image, t Tile, clip Rect, radii Corners) {
	p.on(s).PaintImageTiles(img, t, clip, radii)
}

func (p rasterPlayer) FillPath(s State, path *Path, col Color) { p.on(s).FillPath(path, col) }

func (p rasterPlayer) Glyph(s State, face *Typeface, gid font.GID, scale, penX, penY float64, col Color) {
	c := p.on(s)
	drawGlyph(c, c.clip, face, gid, scale, penX, penY, col)
}

func (p rasterPlayer) Layer(s State, l *Layer, region Rect) {
	old, _ := l.backing.(*Layer)
	l.backing = p.on(s).Layer(old, region)
}

func (p rasterPlayer) ScratchLayer(s State, l *Layer, region Rect) {
	l.backing = p.on(s).ScratchLayer(region)
}

func (p rasterPlayer) Snapshot(s State, l *Layer, region Rect) {
	rasterOf(l).copyFrom(p.on(s), region)
}

func (p rasterPlayer) Release(l *Layer) {
	rasterOf(l).Release()
	l.backing = nil
}

func (p rasterPlayer) Composite(s State, l *Layer, src Rect, m Affine, alpha float64) {
	p.on(s).Composite(rasterOf(l), src, m, alpha)
}

func (p rasterPlayer) CompositeProjective(s State, l *Layer, src Rect, m M3, alpha float64) {
	p.on(s).CompositeProjective(rasterOf(l), src, m, alpha)
}

func (p rasterPlayer) DrawLayer(s State, l *Layer, src Rect, mode BlendMode) {
	p.on(s).DrawLayer(rasterOf(l), src, mode)
}

func (p rasterPlayer) CrossFade(l, other *Layer, region Rect, t float64) {
	rasterOf(l).CrossFade(rasterOf(other), region, t)
}

func (p rasterPlayer) ApplyMaskLayer(l, mask *Layer, region Rect, luminance bool) {
	rasterOf(l).ApplyMaskLayer(rasterOf(mask), region, luminance)
}

func (p rasterPlayer) ApplyPathMask(l *Layer, path *Path, region Rect) {
	rasterOf(l).ApplyPathMask(path, region)
}

func (p rasterPlayer) ApplyFilters(l *Layer, region Rect, f *Filters, shape FilterShape) {
	ApplyFilters(rasterOf(l), region, f, shape)
}
