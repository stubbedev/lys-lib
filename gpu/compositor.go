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
	frags    [7]vk.ShaderModule
	glass    vk.ShaderModule
	sets     setPools
	targets  map[vk.Format]*compTarget
	ycbcr    map[ycbcrKey]*ycbcrKind
	yuv444   map[yuv444Key]*yuv444Kind
	planes   map[planesKey]*planesKind
	color    colorState
}

// The YUV shaders' places in a compositor's frags.
const (
	fragYCbCr444 = 5 + iota
	fragYCbCrPlanes
)

type compTarget struct {
	pass                                vk.RenderPass
	tex, fill, grad, kawase, goo, glass vk.Pipeline
	colorTex, colorGrad, colorGlass     vk.Pipeline
}

type drawConstants struct {
	rect, target     [4]float32
	extra            [2][4]float32
	keep, cut, radii [4]float32
	merge            [4]float32
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
	c.yuv444 = map[yuv444Key]*yuv444Kind{}
	c.planes = map[planesKey]*planesKind{}
	for i, f := range [2]vk.Filter{vk.FilterNearest, vk.FilterLinear} {
		sci := vk.SamplerCreateInfo{
			SType: vk.StructureTypeSamplerCreateInfo, MagFilter: f, MinFilter: f, MipmapMode: vk.SamplerMipmapModeNearest,
			AddressModeU: vk.SamplerAddressModeClampToEdge, AddressModeV: vk.SamplerAddressModeClampToEdge, AddressModeW: vk.SamplerAddressModeClampToEdge,
		}
		if r := d.cmds.CreateSampler(d.device, &sci, nil, &c.samplers[i]); r != vk.Success {
			return fmt.Errorf("gpu: vkCreateSampler: %w", resultError(r))
		}
	}
	var err error
	if c.vert, err = d.shaderModule(compVert); err != nil {
		return err
	}
	for i, spv := range [len(c.frags)][]byte{compFrag, fillFrag, gradFrag, kawaseFrag, gooFrag, fragYCbCr444: ycbcr444Frag, fragYCbCrPlanes: ycbcrPlanesFrag} {
		if c.frags[i], err = d.shaderModule(spv); err != nil {
			return err
		}
	}
	return d.samplerLayouts(nil, 1, 1, &c.sets, &c.layout)
}

// samplerLayouts makes a set layout of bindings combined image samplers
// taking perSet descriptors of a pool's in all, and a pipeline layout of it.
func (d *Device) samplerLayouts(immutable *vk.Sampler, bindings, perSet uint32, sp *setPools, layout *vk.PipelineLayout) error {
	list := make([]vk.DescriptorSetLayoutBinding, bindings)
	for i := range list {
		list[i] = vk.DescriptorSetLayoutBinding{
			Binding: uint32(i), DescriptorType: vk.DescriptorTypeCombinedImageSampler, DescriptorCount: 1,
			StageFlags: vk.ShaderStageFlags(vk.ShaderStageFragmentBit), PImmutableSamplers: immutable,
		}
	}
	sci := vk.DescriptorSetLayoutCreateInfo{SType: vk.StructureTypeDescriptorSetLayoutCreateInfo, BindingCount: bindings, PBindings: &list[0]}
	sp.perSet = perSet
	if r := d.cmds.CreateDescriptorSetLayout(d.device, &sci, nil, &sp.layout); r != vk.Success {
		return fmt.Errorf("gpu: vkCreateDescriptorSetLayout (compositor): %w", resultError(r))
	}
	push := vk.PushConstantRange{StageFlags: vk.ShaderStageFlags(vk.ShaderStageVertexBit | vk.ShaderStageFragmentBit), Size: uint32(unsafe.Sizeof(drawConstants{}))}
	lci := vk.PipelineLayoutCreateInfo{SType: vk.StructureTypePipelineLayoutCreateInfo, SetLayoutCount: 1, PSetLayouts: &sp.layout, PushConstantRangeCount: 1, PPushConstantRanges: &push}
	if r := d.cmds.CreatePipelineLayout(d.device, &lci, nil, layout); r != vk.Success {
		return fmt.Errorf("gpu: vkCreatePipelineLayout (compositor): %w", resultError(r))
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
	if ct.pass, err = d.renderPass(format); err != nil {
		ct.destroy(d)
		return nil, err
	}
	for i, slot := range [5]*vk.Pipeline{&ct.tex, &ct.fill, &ct.grad, &ct.kawase, &ct.goo} {
		modules := [2]vk.ShaderModule{c.vert, c.frags[i]}
		build := func() (vk.Pipeline, error) { return d.graphicsPipeline(modules, c.layout, ct.pass, blendOver) }
		if _, err := d.lazyPipeline(pipelineKey{owner: ct, target: format, which: i}, 0, build, func(p vk.Pipeline) { *slot = p }); err != nil {
			ct.destroy(d)
			return nil, err
		}
	}
	c.targets[format] = ct
	return ct, nil
}

// glassPipeline is DrawGlass's pipeline, built at its first use on a
// target format, so a compositor drawing no glass never pays for it.
func (d *Device) glassPipeline(ct *compTarget) (vk.Pipeline, error) {
	if ct.glass != 0 {
		return ct.glass, nil
	}
	c := &d.comp
	if c.glass == 0 {
		m, err := d.shaderModule(glassFrag)
		if err != nil {
			return 0, err
		}
		c.glass = m
	}
	build := func() (vk.Pipeline, error) {
		return d.graphicsPipeline([2]vk.ShaderModule{c.vert, c.glass}, c.layout, ct.pass, blendOver)
	}
	return d.lazyPipeline(pipelineKey{owner: ct, which: 5}, 0, build, func(p vk.Pipeline) { ct.glass = p })
}

func (ct *compTarget) destroy(d *Device) {
	for _, p := range []vk.Pipeline{ct.tex, ct.fill, ct.grad, ct.kawase, ct.goo, ct.glass, ct.colorTex, ct.colorGrad, ct.colorGlass} {
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
	s, err := d.allocBareSet(sp)
	if err != nil {
		return textureSet{}, err
	}
	return d.writeSet(s, view, sampler), nil
}

func (d *Device) allocBareSet(sp *setPools) (textureSet, error) {
	for _, pool := range sp.pools {
		if set, ok := d.allocateFrom(pool, sp.layout); ok {
			return textureSet{set: set, pool: pool}, nil
		}
	}
	size := vk.DescriptorPoolSize{Type: vk.DescriptorTypeCombinedImageSampler, DescriptorCount: compPoolSets * sp.perSet}
	dci := vk.DescriptorPoolCreateInfo{
		SType: vk.StructureTypeDescriptorPoolCreateInfo, Flags: vk.DescriptorPoolCreateFlags(vk.DescriptorPoolCreateFreeDescriptorSetBit),
		MaxSets: compPoolSets, PoolSizeCount: 1, PPoolSizes: &size,
	}
	var pool vk.DescriptorPool
	if r := d.cmds.CreateDescriptorPool(d.device, &dci, nil, &pool); r != vk.Success {
		return textureSet{}, fmt.Errorf("gpu: vkCreateDescriptorPool (compositor): %w", resultError(r))
	}
	sp.pools = append(sp.pools, pool)
	set, ok := d.allocateFrom(pool, sp.layout)
	if !ok {
		return textureSet{}, errors.New("gpu: a new descriptor pool allocates no set")
	}
	return textureSet{set: set, pool: pool}, nil
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
	for _, k := range c.yuv444 {
		k.destroy(d)
	}
	for _, k := range c.planes {
		k.destroy(d)
	}
	for _, k := range c.ycbcr {
		k.destroy(d)
	}
	c.color.destroy(d)
	c.sets.destroy(d)
	for _, m := range append([]vk.ShaderModule{c.vert, c.glass}, c.frags[:]...) {
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
