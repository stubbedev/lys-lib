package gpu

import (
	"errors"
	"fmt"
	"runtime"
	"slices"
	"unsafe"

	"github.com/stubbedev/lys-lib/third_party/vk"
)

// vulkanBackend encodes on the device's Vulkan video queue: the
// conversion writes one shared plane buffer, copies it into the slot's
// source picture, and the video queue encodes that into the slot's
// share of the bitstream buffer.
type vulkanBackend struct {
	d         *Device
	cfg       EncoderConfig
	coded     [2]int
	codec     videoCodec
	profile   *videoProfile
	caps      *videoCaps
	cqp       bool
	qp        int
	paramSets [][]byte

	session    vk.VideoSessionKHR
	sessionMem []vk.DeviceMemory
	sessionPar vk.VideoSessionParametersKHR
	dpb        videoImage
	rc         *rateControl
	rcSet      bool

	planeBuf  *deviceBuffer
	bitstream *buffer
	slotSize  int
	queries   vk.QueryPool
	pool      vk.CommandPool
	slots     [encodeSlots]vulkanSlot
}

// vulkanSlot is a frame in flight on the video queue: its source
// picture, its commands, the semaphore its conversion signals and the
// fence its encode does, and where in the bitstream buffer it writes.
type vulkanSlot struct {
	src    videoImage
	cb     vk.CommandBuffer
	ready  vk.SemaphoreSubmitInfo
	done   vk.Fence
	offset int
}

// newVulkanBackend makes the video session, its pictures and buffers
// for cfg.
func newVulkanBackend(d *Device, cfg EncoderConfig) (*vulkanBackend, error) {
	vb := &vulkanBackend{d: d, cfg: cfg, codec: newVideoCodec(cfg.Codec)}
	steps := []func() error{vb.pickProfile, vb.createSession, vb.createParameters, vb.createImages, vb.createBuffers, vb.createCommands}
	for _, step := range steps {
		if err := step(); err != nil {
			vb.close()
			return nil, err
		}
	}
	return vb, nil
}

func (vb *vulkanBackend) codedSize() [2]int { return vb.coded }

func (vb *vulkanBackend) parameterSets() [][]byte { return vb.paramSets }

// planeBytes is the size of the coded picture's two planes, tight.
func (vb *vulkanBackend) planeBytes() int { return vb.coded[0] * vb.coded[1] * 3 / 2 }

func (vb *vulkanBackend) planes(int) (vk.Buffer, int, int, int) {
	return vb.planeBuf.buf, vb.planeBytes(), vb.coded[0], vb.coded[0] * vb.coded[1]
}

// createCommands makes the video queue's command buffers, and each
// slot's semaphore and fence.
func (vb *vulkanBackend) createCommands() error {
	d := vb.d
	var err error
	if vb.pool, err = d.commandPool(d.video.family); err != nil {
		return err
	}
	for i := range vb.slots {
		s := &vb.slots[i]
		if s.cb, err = d.commandBuffer(vb.pool); err != nil {
			return err
		}
		if s.done, err = d.fence(); err != nil {
			return err
		}
		s.ready = vk.SemaphoreSubmitInfo{SType: vk.StructureTypeSemaphoreSubmitInfo, StageMask: vk.PipelineStageFlags2(vk.PipelineStage2AllCommandsBit)}
		sci := vk.SemaphoreCreateInfo{SType: vk.StructureTypeSemaphoreCreateInfo}
		if r := d.cmds.CreateSemaphore(d.device, &sci, nil, &s.ready.Semaphore); r != vk.Success {
			return fmt.Errorf("gpu: vkCreateSemaphore: %d", r)
		}
	}
	return nil
}

func (vb *vulkanBackend) beforeConvert(vk.CommandBuffer, int) {}

// afterConvert copies the planes into slot's source picture and
// signals its semaphore for the video queue.
func (vb *vulkanBackend) afterConvert(cb vk.CommandBuffer, slot int) *vk.SemaphoreSubmitInfo {
	d, s := vb.d, &vb.slots[slot]
	planes := vk.MemoryBarrier2{
		SType:        vk.StructureTypeMemoryBarrier2,
		SrcStageMask: vk.PipelineStageFlags2(vk.PipelineStage2ComputeShaderBit), SrcAccessMask: vk.AccessFlags2(vk.Access2ShaderStorageWriteBit),
		DstStageMask: vk.PipelineStageFlags2(vk.PipelineStage2CopyBit), DstAccessMask: vk.AccessFlags2(vk.Access2TransferReadBit),
	}
	all := vk.ImageSubresourceRange{AspectMask: vk.ImageAspectFlags(vk.ImageAspectColorBit), LevelCount: 1, LayerCount: 1}
	toDst := imageBarrier(s.src.image, all, vk.ImageLayoutUndefined, vk.ImageLayoutTransferDstOptimal)
	toDst.DstStageMask, toDst.DstAccessMask = vk.PipelineStageFlags2(vk.PipelineStage2CopyBit), vk.AccessFlags2(vk.Access2TransferWriteBit)
	barrier2(d, cb, &planes, toDst)
	cw, ch := uint32(vb.coded[0]), uint32(vb.coded[1])
	regions := [2]vk.BufferImageCopy{
		{BufferRowLength: cw, BufferImageHeight: ch, ImageSubresource: vk.ImageSubresourceLayers{AspectMask: vk.ImageAspectFlags(vk.ImageAspectPlane0Bit), LayerCount: 1}, ImageExtent: vk.Extent3D{Width: cw, Height: ch, Depth: 1}},
		{BufferOffset: vk.DeviceSize(cw * ch), BufferRowLength: cw / 2, BufferImageHeight: ch / 2, ImageSubresource: vk.ImageSubresourceLayers{AspectMask: vk.ImageAspectFlags(vk.ImageAspectPlane1Bit), LayerCount: 1}, ImageExtent: vk.Extent3D{Width: cw / 2, Height: ch / 2, Depth: 1}},
	}
	d.cmds.CmdCopyBufferToImage(cb, vb.planeBuf.buf, s.src.image, vk.ImageLayoutTransferDstOptimal, 2, &regions[0])
	toSrc := imageBarrier(s.src.image, all, vk.ImageLayoutTransferDstOptimal, vk.ImageLayoutVideoEncodeSrcKhr)
	toSrc.SrcStageMask, toSrc.SrcAccessMask = vk.PipelineStageFlags2(vk.PipelineStage2CopyBit), vk.AccessFlags2(vk.Access2TransferWriteBit)
	barrier2(d, cb, nil, toSrc)
	return &s.ready
}

// collect waits for slot's encode and reads its packet out of the
// bitstream buffer, where the feedback query says it is.
func (vb *vulkanBackend) collect(slot int) ([]byte, error) {
	d, s := vb.d, &vb.slots[slot]
	if r := d.cmds.WaitForFences(d.device, 1, &s.done, vk.True, ^uint64(0)); r != vk.Success {
		return nil, fmt.Errorf("wait for an encode: %d", r)
	}
	var res [3]uint32
	flags := vk.QueryResultFlags(vk.QueryResultWithStatusBitKhr)
	if r := d.cmds.GetQueryPoolResults(d.device, vb.queries, uint32(slot), 1, unsafe.Sizeof(res), unsafe.Pointer(&res[0]), vk.DeviceSize(unsafe.Sizeof(res)), flags); r != vk.Success {
		return nil, fmt.Errorf("vkGetQueryPoolResults (encode feedback): %d", r)
	}
	if status := vk.QueryResultStatusKHR(int32(res[2])); status != vk.QueryResultStatusCompleteKhr {
		return nil, fmt.Errorf("did not encode: status %d", status)
	}
	offset, n := int(res[0]), int(res[1])
	if offset+n > vb.slotSize {
		return nil, fmt.Errorf("the device wrote %d bytes at %d into a %d-byte bitstream slot", n, offset, vb.slotSize)
	}
	start := s.offset + offset
	if err := vb.bitstream.invalidate(); err != nil {
		return nil, err
	}
	return slices.Clone(vb.bitstream.mapped[start : start+n]), nil
}

func (vb *vulkanBackend) close() {
	d := vb.d
	for i := range vb.slots {
		s := &vb.slots[i]
		s.src.close(d)
		if s.ready.Semaphore != 0 {
			d.cmds.DestroySemaphore(d.device, s.ready.Semaphore, nil)
		}
		if s.done != 0 {
			d.cmds.DestroyFence(d.device, s.done, nil)
		}
	}
	vb.dpb.close(d)
	if vb.pool != 0 {
		d.cmds.DestroyCommandPool(d.device, vb.pool, nil)
	}
	if vb.queries != 0 {
		d.cmds.DestroyQueryPool(d.device, vb.queries, nil)
	}
	if vb.bitstream != nil {
		vb.bitstream.close()
	}
	if vb.planeBuf != nil {
		vb.planeBuf.close()
	}
	if vb.sessionPar != 0 {
		d.cmds.DestroyVideoSessionParametersKHR(d.device, vb.sessionPar, nil)
	}
	if vb.session != 0 {
		d.cmds.DestroyVideoSessionKHR(d.device, vb.session, nil)
	}
	for _, m := range vb.sessionMem {
		d.cmds.FreeMemory(d.device, m, nil)
	}
}

// dpbSlots are the reference pictures: the one being reconstructed and
// the one it predicts from.
const dpbSlots = 2

// nv12 is the two-plane 4:2:0 format every picture is in.
const nv12 = vk.FormatG8B8r82plane420Unorm

// videoCodec is what an encoder does differently per codec: the
// profile and capability structures it chains into the generic ones,
// its parameter sets, its part of the rate control and each frame's
// picture state. The structures live in it, for the pointers into them.
type videoCodec interface {
	// profiles is how many profiles the codec tries, best first.
	profiles() int
	// profile lays out profile i over next (the usage hints).
	profile(i int, next unsafe.Pointer) unsafe.Pointer
	// caps is the codec's capability structure, chained last.
	caps() unsafe.Pointer
	// limits reads the capabilities the query filled in.
	limits() codecLimits
	// align is the coded size's unit beyond the device's granularity.
	align() int
	// parameters lays out the parameter sets for cfg coded at coded:
	// the session parameters' create and get chains, and how many NAL
	// units the device writes for them.
	parameters(cfg EncoderConfig, coded [2]int) (create, get unsafe.Pointer, nals int, err error)
	// rateControl is the codec's rate control structure for cfg.
	rateControl(cfg EncoderConfig) unsafe.Pointer
	// picture lays out frame sinceIDR of its key interval, numbered
	// idrs among IDRs, predicting from DPB slot prev: its picture info,
	// and the DPB slot infos of the picture it reconstructs and the one
	// it references.
	picture(sinceIDR, idrs, prev, qp int) (info, setup, ref unsafe.Pointer)
}

// codecLimits are a codec's capabilities the encoder heeds.
type codecLimits struct {
	minQP, maxQP int
	// pRefs is how many references a P frame may predict from.
	pRefs int
}

func newVideoCodec(c Codec) videoCodec {
	if c == H265 {
		return &h265Codec{}
	}
	return &h264Codec{}
}

// videoProfile is the profile chain every video object is made with,
// kept on the heap for the pointers into it.
type videoProfile struct {
	usage vk.VideoEncodeUsageInfoKHR
	info  vk.VideoProfileInfoKHR
	list  vk.VideoProfileListInfoKHR
}

// newVideoProfile is codec's profile i.
func newVideoProfile(c Codec, codec videoCodec, i int) *videoProfile {
	p := &videoProfile{}
	p.usage = vk.VideoEncodeUsageInfoKHR{
		SType:             vk.StructureTypeVideoEncodeUsageInfoKhr,
		VideoUsageHints:   vk.VideoEncodeUsageFlagsKHR(vk.VideoEncodeUsageRecordingBitKhr),
		VideoContentHints: vk.VideoEncodeContentFlagsKHR(vk.VideoEncodeContentDesktopBitKhr),
		TuningMode:        vk.VideoEncodeTuningModeHighQualityKhr,
	}
	p.info = vk.VideoProfileInfoKHR{
		SType: vk.StructureTypeVideoProfileInfoKhr, PNext: (*uintptr)(codec.profile(i, unsafe.Pointer(&p.usage))), VideoCodecOperation: c.operation(),
		ChromaSubsampling: vk.VideoChromaSubsamplingFlagsKHR(vk.VideoChromaSubsampling420BitKhr),
		LumaBitDepth:      vk.VideoComponentBitDepthFlagsKHR(vk.VideoComponentBitDepth8BitKhr),
		ChromaBitDepth:    vk.VideoComponentBitDepthFlagsKHR(vk.VideoComponentBitDepth8BitKhr),
	}
	p.list = vk.VideoProfileListInfoKHR{SType: vk.StructureTypeVideoProfileListInfoKhr, ProfileCount: 1, PProfiles: &p.info}
	return p
}

// videoCaps are what the device encodes in a profile; the codec's own
// part is in the codec.
type videoCaps struct {
	video  vk.VideoCapabilitiesKHR
	encode vk.VideoEncodeCapabilitiesKHR
}

func (d *Device) videoCaps(p *videoProfile, codec videoCodec) (*videoCaps, vk.Result) {
	c := &videoCaps{}
	c.encode = vk.VideoEncodeCapabilitiesKHR{SType: vk.StructureTypeVideoEncodeCapabilitiesKhr, PNext: (*uintptr)(codec.caps())}
	c.video = vk.VideoCapabilitiesKHR{SType: vk.StructureTypeVideoCapabilitiesKhr, PNext: (*uintptr)(unsafe.Pointer(&c.encode))}
	r := d.cmds.GetPhysicalDeviceVideoCapabilitiesKHR(d.phys, &p.info, &c.video)
	runtime.KeepAlive(p)
	runtime.KeepAlive(codec)
	return c, r
}

// videoImage is an NV12 picture: its image, memory and the views the
// coding commands name it by (one per array layer).
type videoImage struct {
	image  vk.Image
	memory vk.DeviceMemory
	views  []vk.ImageView
}

// pickProfile takes the codec's best profile the device encodes, with
// its capabilities, and sizes the coded picture to them.
func (vb *vulkanBackend) pickProfile() error {
	d := vb.d
	var last vk.Result
	for i := range vb.codec.profiles() {
		p := newVideoProfile(vb.cfg.Codec, vb.codec, i)
		caps, r := d.videoCaps(p, vb.codec)
		if r != vk.Success {
			last = r
			continue
		}
		vb.profile, vb.caps = p, caps
		break
	}
	if vb.profile == nil {
		return fmt.Errorf("%w: %s encodes none of the %s profiles: %d", ErrNoVideoEncoder, d.name, vb.cfg.Codec, last)
	}
	c, lim := vb.caps, vb.codec.limits()
	if c.video.MaxDpbSlots < dpbSlots || c.video.MaxActiveReferencePictures < 1 || lim.pRefs < 1 {
		return fmt.Errorf("%w: %s codes no %s P frames (%d DPB slots, %d references)", ErrNoVideoEncoder, d.name, vb.cfg.Codec, c.video.MaxDpbSlots, lim.pRefs)
	}
	unit := vb.codec.align()
	gw := max(unit, int(c.video.PictureAccessGranularity.Width), int(c.encode.EncodeInputPictureGranularity.Width))
	gh := max(unit, int(c.video.PictureAccessGranularity.Height), int(c.encode.EncodeInputPictureGranularity.Height))
	minE, maxE := c.video.MinCodedExtent, c.video.MaxCodedExtent
	vb.coded = [2]int{
		alignUp(max(evenUp(vb.cfg.Width), int(minE.Width)), gw),
		alignUp(max(evenUp(vb.cfg.Height), int(minE.Height)), gh),
	}
	if vb.coded[0] > int(maxE.Width) || vb.coded[1] > int(maxE.Height) {
		return fmt.Errorf("gpu: %s encodes up to %dx%d, not %dx%d", d.name, maxE.Width, maxE.Height, vb.coded[0], vb.coded[1])
	}
	vb.cqp = c.encode.RateControlModes&vk.VideoEncodeRateControlModeFlagsKHR(vk.VideoEncodeRateControlModeDisabledBitKhr) != 0
	if vb.cqp {
		vb.qp = min(max(vb.cfg.QP, lim.minQP), lim.maxQP)
	}
	vb.rc = newRateControl(vb.cfg, vb.cqp, c, vb.codec.rateControl(vb.cfg))
	return nil
}

// createSession makes the video session and binds its memory.
func (vb *vulkanBackend) createSession() error {
	d := vb.d
	sci := vk.VideoSessionCreateInfoKHR{
		SType: vk.StructureTypeVideoSessionCreateInfoKhr, QueueFamilyIndex: d.video.family, PVideoProfile: &vb.profile.info,
		PictureFormat: nv12, MaxCodedExtent: vk.Extent2D{Width: uint32(vb.coded[0]), Height: uint32(vb.coded[1])},
		ReferencePictureFormat: nv12, MaxDpbSlots: dpbSlots, MaxActiveReferencePictures: 1,
		PStdHeaderVersion: &vb.caps.video.StdHeaderVersion,
	}
	r := d.cmds.CreateVideoSessionKHR(d.device, &sci, nil, &vb.session)
	runtime.KeepAlive(vb.profile)
	if r != vk.Success {
		return fmt.Errorf("gpu: vkCreateVideoSessionKHR %dx%d: %d", vb.coded[0], vb.coded[1], r)
	}
	var n uint32
	if r := d.cmds.GetVideoSessionMemoryRequirementsKHR(d.device, vb.session, &n, nil); r != vk.Success {
		return fmt.Errorf("gpu: vkGetVideoSessionMemoryRequirementsKHR: %d", r)
	}
	reqs := make([]vk.VideoSessionMemoryRequirementsKHR, n)
	for i := range reqs {
		reqs[i].SType = vk.StructureTypeVideoSessionMemoryRequirementsKhr
	}
	if n > 0 {
		if r := d.cmds.GetVideoSessionMemoryRequirementsKHR(d.device, vb.session, &n, &reqs[0]); r != vk.Success {
			return fmt.Errorf("gpu: vkGetVideoSessionMemoryRequirementsKHR: %d", r)
		}
	}
	binds := make([]vk.BindVideoSessionMemoryInfoKHR, 0, n)
	for _, req := range reqs[:n] {
		mem, err := d.allocate(req.MemoryRequirements, vk.MemoryPropertyDeviceLocalBit, nil)
		if errors.Is(err, errNoMemoryType) {
			mem, err = d.allocate(req.MemoryRequirements, 0, nil)
		}
		if err != nil {
			return fmt.Errorf("gpu: video session memory %d: %w", req.MemoryBindIndex, err)
		}
		vb.sessionMem = append(vb.sessionMem, mem)
		binds = append(binds, vk.BindVideoSessionMemoryInfoKHR{
			SType: vk.StructureTypeBindVideoSessionMemoryInfoKhr, MemoryBindIndex: req.MemoryBindIndex, Memory: mem, MemorySize: req.MemoryRequirements.Size,
		})
	}
	if len(binds) > 0 {
		if r := d.cmds.BindVideoSessionMemoryKHR(d.device, vb.session, uint32(len(binds)), &binds[0]); r != vk.Success {
			return fmt.Errorf("gpu: vkBindVideoSessionMemoryKHR: %d", r)
		}
	}
	return nil
}

// createParameters makes the session parameters and reads back the
// NAL units the device writes for them.
func (vb *vulkanBackend) createParameters() error {
	d := vb.d
	create, getNext, nals, err := vb.codec.parameters(vb.cfg, vb.coded)
	if err != nil {
		return err
	}
	pci := vk.VideoSessionParametersCreateInfoKHR{SType: vk.StructureTypeVideoSessionParametersCreateInfoKhr, PNext: (*uintptr)(create), VideoSession: vb.session}
	r := d.cmds.CreateVideoSessionParametersKHR(d.device, &pci, nil, &vb.sessionPar)
	runtime.KeepAlive(vb.codec)
	if r != vk.Success {
		return fmt.Errorf("gpu: vkCreateVideoSessionParametersKHR: %d", r)
	}
	get := vk.VideoEncodeSessionParametersGetInfoKHR{SType: vk.StructureTypeVideoEncodeSessionParametersGetInfoKhr, PNext: (*uintptr)(getNext), VideoSessionParameters: vb.sessionPar}
	fb := vk.VideoEncodeSessionParametersFeedbackInfoKHR{SType: vk.StructureTypeVideoEncodeSessionParametersFeedbackInfoKhr}
	var size uintptr
	if r := d.cmds.GetEncodedVideoSessionParametersKHR(d.device, &get, &fb, &size, nil); r != vk.Success {
		return fmt.Errorf("gpu: vkGetEncodedVideoSessionParametersKHR (size): %d", r)
	}
	data := make([]byte, size)
	if size > 0 {
		if r := d.cmds.GetEncodedVideoSessionParametersKHR(d.device, &get, &fb, &size, (*uintptr)(unsafe.Pointer(&data[0]))); r != vk.Success {
			return fmt.Errorf("gpu: vkGetEncodedVideoSessionParametersKHR: %d", r)
		}
	}
	runtime.KeepAlive(vb.codec)
	vb.paramSets = nalUnits(data[:size])
	if len(vb.paramSets) != nals {
		return fmt.Errorf("gpu: the device wrote %d %s parameter set NAL units, want %d", len(vb.paramSets), vb.cfg.Codec, nals)
	}
	return nil
}

// videoFormat finds NV12's properties among the formats the device
// codes pictures of usage in.
func (vb *vulkanBackend) videoFormat(usage vk.ImageUsageFlags) (vk.VideoFormatPropertiesKHR, error) {
	d := vb.d
	fi := vk.PhysicalDeviceVideoFormatInfoKHR{SType: vk.StructureTypePhysicalDeviceVideoFormatInfoKhr, PNext: (*uintptr)(unsafe.Pointer(&vb.profile.list)), ImageUsage: usage}
	var n uint32
	if r := d.cmds.GetPhysicalDeviceVideoFormatPropertiesKHR(d.phys, &fi, &n, nil); r != vk.Success {
		return vk.VideoFormatPropertiesKHR{}, fmt.Errorf("gpu: vkGetPhysicalDeviceVideoFormatPropertiesKHR: %d", r)
	}
	props := make([]vk.VideoFormatPropertiesKHR, n)
	for i := range props {
		props[i].SType = vk.StructureTypeVideoFormatPropertiesKhr
	}
	if n > 0 {
		if r := d.cmds.GetPhysicalDeviceVideoFormatPropertiesKHR(d.phys, &fi, &n, &props[0]); r != vk.Success {
			return vk.VideoFormatPropertiesKHR{}, fmt.Errorf("gpu: vkGetPhysicalDeviceVideoFormatPropertiesKHR: %d", r)
		}
	}
	runtime.KeepAlive(vb.profile)
	for _, p := range props[:n] {
		if p.Format == nv12 && p.ImageType == vk.ImageType2d {
			return p, nil
		}
	}
	return vk.VideoFormatPropertiesKHR{}, fmt.Errorf("%w: %s codes no NV12 pictures for usage %#x", ErrNoVideoEncoder, d.name, usage)
}

// createImages makes each slot's source picture and the DPB.
func (vb *vulkanBackend) createImages() error {
	d := vb.d
	srcUsage := vk.ImageUsageFlags(vk.ImageUsageVideoEncodeSrcBitKhr | vk.ImageUsageTransferDstBit)
	srcFormat, err := vb.videoFormat(srcUsage)
	if err != nil {
		return err
	}
	dpbUsage := vk.ImageUsageFlags(vk.ImageUsageVideoEncodeDpbBitKhr)
	dpbFormat, err := vb.videoFormat(dpbUsage)
	if err != nil {
		return err
	}
	families := []uint32{d.family, d.video.family}
	for i := range vb.slots {
		if err := vb.newVideoImage(&vb.slots[i].src, srcFormat, srcUsage, 1, families); err != nil {
			return fmt.Errorf("gpu: encoder source picture: %w", err)
		}
	}
	if err := vb.newVideoImage(&vb.dpb, dpbFormat, dpbUsage, dpbSlots, nil); err != nil {
		return fmt.Errorf("gpu: encoder DPB: %w", err)
	}
	return nil
}

// newVideoImage makes an NV12 picture of the coded size with layers
// array layers in format's tiling, shared between families when there
// are two different ones, and a view of each layer.
func (vb *vulkanBackend) newVideoImage(vi *videoImage, format vk.VideoFormatPropertiesKHR, usage vk.ImageUsageFlags, layers int, families []uint32) error {
	d := vb.d
	ici := vk.ImageCreateInfo{
		SType: vk.StructureTypeImageCreateInfo, PNext: (*uintptr)(unsafe.Pointer(&vb.profile.list)), Flags: format.ImageCreateFlags,
		ImageType: vk.ImageType2d, Format: nv12, Extent: vk.Extent3D{Width: uint32(vb.coded[0]), Height: uint32(vb.coded[1]), Depth: 1},
		MipLevels: 1, ArrayLayers: uint32(layers), Samples: vk.SampleCount1Bit, Tiling: format.ImageTiling,
		Usage: usage, SharingMode: vk.SharingModeExclusive, InitialLayout: vk.ImageLayoutUndefined,
	}
	if len(families) == 2 && families[0] != families[1] {
		ici.SharingMode, ici.QueueFamilyIndexCount, ici.PQueueFamilyIndices = vk.SharingModeConcurrent, 2, &families[0]
	}
	r := d.cmds.CreateImage(d.device, &ici, nil, &vi.image)
	runtime.KeepAlive(vb.profile)
	runtime.KeepAlive(families)
	if r != vk.Success {
		return fmt.Errorf("vkCreateImage NV12 %dx%d x%d: %d", vb.coded[0], vb.coded[1], layers, r)
	}
	var req vk.MemoryRequirements
	d.cmds.GetImageMemoryRequirements(d.device, vi.image, &req)
	mem, err := d.allocate(req, vk.MemoryPropertyDeviceLocalBit, nil)
	if err != nil {
		return err
	}
	vi.memory = mem
	if r := d.cmds.BindImageMemory(d.device, vi.image, vi.memory, 0); r != vk.Success {
		return fmt.Errorf("vkBindImageMemory: %d", r)
	}
	for l := range layers {
		vci := vk.ImageViewCreateInfo{
			SType: vk.StructureTypeImageViewCreateInfo, Image: vi.image, ViewType: vk.ImageViewType2d, Format: nv12,
			SubresourceRange: vk.ImageSubresourceRange{AspectMask: vk.ImageAspectFlags(vk.ImageAspectColorBit), LevelCount: 1, BaseArrayLayer: uint32(l), LayerCount: 1},
		}
		var v vk.ImageView
		if r := d.cmds.CreateImageView(d.device, &vci, nil, &v); r != vk.Success {
			return fmt.Errorf("vkCreateImageView NV12 layer %d: %d", l, r)
		}
		vi.views = append(vi.views, v)
	}
	return nil
}

func (vi *videoImage) close(d *Device) {
	for _, v := range vi.views {
		d.cmds.DestroyImageView(d.device, v, nil)
	}
	vi.views = nil
	if vi.image != 0 {
		d.cmds.DestroyImage(d.device, vi.image, nil)
		vi.image = 0
	}
	if vi.memory != 0 {
		d.cmds.FreeMemory(d.device, vi.memory, nil)
		vi.memory = 0
	}
}

// createBuffers makes the conversion's plane buffer, the bitstream
// buffer each slot writes its share of, and the feedback queries.
func (vb *vulkanBackend) createBuffers() error {
	d := vb.d
	planes, err := d.newDeviceBuffer(vb.planeBytes(), vk.BufferUsageStorageBufferBit|vk.BufferUsageTransferSrcBit)
	if err != nil {
		return fmt.Errorf("gpu: encoder planes: %w", err)
	}
	vb.planeBuf = planes
	c := vb.caps.video
	vb.slotSize = alignUp(alignUp(bitstreamBytes(vb.coded), int(max(c.MinBitstreamBufferSizeAlignment, 1))), int(max(c.MinBitstreamBufferOffsetAlignment, 1)))
	bs, err := d.newReadBuffer(vb.slotSize*encodeSlots, vk.BufferUsageVideoEncodeDstBitKhr, unsafe.Pointer(&vb.profile.list))
	runtime.KeepAlive(vb.profile)
	if err != nil {
		return fmt.Errorf("gpu: encoder bitstream: %w", err)
	}
	vb.bitstream = bs
	for i := range vb.slots {
		vb.slots[i].offset = i * vb.slotSize
	}
	feedback := vk.QueryPoolVideoEncodeFeedbackCreateInfoKHR{
		SType: vk.StructureTypeQueryPoolVideoEncodeFeedbackCreateInfoKhr, PNext: (*uintptr)(unsafe.Pointer(&vb.profile.info)),
		EncodeFeedbackFlags: vk.VideoEncodeFeedbackFlagsKHR(vk.VideoEncodeFeedbackBitstreamBufferOffsetBitKhr | vk.VideoEncodeFeedbackBitstreamBytesWrittenBitKhr),
	}
	qci := vk.QueryPoolCreateInfo{SType: vk.StructureTypeQueryPoolCreateInfo, PNext: (*uintptr)(unsafe.Pointer(&feedback)), QueryType: vk.QueryTypeVideoEncodeFeedbackKhr, QueryCount: encodeSlots}
	r := d.cmds.CreateQueryPool(d.device, &qci, nil, &vb.queries)
	runtime.KeepAlive(&feedback)
	runtime.KeepAlive(vb.profile)
	if r != vk.Success {
		return fmt.Errorf("gpu: vkCreateQueryPool (encode feedback): %d", r)
	}
	return nil
}

// encode records and submits encoding slot's source picture, after
// its semaphore.
func (vb *vulkanBackend) encode(slot, frame, sinceIDR, idrs int) error {
	d, s := vb.d, &vb.slots[slot]
	cb := s.cb
	if err := begin(d, cb); err != nil {
		return err
	}
	q := uint32(slot)
	d.cmds.CmdResetQueryPool(cb, vb.queries, q, 1)
	dpbAll := vk.ImageSubresourceRange{AspectMask: vk.ImageAspectFlags(vk.ImageAspectColorBit), LevelCount: 1, LayerCount: dpbSlots}
	coding := vk.PipelineStageFlags2(vk.PipelineStage2VideoEncodeBitKhr)
	if frame == 0 {
		b := imageBarrier(vb.dpb.image, dpbAll, vk.ImageLayoutUndefined, vk.ImageLayoutVideoEncodeDpbKhr)
		b.DstStageMask, b.DstAccessMask = coding, vk.AccessFlags2(vk.Access2VideoEncodeReadBitKhr|vk.Access2VideoEncodeWriteBitKhr)
		barrier2(d, cb, nil, b)
	} else {
		m := vk.MemoryBarrier2{
			SType:        vk.StructureTypeMemoryBarrier2,
			SrcStageMask: coding, SrcAccessMask: vk.AccessFlags2(vk.Access2VideoEncodeWriteBitKhr),
			DstStageMask: coding, DstAccessMask: vk.AccessFlags2(vk.Access2VideoEncodeReadBitKhr | vk.Access2VideoEncodeWriteBitKhr),
		}
		barrier2(d, cb, &m)
	}
	cur, prev := frame%dpbSlots, (frame+dpbSlots-1)%dpbSlots
	picInfo, setupInfo, refInfo := vb.codec.picture(sinceIDR, idrs, prev, vb.qp)
	extent := vk.Extent2D{Width: uint32(vb.coded[0]), Height: uint32(vb.coded[1])}
	res := [3]vk.VideoPictureResourceInfoKHR{
		{SType: vk.StructureTypeVideoPictureResourceInfoKhr, CodedExtent: extent, ImageViewBinding: s.src.views[0]},
		{SType: vk.StructureTypeVideoPictureResourceInfoKhr, CodedExtent: extent, ImageViewBinding: vb.dpb.views[cur]},
		{SType: vk.StructureTypeVideoPictureResourceInfoKhr, CodedExtent: extent, ImageViewBinding: vb.dpb.views[prev]},
	}
	refs := 0
	if sinceIDR > 0 {
		refs = 1
	}
	bound := [2]vk.VideoReferenceSlotInfoKHR{
		{SType: vk.StructureTypeVideoReferenceSlotInfoKhr, SlotIndex: -1, PPictureResource: &res[1]},
		{SType: vk.StructureTypeVideoReferenceSlotInfoKhr, SlotIndex: int32(prev), PPictureResource: &res[2]},
	}
	bi := vk.VideoBeginCodingInfoKHR{
		SType: vk.StructureTypeVideoBeginCodingInfoKhr, VideoSession: vb.session, VideoSessionParameters: vb.sessionPar,
		ReferenceSlotCount: uint32(1 + refs), PReferenceSlots: &bound[0],
	}
	if vb.rcSet {
		bi.PNext = vb.rc.chain()
	}
	d.cmds.CmdBeginVideoCodingKHR(cb, &bi)
	if !vb.rcSet {
		ctl := vk.VideoCodingControlInfoKHR{
			SType: vk.StructureTypeVideoCodingControlInfoKhr, PNext: vb.rc.chain(),
			Flags: vk.VideoCodingControlFlagsKHR(vk.VideoCodingControlResetBitKhr | vk.VideoCodingControlEncodeRateControlBitKhr),
		}
		d.cmds.CmdControlVideoCodingKHR(cb, &ctl)
		vb.rcSet = true
	}
	setup := vk.VideoReferenceSlotInfoKHR{SType: vk.StructureTypeVideoReferenceSlotInfoKhr, PNext: (*uintptr)(setupInfo), SlotIndex: int32(cur), PPictureResource: &res[1]}
	ref := vk.VideoReferenceSlotInfoKHR{SType: vk.StructureTypeVideoReferenceSlotInfoKhr, PNext: (*uintptr)(refInfo), SlotIndex: int32(prev), PPictureResource: &res[2]}
	ei := vk.VideoEncodeInfoKHR{
		SType: vk.StructureTypeVideoEncodeInfoKhr, PNext: (*uintptr)(picInfo),
		DstBuffer: vb.bitstream.buf, DstBufferOffset: vk.DeviceSize(s.offset), DstBufferRange: vk.DeviceSize(vb.slotSize),
		SrcPictureResource: res[0], PSetupReferenceSlot: &setup, ReferenceSlotCount: uint32(refs), PReferenceSlots: &ref,
	}
	d.cmds.CmdBeginQuery(cb, vb.queries, q, 0)
	d.cmds.CmdEncodeVideoKHR(cb, &ei)
	d.cmds.CmdEndQuery(cb, vb.queries, q)
	end := vk.VideoEndCodingInfoKHR{SType: vk.StructureTypeVideoEndCodingInfoKhr}
	d.cmds.CmdEndVideoCodingKHR(cb, &end)
	runtime.KeepAlive(vb.codec)
	runtime.KeepAlive(&res)
	runtime.KeepAlive(&bound)
	if r := d.cmds.EndCommandBuffer(cb); r != vk.Success {
		return fmt.Errorf("gpu: vkEndCommandBuffer (encode): %d", r)
	}
	if err := resetFence(d, s.done); err != nil {
		return err
	}
	wait := vk.SemaphoreSubmitInfo{SType: vk.StructureTypeSemaphoreSubmitInfo, Semaphore: s.ready.Semaphore, StageMask: coding}
	return submit2(d, d.video.queue, cb, &wait, nil, s.done)
}

// rateControl is the rate control state the session codes at:
// constant QP where the device has it, else variable bitrate around a
// bitrate that matches QP 20 on desktop content. The GOP is flat: an
// IDR every key interval, P frames between.
type rateControl struct {
	layer vk.VideoEncodeRateControlLayerInfoKHR
	info  vk.VideoEncodeRateControlInfoKHR
}

// newRateControl is cfg's rate control with the codec's part, next.
func newRateControl(cfg EncoderConfig, cqp bool, caps *videoCaps, next unsafe.Pointer) *rateControl {
	rc := &rateControl{}
	rc.info = vk.VideoEncodeRateControlInfoKHR{SType: vk.StructureTypeVideoEncodeRateControlInfoKhr, PNext: (*uintptr)(next)}
	if cqp {
		rc.info.RateControlMode = vk.VideoEncodeRateControlModeDisabledBitKhr
		return rc
	}
	mode := vk.VideoEncodeRateControlModeVbrBitKhr
	if caps.encode.RateControlModes&vk.VideoEncodeRateControlModeFlagsKHR(mode) == 0 {
		mode = vk.VideoEncodeRateControlModeCbrBitKhr
	}
	bitrate := min(desktopBitrate(cfg), caps.encode.MaxBitrate)
	maxRate := bitrate
	if mode == vk.VideoEncodeRateControlModeVbrBitKhr {
		maxRate = min(2*bitrate, caps.encode.MaxBitrate)
	}
	rc.layer = vk.VideoEncodeRateControlLayerInfoKHR{
		SType: vk.StructureTypeVideoEncodeRateControlLayerInfoKhr, AverageBitrate: bitrate, MaxBitrate: maxRate,
		FrameRateNumerator: uint32(cfg.FrameRate), FrameRateDenominator: 1,
	}
	rc.info.RateControlMode, rc.info.LayerCount, rc.info.PLayers = mode, 1, &rc.layer
	rc.info.VirtualBufferSizeInMs, rc.info.InitialVirtualBufferSizeInMs = 1000, 500
	return rc
}

// chain is the state as the pNext of a begin or control command.
func (rc *rateControl) chain() *uintptr { return (*uintptr)(unsafe.Pointer(&rc.info)) }
