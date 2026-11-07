package gpu

import (
	_ "embed" // the cut's GLSL below
	"errors"
	"fmt"
	"image"
	"unsafe"

	"github.com/stubbedev/lys-lib/third_party/vk"
)

// CutGLSL is the GLSL of CutUniforms.Keeps at CutUniforms.UV, cutCoverage,
// valid as GLSL 4.50 and as GLSL ES 1.00 with SAMPLE naming the sampling
// function and CUT_SAMPLER the sampler type, for renderers outside this
// package to share.
//
//go:embed shaders/cut.glsl
var CutGLSL string

// AlphaCut keeps a draw only where a texture's alpha is above Threshold:
// the texture's Src region laid over Dst, in target pixels, through
// Transform, as DrawTexture lays it. Outside Dst nothing is kept. A
// texture sampling opaque has alpha 1 everywhere.
type AlphaCut struct {
	Src       FRect
	Dst       image.Rectangle
	Transform Transform
	Threshold float32
}

// Check reports a cut that does not fit a texture of size: an empty Dst,
// a Src outside the texture, no wl_output transform or a threshold
// outside [0, 1].
func (c AlphaCut) Check(size image.Point) error {
	switch {
	case c.Dst.Empty():
		return fmt.Errorf("gpu: a cut over the empty rectangle %v", c.Dst)
	case c.Transform < TransformNormal || c.Transform > TransformFlipped270:
		return fmt.Errorf("gpu: transform %d is no wl_output transform", c.Transform)
	case !(c.Threshold >= 0 && c.Threshold <= 1):
		return fmt.Errorf("gpu: cut threshold %v outside [0, 1]", c.Threshold)
	case !(c.Src.W > 0 && c.Src.H > 0 && c.Src.X >= 0 && c.Src.Y >= 0 && c.Src.X+c.Src.W <= float64(size.X) && c.Src.Y+c.Src.H <= float64(size.Y)):
		return fmt.Errorf("gpu: cut source %+v outside a %v texture", c.Src, size)
	}
	return nil
}

// CutUniforms is an AlphaCut over one texture as CutGLSL reads it: the
// texture coordinate's change per target pixel along x and y, then its
// value at the target's origin, the threshold and 1 for an opaque texture.
type CutUniforms [2][4]float32

// Uniforms packs c over a texture of size, opaque when it samples opaque.
func (c AlphaCut) Uniforms(size image.Point, opaque bool) CutUniforms {
	corners := textureCorners(c.Src, c.Transform, size)
	o := corners[0]
	w, h := float32(c.Dst.Dx()), float32(c.Dst.Dy())
	ax := [2]float32{(corners[1][0] - o[0]) / w, (corners[1][1] - o[1]) / w}
	ay := [2]float32{(corners[2][0] - o[0]) / h, (corners[2][1] - o[1]) / h}
	x0, y0 := float32(c.Dst.Min.X), float32(c.Dst.Min.Y)
	var full float32
	if opaque {
		full = 1
	}
	return CutUniforms{
		{ax[0], ax[1], ay[0], ay[1]},
		{o[0] - x0*ax[0] - y0*ay[0], o[1] - x0*ax[1] - y0*ay[1], c.Threshold, full},
	}
}

// UV is the normalised texture coordinate under the target point (x, y),
// and whether it lies on the texture.
func (u CutUniforms) UV(x, y float32) (s, t float32, on bool) {
	s = u[1][0] + x*u[0][0] + y*u[0][2]
	t = u[1][1] + x*u[0][1] + y*u[0][3]
	return s, t, s >= 0 && t >= 0 && s <= 1 && t <= 1
}

// Keeps reports whether a texel of alpha a keeps what is drawn over it.
func (u CutUniforms) Keeps(a float32) bool {
	return max(a, u[1][3]) > u[1][2]
}

// textureCorners are the normalised texture coordinates DrawTexture
// samples at a destination's top-left, top-right, bottom-left and
// bottom-right corners.
func textureCorners(src FRect, tr Transform, size image.Point) [4][2]float32 {
	h, v := tr.axes()
	var out [4][2]float32
	for i, corner := range [4][2]float64{{0, 0}, {1, 0}, {0, 1}, {1, 1}} {
		var s [2]float64
		s[h.source] = flip(corner[0], h.reversed)
		s[v.source] = flip(corner[1], v.reversed)
		out[i] = [2]float32{float32((src.X + s[0]*src.W) / float64(size.X)), float32((src.Y + s[1]*src.H) / float64(size.Y))}
	}
	return out
}

const (
	// cutSlots bounds the cuts alive on a device between settles: a few
	// per frame, each a texture and where it lies.
	cutSlots = 256
	// cutSlotBytes is the largest minUniformBufferOffsetAlignment Vulkan
	// allows, which also holds a cut's uniforms.
	cutSlotBytes = 256
)

// The cut variants' places in cutState's frags.
const (
	cutFragTexture = iota
	cutFragTextureColor
	cutFragGlass
	cutFragGlassColor
	cutFragFill
	cutFrags
)

type cutKey struct {
	tex *Texture
	cut AlphaCut
}

// cutState is what drawing through an AlphaCut takes, made on the first
// SetCut so that a device that never cuts pays nothing: the uniforms and
// texture of each cut in use in a set of its own, the fragment shaders and
// the compositor's pipeline layouts with the set added after the sampled
// set, and after the colour transform's.
type cutState struct {
	setLayout   vk.DescriptorSetLayout
	pool        vk.DescriptorPool
	buf         *buffer
	sets        []vk.DescriptorSet
	slots       map[cutKey]int
	free        []int
	frags       [cutFrags]vk.ShaderModule
	layout      vk.PipelineLayout
	colorLayout vk.PipelineLayout
}

func (d *Device) cutInit() (*cutState, error) {
	if err := d.compInit(); err != nil {
		return nil, err
	}
	cs := &d.comp.cut
	if cs.buf != nil {
		return cs, nil
	}
	if err := d.createCut(cs); err != nil {
		cs.destroy(d)
		return nil, fmt.Errorf("gpu: alpha cuts: %w", err)
	}
	return cs, nil
}

func (d *Device) createCut(cs *cutState) error {
	bindings := [2]vk.DescriptorSetLayoutBinding{
		{Binding: 0, DescriptorType: vk.DescriptorTypeCombinedImageSampler, DescriptorCount: 1, StageFlags: vk.ShaderStageFlags(vk.ShaderStageFragmentBit)},
		{Binding: 1, DescriptorType: vk.DescriptorTypeUniformBuffer, DescriptorCount: 1, StageFlags: vk.ShaderStageFlags(vk.ShaderStageFragmentBit)},
	}
	sci := vk.DescriptorSetLayoutCreateInfo{SType: vk.StructureTypeDescriptorSetLayoutCreateInfo, BindingCount: count(bindings[:]), PBindings: &bindings[0]}
	if r := d.cmds.CreateDescriptorSetLayout(d.device, &sci, nil, &cs.setLayout); r != vk.Success {
		return fmt.Errorf("vkCreateDescriptorSetLayout: %w", resultError(r))
	}
	sizes := [2]vk.DescriptorPoolSize{{Type: vk.DescriptorTypeCombinedImageSampler, DescriptorCount: cutSlots}, {Type: vk.DescriptorTypeUniformBuffer, DescriptorCount: cutSlots}}
	pci := vk.DescriptorPoolCreateInfo{SType: vk.StructureTypeDescriptorPoolCreateInfo, MaxSets: cutSlots, PoolSizeCount: count(sizes[:]), PPoolSizes: &sizes[0]}
	if r := d.cmds.CreateDescriptorPool(d.device, &pci, nil, &cs.pool); r != vk.Success {
		return fmt.Errorf("vkCreateDescriptorPool: %w", resultError(r))
	}
	var err error
	if cs.buf, err = d.newBuffer(cutSlots*cutSlotBytes, vk.BufferUsageUniformBufferBit); err != nil {
		return err
	}
	cs.sets = make([]vk.DescriptorSet, cutSlots)
	for i := range cs.sets {
		set, ok := d.allocateFrom(cs.pool, cs.setLayout)
		if !ok {
			return errors.New("a descriptor pool of cuts allocates no set")
		}
		cs.sets[i] = set
		info := vk.DescriptorBufferInfo{Buffer: cs.buf.buf, Offset: vk.DeviceSize(i * cutSlotBytes), Range: cutSlotBytes}
		w := vk.WriteDescriptorSet{SType: vk.StructureTypeWriteDescriptorSet, DstSet: set, DstBinding: 1, DescriptorCount: 1, DescriptorType: vk.DescriptorTypeUniformBuffer, PBufferInfo: &info}
		d.cmds.UpdateDescriptorSets(d.device, 1, &w, 0, nil)
	}
	cs.slots = make(map[cutKey]int, cutSlots)
	cs.free = make([]int, cutSlots)
	for i := range cs.free {
		cs.free[i] = cutSlots - 1 - i
	}
	for i, spv := range [cutFrags][]byte{compCutFrag, compCutColorFrag, glassCutFrag, glassCutColorFrag, fillCutFrag} {
		if cs.frags[i], err = d.shaderModule(spv); err != nil {
			return err
		}
	}
	if cs.layout, err = d.pipelineLayout(d.comp.sets.layout, cs.setLayout); err != nil {
		return err
	}
	return nil
}

// cutColorLayout is the layout of a cut draw through a colour transform,
// made at the first one.
func (d *Device) cutColorLayout(cs *cutState) (vk.PipelineLayout, error) {
	if cs.colorLayout != 0 {
		return cs.colorLayout, nil
	}
	layout, err := d.pipelineLayout(d.comp.sets.layout, d.comp.color.setLayout, cs.setLayout)
	if err != nil {
		return 0, err
	}
	cs.colorLayout = layout
	return layout, nil
}

// pipelineLayout is a pipeline layout of sets, in order, and the
// compositor's push constants.
func (d *Device) pipelineLayout(sets ...vk.DescriptorSetLayout) (vk.PipelineLayout, error) {
	push := vk.PushConstantRange{StageFlags: vk.ShaderStageFlags(vk.ShaderStageVertexBit | vk.ShaderStageFragmentBit), Size: uint32(unsafe.Sizeof(drawConstants{}))}
	lci := vk.PipelineLayoutCreateInfo{SType: vk.StructureTypePipelineLayoutCreateInfo, SetLayoutCount: count(sets), PSetLayouts: &sets[0], PushConstantRangeCount: 1, PPushConstantRanges: &push}
	var layout vk.PipelineLayout
	if r := d.cmds.CreatePipelineLayout(d.device, &lci, nil, &layout); r != vk.Success {
		return 0, fmt.Errorf("vkCreatePipelineLayout: %w", resultError(r))
	}
	return layout, nil
}

func (cs *cutState) destroy(d *Device) {
	for _, m := range cs.frags {
		if m != 0 {
			d.cmds.DestroyShaderModule(d.device, m, nil)
		}
	}
	for _, l := range [2]vk.PipelineLayout{cs.layout, cs.colorLayout} {
		if l != 0 {
			d.cmds.DestroyPipelineLayout(d.device, l, nil)
		}
	}
	if cs.pool != 0 {
		d.cmds.DestroyDescriptorPool(d.device, cs.pool, nil)
	}
	if cs.setLayout != 0 {
		d.cmds.DestroyDescriptorSetLayout(d.device, cs.setLayout, nil)
	}
	if cs.buf != nil {
		cs.buf.close()
	}
	*cs = cutState{}
}

// cutSet is the set holding t and c's uniforms, written the first time they
// come along. The slot stays theirs until settleCuts retires it and the
// submissions that may read it finish, so a recorded pass reads what it was
// given.
func (d *Device) cutSet(t *Texture, c AlphaCut) (vk.DescriptorSet, error) {
	cs, err := d.cutInit()
	if err != nil {
		return 0, err
	}
	key := cutKey{tex: t, cut: c}
	if i, ok := cs.slots[key]; ok {
		return cs.sets[i], nil
	}
	if len(cs.free) == 0 {
		if err := d.idle(); err != nil {
			return 0, err
		}
	}
	n := len(cs.free)
	if n == 0 {
		return 0, fmt.Errorf("gpu: more than %d cuts in one pass", cutSlots)
	}
	i := cs.free[n-1]
	cs.free = cs.free[:n-1]
	u := c.Uniforms(image.Pt(t.w, t.h), t.pf.opaque)
	copy(cs.buf.mapped[i*cutSlotBytes:], unsafe.Slice((*byte)(unsafe.Pointer(&u[0])), unsafe.Sizeof(u)))
	ii := vk.DescriptorImageInfo{Sampler: d.comp.samplers[filterLinear], ImageView: t.view, ImageLayout: vk.ImageLayoutShaderReadOnlyOptimal}
	w := vk.WriteDescriptorSet{SType: vk.StructureTypeWriteDescriptorSet, DstSet: cs.sets[i], DescriptorCount: 1, DescriptorType: vk.DescriptorTypeCombinedImageSampler, PImageInfo: &ii}
	d.cmds.UpdateDescriptorSets(d.device, 1, &w, 0, nil)
	cs.slots[key] = i
	return cs.sets[i], nil
}

// settleCuts frees the slots when they run low, between passes: a cut still
// in use takes a new one in its next pass.
func (d *Device) settleCuts() {
	cs := &d.comp.cut
	if len(cs.slots) < cutSlots/2 {
		return
	}
	slots := make([]int, 0, len(cs.slots))
	for _, i := range cs.slots {
		slots = append(slots, i)
	}
	d.retire(&retiredCuts{cs: cs, slots: slots})
	clear(cs.slots)
}

type retiredCuts struct {
	cs    *cutState
	slots []int
}

func (r *retiredCuts) destroy() { r.cs.free = append(r.cs.free, r.slots...) }

// cutPipeline is the pipeline of frag, one of the cut variants, into ct's
// target, built at its first use.
func (d *Device) cutPipeline(ct *compTarget, frag int, layout vk.PipelineLayout) (vk.Pipeline, error) {
	if ct.cut[frag] != 0 {
		return ct.cut[frag], nil
	}
	cs := &d.comp.cut
	build := func() (vk.Pipeline, error) {
		return d.graphicsPipeline([2]vk.ShaderModule{d.comp.vert, cs.frags[frag]}, layout, ct.pass, blendOver)
	}
	return d.lazyPipeline(pipelineKey{owner: ct, which: 16 + frag}, 0, build, func(p vk.Pipeline) { ct.cut[frag] = p })
}

// cutOp sends op through the pass's cut, frag's colourless variant or
// its colour variant after it: the pipeline, the layout and the set.
func (p *Pass) cutOp(op *passOp, frag int) error {
	cs := &p.d.comp.cut
	layout := cs.layout
	if op.color != 0 {
		frag++
		var err error
		if layout, err = p.d.cutColorLayout(cs); err != nil {
			return err
		}
	}
	pipe, err := p.d.cutPipeline(p.ct, frag, layout)
	if err != nil {
		return err
	}
	op.pipe, op.layout, op.opaque, op.cut, op.cutTex = pipe, layout, false, p.cutSet, p.cutTex
	return nil
}

// SetCut sends every later FillRect, DrawTexture and DrawGlass of the pass
// through c over t, until ClearCut: each keeps only what lies over t's
// texels of alpha above c's threshold. Gradients, goo and Kawase draw uncut.
func (p *Pass) SetCut(t *Texture, c AlphaCut) {
	if !p.open("SetCut") {
		return
	}
	switch {
	case t == nil || t.closed:
		p.fail(errors.New("gpu: SetCut over a closed texture"))
		return
	case t.d != p.d:
		p.fail(errors.New("gpu: SetCut over another device's texture"))
		return
	case t.surf == &p.t.s:
		p.fail(errors.New("gpu: SetCut over the pass's own target"))
		return
	case t.yuv():
		p.fail(errors.New("gpu: SetCut over a YCbCr texture, which has no alpha"))
		return
	}
	if err := c.Check(image.Pt(t.w, t.h)); err != nil {
		p.fail(err)
		return
	}
	set, err := p.d.cutSet(t, c)
	if err != nil {
		p.fail(err)
		return
	}
	p.cutTex, p.cutSet = t, set
}

// ClearCut draws the pass's later calls uncut, as it began.
func (p *Pass) ClearCut() {
	if !p.open("ClearCut") {
		return
	}
	p.cutTex, p.cutSet = nil, 0
}
