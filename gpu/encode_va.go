package gpu

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"unsafe"

	"github.com/stubbedev/lys-lib/internal/nal"
	"github.com/stubbedev/lys-lib/internal/va"
	"github.com/stubbedev/lys-lib/third_party/vk"
)

// vaEncoders are the codecs the device's VA-API driver encodes, probed
// on first use: the profile each is coded in, and for each it does not
// encode, why not.
type vaEncoders struct {
	profiles map[Codec]vaProfile
	why      map[Codec]error
}

// vaProfile is how the driver encodes a codec: the VA profile and
// entrypoint, the rate control mode, the largest picture (0 where the
// driver does not say), whether P frames go as B slices predicting
// from the same picture in both lists (a driver that codes no P
// slices), and the H.265 features and block sizes it reports
// (AttribNotSupported where it does not).
type vaProfile struct {
	profile, entrypoint int32
	stdProfile          vk.StdVideoH264ProfileIdc
	rc                  uint32
	maxW, maxH          int
	gpb                 bool
	hevcFeatures        uint32
	hevcBlocks          uint32
}

// vaCodecProfiles are the VA profiles each codec tries, best first,
// with the H.264 profile_idc each is.
var vaCodecProfiles = map[Codec][]struct {
	profile int32
	std     vk.StdVideoH264ProfileIdc
}{
	H264: {{va.ProfileH264High, vk.StdVideoH264ProfileIdcHigh}, {va.ProfileH264Main, vk.StdVideoH264ProfileIdcMain}},
	H265: {{va.ProfileHEVCMain, 0}},
}

// vaRateControls are the rate control modes the encoder codes at, best
// first: constant QP, else variable bitrate, else constant bitrate.
var vaRateControls = []uint32{va.RCCQP, va.RCVBR, va.RCCBR}

// vaEncodersOf probes the device's VA-API driver once.
func (d *Device) vaEncodersOf() *vaEncoders {
	if d.va != nil {
		return d.va
	}
	v := &vaEncoders{profiles: map[Codec]vaProfile{}, why: map[Codec]error{}}
	d.va = v
	none := func(err error) *vaEncoders {
		for _, c := range codecs {
			v.why[c.codec] = err
		}
		return v
	}
	switch {
	case !d.sync2:
		return none(errors.New("no Vulkan 1.3 synchronization2, which the conversion records with"))
	case !d.exports:
		return none(errors.New("the device exports no dmabufs"))
	case d.renderNode == "":
		return none(errors.New("the driver names no DRM render node (VK_EXT_physical_device_drm)"))
	}
	dpy, err := va.Open(d.renderNode)
	if err != nil {
		return none(err)
	}
	defer dpy.Close()
	for _, c := range codecs {
		p, err := probeVAProfile(dpy, c.codec)
		if err != nil {
			v.why[c.codec] = fmt.Errorf("%s on %s: %w", dpy.Vendor(), d.renderNode, err)
			continue
		}
		v.profiles[c.codec] = p
	}
	return v
}

// vaCodecs are the codecs the VA-API driver encodes.
func (d *Device) vaCodecs() []Codec {
	var out []Codec
	for _, c := range codecs {
		if _, ok := d.vaEncodersOf().profiles[c.codec]; ok {
			out = append(out, c.codec)
		}
	}
	return out
}

// vaWhy is why the VA-API driver does not encode c.
func (d *Device) vaWhy(c Codec) error { return d.vaEncodersOf().why[c] }

// probeVAProfile finds the driver's best profile and entrypoint for c
// that takes 4:2:0 pictures, our packed parameter sets and one of our
// rate control modes.
func probeVAProfile(dpy *va.Display, c Codec) (vaProfile, error) {
	var why []string
	for _, prof := range vaCodecProfiles[c] {
		eps := dpy.Entrypoints(prof.profile)
		for _, ep := range []int32{va.EntrypointEncSliceLP, va.EntrypointEncSlice} {
			if !slices.Contains(eps, ep) {
				continue
			}
			p, err := vaConfigOf(dpy, prof.profile, ep)
			if err != nil {
				why = append(why, fmt.Sprintf("profile %d entrypoint %d: %v", prof.profile, ep, err))
				continue
			}
			p.stdProfile = prof.std
			return p, nil
		}
	}
	if len(why) == 0 {
		return vaProfile{}, fmt.Errorf("no %s encode entrypoint", c)
	}
	return vaProfile{}, fmt.Errorf("no usable %s encode configuration: %s", c, strings.Join(why, "; "))
}

// vaConfigOf reads what profile at entrypoint offers.
func vaConfigOf(dpy *va.Display, profile, entrypoint int32) (vaProfile, error) {
	attrs := []va.ConfigAttrib{
		{Type: va.ConfigAttribRTFormat},
		{Type: va.ConfigAttribRateControl},
		{Type: va.ConfigAttribEncPackedHeaders},
		{Type: va.ConfigAttribEncMaxRefFrames},
		{Type: va.ConfigAttribMaxPictureWidth},
		{Type: va.ConfigAttribMaxPictureHeight},
		{Type: va.ConfigAttribPredictionDirection},
		{Type: va.ConfigAttribEncHEVCFeatures},
		{Type: va.ConfigAttribEncHEVCBlockSizes},
	}
	if err := dpy.ConfigAttributes(profile, entrypoint, attrs); err != nil {
		return vaProfile{}, err
	}
	rt, rc, packed, refs, maxW, maxH, dir := attrs[0].Value, attrs[1].Value, attrs[2].Value, attrs[3].Value, attrs[4].Value, attrs[5].Value, attrs[6].Value
	p := vaProfile{profile: profile, entrypoint: entrypoint, hevcFeatures: attrs[7].Value, hevcBlocks: attrs[8].Value}
	switch {
	case rt == va.AttribNotSupported || rt&va.RTFormatYUV420 == 0:
		return vaProfile{}, fmt.Errorf("no 4:2:0 pictures (render target formats %#x)", rt)
	case packed == va.AttribNotSupported || packed&va.EncPackedHeaderSequence == 0:
		return vaProfile{}, fmt.Errorf("no packed sequence headers (%#x)", packed)
	case refs != va.AttribNotSupported && refs&0xffff == 0:
		return vaProfile{}, errors.New("no P frame references")
	}
	if rc != va.AttribNotSupported {
		for _, m := range vaRateControls {
			if rc&m != 0 {
				p.rc = m
				break
			}
		}
	}
	if p.rc == 0 {
		return vaProfile{}, fmt.Errorf("none of our rate control modes (%#x)", rc)
	}
	if maxW != va.AttribNotSupported && maxH != va.AttribNotSupported {
		p.maxW, p.maxH = int(maxW), int(maxH)
	}
	p.gpb = dir != va.AttribNotSupported && dir&va.PredictionDirectionBiNotEmpty != 0
	return p, nil
}

// vaParam is one parameter buffer a picture is coded with.
type vaParam struct {
	typ  int32
	data unsafe.Pointer
	size int
}

// vaCodec is what a VA encoder does differently per codec: its
// parameter sets and the sequence, picture and slice parameters each
// frame codes with. The structures live in it, for the pointers into
// them.
type vaCodec interface {
	// parameterSets are the stream's parameter set NAL units.
	parameterSets() [][]byte
	// isParameterSet reports whether a NAL unit is one.
	isParameterSet(unit []byte) bool
	// params are frame sinceIDR's of its key interval, the idrs-th
	// IDR's, reconstructed into surface cur from prev (the frame
	// before) and coded into the coded buffer: the sequence's too on
	// an IDR.
	params(sinceIDR, idrs int, cur, prev, coded uint32) []vaParam
}

// vaBackend encodes on the device's VA-API driver: each slot's
// conversion writes an exported linear NV12 buffer the driver imports
// as a surface, and the driver encodes that into the slot's coded
// buffer.
type vaBackend struct {
	d       *Device
	cfg     EncoderConfig
	prof    vaProfile
	coded   [2]int
	dpy     *va.Display
	codec   vaCodec
	config  uint32
	context uint32
	recon   []uint32
	// pitch and uvOffset lay the planes out in each slot's buffer.
	pitch, uvOffset int
	planeSize       int
	bitrate         uint32
	slots           [encodeSlots]vaSlot
}

// vaSlot is a frame in flight on the driver: the buffer its
// conversion writes, the surface the driver reads it as, and the coded
// buffer it encodes into.
type vaSlot struct {
	planes  *exportedBuffer
	surface uint32
	coded   uint32
}

// vaPitch and vaRows align the planes in a slot's buffer: the rows to
// 128 bytes and the chroma plane to 32 rows, what drivers import
// linear NV12 at.
const (
	vaPitch = 128
	vaRows  = 32
)

// newVABackend makes the driver's encode configuration, context and
// surfaces for cfg, and each slot's exported buffer.
func newVABackend(d *Device, cfg EncoderConfig) (*vaBackend, error) {
	vb := &vaBackend{d: d, cfg: cfg, prof: d.vaEncodersOf().profiles[cfg.Codec], config: va.InvalidID, context: va.InvalidID}
	for i := range vb.slots {
		vb.slots[i] = vaSlot{surface: va.InvalidID, coded: va.InvalidID}
	}
	for _, step := range []func() error{vb.open, vb.createConfig, vb.createSlots} {
		if err := step(); err != nil {
			vb.close()
			return nil, err
		}
	}
	return vb, nil
}

// open opens the display and lays out the stream: the coded size, the
// rate and the codec's parameters.
func (vb *vaBackend) open() error {
	dpy, err := va.Open(vb.d.renderNode)
	if err != nil {
		return err
	}
	vb.dpy = dpy
	cqp := vb.prof.rc == va.RCCQP
	vb.bitrate = uint32(min(desktopBitrate(vb.cfg), 1<<32-1))
	initQP := 26
	if cqp {
		initQP = vb.cfg.QP
	}
	switch vb.cfg.Codec {
	case H264:
		vb.coded = [2]int{alignUp(evenUp(vb.cfg.Width), 16), alignUp(evenUp(vb.cfg.Height), 16)}
		vb.codec, err = newH264VA(vb.cfg, vb.coded, vb.prof, initQP, vb.bitrate)
	case H265:
		vb.coded, vb.codec, err = newHEVCVA(vb.cfg, vb.prof, cqp, initQP, vb.bitrate)
	}
	if err != nil {
		return err
	}
	if p := vb.prof; p.maxW > 0 && (vb.coded[0] > p.maxW || vb.coded[1] > p.maxH) {
		return fmt.Errorf("gpu: %s encodes up to %dx%d, not %dx%d", dpy.Vendor(), p.maxW, p.maxH, vb.coded[0], vb.coded[1])
	}
	vb.pitch = alignUp(vb.coded[0], vaPitch)
	vb.uvOffset = vb.pitch * alignUp(vb.coded[1], vaRows)
	vb.planeSize = vb.uvOffset + vb.pitch*vb.coded[1]/2
	return nil
}

// createConfig makes the encode configuration, the reconstructed
// pictures and the context.
func (vb *vaBackend) createConfig() error {
	attrs := []va.ConfigAttrib{
		{Type: va.ConfigAttribRTFormat, Value: va.RTFormatYUV420},
		{Type: va.ConfigAttribRateControl, Value: vb.prof.rc},
		{Type: va.ConfigAttribEncPackedHeaders, Value: va.EncPackedHeaderSequence},
	}
	var err error
	if vb.config, err = vb.dpy.CreateConfig(vb.prof.profile, vb.prof.entrypoint, attrs); err != nil {
		return err
	}
	if vb.recon, err = vb.dpy.CreateSurfaces(va.RTFormatYUV420, vb.coded[0], vb.coded[1], dpbSlots); err != nil {
		vb.recon = nil
		return err
	}
	vb.context, err = vb.dpy.CreateContext(vb.config, vb.coded[0], vb.coded[1], vb.recon)
	return err
}

// createSlots makes each slot's buffer, imports it as a surface and
// makes its coded buffer.
func (vb *vaBackend) createSlots() error {
	for i := range vb.slots {
		s := &vb.slots[i]
		b, err := vb.d.newExportedBuffer(vb.planeSize, vk.BufferUsageStorageBufferBit)
		if err != nil {
			return fmt.Errorf("gpu: encoder planes: %w", err)
		}
		s.planes = b
		desc := va.DRMPRIMESurfaceDescriptor{
			Fourcc: va.FourccNV12, Width: uint32(vb.coded[0]), Height: uint32(vb.coded[1]),
			NumObjects: 1, Objects: [4]va.DRMPRIMESurfaceDescriptorObjects{{Fd: int32(b.fd), Size: uint32(b.size)}},
			NumLayers: 1, Layers: [4]va.DRMPRIMESurfaceDescriptorLayers{{
				DrmFormat: va.FourccNV12, NumPlanes: 2,
				Offset: [4]uint32{0, uint32(vb.uvOffset)}, Pitch: [4]uint32{uint32(vb.pitch), uint32(vb.pitch)},
			}},
		}
		if s.surface, err = vb.dpy.ImportSurface(&desc); err != nil {
			s.surface = va.InvalidID
			return fmt.Errorf("gpu: import the encoder planes (%dx%d, pitch %d): %w", vb.coded[0], vb.coded[1], vb.pitch, err)
		}
		if s.coded, err = vb.dpy.CreateBuffer(vb.context, va.EncCodedBufferType, bitstreamBytes(vb.coded), nil); err != nil {
			s.coded = va.InvalidID
			return err
		}
	}
	return nil
}

func (vb *vaBackend) codedSize() [2]int { return vb.coded }

func (vb *vaBackend) parameterSets() [][]byte { return vb.codec.parameterSets() }

func (vb *vaBackend) planes(slot int) (vk.Buffer, int, int, int) {
	return vb.slots[slot].planes.buf, vb.planeSize, vb.pitch, vb.uvOffset
}

// beforeConvert takes slot's buffer back from the driver.
func (vb *vaBackend) beforeConvert(cb vk.CommandBuffer, slot int) {
	b := vb.ownership(slot)
	b.SrcQueueFamilyIndex, b.DstQueueFamilyIndex = vk.QueueFamilyForeignExt, vb.d.family
	b.DstStageMask, b.DstAccessMask = vk.PipelineStageFlags2(vk.PipelineStage2ComputeShaderBit), vk.AccessFlags2(vk.Access2ShaderStorageWriteBit)
	bufferBarrier2(vb.d, cb, b)
}

// afterConvert hands slot's buffer to the driver, which reads it once
// the host has waited for the conversion.
//
//nolint:unparam // the interface's semaphore is the Vulkan backend's; the host waits for VA
func (vb *vaBackend) afterConvert(cb vk.CommandBuffer, slot int) *vk.SemaphoreSubmitInfo {
	b := vb.ownership(slot)
	b.SrcQueueFamilyIndex, b.DstQueueFamilyIndex = vb.d.family, vk.QueueFamilyForeignExt
	b.SrcStageMask, b.SrcAccessMask = vk.PipelineStageFlags2(vk.PipelineStage2ComputeShaderBit), vk.AccessFlags2(vk.Access2ShaderStorageWriteBit)
	bufferBarrier2(vb.d, cb, b)
	return nil
}

// ownership is a barrier over slot's whole buffer.
func (vb *vaBackend) ownership(slot int) vk.BufferMemoryBarrier2 {
	return vk.BufferMemoryBarrier2{SType: vk.StructureTypeBufferMemoryBarrier2, Buffer: vb.slots[slot].planes.buf, Size: vk.DeviceSize(vk.WholeSize)}
}

// encode hands slot's surface to the driver with the frame's
// parameters, and on an IDR the sequence's, its rate control and our
// parameter sets.
func (vb *vaBackend) encode(slot, frame, sinceIDR, idrs int) error {
	s := &vb.slots[slot]
	cur, prev := vb.recon[frame%dpbSlots], vb.recon[(frame+dpbSlots-1)%dpbSlots]
	params := vb.codec.params(sinceIDR, idrs, cur, prev, s.coded)
	var bufs []uint32
	defer func() {
		for _, b := range bufs {
			vb.dpy.DestroyBuffer(b)
		}
	}()
	add := func(p vaParam) error {
		id, err := vb.dpy.CreateBuffer(vb.context, p.typ, p.size, p.data)
		if err != nil {
			return err
		}
		bufs = append(bufs, id)
		return nil
	}
	misc := func(typ int32, data unsafe.Pointer, size int) error {
		id, err := vb.dpy.CreateMiscBuffer(vb.context, typ, data, size)
		if err != nil {
			return err
		}
		bufs = append(bufs, id)
		return nil
	}
	for _, p := range params {
		if err := add(p); err != nil {
			return err
		}
	}
	if sinceIDR == 0 {
		if vb.prof.rc != va.RCCQP {
			if err := vb.rateControl(misc); err != nil {
				return err
			}
		}
		headers := nal.AnnexB(vb.codec.parameterSets()...)
		packed := va.EncPackedHeaderParameterBuffer{Type: va.EncPackedHeaderSequence, BitLength: uint32(len(headers) * 8), HasEmulationBytes: 1}
		if err := add(vaParam{va.EncPackedHeaderParameterBufferType, unsafe.Pointer(&packed), int(unsafe.Sizeof(packed))}); err != nil {
			return err
		}
		if err := add(vaParam{va.EncPackedHeaderDataBufferType, unsafe.Pointer(&headers[0]), len(headers)}); err != nil {
			return err
		}
	}
	if err := vb.dpy.Encode(vb.context, s.surface, bufs); err != nil {
		return fmt.Errorf("gpu: encode frame %d: %w", frame, err)
	}
	return nil
}

// rateControl adds the variable or constant bitrate state: the target
// rate (half the peak for VBR, which peaks at twice it), the frame
// rate and a one-second buffer starting half full, as the Vulkan
// backend codes at.
func (vb *vaBackend) rateControl(misc func(typ int32, data unsafe.Pointer, size int) error) error {
	peak, percent := vb.bitrate, uint32(100)
	if vb.prof.rc == va.RCVBR {
		peak, percent = uint32(min(2*uint64(vb.bitrate), 1<<32-1)), 50
	}
	rc := va.EncMiscParameterRateControl{BitsPerSecond: peak, TargetPercentage: percent, WindowSize: 1000}
	fr := va.EncMiscParameterFrameRate{Framerate: uint32(vb.cfg.FrameRate)}
	hrd := va.EncMiscParameterHRD{BufferSize: peak, InitialBufferFullness: peak / 2}
	if err := misc(va.EncMiscParameterTypeRateControl, unsafe.Pointer(&rc), int(unsafe.Sizeof(rc))); err != nil {
		return err
	}
	if err := misc(va.EncMiscParameterTypeFrameRate, unsafe.Pointer(&fr), int(unsafe.Sizeof(fr))); err != nil {
		return err
	}
	return misc(va.EncMiscParameterTypeHRD, unsafe.Pointer(&hrd), int(unsafe.Sizeof(hrd)))
}

// collect waits for slot's picture and reads its access unit, without
// the packed parameter sets the driver repeats at an IDR, which
// ParameterSets gives.
func (vb *vaBackend) collect(slot int) ([]byte, error) {
	s := &vb.slots[slot]
	if err := vb.dpy.SyncSurface(s.surface); err != nil {
		return nil, err
	}
	data, err := vb.dpy.ReadCoded(s.coded)
	if err != nil {
		return nil, err
	}
	var units [][]byte
	for _, u := range nalUnits(data) {
		if !vb.codec.isParameterSet(u) {
			units = append(units, u)
		}
	}
	return nal.AnnexB(units...), nil
}

func (vb *vaBackend) close() {
	for i := range vb.slots {
		s := &vb.slots[i]
		if s.coded != va.InvalidID {
			vb.dpy.DestroyBuffer(s.coded)
		}
		if s.surface != va.InvalidID {
			vb.dpy.DestroySurfaces([]uint32{s.surface})
		}
		if s.planes != nil {
			s.planes.close()
		}
	}
	if vb.dpy == nil {
		return
	}
	if vb.context != va.InvalidID {
		vb.dpy.DestroyContext(vb.context)
	}
	vb.dpy.DestroySurfaces(vb.recon)
	if vb.config != va.InvalidID {
		vb.dpy.DestroyConfig(vb.config)
	}
	vb.dpy.Close()
}

// h264VA is H.264's part of a VA encoder.
type h264VA struct {
	header nal.H264
	qp     int
	seq    va.EncSequenceParameterBufferH264
	pic    va.EncPictureParameterBufferH264
	slice  va.EncSliceParameterBufferH264
}

// newH264VA lays out cfg's sequence coded at coded in prof's profile,
// at the lowest level that carries it.
func newH264VA(cfg EncoderConfig, coded [2]int, prof vaProfile, qp int, bitrate uint32) (*h264VA, error) {
	level, err := h264Level(coded[0]/16*(coded[1]/16), cfg.FrameRate, vk.StdVideoH264LevelIdc(len(h264Levels)-1))
	if err != nil {
		return nil, err
	}
	c := &h264VA{header: h264Header(cfg, coded, prof.stdProfile, level, true, true, qp), qp: qp}
	h := &c.header
	c.seq = va.EncSequenceParameterBufferH264{
		LevelIdc: h.LevelIdc, IntraPeriod: uint32(cfg.KeyInterval), IntraIdrPeriod: uint32(cfg.KeyInterval), IpPeriod: 1,
		BitsPerSecond: bitrate, MaxNumRefFrames: uint32(h.MaxNumRefFrames),
		PictureWidthInMbs: uint16(h.WidthMbs), PictureHeightInMbs: uint16(h.HeightMbs),
		SeqFields: va.H264SeqChromaFormatIdc.Put(1) | va.H264SeqFrameMbsOnlyFlag.Put(1) | va.H264SeqDirect8x8InferenceFlag.Put(1) |
			va.H264SeqLog2MaxFrameNumMinus4.Put(uint32(h.Log2MaxFrameNum-4)) | va.H264SeqPicOrderCntType.Put(2),
		FrameCropRightOffset: uint32(h.CropRight), FrameCropBottomOffset: uint32(h.CropBottom),
		VuiParametersPresentFlag: 1,
		VuiFields: va.H264VuiBitstreamRestrictionFlag.Put(1) | va.H264VuiMotionVectorsOverPicBoundariesFlag.Put(1) |
			va.H264VuiLog2MaxMvLengthHorizontal.Put(15) | va.H264VuiLog2MaxMvLengthVertical.Put(15),
		NumUnitsInTick: 1, TimeScale: uint32(2 * cfg.FrameRate),
	}
	if h.CropRight != 0 || h.CropBottom != 0 {
		c.seq.FrameCroppingFlag = 1
	}
	return c, nil
}

func (c *h264VA) parameterSets() [][]byte { return [][]byte{c.header.SPS(), c.header.PPS()} }

func (c *h264VA) isParameterSet(u []byte) bool { t := u[0] & 0x1f; return t == 7 || t == 8 }

func (c *h264VA) params(sinceIDR, idrs int, cur, prev, coded uint32) []vaParam {
	h := &c.header
	idr := sinceIDR == 0
	none := va.PictureH264{PictureID: va.InvalidID, Flags: va.PictureH264Invalid}
	ref := va.PictureH264{
		PictureID: prev, FrameIdx: uint32(sinceIDR - 1), Flags: va.PictureH264ShortTermReference,
		TopFieldOrderCnt: int32(2 * (sinceIDR - 1)), BottomFieldOrderCnt: int32(2 * (sinceIDR - 1)),
	}
	var picFields uint32
	if idr {
		picFields |= va.H264PicIdrPicFlag.Put(1)
	}
	picFields |= va.H264PicReferencePicFlag.Put(1)
	if h.CABAC {
		picFields |= va.H264PicEntropyCodingModeFlag.Put(1)
	}
	if h.Transform8x8 {
		picFields |= va.H264PicTransform8x8ModeFlag.Put(1)
	}
	c.pic = va.EncPictureParameterBufferH264{
		CurrPic:  va.PictureH264{PictureID: cur, FrameIdx: uint32(sinceIDR), TopFieldOrderCnt: int32(2 * sinceIDR), BottomFieldOrderCnt: int32(2 * sinceIDR)},
		CodedBuf: coded, FrameNum: uint16(sinceIDR), PicInitQp: uint8(h.InitQP), PicFields: picFields,
	}
	c.slice = va.EncSliceParameterBufferH264{
		NumMacroblocks: uint32(h.WidthMbs * h.HeightMbs), SliceType: 0, IdrPicID: uint16(idrs), SliceQpDelta: int8(c.qp - h.InitQP),
	}
	for i := range c.pic.ReferenceFrames {
		c.pic.ReferenceFrames[i] = none
	}
	for i := range c.slice.RefPicList0 {
		c.slice.RefPicList0[i], c.slice.RefPicList1[i] = none, none
	}
	if idr {
		c.slice.SliceType = 2
	} else {
		c.pic.ReferenceFrames[0], c.slice.RefPicList0[0] = ref, ref
	}
	out := []vaParam{
		{va.EncPictureParameterBufferType, unsafe.Pointer(&c.pic), int(unsafe.Sizeof(c.pic))},
		{va.EncSliceParameterBufferType, unsafe.Pointer(&c.slice), int(unsafe.Sizeof(c.slice))},
	}
	if idr {
		out = append([]vaParam{{va.EncSequenceParameterBufferType, unsafe.Pointer(&c.seq), int(unsafe.Sizeof(c.seq))}}, out...)
	}
	return out
}

// hevcVA is H.265's part of a VA encoder.
type hevcVA struct {
	header nal.HEVC
	qp     int
	gpb    bool
	ctus   int
	seq    va.EncSequenceParameterBufferHEVC
	pic    va.EncPictureParameterBufferHEVC
	slice  va.EncSliceParameterBufferHEVC
}

// vaHEVCBlocks are the block sizes the driver reports, else 32x32
// coding trees over 8x8 coding blocks and 4x4 to 32x32 transforms.
func vaHEVCBlocks(attr uint32) h265Blocks {
	if attr == va.AttribNotSupported {
		return h265Blocks{minCB: 3, ctb: 5, minTB: 2, maxTB: 5, depth: 3}
	}
	b := h265Blocks{
		minCB: 3 + int(va.HEVCBlockLog2MinLumaCodingBlockSizeMinus3.Get(attr)),
		ctb:   3 + int(va.HEVCBlockLog2MaxCodingTreeBlockSizeMinus3.Get(attr)),
		minTB: 2 + int(va.HEVCBlockLog2MinLumaTransformBlockSizeMinus2.Get(attr)),
		maxTB: 2 + int(va.HEVCBlockLog2MaxLumaTransformBlockSizeMinus2.Get(attr)),
	}
	b.depth = min(int(va.HEVCBlockMaxMaxTransformHierarchyDepthInter.Get(attr)), int(va.HEVCBlockMaxMaxTransformHierarchyDepthIntra.Get(attr)), b.ctb-b.minTB)
	return b
}

// vaHEVCFeature is whether to code with a tool the driver reports as
// feature: where it says nothing, as want says.
func vaHEVCFeature(features uint32, feature va.Bits, want bool) bool {
	if features == va.AttribNotSupported {
		return want
	}
	switch feature.Get(features) {
	case va.FeatureNotSupported:
		return false
	case va.FeatureRequired:
		return true
	}
	return want
}

// newHEVCVA lays out cfg's sequence in the driver's block sizes and
// features, at the lowest level that carries it, and the coded size
// (whole smallest coding blocks) that comes to.
func newHEVCVA(cfg EncoderConfig, prof vaProfile, cqp bool, qp int, bitrate uint32) ([2]int, *hevcVA, error) {
	blocks := vaHEVCBlocks(prof.hevcBlocks)
	unit := 1 << blocks.minCB
	coded := [2]int{alignUp(evenUp(cfg.Width), unit), alignUp(evenUp(cfg.Height), unit)}
	level, err := h265Level(coded[0], coded[1], cfg.FrameRate, vk.StdVideoH265LevelIdc(len(h265Levels)-1))
	if err != nil {
		return coded, nil, err
	}
	f := prof.hevcFeatures
	amp := vaHEVCFeature(f, va.HEVCFeatureAmp, true)
	sao := vaHEVCFeature(f, va.HEVCFeatureSao, true)
	tmvp := vaHEVCFeature(f, va.HEVCFeatureTemporalMvp, false)
	cuQPDelta := vaHEVCFeature(f, va.HEVCFeatureCuQpDelta, !cqp)
	ctb := 1 << blocks.ctb
	c := &hevcVA{
		header: h265Header(cfg, coded, level, blocks, amp, sao, tmvp, cuQPDelta, qp), qp: qp, gpb: prof.gpb,
		ctus: (coded[0] + ctb - 1) / ctb * ((coded[1] + ctb - 1) / ctb),
	}
	h := &c.header
	seqFields := va.HEVCSeqChromaFormatIdc.Put(1) | va.HEVCSeqLowDelaySeq.Put(1)
	for _, t := range []struct {
		on   bool
		bits va.Bits
	}{{h.AMP, va.HEVCSeqAmpEnabledFlag}, {h.SAO, va.HEVCSeqSampleAdaptiveOffsetEnabledFlag}, {h.TemporalMVP, va.HEVCSeqSpsTemporalMvpEnabledFlag}} {
		if t.on {
			seqFields |= t.bits.Put(1)
		}
	}
	c.seq = va.EncSequenceParameterBufferHEVC{
		GeneralProfileIdc: 1, GeneralLevelIdc: h.LevelIdc,
		IntraPeriod: uint32(cfg.KeyInterval), IntraIdrPeriod: uint32(cfg.KeyInterval), IpPeriod: 1, BitsPerSecond: bitrate,
		PicWidthInLumaSamples: uint16(h.Width), PicHeightInLumaSamples: uint16(h.Height), SeqFields: seqFields,
		Log2MinLumaCodingBlockSizeMinus3:  uint8(h.Log2MinCb - 3),
		Log2DiffMaxMinLumaCodingBlockSize: uint8(h.Log2Ctb - h.Log2MinCb),
		Log2MinTransformBlockSizeMinus2:   uint8(h.Log2MinTb - 2),
		Log2DiffMaxMinTransformBlockSize:  uint8(h.Log2MaxTb - h.Log2MinTb),
		MaxTransformHierarchyDepthInter:   uint8(h.TransformDepth),
		MaxTransformHierarchyDepthIntra:   uint8(h.TransformDepth),
		VuiParametersPresentFlag:          1,
		VuiFields: va.HEVCVuiBitstreamRestrictionFlag.Put(1) | va.HEVCVuiMotionVectorsOverPicBoundariesFlag.Put(1) |
			va.HEVCVuiRestrictedRefPicListsFlag.Put(1) | va.HEVCVuiLog2MaxMvLengthHorizontal.Put(15) | va.HEVCVuiLog2MaxMvLengthVertical.Put(15),
		VuiNumUnitsInTick: 1, VuiTimeScale: uint32(cfg.FrameRate),
	}
	return coded, c, nil
}

func (c *hevcVA) parameterSets() [][]byte {
	return [][]byte{c.header.VPS(), c.header.SPS(), c.header.PPS()}
}

func (c *hevcVA) isParameterSet(u []byte) bool { t := u[0] >> 1 & 0x3f; return t >= 32 && t <= 34 }

// The H.265 NAL unit types (Table 7-1) and slice types (Table 7-7) the
// encoder codes.
const (
	hevcTrailR   = 1
	hevcIDRWRADL = 19
	hevcSliceB   = 0
	hevcSliceP   = 1
	hevcSliceI   = 2
)

func (c *hevcVA) params(sinceIDR, _ int, cur, prev, coded uint32) []vaParam {
	h := &c.header
	idr := sinceIDR == 0
	none := va.PictureHEVC{PictureID: va.InvalidID, Flags: va.PictureHEVCInvalid}
	ref := va.PictureHEVC{PictureID: prev, PicOrderCnt: int32(sinceIDR - 1), Flags: va.PictureHEVCRPSStCurrBefore}
	picFields := va.HEVCPicReferencePicFlag.Put(1) | va.HEVCPicPpsLoopFilterAcrossSlicesEnabledFlag.Put(1)
	nalType, codingType, sliceType := uint8(hevcTrailR), uint32(2), uint8(hevcSliceP)
	if idr {
		picFields |= va.HEVCPicIdrPicFlag.Put(1)
		nalType, codingType, sliceType = hevcIDRWRADL, 1, hevcSliceI
	} else if c.gpb {
		sliceType = hevcSliceB
	}
	picFields |= va.HEVCPicCodingType.Put(codingType)
	if h.CuQPDelta {
		picFields |= va.HEVCPicCuQpDeltaEnabledFlag.Put(1)
	}
	collocated := uint8(0xff)
	sliceFields := va.HEVCSliceLastSliceOfPicFlag.Put(1) | va.HEVCSliceSliceLoopFilterAcrossSlicesEnabledFlag.Put(1)
	if h.SAO {
		sliceFields |= va.HEVCSliceSliceSaoLumaFlag.Put(1) | va.HEVCSliceSliceSaoChromaFlag.Put(1)
	}
	if h.TemporalMVP && !idr {
		collocated = 0
		sliceFields |= va.HEVCSliceSliceTemporalMvpEnabledFlag.Put(1) | va.HEVCSliceCollocatedFromL0Flag.Put(1)
	}
	c.pic = va.EncPictureParameterBufferHEVC{
		DecodedCurrPic: va.PictureHEVC{PictureID: cur, PicOrderCnt: int32(sinceIDR)},
		CodedBuf:       coded, CollocatedRefPicIndex: collocated, PicInitQp: uint8(h.InitQP), NalUnitType: nalType, PicFields: picFields,
	}
	c.slice = va.EncSliceParameterBufferHEVC{
		NumCtuInSlice: uint32(c.ctus), SliceType: sliceType, MaxNumMergeCand: 5, SliceQpDelta: int8(c.qp - h.InitQP), SliceFields: sliceFields,
	}
	for i := range c.pic.ReferenceFrames {
		c.pic.ReferenceFrames[i] = none
	}
	for i := range c.slice.RefPicList0 {
		c.slice.RefPicList0[i], c.slice.RefPicList1[i] = none, none
	}
	if !idr {
		c.pic.ReferenceFrames[0], c.slice.RefPicList0[0] = ref, ref
		if c.gpb {
			c.slice.RefPicList1[0] = ref
		}
	}
	out := []vaParam{
		{va.EncPictureParameterBufferType, unsafe.Pointer(&c.pic), int(unsafe.Sizeof(c.pic))},
		{va.EncSliceParameterBufferType, unsafe.Pointer(&c.slice), int(unsafe.Sizeof(c.slice))},
	}
	if idr {
		out = append([]vaParam{{va.EncSequenceParameterBufferType, unsafe.Pointer(&c.seq), int(unsafe.Sizeof(c.seq))}}, out...)
	}
	return out
}
