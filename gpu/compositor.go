package gpu

import (
	"errors"
	"fmt"
	"unsafe"

	"github.com/stubbedev/lys-lib/third_party/vk"
)

type filter int

const (
	filterNearest filter = iota
	filterLinear
)

type compositor struct {
	layout   vk.PipelineLayout
	samplers [2]vk.Sampler
	vert     vk.ShaderModule
	frags    [2]vk.ShaderModule
	sets     setPools
	targets  map[vk.Format]*compTarget
	ycbcr    map[ycbcrKey]*ycbcrKind
}

type compTarget struct {
	pass      vk.RenderPass
	tex, fill vk.Pipeline
}

type drawConstants struct {
	rect, target     [4]float32
	extra            [2][4]float32
	keep, cut, radii [4]float32
}

const compPoolSets = 256

func (d *Device) compInit() error {
	if d.comp.layout != 0 {
		return nil
	}
	if err := d.createComp(); err != nil {
		d.destroyComp()
		return err
	}
	return nil
}

func (d *Device) createComp() error {
	c := &d.comp
	c.targets = map[vk.Format]*compTarget{}
	c.ycbcr = map[ycbcrKey]*ycbcrKind{}
	for i, f := range [2]vk.Filter{vk.FilterNearest, vk.FilterLinear} {
		sci := vk.SamplerCreateInfo{
			SType: vk.StructureTypeSamplerCreateInfo, MagFilter: f, MinFilter: f, MipmapMode: vk.SamplerMipmapModeNearest,
			AddressModeU: vk.SamplerAddressModeClampToEdge, AddressModeV: vk.SamplerAddressModeClampToEdge, AddressModeW: vk.SamplerAddressModeClampToEdge,
		}
		if r := d.cmds.CreateSampler(d.device, &sci, nil, &c.samplers[i]); r != vk.Success {
			return fmt.Errorf("gpu: vkCreateSampler: %d", r)
		}
	}
	var err error
	if c.vert, err = d.shaderModule(compVert); err != nil {
		return err
	}
	for i, spv := range [2][]byte{compFrag, fillFrag} {
		if c.frags[i], err = d.shaderModule(spv); err != nil {
			return err
		}
	}
	return d.samplerLayouts(nil, 1, &c.sets, &c.layout)
}

func (d *Device) samplerLayouts(immutable *vk.Sampler, perSet uint32, sp *setPools, layout *vk.PipelineLayout) error {
	binding := vk.DescriptorSetLayoutBinding{
		Binding: 0, DescriptorType: vk.DescriptorTypeCombinedImageSampler, DescriptorCount: 1,
		StageFlags: vk.ShaderStageFlags(vk.ShaderStageFragmentBit), PImmutableSamplers: immutable,
	}
	sci := vk.DescriptorSetLayoutCreateInfo{SType: vk.StructureTypeDescriptorSetLayoutCreateInfo, BindingCount: 1, PBindings: &binding}
	sp.perSet = perSet
	if r := d.cmds.CreateDescriptorSetLayout(d.device, &sci, nil, &sp.layout); r != vk.Success {
		return fmt.Errorf("gpu: vkCreateDescriptorSetLayout (compositor): %d", r)
	}
	push := vk.PushConstantRange{StageFlags: vk.ShaderStageFlags(vk.ShaderStageVertexBit | vk.ShaderStageFragmentBit), Size: uint32(unsafe.Sizeof(drawConstants{}))}
	lci := vk.PipelineLayoutCreateInfo{SType: vk.StructureTypePipelineLayoutCreateInfo, SetLayoutCount: 1, PSetLayouts: &sp.layout, PushConstantRangeCount: 1, PPushConstantRanges: &push}
	if r := d.cmds.CreatePipelineLayout(d.device, &lci, nil, layout); r != vk.Success {
		return fmt.Errorf("gpu: vkCreatePipelineLayout (compositor): %d", r)
	}
	return nil
}

func (d *Device) compTargetOf(format vk.Format) (*compTarget, error) {
	if err := d.compInit(); err != nil {
		return nil, err
	}
	c := &d.comp
	if ct, ok := c.targets[format]; ok {
		return ct, nil
	}
	ct := &compTarget{}
	var err error
	if ct.pass, err = d.renderPass(format); err == nil {
		if ct.tex, err = d.graphicsPipeline([2]vk.ShaderModule{c.vert, c.frags[0]}, c.layout, ct.pass, blendOver); err == nil {
			ct.fill, err = d.graphicsPipeline([2]vk.ShaderModule{c.vert, c.frags[1]}, c.layout, ct.pass, blendOver)
		}
	}
	if err != nil {
		ct.destroy(d)
		return nil, err
	}
	c.targets[format] = ct
	return ct, nil
}

func (ct *compTarget) destroy(d *Device) {
	for _, p := range []vk.Pipeline{ct.tex, ct.fill} {
		if p != 0 {
			d.cmds.DestroyPipeline(d.device, p, nil)
		}
	}
	if ct.pass != 0 {
		d.cmds.DestroyRenderPass(d.device, ct.pass, nil)
	}
}

type textureSet struct {
	set  vk.DescriptorSet
	pool vk.DescriptorPool
}

func (s *textureSet) free(d *Device) {
	if s.set != 0 {
		d.cmds.FreeDescriptorSets(d.device, s.pool, 1, &s.set)
	}
	*s = textureSet{}
}

type setPools struct {
	layout vk.DescriptorSetLayout
	perSet uint32
	pools  []vk.DescriptorPool
}

func (d *Device) allocSet(sp *setPools, view vk.ImageView, sampler vk.Sampler) (textureSet, error) {
	for _, pool := range sp.pools {
		if set, ok := d.allocateFrom(pool, sp.layout); ok {
			return d.writeSet(textureSet{set: set, pool: pool}, view, sampler), nil
		}
	}
	size := vk.DescriptorPoolSize{Type: vk.DescriptorTypeCombinedImageSampler, DescriptorCount: compPoolSets * sp.perSet}
	dci := vk.DescriptorPoolCreateInfo{
		SType: vk.StructureTypeDescriptorPoolCreateInfo, Flags: vk.DescriptorPoolCreateFlags(vk.DescriptorPoolCreateFreeDescriptorSetBit),
		MaxSets: compPoolSets, PoolSizeCount: 1, PPoolSizes: &size,
	}
	var pool vk.DescriptorPool
	if r := d.cmds.CreateDescriptorPool(d.device, &dci, nil, &pool); r != vk.Success {
		return textureSet{}, fmt.Errorf("gpu: vkCreateDescriptorPool (compositor): %d", r)
	}
	sp.pools = append(sp.pools, pool)
	set, ok := d.allocateFrom(pool, sp.layout)
	if !ok {
		return textureSet{}, errors.New("gpu: a new descriptor pool allocates no set")
	}
	return d.writeSet(textureSet{set: set, pool: pool}, view, sampler), nil
}

func (d *Device) allocateFrom(pool vk.DescriptorPool, layout vk.DescriptorSetLayout) (vk.DescriptorSet, bool) {
	ai := vk.DescriptorSetAllocateInfo{SType: vk.StructureTypeDescriptorSetAllocateInfo, DescriptorPool: pool, DescriptorSetCount: 1, PSetLayouts: &layout}
	var set vk.DescriptorSet
	return set, d.cmds.AllocateDescriptorSets(d.device, &ai, &set) == vk.Success
}

func (d *Device) writeSet(s textureSet, view vk.ImageView, sampler vk.Sampler) textureSet {
	ii := vk.DescriptorImageInfo{Sampler: sampler, ImageView: view, ImageLayout: vk.ImageLayoutShaderReadOnlyOptimal}
	w := vk.WriteDescriptorSet{SType: vk.StructureTypeWriteDescriptorSet, DstSet: s.set, DescriptorCount: 1, DescriptorType: vk.DescriptorTypeCombinedImageSampler, PImageInfo: &ii}
	d.cmds.UpdateDescriptorSets(d.device, 1, &w, 0, nil)
	return s
}

func (sp *setPools) destroy(d *Device) {
	for _, p := range sp.pools {
		d.cmds.DestroyDescriptorPool(d.device, p, nil)
	}
	if sp.layout != 0 {
		d.cmds.DestroyDescriptorSetLayout(d.device, sp.layout, nil)
	}
	*sp = setPools{}
}

func (d *Device) destroyComp() {
	c, dev := &d.comp, d.device
	for _, ct := range c.targets {
		ct.destroy(d)
	}
	for _, k := range c.ycbcr {
		k.destroy(d)
	}
	c.sets.destroy(d)
	for _, m := range []vk.ShaderModule{c.vert, c.frags[0], c.frags[1]} {
		if m != 0 {
			d.cmds.DestroyShaderModule(dev, m, nil)
		}
	}
	for _, s := range c.samplers {
		if s != 0 {
			d.cmds.DestroySampler(dev, s, nil)
		}
	}
	if c.layout != 0 {
		d.cmds.DestroyPipelineLayout(dev, c.layout, nil)
	}
	*c = compositor{}
}
