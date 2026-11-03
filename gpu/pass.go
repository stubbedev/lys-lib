package gpu

import (
	"errors"
	"fmt"
	"image"
	"image/color"
	"math"
	"slices"
	"unsafe"

	"github.com/stubbedev/lys-lib/third_party/vk"
)

// FRect is a rectangle in fractional pixels: the region of a texture
// a draw samples, as wp_viewporter crops a buffer.
type FRect struct {
	X, Y, W, H float64
}

// Pass draws a compositor's frame into a Target, every call clipped to
// the damage it began with. The first bad call fails the pass: End
// reports it and draws nothing. The device reuses it for its next Begin,
// so a frame costs no allocation: nothing holds one past that Begin.
type Pass struct {
	d      *Device
	t      *Target
	ct     *compTarget
	damage []image.Rectangle
	ops    []passOp
	err    error
	ended  bool
	// color is what SetColor last set; colorSet holds it for the shaders.
	color    *ColorTransform
	colorSet vk.DescriptorSet
}

// passWork counts the commands the last recorded pass issued for its
// draws.
type passWork struct {
	binds, pushes, scissors, draws int
}

type opKind uint8

const (
	opClear opKind = iota
	opFill
	opTexture
)

type passOp struct {
	kind   opKind
	dst    image.Rectangle
	tex    *Texture
	set    vk.DescriptorSet
	pipe   vk.Pipeline
	layout vk.PipelineLayout
	color  vk.DescriptorSet
	dc     drawConstants
}

// Begin starts a pass drawing into t, where only damage, clipped to t,
// changes. One pass at a time is open on a device.
func (d *Device) Begin(t *Target, damage []image.Rectangle) (*Pass, error) {
	switch {
	case t == nil || t.closed:
		return nil, errors.New("gpu: Begin on a closed target")
	case t.s.d != d:
		return nil, errors.New("gpu: Begin on another device's target")
	case d.active != nil:
		return nil, errors.New("gpu: Begin while a pass is open")
	}
	ct, err := d.compTargetOf(t.s.format())
	if err != nil {
		return nil, err
	}
	if err := t.s.ready(); err != nil {
		return nil, err
	}
	if err := d.settleColors(); err != nil {
		return nil, err
	}
	p := &d.reused
	*p = Pass{d: d, t: t, ct: ct, damage: d.passRects.disjoint(damage, image.Rectangle{Max: t.Size()}), ops: d.passOps[:0]}
	d.active = p
	return p, nil
}

// rectScratch is the storage disjoint keeps between frames.
type rectScratch struct {
	out, pieces, next []image.Rectangle
}

// disjoint is rects clipped to bounds and cut so none overlap. The
// result is valid until the next call.
func (s *rectScratch) disjoint(rects []image.Rectangle, bounds image.Rectangle) []image.Rectangle {
	s.out = s.out[:0]
	for _, r := range rects {
		r = r.Intersect(bounds)
		if r.Empty() {
			continue
		}
		s.pieces = append(s.pieces[:0], r)
		for _, o := range s.out {
			s.next = s.next[:0]
			for _, p := range s.pieces {
				s.next = appendSubtracted(s.next, p, o)
			}
			s.pieces, s.next = s.next, s.pieces
		}
		s.out = append(s.out, s.pieces...)
	}
	return s.out
}

// appendSubtracted appends what is left of r outside o to out.
func appendSubtracted(out []image.Rectangle, r, o image.Rectangle) []image.Rectangle {
	c := r.Intersect(o)
	if c.Empty() {
		return append(out, r)
	}
	for _, p := range [4]image.Rectangle{
		{Min: r.Min, Max: image.Pt(r.Max.X, c.Min.Y)},
		{Min: image.Pt(r.Min.X, c.Min.Y), Max: image.Pt(c.Min.X, c.Max.Y)},
		{Min: image.Pt(c.Max.X, c.Min.Y), Max: image.Pt(r.Max.X, c.Max.Y)},
		{Min: image.Pt(r.Min.X, c.Max.Y), Max: r.Max},
	} {
		if !p.Empty() {
			out = append(out, p)
		}
	}
	return out
}

func (p *Pass) fail(err error) {
	if p.err == nil {
		p.err = err
	}
}

func (p *Pass) open(call string) bool {
	if p.ended {
		p.fail(fmt.Errorf("gpu: %s after End", call))
	}
	return p.err == nil
}

func premultiplied(c color.RGBA) ([4]float32, error) {
	if c.R > c.A || c.G > c.A || c.B > c.A {
		return [4]float32{}, fmt.Errorf("gpu: color %v is not premultiplied", c)
	}
	return [4]float32{float32(c.R) / 255, float32(c.G) / 255, float32(c.B) / 255, float32(c.A) / 255}, nil
}

// Clear sets the damage to c, premultiplied.
func (p *Pass) Clear(c color.RGBA) {
	if !p.open("Clear") {
		return
	}
	col, err := premultiplied(c)
	if err != nil {
		p.fail(err)
		return
	}
	p.ops = append(p.ops, passOp{kind: opClear, dc: drawConstants{extra: [2][4]float32{p.coloredFill(col)}}})
}

func (dc *drawConstants) setMask(m Mask) {
	rect := func(r image.Rectangle) [4]float32 {
		return [4]float32{float32(r.Min.X), float32(r.Min.Y), float32(r.Max.X), float32(r.Max.Y)}
	}
	dc.keep, dc.cut, dc.radii = rect(m.Keep.Rect), rect(m.Cut.Rect), [4]float32{m.Keep.Radius, m.Cut.Radius, m.Falloff.Range, m.Falloff.Power}
	dc.merge, dc.target[3] = rect(m.Merge), m.Smooth
}

// FillRect blends c, premultiplied, over r's damaged part within m.
func (p *Pass) FillRect(r image.Rectangle, c color.RGBA, m Mask) {
	if !p.open("FillRect") || r.Empty() {
		return
	}
	col, err := premultiplied(c)
	if err != nil {
		p.fail(err)
		return
	}
	if err := m.Check(); err != nil {
		p.fail(err)
		return
	}
	dc := drawConstants{extra: [2][4]float32{p.coloredFill(col)}}
	dc.setMask(m)
	p.ops = append(p.ops, passOp{kind: opFill, dst: r, pipe: p.ct.fill, layout: p.d.comp.layout, dc: dc})
}

// Gradient is a linear gradient from From to To, in target pixels:
// Stops, an N x 1 texture of N >= 2 premultiplied colours, spread
// evenly from From (the first) to To (the last), each pixel taking the
// colour at its projection onto the line, clamped at the ends, the
// stops between interpolated linearly, all of it at Alpha.
type Gradient struct {
	Stops    *Texture
	From, To [2]float32
	Alpha    float32
}

// FillGradient blends g over r's damaged part within m.
func (p *Pass) FillGradient(r image.Rectangle, g Gradient, m Mask) {
	if !p.open("FillGradient") || r.Empty() {
		return
	}
	t := g.Stops
	switch {
	case t == nil || t.closed:
		p.fail(errors.New("gpu: FillGradient with closed stops"))
		return
	case t.d != p.d:
		p.fail(errors.New("gpu: FillGradient with another device's stops"))
		return
	case t.h != 1 || t.w < 2 || t.yuv():
		p.fail(fmt.Errorf("gpu: gradient stops are an RGB texture N x 1 with N >= 2, not %dx%d", t.w, t.h))
		return
	case t.surf == &p.t.s:
		p.fail(errors.New("gpu: FillGradient with the pass's own target as stops"))
		return
	case g.From == g.To:
		p.fail(fmt.Errorf("gpu: a gradient from %v to itself has no direction", g.From))
		return
	case !(g.Alpha >= 0 && g.Alpha <= 1):
		p.fail(fmt.Errorf("gpu: gradient alpha %v outside [0, 1]", g.Alpha))
		return
	}
	if err := m.Check(); err != nil {
		p.fail(err)
		return
	}
	set, err := t.set(filterLinear)
	if err != nil {
		p.fail(err)
		return
	}
	dc := drawConstants{extra: [2][4]float32{{g.From[0], g.From[1], g.To[0], g.To[1]}, {float32(t.w), g.Alpha}}}
	dc.setMask(m)
	op := passOp{kind: opTexture, dst: r, tex: t, set: set, pipe: p.ct.grad, layout: p.d.comp.layout, dc: dc}
	if p.colorSet != 0 {
		if op.pipe, err = p.d.colorGradPipeline(p.ct); err != nil {
			p.fail(err)
			return
		}
		op.layout, op.color = p.d.comp.color.layout, p.colorSet
	}
	p.ops = append(p.ops, op)
}

// Goo is two rounded rectangles merged into one blob: their distance
// fields joined by a smooth minimum that melts them together where
// they come within Smooth pixels of each other, as metaballs do; zero
// Smooth is their plain union. GooCoverage mirrors it in Go.
type Goo struct {
	A, B   RoundRect
	Smooth float32
}

func (g Goo) check() error {
	for _, v := range [3]float32{g.A.Radius, g.B.Radius, g.Smooth} {
		if !(v >= 0) || math.IsInf(float64(v), 0) {
			return fmt.Errorf("gpu: goo radius or smoothing %v is not zero or more", v)
		}
	}
	if g.A.Rect.Empty() || g.B.Rect.Empty() {
		return fmt.Errorf("gpu: goo of an empty rectangle (%v, %v)", g.A.Rect, g.B.Rect)
	}
	return nil
}

// FillGoo blends c, premultiplied, over the blob g covers within r's
// damaged part.
func (p *Pass) FillGoo(r image.Rectangle, c color.RGBA, g Goo) {
	if !p.open("FillGoo") || r.Empty() {
		return
	}
	col, err := premultiplied(c)
	if err != nil {
		p.fail(err)
		return
	}
	if err := g.check(); err != nil {
		p.fail(err)
		return
	}
	rect := func(r image.Rectangle) [4]float32 {
		return [4]float32{float32(r.Min.X), float32(r.Min.Y), float32(r.Max.X), float32(r.Max.Y)}
	}
	dc := drawConstants{extra: [2][4]float32{p.coloredFill(col)}, keep: rect(g.A.Rect), merge: rect(g.B.Rect), radii: [4]float32{g.A.Radius, g.B.Radius, g.Smooth}}
	p.ops = append(p.ops, passOp{kind: opFill, dst: r, pipe: p.ct.goo, layout: p.d.comp.layout, dc: dc})
}

// Kawase draws all of t scaled to dst through one pass of a dual
// Kawase blur, sampling t linearly at offset source half-pixels from
// each pixel: four diagonal taps around a centre weighted four (down,
// for a target smaller than t) or eight taps around it (up, for a
// larger one). KawaseAt mirrors it in Go.
func (p *Pass) Kawase(t *Texture, dst image.Rectangle, offset float32, up bool) {
	if !p.open("Kawase") || dst.Empty() {
		return
	}
	switch {
	case t == nil || t.closed:
		p.fail(errors.New("gpu: Kawase of a closed texture"))
		return
	case t.d != p.d:
		p.fail(errors.New("gpu: Kawase of another device's texture"))
		return
	case t.surf == &p.t.s:
		p.fail(errors.New("gpu: Kawase of the pass's own target"))
		return
	case t.yuv():
		p.fail(errors.New("gpu: Kawase of a YCbCr texture"))
		return
	case !(offset >= 0) || math.IsInf(float64(offset), 0):
		p.fail(fmt.Errorf("gpu: Kawase offset %v is not zero or more", offset))
		return
	}
	set, err := t.set(filterLinear)
	if err != nil {
		p.fail(err)
		return
	}
	mode := float32(0)
	if up {
		mode = 1
	}
	dc := drawConstants{target: [4]float32{2: 1}, extra: [2][4]float32{{0, 0, 1, 0}, {0, 1, 1, 1}}}
	dc.keep = [4]float32{0.5 / float32(t.w) * offset, 0.5 / float32(t.h) * offset, mode}
	p.ops = append(p.ops, passOp{kind: opTexture, dst: dst, tex: t, set: set, pipe: p.ct.kawase, layout: p.d.comp.layout, dc: dc})
}

// DrawTexture blends src of t, scaled to dst and turned by tr, at
// alpha over dst's damaged part within m. tr is the buffer transform of the
// texture's content: under Transform90, dst's x runs along src's y and
// dst's y against src's x. A draw that maps whole source pixels one to
// one samples them as they are; any other samples bilinearly.
func (p *Pass) DrawTexture(t *Texture, src FRect, dst image.Rectangle, tr Transform, alpha float32, m Mask) {
	if !p.open("DrawTexture") {
		return
	}
	switch {
	case t == nil || t.closed:
		p.fail(errors.New("gpu: DrawTexture of a closed texture"))
		return
	case t.d != p.d:
		p.fail(errors.New("gpu: DrawTexture of another device's texture"))
		return
	case t.surf == &p.t.s:
		p.fail(errors.New("gpu: DrawTexture of the pass's own target"))
		return
	case tr < TransformNormal || tr > TransformFlipped270:
		p.fail(fmt.Errorf("gpu: transform %d is no wl_output transform", tr))
		return
	case !(alpha >= 0 && alpha <= 1):
		p.fail(fmt.Errorf("gpu: alpha %v outside [0, 1]", alpha))
		return
	case !(src.W > 0 && src.H > 0 && src.X >= 0 && src.Y >= 0 && src.X+src.W <= float64(t.w) && src.Y+src.H <= float64(t.h)):
		p.fail(fmt.Errorf("gpu: source %+v outside a %dx%d texture", src, t.w, t.h))
		return
	}
	if err := m.Check(); err != nil {
		p.fail(err)
		return
	}
	if dst.Empty() || alpha == 0 {
		return
	}
	f := filterLinear
	if t.nearest || whole(src) && transformSize(tr, image.Pt(int(src.W), int(src.H))) == dst.Size() {
		f = filterNearest
	}
	set, err := t.set(f)
	if err != nil {
		p.fail(err)
		return
	}
	dc := drawConstants{target: [4]float32{2: alpha}}
	dc.setMask(m)
	h, v := tr.axes()
	for i, corner := range [4][2]float64{{0, 0}, {1, 0}, {0, 1}, {1, 1}} {
		var s [2]float64
		s[h.source] = flip(corner[0], h.reversed)
		s[v.source] = flip(corner[1], v.reversed)
		dc.extra[i/2][i%2*2] = float32((src.X + float64(s[0]*src.W)) / float64(t.w))
		dc.extra[i/2][i%2*2+1] = float32((src.Y + float64(s[1]*src.H)) / float64(t.h))
	}
	op := passOp{kind: opTexture, dst: dst, tex: t, set: set, pipe: p.ct.tex, layout: p.d.comp.layout, dc: dc}
	if p.colorSet != 0 {
		op.color = p.colorSet
		if op.pipe, op.layout, err = p.d.colorTexturePipeline(t, p.ct, p.t.s.vf); err != nil {
			p.fail(err)
		}
		p.ops = append(p.ops, op)
		return
	}
	if t.ycbcr != nil {
		if op.pipe, err = t.ycbcr.pipeline(p.d, p.t.s.vf); err != nil {
			p.fail(err)
			return
		}
		op.layout = t.ycbcr.layout
	}
	if t.yuv444 != nil {
		if op.pipe, err = t.yuv444.pipeline(p.d, p.t.s.vf); err != nil {
			p.fail(err)
			return
		}
	}
	if t.planes != nil {
		if op.pipe, err = t.planes.pipeline(p.d, p.t.s.vf); err != nil {
			p.fail(err)
			return
		}
		op.layout = t.planes.layout
	}
	p.ops = append(p.ops, op)
}

func whole(r FRect) bool {
	for _, v := range [4]float64{r.X, r.Y, r.W, r.H} {
		if v != math.Trunc(v) {
			return false
		}
	}
	return true
}

func transformSize(t Transform, size image.Point) image.Point {
	if t.swaps() {
		return image.Pt(size.Y, size.X)
	}
	return size
}

type axis struct {
	source   int
	reversed bool
}

func (t Transform) axes() (h, v axis) {
	h, v = axis{0, false}, axis{1, false}
	switch t &^ TransformFlipped {
	case Transform90:
		h, v = axis{1, false}, axis{0, true}
	case Transform180:
		h, v = axis{0, true}, axis{1, true}
	case Transform270:
		h, v = axis{1, true}, axis{0, false}
	}
	if t&TransformFlipped != 0 {
		if h.source == 0 {
			h.reversed = !h.reversed
		} else {
			v.reversed = !v.reversed
		}
	}
	return h, v
}

func flip(f float64, reversed bool) float64 {
	if reversed {
		return 1 - f
	}
	return f
}

func (t *Texture) yuv() bool { return t.ycbcr != nil || t.yuv444 != nil || t.planes != nil }

func (t *Texture) set(f filter) (vk.DescriptorSet, error) {
	sp, sampler := &t.d.comp.sets, t.d.comp.samplers[f]
	if t.ycbcr != nil {
		f, sp, sampler = filterNearest, &t.ycbcr.sets, t.ycbcr.sampler
	}
	if t.sets[f].set == 0 {
		var s textureSet
		var err error
		if t.planes != nil {
			s, err = t.setPlanes(f)
		} else {
			s, err = t.d.allocSet(sp, t.view, sampler)
		}
		if err != nil {
			return 0, err
		}
		t.sets[f] = s
	}
	return t.sets[f].set, nil
}

// End submits what the pass recorded, to draw into its target and hand
// the target back to its consumer, and returns without waiting for the
// GPU: SyncFile signals when it is done, and FrameDone says when. A
// pass whose call failed submits nothing and reports the first error.
func (p *Pass) End() error {
	if p.ended {
		return errors.New("gpu: End of an ended pass")
	}
	p.ended = true
	p.d.active = nil
	err := p.submit()
	clear(p.ops)
	p.d.passOps, p.ops = p.ops[:0], nil
	return err
}

func (p *Pass) submit() error {
	d := p.d
	if p.err != nil {
		return p.err
	}
	if p.t.closed {
		return errors.New("gpu: End on a closed target")
	}
	for _, op := range p.ops {
		if op.tex != nil && op.tex.closed {
			return errors.New("gpu: a pass drew a texture closed before End")
		}
	}
	if len(p.ops) == 0 || len(p.damage) == 0 {
		return nil
	}
	return d.submitAsync(p.record, submission{pass: true})
}

func (p *Pass) record(cb vk.CommandBuffer) {
	d, s, ct := p.d, &p.t.s, p.ct
	p.t.acquire(cb)
	var foreign []*Texture
	for _, op := range p.ops {
		if op.tex != nil && op.tex.foreign && !slices.Contains(foreign, op.tex) {
			foreign = append(foreign, op.tex)
			op.tex.acquireForeign(cb)
		}
	}
	for _, op := range p.ops {
		if op.tex != nil && op.tex.surf != nil {
			op.tex.surf.to(cb, vk.ImageLayoutShaderReadOnlyOptimal)
		}
	}
	s.to(cb, vk.ImageLayoutColorAttachmentOptimal)
	area := vk.Rect2D{Extent: extent2D(s.w, s.h)}
	d.args.passBegin = vk.RenderPassBeginInfo{SType: vk.StructureTypeRenderPassBeginInfo, RenderPass: ct.pass, Framebuffer: s.fb, RenderArea: area}
	d.cmds.CmdBeginRenderPass(cb, &d.args.passBegin, vk.SubpassContentsInline)
	d.args.viewport = vk.Viewport{Width: float32(s.w), Height: float32(s.h), MaxDepth: 1}
	d.cmds.CmdSetViewport(cb, 0, 1, &d.args.viewport)
	stages := vk.ShaderStageFlags(vk.ShaderStageVertexBit | vk.ShaderStageFragmentBit)
	var bound vk.Pipeline
	var set, colorBound vk.DescriptorSet
	var scissor image.Rectangle
	d.passWork = passWork{}
	for i := range p.ops {
		op := &p.ops[i]
		if op.kind == opClear {
			p.clear(cb, op.dc.extra[0])
			continue
		}
		if !p.touchesDamage(op.dst) {
			continue
		}
		if op.pipe != bound {
			d.cmds.CmdBindPipeline(cb, vk.PipelineBindPointGraphics, op.pipe)
			bound = op.pipe
			d.passWork.binds++
		}
		if op.kind == opTexture && op.set != set {
			d.cmds.CmdBindDescriptorSets(cb, vk.PipelineBindPointGraphics, op.layout, 0, 1, &op.set, 0, nil)
			set, colorBound = op.set, 0
		}
		if op.color != 0 && op.color != colorBound {
			d.cmds.CmdBindDescriptorSets(cb, vk.PipelineBindPointGraphics, op.layout, 1, 1, &op.color, 0, nil)
			colorBound = op.color
		}
		op.dc.rect = [4]float32{float32(op.dst.Min.X), float32(op.dst.Min.Y), float32(op.dst.Dx()), float32(op.dst.Dy())}
		op.dc.target[0], op.dc.target[1] = float32(s.w), float32(s.h)
		d.cmds.CmdPushConstants(cb, op.layout, stages, 0, uint32(unsafe.Sizeof(op.dc)), unsafe.Pointer(&op.dc))
		d.passWork.pushes++
		for _, r := range p.damage {
			if !op.dst.Overlaps(r) {
				continue
			}
			if r != scissor {
				scissor = r
				d.args.scissor = rect2D(r)
				d.cmds.CmdSetScissor(cb, 0, 1, &d.args.scissor)
				d.passWork.scissors++
			}
			d.cmds.CmdDraw(cb, 4, 1, 0, 0)
			d.passWork.draws++
		}
	}
	d.cmds.CmdEndRenderPass(cb)
	for _, t := range foreign {
		t.releaseForeign(cb)
	}
	p.t.release(cb)
}

func (p *Pass) touchesDamage(dst image.Rectangle) bool {
	return slices.ContainsFunc(p.damage, dst.Overlaps)
}

func (p *Pass) clear(cb vk.CommandBuffer, col [4]float32) {
	a := &p.d.args
	a.clear = vk.ClearAttachment{AspectMask: vk.ImageAspectFlags(vk.ImageAspectColorBit)}
	copy(a.clear.ClearValue[:], unsafe.Slice((*byte)(unsafe.Pointer(&col[0])), len(a.clear.ClearValue)))
	a.clearRects = a.clearRects[:0]
	for _, r := range p.damage {
		a.clearRects = append(a.clearRects, vk.ClearRect{Rect: rect2D(r), LayerCount: 1})
	}
	p.d.cmds.CmdClearAttachments(cb, 1, &a.clear, count(a.clearRects), &a.clearRects[0])
}
