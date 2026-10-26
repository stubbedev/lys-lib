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

	"github.com/stubbedev/lys-lib/internal/intconv"
	"github.com/stubbedev/lys-lib/third_party/vk"
)

// ErrNoVideoEncoder reports a device that does not encode the codec
// asked for: neither on a Vulkan video encode queue (lavapipe, older
// radv, anv on Xe2) nor through a VA-API driver.
var ErrNoVideoEncoder = errors.New("gpu: the device has no video encoder")

// Codecs are the codecs the device encodes, on its Vulkan video queue
// or its VA-API driver.
func (d *Device) Codecs() []Codec {
	var out []Codec
	for _, c := range codecs {
		if (d.video != nil && slices.Contains(d.video.codecs, c.codec)) || slices.Contains(d.vaCodecs(), c.codec) {
			out = append(out, c.codec)
		}
	}
	return out
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
	// Source is the region of the buffer Encode reads, in the buffer's
	// own orientation; the zero rectangle reads all of it.
	Source image.Rectangle
	// Transform is the captured output's wl_output transform: the
	// buffer holds the output's pixels so turned, and the encoder turns
	// them back, so the frame shows what the output shows. Width and
	// Height are the frame's, turned: a 90 or 270 degree transform
	// swaps the source region's.
	Transform Transform
	// QP is the constant quantiser, 0 (best) to 51. A device without
	// constant-QP coding runs its own rate control at a bitrate
	// matching QP 20 on desktop content instead.
	QP int
}

// Transform is a wl_output transform: how an output's pixels are
// turned in its buffer.
type Transform int

// The transforms, valued as wl_output's.
const (
	TransformNormal Transform = iota
	Transform90
	Transform180
	Transform270
	TransformFlipped
	TransformFlipped90
	TransformFlipped180
	TransformFlipped270
)

// swaps reports whether t turns by a quarter, swapping width and
// height.
func (t Transform) swaps() bool { return t&1 != 0 }

// sourceSize is the source region's size for a w x h frame.
func (c EncoderConfig) sourceSize() image.Point {
	if c.Transform.swaps() {
		return image.Pt(c.Height, c.Width)
	}
	return image.Pt(c.Width, c.Height)
}

// source is the region of a w x h buffer Encode reads, or an error
// naming it and the buffer's size when the buffer does not hold it.
func (c EncoderConfig) source(w, h int) (image.Rectangle, error) {
	buf := image.Rect(0, 0, w, h)
	r := c.Source
	if r == (image.Rectangle{}) {
		r = buf
	}
	if r.Size() != c.sourceSize() || !r.In(buf) {
		return image.Rectangle{}, fmt.Errorf("gpu: source %v (want %v for a %dx%d frame under transform %d) is not in the %dx%d buffer", r, c.sourceSize(), c.Width, c.Height, c.Transform, w, h)
	}
	return r, nil
}

func (c EncoderConfig) validate() error {
	switch {
	case c.Transform < TransformNormal || c.Transform > TransformFlipped270:
		return fmt.Errorf("gpu: transform %d is no wl_output transform", c.Transform)
	case c.Source != (image.Rectangle{}) && (c.Source.Empty() || c.Source.Size() != c.sourceSize()):
		return fmt.Errorf("gpu: source %v is not the %v region a %dx%d frame under transform %d reads", c.Source, c.sourceSize(), c.Width, c.Height, c.Transform)
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

// evenUp rounds n up to even: 4:2:0 crops in pairs of pixels, so an odd
// size shows its last row or column twice.
func evenUp(n int) int { return n + n&1 }

// encodeSlot is one frame in flight: whether it holds one, and the
// time and keyframe flag its packet takes.
type encodeSlot struct {
	busy bool
	pts  time.Duration
	key  bool
}

// Encoder encodes frames into H.264 or H.265 on the device's hardware
// encoder: its Vulkan video queue, else its VA-API driver. A frame's
// pixels come from a Buffer a compositor copied a screen into: a
// compute pass crops, turns, overlays and converts them to NV12, and
// the backend encodes that while the next frame converts. Like the
// Device, it belongs to one goroutine.
type Encoder struct {
	d       *Device
	cfg     EncoderConfig
	coded   [2]int
	backend encodeBackend
	// planes is where the conversion writes the coded picture's NV12.
	planes *deviceBuffer

	setLayout vk.DescriptorSetLayout
	layout    vk.PipelineLayout
	pipeline  vk.Pipeline
	module    vk.ShaderModule
	descPool  vk.DescriptorPool
	set       vk.DescriptorSet
	gfxPool   vk.CommandPool
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
	idrs   uint16
	closed bool
}

// EncoderBackend is the path an Encoder encodes through.
type EncoderBackend int

// The encoder backends.
const (
	// VulkanVideo is the device's Vulkan video encode queue.
	VulkanVideo EncoderBackend = iota
	// VAAPI is the device's VA-API driver.
	VAAPI
)

func (b EncoderBackend) String() string {
	if b == VAAPI {
		return "VA-API"
	}
	return "Vulkan video"
}

// Backend is the path the encoder encodes through, the Vulkan video
// queue or the VA-API driver, as NewEncoder chose it.
func (e *Encoder) Backend() EncoderBackend { return e.backend.kind() }

// encodeBackend is what encodes the converted frames: the Vulkan video
// queue or a VA-API driver.
type encodeBackend interface {
	// kind is which one.
	kind() EncoderBackend
	// codedSize is the coded picture's size.
	codedSize() [2]int
	// afterConvert records what follows slot's conversion on the
	// graphics queue, which wrote the coded picture's planes tight into
	// planes: their copy into the backend's picture. It returns the
	// semaphore the conversion signals for the backend, or nil.
	afterConvert(cb vk.CommandBuffer, slot int, planes vk.Buffer) *vk.SemaphoreSubmitInfo
	// encode queues slot's converted frame, the encoder's frame-th,
	// sinceIDR frames after its IDR, the idrs-th IDR's.
	encode(slot, frame, sinceIDR int, idrs uint16) error
	// collect waits for slot's frame and returns its access unit.
	collect(slot int) ([]byte, error)
	// parameterSets are the stream's parameter set NAL units.
	parameterSets() [][]byte
	close()
}

// NewEncoder makes an encoder for cfg on the device's Vulkan video
// queue when it encodes cfg.Codec, else on its VA-API driver:
// ErrNoVideoEncoder, with both backends' reasons, when neither does,
// and an error naming the limit when the encoder cannot code cfg's
// size.
func (d *Device) NewEncoder(cfg EncoderConfig) (*Encoder, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	switch {
	case d.video != nil && slices.Contains(d.video.codecs, cfg.Codec):
		return newEncoderOn(d, cfg, newVulkanBackend)
	case slices.Contains(d.vaCodecs(), cfg.Codec):
		return newEncoderOn(d, cfg, newVABackend)
	}
	return nil, fmt.Errorf("%w: %s encodes no %s: Vulkan: %w; VA-API: %w", ErrNoVideoEncoder, d.name, cfg.Codec, d.videoWhy[cfg.Codec], d.vaWhy(cfg.Codec))
}

// newEncoderOn makes an encoder for cfg on the backend newBackend makes.
func newEncoderOn[B encodeBackend](d *Device, cfg EncoderConfig, newBackend func(*Device, EncoderConfig) (B, error)) (*Encoder, error) {
	backend, err := newBackend(d, cfg)
	if err != nil {
		return nil, err
	}
	e := &Encoder{d: d, cfg: cfg, backend: backend, coded: backend.codedSize()}
	for _, step := range []func() error{e.createPlanes, e.createPipeline, e.createCommands} {
		if err := step(); err != nil {
			e.Close()
			return nil, err
		}
	}
	return e, nil
}

func alignUp(n, to int) int { return (n + to - 1) / to * to }

// bitstreamBytes is the room a frame coded at coded gets: its raw
// pictures' size and 64 KiB for headers, which no frame passes.
func bitstreamBytes(coded [2]int) int { return coded[0]*coded[1]*3/2 + 1<<16 }

// desktopBitsPerPixel is the variable-bitrate target per pixel a frame:
// what QP 20 averages on screen content.
const desktopBitsPerPixel = 0.1

// desktopBitrate is the bitrate the backends target for cfg where they
// have no constant QP.
func desktopBitrate(cfg EncoderConfig) uint64 {
	return uint64(float32(cfg.Width*cfg.Height*cfg.FrameRate) * desktopBitsPerPixel)
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
	sets := e.backend.parameterSets()
	out := make([][]byte, len(sets))
	for i, p := range sets {
		out[i] = slices.Clone(p)
	}
	return out
}

// nv12Constants is nv12.comp's push constant block: the frame and
// coded sizes, the overlay's rectangle (empty for none), the source
// region's origin and the transform.
type nv12Constants struct {
	frameW, frameH, codedW, codedH int32
	overlay                        [4]int32
	originX, originY, transform    int32
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
	var pin runtime.Pinner
	defer pin.Unpin()
	cpi := vk.ComputePipelineCreateInfo{
		SType:  vk.StructureTypeComputePipelineCreateInfo,
		Stage:  vk.PipelineShaderStageCreateInfo{SType: vk.StructureTypePipelineShaderStageCreateInfo, Stage: vk.ShaderStageComputeBit, Module: m, PName: cstr(&pin, "main")},
		Layout: e.layout,
	}
	r := d.cmds.CreateComputePipelines(d.device, 0, 1, &cpi, nil, &e.pipeline)
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
	bi := vk.DescriptorBufferInfo{Buffer: e.planes.buf, Range: intconv.Must[vk.DeviceSize](nv12Bytes(e.coded))}
	w := vk.WriteDescriptorSet{SType: vk.StructureTypeWriteDescriptorSet, DstSet: e.set, DstBinding: 1, DescriptorCount: 1, DescriptorType: vk.DescriptorTypeStorageBuffer, PBufferInfo: &bi}
	d.cmds.UpdateDescriptorSets(d.device, 1, &w, 0, nil)
	return nil
}

// createPlanes makes the buffer the conversion writes the planes into.
func (e *Encoder) createPlanes() error {
	planes, err := e.d.newDeviceBuffer(nv12Bytes(e.coded), vk.BufferUsageStorageBufferBit|vk.BufferUsageTransferSrcBit)
	if err != nil {
		return fmt.Errorf("gpu: encoder planes: %w", err)
	}
	e.planes = planes
	return nil
}

// nv12Bytes is the size of a coded picture's two planes, tight.
func nv12Bytes(coded [2]int) int { return coded[0] * coded[1] * 3 / 2 }

// copyPlanes records the copy of the tight planes the conversion wrote
// into dst, an NV12 picture whose contents it discards, taking dst
// from queue family from first (QueueFamilyIgnored for none). It
// leaves dst in TransferDstOptimal.
func copyPlanes(d *Device, cb vk.CommandBuffer, planes vk.Buffer, dst vk.Image, coded [2]int, from uint32) {
	written := vk.MemoryBarrier2{
		SType:        vk.StructureTypeMemoryBarrier2,
		SrcStageMask: vk.PipelineStageFlags2(vk.PipelineStage2ComputeShaderBit), SrcAccessMask: vk.AccessFlags2(vk.Access2ShaderStorageWriteBit),
		DstStageMask: vk.PipelineStageFlags2(vk.PipelineStage2CopyBit), DstAccessMask: vk.AccessFlags2(vk.Access2TransferReadBit),
	}
	toDst := imageBarrier(dst, nv12Range, vk.ImageLayoutUndefined, vk.ImageLayoutTransferDstOptimal)
	toDst.DstStageMask, toDst.DstAccessMask = vk.PipelineStageFlags2(vk.PipelineStage2CopyBit), vk.AccessFlags2(vk.Access2TransferWriteBit)
	if from != vk.QueueFamilyIgnored {
		toDst.SrcQueueFamilyIndex, toDst.DstQueueFamilyIndex = from, d.family
	}
	barrier2(d, cb, &written, toDst)
	cw, ch := intconv.Must[uint32](coded[0]), intconv.Must[uint32](coded[1])
	regions := [2]vk.BufferImageCopy{
		{BufferRowLength: cw, BufferImageHeight: ch, ImageSubresource: vk.ImageSubresourceLayers{AspectMask: vk.ImageAspectFlags(vk.ImageAspectPlane0Bit), LayerCount: 1}, ImageExtent: vk.Extent3D{Width: cw, Height: ch, Depth: 1}},
		{BufferOffset: vk.DeviceSize(cw * ch), BufferRowLength: cw / 2, BufferImageHeight: ch / 2, ImageSubresource: vk.ImageSubresourceLayers{AspectMask: vk.ImageAspectFlags(vk.ImageAspectPlane1Bit), LayerCount: 1}, ImageExtent: vk.Extent3D{Width: cw / 2, Height: ch / 2, Depth: 1}},
	}
	d.cmds.CmdCopyBufferToImage(cb, planes, dst, vk.ImageLayoutTransferDstOptimal, 2, &regions[0])
}

// nv12Range is a whole NV12 picture of one layer.
var nv12Range = vk.ImageSubresourceRange{AspectMask: vk.ImageAspectFlags(vk.ImageAspectColorBit), LevelCount: 1, LayerCount: 1}

// createCommands makes the conversion's command buffer and the fence
// the host waits for it on.
func (e *Encoder) createCommands() error {
	d := e.d
	var err error
	if e.gfxPool, err = d.commandPool(d.family); err != nil {
		return err
	}
	if e.gfxCB, err = d.commandBuffer(e.gfxPool); err != nil {
		return err
	}
	e.gfxDone, err = d.fence()
	return err
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
	if src.closed {
		return nil, errors.New("gpu: encode a closed buffer")
	}
	region, err := e.cfg.source(src.e.buf.Width, src.e.buf.Height)
	if err != nil {
		return nil, err
	}
	i := e.frames % encodeSlots
	slot := &e.slots[i]
	var out []Packet
	if slot.busy {
		p, err := e.collect(i)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	if err := e.convert(src, region.Min, i); err != nil {
		return nil, err
	}
	if r := e.d.cmds.WaitForFences(e.d.device, 1, &e.gfxDone, vk.True, ^uint64(0)); r != vk.Success {
		return nil, fmt.Errorf("gpu: wait for the NV12 conversion: %d", r)
	}
	sinceIDR := e.frames % e.cfg.KeyInterval
	if err := e.backend.encode(i, e.frames, sinceIDR, e.idrs); err != nil {
		return nil, err
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
		i := (e.frames + i) % encodeSlots
		if !e.slots[i].busy {
			continue
		}
		p, err := e.collect(i)
		if err != nil {
			return out, err
		}
		out = append(out, p)
	}
	return out, nil
}

// collect waits for slot i's frame and takes its packet.
func (e *Encoder) collect(i int) (Packet, error) {
	slot := &e.slots[i]
	slot.busy = false
	data, err := e.backend.collect(i)
	if err != nil {
		return Packet{}, fmt.Errorf("gpu: the frame at %v: %w", slot.pts, err)
	}
	return Packet{Data: data, PTS: slot.pts, Key: slot.key}, nil
}

// convert records and submits the NV12 conversion of src into slot's
// planes, and what the backend does with them on the graphics queue.
func (e *Encoder) convert(src *Buffer, origin image.Point, slot int) error {
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
		over = [4]int32{intconv.Must[int32](r.Min.X), intconv.Must[int32](r.Min.Y), intconv.Must[int32](r.Max.X), intconv.Must[int32](r.Max.Y)}
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
	pc := nv12Constants{
		frameW: intconv.Must[int32](e.cfg.Width), frameH: intconv.Must[int32](e.cfg.Height), codedW: intconv.Must[int32](e.coded[0]), codedH: intconv.Must[int32](e.coded[1]), overlay: over,
		originX: intconv.Must[int32](origin.X), originY: intconv.Must[int32](origin.Y), transform: intconv.Must[int32](e.cfg.Transform),
	}
	d.cmds.CmdPushConstants(cb, e.layout, vk.ShaderStageFlags(vk.ShaderStageComputeBit), 0, uint32(unsafe.Sizeof(pc)), unsafe.Pointer(&pc))
	d.cmds.CmdDispatch(cb, intconv.Must[uint32]((e.coded[0]/4+7)/8), intconv.Must[uint32]((e.coded[1]/2+7)/8), 1)
	release := imageBarrier(src.e.image, colorRange, vk.ImageLayoutGeneral, vk.ImageLayoutGeneral)
	release.SrcQueueFamilyIndex, release.DstQueueFamilyIndex = d.family, vk.QueueFamilyForeignExt
	release.SrcStageMask, release.SrcAccessMask = vk.PipelineStageFlags2(vk.PipelineStage2ComputeShaderBit), vk.AccessFlags2(vk.Access2ShaderSampledReadBit)
	barrier2(d, cb, nil, release)
	signal := e.backend.afterConvert(cb, slot, e.planes.buf)
	if r := d.cmds.EndCommandBuffer(cb); r != vk.Success {
		return fmt.Errorf("gpu: vkEndCommandBuffer (NV12): %d", r)
	}
	if err := resetFence(d, e.gfxDone); err != nil {
		return err
	}
	return submit2(d, d.queue, cb, nil, signal, e.gfxDone)
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
	if e.backend != nil {
		e.backend.close()
	}
	if e.overlay != nil {
		e.overlay.close()
	}
	if e.overlayStaging != nil {
		e.overlayStaging.close()
	}
	if e.planes != nil {
		e.planes.close()
	}
	if e.linear != 0 {
		d.cmds.DestroySampler(d.device, e.linear, nil)
	}
	if e.gfxDone != 0 {
		d.cmds.DestroyFence(d.device, e.gfxDone, nil)
	}
	if e.gfxPool != 0 {
		d.cmds.DestroyCommandPool(d.device, e.gfxPool, nil)
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
}
