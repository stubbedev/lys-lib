package gpu

import (
	"fmt"
	"image"
	"slices"
	"unsafe"

	"github.com/stubbedev/lys-lib/intconv"
	"github.com/stubbedev/lys-lib/internal/vp9"
	"github.com/stubbedev/lys-lib/media/demux"
	"github.com/stubbedev/lys-lib/third_party/vk"
)

// vp9Decoder is VP9's part of a decoder, as av1Decoder is AV1's. VP9
// has no session parameters: every frame's uncompressed header says
// all a decode needs.
type vp9Decoder struct {
	stream  vp9.Stream
	started bool
	hdr     vp9.FrameHeader
	frames  [vp9.MaxFrames][]byte
	refSlots

	profileInfo vk.VideoDecodeVP9ProfileInfoKHR
	capsInfo    vk.VideoDecodeVP9CapabilitiesKHR

	pic      vk.StdVideoDecodeVP9PictureInfo
	picInfo  vk.VideoDecodeVP9PictureInfoKHR
	colorStd vk.StdVideoVP9ColorConfig
	lfStd    vk.StdVideoVP9LoopFilter
	segStd   vk.StdVideoVP9Segmentation
	active   [vp9.RefsPerFrame]int
	// refInfo stays nil: VP9 references carry no DPB slot info.
	refInfo [vp9.RefsPerFrame]unsafe.Pointer
	req     decodePicture
}

// vp9RefUpscale is how many times its reference's size a frame may be
// (the frame size with refs semantics, 7.2.5).
const vp9RefUpscale = 16

// newVP9Decoder makes VP9's part of a decoder. The configuration record
// (MP4's vpcC; WebM has none) repeats what every key frame's header
// says, so the decoder reads the headers instead.
func newVP9Decoder([]byte) (decodeCodec, error) {
	c := &vp9Decoder{}
	c.forget()
	return c, nil
}

func (c *vp9Decoder) reset() {
	c.stream.Reset()
	c.forget()
	c.started = false
}

// decode reads the packet's frames, a superframe's hidden frames and
// the one it shows, and decodes them; the one it shows goes to Next.
// Decoding starts at a packet marked a keyframe.
func (c *vp9Decoder) decode(v *VideoDecoder, p demux.Packet) error {
	if !c.started && !p.Key {
		return nil
	}
	frames, err := vp9.Frames(p.Data, &c.frames)
	if err != nil {
		return err
	}
	for i, f := range frames {
		if err := c.frame(v, p, f); err != nil {
			return fmt.Errorf("frame %d of %d: %w", i, len(frames), err)
		}
	}
	return nil
}

// frame decodes b, one frame, or shows the frame decoded hidden that
// its header names.
func (c *vp9Decoder) frame(v *VideoDecoder, p demux.Packet, b []byte) error {
	h := &c.hdr
	if err := c.stream.ParseFrame(h, b); err != nil {
		return err
	}
	if h.ShowExistingFrame {
		f, _, _, err := c.show(v, h.FrameToShowMapIdx)
		if err != nil {
			return err
		}
		f.pts, f.duration = p.PTS, p.Duration
		v.queue(f, 0, false, false)
		return nil
	}
	reset, err := c.contextReset()
	if err != nil {
		return err
	}
	shape, err := c.shape()
	if err != nil {
		return err
	}
	if v.session != 0 && !c.independent() {
		if shape, err = c.within(v.shape, shape); err != nil {
			return err
		}
	}
	if v.session == 0 || shape != v.shape {
		c.forget()
	}
	if err := v.shapeFor(shape); err != nil {
		return err
	}
	setup := c.free()
	frame, err := v.frame()
	if err != nil {
		return err
	}
	buf, err := v.stream(len(b))
	if err != nil {
		return err
	}
	copy(buf, b)
	c.fill(reset)
	c.req = decodePicture{frame: frame, coded: [2]int{h.FrameWidth, h.FrameHeight}, bytes: len(b), info: unsafe.Pointer(&c.picInfo), setup: setup}
	n := 0
	if !h.FrameIsIntra() {
		for i, idx := range h.RefFrameIdx {
			slot := c.slotOf[idx]
			if slot < 0 {
				return fmt.Errorf("gpu: a VP9 %s references slot %d, which holds no decoded picture", h.FrameType, idx)
			}
			if ref := v.coded[slot]; 2*h.FrameWidth < ref[0] || 2*h.FrameHeight < ref[1] || h.FrameWidth > vp9RefUpscale*ref[0] || h.FrameHeight > vp9RefUpscale*ref[1] {
				return fmt.Errorf("%w: a VP9 %s of %dx%d names reference frame slot %d, a %dx%d picture, which VP9 scales to no frame less than half or more than %d times its size: the frame may name it only where no block predicts from it, which the decoder does not read the blocks to tell", ErrNoVideoDecoder, h.FrameType, h.FrameWidth, h.FrameHeight, idx, ref[0], ref[1], vp9RefUpscale)
			}
			c.picInfo.ReferenceNameSlotIndices[i] = intconv.Must[int32](slot)
			if slices.Contains(c.active[:n], slot) {
				continue
			}
			c.active[n] = slot
			n++
		}
	}
	c.req.refs, c.req.refInfo = c.active[:n], c.refInfo[:n]
	if err := v.submit(&c.req); err != nil {
		return err
	}
	c.started = true
	pic := &c.pictures[setup]
	pic.rect = image.Rect(0, 0, h.FrameWidth, h.FrameHeight)
	frame.rect = pic.rect
	c.refresh(setup, h.RefreshFrameFlags)
	if h.ShowFrame {
		frame.pts, frame.duration = p.PTS, p.Duration
		v.queue(frame, 0, false, false)
	} else {
		c.keep(setup, frame, true)
	}
	return nil
}

// independent reports whether the frame c.hdr heads takes nothing from
// the frames before it and leaves nothing of them to the frames after
// it: a key frame, or an intra-only frame that refreshes every
// reference frame slot and resets every frame context. Such a frame
// may start another video session, which has neither reference
// pictures nor frame contexts.
func (c *vp9Decoder) independent() bool {
	h := &c.hdr
	return h.FrameType == vp9.KeyFrame || h.IntraOnly && h.RefreshFrameFlags == 1<<vp9.NumRefFrames-1 && (h.ErrorResilientMode || h.ResetFrameContext == vp9.ResetAllContexts)
}

// within is the session a frame that is not independent decodes in:
// the one there is, when the frame is of its format and no larger than
// the pictures the session was made with. Its size is its own (VP9
// scales the references of another size as it predicts from them), but
// another session would have none of what the frame, or the frames
// after it, decode from.
func (c *vp9Decoder) within(session, frame decodeShape) (decodeShape, error) {
	h := &c.hdr
	sized := frame
	sized.coded = session.coded
	switch {
	case sized != session:
		return decodeShape{}, fmt.Errorf("%w: a VP9 %s is profile %d at %s with colours %+v, the frames before it profile %d at %s with colours %+v, and it keeps reference pictures or frame contexts of theirs (it refreshes slots %08b and resets contexts as reset_frame_context %d says), which the video session another format takes has none of", ErrNoVideoDecoder, h.FrameType, frame.profile, frame.samples, frame.ycbcr, session.profile, session.samples, session.ycbcr, h.RefreshFrameFlags, h.ResetFrameContext)
	case frame.coded[0] > session.coded[0] || frame.coded[1] > session.coded[1]:
		return decodeShape{}, fmt.Errorf("%w: a VP9 %s is %dx%d, larger than the %dx%d pictures of its video session, and it keeps reference pictures or frame contexts of the frames before it (it refreshes slots %08b and resets contexts as reset_frame_context %d says): a session's pictures do not grow, and another session has neither", ErrNoVideoDecoder, h.FrameType, frame.coded[0], frame.coded[1], session.coded[0], session.coded[1], h.RefreshFrameFlags, h.ResetFrameContext)
	}
	return session, nil
}

// shape is the session the frame would start: one of the frame's size.
// An unnamed colour space converts by the size the frame presents at,
// its render size, which a stream that scales its frames keeps.
func (c *vp9Decoder) shape() (decodeShape, error) {
	h := &c.hdr
	col := &h.Color
	chroma, coloured := chromaOfSubsampling(col.SubsamplingX, col.SubsamplingY)
	samples, ok := samplesOf(col.BitDepth, chroma)
	if !coloured || !ok {
		return decodeShape{}, fmt.Errorf("%w: the stream is VP9 profile %d at %d bits, subsampled %v by %v, which pictures have no format for", ErrNoVideoDecoder, h.Profile, col.BitDepth, col.SubsamplingX, col.SubsamplingY)
	}
	unverified := ""
	if chroma != chroma420 {
		unverified = "decoding VP9 beyond 4:2:0 (profiles 1 and 3)"
	}
	signal := videoSignal{typed: true, fullRange: col.FullRange, described: true, lines: h.RenderHeight}
	switch col.ColorSpace {
	case vp9.CSUnknown:
		signal.described = false
	case vp9.CSBT601, vp9.CSSMPTE170:
		signal.matrix = 6
	case vp9.CSBT709:
		signal.matrix = 1
	case vp9.CSBT2020:
		signal.matrix = 9
	default:
		return decodeShape{}, fmt.Errorf("%w: VP9 color_space %d, which the renderer does not convert", ErrNoVideoDecoder, col.ColorSpace)
	}
	ycbcr, err := signal.ycbcr()
	if err != nil {
		return decodeShape{}, err
	}
	return decodeShape{
		profile: int(h.Profile), coded: [2]int{h.FrameWidth, h.FrameHeight},
		slots: refSlotPictures, refs: vp9.RefsPerFrame, hidden: vp9.NumRefFrames, samples: samples, ycbcr: ycbcr, unverified: unverified,
	}, nil
}

func (c *vp9Decoder) profiles() int { return 1 }

func (c *vp9Decoder) profile(_ int, next unsafe.Pointer) unsafe.Pointer {
	c.profileInfo = vk.VideoDecodeVP9ProfileInfoKHR{SType: vk.StructureTypeVideoDecodeVp9ProfileInfoKhr, PNext: (*uintptr)(next), StdProfile: vk.StdVideoVP9Profile(c.hdr.Profile)}
	return unsafe.Pointer(&c.profileInfo)
}

func (c *vp9Decoder) caps() unsafe.Pointer {
	c.capsInfo = vk.VideoDecodeVP9CapabilitiesKHR{SType: vk.StructureTypeVideoDecodeVp9CapabilitiesKhr}
	return unsafe.Pointer(&c.capsInfo)
}

// checkCaps has nothing to check: a VP9 stream codes no level to hold
// against the device's.
func (c *vp9Decoder) checkCaps() error { return nil }

func (c *vp9Decoder) parameters() unsafe.Pointer { return nil }

// contextReset is the reset_frame_context the device is told for the
// frame c.hdr heads. The picture info names one frame context, the one
// the frame decodes from and saves to, which is then the one a device
// resets; an intra-only frame may name another to reset and still
// decode from context 0. Resetting a context nothing was saved to
// resets nothing, so the device is told so; one that holds saved
// probabilities cannot be reset through the picture info, and the frame
// is refused.
func (c *vp9Decoder) contextReset() (uint8, error) {
	h := &c.hdr
	if !h.IntraOnly || h.ResetFrameContext != vp9.ResetOneContext || h.ResetContextIdx == h.FrameContextIdx {
		return h.ResetFrameContext, nil
	}
	if h.SavedContexts>>h.ResetContextIdx&1 != 0 {
		return 0, fmt.Errorf("%w: a VP9 intra-only frame resets frame context %d, which holds saved probabilities, and decodes from context %d; a Vulkan Video picture names one frame context, which is both the one a device resets and the one it decodes from", ErrNoVideoDecoder, h.ResetContextIdx, h.FrameContextIdx)
	}
	return 0, nil
}

// fill lays out the frame's picture info, the device told reset for
// its reset_frame_context.
func (c *vp9Decoder) fill(reset uint8) {
	h := &c.hdr
	col := &h.Color
	c.colorStd = vk.StdVideoVP9ColorConfig{
		Flags:    bitfield(flagBit{col.FullRange, vk.StdVideoVP9ColorRange}),
		BitDepth: intconv.Must[uint8](col.BitDepth), SubsamplingX: b2u8(col.SubsamplingX), SubsamplingY: b2u8(col.SubsamplingY),
		ColorSpace: int32(col.ColorSpace),
	}
	l := &h.LoopFilter
	c.lfStd = vk.StdVideoVP9LoopFilter{
		Flags:           bitfield(flagBit{l.DeltaEnabled, vk.StdVideoVP9LoopFilterDeltaEnabled}, flagBit{l.DeltaUpdate, vk.StdVideoVP9LoopFilterDeltaUpdate}),
		LoopFilterLevel: l.Level, LoopFilterSharpness: l.Sharpness,
		UpdateRefDelta: l.UpdateRefDelta, LoopFilterRefDeltas: l.RefDeltas, UpdateModeDelta: l.UpdateModeDelta, LoopFilterModeDeltas: l.ModeDeltas,
	}
	g := &h.Seg
	c.segStd = vk.StdVideoVP9Segmentation{
		Flags: bitfield(
			flagBit{g.UpdateMap, vk.StdVideoVP9SegmentationUpdateMap},
			flagBit{g.TemporalUpdate, vk.StdVideoVP9SegmentationTemporalUpdate},
			flagBit{g.UpdateData, vk.StdVideoVP9SegmentationUpdateData},
			flagBit{g.AbsOrDeltaUpdate, vk.StdVideoVP9SegmentationAbsOrDeltaUpdate},
		),
		SegmentationTreeProbs: g.TreeProbs, SegmentationPredProb: g.PredProbs, FeatureData: g.FeatureData,
	}
	for i, features := range g.FeatureEnabled {
		var mask uint8
		for j, on := range features {
			if on {
				mask |= 1 << j
			}
		}
		c.segStd.FeatureEnabled[i] = mask
	}
	var signBias uint8
	for i, on := range h.RefFrameSignBias {
		if on {
			signBias |= 1 << i
		}
	}
	q := &h.Quant
	c.pic = vk.StdVideoDecodeVP9PictureInfo{
		Flags: bitfield(
			flagBit{h.ErrorResilientMode, vk.StdVideoDecodeVP9PictureErrorResilientMode},
			flagBit{h.IntraOnly, vk.StdVideoDecodeVP9PictureIntraOnly},
			flagBit{h.AllowHighPrecisionMV, vk.StdVideoDecodeVP9PictureAllowHighPrecisionMv},
			flagBit{h.RefreshFrameContext, vk.StdVideoDecodeVP9PictureRefreshFrameContext},
			flagBit{h.FrameParallelDecodingMode, vk.StdVideoDecodeVP9PictureFrameParallelDecoding},
			flagBit{g.Enabled, vk.StdVideoDecodeVP9PictureSegmentationEnabled},
			flagBit{h.ShowFrame, vk.StdVideoDecodeVP9PictureShowFrame},
			flagBit{h.UsePrevFrameMVs, vk.StdVideoDecodeVP9PictureUsePrevFrameMvs},
		),
		Profile: vk.StdVideoVP9Profile(h.Profile), FrameType: int32(h.FrameType),
		FrameContextIdx: h.FrameContextIdx, ResetFrameContext: reset, RefreshFrameFlags: h.RefreshFrameFlags,
		RefFrameSignBiasMask: signBias, InterpolationFilter: int32(h.InterpolationFilter),
		BaseQIdx: q.BaseQIdx, DeltaQYDc: q.DeltaQYDc, DeltaQUVDc: q.DeltaQUVDc, DeltaQUVAc: q.DeltaQUVAc,
		TileColsLog2: h.TileColsLog2, TileRowsLog2: h.TileRowsLog2,
		PColorConfig: &c.colorStd, PLoopFilter: &c.lfStd, PSegmentation: &c.segStd,
	}
	c.picInfo = vk.VideoDecodeVP9PictureInfoKHR{
		SType: vk.StructureTypeVideoDecodeVp9PictureInfoKhr, PStdPictureInfo: &c.pic,
		ReferenceNameSlotIndices: [vp9.RefsPerFrame]int32{-1, -1, -1},
		CompressedHeaderOffset:   intconv.Must[uint32](h.HeaderBytes), TilesOffset: intconv.Must[uint32](h.HeaderBytes + h.CompressedBytes),
	}
}
