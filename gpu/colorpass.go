package gpu

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"unsafe"

	"github.com/stubbedev/lys-lib/third_party/vk"
)

const (
	// colorSlots bounds the different transforms alive on a device: one per
	// pair of encodings in use, so a few.
	colorSlots = 256
	// colorSlotBytes is the largest minUniformBufferOffsetAlignment Vulkan
	// allows, which also holds a transform's uniforms.
	colorSlotBytes = 256
)

// The colour variants' places in colorState's frags.
const (
	colorFragTexture = iota
	colorFragGradient
	colorFragYCbCr444
	colorFragYCbCrPlanes
	colorFragGlass
	colorFrags
)

// colorState is what drawing through a ColorTransform takes, made on the
// first SetColor so that a device that never sets one pays nothing: the
// uniforms of the transforms in use, a set for each, the fragment shaders and
// the compositor's pipeline layout with the set added.
type colorState struct {
	setLayout vk.DescriptorSetLayout
	pool      vk.DescriptorPool
	buf       *buffer
	sets      []vk.DescriptorSet
	slots     map[ColorTransform]int
	free      []int
	frags     [colorFrags]vk.ShaderModule
	layout    vk.PipelineLayout
}

func (d *Device) colorInit() (*colorState, error) {
	if err := d.compInit(); err != nil {
		return nil, err
	}
	cs := &d.comp.color
	if cs.buf != nil {
		return cs, nil
	}
	if err := d.createColor(cs); err != nil {
		cs.destroy(d)
		return nil, fmt.Errorf("gpu: colour transforms: %w", err)
	}
	return cs, nil
}

func (d *Device) createColor(cs *colorState) error {
	binding := vk.DescriptorSetLayoutBinding{DescriptorType: vk.DescriptorTypeUniformBuffer, DescriptorCount: 1, StageFlags: vk.ShaderStageFlags(vk.ShaderStageFragmentBit)}
	sci := vk.DescriptorSetLayoutCreateInfo{SType: vk.StructureTypeDescriptorSetLayoutCreateInfo, BindingCount: 1, PBindings: &binding}
	if r := d.cmds.CreateDescriptorSetLayout(d.device, &sci, nil, &cs.setLayout); r != vk.Success {
		return fmt.Errorf("vkCreateDescriptorSetLayout: %w", resultError(r))
	}
	size := vk.DescriptorPoolSize{Type: vk.DescriptorTypeUniformBuffer, DescriptorCount: colorSlots}
	pci := vk.DescriptorPoolCreateInfo{SType: vk.StructureTypeDescriptorPoolCreateInfo, MaxSets: colorSlots, PoolSizeCount: 1, PPoolSizes: &size}
	if r := d.cmds.CreateDescriptorPool(d.device, &pci, nil, &cs.pool); r != vk.Success {
		return fmt.Errorf("vkCreateDescriptorPool: %w", resultError(r))
	}
	var err error
	if cs.buf, err = d.newBuffer(colorSlots*colorSlotBytes, vk.BufferUsageUniformBufferBit); err != nil {
		return err
	}
	cs.sets = make([]vk.DescriptorSet, colorSlots)
	for i := range cs.sets {
		set, ok := d.allocateFrom(cs.pool, cs.setLayout)
		if !ok {
			return errors.New("a descriptor pool of uniform buffers allocates no set")
		}
		cs.sets[i] = set
		info := vk.DescriptorBufferInfo{Buffer: cs.buf.buf, Offset: vk.DeviceSize(i * colorSlotBytes), Range: colorSlotBytes}
		w := vk.WriteDescriptorSet{SType: vk.StructureTypeWriteDescriptorSet, DstSet: set, DescriptorCount: 1, DescriptorType: vk.DescriptorTypeUniformBuffer, PBufferInfo: &info}
		d.cmds.UpdateDescriptorSets(d.device, 1, &w, 0, nil)
	}
	cs.slots = make(map[ColorTransform]int, colorSlots)
	cs.free = make([]int, colorSlots)
	for i := range cs.free {
		cs.free[i] = colorSlots - 1 - i
	}
	for i, spv := range [colorFrags][]byte{compColorFrag, gradColorFrag, ycbcr444ColorFrag, ycbcrPlanesColorFrag, glassColorFrag} {
		if cs.frags[i], err = d.shaderModule(spv); err != nil {
			return err
		}
	}
	cs.layout, err = d.colorLayout(d.comp.sets.layout)
	return err
}

// colorLayout is the compositor's pipeline layout over the sampled set in
// base, with the colour transform's set after it.
func (d *Device) colorLayout(base vk.DescriptorSetLayout) (vk.PipelineLayout, error) {
	sets := [2]vk.DescriptorSetLayout{base, d.comp.color.setLayout}
	push := vk.PushConstantRange{StageFlags: vk.ShaderStageFlags(vk.ShaderStageVertexBit | vk.ShaderStageFragmentBit), Size: uint32(unsafe.Sizeof(drawConstants{}))}
	lci := vk.PipelineLayoutCreateInfo{SType: vk.StructureTypePipelineLayoutCreateInfo, SetLayoutCount: 2, PSetLayouts: &sets[0], PushConstantRangeCount: 1, PPushConstantRanges: &push}
	var layout vk.PipelineLayout
	if r := d.cmds.CreatePipelineLayout(d.device, &lci, nil, &layout); r != vk.Success {
		return 0, fmt.Errorf("vkCreatePipelineLayout: %w", resultError(r))
	}
	return layout, nil
}

func (cs *colorState) destroy(d *Device) {
	for _, m := range cs.frags {
		if m != 0 {
			d.cmds.DestroyShaderModule(d.device, m, nil)
		}
	}
	if cs.layout != 0 {
		d.cmds.DestroyPipelineLayout(d.device, cs.layout, nil)
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
	*cs = colorState{}
}

// colorSet is the set holding t's uniforms, written the first time t comes
// along. The slot stays t's until settleColors retires it and the submissions
// that may read it finish, so a recorded pass reads what it was given.
func (d *Device) colorSet(t *ColorTransform) (vk.DescriptorSet, error) {
	cs, err := d.colorInit()
	if err != nil {
		return 0, err
	}
	if i, ok := cs.slots[*t]; ok {
		return cs.sets[i], nil
	}
	if len(cs.free) == 0 {
		if err := d.idle(); err != nil {
			return 0, err
		}
	}
	n := len(cs.free)
	if n == 0 {
		return 0, fmt.Errorf("gpu: more than %d colour transforms in one pass", colorSlots)
	}
	i := cs.free[n-1]
	cs.free = cs.free[:n-1]
	u := t.Uniforms()
	copy(cs.buf.mapped[i*colorSlotBytes:], unsafe.Slice((*byte)(unsafe.Pointer(&u[0])), unsafe.Sizeof(u)))
	cs.slots[*t] = i
	return cs.sets[i], nil
}

// settleColors frees the slots when they run low, between passes: a transform
// still in use takes a new one in its next pass.
func (d *Device) settleColors() {
	cs := &d.comp.color
	if len(cs.slots) < colorSlots/2 {
		return
	}
	d.retire(&retiredColors{cs: cs, slots: slices.Collect(maps.Values(cs.slots))})
	clear(cs.slots)
}

type retiredColors struct {
	cs    *colorState
	slots []int
}

func (r *retiredColors) destroy() { r.cs.free = append(r.cs.free, r.slots...) }

// coloredPipes are one YUV kind's pipelines through a colour transform.
type coloredPipes struct {
	layout vk.PipelineLayout
	pipes  map[vk.Format]vk.Pipeline
}

func (c *coloredPipes) get(d *Device, target vk.Format, base vk.DescriptorSetLayout, make func(layout vk.PipelineLayout) (vk.Pipeline, error)) (vk.Pipeline, vk.PipelineLayout, error) {
	if p, ok := c.pipes[target]; ok {
		return p, c.layout, nil
	}
	if c.layout == 0 {
		layout, err := d.colorLayout(base)
		if err != nil {
			return 0, 0, err
		}
		c.layout, c.pipes = layout, map[vk.Format]vk.Pipeline{}
	}
	p, err := make(c.layout)
	if err != nil {
		return 0, 0, err
	}
	c.pipes[target] = p
	return p, c.layout, nil
}

func (c *coloredPipes) destroy(d *Device) {
	for _, p := range c.pipes {
		d.cmds.DestroyPipeline(d.device, p, nil)
	}
	if c.layout != 0 {
		d.cmds.DestroyPipelineLayout(d.device, c.layout, nil)
	}
	*c = coloredPipes{}
}

func (d *Device) colorGradPipeline(ct *compTarget) (vk.Pipeline, error) {
	if ct.colorGrad != 0 {
		return ct.colorGrad, nil
	}
	cs := &d.comp.color
	var err error
	ct.colorGrad, err = d.graphicsPipeline([2]vk.ShaderModule{d.comp.vert, cs.frags[colorFragGradient]}, cs.layout, ct.pass, blendOver)
	return ct.colorGrad, err
}

func (d *Device) colorGlassPipeline(ct *compTarget) (vk.Pipeline, error) {
	if ct.colorGlass != 0 {
		return ct.colorGlass, nil
	}
	cs := &d.comp.color
	var err error
	ct.colorGlass, err = d.graphicsPipeline([2]vk.ShaderModule{d.comp.vert, cs.frags[colorFragGlass]}, cs.layout, ct.pass, blendOver)
	return ct.colorGlass, err
}

// colorTexturePipeline is how t, of any kind, draws through a colour transform
// into a ct target in format target.
func (d *Device) colorTexturePipeline(t *Texture, ct *compTarget, target vk.Format) (vk.Pipeline, vk.PipelineLayout, error) {
	cs := &d.comp.color
	switch {
	case t.ycbcr != nil:
		kind := t.ycbcr
		return kind.colored.get(d, target, kind.sets.layout, func(layout vk.PipelineLayout) (vk.Pipeline, error) {
			return d.graphicsPipeline([2]vk.ShaderModule{d.comp.vert, cs.frags[colorFragTexture]}, layout, ct.pass, blendOver)
		})
	case t.yuv444 != nil:
		kind := t.yuv444
		return kind.colored.get(d, target, d.comp.sets.layout, func(layout vk.PipelineLayout) (vk.Pipeline, error) {
			return d.constantsPipelineOf(target, cs.frags[colorFragYCbCr444], layout, unsafe.Pointer(&kind.constants), unsafe.Sizeof(kind.constants))
		})
	case t.planes != nil:
		kind := t.planes
		return kind.colored.get(d, target, kind.sets.layout, func(layout vk.PipelineLayout) (vk.Pipeline, error) {
			return d.constantsPipelineOf(target, cs.frags[colorFragYCbCrPlanes], layout, unsafe.Pointer(&kind.constants), unsafe.Sizeof(kind.constants))
		})
	}
	if ct.colorTex == 0 {
		p, err := d.graphicsPipeline([2]vk.ShaderModule{d.comp.vert, cs.frags[colorFragTexture]}, cs.layout, ct.pass, blendOver)
		if err != nil {
			return 0, 0, err
		}
		ct.colorTex = p
	}
	return ct.colorTex, cs.layout, nil
}

// SetColor sends every later draw of the pass through t before it blends: a
// fill's colour, a gradient's stops and a texture's samples of any format
// after YCbCr conversion, while coverage and alpha stay outside it. Nil, what
// a pass starts with, draws without a transform. Kawase never takes one: it
// averages a target already in the pass's encoding.
func (p *Pass) SetColor(t *ColorTransform) {
	if !p.open("SetColor") {
		return
	}
	if t == nil {
		p.color, p.colorSet = nil, 0
		return
	}
	set, err := p.d.colorSet(t)
	if err != nil {
		p.fail(err)
		return
	}
	p.color, p.colorSet = t, set
}

func (p *Pass) coloredFill(c [4]float32) [4]float32 {
	if p.color == nil {
		return c
	}
	return p.color.Apply(c)
}
