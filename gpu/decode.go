package gpu

import (
	"errors"
	"fmt"
	"image"
	"runtime"
	"slices"
	"time"
	"unsafe"

	"github.com/stubbedev/lys-lib/fourcc"
	"github.com/stubbedev/lys-lib/intconv"
	"github.com/stubbedev/lys-lib/media/demux"
	"github.com/stubbedev/lys-lib/third_party/vk"
)

// ErrNoVideoDecoder reports a device that does not decode the codec
// asked for on a Vulkan video decode queue.
var ErrNoVideoDecoder = errors.New("gpu: the device has no video decoder")

// DecodeCodecs are the codecs NewVideoDecoder decodes on the device.
func (d *Device) DecodeCodecs() []demux.Codec {
	if d.decode == nil {
		return nil
	}
	return slices.Clone(d.decode.codecs)
}

// decodeSlots are the pictures in flight on the decode queue at once.
const decodeSlots = 4

// heldFrames are the frames a caller may hold at once, beside the ones
// the decoder holds back to put them in presentation order.
const heldFrames = 8

// VideoDecoder decodes a video track on the device's video decode
// queue into frames the renderer samples: Decode takes the track's
// packets in decode order, Next hands out frames in presentation order.
// A VideoDecoder belongs to its device's goroutine.
type VideoDecoder struct {
	d     *Device
	name  string
	op    vk.VideoCodecOperationFlagBitsKHR
	codec decodeCodec
	shape decodeShape

	// format and usage are the output pictures' and the DPB's; copies is
	// whether the device decodes into the DPB alone, each picture copied
	// out of it.
	format [2]vk.VideoFormatPropertiesKHR
	usage  [2]vk.ImageUsageFlags
	copies bool

	profile    *decodeProfile
	caps       *decodeCaps
	session    vk.VideoSessionKHR
	sessionMem []vk.DeviceMemory
	params     vk.VideoSessionParametersKHR
	dpb        videoImage
	dpbFresh   bool
	reset      bool

	pool      vk.CommandPool
	slots     [decodeSlots]decodeSlot
	submitted uint64
	completed uint64

	frames  []*VideoFrame
	pending reorder
	ready   []*VideoFrame
	closed  bool

	cmd decodeCommands
}

// decodeSlot is a picture in flight: its commands, the fence its decode
// signals, the bitstream it reads and the submission it is.
type decodeSlot struct {
	cb        vk.CommandBuffer
	done      vk.Fence
	bitstream *buffer
	seq       uint64
}

// decodeShape is what a video session is made for: the codec profile,
// the coded size in luma samples, the DPB slots and the references a
// picture may name, how many pictures presentation order holds back,
// how many decoded pictures the codec keeps hidden to show later, the
// pictures' sample depth and the YCbCr conversion they sample through.
type decodeShape struct {
	profile int
	coded   [2]int
	slots   int
	refs    int
	reorder int
	hidden  int
	depth   videoDepth
	ycbcr   YCbCr
}

// videoDepth is the sample depth of a stream's 4:2:0 pictures, which
// picks their format.
type videoDepth uint8

// The depths decoded.
const (
	depth8 videoDepth = iota
	depth10
)

// depthOf is the depth of bits-bit samples; false for one not decoded.
func depthOf(bits int) (videoDepth, bool) {
	switch bits {
	case 8:
		return depth8, true
	case 10:
		return depth10, true
	}
	return 0, false
}

// format is the depth's picture format: NV12, or P010 with 10 bits in
// the high bits of each 16.
func (b videoDepth) format() fourcc.Format {
	if b == depth10 {
		return fourcc.P010
	}
	return fourcc.NV12
}

// vk is the depth's picture format in Vulkan.
func (b videoDepth) vk() vk.Format { return pixelFormats[b.format()].vk }

// component is the depth as a video profile names it.
func (b videoDepth) component() vk.VideoComponentBitDepthFlagsKHR {
	if b == depth10 {
		return vk.VideoComponentBitDepthFlagsKHR(vk.VideoComponentBitDepth10BitKhr)
	}
	return vk.VideoComponentBitDepthFlagsKHR(vk.VideoComponentBitDepth8BitKhr)
}

// decodeCodec is what a decoder does differently per codec: it reads
// packets, keeps the reference state and lays out the codec's
// structures, which live in it for the pointers into them.
type decodeCodec interface {
	// decode reads one packet and decodes its pictures through v.
	decode(v *VideoDecoder, p demux.Packet) error
	// profiles is how many profiles the codec tries for the stream's
	// shape, best first; profile lays out profile i over next.
	profiles() int
	profile(i int, next unsafe.Pointer) unsafe.Pointer
	// caps is the codec's capability structure, chained last; checkCaps
	// refuses a stream the capabilities it filled in do not cover.
	caps() unsafe.Pointer
	checkCaps() error
	// parameters is the session parameters' create chain, nil for a
	// codec that has none (VP9).
	parameters() unsafe.Pointer
	// reset forgets every reference picture.
	reset()
}

// decodeProfile is the profile chain the session and its pictures are
// made with, kept on the heap for the pointers into it.
type decodeProfile struct {
	info vk.VideoProfileInfoKHR
	list vk.VideoProfileListInfoKHR
}

// decodeCaps are what the device decodes in a profile; the codec's own
// part is in the codec.
type decodeCaps struct {
	video  vk.VideoCapabilitiesKHR
	decode vk.VideoDecodeCapabilitiesKHR
}

// NewVideoDecoder makes a decoder for track, one of the device's
// DecodeCodecs; its configuration record is the stream's parameter
// sets. A codec the device does not decode is ErrNoVideoDecoder,
// naming the codec and why.
func (d *Device) NewVideoDecoder(track demux.Track) (*VideoDecoder, error) {
	if track.Kind != demux.Video {
		return nil, fmt.Errorf("gpu: track %d is %s, not video", track.Index, track.Kind)
	}
	if d.decode == nil || !slices.Contains(d.decode.codecs, track.Codec) {
		why, ok := d.decodeWhy[track.Codec]
		if !ok {
			why = errors.New("no Vulkan decoder for it")
		}
		return nil, fmt.Errorf("%w: %s decodes no %s (track %d): %w", ErrNoVideoDecoder, d.name, track.Codec, track.Index, why)
	}
	i := slices.IndexFunc(decodeCodecs, func(c decodeEntry) bool { return c.codec == track.Codec })
	codec, err := decodeCodecs[i].open(track.Config)
	if err != nil {
		return nil, fmt.Errorf("gpu: %s track %d: %w", track.Codec, track.Index, err)
	}
	v := &VideoDecoder{d: d, name: track.Codec.String(), op: decodeCodecs[i].op.operation, codec: codec, reset: true}
	if v.pool, err = d.commandPool(d.decode.family); err != nil {
		return nil, err
	}
	for i := range v.slots {
		s := &v.slots[i]
		if s.cb, err = d.commandBuffer(v.pool); err == nil {
			s.done, err = d.fence()
		}
		if err != nil {
			v.Close()
			return nil, err
		}
	}
	d.decoders[v] = struct{}{}
	return v, nil
}

// Decode decodes p, the track's next packet in decode order; the
// frames it completes come out of Next. A picture that predicts from
// pictures before the one decoding started at (after Reset, at a
// keyframe that is not an IDR) is dropped.
func (v *VideoDecoder) Decode(p demux.Packet) error {
	if v.closed {
		return fmt.Errorf("gpu: %s decode of a closed decoder", v.name)
	}
	if err := v.codec.decode(v, p); err != nil {
		return fmt.Errorf("gpu: %s packet at %v: %w", v.name, p.PTS, err)
	}
	return nil
}

// Next is the next frame in presentation order, nil when the packets
// decoded so far complete none: Decode more, or Flush at the end.
// It waits for the frame's decode to finish. The frame is the caller's
// until Release.
func (v *VideoDecoder) Next() (*VideoFrame, error) {
	if len(v.ready) == 0 {
		return nil, nil
	}
	f := v.ready[0]
	if err := v.wait(f.seq); err != nil {
		return nil, err
	}
	copy(v.ready, v.ready[1:])
	v.ready = v.ready[:len(v.ready)-1]
	f.state = frameHeld
	return f, nil
}

// Flush hands every frame held back for presentation order to Next:
// what the end of the track does.
func (v *VideoDecoder) Flush() {
	v.pending.drain(v.present)
}

// Reset drops every frame not yet handed out and every reference
// picture, for decoding to start again at a keyframe: what a seek
// does. Frames the caller holds stay valid until released.
func (v *VideoDecoder) Reset() error {
	if err := v.idle(); err != nil {
		return err
	}
	v.pending.drain(func(f *VideoFrame) { f.free(0) })
	for _, f := range v.ready {
		f.free(0)
	}
	v.ready = v.ready[:0]
	v.codec.reset()
	v.reset = true
	return nil
}

// Close releases the decoder and its frames, the ones the caller still
// holds too. Closing it again does nothing.
func (v *VideoDecoder) Close() {
	if v.closed {
		return
	}
	v.closed = true
	d := v.d
	delete(d.decoders, v)
	d.cmds.QueueWaitIdle(d.decode.queue)
	for _, f := range v.frames {
		f.destroy()
	}
	v.frames = nil
	for i := range v.slots {
		s := &v.slots[i]
		if s.done != 0 {
			d.cmds.DestroyFence(d.device, s.done, nil)
		}
		if s.bitstream != nil {
			s.bitstream.close()
		}
	}
	if v.pool != 0 {
		d.cmds.DestroyCommandPool(d.device, v.pool, nil)
	}
	v.closeSession()
}

// closeSession releases the session and what was made for its shape.
func (v *VideoDecoder) closeSession() {
	d := v.d
	v.dpb.close(d)
	if v.params != 0 {
		d.cmds.DestroyVideoSessionParametersKHR(d.device, v.params, nil)
		v.params = 0
	}
	if v.session != 0 {
		d.cmds.DestroyVideoSessionKHR(d.device, v.session, nil)
		v.session = 0
	}
	d.freeMemory(v.sessionMem)
	v.sessionMem = nil
}

// wait waits for decode submission seq to finish.
func (v *VideoDecoder) wait(seq uint64) error {
	if seq <= v.completed {
		return nil
	}
	d, s := v.d, &v.slots[(seq-1)%decodeSlots]
	d.blockingWaits++
	if r := d.cmds.WaitForFences(d.device, 1, &s.done, vk.True, intconv.Must[uint64](d.waitLimit.Nanoseconds())); r != vk.Success {
		if r == vk.ErrorDeviceLost {
			d.lost = true
		}
		return fmt.Errorf("gpu: wait for %s decode %d, %v at most: %w", v.name, seq, d.waitLimit, resultError(r))
	}
	v.completed = seq
	return nil
}

// idle waits for every decode in flight.
func (v *VideoDecoder) idle() error { return v.wait(v.submitted) }

// shapeFor makes the session for shape, when the one there is was made
// for another: at the stream's first picture and where its parameter
// sets change size or profile. The frames still to come out keep their
// pictures.
func (v *VideoDecoder) shapeFor(shape decodeShape) error {
	if v.session != 0 && shape == v.shape {
		return nil
	}
	if err := v.idle(); err != nil {
		return err
	}
	v.closeSession()
	v.shape = shape
	v.pending.depth = shape.reorder
	steps := []func() error{v.pickProfile, v.createSession, v.createDPB}
	for _, step := range steps {
		if err := step(); err != nil {
			v.closeSession()
			return err
		}
	}
	v.dpbFresh, v.reset = true, true
	return nil
}

// pickProfile takes the codec's best profile the device decodes the
// shape in.
func (v *VideoDecoder) pickProfile() error {
	d := v.d
	var last vk.Result
	v.profile = nil
	for i := range v.codec.profiles() {
		p := &decodeProfile{}
		p.info = vk.VideoProfileInfoKHR{
			SType: vk.StructureTypeVideoProfileInfoKhr, PNext: (*uintptr)(v.codec.profile(i, nil)), VideoCodecOperation: v.op,
			ChromaSubsampling: vk.VideoChromaSubsamplingFlagsKHR(vk.VideoChromaSubsampling420BitKhr),
			LumaBitDepth:      v.shape.depth.component(), ChromaBitDepth: v.shape.depth.component(),
		}
		p.list = vk.VideoProfileListInfoKHR{SType: vk.StructureTypeVideoProfileListInfoKhr, ProfileCount: 1, PProfiles: &p.info}
		c := &decodeCaps{}
		c.decode = vk.VideoDecodeCapabilitiesKHR{SType: vk.StructureTypeVideoDecodeCapabilitiesKhr, PNext: (*uintptr)(v.codec.caps())}
		c.video = vk.VideoCapabilitiesKHR{SType: vk.StructureTypeVideoCapabilitiesKhr, PNext: (*uintptr)(unsafe.Pointer(&c.decode))}
		r := d.cmds.GetPhysicalDeviceVideoCapabilitiesKHR(d.phys, &p.info, &c.video)
		runtime.KeepAlive(p)
		runtime.KeepAlive(v.codec)
		if r != vk.Success {
			last = r
			continue
		}
		v.profile, v.caps = p, c
		break
	}
	if v.profile == nil {
		return fmt.Errorf("%w: %s decodes none of the %s profiles for profile %d: %w", ErrNoVideoDecoder, d.name, v.name, v.shape.profile, resultError(last))
	}
	if err := v.codec.checkCaps(); err != nil {
		return fmt.Errorf("%w: %s: %w", ErrNoVideoDecoder, d.name, err)
	}
	c, s := &v.caps.video, v.shape
	distinct := v.caps.decode.Flags&vk.VideoDecodeCapabilityFlagsKHR(vk.VideoDecodeCapabilityDpbAndOutputDistinctBitKhr) != 0
	coincide := v.caps.decode.Flags&vk.VideoDecodeCapabilityFlagsKHR(vk.VideoDecodeCapabilityDpbAndOutputCoincideBitKhr) != 0
	switch {
	case !distinct && (!coincide || !d.decode.transfer):
		return fmt.Errorf("%w: %s decodes %s only into its reference pictures (capability flags %#x), on a queue family that copies no images out of them", ErrNoVideoDecoder, d.name, v.name, v.caps.decode.Flags)
	case s.coded[0] > int(c.MaxCodedExtent.Width) || s.coded[1] > int(c.MaxCodedExtent.Height):
		return fmt.Errorf("%w: %s decodes %s up to %dx%d, not %dx%d", ErrNoVideoDecoder, d.name, v.name, c.MaxCodedExtent.Width, c.MaxCodedExtent.Height, s.coded[0], s.coded[1])
	case s.coded[0] < int(c.MinCodedExtent.Width) || s.coded[1] < int(c.MinCodedExtent.Height):
		return fmt.Errorf("%w: %s decodes %s from %dx%d, not %dx%d", ErrNoVideoDecoder, d.name, v.name, c.MinCodedExtent.Width, c.MinCodedExtent.Height, s.coded[0], s.coded[1])
	case s.slots > int(c.MaxDpbSlots) || s.refs > int(c.MaxActiveReferencePictures):
		return fmt.Errorf("%w: %s decodes %s with %d DPB slots and %d references, not %d and %d", ErrNoVideoDecoder, d.name, v.name, c.MaxDpbSlots, c.MaxActiveReferencePictures, s.slots, s.refs)
	}
	v.copies = !distinct
	var err error
	if v.copies {
		v.usage = [2]vk.ImageUsageFlags{
			vk.ImageUsageFlags(vk.ImageUsageTransferDstBit | vk.ImageUsageSampledBit | vk.ImageUsageTransferSrcBit),
			vk.ImageUsageFlags(vk.ImageUsageVideoDecodeDpbBitKhr | vk.ImageUsageVideoDecodeDstBitKhr | vk.ImageUsageTransferSrcBit),
		}
		v.format[0] = vk.VideoFormatPropertiesKHR{Format: v.shape.depth.vk(), ImageType: vk.ImageType2d, ImageTiling: vk.ImageTilingOptimal}
		need := vk.FormatFeatureFlags(vk.FormatFeatureSampledImageBit | vk.FormatFeatureTransferDstBit)
		if v.sampledFeatures()&need != need {
			return fmt.Errorf("%w: %s neither samples nor copies into %s images", ErrNoVideoDecoder, d.name, v.shape.depth.format())
		}
	} else {
		v.usage = [2]vk.ImageUsageFlags{
			vk.ImageUsageFlags(vk.ImageUsageVideoDecodeDstBitKhr | vk.ImageUsageSampledBit | vk.ImageUsageTransferSrcBit),
			vk.ImageUsageFlags(vk.ImageUsageVideoDecodeDpbBitKhr),
		}
		if v.format[0], err = d.videoFormat(&v.profile.list, v.usage[0], v.shape.depth); err != nil {
			return err
		}
	}
	v.format[1], err = d.videoFormat(&v.profile.list, v.usage[1], v.shape.depth)
	return err
}

// videoFormat finds the properties of depth's format among the formats
// the device codes pictures of usage in, in the profiles list names.
func (d *Device) videoFormat(list *vk.VideoProfileListInfoKHR, usage vk.ImageUsageFlags, depth videoDepth) (vk.VideoFormatPropertiesKHR, error) {
	fi := vk.PhysicalDeviceVideoFormatInfoKHR{SType: vk.StructureTypePhysicalDeviceVideoFormatInfoKhr, PNext: (*uintptr)(unsafe.Pointer(list)), ImageUsage: usage}
	var n uint32
	if r := d.cmds.GetPhysicalDeviceVideoFormatPropertiesKHR(d.phys, &fi, &n, nil); r != vk.Success {
		return vk.VideoFormatPropertiesKHR{}, fmt.Errorf("gpu: vkGetPhysicalDeviceVideoFormatPropertiesKHR usage %#x: %w", usage, resultError(r))
	}
	props := make([]vk.VideoFormatPropertiesKHR, n)
	for i := range props {
		props[i].SType = vk.StructureTypeVideoFormatPropertiesKhr
	}
	if n > 0 {
		if r := d.cmds.GetPhysicalDeviceVideoFormatPropertiesKHR(d.phys, &fi, &n, &props[0]); r != vk.Success {
			return vk.VideoFormatPropertiesKHR{}, fmt.Errorf("gpu: vkGetPhysicalDeviceVideoFormatPropertiesKHR usage %#x: %w", usage, resultError(r))
		}
	}
	runtime.KeepAlive(list)
	for _, p := range props[:n] {
		if p.Format == depth.vk() && p.ImageType == vk.ImageType2d {
			return p, nil
		}
	}
	return vk.VideoFormatPropertiesKHR{}, fmt.Errorf("%w: %s codes no %s pictures for usage %#x", ErrNoVideoDecoder, d.name, depth.format(), usage)
}

// createSession makes the video session, binds its memory and makes
// its parameters.
func (v *VideoDecoder) createSession() error {
	d := v.d
	extent := v.extent()
	sci := vk.VideoSessionCreateInfoKHR{
		SType: vk.StructureTypeVideoSessionCreateInfoKhr, QueueFamilyIndex: d.decode.family, PVideoProfile: &v.profile.info,
		PictureFormat: v.shape.depth.vk(), MaxCodedExtent: extent2D(extent[0], extent[1]), ReferencePictureFormat: v.shape.depth.vk(),
		MaxDpbSlots: intconv.Must[uint32](v.shape.slots), MaxActiveReferencePictures: intconv.Must[uint32](v.shape.refs),
		PStdHeaderVersion: &v.caps.video.StdHeaderVersion,
	}
	r := d.cmds.CreateVideoSessionKHR(d.device, &sci, nil, &v.session)
	runtime.KeepAlive(v.profile)
	runtime.KeepAlive(v.caps)
	if r != vk.Success {
		return fmt.Errorf("gpu: vkCreateVideoSessionKHR %s %dx%d: %w", v.name, extent[0], extent[1], resultError(r))
	}
	var err error
	if v.sessionMem, err = d.bindVideoSession(v.session); err != nil {
		return err
	}
	return v.createParameters()
}

// createParameters makes the session parameters of the stream's
// parameter sets, replacing the ones there were.
func (v *VideoDecoder) createParameters() error {
	d := v.d
	if v.params != 0 {
		if err := v.idle(); err != nil {
			return err
		}
		d.cmds.DestroyVideoSessionParametersKHR(d.device, v.params, nil)
		v.params = 0
	}
	next := v.codec.parameters()
	if next == nil {
		return nil
	}
	pci := vk.VideoSessionParametersCreateInfoKHR{SType: vk.StructureTypeVideoSessionParametersCreateInfoKhr, PNext: (*uintptr)(next), VideoSession: v.session}
	r := d.cmds.CreateVideoSessionParametersKHR(d.device, &pci, nil, &v.params)
	runtime.KeepAlive(v.codec)
	if r != vk.Success {
		return fmt.Errorf("gpu: vkCreateVideoSessionParametersKHR %s: %w", v.name, resultError(r))
	}
	return nil
}

// bindVideoSession allocates and binds the memory session needs.
func (d *Device) bindVideoSession(session vk.VideoSessionKHR) ([]vk.DeviceMemory, error) {
	var n uint32
	if r := d.cmds.GetVideoSessionMemoryRequirementsKHR(d.device, session, &n, nil); r != vk.Success {
		return nil, fmt.Errorf("gpu: vkGetVideoSessionMemoryRequirementsKHR: %w", resultError(r))
	}
	reqs := make([]vk.VideoSessionMemoryRequirementsKHR, n)
	for i := range reqs {
		reqs[i].SType = vk.StructureTypeVideoSessionMemoryRequirementsKhr
	}
	if n > 0 {
		if r := d.cmds.GetVideoSessionMemoryRequirementsKHR(d.device, session, &n, &reqs[0]); r != vk.Success {
			return nil, fmt.Errorf("gpu: vkGetVideoSessionMemoryRequirementsKHR: %w", resultError(r))
		}
	}
	mems := make([]vk.DeviceMemory, 0, n)
	binds := make([]vk.BindVideoSessionMemoryInfoKHR, 0, n)
	for _, req := range reqs[:n] {
		mem, err := d.allocate(req.MemoryRequirements, vk.MemoryPropertyDeviceLocalBit, nil)
		if errors.Is(err, errNoMemoryType) {
			mem, err = d.allocate(req.MemoryRequirements, 0, nil)
		}
		if err != nil {
			d.freeMemory(mems)
			return nil, fmt.Errorf("gpu: video session memory %d: %w", req.MemoryBindIndex, err)
		}
		mems = append(mems, mem)
		binds = append(binds, vk.BindVideoSessionMemoryInfoKHR{
			SType: vk.StructureTypeBindVideoSessionMemoryInfoKhr, MemoryBindIndex: req.MemoryBindIndex, Memory: mem, MemorySize: req.MemoryRequirements.Size,
		})
	}
	if len(binds) > 0 {
		if r := d.cmds.BindVideoSessionMemoryKHR(d.device, session, count(binds), &binds[0]); r != vk.Success {
			d.freeMemory(mems)
			return nil, fmt.Errorf("gpu: vkBindVideoSessionMemoryKHR: %w", resultError(r))
		}
	}
	return mems, nil
}

// extent is the size of the session's pictures: the coded size rounded
// up to the device's picture access granularity.
func (v *VideoDecoder) extent() [2]int {
	g := v.caps.video.PictureAccessGranularity
	return [2]int{alignUp(v.shape.coded[0], max(int(g.Width), 1)), alignUp(v.shape.coded[1], max(int(g.Height), 1))}
}

// createDPB makes the reference pictures, a layer per DPB slot.
func (v *VideoDecoder) createDPB() error {
	img, err := v.newPicture(v.format[1], v.usage[1], v.shape.slots, false)
	if err != nil {
		return fmt.Errorf("gpu: %s DPB: %w", v.name, err)
	}
	v.dpb = img
	return nil
}

// decodeCoding are the usages of a picture the coding commands name.
const decodeCoding = vk.ImageUsageFlags(vk.ImageUsageVideoDecodeDstBitKhr | vk.ImageUsageVideoDecodeDpbBitKhr)

// newPicture makes a picture of the session's size and depth with layers
// array layers, laid out as format says, shared with the graphics queue
// when shared. A picture the coding commands name (usage has a video
// decode usage) is made for the session's profile, with a view of each
// layer for them.
func (v *VideoDecoder) newPicture(format vk.VideoFormatPropertiesKHR, usage vk.ImageUsageFlags, layers int, shared bool) (videoImage, error) {
	d := v.d
	extent := v.extent()
	ici := vk.ImageCreateInfo{
		SType: vk.StructureTypeImageCreateInfo, Flags: format.ImageCreateFlags,
		ImageType: vk.ImageType2d, Format: v.shape.depth.vk(), Extent: extent3D(extent[0], extent[1]),
		MipLevels: 1, ArrayLayers: intconv.Must[uint32](layers), Samples: vk.SampleCount1Bit, Tiling: format.ImageTiling,
		Usage: usage, SharingMode: vk.SharingModeExclusive, InitialLayout: vk.ImageLayoutUndefined,
	}
	if usage&decodeCoding != 0 {
		ici.PNext = (*uintptr)(unsafe.Pointer(&v.profile.list))
	}
	families := [2]uint32{d.family, d.decode.family}
	if shared && families[0] != families[1] {
		ici.SharingMode, ici.QueueFamilyIndexCount, ici.PQueueFamilyIndices = vk.SharingModeConcurrent, 2, &families[0]
	}
	var vi videoImage
	r := d.cmds.CreateImage(d.device, &ici, nil, &vi.image)
	runtime.KeepAlive(v.profile)
	runtime.KeepAlive(&families)
	if r != vk.Success {
		return videoImage{}, fmt.Errorf("vkCreateImage %s %dx%d x%d: %w", v.shape.depth.format(), extent[0], extent[1], layers, resultError(r))
	}
	var req vk.MemoryRequirements
	d.cmds.GetImageMemoryRequirements(d.device, vi.image, &req)
	mem, err := d.allocate(req, vk.MemoryPropertyDeviceLocalBit, nil)
	if err != nil {
		vi.close(d)
		return videoImage{}, err
	}
	vi.memory = mem
	if r := d.cmds.BindImageMemory(d.device, vi.image, vi.memory, 0); r != vk.Success {
		vi.close(d)
		return videoImage{}, fmt.Errorf("vkBindImageMemory: %w", resultError(r))
	}
	codingUsage := usage & decodeCoding
	if codingUsage == 0 {
		return vi, nil
	}
	viewUsage := vk.ImageViewUsageCreateInfo{SType: vk.StructureTypeImageViewUsageCreateInfo, Usage: codingUsage}
	for l := range layers {
		vci := vk.ImageViewCreateInfo{
			SType: vk.StructureTypeImageViewCreateInfo, PNext: (*uintptr)(unsafe.Pointer(&viewUsage)), Image: vi.image, ViewType: vk.ImageViewType2d, Format: v.shape.depth.vk(),
			SubresourceRange: vk.ImageSubresourceRange{AspectMask: vk.ImageAspectFlags(vk.ImageAspectColorBit), LevelCount: 1, BaseArrayLayer: uint32(l), LayerCount: 1},
		}
		var view vk.ImageView
		r := d.cmds.CreateImageView(d.device, &vci, nil, &view)
		runtime.KeepAlive(&viewUsage)
		if r != vk.Success {
			vi.close(d)
			return videoImage{}, fmt.Errorf("vkCreateImageView %s layer %d: %w", v.shape.depth.format(), l, resultError(r))
		}
		vi.views = append(vi.views, view)
	}
	return vi, nil
}

// VideoFrame is a decoded picture the renderer samples: Texture, drawn
// through its YCbCr conversion, with the picture in Rect.
type VideoFrame struct {
	v        *VideoDecoder
	tex      *Texture
	pic      videoImage
	extent   [2]int
	ycbcr    YCbCr
	rect     image.Rectangle
	pts      time.Duration
	duration time.Duration
	// seq is the decode submission that writes the picture; after the
	// device submission after which the renderer no longer samples it.
	seq   uint64
	after uint64
	state frameState
}

// frameState is where a frame's picture is.
type frameState uint8

const (
	// frameFree pictures take the next decode.
	frameFree frameState = iota
	// frameQueued pictures are decoding or decoded, waiting for Next.
	frameQueued
	// frameHeld pictures are the caller's.
	frameHeld
)

// PTS is when the frame presents, its packet's PTS.
func (f *VideoFrame) PTS() time.Duration { return f.pts }

// Duration is how long the frame presents, its packet's; zero when
// the container does not say.
func (f *VideoFrame) Duration() time.Duration { return f.duration }

// Texture is the picture as a texture a Pass draws (DrawTexture), its
// YCbCr matrix, range and chroma siting the stream's. It is larger
// than the picture by the codec's padding: draw Rect of it. It belongs
// to the decoder: neither Update nor Close it.
func (f *VideoFrame) Texture() *Texture { return f.tex }

// Rect is the picture within Texture, the stream's cropping applied.
func (f *VideoFrame) Rect() image.Rectangle { return f.rect }

// Release hands the frame back for the decoder to decode into once the
// passes submitted so far, which may sample it, finish. The frame is
// not to be used after.
func (f *VideoFrame) Release() {
	if f.state != frameHeld {
		return
	}
	d := f.v.d
	after := d.submitted
	if d.batch != 0 || d.framing {
		after++
	}
	f.free(after)
}

func (f *VideoFrame) free(after uint64) {
	f.state, f.after = frameFree, after
}

// destroy releases the picture once the renderer is done with it.
func (f *VideoFrame) destroy() {
	d := f.v.d
	for _, view := range f.pic.views {
		d.cmds.DestroyImageView(d.device, view, nil)
	}
	f.pic.views = nil
	if f.tex != nil {
		d.retire(f.tex)
		f.tex = nil
	}
}

// frame is a free frame of the session's size to decode into: one the
// renderer is done with, else a new one, else, with as many as the
// decoder may hold, the first the renderer finishes with.
func (v *VideoDecoder) frame() (*VideoFrame, error) {
	d := v.d
	extent := v.extent()
	pick := func() *VideoFrame {
		for i := 0; i < len(v.frames); i++ {
			f := v.frames[i]
			if f.state != frameFree || f.after > d.completed {
				continue
			}
			if f.extent == extent && f.ycbcr == v.shape.ycbcr && f.tex.format == v.shape.depth.format() {
				return f
			}
			f.destroy()
			v.frames = slices.Delete(v.frames, i, i+1)
			i--
		}
		return nil
	}
	if f := pick(); f != nil {
		return f, nil
	}
	d.reap(false)
	if f := pick(); f != nil {
		return f, nil
	}
	if len(v.frames) < v.shape.reorder+v.shape.hidden+1+heldFrames {
		return v.newFrame()
	}
	soonest := uint64(0)
	for _, f := range v.frames {
		if f.state == frameFree && (soonest == 0 || f.after < soonest) {
			soonest = f.after
		}
	}
	if soonest == 0 {
		return nil, fmt.Errorf("gpu: %s: all %d frames are held or waiting for Next; release frames and call Next", v.name, len(v.frames))
	}
	if err := d.flushBatch(); err != nil {
		return nil, err
	}
	for soonest > d.completed {
		if soonest > d.submitted || len(d.inflight) == 0 {
			return nil, fmt.Errorf("gpu: %s: every free frame waits on a pass not yet submitted", v.name)
		}
		if err := d.waitOldest(); err != nil {
			return nil, err
		}
	}
	return pick(), nil
}

// newFrame makes a picture the decoder writes and the renderer samples
// through the stream's YCbCr conversion.
func (v *VideoDecoder) newFrame() (*VideoFrame, error) {
	d := v.d
	pic, err := v.newPicture(v.format[0], v.usage[0], 1, true)
	if err != nil {
		return nil, fmt.Errorf("gpu: %s frame: %w", v.name, err)
	}
	extent := v.extent()
	format := v.shape.depth.format()
	pf := pixelFormats[format]
	t := &Texture{d: d, format: format, pf: pf, w: extent[0], h: extent[1], image: pic.image, memory: []vk.DeviceMemory{pic.memory}, layout: vk.ImageLayoutShaderReadOnlyOptimal}
	f := &VideoFrame{v: v, pic: videoImage{views: pic.views}, extent: extent, ycbcr: v.shape.ycbcr}
	if t.ycbcr, err = d.ycbcrKindOf(pf, v.shape.ycbcr, v.sampledFeatures()); err == nil {
		err = t.createView()
	}
	f.tex = t
	if err != nil {
		f.destroy()
		return nil, fmt.Errorf("gpu: %s frame: %w", v.name, err)
	}
	v.frames = append(v.frames, f)
	return f, nil
}

// sampledFeatures are what the device does with the pictures' format in
// the tiling the decoder's output pictures have.
func (v *VideoDecoder) sampledFeatures() vk.FormatFeatureFlags {
	var props vk.FormatProperties
	v.d.cmds.GetPhysicalDeviceFormatProperties(v.d.phys, v.shape.depth.vk(), &props)
	if v.format[0].ImageTiling == vk.ImageTilingLinear {
		return props.LinearTilingFeatures
	}
	return props.OptimalTilingFeatures
}

// stream is the bitstream buffer of the next decode submission, n
// bytes of it ready to write: it waits for the slot's last decode.
func (v *VideoDecoder) stream(n int) ([]byte, error) {
	s := &v.slots[v.submitted%decodeSlots]
	if err := v.wait(s.seq); err != nil {
		return nil, err
	}
	size := alignUp(n, v.streamAlign())
	if s.bitstream == nil || len(s.bitstream.mapped) < size {
		if s.bitstream != nil {
			s.bitstream.close()
			s.bitstream = nil
		}
		b, err := v.d.newMappedBuffer(max(2*size, 1<<20), vk.BufferUsageVideoDecodeSrcBitKhr, unsafe.Pointer(&v.profile.list), vk.MemoryPropertyHostVisibleBit|vk.MemoryPropertyHostCoherentBit)
		runtime.KeepAlive(v.profile)
		if err != nil {
			return nil, fmt.Errorf("gpu: %s bitstream: %w", v.name, err)
		}
		s.bitstream = b
	}
	clear(s.bitstream.mapped[n:size])
	return s.bitstream.mapped[:n], nil
}

// streamAlign is the unit a bitstream range is a multiple of.
func (v *VideoDecoder) streamAlign() int {
	return max(intconv.Must[int](v.caps.video.MinBitstreamBufferSizeAlignment), 1)
}

// decodePicture is one picture to decode: the frame it decodes into,
// the bytes of the stream buffer it reads, the codec's picture info,
// the DPB slot it sets up and the slots it references, each with the
// codec's DPB slot info.
type decodePicture struct {
	frame     *VideoFrame
	bytes     int
	info      unsafe.Pointer
	setup     int
	setupInfo unsafe.Pointer
	refs      []int
	refInfo   []unsafe.Pointer
}

// submit records and submits decoding p into its frame, reading the
// stream buffer stream returned.
func (v *VideoDecoder) submit(p *decodePicture) error {
	d, c := v.d, &v.cmd
	s := &v.slots[v.submitted%decodeSlots]
	cb := s.cb
	if err := begin(d, cb); err != nil {
		return err
	}
	v.barriersBefore(cb, p)
	extent := extent2D(v.shape.coded[0], v.shape.coded[1])
	resource := func(view vk.ImageView) vk.VideoPictureResourceInfoKHR {
		return vk.VideoPictureResourceInfoKHR{SType: vk.StructureTypeVideoPictureResourceInfoKhr, CodedExtent: extent, ImageViewBinding: view}
	}
	n := len(p.refs)
	c.res[0] = resource(v.dpb.views[p.setup])
	c.bound[0] = vk.VideoReferenceSlotInfoKHR{SType: vk.StructureTypeVideoReferenceSlotInfoKhr, SlotIndex: -1, PPictureResource: &c.res[0]}
	for i, slot := range p.refs {
		c.res[1+i] = resource(v.dpb.views[slot])
		c.bound[1+i] = vk.VideoReferenceSlotInfoKHR{SType: vk.StructureTypeVideoReferenceSlotInfoKhr, PNext: (*uintptr)(p.refInfo[i]), SlotIndex: intconv.Must[int32](slot), PPictureResource: &c.res[1+i]}
	}
	c.begin = vk.VideoBeginCodingInfoKHR{
		SType: vk.StructureTypeVideoBeginCodingInfoKhr, VideoSession: v.session, VideoSessionParameters: v.params,
		ReferenceSlotCount: intconv.Must[uint32](1 + n), PReferenceSlots: &c.bound[0],
	}
	d.cmds.CmdBeginVideoCodingKHR(cb, &c.begin)
	if v.reset {
		c.control = vk.VideoCodingControlInfoKHR{SType: vk.StructureTypeVideoCodingControlInfoKhr, Flags: vk.VideoCodingControlFlagsKHR(vk.VideoCodingControlResetBitKhr)}
		d.cmds.CmdControlVideoCodingKHR(cb, &c.control)
		v.reset = false
	}
	c.setup = vk.VideoReferenceSlotInfoKHR{SType: vk.StructureTypeVideoReferenceSlotInfoKhr, PNext: (*uintptr)(p.setupInfo), SlotIndex: intconv.Must[int32](p.setup), PPictureResource: &c.res[0]}
	c.decode = vk.VideoDecodeInfoKHR{
		SType: vk.StructureTypeVideoDecodeInfoKhr, PNext: (*uintptr)(p.info),
		SrcBuffer: s.bitstream.buf, SrcBufferRange: intconv.Must[vk.DeviceSize](alignUp(p.bytes, v.streamAlign())),
		DstPictureResource: c.res[0], PSetupReferenceSlot: &c.setup,
	}
	if !v.copies {
		c.decode.DstPictureResource = resource(p.frame.pic.views[0])
	}
	if n > 0 {
		c.decode.ReferenceSlotCount, c.decode.PReferenceSlots = intconv.Must[uint32](n), &c.bound[1]
	}
	d.cmds.CmdDecodeVideoKHR(cb, &c.decode)
	c.end = vk.VideoEndCodingInfoKHR{SType: vk.StructureTypeVideoEndCodingInfoKhr}
	d.cmds.CmdEndVideoCodingKHR(cb, &c.end)
	v.barriersAfter(cb, p)
	if r := d.cmds.EndCommandBuffer(cb); r != vk.Success {
		return fmt.Errorf("gpu: vkEndCommandBuffer (%s decode): %w", v.name, resultError(r))
	}
	if err := resetFence(d, s.done); err != nil {
		return err
	}
	if err := submit2(d, d.decode.queue, cb, nil, nil, s.done); err != nil {
		return err
	}
	v.submitted++
	s.seq = v.submitted
	p.frame.seq, p.frame.state = s.seq, frameQueued
	return nil
}

// decodeCommands are the structures the coding commands take, kept in
// the decoder: what a Vulkan command points at escapes to the heap, so
// each picture's would be allocations. The barriers and the submission
// go through barrier2 and submit2, which keep theirs in the device.
type decodeCommands struct {
	res     [maxDPBSlots + 1]vk.VideoPictureResourceInfoKHR
	bound   [maxDPBSlots + 1]vk.VideoReferenceSlotInfoKHR
	setup   vk.VideoReferenceSlotInfoKHR
	begin   vk.VideoBeginCodingInfoKHR
	control vk.VideoCodingControlInfoKHR
	decode  vk.VideoDecodeInfoKHR
	end     vk.VideoEndCodingInfoKHR
	regions [2]vk.ImageCopy
}

// barriersBefore records what p's decode waits for: the DPB in its
// layout once, the decodes and copies before it done with the DPB, and
// its output picture made ready to write, its contents dropped.
func (v *VideoDecoder) barriersBefore(cb vk.CommandBuffer, p *decodePicture) {
	coding := vk.PipelineStageFlags2(vk.PipelineStage2VideoDecodeBitKhr)
	readWrite := vk.AccessFlags2(vk.Access2VideoDecodeReadBitKhr | vk.Access2VideoDecodeWriteBitKhr)
	var images [2]vk.ImageMemoryBarrier2
	n := 0
	if !v.copies {
		images[n] = imageBarrier(p.frame.tex.image, colorRange, vk.ImageLayoutUndefined, vk.ImageLayoutVideoDecodeDstKhr)
		images[n].DstStageMask, images[n].DstAccessMask = coding, vk.AccessFlags2(vk.Access2VideoDecodeWriteBitKhr)
		n++
	}
	if v.dpbFresh {
		dpbAll := vk.ImageSubresourceRange{AspectMask: vk.ImageAspectFlags(vk.ImageAspectColorBit), LevelCount: 1, LayerCount: intconv.Must[uint32](v.shape.slots)}
		images[n] = imageBarrier(v.dpb.image, dpbAll, vk.ImageLayoutUndefined, vk.ImageLayoutVideoDecodeDpbKhr)
		images[n].DstStageMask, images[n].DstAccessMask = coding, readWrite
		n++
		v.dpbFresh = false
	}
	done := vk.MemoryBarrier2{
		SType:        vk.StructureTypeMemoryBarrier2,
		SrcStageMask: coding | vk.PipelineStageFlags2(vk.PipelineStage2CopyBit), SrcAccessMask: vk.AccessFlags2(vk.Access2VideoDecodeWriteBitKhr | vk.Access2TransferReadBit),
		DstStageMask: coding, DstAccessMask: readWrite,
	}
	barrier2(v.d, cb, &done, images[:n]...)
}

// barriersAfter records what follows p's decode: with the DPB the
// output, the picture copied out of its DPB slot into the frame; the
// frame made ready to sample.
func (v *VideoDecoder) barriersAfter(cb vk.CommandBuffer, p *decodePicture) {
	d, c := v.d, &v.cmd
	coding := vk.PipelineStageFlags2(vk.PipelineStage2VideoDecodeBitKhr)
	frame := p.frame.tex.image
	if !v.copies {
		decoded := imageBarrier(frame, colorRange, vk.ImageLayoutVideoDecodeDstKhr, vk.ImageLayoutShaderReadOnlyOptimal)
		decoded.SrcStageMask, decoded.SrcAccessMask = coding, vk.AccessFlags2(vk.Access2VideoDecodeWriteBitKhr)
		barrier2(d, cb, nil, decoded)
		return
	}
	copying := vk.PipelineStageFlags2(vk.PipelineStage2CopyBit)
	layer := vk.ImageSubresourceRange{AspectMask: vk.ImageAspectFlags(vk.ImageAspectColorBit), LevelCount: 1, BaseArrayLayer: intconv.Must[uint32](p.setup), LayerCount: 1}
	from := imageBarrier(v.dpb.image, layer, vk.ImageLayoutVideoDecodeDpbKhr, vk.ImageLayoutTransferSrcOptimal)
	from.SrcStageMask, from.SrcAccessMask = coding, vk.AccessFlags2(vk.Access2VideoDecodeWriteBitKhr)
	from.DstStageMask, from.DstAccessMask = copying, vk.AccessFlags2(vk.Access2TransferReadBit)
	to := imageBarrier(frame, colorRange, vk.ImageLayoutUndefined, vk.ImageLayoutTransferDstOptimal)
	to.DstStageMask, to.DstAccessMask = copying, vk.AccessFlags2(vk.Access2TransferWriteBit)
	barrier2(d, cb, nil, from, to)
	w, h := v.shape.coded[0], v.shape.coded[1]
	plane := func(aspect vk.ImageAspectFlagBits, layer uint32) vk.ImageSubresourceLayers {
		return vk.ImageSubresourceLayers{AspectMask: flags[vk.ImageAspectFlags](aspect), BaseArrayLayer: layer, LayerCount: 1}
	}
	src := intconv.Must[uint32](p.setup)
	c.regions = [2]vk.ImageCopy{
		{SrcSubresource: plane(vk.ImageAspectPlane0Bit, src), DstSubresource: plane(vk.ImageAspectPlane0Bit, 0), Extent: extent3D(w, h)},
		{SrcSubresource: plane(vk.ImageAspectPlane1Bit, src), DstSubresource: plane(vk.ImageAspectPlane1Bit, 0), Extent: extent3D((w+1)/2, (h+1)/2)},
	}
	d.cmds.CmdCopyImage(cb, v.dpb.image, vk.ImageLayoutTransferSrcOptimal, frame, vk.ImageLayoutTransferDstOptimal, 2, &c.regions[0])
	from = imageBarrier(v.dpb.image, layer, vk.ImageLayoutTransferSrcOptimal, vk.ImageLayoutVideoDecodeDpbKhr)
	from.SrcStageMask, from.SrcAccessMask = copying, vk.AccessFlags2(vk.Access2TransferReadBit)
	to = imageBarrier(frame, colorRange, vk.ImageLayoutTransferDstOptimal, vk.ImageLayoutShaderReadOnlyOptimal)
	to.SrcStageMask, to.SrcAccessMask = copying, vk.AccessFlags2(vk.Access2TransferWriteBit)
	barrier2(d, cb, nil, from, to)
}

// queue puts f, decoded, among the frames waiting for presentation
// order at order. A boundary (an IDR) first hands every frame before
// it to Next, or with discard drops them.
func (v *VideoDecoder) queue(f *VideoFrame, order int32, boundary, discard bool) {
	if boundary {
		if discard {
			v.pending.drain(func(f *VideoFrame) { f.free(0) })
		} else {
			v.pending.drain(v.present)
		}
	}
	v.pending.add(f, order, v.present)
}

// present hands f to Next.
func (v *VideoDecoder) present(f *VideoFrame) { v.ready = append(v.ready, f) }

// reorder holds decoded frames back until depth others follow, to hand
// them out by picture order.
type reorder struct {
	depth  int
	frames []*VideoFrame
	orders []int32
}

// add queues f at order and hands out the frames past depth, lowest
// order first.
func (r *reorder) add(f *VideoFrame, order int32, out func(*VideoFrame)) {
	r.frames, r.orders = append(r.frames, f), append(r.orders, order)
	for len(r.frames) > r.depth {
		r.pop(out)
	}
}

// drain hands out every frame, lowest order first.
func (r *reorder) drain(out func(*VideoFrame)) {
	for len(r.frames) > 0 {
		r.pop(out)
	}
}

func (r *reorder) pop(out func(*VideoFrame)) {
	i := 0
	for j, o := range r.orders {
		if o < r.orders[i] {
			i = j
		}
	}
	f := r.frames[i]
	r.frames, r.orders = slices.Delete(r.frames, i, i+1), slices.Delete(r.orders, i, i+1)
	out(f)
}

// videoSignal is what an H.264 or H.265 VUI, or an AV1 colour config,
// says of how its pictures convert to RGB: the video signal type
// (typed), the colour description (described) and the chroma location
// (sited), each with its flag, and the picture's height in lines.
type videoSignal struct {
	typed, fullRange bool
	described        bool
	matrix           uint8
	sited            bool
	siting           uint8
	lines            int
}

// ycbcr is the conversion s names; an unnamed matrix is BT.709 from
// 720 lines up, BT.601 below.
func (s videoSignal) ycbcr() (YCbCr, error) {
	var c YCbCr
	matrix := uint8(2)
	if s.typed {
		if s.fullRange {
			c.Range = FullRange
		}
		if s.described {
			matrix = s.matrix
		}
	}
	switch matrix {
	case 1:
		c.Matrix = BT709
	case 5, 6:
		c.Matrix = BT601
	case 9, 10:
		c.Matrix = BT2020
	case 2:
		if s.lines >= 720 {
			c.Matrix = BT709
		}
	default:
		return YCbCr{}, fmt.Errorf("matrix_coefficients %d, which the renderer does not convert", matrix)
	}
	if !s.sited {
		return c, nil
	}
	switch s.siting {
	case 0:
		c.Chroma = ChromaSiting{SitedAt0, SitedAtHalf}
	case 1:
		c.Chroma = ChromaSiting{SitedAtHalf, SitedAtHalf}
	case 2:
		c.Chroma = ChromaSiting{SitedAt0, SitedAt0}
	case 3:
		c.Chroma = ChromaSiting{SitedAtHalf, SitedAt0}
	default:
		return YCbCr{}, fmt.Errorf("chroma sited at type %d, below its luma, which the renderer does not sample", s.siting)
	}
	return c, nil
}

// flagBit is a bit of a StdVideo flags bitfield and whether it is set.
type flagBit struct {
	set bool
	bit uint32
}

// bitfield is the bits of flags that are set.
func bitfield(flags ...flagBit) uint32 {
	var out uint32
	for _, f := range flags {
		if f.set {
			out |= f.bit
		}
	}
	return out
}

// errSkipPicture is a picture the decoder drops: one before the first
// picture decoding can start at, or one leading that start, which may
// predict from pictures before it.
var errSkipPicture = errors.New("gpu: the picture predicts from pictures not decoded")

// startCode is what each slice is behind in the stream buffer.
var startCode = []byte{0, 0, 1}

// sliceHeaderBytes is how much of a slice is unescaped to read its
// header: all of it when the header runs past.
const sliceHeaderBytes = 256

// maxDPBSlots are the DPB slots a stream of any codec can need.
const maxDPBSlots = max(h264MaxSlots, h265MaxSlots, refSlotPictures)
