package gpu

import (
	"errors"
	"fmt"
	"runtime"
	"slices"
	"unsafe"

	"github.com/stubbedev/lys-lib/intconv"
	"github.com/stubbedev/lys-lib/third_party/vk"
)

// vulkanBackend encodes on the device's Vulkan video queue: the
// conversion's planes are copied into the slot's source picture, and
// the video queue encodes that into the slot's share of the bitstream
// buffer.
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

	bitstream *buffer
	slotSize  int
	queries   vk.QueryPool
	pool      vk.CommandPool
	slots     [encodeSlots]vulkanSlot
	// converted is the timeline semaphore the n-th conversion signals
	// with n, which its encode waits for on the video queue; conversions
	// counts them.
	converted   vk.Semaphore
	conversions uint64

	cmd      encodeCommands
	feedback encodeFeedback
}

// encodeCommands are the structures the coding commands take, kept in
// the backend: what a Vulkan command points at escapes to the heap, so
// each frame's would be allocations. res holds the source picture, the
// one reconstructed and the one referenced; bound the latter two as the
// session binds them.
type encodeCommands struct {
	res     [3]vk.VideoPictureResourceInfoKHR
	bound   [2]vk.VideoReferenceSlotInfoKHR
	begin   vk.VideoBeginCodingInfoKHR
	control vk.VideoCodingControlInfoKHR
	setup   vk.VideoReferenceSlotInfoKHR
	ref     vk.VideoReferenceSlotInfoKHR
	encode  vk.VideoEncodeInfoKHR
	end     vk.VideoEndCodingInfoKHR
}

// encodeFeedback is what a feedback query reports of an encode: where
// in its bitstream slot the packet is, and whether it encoded.
type encodeFeedback struct {
	offset, bytes uint32
	status        vk.QueryResultStatusKHR
}

// vulkanSlot is a frame in flight on the video queue: its source
// picture, its commands, the fence its encode signals, and where in
// the bitstream buffer it writes.
type vulkanSlot struct {
	src    videoImage
	cb     vk.CommandBuffer
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
			return nil, errors.Join(err, vb.close())
		}
	}
	return vb, nil
}

func (vb *vulkanBackend) kind() EncoderBackend { return VulkanVideo }

func (vb *vulkanBackend) waitsForConversionOnGPU() bool { return true }

func (vb *vulkanBackend) codedSize() [2]int { return vb.coded }

func (vb *vulkanBackend) parameterSets() [][]byte { return vb.paramSets }

// createCommands makes the video queue's command buffers, each slot's
// fence and the conversions' semaphore.
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
	}
	vb.converted, err = d.timelineSemaphore()
	return err
}

// afterConvert copies the planes into slot's source picture and has the
// conversion's submission signal the video queue.
func (vb *vulkanBackend) afterConvert(cb vk.CommandBuffer, slot int, planes vk.Buffer) {
	d, s := vb.d, &vb.slots[slot]
	copyPlanes(d, cb, planes, s.src.image, vb.coded, vk.QueueFamilyIgnored)
	toSrc := imageBarrier(s.src.image, nv12Range, vk.ImageLayoutTransferDstOptimal, vk.ImageLayoutVideoEncodeSrcKhr)
	toSrc.SrcStageMask, toSrc.SrcAccessMask = vk.PipelineStageFlags2(vk.PipelineStage2CopyBit), vk.AccessFlags2(vk.Access2TransferWriteBit)
	barrier2(d, cb, nil, toSrc)
	vb.conversions++
	d.signalTimeline(timelineWait{semaphore: vb.converted, value: vb.conversions})
}

// collect waits for slot's encode, when it has not finished, and reads
// its packet out of the bitstream buffer, where the feedback query says
// it is.
func (vb *vulkanBackend) collect(slot int) ([]byte, error) {
	d, s := vb.d, &vb.slots[slot]
	if !d.fenceFinished(s.done) {
		if err := d.waitFence(s.done); err != nil {
			return nil, fmt.Errorf("wait for an encode: %w", err)
		}
	}
	res := &vb.feedback
	*res = encodeFeedback{}
	flags := vk.QueryResultFlags(vk.QueryResultWithStatusBitKhr)
	if r := d.cmds.GetQueryPoolResults(d.device, vb.queries, intconv.Must[uint32](slot), 1, unsafe.Sizeof(*res), unsafe.Pointer(res), vk.DeviceSize(unsafe.Sizeof(*res)), flags); r != vk.Success {
		return nil, fmt.Errorf("vkGetQueryPoolResults (encode feedback): %w", resultError(r))
	}
	if res.status != vk.QueryResultStatusCompleteKhr {
		return nil, fmt.Errorf("did not encode: status %d", res.status)
	}
	offset, n := int(res.offset), int(res.bytes)
	if offset+n > vb.slotSize {
		return nil, fmt.Errorf("the device wrote %d bytes at %d into a %d-byte bitstream slot", n, offset, vb.slotSize)
	}
	start := s.offset + offset
	if err := vb.bitstream.invalidate(); err != nil {
		return nil, err
	}
	return slices.Clone(vb.bitstream.mapped[start : start+n]), nil
}

func (vb *vulkanBackend) close() error { //nolint:unparam // encodeBackend's: the encoder's device idle wait has finished the video queue's frames
	d := vb.d
	for i := range vb.slots {
		s := &vb.slots[i]
		s.src.close(d)
		if s.done != 0 {
			d.cmds.DestroyFence(d.device, s.done, nil)
		}
	}
	vb.dpb.close(d)
	if vb.converted != 0 {
		d.cmds.DestroySemaphore(d.device, vb.converted, nil)
	}
	if vb.pool != 0 {
		d.cmds.DestroyCommandPool(d.device, vb.pool, nil)
	}
	if vb.queries != 0 {
		d.cmds.DestroyQueryPool(d.device, vb.queries, nil)
	}
	if vb.bitstream != nil {
		vb.bitstream.close()
	}
	if vb.sessionPar != 0 {
		d.cmds.DestroyVideoSessionParametersKHR(d.device, vb.sessionPar, nil)
	}
	if vb.session != 0 {
		d.cmds.DestroyVideoSessionKHR(d.device, vb.session, nil)
	}
	d.freeMemory(vb.sessionMem)
	return nil
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
	picture(sinceIDR int, idrs uint16, prev, qp int) (info, setup, ref unsafe.Pointer)
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
		LumaBitDepth:      nv12Samples.component(),
		ChromaBitDepth:    nv12Samples.component(),
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
		PictureFormat: nv12, MaxCodedExtent: extent2D(vb.coded[0], vb.coded[1]),
		ReferencePictureFormat: nv12, MaxDpbSlots: dpbSlots, MaxActiveReferencePictures: 1,
		PStdHeaderVersion: &vb.caps.video.StdHeaderVersion,
	}
	r := d.cmds.CreateVideoSessionKHR(d.device, &sci, nil, &vb.session)
	runtime.KeepAlive(vb.profile)
	if r != vk.Success {
		return fmt.Errorf("gpu: vkCreateVideoSessionKHR %dx%d: %w", vb.coded[0], vb.coded[1], resultError(r))
	}
	var err error
	vb.sessionMem, err = d.bindVideoSession(vb.session)
	return err
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
		return fmt.Errorf("gpu: vkCreateVideoSessionParametersKHR: %w", resultError(r))
	}
	get := vk.VideoEncodeSessionParametersGetInfoKHR{SType: vk.StructureTypeVideoEncodeSessionParametersGetInfoKhr, PNext: (*uintptr)(getNext), VideoSessionParameters: vb.sessionPar}
	fb := vk.VideoEncodeSessionParametersFeedbackInfoKHR{SType: vk.StructureTypeVideoEncodeSessionParametersFeedbackInfoKhr}
	var size uintptr
	if r := d.cmds.GetEncodedVideoSessionParametersKHR(d.device, &get, &fb, &size, nil); r != vk.Success {
		return fmt.Errorf("gpu: vkGetEncodedVideoSessionParametersKHR (size): %w", resultError(r))
	}
	data := make([]byte, size)
	if size > 0 {
		if r := d.cmds.GetEncodedVideoSessionParametersKHR(d.device, &get, &fb, &size, (*uintptr)(unsafe.Pointer(&data[0]))); r != vk.Success {
			return fmt.Errorf("gpu: vkGetEncodedVideoSessionParametersKHR: %w", resultError(r))
		}
	}
	runtime.KeepAlive(vb.codec)
	vb.paramSets = nalUnits(data[:size])
	if len(vb.paramSets) != nals {
		return fmt.Errorf("gpu: the device wrote %d %s parameter set NAL units, want %d", len(vb.paramSets), vb.cfg.Codec, nals)
	}
	return nil
}

// createImages makes each slot's source picture and the DPB.
func (vb *vulkanBackend) createImages() error {
	d := vb.d
	src := pictureSpec{
		extent: vb.coded, samples: nv12Samples, profiles: &vb.profile.list, layers: 1,
		usage:  vk.ImageUsageFlags(vk.ImageUsageVideoEncodeSrcBitKhr | vk.ImageUsageTransferDstBit),
		family: d.video.family, shared: true,
	}
	dpb := src
	dpb.usage, dpb.layers, dpb.shared = vk.ImageUsageFlags(vk.ImageUsageVideoEncodeDpbBitKhr), dpbSlots, false
	for _, spec := range []*pictureSpec{&src, &dpb} {
		tiling, err := d.videoTiling(spec.profiles, spec.usage, spec.samples, ErrNoVideoEncoder)
		if err != nil {
			return err
		}
		spec.tiling = tiling
	}
	var err error
	for i := range vb.slots {
		if vb.slots[i].src, err = d.newPicture(src); err != nil {
			return fmt.Errorf("gpu: encoder source picture: %w", err)
		}
	}
	if vb.dpb, err = d.newPicture(dpb); err != nil {
		return fmt.Errorf("gpu: encoder DPB: %w", err)
	}
	return nil
}

// createBuffers makes the bitstream buffer each slot writes its share
// of, and the feedback queries.
func (vb *vulkanBackend) createBuffers() error {
	d := vb.d
	c := vb.caps.video
	sizeAlign, errSize := intconv.Exact[int](max(c.MinBitstreamBufferSizeAlignment, 1))
	offsetAlign, errOffset := intconv.Exact[int](max(c.MinBitstreamBufferOffsetAlignment, 1))
	if err := errors.Join(errSize, errOffset); err != nil {
		return fmt.Errorf("gpu: %s's bitstream alignments %d and %d: %w", d.name, c.MinBitstreamBufferSizeAlignment, c.MinBitstreamBufferOffsetAlignment, err)
	}
	vb.slotSize = alignUp(alignUp(bitstreamBytes(vb.coded), sizeAlign), offsetAlign)
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
		return fmt.Errorf("gpu: vkCreateQueryPool (encode feedback): %w", resultError(r))
	}
	return nil
}

// encode records and submits encoding slot's source picture, after
// the conversion submitted last.
func (vb *vulkanBackend) encode(slot, frame, sinceIDR int, idrs uint16) error {
	d, s, c := vb.d, &vb.slots[slot], &vb.cmd
	cb := s.cb
	if err := begin(d, cb); err != nil {
		return err
	}
	q := intconv.Must[uint32](slot)
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
	extent := extent2D(vb.coded[0], vb.coded[1])
	c.res = [3]vk.VideoPictureResourceInfoKHR{
		{SType: vk.StructureTypeVideoPictureResourceInfoKhr, CodedExtent: extent, ImageViewBinding: s.src.views[0]},
		{SType: vk.StructureTypeVideoPictureResourceInfoKhr, CodedExtent: extent, ImageViewBinding: vb.dpb.views[cur]},
		{SType: vk.StructureTypeVideoPictureResourceInfoKhr, CodedExtent: extent, ImageViewBinding: vb.dpb.views[prev]},
	}
	refs := 0
	if sinceIDR > 0 {
		refs = 1
	}
	c.bound = [2]vk.VideoReferenceSlotInfoKHR{
		{SType: vk.StructureTypeVideoReferenceSlotInfoKhr, SlotIndex: -1, PPictureResource: &c.res[1]},
		{SType: vk.StructureTypeVideoReferenceSlotInfoKhr, SlotIndex: int32(prev), PPictureResource: &c.res[2]},
	}
	c.begin = vk.VideoBeginCodingInfoKHR{
		SType: vk.StructureTypeVideoBeginCodingInfoKhr, VideoSession: vb.session, VideoSessionParameters: vb.sessionPar,
		ReferenceSlotCount: uint32(1 + refs), PReferenceSlots: &c.bound[0],
	}
	if vb.rcSet {
		c.begin.PNext = vb.rc.chain()
	}
	d.cmds.CmdBeginVideoCodingKHR(cb, &c.begin)
	if !vb.rcSet {
		c.control = vk.VideoCodingControlInfoKHR{
			SType: vk.StructureTypeVideoCodingControlInfoKhr, PNext: vb.rc.chain(),
			Flags: vk.VideoCodingControlFlagsKHR(vk.VideoCodingControlResetBitKhr | vk.VideoCodingControlEncodeRateControlBitKhr),
		}
		d.cmds.CmdControlVideoCodingKHR(cb, &c.control)
		vb.rcSet = true
	}
	c.setup = vk.VideoReferenceSlotInfoKHR{SType: vk.StructureTypeVideoReferenceSlotInfoKhr, PNext: (*uintptr)(setupInfo), SlotIndex: int32(cur), PPictureResource: &c.res[1]}
	c.ref = vk.VideoReferenceSlotInfoKHR{SType: vk.StructureTypeVideoReferenceSlotInfoKhr, PNext: (*uintptr)(refInfo), SlotIndex: int32(prev), PPictureResource: &c.res[2]}
	c.encode = vk.VideoEncodeInfoKHR{
		SType: vk.StructureTypeVideoEncodeInfoKhr, PNext: (*uintptr)(picInfo),
		DstBuffer: vb.bitstream.buf, DstBufferOffset: intconv.Must[vk.DeviceSize](s.offset), DstBufferRange: intconv.Must[vk.DeviceSize](vb.slotSize),
		SrcPictureResource: c.res[0], PSetupReferenceSlot: &c.setup, ReferenceSlotCount: uint32(refs), PReferenceSlots: &c.ref,
	}
	d.cmds.CmdBeginQuery(cb, vb.queries, q, 0)
	d.cmds.CmdEncodeVideoKHR(cb, &c.encode)
	d.cmds.CmdEndQuery(cb, vb.queries, q)
	c.end = vk.VideoEndCodingInfoKHR{SType: vk.StructureTypeVideoEndCodingInfoKhr}
	d.cmds.CmdEndVideoCodingKHR(cb, &c.end)
	runtime.KeepAlive(vb.codec)
	if r := d.cmds.EndCommandBuffer(cb); r != vk.Success {
		return fmt.Errorf("gpu: vkEndCommandBuffer (encode): %w", resultError(r))
	}
	if err := resetFence(d, s.done); err != nil {
		return err
	}
	wait := vk.SemaphoreSubmitInfo{SType: vk.StructureTypeSemaphoreSubmitInfo, Semaphore: vb.converted, Value: vb.conversions, StageMask: coding}
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
		FrameRateNumerator: intconv.Must[uint32](cfg.FrameRate), FrameRateDenominator: 1,
	}
	rc.info.RateControlMode, rc.info.LayerCount, rc.info.PLayers = mode, 1, &rc.layer
	rc.info.VirtualBufferSizeInMs, rc.info.InitialVirtualBufferSizeInMs = 1000, 500
	return rc
}

// chain is the state as the pNext of a begin or control command.
func (rc *rateControl) chain() *uintptr { return (*uintptr)(unsafe.Pointer(&rc.info)) }
