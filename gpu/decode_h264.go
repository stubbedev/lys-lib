package gpu

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"time"
	"unsafe"

	"github.com/stubbedev/lys-lib/intconv"
	"github.com/stubbedev/lys-lib/internal/nal"
	"github.com/stubbedev/lys-lib/media/demux"
	"github.com/stubbedev/lys-lib/third_party/vk"
)

// h264Decoder is H.264's part of a decoder: the parameter sets, the
// reference state, and the structures the session and each picture are
// coded with.
type h264Decoder struct {
	lengthSize int
	sps        [32]*nal.H264SPS
	pps        [256]*nal.H264PPS
	spsRaw     [32][]byte
	spsLevel   [32]vk.StdVideoH264LevelIdc
	ppsRaw     [256][]byte
	// paramsChanged marks parameter sets the session parameters lack.
	paramsChanged bool
	active        *nal.H264SPS
	state         h264State
	// before is the reference state before the first of the packet's
	// pictures took its place, once kept is set: what a packet that fails
	// returns to.
	before h264State
	kept   bool
	hdr    nal.H264SliceHeader
	other  nal.H264SliceHeader
	rbsp   []byte
	// slices are the slices read of the packet's part-th picture; whole
	// is whether the picture before it in the packet is a frame.
	slices []h264Slice
	part   int
	whole  bool
	// fieldPTS and fieldDuration are the packet's of a frame's first
	// field, for the frame its second field completes.
	fieldPTS, fieldDuration time.Duration
	offsets                 []uint32
	// gapPPS is the PPS the skip slices of the frames a gap in
	// frame_num infers name, and gapPic the frame being inferred.
	gapPPS *nal.H264PPS
	gapPic h264Pic

	candidates  []vk.StdVideoH264ProfileIdc
	profileInfo vk.VideoDecodeH264ProfileInfoKHR
	capsInfo    vk.VideoDecodeH264CapabilitiesKHR
	stdSPS      []vk.StdVideoH264SequenceParameterSet
	stdVUI      []vk.StdVideoH264SequenceParameterSetVui
	stdSPSLists []vk.StdVideoH264ScalingLists
	stdPPS      []vk.StdVideoH264PictureParameterSet
	stdPPSLists []vk.StdVideoH264ScalingLists
	add         vk.VideoDecodeH264SessionParametersAddInfoKHR
	create      vk.VideoDecodeH264SessionParametersCreateInfoKHR

	pic       vk.StdVideoDecodeH264PictureInfo
	picInfo   vk.VideoDecodeH264PictureInfoKHR
	setupStd  vk.StdVideoDecodeH264ReferenceInfo
	setupInfo vk.VideoDecodeH264DpbSlotInfoKHR
	refStd    [h264MaxSlots]vk.StdVideoDecodeH264ReferenceInfo
	refInfo   [h264MaxSlots]vk.VideoDecodeH264DpbSlotInfoKHR
	refSlots  [h264MaxSlots]int
	refPtrs   [h264MaxSlots]unsafe.Pointer
	req       decodePicture
}

// newH264Decoder reads config, the track's avcC
// (AVCDecoderConfigurationRecord, ISO/IEC 14496-15 5.3.3.1): the NAL
// unit length size and the parameter sets.
func newH264Decoder(config []byte) (decodeCodec, error) {
	c := &h264Decoder{candidates: make([]vk.StdVideoH264ProfileIdc, 0, 3)}
	c.state.reset()
	if len(config) < 7 || config[0] != 1 {
		return nil, fmt.Errorf("avcC of %d bytes, version %v: %w", len(config), config[:min(len(config), 1)], demux.ErrMalformed)
	}
	c.lengthSize = int(config[4]&3) + 1
	if c.lengthSize == 3 {
		return nil, fmt.Errorf("avcC NAL unit length size 3: %w", demux.ErrMalformed)
	}
	b := config[5:]
	for set := range 2 {
		if len(b) < 1 {
			return nil, fmt.Errorf("avcC ends before its parameter set count: %w", demux.ErrMalformed)
		}
		n := int(b[0])
		if set == 0 {
			n &= 0x1f
		}
		b = b[1:]
		for range n {
			if len(b) < 2 || len(b) < 2+int(b[0])<<8|int(b[1]) {
				return nil, fmt.Errorf("avcC ends inside a parameter set: %w", demux.ErrMalformed)
			}
			size := int(b[0])<<8 | int(b[1])
			if err := c.parameterSet(b[2 : 2+size]); err != nil {
				return nil, fmt.Errorf("avcC: %w", err)
			}
			b = b[2+size:]
		}
	}
	for _, s := range c.sps {
		if s != nil {
			return c, nil
		}
	}
	return nil, fmt.Errorf("avcC holds no SPS: %w", demux.ErrMalformed)
}

// parameterSet keeps unit, an SPS or PPS, when it differs from the one
// of its ID there is.
func (c *h264Decoder) parameterSet(unit []byte) error {
	if len(unit) < 2 {
		return fmt.Errorf("%w parameter set of %d bytes", nal.ErrH264, len(unit))
	}
	c.rbsp = nal.Unescape(c.rbsp[:0], unit[1:])
	switch nal.H264Type(unit) {
	case nal.H264NALSPS:
		s := &nal.H264SPS{}
		if err := s.Parse(c.rbsp); err != nil {
			return err
		}
		if bytes.Equal(c.spsRaw[s.ID], unit) {
			return nil
		}
		level, err := h264LevelOf(s)
		if err != nil {
			return err
		}
		c.sps[s.ID], c.spsRaw[s.ID], c.spsLevel[s.ID] = s, bytes.Clone(unit), level
	case nal.H264NALPPS:
		p := &nal.H264PPS{}
		if err := p.Parse(c.rbsp, &c.sps); err != nil {
			return err
		}
		if bytes.Equal(c.ppsRaw[p.ID], unit) {
			return nil
		}
		c.pps[p.ID], c.ppsRaw[p.ID] = p, bytes.Clone(unit)
	default:
		return fmt.Errorf("%w NAL unit type %d is no parameter set", nal.ErrH264, nal.H264Type(unit))
	}
	c.paramsChanged = true
	return nil
}

// h264Slice is a slice of the picture being read: its NAL unit, and of
// a slice after the picture's first, whether it is an I or SI slice.
type h264Slice struct {
	unit  []byte
	intra bool
}

// h264Intra reports whether the slice h heads is an I or SI slice.
func h264Intra(h *nal.H264SliceHeader) bool {
	return h.SliceType == nal.H264SliceI || h.SliceType == nal.H264SliceSI
}

// decode reads the packet's NAL units, length-prefixed, and decodes the
// pictures its slices make: a frame, a field, or a frame's two fields,
// each with the parameter sets that come before it, which a stream may
// change between the two fields. A packet that fails leaves the
// reference state as it was before its first picture, so the packet
// decodes again from its start: a frame's first field decoded before
// its second failed is forgotten, its slot free again. The frames a gap
// in frame_num before the packet infers stay, each a picture complete
// in its own right that may have taken the slot of a reference frame it
// slid out.
func (c *h264Decoder) decode(v *VideoDecoder, p demux.Packet) error {
	c.slices, c.part, c.kept = c.slices[:0], 0, false
	err := c.pictures(v, p)
	if err != nil && c.kept {
		c.state = c.before
	}
	return err
}

// pictures decodes the pictures of p, one at a time.
func (c *h264Decoder) pictures(v *VideoDecoder, p demux.Packet) error {
	for data := p.Data; len(data) > 0; {
		if len(data) < c.lengthSize {
			return fmt.Errorf("%d bytes left for a %d-byte NAL unit length: %w", len(data), c.lengthSize, demux.ErrMalformed)
		}
		n := 0
		for _, b := range data[:c.lengthSize] {
			n = n<<8 | int(b)
		}
		data = data[c.lengthSize:]
		if n > len(data) {
			return fmt.Errorf("a %d-byte NAL unit in %d bytes: %w", n, len(data), demux.ErrMalformed)
		}
		unit := data[:n]
		data = data[n:]
		if n == 0 {
			continue
		}
		switch nal.H264Type(unit) {
		case nal.H264NALSPS, nal.H264NALPPS:
			if err := c.picture(v, p); err != nil {
				return err
			}
			if err := c.parameterSet(unit); err != nil {
				return err
			}
		case nal.H264NALSlice, nal.H264NALIDR:
			s := h264Slice{unit: unit}
			if len(c.slices) > 0 {
				c.rbsp = nal.Unescape(c.rbsp[:0], unit[1:min(len(unit), 17)])
				if err := c.other.ParseType(c.rbsp); err != nil {
					return err
				}
				if c.other.FirstMbInSlice == 0 {
					if err := c.picture(v, p); err != nil {
						return err
					}
				}
				s.intra = h264Intra(&c.other)
			}
			c.slices = append(c.slices, s)
		}
	}
	return c.picture(v, p)
}

// header reads the slice header of unit into h, unescaping only as much
// of the slice as the header needs.
func (c *h264Decoder) header(h *nal.H264SliceHeader, unit []byte) error {
	end := min(len(unit), 1+sliceHeaderBytes)
	c.rbsp = nal.Unescape(c.rbsp[:0], unit[1:end])
	err := h.Parse(c.rbsp, unit[0], &c.pps, &c.sps)
	if err != nil && end < len(unit) {
		c.rbsp = nal.Unescape(c.rbsp[:0], unit[1:])
		err = h.Parse(c.rbsp, unit[0], &c.pps, &c.sps)
	}
	return err
}

// picture decodes the slices read, if any: one picture, the packet's
// next. A packet holds one picture, or the two fields of a frame. A
// frame, or the second field of one, completes a frame for Next; a
// first field waits in its DPB slot for the second, and comes out with
// it, their lines woven. A field whose frame's other field does not
// follow is decoded for the pictures that predict from it and never
// shown.
func (c *h264Decoder) picture(v *VideoDecoder, p demux.Packet) error {
	if len(c.slices) == 0 {
		return nil
	}
	slices, part := c.slices, c.part
	c.slices, c.part = c.slices[:0], c.part+1
	first := slices[0].unit
	if err := c.header(&c.hdr, first); err != nil {
		return err
	}
	sps := c.sps[c.pps[c.hdr.PPSID].SPSID]
	if err := h264Decodes(sps); err != nil {
		return err
	}
	pic := h264Pic{hdr: &c.hdr, sps: sps, idr: nal.H264Type(first) == nal.H264NALIDR, ref: nal.H264RefIdc(first) != 0, intra: h264Intra(&c.hdr), fields: h264FieldsOf(&c.hdr)}
	if part > 1 || part == 1 && (c.whole || pic.fields == h264Frame) {
		return fmt.Errorf("%w: the packet holds more pictures than a frame or the two fields of one", nal.ErrH264)
	}
	c.whole = pic.fields == h264Frame
	total := len(startCode) + len(first)
	for _, s := range slices[1:] {
		pic.intra = pic.intra && s.intra
		total += len(startCode) + len(s.unit)
	}
	shape, err := c.shape(sps)
	if err != nil {
		return err
	}
	c.state.slots = shape.slots
	gap, err := c.state.begin(&pic)
	if err == nil && gap > 0 {
		err = c.inferGap(v, sps, shape, gap)
	}
	if err == nil && !c.kept {
		c.before, c.kept = c.state, true
	}
	if err == nil {
		err = c.state.place(&pic)
	}
	if errors.Is(err, errSkipPicture) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := c.prepare(v, sps, shape); err != nil {
		return err
	}
	completes := pic.fields == h264Frame || pic.second
	var frame *VideoFrame
	if completes {
		if frame, err = v.frame(); err != nil {
			return err
		}
	}
	buf, err := v.stream(total)
	if err != nil {
		return err
	}
	c.offsets = c.offsets[:0]
	at := 0
	for i := range slices {
		c.offsets = append(c.offsets, intconv.Must[uint32](at))
		at += copy(buf[at:], startCode)
		at += copy(buf[at:], slices[i].unit)
	}
	if err := c.submit(v, &pic, frame, total); err != nil {
		return err
	}
	c.state.finish(&pic)
	if pic.idr || pic.mmco5 {
		v.drain(pic.idr && c.hdr.NoOutputOfPriorPics)
	}
	switch {
	case !completes:
		c.fieldPTS, c.fieldDuration = p.PTS, p.Duration
		return nil
	case pic.second && part == 0:
		frame.pts, frame.duration = c.fieldPTS, c.fieldDuration+p.Duration
	default:
		frame.pts, frame.duration = p.PTS, p.Duration
	}
	x0, y0, x1, y1 := sps.CropRect()
	frame.rect = image.Rect(x0, y0, x1, y1)
	held := &c.state.refs[pic.slot]
	switch {
	case pic.fields == h264Frame && !sps.MbAdaptiveFrameField:
	case held.poc[1] < held.poc[0]:
		frame.fields = BottomFieldFirst
	default:
		frame.fields = TopFieldFirst
	}
	v.queue(frame, held.order(), false, false)
	return nil
}

// prepare makes the session for shape, sps active, and the session
// parameters of the parameter sets the stream has sent.
func (c *h264Decoder) prepare(v *VideoDecoder, sps *nal.H264SPS, shape decodeShape) error {
	c.active = sps
	if err := v.shapeFor(shape); err != nil {
		return err
	}
	if shape.fields && !v.copies {
		return fmt.Errorf("%w: SPS %d codes fields or field-pair frames, which are verified only on a device that decodes into its reference pictures alone, where a frame's fields meet before they are copied out; %s decodes into output pictures apart from them", ErrNoVideoDecoder, sps.ID, v.d.name)
	}
	if !c.paramsChanged {
		return nil
	}
	if err := v.createParameters(); err != nil {
		return err
	}
	c.paramsChanged = false
	return nil
}

// h264FieldFlags are the StdVideoDecodeH264ReferenceInfo flags that
// name fields of a DPB slot's picture.
func h264FieldFlags(fields h264Fields) uint32 {
	return bitfield(flagBit{fields&h264Top != 0, vk.StdVideoDecodeH264ReferenceTopField}, flagBit{fields&h264Bottom != 0, vk.StdVideoDecodeH264ReferenceBottomField})
}

// submit decodes pic from the first total bytes of the stream buffer,
// its slices at c.offsets, into frame where it completes one. A frame
// predicts from the reference frames whose fields both are references,
// a field from every reference field. A Vulkan Video reference picture
// is what its slot was decoded as: a frame decoded as one picture is
// named whole, and a device takes its fields where the picture being
// decoded is a field; one decoded as two fields is named by the fields
// that are references, each a reference picture of the ones a device
// decodes a picture from.
func (c *h264Decoder) submit(v *VideoDecoder, pic *h264Pic, frame *VideoFrame, total int) error {
	c.fill(pic)
	c.req = decodePicture{frame: frame, coded: v.shape.coded, bytes: total, info: unsafe.Pointer(&c.picInfo), setup: pic.slot, setupInfo: unsafe.Pointer(&c.setupInfo), held: pic.second && pic.ref}
	n, active := 0, 0
	for slot := range c.state.slots {
		r := &c.state.refs[slot]
		marked := r.marked()
		if marked == 0 || pic.fields == h264Frame && marked != h264Frame {
			continue
		}
		switch {
		case r.short != 0 && r.long != 0:
			return fmt.Errorf("%w: H.264 frame %d has a field that is a short-term reference and one that is a long-term reference, which a Vulkan Video reference picture, long-term or not as a whole, cannot say", ErrNoVideoDecoder, r.frameNum)
		case r.whole && marked != h264Frame:
			return fmt.Errorf("%w: H.264 frame %d, decoded as one picture, has its %s alone left a reference, which a Vulkan Video reference picture cannot say: it names the fields of a frame decoded as two", ErrNoVideoDecoder, r.frameNum, marked)
		}
		c.refStd[n] = vk.StdVideoDecodeH264ReferenceInfo{FrameNum: uint16(r.frameNum & 0xffff), PicOrderCnt: r.poc}
		active++
		if !r.whole {
			c.refStd[n].Flags = h264FieldFlags(marked)
			active += b2i(marked == h264Frame)
		}
		if r.long != 0 {
			c.refStd[n].Flags |= vk.StdVideoDecodeH264ReferenceUsedForLongTerm
			c.refStd[n].FrameNum = uint16(r.longIdx & 0xffff)
		}
		c.refInfo[n] = vk.VideoDecodeH264DpbSlotInfoKHR{SType: vk.StructureTypeVideoDecodeH264DpbSlotInfoKhr, PStdReferenceInfo: &c.refStd[n]}
		c.refSlots[n], c.refPtrs[n] = slot, unsafe.Pointer(&c.refInfo[n])
		n++
	}
	if limit := v.activeRefs(); active > limit {
		return fmt.Errorf("%w: an H.264 %s predicts from %d reference pictures, each field decoded as a picture one, where %s decodes from %d", ErrNoVideoDecoder, pic.fields, active, v.d.name, limit)
	}
	c.req.refs, c.req.refInfo = c.refSlots[:n], c.refPtrs[:n]
	return v.submit(&c.req)
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// inferGap decodes a picture for each of the gap frames in frame_num
// before the picture being decoded, a skip slice copying the reference
// frame before it, and takes it into the reference frames unshown. The
// frames are non-existing (8.2.5.2), but the device needs a decoded
// picture in each DPB slot a decode names; they go to it as ordinary
// short-term references, not flagged is_non_existing, so a stream that
// predicts from them (frames lost) reads the copy, as ffmpeg conceals
// the loss.
func (c *h264Decoder) inferGap(v *VideoDecoder, sps *nal.H264SPS, shape decodeShape, gap int) error {
	if !sps.FrameMbsOnly {
		return fmt.Errorf("%w: frame_num skips %d frames in SPS %d, a sequence of fields or field-pair frames, where the decoder infers no frames for a gap", ErrNoVideoDecoder, gap, sps.ID)
	}
	pps, err := c.skipPPS(sps)
	if err != nil {
		return err
	}
	if err := c.prepare(v, sps, shape); err != nil {
		return err
	}
	lsbMask := int32(1)<<sps.Log2MaxPicOrderCntLsb - 1
	for range gap {
		q := &c.gapPic
		if err := c.state.infer(q, sps); err != nil {
			return err
		}
		q.hdr.PPSID = pps.ID
		unit := nal.H264SkipSlice(sps, pps, q.hdr.FrameNum, intconv.Must[uint32](q.poc[0]&lsbMask))
		frame, err := v.frame()
		if err != nil {
			return err
		}
		total := len(startCode) + len(unit)
		buf, err := v.stream(total)
		if err != nil {
			return err
		}
		copy(buf[copy(buf, startCode):], unit)
		c.offsets = append(c.offsets[:0], 0)
		if err := c.submit(v, q, frame, total); err != nil {
			return err
		}
		frame.free(0)
		c.state.finish(q)
	}
	return nil
}

// skipPPS is the PPS the skip slices of sps's gap frames name: one the
// parameter sets hold under an ID the stream leaves unused.
func (c *h264Decoder) skipPPS(sps *nal.H264SPS) (*nal.H264PPS, error) {
	if p := c.gapPPS; p != nil && c.pps[p.ID] == p && p.SPSID == sps.ID {
		return p, nil
	}
	for id := len(c.pps) - 1; id >= 0; id-- {
		if c.pps[id] != nil {
			continue
		}
		p := nal.H264SkipPPS(uint8(id), sps.ID)
		c.gapPPS, c.pps[id], c.paramsChanged = &p, &p, true
		return &p, nil
	}
	return nil, fmt.Errorf("%w: the stream uses every PPS ID, leaving none for the skip slices of the frames a gap in frame_num infers: %w", nal.ErrH264, nal.ErrUnsupported)
}

// fill lays out pic's picture info and the reference info of the DPB
// slot it sets up.
func (c *h264Decoder) fill(pic *h264Pic) {
	h := pic.hdr
	var flags uint32
	if pic.idr {
		flags |= vk.StdVideoDecodeH264PictureIdr
	}
	if pic.intra {
		flags |= vk.StdVideoDecodeH264PictureIsIntra
	}
	if pic.ref {
		flags |= vk.StdVideoDecodeH264PictureIsReference
	}
	flags |= bitfield(
		flagBit{pic.fields != h264Frame, vk.StdVideoDecodeH264PictureFieldPic},
		flagBit{pic.fields == h264Bottom, vk.StdVideoDecodeH264PictureBottomField},
		flagBit{pic.second, vk.StdVideoDecodeH264PictureComplementaryFieldPair},
	)
	pps := c.pps[h.PPSID]
	c.pic = vk.StdVideoDecodeH264PictureInfo{
		Flags: flags, SeqParameterSetID: pps.SPSID, PicParameterSetID: pps.ID,
		FrameNum: uint16(h.FrameNum & 0xffff), IdrPicID: h.IDRPicID, PicOrderCnt: pic.poc,
	}
	c.picInfo = vk.VideoDecodeH264PictureInfoKHR{
		SType: vk.StructureTypeVideoDecodeH264PictureInfoKhr, PStdPictureInfo: &c.pic,
		SliceCount: intconv.Must[uint32](len(c.offsets)), PSliceOffsets: &c.offsets[0],
	}
	c.setupStd = vk.StdVideoDecodeH264ReferenceInfo{FrameNum: uint16(h.FrameNum & 0xffff), PicOrderCnt: pic.poc}
	if pic.fields != h264Frame {
		c.setupStd.Flags = h264FieldFlags(pic.fields)
	}
	if idx, long := h264LongTerm(pic); long {
		c.setupStd.Flags |= vk.StdVideoDecodeH264ReferenceUsedForLongTerm
		c.setupStd.FrameNum = uint16(idx & 0xffff)
	}
	c.setupInfo = vk.VideoDecodeH264DpbSlotInfoKHR{SType: vk.StructureTypeVideoDecodeH264DpbSlotInfoKhr, PStdReferenceInfo: &c.setupStd}
}

// h264LongTerm is the LongTermFrameIdx pic becomes a long-term
// reference frame at, if it does.
func h264LongTerm(pic *h264Pic) (uint32, bool) {
	h := pic.hdr
	if !pic.ref {
		return 0, false
	}
	if pic.idr {
		return 0, h.LongTermReference
	}
	for _, m := range h.MMCO[:h.NumMMCO] {
		if m.Op == 6 {
			return m.LongTermFrameIdx, true
		}
	}
	return 0, false
}

// h264Decodes refuses a sequence no device is asked to decode:
// pictures there is no format for (monochrome, colour planes coded
// apart, luma and chroma of different depths, a depth past 12 bits).
func h264Decodes(s *nal.H264SPS) error {
	if _, err := h264Samples(s); err != nil {
		return err
	}
	return nil
}

// h264Samples are the samples of s's pictures.
func h264Samples(s *nal.H264SPS) (videoSamples, error) {
	chroma, coloured := chromaOfIdc(s.ChromaFormatIdc)
	samples, ok := samplesOf(8+int(s.BitDepthLumaMinus8), chroma)
	if !coloured || !ok || s.SeparateColourPlane || s.BitDepthChromaMinus8 != s.BitDepthLumaMinus8 {
		return videoSamples{}, fmt.Errorf("%w: SPS %d is chroma_format_idc %d (colour planes coded apart: %v) at %d and %d bits, which pictures have no format for: %w",
			nal.ErrH264, s.ID, s.ChromaFormatIdc, s.SeparateColourPlane, 8+s.BitDepthLumaMinus8, 8+s.BitDepthChromaMinus8, nal.ErrUnsupported)
	}
	return samples, nil
}

// h264MaxDpbMbs is MaxDpbMbs (Table A-1) of each level of h264Levels.
var h264MaxDpbMbs = []int{396, 900, 2376, 2376, 2376, 4752, 8100, 8100, 18000, 20480, 32768, 32768, 34816, 110400, 184320, 184320, 696320, 696320, 696320}

// h264LevelOf is s's level as a StdVideo level enumerant, an index of
// h264Levels; level 1b counts as 1.1, the next above it.
func h264LevelOf(s *nal.H264SPS) (vk.StdVideoH264LevelIdc, error) {
	idc := int(s.LevelIdc)
	if idc == 9 {
		idc = 11
	}
	for i, l := range h264Levels {
		if l.idc == idc {
			return vk.StdVideoH264LevelIdc(i), nil
		}
	}
	return 0, fmt.Errorf("%w: SPS %d names level_idc %d, no level", nal.ErrH264, s.ID, s.LevelIdc)
}

// shape is the session s decodes in, and the profiles the device may
// decode it as: its own, then the ones above that contain it.
func (c *h264Decoder) shape(s *nal.H264SPS) (decodeShape, error) {
	level, err := h264LevelOf(s)
	if err != nil {
		return decodeShape{}, err
	}
	ycbcr, err := h264YCbCr(s)
	if err != nil {
		return decodeShape{}, err
	}
	samples, err := h264Samples(s)
	if err != nil {
		return decodeShape{}, err
	}
	c.candidates = c.candidates[:0]
	unverified := ""
	switch {
	case s.ProfileIdc == 66 && samples == nv12Samples:
		c.candidates = append(c.candidates, vk.StdVideoH264ProfileIdcBaseline)
		if s.Constraints&0x40 != 0 {
			c.candidates = append(c.candidates, vk.StdVideoH264ProfileIdcMain, vk.StdVideoH264ProfileIdcHigh)
		}
	case (s.ProfileIdc == 77 || s.ProfileIdc == h264Extended && s.Constraints&0x40 != 0) && samples == nv12Samples:
		c.candidates = append(c.candidates, vk.StdVideoH264ProfileIdcMain, vk.StdVideoH264ProfileIdcHigh)
	case s.ProfileIdc == 100 && samples == nv12Samples:
		c.candidates = append(c.candidates, vk.StdVideoH264ProfileIdcHigh)
	case s.ProfileIdc == h264High10 || s.ProfileIdc == h264High422 || s.ProfileIdc == h264High444:
		c.candidates = append(c.candidates, vk.StdVideoH264ProfileIdc(s.ProfileIdc))
		unverified = "decoding H.264 beyond 8-bit 4:2:0 (the High 10, High 4:2:2 and High 4:4:4 Predictive profiles)"
	default:
		return decodeShape{}, fmt.Errorf("%w: SPS %d is profile_idc %d at %s, none of Baseline, Main, Extended within Main, High, High 10, High 4:2:2 and High 4:4:4 Predictive: %w", nal.ErrH264, s.ID, s.ProfileIdc, samples, nal.ErrUnsupported)
	}
	refs := max(int(s.MaxNumRefFrames), 1)
	mbs := s.WidthMbs * s.FrameHeightMbs()
	reorder := min(h264MaxDpbMbs[level]/mbs, 16)
	switch {
	case s.VUIPresent && s.VUI.BitstreamRestriction:
		reorder = min(int(s.VUI.MaxNumReorderFrames), 16)
	case s.PicOrderCntType == 2:
		reorder = 0
	case s.Constraints&0x10 != 0 && (s.ProfileIdc == 44 || s.ProfileIdc == 86 || s.ProfileIdc == 100 || s.ProfileIdc == 110 || s.ProfileIdc == 122 || s.ProfileIdc == 244):
		reorder = 0
	}
	active := refs
	if !s.FrameMbsOnly {
		active *= 2
	}
	return decodeShape{
		profile: int(s.ProfileIdc), coded: [2]int{16 * s.WidthMbs, 16 * s.FrameHeightMbs()},
		slots: refs + 1, refs: active, reorder: reorder, samples: samples, ycbcr: ycbcr, fields: !s.FrameMbsOnly, unverified: unverified,
	}, nil
}

// The H.264 profiles past High, which the Vulkan headers have no
// enumerant for but High 4:4:4 Predictive: a device is asked for them
// by their profile_idc. A stream of the Extended profile that says it
// obeys Main's constraints (constraint_set1_flag) decodes as Main.
const (
	h264Extended = 88
	h264High10   = 110
	h264High422  = 122
	h264High444  = 244
)

// h264YCbCr is how s's pictures convert to RGB, as its VUI says.
func h264YCbCr(s *nal.H264SPS) (YCbCr, error) {
	_, y0, _, y1 := s.CropRect()
	v := &s.VUI
	c, err := videoSignal{
		typed: s.VUIPresent && v.VideoSignalTypePresent, fullRange: v.FullRange,
		described: v.ColourDescriptionPresent, matrix: v.MatrixCoefficients,
		sited: s.VUIPresent && v.ChromaLocInfoPresent, siting: v.ChromaSampleLocTypeTop, lines: y1 - y0,
	}.ycbcr()
	if err != nil {
		return YCbCr{}, fmt.Errorf("%w: SPS %d: %w: %w", nal.ErrH264, s.ID, err, nal.ErrUnsupported)
	}
	return c, nil
}

func (c *h264Decoder) profiles() int { return len(c.candidates) }

func (c *h264Decoder) profile(i int, next unsafe.Pointer) unsafe.Pointer {
	c.profileInfo = vk.VideoDecodeH264ProfileInfoKHR{
		SType: vk.StructureTypeVideoDecodeH264ProfileInfoKhr, PNext: (*uintptr)(next),
		StdProfileIdc: c.candidates[i], PictureLayout: vk.VideoDecodeH264PictureLayoutProgressiveKhr,
	}
	if !c.active.FrameMbsOnly {
		c.profileInfo.PictureLayout = vk.VideoDecodeH264PictureLayoutInterlacedInterleavedLinesBitKhr
	}
	return unsafe.Pointer(&c.profileInfo)
}

func (c *h264Decoder) caps() unsafe.Pointer {
	c.capsInfo = vk.VideoDecodeH264CapabilitiesKHR{SType: vk.StructureTypeVideoDecodeH264CapabilitiesKhr}
	return unsafe.Pointer(&c.capsInfo)
}

func (c *h264Decoder) checkCaps() error {
	level := c.spsLevel[c.active.ID]
	if level > c.capsInfo.MaxLevelIdc {
		return fmt.Errorf("H.264 level %d.%d is past the device's (StdVideo level %d)", c.active.LevelIdc/10, c.active.LevelIdc%10, c.capsInfo.MaxLevelIdc)
	}
	return nil
}

func (c *h264Decoder) reset() { c.state.reset() }

// parameters lays out every parameter set the stream has sent as the
// session parameters' create chain.
func (c *h264Decoder) parameters() unsafe.Pointer {
	c.stdSPS, c.stdVUI, c.stdSPSLists = c.stdSPS[:0], c.stdVUI[:0], c.stdSPSLists[:0]
	c.stdPPS, c.stdPPSLists = c.stdPPS[:0], c.stdPPSLists[:0]
	for _, s := range c.sps {
		if s == nil {
			continue
		}
		c.stdVUI = append(c.stdVUI, h264StdVUI(&s.VUI))
		c.stdSPSLists = append(c.stdSPSLists, h264StdLists(&s.Scaling, 0xff))
	}
	for _, p := range c.pps {
		if p == nil {
			continue
		}
		mask := uint16(0x3f)
		if p.Transform8x8Mode {
			mask = 0xff
		}
		c.stdPPSLists = append(c.stdPPSLists, h264StdLists(&p.Scaling, mask))
	}
	i := 0
	for _, s := range c.sps {
		if s == nil {
			continue
		}
		c.stdSPS = append(c.stdSPS, h264StdSPS(s, c.spsLevel[s.ID], &c.stdVUI[i], &c.stdSPSLists[i]))
		i++
	}
	i = 0
	for _, p := range c.pps {
		if p == nil {
			continue
		}
		c.stdPPS = append(c.stdPPS, h264StdPPS(p, &c.stdPPSLists[i]))
		i++
	}
	c.add = vk.VideoDecodeH264SessionParametersAddInfoKHR{
		SType:       vk.StructureTypeVideoDecodeH264SessionParametersAddInfoKhr,
		StdSPSCount: intconv.Must[uint32](len(c.stdSPS)), PStdSPSs: &c.stdSPS[0],
		StdPPSCount: intconv.Must[uint32](len(c.stdPPS)),
	}
	if len(c.stdPPS) > 0 {
		c.add.PStdPPSs = &c.stdPPS[0]
	}
	c.create = vk.VideoDecodeH264SessionParametersCreateInfoKHR{
		SType: vk.StructureTypeVideoDecodeH264SessionParametersCreateInfoKhr, MaxStdSPSCount: c.add.StdSPSCount, MaxStdPPSCount: max(c.add.StdPPSCount, 1),
		PParametersAddInfo: &c.add,
	}
	return unsafe.Pointer(&c.create)
}

// h264StdSPS lays s out as a StdVideo SPS at level, its VUI and scaling lists
// in vui and lists.
func h264StdSPS(s *nal.H264SPS, level vk.StdVideoH264LevelIdc, vui *vk.StdVideoH264SequenceParameterSetVui, lists *vk.StdVideoH264ScalingLists) vk.StdVideoH264SequenceParameterSet {
	var flags uint32
	for i := range 6 {
		if s.Constraints>>(7-i)&1 != 0 {
			flags |= vk.StdVideoH264SpsConstraintSet0 << i
		}
	}
	flags |= bitfield(
		flagBit{s.Direct8x8Inference, vk.StdVideoH264SpsDirect8x8Inference},
		flagBit{s.MbAdaptiveFrameField, vk.StdVideoH264SpsMbAdaptiveFrameField},
		flagBit{s.FrameMbsOnly, vk.StdVideoH264SpsFrameMbsOnly},
		flagBit{s.DeltaPicOrderAlwaysZero, vk.StdVideoH264SpsDeltaPicOrderAlwaysZero},
		flagBit{s.SeparateColourPlane, vk.StdVideoH264SpsSeparateColourPlane},
		flagBit{s.GapsInFrameNumAllowed, vk.StdVideoH264SpsGapsInFrameNumAllowed},
		flagBit{s.QpprimeYZeroTransformBypass, vk.StdVideoH264SpsQpprimeYZeroTransformBypass},
		flagBit{s.FrameCropping, vk.StdVideoH264SpsFrameCropping},
		flagBit{s.ScalingMatrixPresent, vk.StdVideoH264SpsSeqScalingMatrixPresent},
		flagBit{s.VUIPresent, vk.StdVideoH264SpsVuiParametersPresent},
	)
	out := vk.StdVideoH264SequenceParameterSet{
		Flags: flags, ProfileIdc: vk.StdVideoH264ProfileIdc(s.ProfileIdc), LevelIdc: level,
		ChromaFormatIdc: vk.StdVideoH264ChromaFormatIdc(s.ChromaFormatIdc), SeqParameterSetID: s.ID,
		BitDepthLumaMinus8: s.BitDepthLumaMinus8, BitDepthChromaMinus8: s.BitDepthChromaMinus8,
		Log2MaxFrameNumMinus4: s.Log2MaxFrameNum - 4, PicOrderCntType: vk.StdVideoH264PocType(s.PicOrderCntType),
		OffsetForNonRefPic: s.OffsetForNonRefPic, OffsetForTopToBottomField: s.OffsetForTopToBottomField,
		NumRefFramesInPicOrderCntCycle: s.NumRefFramesInPicOrderCntCycle, MaxNumRefFrames: s.MaxNumRefFrames,
		PicWidthInMbsMinus1: intconv.Must[uint32](s.WidthMbs - 1), PicHeightInMapUnitsMinus1: intconv.Must[uint32](s.HeightMapUnits - 1),
		FrameCropLeftOffset: s.Crop[0], FrameCropRightOffset: s.Crop[1], FrameCropTopOffset: s.Crop[2], FrameCropBottomOffset: s.Crop[3],
	}
	if s.PicOrderCntType == 0 {
		out.Log2MaxPicOrderCntLsbMinus4 = s.Log2MaxPicOrderCntLsb - 4
	}
	if s.NumRefFramesInPicOrderCntCycle > 0 {
		out.POffsetForRefFrame = &s.OffsetForRefFrame[0]
	}
	if s.ScalingMatrixPresent {
		out.PScalingLists = unsafe.Pointer(lists)
	}
	if s.VUIPresent {
		out.PSequenceParameterSetVui = vui
	}
	return out
}

// h264StdVUI lays v out as a StdVideo VUI, its HRD parameters left out:
// decoding reads none of them.
func h264StdVUI(v *nal.H264VUI) vk.StdVideoH264SequenceParameterSetVui {
	flags := bitfield(
		flagBit{v.AspectRatioInfoPresent, vk.StdVideoH264VuiAspectRatioInfoPresent},
		flagBit{v.OverscanInfoPresent, vk.StdVideoH264VuiOverscanInfoPresent},
		flagBit{v.OverscanAppropriate, vk.StdVideoH264VuiOverscanAppropriate},
		flagBit{v.VideoSignalTypePresent, vk.StdVideoH264VuiVideoSignalTypePresent},
		flagBit{v.FullRange, vk.StdVideoH264VuiVideoFullRange},
		flagBit{v.ColourDescriptionPresent, vk.StdVideoH264VuiColorDescriptionPresent},
		flagBit{v.ChromaLocInfoPresent, vk.StdVideoH264VuiChromaLocInfoPresent},
		flagBit{v.TimingInfoPresent, vk.StdVideoH264VuiTimingInfoPresent},
		flagBit{v.FixedFrameRate, vk.StdVideoH264VuiFixedFrameRate},
		flagBit{v.BitstreamRestriction, vk.StdVideoH264VuiBitstreamRestriction},
	)
	return vk.StdVideoH264SequenceParameterSetVui{
		Flags: flags, AspectRatioIdc: vk.StdVideoH264AspectRatioIdc(v.AspectRatioIdc), SarWidth: v.SarWidth, SarHeight: v.SarHeight,
		VideoFormat: v.VideoFormat, ColourPrimaries: v.ColourPrimaries, TransferCharacteristics: v.TransferCharacteristics, MatrixCoefficients: v.MatrixCoefficients,
		NumUnitsInTick: v.NumUnitsInTick, TimeScale: v.TimeScale, MaxNumReorderFrames: v.MaxNumReorderFrames, MaxDecFrameBuffering: v.MaxDecFrameBuffering,
		ChromaSampleLocTypeTopField: v.ChromaSampleLocTypeTop, ChromaSampleLocTypeBottomField: v.ChromaSampleLocTypeBot,
	}
}

// h264StdLists lays l out as StdVideo scaling lists, the lists in
// present marked so: each is resolved, so none falls back.
func h264StdLists(l *nal.H264ScalingLists, present uint16) vk.StdVideoH264ScalingLists {
	return vk.StdVideoH264ScalingLists{ScalingListPresentMask: present, ScalingList4x4: l.List4x4, ScalingList8x8: l.List8x8}
}

// h264StdPPS lays p out as a StdVideo PPS, its scaling lists in lists.
func h264StdPPS(p *nal.H264PPS, lists *vk.StdVideoH264ScalingLists) vk.StdVideoH264PictureParameterSet {
	flags := bitfield(
		flagBit{p.Transform8x8Mode, vk.StdVideoH264PpsTransform8x8Mode},
		flagBit{p.RedundantPicCntPresent, vk.StdVideoH264PpsRedundantPicCntPresent},
		flagBit{p.ConstrainedIntraPred, vk.StdVideoH264PpsConstrainedIntraPred},
		flagBit{p.DeblockingFilterControlPresent, vk.StdVideoH264PpsDeblockingFilterControl},
		flagBit{p.WeightedPred, vk.StdVideoH264PpsWeightedPred},
		flagBit{p.BottomFieldPicOrderInFramePresent, vk.StdVideoH264PpsBottomFieldPicOrderInFramePresent},
		flagBit{p.EntropyCodingMode, vk.StdVideoH264PpsEntropyCodingMode},
		flagBit{p.ScalingMatrixPresent, vk.StdVideoH264PpsPicScalingMatrixPresent},
	)
	out := vk.StdVideoH264PictureParameterSet{
		Flags: flags, SeqParameterSetID: p.SPSID, PicParameterSetID: p.ID,
		NumRefIdxL0DefaultActiveMinus1: p.NumRefIdxL0DefaultActiveMinus1, NumRefIdxL1DefaultActiveMinus1: p.NumRefIdxL1DefaultActiveMinus1,
		WeightedBipredIdc: vk.StdVideoH264WeightedBipredIdc(p.WeightedBipredIdc), PicInitQpMinus26: p.PicInitQpMinus26, PicInitQsMinus26: p.PicInitQsMinus26,
		ChromaQpIndexOffset: p.ChromaQpIndexOffset, SecondChromaQpIndexOffset: p.SecondChromaQpIndexOffset,
	}
	if p.ScalingMatrixPresent {
		out.PScalingLists = unsafe.Pointer(lists)
	}
	return out
}
