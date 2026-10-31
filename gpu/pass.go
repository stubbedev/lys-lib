package gpu

import (
	"errors"
	"fmt"
	"image"
	"image/color"
	"math"
	"slices"
	"unsafe"

	"github.com/stubbedev/lys-lib/render"
	"github.com/stubbedev/lys-lib/third_party/vk"
)

// FRect is a rectangle in fractional pixels: the region of a texture
// a draw samples, as wp_viewporter crops a buffer.
type FRect struct {
	X, Y, W, H float64
}

// Pass draws a compositor's frame into a Target, every call clipped to
// the damage it began with. The first bad call fails the pass: End
// reports it and draws nothing.
type Pass struct {
	d      *Device
	t      *Target
	ct     *compTarget
	damage []image.Rectangle
	ops    []passOp
	err    error
	ended  bool
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
	p := &Pass{d: d, t: t, ct: ct, damage: disjoint(damage, image.Rectangle{Max: t.Size()})}
	d.active = p
	return p, nil
}

func disjoint(rects []image.Rectangle, bounds image.Rectangle) []image.Rectangle {
	var out []render.Rect
	for _, r := range rects {
		r = r.Intersect(bounds)
		if r.Empty() {
			continue
		}
		pieces := []render.Rect{{X: r.Min.X, Y: r.Min.Y, W: r.Dx(), H: r.Dy()}}
		for _, o := range out {
			var next []render.Rect
			for _, p := range pieces {
				next = append(next, p.Subtract(o)...)
			}
			pieces = next
		}
		out = append(out, pieces...)
	}
	rs := make([]image.Rectangle, 0, len(out))
	for _, r := range out {
		if !r.Empty() {
			rs = append(rs, rectOf(r))
		}
	}
	return rs
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
	p.ops = append(p.ops, passOp{kind: opClear, dc: drawConstants{extra: [2][4]float32{col}}})
}

func (dc *drawConstants) setMask(m Mask) {
	rect := func(r image.Rectangle) [4]float32 {
		return [4]float32{float32(r.Min.X), float32(r.Min.Y), float32(r.Max.X), float32(r.Max.Y)}
	}
	dc.keep, dc.cut, dc.radii = rect(m.Keep.Rect), rect(m.Cut.Rect), [4]float32{m.Keep.Radius, m.Cut.Radius, m.Falloff.Range, m.Falloff.Power}
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
	dc := drawConstants{extra: [2][4]float32{col}}
	dc.setMask(m)
	p.ops = append(p.ops, passOp{kind: opFill, dst: r, pipe: p.ct.fill, layout: p.d.comp.layout, dc: dc})
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
	if whole(src) && transformSize(tr, image.Pt(int(src.W), int(src.H))) == dst.Size() {
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
		dc.extra[i/2][i%2*2] = float32((src.X + s[0]*src.W) / float64(t.w))
		dc.extra[i/2][i%2*2+1] = float32((src.Y + s[1]*src.H) / float64(t.h))
	}
	op := passOp{kind: opTexture, dst: dst, tex: t, set: set, pipe: p.ct.tex, layout: p.d.comp.layout, dc: dc}
	if t.ycbcr != nil {
		if op.pipe, err = t.ycbcr.pipeline(p.d, p.t.s.vf); err != nil {
			p.fail(err)
			return
		}
		op.layout = t.ycbcr.layout
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

func (t *Texture) set(f filter) (vk.DescriptorSet, error) {
	sp, sampler := &t.d.comp.sets, t.d.comp.samplers[f]
	if t.ycbcr != nil {
		f, sp, sampler = filterNearest, &t.ycbcr.sets, t.ycbcr.sampler
	}
	if t.sets[f].set == 0 {
		s, err := t.d.allocSet(sp, t.view, sampler)
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
	d := p.d
	d.active = nil
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
	ct, err := d.compTargetOf(p.t.s.format())
	if err != nil {
		return err
	}
	return d.submitAsync(func(cb vk.CommandBuffer) { p.record(cb, ct) }, true)
}

func (p *Pass) record(cb vk.CommandBuffer, ct *compTarget) {
	d, s := p.d, &p.t.s
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
	bi := vk.RenderPassBeginInfo{SType: vk.StructureTypeRenderPassBeginInfo, RenderPass: ct.pass, Framebuffer: s.fb, RenderArea: area}
	d.cmds.CmdBeginRenderPass(cb, &bi, vk.SubpassContentsInline)
	vp := vk.Viewport{Width: float32(s.w), Height: float32(s.h), MaxDepth: 1}
	d.cmds.CmdSetViewport(cb, 0, 1, &vp)
	stages := vk.ShaderStageFlags(vk.ShaderStageVertexBit | vk.ShaderStageFragmentBit)
	var bound vk.Pipeline
	var set vk.DescriptorSet
	for i := range p.ops {
		op := &p.ops[i]
		if op.kind == opClear {
			p.clear(cb, op.dc.extra[0])
			continue
		}
		if op.pipe != bound {
			d.cmds.CmdBindPipeline(cb, vk.PipelineBindPointGraphics, op.pipe)
			bound = op.pipe
		}
		if op.kind == opTexture && op.set != set {
			d.cmds.CmdBindDescriptorSets(cb, vk.PipelineBindPointGraphics, op.layout, 0, 1, &op.set, 0, nil)
			set = op.set
		}
		op.dc.rect = [4]float32{float32(op.dst.Min.X), float32(op.dst.Min.Y), float32(op.dst.Dx()), float32(op.dst.Dy())}
		op.dc.target[0], op.dc.target[1] = float32(s.w), float32(s.h)
		d.cmds.CmdPushConstants(cb, op.layout, stages, 0, uint32(unsafe.Sizeof(op.dc)), unsafe.Pointer(&op.dc))
		for _, r := range p.damage {
			clip := op.dst.Intersect(r)
			if clip.Empty() {
				continue
			}
			sc := rect2D(clip)
			d.cmds.CmdSetScissor(cb, 0, 1, &sc)
			d.cmds.CmdDraw(cb, 4, 1, 0, 0)
		}
	}
	d.cmds.CmdEndRenderPass(cb)
	for _, t := range foreign {
		t.releaseForeign(cb)
	}
	p.t.release(cb)
}

func (p *Pass) clear(cb vk.CommandBuffer, col [4]float32) {
	att := vk.ClearAttachment{AspectMask: vk.ImageAspectFlags(vk.ImageAspectColorBit)}
	copy(att.ClearValue[:], unsafe.Slice((*byte)(unsafe.Pointer(&col[0])), len(att.ClearValue)))
	rects := make([]vk.ClearRect, len(p.damage))
	for i, r := range p.damage {
		rects[i] = vk.ClearRect{Rect: rect2D(r), LayerCount: 1}
	}
	p.d.cmds.CmdClearAttachments(cb, 1, &att, count(rects), &rects[0])
}
