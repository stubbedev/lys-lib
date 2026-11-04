package render

import (
	"fmt"
	"image"

	"github.com/stubbedev/lys-lib/intconv"
	"github.com/stubbedev/lys-lib/third_party/typesetting/font"
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
	// ops are the calls in order, each naming its state in states and its
	// arguments in glyphs, for a glyph, or else args.
	ops    []op
	states []opState
	args   []opArgs
	glyphs []glyphOp
	// stops holds the recorded gradients' stops, copied so a caller's
	// stop slice need not outlive the call.
	stops []GradientStop
	// free holds the released recorded scratch layers, reused by the
	// next ScratchLayer.
	free []*Layer
	// num/denom is the recording canvas's device scale.
	num, denom int
	// pending is the List's place among the open recordings Retire
	// waits for; ended marks a List End ended.
	pending *recording
	ended   bool
	// scratchLayers is whether a recording made a scratch layer, whose
	// calls cull trims to what is read of it, once: culled.
	scratchLayers, culled bool
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
	// Glyph's mask is its coverage for the pen, resolved at recording; nil
	// for a glyph drawn in color or from a bitmap.
	Glyph(s State, face *Typeface, gid font.GID, scale, penX, penY float64, col Color, mask *GlyphMask)
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

// op is one recorded call: its kind, the state it ran under and its
// arguments, both by index, and the device rect it can touch.
type op struct {
	kind   opKind
	skip   bool
	state  uint32
	args   uint32
	bounds Rect
}

// opState is the canvas state calls ran under; consecutive calls share one.
type opState struct {
	layer         *Layer
	clip          Rect
	alpha, bright float64
}

// glyphOp is a glyph's arguments, the call text records the most of.
type glyphOp struct {
	face              *Typeface
	mask              *GlyphMask
	scale, penX, penY float64
	gid               font.GID
	col               Color
}

// opArgs is a call's arguments; only the fields its kind passes on are set.
type opArgs struct {
	kind  opKind
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
	glyph *GlyphMask
}

// NewRecorder returns a canvas that records into a fresh List instead
// of painting: a width x height device canvas at scale num/denom, as
// NewScaled's. Its layers record into the same List.
func NewRecorder(width, height, num, denom int) *Canvas {
	return recordings.newRecorder(width, height, num, denom)
}

// newRecorder is NewRecorder with its List open in f.
func (f *fence) newRecorder(width, height, num, denom int) *Canvas {
	l := &List{}
	l.pending = f.newRecording(l)
	c := &Canvas{rec: l}
	c.resetRecording(width, height, num, denom)
	return c
}

// ResetRecorder readies a recording canvas for the next frame, as
// Reset does a raster one: its List ends, then empties and opens again
// (Retire), and the canvas takes the given size and scale.
func (c *Canvas) ResetRecorder(width, height, num, denom int) *Canvas {
	if c.rec == nil {
		panic("render: ResetRecorder of a raster canvas")
	}
	c.resetRecording(width, height, num, denom)
	c.rec.pending.restart()
	return c
}

// resetRecording empties c's List and gives c the size and scale.
func (c *Canvas) resetRecording(width, height, num, denom int) {
	c.rec.ended, c.rec.scratchLayers, c.rec.culled = false, false, false
	c.rec.ops, c.rec.stops = c.rec.ops[:0], c.rec.stops[:0]
	clear(c.rec.args)
	clear(c.rec.glyphs)
	clear(c.rec.states)
	c.rec.args, c.rec.glyphs, c.rec.states = c.rec.args[:0], c.rec.glyphs[:0], c.rec.states[:0]
	c.rec.num, c.rec.denom = num, denom
	c.resetFrame(width, height, num, denom)
}

// Scale is the recording's device scale: num device pixels per denom
// logical pixels.
func (l *List) Scale() (num, denom int) { return l.num, l.denom }

// ApplyState gives c the clip, opacity and brightness of s: a player
// rasterizing one recorded call onto a canvas of its own.
func (c *Canvas) ApplyState(s State) {
	c.clip, c.alpha, c.bright = s.Clip, s.Alpha, s.Bright
	c.rescaleAlpha()
}

// Recording is the List c records into, nil for a raster canvas.
func (c *Canvas) Recording() *List { return c.rec }

// Len is the number of recorded calls.
func (l *List) Len() int { return len(l.ops) }

// record appends o under c's state; c must be recording. A drawing
// call whose bounds miss the clip is dropped: it would paint nothing.
func (c *Canvas) record(o opArgs) {
	rec := c.rec
	if rec.ended {
		panic("render: recording into an ended List")
	}
	bounds := c.clip.Intersect(c.bounds(&o))
	if o.kind < opLayer && bounds.Empty() {
		return
	}
	compact := op{kind: o.kind, state: rec.stateOf(opState{layer: c.owner, clip: c.clip, alpha: c.alpha, bright: c.bright}), bounds: bounds}
	if o.kind == opGlyph {
		compact.args = index(len(rec.glyphs))
		rec.glyphs = append(rec.glyphs, glyphOp{face: o.face, mask: o.glyph, scale: o.f[0], penX: o.f[1], penY: o.f[2], gid: o.gid, col: o.col})
	} else {
		compact.args = index(len(rec.args))
		rec.args = append(rec.args, o)
	}
	rec.ops = append(rec.ops, compact)
}

// stateOf is st's index, the last state's when it is the same.
func (l *List) stateOf(st opState) uint32 {
	if n := len(l.states); n > 0 && l.states[n-1] == st {
		return index(n - 1)
	}
	l.states = append(l.states, st)
	return index(len(l.states) - 1)
}

func index(n int) uint32 { return intconv.Must[uint32](n) }

// stateAt is the State op o ran under.
func (l *List) stateAt(o *op) State {
	st := &l.states[o.state]
	return State{Layer: st.layer, Clip: st.clip, Bounds: o.bounds, Alpha: st.alpha, Bright: st.bright}
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
	c.rec.scratchLayers = c.rec.scratchLayers || scratch
	l.cv.rec = c.rec
	l.cv.resetFrame(c.w, c.h, c.num, c.denom)
	l.cv.clip = l.cv.Rect().Intersect(region)
	kind := opLayer
	if scratch {
		kind = opScratch
	}
	c.record(opArgs{kind: kind, layer: l, r: region})
	return l
}

// Backing is what the player that last readied l keeps for it: a
// raster layer for Paint, a GPU image for a GPU player.
func (l *Layer) Backing() any { return l.backing }

// SetBacking stores what stands for the recorded layer l.
func (l *Layer) SetBacking(b any) { l.backing = b }

// Play hands l's calls to p in recorded order.
func (l *List) Play(p Player) {
	if l.ended {
		panic("render: Play of an ended List")
	}
	l.cull()
	for i := range l.ops {
		o := &l.ops[i]
		if o.skip {
			continue
		}
		s := l.stateAt(o)
		if o.kind == opGlyph {
			g := &l.glyphs[o.args]
			p.Glyph(s, g.face, g.gid, g.scale, g.penX, g.penY, g.col, g.mask)
			continue
		}
		a := &l.args[o.args]
		switch a.kind {
		case opClear:
			p.ClearDevice(s, a.r, a.col)
		case opFill:
			p.FillRectDevice(s, a.r, a.col)
		case opCanvas:
			p.DrawCanvasDevice(s, a.cv, a.i[0], a.i[1])
		case opLayerAt:
			p.DrawLayerAt(s, a.layer, a.i[0], a.i[1])
		case opRoundedRect:
			p.RoundedRect(s, a.r, a.i[0], a.col)
		case opLine:
			p.Line(s, a.i[0], a.i[1], a.i[2], a.i[3], a.i[4], a.col)
		case opShadow:
			p.Shadow(s, a.r, a.i[0], a.i[1], a.col)
		case opImage:
			p.DrawImage(s, a.img, a.i[0], a.i[1])
		case opImageCover:
			p.DrawImageCover(s, a.img, a.r, a.radii)
		case opImageDevice:
			p.DrawImageDevice(s, a.img, a.i[0], a.i[1])
		case opImageRect:
			p.DrawImageRect(s, a.img, a.r)
		case opIcon:
			p.DrawIcon(s, a.icon, a.i[0], a.i[1], a.aff)
		case opArc:
			p.Arc(s, a.f[0], a.f[1], a.f[2], a.f[3], a.f[4], a.f[5], a.col)
		case opBoxShadow:
			p.BoxShadow(s, a.r, a.radii, a.sh)
		case opGrain:
			p.PaintGrain(s, a.r, a.radii, a.f[0], a.i[0], a.i[1])
		case opBorder:
			p.RoundedBorderSides(s, a.r, a.radii, a.ins, a.cols)
		case opRRect:
			p.RoundedRectCorners(s, a.r, a.radii, a.col)
		case opGradient:
			p.PaintGradient(s, a.r, a.radii, a.grad)
		case opGradientTiles:
			p.PaintGradientTiles(s, a.grad, a.tile, a.r, a.radii)
		case opImageTiles:
			p.PaintImageTiles(s, a.img, a.tile, a.r, a.radii)
		case opPath:
			p.FillPath(s, a.path, a.col)
		case opLayer:
			p.Layer(s, a.layer, a.r)
		case opScratch:
			p.ScratchLayer(s, a.layer, a.r)
		case opSnapshot:
			p.Snapshot(s, a.layer, a.r)
		case opRelease:
			p.Release(a.layer)
		case opComposite:
			p.Composite(s, a.layer, a.r, a.aff, a.f[0])
		case opProjective:
			p.CompositeProjective(s, a.layer, a.r, a.m3, a.f[0])
		case opDrawLayer:
			p.DrawLayer(s, a.layer, a.r, a.mode)
		case opCrossFade:
			p.CrossFade(a.layer, a.other, a.r, a.f[0])
		case opMaskLayer:
			p.ApplyMaskLayer(a.layer, a.other, a.r, a.flag)
		case opPathMask:
			p.ApplyPathMask(a.layer, a.path, a.r)
		case opFilters:
			p.ApplyFilters(a.layer, a.r, a.filt, a.shape)
		default:
			panic(fmt.Sprintf("render: play of unknown op %d", a.kind))
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
	c.ApplyState(s)
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

func (p rasterPlayer) Glyph(s State, face *Typeface, gid font.GID, scale, penX, penY float64, col Color, mask *GlyphMask) {
	cv := p.on(s)
	if mask == nil {
		cv.DrawGlyph(face, gid, scale, penX, penY, col)
		return
	}
	at := mask.bounds(penX, penY)
	blitMask(cv, cv.clip, mask, at.X, at.Y, col)
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
