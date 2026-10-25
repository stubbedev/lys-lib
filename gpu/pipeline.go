package gpu

import (
	"fmt"
	"runtime"
	"unsafe"

	"github.com/stubbedev/lys-ui/third_party/vk"
)

// maxSurfaces bounds the surfaces sampled at once, one descriptor set
// each: targets, layers and upload pages.
const maxSurfaces = 1024

// pipelines are what drawing primitives needs, made once per device:
// the render pass every surface draws in, the primitive pipelines
// (source-over and replace), and the descriptor plumbing.
type pipelines struct {
	pass        vk.RenderPass
	primLayout  vk.DescriptorSetLayout
	texLayout   vk.DescriptorSetLayout
	layout      vk.PipelineLayout
	over        vk.Pipeline
	replace     vk.Pipeline
	sampler     vk.Sampler
	descPool    vk.DescriptorPool
	primSet     vk.DescriptorSet
	primModules [2]vk.ShaderModule
}

func (d *Device) createPipelines() error {
	steps := []func() error{d.createPass, d.createLayouts, d.createDescriptors, d.createPrimPipelines}
	for _, step := range steps {
		if err := step(); err != nil {
			return err
		}
	}
	return nil
}

// createPass is the render pass: one color attachment kept across the
// pass, so a frame's draws land over what the surface holds.
func (d *Device) createPass() error {
	att := vk.AttachmentDescription{
		Format: surfaceFormat, Samples: vk.SampleCount1Bit,
		LoadOp: vk.AttachmentLoadOpLoad, StoreOp: vk.AttachmentStoreOpStore,
		StencilLoadOp: vk.AttachmentLoadOpDontCare, StencilStoreOp: vk.AttachmentStoreOpDontCare,
		InitialLayout: vk.ImageLayoutColorAttachmentOptimal, FinalLayout: vk.ImageLayoutColorAttachmentOptimal,
	}
	ref := vk.AttachmentReference{Attachment: 0, Layout: vk.ImageLayoutColorAttachmentOptimal}
	sub := vk.SubpassDescription{PipelineBindPoint: vk.PipelineBindPointGraphics, ColorAttachmentCount: 1, PColorAttachments: &ref}
	rci := vk.RenderPassCreateInfo{SType: vk.StructureTypeRenderPassCreateInfo, AttachmentCount: 1, PAttachments: &att, SubpassCount: 1, PSubpasses: &sub}
	if r := d.cmds.CreateRenderPass(d.device, &rci, nil, &d.pass); r != vk.Success {
		return fmt.Errorf("gpu: vkCreateRenderPass: %d", r)
	}
	return nil
}

// createLayouts makes set 0 (the frame's primitives), set 1 (the
// sampled surface) and the push constant (the target size).
func (d *Device) createLayouts() error {
	both := vk.ShaderStageFlags(vk.ShaderStageVertexBit | vk.ShaderStageFragmentBit)
	prim := [2]vk.DescriptorSetLayoutBinding{
		{Binding: 0, DescriptorType: vk.DescriptorTypeStorageBuffer, DescriptorCount: 1, StageFlags: both},
		{Binding: 1, DescriptorType: vk.DescriptorTypeStorageBuffer, DescriptorCount: 1, StageFlags: vk.ShaderStageFlags(vk.ShaderStageFragmentBit)},
	}
	pci := vk.DescriptorSetLayoutCreateInfo{SType: vk.StructureTypeDescriptorSetLayoutCreateInfo, BindingCount: 2, PBindings: &prim[0]}
	if r := d.cmds.CreateDescriptorSetLayout(d.device, &pci, nil, &d.primLayout); r != vk.Success {
		return fmt.Errorf("gpu: vkCreateDescriptorSetLayout (primitives): %d", r)
	}
	tex := vk.DescriptorSetLayoutBinding{Binding: 0, DescriptorType: vk.DescriptorTypeCombinedImageSampler, DescriptorCount: 1, StageFlags: vk.ShaderStageFlags(vk.ShaderStageFragmentBit)}
	tci := vk.DescriptorSetLayoutCreateInfo{SType: vk.StructureTypeDescriptorSetLayoutCreateInfo, BindingCount: 1, PBindings: &tex}
	if r := d.cmds.CreateDescriptorSetLayout(d.device, &tci, nil, &d.texLayout); r != vk.Success {
		return fmt.Errorf("gpu: vkCreateDescriptorSetLayout (texture): %d", r)
	}
	sets := [2]vk.DescriptorSetLayout{d.primLayout, d.texLayout}
	push := vk.PushConstantRange{StageFlags: both, Size: uint32(unsafe.Sizeof(frameConstants{}))}
	lci := vk.PipelineLayoutCreateInfo{SType: vk.StructureTypePipelineLayoutCreateInfo, SetLayoutCount: 2, PSetLayouts: &sets[0], PushConstantRangeCount: 1, PPushConstantRanges: &push}
	if r := d.cmds.CreatePipelineLayout(d.device, &lci, nil, &d.layout); r != vk.Success {
		return fmt.Errorf("gpu: vkCreatePipelineLayout: %d", r)
	}
	return nil
}

// frameConstants is the push constant block, prim.glsl's Frame.
type frameConstants struct {
	w, h float32
}

func (d *Device) createDescriptors() error {
	sci := vk.SamplerCreateInfo{
		SType: vk.StructureTypeSamplerCreateInfo, MagFilter: vk.FilterNearest, MinFilter: vk.FilterNearest,
		MipmapMode:   vk.SamplerMipmapModeNearest,
		AddressModeU: vk.SamplerAddressModeClampToEdge, AddressModeV: vk.SamplerAddressModeClampToEdge, AddressModeW: vk.SamplerAddressModeClampToEdge,
	}
	if r := d.cmds.CreateSampler(d.device, &sci, nil, &d.sampler); r != vk.Success {
		return fmt.Errorf("gpu: vkCreateSampler: %d", r)
	}
	sizes := [2]vk.DescriptorPoolSize{
		{Type: vk.DescriptorTypeStorageBuffer, DescriptorCount: 2},
		{Type: vk.DescriptorTypeCombinedImageSampler, DescriptorCount: maxSurfaces},
	}
	dci := vk.DescriptorPoolCreateInfo{
		SType: vk.StructureTypeDescriptorPoolCreateInfo, Flags: vk.DescriptorPoolCreateFlags(vk.DescriptorPoolCreateFreeDescriptorSetBit),
		MaxSets: maxSurfaces + 1, PoolSizeCount: 2, PPoolSizes: &sizes[0],
	}
	if r := d.cmds.CreateDescriptorPool(d.device, &dci, nil, &d.descPool); r != vk.Success {
		return fmt.Errorf("gpu: vkCreateDescriptorPool: %d", r)
	}
	set, err := d.allocateSet(d.primLayout)
	if err != nil {
		return err
	}
	d.primSet = set
	return nil
}

func (d *Device) allocateSet(layout vk.DescriptorSetLayout) (vk.DescriptorSet, error) {
	ai := vk.DescriptorSetAllocateInfo{SType: vk.StructureTypeDescriptorSetAllocateInfo, DescriptorPool: d.descPool, DescriptorSetCount: 1, PSetLayouts: &layout}
	var set vk.DescriptorSet
	if r := d.cmds.AllocateDescriptorSets(d.device, &ai, &set); r != vk.Success {
		return 0, fmt.Errorf("gpu: vkAllocateDescriptorSets: %d (more than %d surfaces?)", r, maxSurfaces)
	}
	return set, nil
}

// textureSet is a set 1 sampling view.
func (d *Device) textureSet(view vk.ImageView) (vk.DescriptorSet, error) {
	set, err := d.allocateSet(d.texLayout)
	if err != nil {
		return 0, err
	}
	ii := vk.DescriptorImageInfo{Sampler: d.sampler, ImageView: view, ImageLayout: vk.ImageLayoutShaderReadOnlyOptimal}
	w := vk.WriteDescriptorSet{SType: vk.StructureTypeWriteDescriptorSet, DstSet: set, DescriptorCount: 1, DescriptorType: vk.DescriptorTypeCombinedImageSampler, PImageInfo: &ii}
	d.cmds.UpdateDescriptorSets(d.device, 1, &w, 0, nil)
	return set, nil
}

// bindPrims points set 0 at the primitive buffer and the word buffer
// (gradient stops) primitives index into.
func (d *Device) bindPrims(prims, words *buffer) {
	bi := [2]vk.DescriptorBufferInfo{
		{Buffer: prims.buf, Range: vk.DeviceSize(len(prims.mapped))},
		{Buffer: words.buf, Range: vk.DeviceSize(len(words.mapped))},
	}
	w := [2]vk.WriteDescriptorSet{
		{SType: vk.StructureTypeWriteDescriptorSet, DstSet: d.primSet, DstBinding: 0, DescriptorCount: 1, DescriptorType: vk.DescriptorTypeStorageBuffer, PBufferInfo: &bi[0]},
		{SType: vk.StructureTypeWriteDescriptorSet, DstSet: d.primSet, DstBinding: 1, DescriptorCount: 1, DescriptorType: vk.DescriptorTypeStorageBuffer, PBufferInfo: &bi[1]},
	}
	d.cmds.UpdateDescriptorSets(d.device, 2, &w[0], 0, nil)
}

func (d *Device) shaderModule(spv []byte) (vk.ShaderModule, error) {
	if len(spv)%4 != 0 {
		return 0, fmt.Errorf("gpu: a SPIR-V module of %d bytes is not whole words", len(spv))
	}
	words := make([]uint32, len(spv)/4)
	copy(unsafe.Slice((*byte)(unsafe.Pointer(&words[0])), len(spv)), spv)
	ci := vk.ShaderModuleCreateInfo{SType: vk.StructureTypeShaderModuleCreateInfo, CodeSize: uintptr(len(spv)), PCode: &words[0]}
	var m vk.ShaderModule
	r := d.cmds.CreateShaderModule(d.device, &ci, nil, &m)
	runtime.KeepAlive(words)
	if r != vk.Success {
		return 0, fmt.Errorf("gpu: vkCreateShaderModule: %d", r)
	}
	return m, nil
}

// createPrimPipelines makes the primitive pipelines: over blends a
// premultiplied source over the surface, replace writes it as is.
func (d *Device) createPrimPipelines() error {
	var err error
	if d.primModules[0], err = d.shaderModule(primVert); err != nil {
		return err
	}
	if d.primModules[1], err = d.shaderModule(primFrag); err != nil {
		return err
	}
	main := cstr("main")
	stages := [2]vk.PipelineShaderStageCreateInfo{
		{SType: vk.StructureTypePipelineShaderStageCreateInfo, Stage: vk.ShaderStageVertexBit, Module: d.primModules[0], PName: uintptr(unsafe.Pointer(&main[0]))},
		{SType: vk.StructureTypePipelineShaderStageCreateInfo, Stage: vk.ShaderStageFragmentBit, Module: d.primModules[1], PName: uintptr(unsafe.Pointer(&main[0]))},
	}
	vin := vk.PipelineVertexInputStateCreateInfo{SType: vk.StructureTypePipelineVertexInputStateCreateInfo}
	asm := vk.PipelineInputAssemblyStateCreateInfo{SType: vk.StructureTypePipelineInputAssemblyStateCreateInfo, Topology: vk.PrimitiveTopologyTriangleStrip}
	vps := vk.PipelineViewportStateCreateInfo{SType: vk.StructureTypePipelineViewportStateCreateInfo, ViewportCount: 1, ScissorCount: 1}
	ras := vk.PipelineRasterizationStateCreateInfo{
		SType: vk.StructureTypePipelineRasterizationStateCreateInfo, PolygonMode: vk.PolygonModeFill,
		CullMode: vk.CullModeFlags(vk.CullModeNone), FrontFace: vk.FrontFaceCounterClockwise, LineWidth: 1,
	}
	ms := vk.PipelineMultisampleStateCreateInfo{SType: vk.StructureTypePipelineMultisampleStateCreateInfo, RasterizationSamples: vk.SampleCount1Bit}
	dyn := [2]vk.DynamicState{vk.DynamicStateViewport, vk.DynamicStateScissor}
	ds := vk.PipelineDynamicStateCreateInfo{SType: vk.StructureTypePipelineDynamicStateCreateInfo, DynamicStateCount: 2, PDynamicStates: &dyn[0]}
	rgba := vk.ColorComponentFlags(vk.ColorComponentRBit | vk.ColorComponentGBit | vk.ColorComponentBBit | vk.ColorComponentABit)
	make := func(blend vk.PipelineColorBlendAttachmentState, out *vk.Pipeline) error {
		cb := vk.PipelineColorBlendStateCreateInfo{SType: vk.StructureTypePipelineColorBlendStateCreateInfo, AttachmentCount: 1, PAttachments: &blend}
		gci := vk.GraphicsPipelineCreateInfo{
			SType: vk.StructureTypeGraphicsPipelineCreateInfo, StageCount: 2, PStages: &stages[0],
			PVertexInputState: &vin, PInputAssemblyState: &asm, PViewportState: &vps, PRasterizationState: &ras,
			PMultisampleState: &ms, PColorBlendState: &cb, PDynamicState: &ds, Layout: d.layout, RenderPass: d.pass,
		}
		if r := d.cmds.CreateGraphicsPipelines(d.device, 0, 1, &gci, nil, out); r != vk.Success {
			return fmt.Errorf("gpu: vkCreateGraphicsPipelines: %d", r)
		}
		return nil
	}
	over := vk.PipelineColorBlendAttachmentState{
		BlendEnable: vk.True, ColorWriteMask: rgba,
		SrcColorBlendFactor: vk.BlendFactorOne, DstColorBlendFactor: vk.BlendFactorOneMinusSrcAlpha, ColorBlendOp: vk.BlendOpAdd,
		SrcAlphaBlendFactor: vk.BlendFactorOne, DstAlphaBlendFactor: vk.BlendFactorOneMinusSrcAlpha, AlphaBlendOp: vk.BlendOpAdd,
	}
	if err := make(over, &d.over); err != nil {
		return err
	}
	err = make(vk.PipelineColorBlendAttachmentState{ColorWriteMask: rgba}, &d.replace)
	runtime.KeepAlive(main)
	return err
}

func (d *Device) destroyPipelines() {
	c, dev := d.cmds, d.device
	for _, p := range []vk.Pipeline{d.over, d.replace} {
		if p != 0 {
			c.DestroyPipeline(dev, p, nil)
		}
	}
	for _, m := range d.primModules {
		if m != 0 {
			c.DestroyShaderModule(dev, m, nil)
		}
	}
	if d.descPool != 0 {
		c.DestroyDescriptorPool(dev, d.descPool, nil)
	}
	if d.sampler != 0 {
		c.DestroySampler(dev, d.sampler, nil)
	}
	if d.layout != 0 {
		c.DestroyPipelineLayout(dev, d.layout, nil)
	}
	for _, l := range []vk.DescriptorSetLayout{d.primLayout, d.texLayout} {
		if l != 0 {
			c.DestroyDescriptorSetLayout(dev, l, nil)
		}
	}
	if d.pass != 0 {
		c.DestroyRenderPass(dev, d.pass, nil)
	}
}
