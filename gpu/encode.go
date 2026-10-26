package gpu

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"runtime"
	"slices"
	"time"
	"unsafe"

	"github.com/stubbedev/lys-ui/third_party/vk"
)

// ErrNoVideoEncoder reports a device that does not encode the codec
// asked for: no video encode queue (lavapipe, older radv and anv), or
// one without that codec.
var ErrNoVideoEncoder = errors.New("gpu: the device has no video encoder")

// Codecs are the codecs the device encodes; none on a device without a
// video encode queue.
func (d *Device) Codecs() []Codec {
	if d.video == nil {
		return nil
	}
	return slices.Clone(d.video.codecs)
}

// EncoderConfig is a video stream's shape.
type EncoderConfig struct {
	Codec Codec
	// Width and Height are the frame size. The stream is coded in the
	// device's units and at least its smallest size, the padding's
	// pixels the edge's repeated and cropped off on decode; an odd size
	// decodes one pixel larger, its last row or column twice.
	Width, Height int
	// FrameRate is the frames a second the stream runs at, for its
	// level; frames may come at any time.
	FrameRate int
	// KeyInterval is the frames from one keyframe to the next.
	KeyInterval int
	// QP is the constant quantiser, 0 (best) to 51. A device without
	// constant-QP coding runs its own rate control at a bitrate
	// matching QP 20 on desktop content instead.
	QP int
}

func (c EncoderConfig) validate() error {
	switch {
	case c.Codec < 0 || int(c.Codec) >= len(codecs):
		return fmt.Errorf("gpu: codec %d is none of the %d known", c.Codec, len(codecs))
	case c.Width <= 0 || c.Height <= 0:
		return fmt.Errorf("gpu: encoder size %dx%d is not positive", c.Width, c.Height)
	case c.FrameRate <= 0:
		return fmt.Errorf("gpu: encoder frame rate %d is not positive", c.FrameRate)
	case c.KeyInterval <= 0 || c.KeyInterval > 1<<16:
		return fmt.Errorf("gpu: key interval %d is not 1 to 65536 frames", c.KeyInterval)
	case c.QP < 0 || c.QP > 51:
		return fmt.Errorf("gpu: QP %d is not 0 to 51", c.QP)
	}
	return nil
}

// Packet is one encoded frame: an Annex-B access unit (start-code
// delimited NAL units, without the parameter sets, which
// ParameterSets gives), the time it was handed in at, and whether it
// is a keyframe (an IDR).
type Packet struct {
	Data []byte
	PTS  time.Duration
	Key  bool
}

// encodeSlots are the frames in flight on the video queue: Encode
// returns once a frame's conversion is done and its source free, while
// the frame before still encodes.
const encodeSlots = 2

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

// evenUp rounds n up to even: 4:2:0 crops in pairs of pixels, so an odd
// size shows its last row or column twice.
func evenUp(n int) int { return n + n&1 }

// encodeSlot is one frame in flight: its source picture, its share of
// the bitstream buffer and its query, the video queue's command buffer
// and the semaphore and fence around it, and the frame it holds.
type encodeSlot struct {
	src    videoImage
	cb     vk.CommandBuffer
	ready  vk.Semaphore
	done   vk.Fence
	busy   bool
	pts    time.Duration
	key    bool
	offset int
}

// videoImage is an NV12 picture: its image, memory and the views the
// coding commands name it by (one per array layer).
type videoImage struct {
	image  vk.Image
	memory vk.DeviceMemory
	views  []vk.ImageView
}

// Encoder encodes frames into H.264 or H.265 on the device's video
// queue. A
// frame's pixels come from a Buffer a compositor copied a screen into:
// a compute pass converts them to NV12, and the video queue encodes
// that while the next frame converts. Like the Device, it belongs to
// one goroutine.
type Encoder struct {
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

	planes    *deviceBuffer
	bitstream *buffer
	slotSize  int
	queries   vk.QueryPool

	setLayout vk.DescriptorSetLayout
	layout    vk.PipelineLayout
	pipeline  vk.Pipeline
	module    vk.ShaderModule
	descPool  vk.DescriptorPool
	set       vk.DescriptorSet
	gfxPool   vk.CommandPool
	videoPool vk.CommandPool
	gfxCB     vk.CommandBuffer
	gfxDone   vk.Fence
	linear    vk.Sampler

	// overlay is the picture SetOverlay draws over every frame, scaled
	// to overlayDst; nil for none. overlayStaging carries its uploads.
	overlay        *surface
	overlayDst     image.Rectangle
	overlayStaging *buffer

	slots  [encodeSlots]encodeSlot
	frames int
	idrs   int
	closed bool
}

// NewEncoder makes an encoder for cfg: ErrNoVideoEncoder when the
// device does not encode cfg.Codec, an error naming the limit when it
// cannot encode cfg's size.
func (d *Device) NewEncoder(cfg EncoderConfig) (*Encoder, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	if d.video == nil || !slices.Contains(d.video.codecs, cfg.Codec) {
		return nil, fmt.Errorf("%w: %s encodes no %s", ErrNoVideoEncoder, d.name, cfg.Codec)
	}
	e := &Encoder{d: d, cfg: cfg, codec: newVideoCodec(cfg.Codec)}
	steps := []func() error{e.pickProfile, e.createSession, e.createParameters, e.createImages, e.createBuffers, e.createPipeline, e.createCommands}
	for _, step := range steps {
		if err := step(); err != nil {
			e.Close()
			return nil, err
		}
	}
	return e, nil
}

// pickProfile takes the codec's best profile the device encodes, with
// its capabilities, and sizes the coded picture to them.
func (e *Encoder) pickProfile() error {
	d := e.d
	var last vk.Result
	for i := range e.codec.profiles() {
		p := newVideoProfile(e.cfg.Codec, e.codec, i)
		caps, r := d.videoCaps(p, e.codec)
		if r != vk.Success {
			last = r
			continue
		}
		e.profile, e.caps = p, caps
		break
	}
	if e.profile == nil {
		return fmt.Errorf("%w: %s encodes none of the %s profiles: %d", ErrNoVideoEncoder, d.name, e.cfg.Codec, last)
	}
	c, lim := e.caps, e.codec.limits()
	if c.video.MaxDpbSlots < dpbSlots || c.video.MaxActiveReferencePictures < 1 || lim.pRefs < 1 {
		return fmt.Errorf("%w: %s codes no %s P frames (%d DPB slots, %d references)", ErrNoVideoEncoder, d.name, e.cfg.Codec, c.video.MaxDpbSlots, lim.pRefs)
	}
	unit := e.codec.align()
	gw := max(unit, int(c.video.PictureAccessGranularity.Width), int(c.encode.EncodeInputPictureGranularity.Width))
	gh := max(unit, int(c.video.PictureAccessGranularity.Height), int(c.encode.EncodeInputPictureGranularity.Height))
	minE, maxE := c.video.MinCodedExtent, c.video.MaxCodedExtent
	e.coded = [2]int{
		alignUp(max(evenUp(e.cfg.Width), int(minE.Width)), gw),
		alignUp(max(evenUp(e.cfg.Height), int(minE.Height)), gh),
	}
	if e.coded[0] > int(maxE.Width) || e.coded[1] > int(maxE.Height) {
		return fmt.Errorf("gpu: %s encodes up to %dx%d, not %dx%d", d.name, maxE.Width, maxE.Height, e.coded[0], e.coded[1])
	}
	e.cqp = c.encode.RateControlModes&vk.VideoEncodeRateControlModeFlagsKHR(vk.VideoEncodeRateControlModeDisabledBitKhr) != 0
	if e.cqp {
		e.qp = min(max(e.cfg.QP, lim.minQP), lim.maxQP)
	}
	e.rc = newRateControl(e.cfg, e.cqp, c, e.codec.rateControl(e.cfg))
	return nil
}

func alignUp(n, to int) int { return (n + to - 1) / to * to }

// createSession makes the video session and binds its memory.
func (e *Encoder) createSession() error {
	d := e.d
	sci := vk.VideoSessionCreateInfoKHR{
		SType: vk.StructureTypeVideoSessionCreateInfoKhr, QueueFamilyIndex: d.video.family, PVideoProfile: &e.profile.info,
		PictureFormat: nv12, MaxCodedExtent: vk.Extent2D{Width: uint32(e.coded[0]), Height: uint32(e.coded[1])},
		ReferencePictureFormat: nv12, MaxDpbSlots: dpbSlots, MaxActiveReferencePictures: 1,
		PStdHeaderVersion: &e.caps.video.StdHeaderVersion,
	}
	r := d.cmds.CreateVideoSessionKHR(d.device, &sci, nil, &e.session)
	runtime.KeepAlive(e.profile)
	if r != vk.Success {
		return fmt.Errorf("gpu: vkCreateVideoSessionKHR %dx%d: %d", e.coded[0], e.coded[1], r)
	}
	var n uint32
	if r := d.cmds.GetVideoSessionMemoryRequirementsKHR(d.device, e.session, &n, nil); r != vk.Success {
		return fmt.Errorf("gpu: vkGetVideoSessionMemoryRequirementsKHR: %d", r)
	}
	reqs := make([]vk.VideoSessionMemoryRequirementsKHR, n)
	for i := range reqs {
		reqs[i].SType = vk.StructureTypeVideoSessionMemoryRequirementsKhr
	}
	if n > 0 {
		if r := d.cmds.GetVideoSessionMemoryRequirementsKHR(d.device, e.session, &n, &reqs[0]); r != vk.Success {
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
		e.sessionMem = append(e.sessionMem, mem)
		binds = append(binds, vk.BindVideoSessionMemoryInfoKHR{
			SType: vk.StructureTypeBindVideoSessionMemoryInfoKhr, MemoryBindIndex: req.MemoryBindIndex, Memory: mem, MemorySize: req.MemoryRequirements.Size,
		})
	}
	if len(binds) > 0 {
		if r := d.cmds.BindVideoSessionMemoryKHR(d.device, e.session, uint32(len(binds)), &binds[0]); r != vk.Success {
			return fmt.Errorf("gpu: vkBindVideoSessionMemoryKHR: %d", r)
		}
	}
	return nil
}

// createParameters makes the session parameters and reads back the
// NAL units the device writes for them.
func (e *Encoder) createParameters() error {
	d := e.d
	create, getNext, nals, err := e.codec.parameters(e.cfg, e.coded)
	if err != nil {
		return err
	}
	pci := vk.VideoSessionParametersCreateInfoKHR{SType: vk.StructureTypeVideoSessionParametersCreateInfoKhr, PNext: (*uintptr)(create), VideoSession: e.session}
	r := d.cmds.CreateVideoSessionParametersKHR(d.device, &pci, nil, &e.sessionPar)
	runtime.KeepAlive(e.codec)
	if r != vk.Success {
		return fmt.Errorf("gpu: vkCreateVideoSessionParametersKHR: %d", r)
	}
	get := vk.VideoEncodeSessionParametersGetInfoKHR{SType: vk.StructureTypeVideoEncodeSessionParametersGetInfoKhr, PNext: (*uintptr)(getNext), VideoSessionParameters: e.sessionPar}
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
	runtime.KeepAlive(e.codec)
	e.paramSets = nalUnits(data[:size])
	if len(e.paramSets) != nals {
		return fmt.Errorf("gpu: the device wrote %d %s parameter set NAL units, want %d", len(e.paramSets), e.cfg.Codec, nals)
	}
	return nil
}

// nalUnits splits an Annex-B stream into its NAL units, start codes
// dropped.
func nalUnits(b []byte) [][]byte {
	var out [][]byte
	for len(b) > 0 {
		i := bytes.Index(b, []byte{0, 0, 1})
		if i < 0 {
			break
		}
		b = b[i+3:]
		j := bytes.Index(b, []byte{0, 0, 1})
		if j < 0 {
			j = len(b)
		}
		nal := bytes.TrimRight(b[:j], "\x00")
		if len(nal) > 0 {
			out = append(out, nal)
		}
		b = b[j:]
	}
	return out
}

// ParameterSets are the stream's parameter sets as NAL units without
// start codes: the SPS and the PPS for H.264, the VPS, SPS and PPS for
// H.265.
func (e *Encoder) ParameterSets() [][]byte {
	out := make([][]byte, len(e.paramSets))
	for i, p := range e.paramSets {
		out[i] = slices.Clone(p)
	}
	return out
}

// videoFormat finds NV12's properties among the formats the device
// codes pictures of usage in.
func (e *Encoder) videoFormat(usage vk.ImageUsageFlags) (vk.VideoFormatPropertiesKHR, error) {
	d := e.d
	fi := vk.PhysicalDeviceVideoFormatInfoKHR{SType: vk.StructureTypePhysicalDeviceVideoFormatInfoKhr, PNext: (*uintptr)(unsafe.Pointer(&e.profile.list)), ImageUsage: usage}
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
	runtime.KeepAlive(e.profile)
	for _, p := range props[:n] {
		if p.Format == nv12 && p.ImageType == vk.ImageType2d {
			return p, nil
		}
	}
	return vk.VideoFormatPropertiesKHR{}, fmt.Errorf("%w: %s codes no NV12 pictures for usage %#x", ErrNoVideoEncoder, d.name, usage)
}

// createImages makes each slot's source picture and the DPB.
func (e *Encoder) createImages() error {
	d := e.d
	srcUsage := vk.ImageUsageFlags(vk.ImageUsageVideoEncodeSrcBitKhr | vk.ImageUsageTransferDstBit)
	srcFormat, err := e.videoFormat(srcUsage)
	if err != nil {
		return err
	}
	dpbUsage := vk.ImageUsageFlags(vk.ImageUsageVideoEncodeDpbBitKhr)
	dpbFormat, err := e.videoFormat(dpbUsage)
	if err != nil {
		return err
	}
	families := []uint32{d.family, d.video.family}
	for i := range e.slots {
		if err := e.newVideoImage(&e.slots[i].src, srcFormat, srcUsage, 1, families); err != nil {
			return fmt.Errorf("gpu: encoder source picture: %w", err)
		}
	}
	if err := e.newVideoImage(&e.dpb, dpbFormat, dpbUsage, dpbSlots, nil); err != nil {
		return fmt.Errorf("gpu: encoder DPB: %w", err)
	}
	return nil
}

// newVideoImage makes an NV12 picture of the coded size with layers
// array layers in format's tiling, shared between families when there
// are two different ones, and a view of each layer.
func (e *Encoder) newVideoImage(vi *videoImage, format vk.VideoFormatPropertiesKHR, usage vk.ImageUsageFlags, layers int, families []uint32) error {
	d := e.d
	ici := vk.ImageCreateInfo{
		SType: vk.StructureTypeImageCreateInfo, PNext: (*uintptr)(unsafe.Pointer(&e.profile.list)), Flags: format.ImageCreateFlags,
		ImageType: vk.ImageType2d, Format: nv12, Extent: vk.Extent3D{Width: uint32(e.coded[0]), Height: uint32(e.coded[1]), Depth: 1},
		MipLevels: 1, ArrayLayers: uint32(layers), Samples: vk.SampleCount1Bit, Tiling: format.ImageTiling,
		Usage: usage, SharingMode: vk.SharingModeExclusive, InitialLayout: vk.ImageLayoutUndefined,
	}
	if len(families) == 2 && families[0] != families[1] {
		ici.SharingMode, ici.QueueFamilyIndexCount, ici.PQueueFamilyIndices = vk.SharingModeConcurrent, 2, &families[0]
	}
	r := d.cmds.CreateImage(d.device, &ici, nil, &vi.image)
	runtime.KeepAlive(e.profile)
	runtime.KeepAlive(families)
	if r != vk.Success {
		return fmt.Errorf("vkCreateImage NV12 %dx%d x%d: %d", e.coded[0], e.coded[1], layers, r)
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

// planeBytes is the size of the coded picture's two planes, tight.
func (e *Encoder) planeBytes() int { return e.coded[0] * e.coded[1] * 3 / 2 }

// createBuffers makes the conversion's plane buffer, the bitstream
// buffer each slot writes its share of, and the feedback queries.
func (e *Encoder) createBuffers() error {
	d := e.d
	planes, err := d.newDeviceBuffer(e.planeBytes(), vk.BufferUsageStorageBufferBit|vk.BufferUsageTransferSrcBit)
	if err != nil {
		return fmt.Errorf("gpu: encoder planes: %w", err)
	}
	e.planes = planes
	c := e.caps.video
	e.slotSize = alignUp(alignUp(e.planeBytes()+(1<<16), int(max(c.MinBitstreamBufferSizeAlignment, 1))), int(max(c.MinBitstreamBufferOffsetAlignment, 1)))
	bs, err := d.newReadBuffer(e.slotSize*encodeSlots, vk.BufferUsageVideoEncodeDstBitKhr, unsafe.Pointer(&e.profile.list))
	runtime.KeepAlive(e.profile)
	if err != nil {
		return fmt.Errorf("gpu: encoder bitstream: %w", err)
	}
	e.bitstream = bs
	for i := range e.slots {
		e.slots[i].offset = i * e.slotSize
	}
	feedback := vk.QueryPoolVideoEncodeFeedbackCreateInfoKHR{
		SType: vk.StructureTypeQueryPoolVideoEncodeFeedbackCreateInfoKhr, PNext: (*uintptr)(unsafe.Pointer(&e.profile.info)),
		EncodeFeedbackFlags: vk.VideoEncodeFeedbackFlagsKHR(vk.VideoEncodeFeedbackBitstreamBufferOffsetBitKhr | vk.VideoEncodeFeedbackBitstreamBytesWrittenBitKhr),
	}
	qci := vk.QueryPoolCreateInfo{SType: vk.StructureTypeQueryPoolCreateInfo, PNext: (*uintptr)(unsafe.Pointer(&feedback)), QueryType: vk.QueryTypeVideoEncodeFeedbackKhr, QueryCount: encodeSlots}
	r := d.cmds.CreateQueryPool(d.device, &qci, nil, &e.queries)
	runtime.KeepAlive(&feedback)
	runtime.KeepAlive(e.profile)
	if r != vk.Success {
		return fmt.Errorf("gpu: vkCreateQueryPool (encode feedback): %d", r)
	}
	return nil
}

// nv12Constants is nv12.comp's push constant block: the source and
// coded sizes and the overlay's rectangle, empty for none.
type nv12Constants struct {
	srcW, srcH, codedW, codedH int32
	overlay                    [4]int32
}

// createPipeline makes the NV12 conversion's compute pipeline and the
// descriptor set it reads the source and writes the planes through.
func (e *Encoder) createPipeline() error {
	d := e.d
	comp := vk.ShaderStageFlags(vk.ShaderStageComputeBit)
	b := [3]vk.DescriptorSetLayoutBinding{
		{Binding: 0, DescriptorType: vk.DescriptorTypeCombinedImageSampler, DescriptorCount: 1, StageFlags: comp},
		{Binding: 1, DescriptorType: vk.DescriptorTypeStorageBuffer, DescriptorCount: 1, StageFlags: comp},
		{Binding: 2, DescriptorType: vk.DescriptorTypeCombinedImageSampler, DescriptorCount: 1, StageFlags: comp},
	}
	lci := vk.DescriptorSetLayoutCreateInfo{SType: vk.StructureTypeDescriptorSetLayoutCreateInfo, BindingCount: 3, PBindings: &b[0]}
	if r := d.cmds.CreateDescriptorSetLayout(d.device, &lci, nil, &e.setLayout); r != vk.Success {
		return fmt.Errorf("gpu: vkCreateDescriptorSetLayout (NV12): %d", r)
	}
	push := vk.PushConstantRange{StageFlags: comp, Size: uint32(unsafe.Sizeof(nv12Constants{}))}
	pci := vk.PipelineLayoutCreateInfo{SType: vk.StructureTypePipelineLayoutCreateInfo, SetLayoutCount: 1, PSetLayouts: &e.setLayout, PushConstantRangeCount: 1, PPushConstantRanges: &push}
	if r := d.cmds.CreatePipelineLayout(d.device, &pci, nil, &e.layout); r != vk.Success {
		return fmt.Errorf("gpu: vkCreatePipelineLayout (NV12): %d", r)
	}
	m, err := d.shaderModule(nv12Comp)
	if err != nil {
		return err
	}
	e.module = m
	name := cstr("main")
	cpi := vk.ComputePipelineCreateInfo{
		SType:  vk.StructureTypeComputePipelineCreateInfo,
		Stage:  vk.PipelineShaderStageCreateInfo{SType: vk.StructureTypePipelineShaderStageCreateInfo, Stage: vk.ShaderStageComputeBit, Module: m, PName: uintptr(unsafe.Pointer(&name[0]))},
		Layout: e.layout,
	}
	r := d.cmds.CreateComputePipelines(d.device, 0, 1, &cpi, nil, &e.pipeline)
	runtime.KeepAlive(name)
	if r != vk.Success {
		return fmt.Errorf("gpu: vkCreateComputePipelines (NV12): %d", r)
	}
	sci := vk.SamplerCreateInfo{
		SType: vk.StructureTypeSamplerCreateInfo, MagFilter: vk.FilterLinear, MinFilter: vk.FilterLinear, MipmapMode: vk.SamplerMipmapModeNearest,
		AddressModeU: vk.SamplerAddressModeClampToEdge, AddressModeV: vk.SamplerAddressModeClampToEdge, AddressModeW: vk.SamplerAddressModeClampToEdge,
	}
	if r := d.cmds.CreateSampler(d.device, &sci, nil, &e.linear); r != vk.Success {
		return fmt.Errorf("gpu: vkCreateSampler (overlay): %d", r)
	}
	sizes := [2]vk.DescriptorPoolSize{{Type: vk.DescriptorTypeCombinedImageSampler, DescriptorCount: 2}, {Type: vk.DescriptorTypeStorageBuffer, DescriptorCount: 1}}
	dci := vk.DescriptorPoolCreateInfo{SType: vk.StructureTypeDescriptorPoolCreateInfo, MaxSets: 1, PoolSizeCount: 2, PPoolSizes: &sizes[0]}
	if r := d.cmds.CreateDescriptorPool(d.device, &dci, nil, &e.descPool); r != vk.Success {
		return fmt.Errorf("gpu: vkCreateDescriptorPool (NV12): %d", r)
	}
	ai := vk.DescriptorSetAllocateInfo{SType: vk.StructureTypeDescriptorSetAllocateInfo, DescriptorPool: e.descPool, DescriptorSetCount: 1, PSetLayouts: &e.setLayout}
	if r := d.cmds.AllocateDescriptorSets(d.device, &ai, &e.set); r != vk.Success {
		return fmt.Errorf("gpu: vkAllocateDescriptorSets (NV12): %d", r)
	}
	bi := vk.DescriptorBufferInfo{Buffer: e.planes.buf, Range: vk.DeviceSize(e.planeBytes())}
	w := vk.WriteDescriptorSet{SType: vk.StructureTypeWriteDescriptorSet, DstSet: e.set, DstBinding: 1, DescriptorCount: 1, DescriptorType: vk.DescriptorTypeStorageBuffer, PBufferInfo: &bi}
	d.cmds.UpdateDescriptorSets(d.device, 1, &w, 0, nil)
	return nil
}

// createCommands makes the command pools and buffers, and the
// semaphores and fences between the queues and the host.
func (e *Encoder) createCommands() error {
	d := e.d
	var err error
	if e.gfxPool, err = d.commandPool(d.family); err != nil {
		return err
	}
	if e.videoPool, err = d.commandPool(d.video.family); err != nil {
		return err
	}
	if e.gfxCB, err = d.commandBuffer(e.gfxPool); err != nil {
		return err
	}
	if e.gfxDone, err = d.fence(); err != nil {
		return err
	}
	for i := range e.slots {
		s := &e.slots[i]
		if s.cb, err = d.commandBuffer(e.videoPool); err != nil {
			return err
		}
		if s.done, err = d.fence(); err != nil {
			return err
		}
		sci := vk.SemaphoreCreateInfo{SType: vk.StructureTypeSemaphoreCreateInfo}
		if r := d.cmds.CreateSemaphore(d.device, &sci, nil, &s.ready); r != vk.Success {
			return fmt.Errorf("gpu: vkCreateSemaphore: %d", r)
		}
	}
	return nil
}

// Encode converts src's pixels (a Buffer the size the encoder was made
// for) and queues them for encoding, presented at pts; it returns the
// packets of the frames that finished meanwhile, oldest first. src is
// free to take the next frame when Encode returns.
func (e *Encoder) Encode(src *Buffer, pts time.Duration) ([]Packet, error) {
	if e.closed {
		return nil, errors.New("gpu: encode on a closed encoder")
	}
	if src.d != e.d {
		return nil, errors.New("gpu: encode a buffer of another device")
	}
	if src.closed || src.e.buf.Width != e.cfg.Width || src.e.buf.Height != e.cfg.Height {
		return nil, fmt.Errorf("gpu: encode a %dx%d buffer (closed %v) on a %dx%d encoder", src.e.buf.Width, src.e.buf.Height, src.closed, e.cfg.Width, e.cfg.Height)
	}
	slot := &e.slots[e.frames%encodeSlots]
	var out []Packet
	if slot.busy {
		p, err := e.collect(slot)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	if err := e.convert(src, slot); err != nil {
		return nil, err
	}
	sinceIDR := e.frames % e.cfg.KeyInterval
	if err := e.encode(slot, sinceIDR); err != nil {
		return nil, err
	}
	if r := e.d.cmds.WaitForFences(e.d.device, 1, &e.gfxDone, vk.True, ^uint64(0)); r != vk.Success {
		return nil, fmt.Errorf("gpu: wait for the NV12 conversion: %d", r)
	}
	slot.busy, slot.pts, slot.key = true, pts, sinceIDR == 0
	if sinceIDR == 0 {
		e.idrs++
	}
	e.frames++
	return out, nil
}

// maxOverlayCoord bounds an overlay rectangle's coordinates, which the
// conversion takes as 32-bit integers.
const maxOverlayCoord = 1 << 24

// SetOverlay draws img over every following frame's pixels, scaled
// (linear filtering) to fill dst in frame coordinates, opaque, before
// the NV12 conversion; nil removes it. img is uploaded here, so the
// caller may reuse it once SetOverlay returns. dst is clipped to the
// frame; an empty dst, or one off the frame, is an error.
func (e *Encoder) SetOverlay(img *image.RGBA, dst image.Rectangle) error {
	if e.closed {
		return errors.New("gpu: overlay on a closed encoder")
	}
	if img == nil {
		e.overlayDst = image.Rectangle{}
		if e.overlay != nil {
			e.overlay.close()
			e.overlay = nil
		}
		return nil
	}
	frame := image.Rect(0, 0, e.cfg.Width, e.cfg.Height)
	bound := image.Rect(-maxOverlayCoord, -maxOverlayCoord, maxOverlayCoord, maxOverlayCoord)
	switch {
	case dst.Empty():
		return fmt.Errorf("gpu: overlay into the empty rectangle %v", dst)
	case !dst.Overlaps(frame):
		return fmt.Errorf("gpu: overlay rectangle %v is off the %dx%d frame", dst, e.cfg.Width, e.cfg.Height)
	case !dst.In(bound):
		return fmt.Errorf("gpu: overlay rectangle %v reaches past %d", dst, maxOverlayCoord)
	case img.Rect.Empty():
		return fmt.Errorf("gpu: an empty overlay picture %v", img.Rect)
	}
	if err := e.uploadOverlay(img); err != nil {
		return err
	}
	e.overlayDst = dst
	return nil
}

// uploadOverlay copies img into the overlay surface, made anew when
// its size changes.
func (e *Encoder) uploadOverlay(img *image.RGBA) error {
	d := e.d
	w, h := img.Rect.Dx(), img.Rect.Dy()
	if e.overlay != nil && (e.overlay.w != w || e.overlay.h != h) {
		e.overlay.close()
		e.overlay = nil
	}
	if e.overlay == nil {
		s, err := d.newSurface(w, h)
		if err != nil {
			return fmt.Errorf("gpu: overlay %dx%d: %w", w, h, err)
		}
		if err := s.ready(); err != nil {
			s.close()
			return fmt.Errorf("gpu: overlay %dx%d: %w", w, h, err)
		}
		e.overlay = s
	}
	staging := e.overlayStaging
	if staging == nil {
		b, err := d.newBuffer(max(w*h*4, 1<<16), vk.BufferUsageTransferSrcBit)
		if err != nil {
			return err
		}
		staging = b
	}
	staging, err := staging.fit(w * h * 4)
	e.overlayStaging = staging
	if err != nil {
		e.overlayStaging = nil
		return err
	}
	for y := range h {
		row := img.Pix[y*img.Stride : y*img.Stride+w*4]
		out := staging.mapped[y*w*4 : (y+1)*w*4]
		for i := 0; i < len(row); i += 4 {
			out[i], out[i+1], out[i+2], out[i+3] = row[i+2], row[i+1], row[i], 0xff
		}
	}
	return d.submit(func(cb vk.CommandBuffer) {
		e.overlay.upload(cb, image.Rect(0, 0, w, h), staging, 0, w)
		e.overlay.to(cb, vk.ImageLayoutShaderReadOnlyOptimal)
	})
}

// Flush waits for the frames in flight and returns their packets,
// oldest first.
func (e *Encoder) Flush() ([]Packet, error) {
	var out []Packet
	for i := range encodeSlots {
		slot := &e.slots[(e.frames+i)%encodeSlots]
		if !slot.busy {
			continue
		}
		p, err := e.collect(slot)
		if err != nil {
			return out, err
		}
		out = append(out, p)
	}
	return out, nil
}

// collect waits for slot's frame and takes its packet.
func (e *Encoder) collect(slot *encodeSlot) (Packet, error) {
	d := e.d
	if r := d.cmds.WaitForFences(d.device, 1, &slot.done, vk.True, ^uint64(0)); r != vk.Success {
		return Packet{}, fmt.Errorf("gpu: wait for an encode: %d", r)
	}
	slot.busy = false
	q := uint32(slot.offset / e.slotSize)
	var res [3]uint32
	flags := vk.QueryResultFlags(vk.QueryResultWithStatusBitKhr)
	if r := d.cmds.GetQueryPoolResults(d.device, e.queries, q, 1, unsafe.Sizeof(res), unsafe.Pointer(&res[0]), vk.DeviceSize(unsafe.Sizeof(res)), flags); r != vk.Success {
		return Packet{}, fmt.Errorf("gpu: vkGetQueryPoolResults (encode feedback): %d", r)
	}
	if status := vk.QueryResultStatusKHR(int32(res[2])); status != vk.QueryResultStatusCompleteKhr {
		return Packet{}, fmt.Errorf("gpu: the frame at %v did not encode: status %d", slot.pts, status)
	}
	offset, n := int(res[0]), int(res[1])
	if offset+n > e.slotSize {
		return Packet{}, fmt.Errorf("gpu: the device wrote %d bytes at %d into a %d-byte bitstream slot", n, offset, e.slotSize)
	}
	start := slot.offset + offset
	if err := e.bitstream.invalidate(); err != nil {
		return Packet{}, err
	}
	return Packet{Data: slices.Clone(e.bitstream.mapped[start : start+n]), PTS: slot.pts, Key: slot.key}, nil
}

// convert records and submits the NV12 conversion of src into slot's
// source picture, signalling slot.ready for the video queue.
func (e *Encoder) convert(src *Buffer, slot *encodeSlot) error {
	d := e.d
	view, err := src.view()
	if err != nil {
		return err
	}
	ii := [2]vk.DescriptorImageInfo{
		{Sampler: d.sampler, ImageView: view, ImageLayout: vk.ImageLayoutGeneral},
		{Sampler: e.linear, ImageView: view, ImageLayout: vk.ImageLayoutGeneral},
	}
	var over [4]int32
	if e.overlay != nil {
		ii[1] = vk.DescriptorImageInfo{Sampler: e.linear, ImageView: e.overlay.view, ImageLayout: vk.ImageLayoutShaderReadOnlyOptimal}
		r := e.overlayDst
		over = [4]int32{int32(r.Min.X), int32(r.Min.Y), int32(r.Max.X), int32(r.Max.Y)}
	}
	w := [2]vk.WriteDescriptorSet{
		{SType: vk.StructureTypeWriteDescriptorSet, DstSet: e.set, DstBinding: 0, DescriptorCount: 1, DescriptorType: vk.DescriptorTypeCombinedImageSampler, PImageInfo: &ii[0]},
		{SType: vk.StructureTypeWriteDescriptorSet, DstSet: e.set, DstBinding: 2, DescriptorCount: 1, DescriptorType: vk.DescriptorTypeCombinedImageSampler, PImageInfo: &ii[1]},
	}
	d.cmds.UpdateDescriptorSets(d.device, 2, &w[0], 0, nil)
	cb := e.gfxCB
	if err := begin(d, cb); err != nil {
		return err
	}
	foreign := imageBarrier(src.e.image, colorRange, vk.ImageLayoutGeneral, vk.ImageLayoutGeneral)
	foreign.SrcQueueFamilyIndex, foreign.DstQueueFamilyIndex = vk.QueueFamilyForeignExt, d.family
	foreign.DstStageMask, foreign.DstAccessMask = vk.PipelineStageFlags2(vk.PipelineStage2ComputeShaderBit), vk.AccessFlags2(vk.Access2ShaderSampledReadBit)
	barrier2(d, cb, nil, foreign)
	d.cmds.CmdBindPipeline(cb, vk.PipelineBindPointCompute, e.pipeline)
	d.cmds.CmdBindDescriptorSets(cb, vk.PipelineBindPointCompute, e.layout, 0, 1, &e.set, 0, nil)
	pc := nv12Constants{int32(e.cfg.Width), int32(e.cfg.Height), int32(e.coded[0]), int32(e.coded[1]), over}
	d.cmds.CmdPushConstants(cb, e.layout, vk.ShaderStageFlags(vk.ShaderStageComputeBit), 0, uint32(unsafe.Sizeof(pc)), unsafe.Pointer(&pc))
	d.cmds.CmdDispatch(cb, uint32((e.coded[0]/4+7)/8), uint32((e.coded[1]/2+7)/8), 1)
	planes := vk.MemoryBarrier2{
		SType:        vk.StructureTypeMemoryBarrier2,
		SrcStageMask: vk.PipelineStageFlags2(vk.PipelineStage2ComputeShaderBit), SrcAccessMask: vk.AccessFlags2(vk.Access2ShaderStorageWriteBit),
		DstStageMask: vk.PipelineStageFlags2(vk.PipelineStage2CopyBit), DstAccessMask: vk.AccessFlags2(vk.Access2TransferReadBit),
	}
	all := vk.ImageSubresourceRange{AspectMask: vk.ImageAspectFlags(vk.ImageAspectColorBit), LevelCount: 1, LayerCount: 1}
	toDst := imageBarrier(slot.src.image, all, vk.ImageLayoutUndefined, vk.ImageLayoutTransferDstOptimal)
	toDst.DstStageMask, toDst.DstAccessMask = vk.PipelineStageFlags2(vk.PipelineStage2CopyBit), vk.AccessFlags2(vk.Access2TransferWriteBit)
	release := imageBarrier(src.e.image, colorRange, vk.ImageLayoutGeneral, vk.ImageLayoutGeneral)
	release.SrcQueueFamilyIndex, release.DstQueueFamilyIndex = d.family, vk.QueueFamilyForeignExt
	release.SrcStageMask, release.SrcAccessMask = vk.PipelineStageFlags2(vk.PipelineStage2ComputeShaderBit), vk.AccessFlags2(vk.Access2ShaderSampledReadBit)
	barrier2(d, cb, &planes, toDst, release)
	cw, ch := uint32(e.coded[0]), uint32(e.coded[1])
	regions := [2]vk.BufferImageCopy{
		{BufferRowLength: cw, BufferImageHeight: ch, ImageSubresource: vk.ImageSubresourceLayers{AspectMask: vk.ImageAspectFlags(vk.ImageAspectPlane0Bit), LayerCount: 1}, ImageExtent: vk.Extent3D{Width: cw, Height: ch, Depth: 1}},
		{BufferOffset: vk.DeviceSize(cw * ch), BufferRowLength: cw / 2, BufferImageHeight: ch / 2, ImageSubresource: vk.ImageSubresourceLayers{AspectMask: vk.ImageAspectFlags(vk.ImageAspectPlane1Bit), LayerCount: 1}, ImageExtent: vk.Extent3D{Width: cw / 2, Height: ch / 2, Depth: 1}},
	}
	d.cmds.CmdCopyBufferToImage(cb, e.planes.buf, slot.src.image, vk.ImageLayoutTransferDstOptimal, 2, &regions[0])
	toSrc := imageBarrier(slot.src.image, all, vk.ImageLayoutTransferDstOptimal, vk.ImageLayoutVideoEncodeSrcKhr)
	toSrc.SrcStageMask, toSrc.SrcAccessMask = vk.PipelineStageFlags2(vk.PipelineStage2CopyBit), vk.AccessFlags2(vk.Access2TransferWriteBit)
	barrier2(d, cb, nil, toSrc)
	if r := d.cmds.EndCommandBuffer(cb); r != vk.Success {
		return fmt.Errorf("gpu: vkEndCommandBuffer (NV12): %d", r)
	}
	if err := resetFence(d, e.gfxDone); err != nil {
		return err
	}
	return submit2(d, d.queue, cb, nil, &vk.SemaphoreSubmitInfo{SType: vk.StructureTypeSemaphoreSubmitInfo, Semaphore: slot.ready, StageMask: vk.PipelineStageFlags2(vk.PipelineStage2AllCommandsBit)}, e.gfxDone)
}

// encode records and submits encoding slot's source picture as frame
// sinceIDR of its key interval, after slot.ready.
func (e *Encoder) encode(slot *encodeSlot, sinceIDR int) error {
	d := e.d
	cb := slot.cb
	if err := begin(d, cb); err != nil {
		return err
	}
	q := uint32(slot.offset / e.slotSize)
	d.cmds.CmdResetQueryPool(cb, e.queries, q, 1)
	dpbAll := vk.ImageSubresourceRange{AspectMask: vk.ImageAspectFlags(vk.ImageAspectColorBit), LevelCount: 1, LayerCount: dpbSlots}
	coding := vk.PipelineStageFlags2(vk.PipelineStage2VideoEncodeBitKhr)
	if e.frames == 0 {
		b := imageBarrier(e.dpb.image, dpbAll, vk.ImageLayoutUndefined, vk.ImageLayoutVideoEncodeDpbKhr)
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
	cur, prev := e.frames%dpbSlots, (e.frames+dpbSlots-1)%dpbSlots
	picInfo, setupInfo, refInfo := e.codec.picture(sinceIDR, e.idrs, prev, e.qp)
	extent := vk.Extent2D{Width: uint32(e.coded[0]), Height: uint32(e.coded[1])}
	res := [3]vk.VideoPictureResourceInfoKHR{
		{SType: vk.StructureTypeVideoPictureResourceInfoKhr, CodedExtent: extent, ImageViewBinding: slot.src.views[0]},
		{SType: vk.StructureTypeVideoPictureResourceInfoKhr, CodedExtent: extent, ImageViewBinding: e.dpb.views[cur]},
		{SType: vk.StructureTypeVideoPictureResourceInfoKhr, CodedExtent: extent, ImageViewBinding: e.dpb.views[prev]},
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
		SType: vk.StructureTypeVideoBeginCodingInfoKhr, VideoSession: e.session, VideoSessionParameters: e.sessionPar,
		ReferenceSlotCount: uint32(1 + refs), PReferenceSlots: &bound[0],
	}
	if e.rcSet {
		bi.PNext = e.rc.chain()
	}
	d.cmds.CmdBeginVideoCodingKHR(cb, &bi)
	if !e.rcSet {
		ctl := vk.VideoCodingControlInfoKHR{
			SType: vk.StructureTypeVideoCodingControlInfoKhr, PNext: e.rc.chain(),
			Flags: vk.VideoCodingControlFlagsKHR(vk.VideoCodingControlResetBitKhr | vk.VideoCodingControlEncodeRateControlBitKhr),
		}
		d.cmds.CmdControlVideoCodingKHR(cb, &ctl)
		e.rcSet = true
	}
	setup := vk.VideoReferenceSlotInfoKHR{SType: vk.StructureTypeVideoReferenceSlotInfoKhr, PNext: (*uintptr)(setupInfo), SlotIndex: int32(cur), PPictureResource: &res[1]}
	ref := vk.VideoReferenceSlotInfoKHR{SType: vk.StructureTypeVideoReferenceSlotInfoKhr, PNext: (*uintptr)(refInfo), SlotIndex: int32(prev), PPictureResource: &res[2]}
	ei := vk.VideoEncodeInfoKHR{
		SType: vk.StructureTypeVideoEncodeInfoKhr, PNext: (*uintptr)(picInfo),
		DstBuffer: e.bitstream.buf, DstBufferOffset: vk.DeviceSize(slot.offset), DstBufferRange: vk.DeviceSize(e.slotSize),
		SrcPictureResource: res[0], PSetupReferenceSlot: &setup, ReferenceSlotCount: uint32(refs), PReferenceSlots: &ref,
	}
	d.cmds.CmdBeginQuery(cb, e.queries, q, 0)
	d.cmds.CmdEncodeVideoKHR(cb, &ei)
	d.cmds.CmdEndQuery(cb, e.queries, q)
	end := vk.VideoEndCodingInfoKHR{SType: vk.StructureTypeVideoEndCodingInfoKhr}
	d.cmds.CmdEndVideoCodingKHR(cb, &end)
	runtime.KeepAlive(e.codec)
	runtime.KeepAlive(&res)
	runtime.KeepAlive(&bound)
	if r := d.cmds.EndCommandBuffer(cb); r != vk.Success {
		return fmt.Errorf("gpu: vkEndCommandBuffer (encode): %d", r)
	}
	if err := resetFence(d, slot.done); err != nil {
		return err
	}
	wait := vk.SemaphoreSubmitInfo{SType: vk.StructureTypeSemaphoreSubmitInfo, Semaphore: slot.ready, StageMask: coding}
	return submit2(d, d.video.queue, cb, &wait, nil, slot.done)
}

// Close waits for the frames in flight, dropping their packets, and
// releases the encoder; closing it again does nothing.
func (e *Encoder) Close() {
	if e.closed {
		return
	}
	e.closed = true
	d := e.d
	d.cmds.DeviceWaitIdle(d.device)
	for i := range e.slots {
		s := &e.slots[i]
		s.src.close(d)
		if s.ready != 0 {
			d.cmds.DestroySemaphore(d.device, s.ready, nil)
		}
		if s.done != 0 {
			d.cmds.DestroyFence(d.device, s.done, nil)
		}
	}
	e.dpb.close(d)
	if e.overlay != nil {
		e.overlay.close()
	}
	if e.overlayStaging != nil {
		e.overlayStaging.close()
	}
	if e.linear != 0 {
		d.cmds.DestroySampler(d.device, e.linear, nil)
	}
	if e.gfxDone != 0 {
		d.cmds.DestroyFence(d.device, e.gfxDone, nil)
	}
	for _, p := range []vk.CommandPool{e.gfxPool, e.videoPool} {
		if p != 0 {
			d.cmds.DestroyCommandPool(d.device, p, nil)
		}
	}
	if e.descPool != 0 {
		d.cmds.DestroyDescriptorPool(d.device, e.descPool, nil)
	}
	if e.pipeline != 0 {
		d.cmds.DestroyPipeline(d.device, e.pipeline, nil)
	}
	if e.module != 0 {
		d.cmds.DestroyShaderModule(d.device, e.module, nil)
	}
	if e.layout != 0 {
		d.cmds.DestroyPipelineLayout(d.device, e.layout, nil)
	}
	if e.setLayout != 0 {
		d.cmds.DestroyDescriptorSetLayout(d.device, e.setLayout, nil)
	}
	if e.queries != 0 {
		d.cmds.DestroyQueryPool(d.device, e.queries, nil)
	}
	if e.bitstream != nil {
		e.bitstream.close()
	}
	if e.planes != nil {
		e.planes.close()
	}
	if e.sessionPar != 0 {
		d.cmds.DestroyVideoSessionParametersKHR(d.device, e.sessionPar, nil)
	}
	if e.session != 0 {
		d.cmds.DestroyVideoSessionKHR(d.device, e.session, nil)
	}
	for _, m := range e.sessionMem {
		d.cmds.FreeMemory(d.device, m, nil)
	}
}

// rateControl is the rate control state the session codes at:
// constant QP where the device has it, else variable bitrate around a
// bitrate that matches QP 20 on desktop content. The GOP is flat: an
// IDR every key interval, P frames between.
type rateControl struct {
	layer vk.VideoEncodeRateControlLayerInfoKHR
	info  vk.VideoEncodeRateControlInfoKHR
}

// desktopBitsPerPixel is the variable-bitrate target per pixel a frame:
// what QP 20 averages on screen content.
const desktopBitsPerPixel = 0.1

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
	bitrate := min(uint64(float32(cfg.Width*cfg.Height*cfg.FrameRate)*desktopBitsPerPixel), caps.encode.MaxBitrate)
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
