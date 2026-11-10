package gpu

import (
	"fmt"
	"runtime"
	"unsafe"

	"github.com/stubbedev/lys-lib/intconv"
	"github.com/stubbedev/lys-lib/internal/av1"
	"github.com/stubbedev/lys-lib/third_party/vk"
)

// grainPass synthesizes AV1 film grain into decoded pictures on the
// graphics queue, for a device that decodes only into its reference
// pictures and so applies none itself: a compute pass over the
// picture's planes, copied out of the frame and back, after its decode.
// The host makes the grain templates, the scaling functions and the
// blocks' random offsets, the sequential parts of the process, and
// stages them for the pass.
type grainPass struct {
	d         *Device
	setLayout vk.DescriptorSetLayout
	layout    vk.PipelineLayout
	pipeline  vk.Pipeline
	module    vk.ShaderModule
	descPool  vk.DescriptorPool
	set       vk.DescriptorSet
	// in and out hold a picture's planes as the pass reads and writes
	// them, rows of stride words; tables holds what the host staged.
	in, out, tables *deviceBuffer
	stride          int
	grain           av1.Grain
	constants       grainConstants
	planes          [2]vk.BufferImageCopy
	staged          vk.BufferCopy
}

// grainConstants is grain.comp's push constant block.
type grainConstants struct {
	width, height        int32
	stride, chromaAt     int32
	plane, depth, blocks int32
	scalingShift, flags  int32
	lo, hiLuma, hiChroma int32
	cb                   [3]int32
	_                    int32
	cr                   [3]int32
	_                    int32
}

// grain.comp's flags.
const (
	grainOverlap = 1 << iota
	grainLuma
	grainCb
	grainCr
	grainFromLuma
)

// grainTables are the words of an av1.Grain's templates and scaling
// functions, which its offsets follow.
const grainTables = av1.GrainLumaHeight*av1.GrainLumaWidth + 2*av1.GrainChromaHeight*av1.GrainChromaWidth + 3*256

// newGrainPass makes the pass for 4:2:0 pictures of extent with samples
// of bytes bytes.
func (d *Device) newGrainPass(extent [2]int, bytes int) (*grainPass, error) {
	g := &grainPass{d: d}
	g.stride = alignUp((extent[0]+extent[0]%2)*bytes, 4) / 4
	rows := extent[1] + (extent[1]+1)/2
	blocks := ((extent[0]+1)/2 + 15) / 16 * (((extent[1]+1)/2 + 15) / 16)
	var err error
	for _, b := range []struct {
		buf   **deviceBuffer
		words int
		usage vk.BufferUsageFlagBits
	}{
		{&g.in, g.stride * rows, vk.BufferUsageStorageBufferBit | vk.BufferUsageTransferDstBit},
		{&g.out, g.stride * rows, vk.BufferUsageStorageBufferBit | vk.BufferUsageTransferSrcBit},
		{&g.tables, grainTables + blocks, vk.BufferUsageStorageBufferBit | vk.BufferUsageTransferDstBit},
	} {
		if *b.buf, err = d.newDeviceBuffer(4*b.words, b.usage); err != nil {
			g.destroy()
			return nil, fmt.Errorf("gpu: film grain buffers for %dx%d pictures: %w", extent[0], extent[1], err)
		}
	}
	if err := g.createPipeline(); err != nil {
		g.destroy()
		return nil, err
	}
	return g, nil
}

// createPipeline makes the compute pipeline and the descriptor set of
// the three buffers.
func (g *grainPass) createPipeline() error {
	d := g.d
	comp := vk.ShaderStageFlags(vk.ShaderStageComputeBit)
	var bindings [3]vk.DescriptorSetLayoutBinding
	for i := range bindings {
		bindings[i] = vk.DescriptorSetLayoutBinding{Binding: intconv.Must[uint32](i), DescriptorType: vk.DescriptorTypeStorageBuffer, DescriptorCount: 1, StageFlags: comp}
	}
	lci := vk.DescriptorSetLayoutCreateInfo{SType: vk.StructureTypeDescriptorSetLayoutCreateInfo, BindingCount: count(bindings[:]), PBindings: &bindings[0]}
	if r := d.cmds.CreateDescriptorSetLayout(d.device, &lci, nil, &g.setLayout); r != vk.Success {
		return fmt.Errorf("gpu: vkCreateDescriptorSetLayout (film grain): %w", resultError(r))
	}
	push := vk.PushConstantRange{StageFlags: comp, Size: uint32(unsafe.Sizeof(grainConstants{}))}
	pci := vk.PipelineLayoutCreateInfo{SType: vk.StructureTypePipelineLayoutCreateInfo, SetLayoutCount: 1, PSetLayouts: &g.setLayout, PushConstantRangeCount: 1, PPushConstantRanges: &push}
	if r := d.cmds.CreatePipelineLayout(d.device, &pci, nil, &g.layout); r != vk.Success {
		return fmt.Errorf("gpu: vkCreatePipelineLayout (film grain): %w", resultError(r))
	}
	var err error
	if g.module, err = d.shaderModule(grainComp); err != nil {
		return err
	}
	var pin runtime.Pinner
	defer pin.Unpin()
	cpi := vk.ComputePipelineCreateInfo{
		SType:  vk.StructureTypeComputePipelineCreateInfo,
		Stage:  vk.PipelineShaderStageCreateInfo{SType: vk.StructureTypePipelineShaderStageCreateInfo, Stage: vk.ShaderStageComputeBit, Module: g.module, PName: cstr(&pin, "main")},
		Layout: g.layout,
	}
	if r := d.cmds.CreateComputePipelines(d.device, 0, 1, &cpi, nil, &g.pipeline); r != vk.Success {
		return fmt.Errorf("gpu: vkCreateComputePipelines (film grain): %w", resultError(r))
	}
	size := vk.DescriptorPoolSize{Type: vk.DescriptorTypeStorageBuffer, DescriptorCount: count(bindings[:])}
	dci := vk.DescriptorPoolCreateInfo{SType: vk.StructureTypeDescriptorPoolCreateInfo, MaxSets: 1, PoolSizeCount: 1, PPoolSizes: &size}
	if r := d.cmds.CreateDescriptorPool(d.device, &dci, nil, &g.descPool); r != vk.Success {
		return fmt.Errorf("gpu: vkCreateDescriptorPool (film grain): %w", resultError(r))
	}
	ai := vk.DescriptorSetAllocateInfo{SType: vk.StructureTypeDescriptorSetAllocateInfo, DescriptorPool: g.descPool, DescriptorSetCount: 1, PSetLayouts: &g.setLayout}
	if r := d.cmds.AllocateDescriptorSets(d.device, &ai, &g.set); r != vk.Success {
		return fmt.Errorf("gpu: vkAllocateDescriptorSets (film grain): %w", resultError(r))
	}
	infos := [3]vk.DescriptorBufferInfo{{Buffer: g.in.buf}, {Buffer: g.out.buf}, {Buffer: g.tables.buf}}
	var writes [3]vk.WriteDescriptorSet
	for i := range writes {
		infos[i].Range = vk.DeviceSize(vk.WholeSize)
		writes[i] = vk.WriteDescriptorSet{
			SType: vk.StructureTypeWriteDescriptorSet, DstSet: g.set, DstBinding: intconv.Must[uint32](i),
			DescriptorCount: 1, DescriptorType: vk.DescriptorTypeStorageBuffer, PBufferInfo: &infos[i],
		}
	}
	d.cmds.UpdateDescriptorSets(d.device, count(writes[:]), &writes[0], 0, nil)
	runtime.KeepAlive(&infos)
	return nil
}

// destroy releases the pass: what a decoder retires it to once the
// submissions that run it have finished.
func (g *grainPass) destroy() {
	d := g.d
	for _, b := range []*deviceBuffer{g.in, g.out, g.tables} {
		if b != nil {
			b.close()
		}
	}
	if g.descPool != 0 {
		d.cmds.DestroyDescriptorPool(d.device, g.descPool, nil)
	}
	if g.pipeline != 0 {
		d.cmds.DestroyPipeline(d.device, g.pipeline, nil)
	}
	if g.module != 0 {
		d.cmds.DestroyShaderModule(d.device, g.module, nil)
	}
	if g.layout != 0 {
		d.cmds.DestroyPipelineLayout(d.device, g.layout, nil)
	}
	if g.setLayout != 0 {
		d.cmds.DestroyDescriptorSetLayout(d.device, g.setLayout, nil)
	}
}

// grainFrame is a frame's film grain: its parameters, the size of the
// frame in luma samples, its samples' depth, and whether its matrix is
// the identity, which clips chroma as luma.
type grainFrame struct {
	params   *av1.FilmGrain
	w, h     int
	bits     int
	identity bool
}

// applyGrain synthesizes g into f's picture, whose decode is submitted,
// on the graphics queue, behind that decode and ahead of every pass
// that draws f: nothing waits here.
func (v *VideoDecoder) applyGrain(f *VideoFrame, g grainFrame) error {
	d := v.d
	if v.grain == nil {
		pass, err := d.newGrainPass(v.extent(), v.shape.samples.bytes())
		if err != nil {
			return err
		}
		v.grain = pass
	}
	p := v.grain
	g.params.Synthesize(&p.grain, g.bits, g.w, g.h)
	p.fill(g)
	t := &p.grain
	words := grainTables + len(t.Offsets)
	at, err := d.reserveUpload(4 * words)
	if err != nil {
		return err
	}
	dst := unsafe.Slice((*int32)(unsafe.Pointer(&d.upload.mapped[at])), words)
	n := copy(dst, t.Luma[:])
	n += copy(dst[n:], t.Cb[:])
	n += copy(dst[n:], t.Cr[:])
	for i := range t.Scaling {
		n += copy(dst[n:], t.Scaling[i][:])
	}
	copy(dst[n:], t.Offsets)
	cb, err := d.stagedCommands(4 * words)
	if err != nil {
		return err
	}
	d.awaitTimeline(f.decoding())
	p.record(cb, f.tex.image, at, 4*words, g, v.shape.samples.bytes())
	if err := d.flushBatch(); err != nil {
		return err
	}
	f.post = d.submitted
	return nil
}

// fill lays out the constants of g's pass.
func (p *grainPass) fill(g grainFrame) {
	f := g.params
	up := g.bits - 8
	c := &p.constants
	*c = grainConstants{
		width: intconv.Must[int32](g.w), height: intconv.Must[int32](g.h),
		stride: intconv.Must[int32](p.stride), chromaAt: intconv.Must[int32](p.stride * (g.h + g.h%2)),
		depth: intconv.Must[int32](g.bits), blocks: intconv.Must[int32](p.grain.Stride),
		scalingShift: int32(f.ScalingMinus8) + 8,
		hiLuma:       256<<up - 1, hiChroma: 256<<up - 1,
		cb: [3]int32{int32(f.CbLumaMult) - 128, int32(f.CbMult) - 128, (int32(f.CbOffset) - 256) << up},
		cr: [3]int32{int32(f.CrLumaMult) - 128, int32(f.CrMult) - 128, (int32(f.CrOffset) - 256) << up},
	}
	if f.ClipToRestrictedRange {
		c.lo, c.hiLuma, c.hiChroma = 16<<up, 235<<up, 240<<up
		if g.identity {
			c.hiChroma = c.hiLuma
		}
	}
	c.flags = intconv.Must[int32](bitfield(
		flagBit{f.OverlapFlag, grainOverlap}, flagBit{f.NumYPoints > 0, grainLuma}, flagBit{f.NumCbPoints > 0, grainCb},
		flagBit{f.NumCrPoints > 0, grainCr}, flagBit{f.ChromaScalingFromLuma, grainFromLuma},
	))
}

// record records the pass over img, a frame's picture ready to sample:
// the tables staged at at copied in, the planes copied out, the two
// dispatches (luma, then chroma, which reads the luma that went in),
// and the planes copied back. The buffers are the pass's own, so each
// part waits for the pass before it.
func (p *grainPass) record(cb vk.CommandBuffer, img vk.Image, at, bytes int, g grainFrame, sample int) {
	d, c := p.d, &p.constants
	copying, computing := vk.PipelineStageFlags2(vk.PipelineStage2CopyBit), vk.PipelineStageFlags2(vk.PipelineStage2ComputeShaderBit)
	transfer := vk.AccessFlags2(vk.Access2TransferReadBit | vk.Access2TransferWriteBit)
	storage := vk.AccessFlags2(vk.Access2ShaderStorageReadBit | vk.Access2ShaderStorageWriteBit)
	before := vk.MemoryBarrier2{
		SType:        vk.StructureTypeMemoryBarrier2,
		SrcStageMask: copying | computing, SrcAccessMask: transfer | storage,
		DstStageMask: copying, DstAccessMask: transfer,
	}
	out := imageBarrier(img, colorRange, vk.ImageLayoutShaderReadOnlyOptimal, vk.ImageLayoutTransferSrcOptimal)
	out.SrcStageMask = vk.PipelineStageFlags2(vk.PipelineStage2AllCommandsBit)
	out.DstStageMask, out.DstAccessMask = copying, vk.AccessFlags2(vk.Access2TransferReadBit)
	barrier2(d, cb, &before, out)
	p.staged = vk.BufferCopy{SrcOffset: intconv.Must[vk.DeviceSize](at), Size: intconv.Must[vk.DeviceSize](bytes)}
	d.cmds.CmdCopyBuffer(cb, d.upload.buf, p.tables.buf, 1, &p.staged)
	w, h := g.w+g.w%2, g.h+g.h%2
	row := 4 * p.stride / sample
	plane := func(aspect vk.ImageAspectFlagBits) vk.ImageSubresourceLayers {
		return vk.ImageSubresourceLayers{AspectMask: flags[vk.ImageAspectFlags](aspect), LayerCount: 1}
	}
	p.planes = [2]vk.BufferImageCopy{
		{BufferRowLength: intconv.Must[uint32](row), ImageSubresource: plane(vk.ImageAspectPlane0Bit), ImageExtent: extent3D(w, h)},
		{BufferOffset: intconv.Must[vk.DeviceSize](4 * int(c.chromaAt)), BufferRowLength: intconv.Must[uint32](row / 2), ImageSubresource: plane(vk.ImageAspectPlane1Bit), ImageExtent: extent3D(w/2, h/2)},
	}
	d.cmds.CmdCopyImageToBuffer(cb, img, vk.ImageLayoutTransferSrcOptimal, p.in.buf, 2, &p.planes[0])
	read := vk.MemoryBarrier2{
		SType:        vk.StructureTypeMemoryBarrier2,
		SrcStageMask: copying, SrcAccessMask: vk.AccessFlags2(vk.Access2TransferWriteBit),
		DstStageMask: computing, DstAccessMask: storage,
	}
	barrier2(d, cb, &read)
	d.cmds.CmdBindPipeline(cb, vk.PipelineBindPointCompute, p.pipeline)
	d.cmds.CmdBindDescriptorSets(cb, vk.PipelineBindPointCompute, p.layout, 0, 1, &p.set, 0, nil)
	columns := intconv.Must[uint32]((p.stride + 7) / 8)
	for i, rows := range [2]int{h, h / 2} {
		c.plane = intconv.Must[int32](i)
		d.cmds.CmdPushConstants(cb, p.layout, vk.ShaderStageFlags(vk.ShaderStageComputeBit), 0, uint32(unsafe.Sizeof(*c)), unsafe.Pointer(c))
		d.cmds.CmdDispatch(cb, columns, intconv.Must[uint32]((rows+7)/8), 1)
	}
	written := vk.MemoryBarrier2{
		SType:        vk.StructureTypeMemoryBarrier2,
		SrcStageMask: computing, SrcAccessMask: vk.AccessFlags2(vk.Access2ShaderStorageWriteBit),
		DstStageMask: copying, DstAccessMask: vk.AccessFlags2(vk.Access2TransferReadBit),
	}
	in := imageBarrier(img, colorRange, vk.ImageLayoutTransferSrcOptimal, vk.ImageLayoutTransferDstOptimal)
	in.SrcStageMask, in.SrcAccessMask = copying, vk.AccessFlags2(vk.Access2TransferReadBit)
	in.DstStageMask, in.DstAccessMask = copying, vk.AccessFlags2(vk.Access2TransferWriteBit)
	barrier2(d, cb, &written, in)
	d.cmds.CmdCopyBufferToImage(cb, p.out.buf, img, vk.ImageLayoutTransferDstOptimal, 2, &p.planes[0])
	sampled := imageBarrier(img, colorRange, vk.ImageLayoutTransferDstOptimal, vk.ImageLayoutShaderReadOnlyOptimal)
	sampled.SrcStageMask, sampled.SrcAccessMask = copying, vk.AccessFlags2(vk.Access2TransferWriteBit)
	sampled.DstStageMask = vk.PipelineStageFlags2(vk.PipelineStage2AllCommandsBit)
	sampled.DstAccessMask = vk.AccessFlags2(vk.Access2ShaderSampledReadBit | vk.Access2TransferReadBit)
	barrier2(d, cb, nil, sampled)
}
