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
	setLayout vk.DescriptorSetLayout
	layout    vk.PipelineLayout
	samplers  [2]vk.Sampler
	vert      vk.ShaderModule
	frags     [2]vk.ShaderModule
	pools     []vk.DescriptorPool
	targets   map[vk.Format]*compTarget
}

type compTarget struct {
	pass      vk.RenderPass
	tex, fill vk.Pipeline
}

type drawConstants struct {
	rect, target [4]float32
	extra        [2][4]float32
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
	binding := vk.DescriptorSetLayoutBinding{Binding: 0, DescriptorType: vk.DescriptorTypeCombinedImageSampler, DescriptorCount: 1, StageFlags: vk.ShaderStageFlags(vk.ShaderStageFragmentBit)}
	sci := vk.DescriptorSetLayoutCreateInfo{SType: vk.StructureTypeDescriptorSetLayoutCreateInfo, BindingCount: 1, PBindings: &binding}
	if r := d.cmds.CreateDescriptorSetLayout(d.device, &sci, nil, &c.setLayout); r != vk.Success {
		return fmt.Errorf("gpu: vkCreateDescriptorSetLayout (compositor): %d", r)
	}
	push := vk.PushConstantRange{StageFlags: vk.ShaderStageFlags(vk.ShaderStageVertexBit | vk.ShaderStageFragmentBit), Size: uint32(unsafe.Sizeof(drawConstants{}))}
	lci := vk.PipelineLayoutCreateInfo{SType: vk.StructureTypePipelineLayoutCreateInfo, SetLayoutCount: 1, PSetLayouts: &c.setLayout, PushConstantRangeCount: 1, PPushConstantRanges: &push}
	if r := d.cmds.CreatePipelineLayout(d.device, &lci, nil, &c.layout); r != vk.Success {
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

func (d *Device) compSet(view vk.ImageView, f filter) (textureSet, error) {
	c := &d.comp
	for _, pool := range c.pools {
		if set, ok := d.allocateCompSet(pool); ok {
			return d.writeCompSet(textureSet{set: set, pool: pool}, view, f), nil
		}
	}
	size := vk.DescriptorPoolSize{Type: vk.DescriptorTypeCombinedImageSampler, DescriptorCount: compPoolSets}
	dci := vk.DescriptorPoolCreateInfo{
		SType: vk.StructureTypeDescriptorPoolCreateInfo, Flags: vk.DescriptorPoolCreateFlags(vk.DescriptorPoolCreateFreeDescriptorSetBit),
		MaxSets: compPoolSets, PoolSizeCount: 1, PPoolSizes: &size,
	}
	var pool vk.DescriptorPool
	if r := d.cmds.CreateDescriptorPool(d.device, &dci, nil, &pool); r != vk.Success {
		return textureSet{}, fmt.Errorf("gpu: vkCreateDescriptorPool (compositor): %d", r)
	}
	c.pools = append(c.pools, pool)
	set, ok := d.allocateCompSet(pool)
	if !ok {
		return textureSet{}, errors.New("gpu: a new descriptor pool allocates no set")
	}
	return d.writeCompSet(textureSet{set: set, pool: pool}, view, f), nil
}

func (d *Device) allocateCompSet(pool vk.DescriptorPool) (vk.DescriptorSet, bool) {
	ai := vk.DescriptorSetAllocateInfo{SType: vk.StructureTypeDescriptorSetAllocateInfo, DescriptorPool: pool, DescriptorSetCount: 1, PSetLayouts: &d.comp.setLayout}
	var set vk.DescriptorSet
	return set, d.cmds.AllocateDescriptorSets(d.device, &ai, &set) == vk.Success
}

func (d *Device) writeCompSet(s textureSet, view vk.ImageView, f filter) textureSet {
	ii := vk.DescriptorImageInfo{Sampler: d.comp.samplers[f], ImageView: view, ImageLayout: vk.ImageLayoutShaderReadOnlyOptimal}
	w := vk.WriteDescriptorSet{SType: vk.StructureTypeWriteDescriptorSet, DstSet: s.set, DescriptorCount: 1, DescriptorType: vk.DescriptorTypeCombinedImageSampler, PImageInfo: &ii}
	d.cmds.UpdateDescriptorSets(d.device, 1, &w, 0, nil)
	return s
}

func (d *Device) destroyComp() {
	c, dev := &d.comp, d.device
	for _, ct := range c.targets {
		ct.destroy(d)
	}
	for _, p := range c.pools {
		d.cmds.DestroyDescriptorPool(dev, p, nil)
	}
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
	if c.setLayout != 0 {
		d.cmds.DestroyDescriptorSetLayout(dev, c.setLayout, nil)
	}
	*c = compositor{}
}
