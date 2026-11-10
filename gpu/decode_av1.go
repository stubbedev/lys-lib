package gpu

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"slices"
	"unsafe"

	"github.com/stubbedev/lys-lib/intconv"
	"github.com/stubbedev/lys-lib/internal/av1"
	"github.com/stubbedev/lys-lib/media/demux"
	"github.com/stubbedev/lys-lib/third_party/vk"
)

// av1Decoder is AV1's part of a decoder, as h264Decoder is H.264's.
type av1Decoder struct {
	stream        av1.Stream
	seq           []byte
	paramsChanged bool
	started       bool
	hdr           av1.FrameHeader
	// frameAt is where the frame being read starts in its packet, -1
	// when no frame is being read; skipping is set while the tile groups
	// of a frame before the one decoding can start at are passed over.
	frameAt  int
	skipping bool
	tiles    []av1.Tile
	offsets  []uint32
	sizes    []uint32
	refSlots
	// refs are what decoding a picture that references a DPB slot reads
	// of its picture.
	refs [refSlotPictures]vk.StdVideoDecodeAV1ReferenceInfo

	// filmGrain is whether the stream codes film grain, which the
	// profile then asks the device for; deviceGrain whether the device
	// applies the frame's, into an output picture apart from its
	// reference picture. A device that decodes into its reference
	// pictures alone leaves the grain to the decoder's grain pass.
	filmGrain   bool
	deviceGrain bool
	profileInfo vk.VideoDecodeAV1ProfileInfoKHR
	capsInfo    vk.VideoDecodeAV1CapabilitiesKHR
	seqStd      vk.StdVideoAV1SequenceHeader
	colorStd    vk.StdVideoAV1ColorConfig
	timingStd   vk.StdVideoAV1TimingInfo
	params      vk.VideoDecodeAV1SessionParametersCreateInfoKHR

	pic       vk.StdVideoDecodeAV1PictureInfo
	picInfo   vk.VideoDecodeAV1PictureInfoKHR
	tileStd   vk.StdVideoAV1TileInfo
	quantStd  vk.StdVideoAV1Quantization
	segStd    vk.StdVideoAV1Segmentation
	lfStd     vk.StdVideoAV1LoopFilter
	cdefStd   vk.StdVideoAV1CDEF
	lrStd     vk.StdVideoAV1LoopRestoration
	gmStd     vk.StdVideoAV1GlobalMotion
	grainStd  vk.StdVideoAV1FilmGrain
	setupInfo vk.VideoDecodeAV1DpbSlotInfoKHR
	refInfo   [av1.RefsPerFrame]vk.VideoDecodeAV1DpbSlotInfoKHR
	active    [av1.RefsPerFrame]int
	refPtrs   [av1.RefsPerFrame]unsafe.Pointer
	req       decodePicture
}

// newAV1Decoder reads config, the track's av1C, for the sequence header
// its configOBUs carry; a stream may instead send it in band.
func newAV1Decoder(config []byte) (decodeCodec, error) {
	c := &av1Decoder{frameAt: -1}
	c.forget()
	if len(config) == 0 {
		return c, nil
	}
	obus, err := av1.ConfigOBUs(config)
	if err != nil {
		return nil, fmt.Errorf("av1C: %w", err)
	}
	walk := av1.NewOBUs(obus)
	for u, ok := walk.Next(); ok; u, ok = walk.Next() {
		if u.Type != av1.OBUSequenceHeader {
			continue
		}
		if err := c.sequence(u.Payload); err != nil {
			return nil, fmt.Errorf("av1C: %w", err)
		}
	}
	if err := walk.Err(); err != nil {
		return nil, fmt.Errorf("av1C: %w", err)
	}
	return c, nil
}

// sequence takes b, a sequence header OBU's payload, when it differs
// from the stream's.
func (c *av1Decoder) sequence(b []byte) error {
	if c.seq != nil && bytes.Equal(c.seq, b) {
		return nil
	}
	if err := c.stream.Seq.Parse(b); err != nil {
		c.seq = nil
		return err
	}
	c.seq = append(c.seq[:0], b...)
	c.paramsChanged = true
	return nil
}

func (c *av1Decoder) reset() {
	c.stream.Reset()
	c.forget()
	c.started, c.frameAt, c.skipping = false, -1, false
}

// decode reads the packet's OBUs, a temporal unit, and decodes the
// frames it codes; the one it shows goes to Next.
func (c *av1Decoder) decode(v *VideoDecoder, p demux.Packet) error {
	if !c.started && !p.Key {
		return nil
	}
	walk := av1.NewOBUs(p.Data)
	for u, ok := walk.Next(); ok; u, ok = walk.Next() {
		if c.seq != nil && !u.InOperatingPoint(c.stream.Seq.OperatingPointIdc[0]) {
			continue
		}
		var err error
		switch u.Type {
		case av1.OBUSequenceHeader:
			err = c.sequence(u.Payload)
		case av1.OBUFrameHeader, av1.OBUFrame:
			if c.frameAt >= 0 && u.Type == av1.OBUFrameHeader {
				continue
			}
			c.skipping = false
			err = c.frameHeader(v, p, &u)
		case av1.OBUTileGroup:
			if c.skipping {
				continue
			}
			if c.frameAt < 0 {
				return fmt.Errorf("%w: a tile group OBU at byte %d before its frame header", av1.ErrSyntax, u.Offset)
			}
			err = c.tileGroup(v, p, &u, u.Payload, u.PayloadOffset)
		}
		if err != nil {
			return err
		}
	}
	if err := walk.Err(); err != nil {
		return err
	}
	c.skipping = false
	if c.frameAt >= 0 {
		return fmt.Errorf("%w: the temporal unit ends inside a frame, after %d tiles", av1.ErrSyntax, len(c.tiles))
	}
	return nil
}

// frameHeader reads a frame header or frame OBU: it shows an existing
// frame, or starts reading a frame's tiles.
func (c *av1Decoder) frameHeader(v *VideoDecoder, p demux.Packet, u *av1.OBU) error {
	if c.seq == nil {
		return fmt.Errorf("%w: a frame header before any sequence header", av1.ErrSyntax)
	}
	if !c.started {
		if showExisting, typ := c.stream.Peek(u.Payload); showExisting || typ != av1.KeyFrame {
			c.skipping = u.Type == av1.OBUFrameHeader
			return nil
		}
	}
	h := &c.hdr
	if err := c.stream.ParseFrameHeader(h, u); err != nil {
		return err
	}
	if h.ShowExistingFrame {
		return c.showExisting(v, p)
	}
	c.frameAt, c.tiles = u.Offset, c.tiles[:0]
	if u.Type != av1.OBUFrame {
		return nil
	}
	return c.tileGroup(v, p, u, u.Payload[h.HeaderBytes:], u.PayloadOffset+h.HeaderBytes)
}

// tileGroup reads a tile group, b of the packet from at on, and decodes
// the frame once it has all its tiles.
func (c *av1Decoder) tileGroup(v *VideoDecoder, p demux.Packet, u *av1.OBU, b []byte, at int) error {
	from := len(c.tiles)
	tiles, last, err := av1.ParseTileGroup(&c.hdr, b, c.tiles)
	c.tiles = tiles
	if err != nil {
		return err
	}
	for i := from; i < len(c.tiles); i++ {
		c.tiles[i].Offset += at - c.frameAt
	}
	if !last {
		return nil
	}
	start := c.frameAt
	c.frameAt = -1
	return c.picture(v, p, p.Data[start:u.PayloadOffset+len(u.Payload)])
}

// picture decodes the frame c.hdr heads, data from its header on with
// its tiles at c.tiles.
func (c *av1Decoder) picture(v *VideoDecoder, p demux.Packet, data []byte) error {
	h := &c.hdr
	if h.UseSuperres {
		return fmt.Errorf("%w: the %s at order hint %d is coded at a superres-scaled width, which the decoder does not upscale", ErrNoVideoDecoder, h.FrameType, h.OrderHint)
	}
	shape, err := c.shape()
	if err != nil {
		return err
	}
	if v.session != 0 && shape != v.shape && h.FrameType != av1.KeyFrame {
		return fmt.Errorf("%w: the %s at order hint %d is %dx%d, its references %dx%d, which needs reference scaling the decoder does not do", ErrNoVideoDecoder, h.FrameType, h.OrderHint, shape.coded[0], shape.coded[1], v.shape.coded[0], v.shape.coded[1])
	}
	if v.session == 0 || shape != v.shape {
		c.forget()
	}
	if err := v.shapeFor(shape); err != nil {
		if c.stream.Seq.FilmGrainPresent && errors.Is(err, ErrNoVideoDecoder) {
			return fmt.Errorf("%w (the stream codes film grain, which the device would apply)", err)
		}
		return err
	}
	c.deviceGrain = h.FilmGrain.ApplyGrain && !v.copies
	if c.paramsChanged {
		if err := v.createParameters(); err != nil {
			return err
		}
	}
	setup := c.free()
	frame, err := v.frame()
	if err != nil {
		return err
	}
	buf, err := v.stream(len(data))
	if err != nil {
		return err
	}
	copy(buf, data)
	c.fill(setup)
	c.req = decodePicture{frame: frame, bytes: len(data), info: unsafe.Pointer(&c.picInfo), setup: setup, setupInfo: unsafe.Pointer(&c.setupInfo)}
	n := 0
	if !h.FrameIsIntra {
		for i, idx := range h.RefFrameIdx {
			slot := c.slotOf[idx]
			if slot < 0 {
				return fmt.Errorf("gpu: AV1 %s at order hint %d references slot %d, which holds no decoded picture", h.FrameType, h.OrderHint, idx)
			}
			c.picInfo.ReferenceNameSlotIndices[i] = intconv.Must[int32](slot)
			if slices.Contains(c.active[:n], slot) {
				continue
			}
			c.refInfo[n] = vk.VideoDecodeAV1DpbSlotInfoKHR{SType: vk.StructureTypeVideoDecodeAv1DpbSlotInfoKhr, PStdReferenceInfo: &c.refs[slot]}
			c.active[n], c.refPtrs[n] = slot, unsafe.Pointer(&c.refInfo[n])
			n++
		}
	}
	c.req.refs, c.req.refInfo = c.active[:n], c.refPtrs[:n]
	if err := v.submit(&c.req); err != nil {
		return err
	}
	if h.FilmGrain.ApplyGrain && v.copies && (h.ShowFrame || h.ShowableFrame) {
		col := &c.stream.Seq.Color
		grain := grainFrame{params: &h.FilmGrain, w: h.FrameWidth, h: h.FrameHeight, bits: col.BitDepth, identity: col.MatrixCoefficients == 0}
		if err := v.applyGrain(frame, grain); err != nil {
			return err
		}
	}
	c.started = true
	pic := &c.pictures[setup]
	pic.rect = c.rect()
	frame.rect = pic.rect
	c.refresh(setup, h.RefreshFrameFlags)
	c.stream.Update(h)
	if h.ShowFrame {
		frame.pts, frame.duration = p.PTS, p.Duration
		v.queue(frame, 0, false, false)
	} else {
		c.keep(setup, frame, h.ShowableFrame)
	}
	return nil
}

// showExisting shows the frame c.hdr names, decoded hidden before; a
// key frame shown so also takes every reference frame slot.
func (c *av1Decoder) showExisting(v *VideoDecoder, p demux.Packet) error {
	h := &c.hdr
	f, slot, err := c.takeHidden(h.FrameToShowMapIdx)
	if err != nil {
		return err
	}
	f.pts, f.duration = p.PTS, p.Duration
	v.queue(f, 0, false, false)
	c.refresh(slot, h.RefreshFrameFlags)
	c.stream.Update(h)
	return nil
}

// rect is the frame's picture: its render size where that crops the
// frame, else the frame.
func (c *av1Decoder) rect() image.Rectangle {
	h := &c.hdr
	return image.Rect(0, 0, min(h.RenderWidth, h.UpscaledWidth), min(h.RenderHeight, h.FrameHeight))
}

// shape is the session the frame decodes in; it notes whether the
// profile asks the device for film grain.
func (c *av1Decoder) shape() (decodeShape, error) {
	q := &c.stream.Seq
	col := &q.Color
	depth, ok := depthOf(col.BitDepth)
	if q.Profile != 0 || !ok || col.MonoChrome || !col.SubsamplingX || !col.SubsamplingY {
		return decodeShape{}, fmt.Errorf("%w: the sequence is profile %d at %d bits, monochrome %v, subsampled %v by %v; only 4:2:0 Main at 8 or 10 bits is decoded", ErrNoVideoDecoder, q.Profile, col.BitDepth, col.MonoChrome, col.SubsamplingX, col.SubsamplingY)
	}
	signal := videoSignal{typed: true, fullRange: col.ColorRange, described: col.ColorDescriptionPresent, matrix: col.MatrixCoefficients, lines: c.hdr.FrameHeight}
	switch col.ChromaSamplePosition {
	case av1.ChromaSamplePositionUnknown:
	case av1.ChromaSamplePositionVertical:
		signal.sited, signal.siting = true, 0
	case av1.ChromaSamplePositionColocated:
		signal.sited, signal.siting = true, 2
	default:
		return decodeShape{}, fmt.Errorf("%w: chroma_sample_position %d is reserved", av1.ErrSyntax, col.ChromaSamplePosition)
	}
	ycbcr, err := signal.ycbcr()
	if err != nil {
		return decodeShape{}, fmt.Errorf("%w: %w", av1.ErrSyntax, err)
	}
	c.filmGrain = q.FilmGrainPresent
	return decodeShape{
		profile: int(q.Profile), coded: [2]int{c.hdr.FrameWidth, c.hdr.FrameHeight},
		slots: refSlotPictures, refs: av1.RefsPerFrame, hidden: av1.NumRefFrames, depth: depth, ycbcr: ycbcr,
	}, nil
}

func (c *av1Decoder) profiles() int { return 1 }

func (c *av1Decoder) profile(_ int, next unsafe.Pointer) unsafe.Pointer {
	c.profileInfo = vk.VideoDecodeAV1ProfileInfoKHR{SType: vk.StructureTypeVideoDecodeAv1ProfileInfoKhr, PNext: (*uintptr)(next), StdProfile: vk.StdVideoAV1ProfileMain}
	if c.filmGrain {
		c.profileInfo.FilmGrainSupport = vk.True
	}
	return unsafe.Pointer(&c.profileInfo)
}

func (c *av1Decoder) caps() unsafe.Pointer {
	c.capsInfo = vk.VideoDecodeAV1CapabilitiesKHR{SType: vk.StructureTypeVideoDecodeAv1CapabilitiesKhr}
	return unsafe.Pointer(&c.capsInfo)
}

func (c *av1Decoder) checkCaps() error {
	if level := vk.StdVideoAV1Level(c.stream.Seq.LevelIdx[0]); level > c.capsInfo.MaxLevel {
		return fmt.Errorf("AV1 seq_level_idx %d is past the device's %d", level, c.capsInfo.MaxLevel)
	}
	return nil
}

// parameters lays out the sequence header as the session parameters'
// create chain.
func (c *av1Decoder) parameters() unsafe.Pointer {
	q := &c.stream.Seq
	col := &q.Color
	c.colorStd = vk.StdVideoAV1ColorConfig{
		Flags: bitfield(
			flagBit{col.MonoChrome, vk.StdVideoAV1ColorMonoChrome},
			flagBit{col.ColorRange, vk.StdVideoAV1ColorRange},
			flagBit{col.SeparateUVDeltaQ, vk.StdVideoAV1ColorSeparateUVDeltaQ},
			flagBit{col.ColorDescriptionPresent, vk.StdVideoAV1ColorDescriptionPresent},
		),
		BitDepth: intconv.Must[uint8](col.BitDepth), SubsamplingX: b2u8(col.SubsamplingX), SubsamplingY: b2u8(col.SubsamplingY),
		ColorPrimaries: int32(col.ColorPrimaries), TransferCharacteristics: int32(col.TransferCharacteristics),
		MatrixCoefficients: int32(col.MatrixCoefficients), ChromaSamplePosition: int32(col.ChromaSamplePosition),
	}
	t := &q.Timing
	c.timingStd = vk.StdVideoAV1TimingInfo{
		Flags:                 bitfield(flagBit{t.EqualPictureInterval, vk.StdVideoAV1TimingEqualPictureInterval}),
		NumUnitsInDisplayTick: t.NumUnitsInDisplayTick, TimeScale: t.TimeScale, NumTicksPerPictureMinus1: t.NumTicksPerPictureMinus1,
	}
	c.seqStd = vk.StdVideoAV1SequenceHeader{
		Flags: bitfield(
			flagBit{q.StillPicture, vk.StdVideoAV1SeqStillPicture},
			flagBit{q.ReducedStillPictureHeader, vk.StdVideoAV1SeqReducedStillPictureHeader},
			flagBit{q.Use128x128Superblock, vk.StdVideoAV1SeqUse128x128Superblock},
			flagBit{q.EnableFilterIntra, vk.StdVideoAV1SeqEnableFilterIntra},
			flagBit{q.EnableIntraEdgeFilter, vk.StdVideoAV1SeqEnableIntraEdgeFilter},
			flagBit{q.EnableInterintraCompound, vk.StdVideoAV1SeqEnableInterintraCompound},
			flagBit{q.EnableMaskedCompound, vk.StdVideoAV1SeqEnableMaskedCompound},
			flagBit{q.EnableWarpedMotion, vk.StdVideoAV1SeqEnableWarpedMotion},
			flagBit{q.EnableDualFilter, vk.StdVideoAV1SeqEnableDualFilter},
			flagBit{q.EnableOrderHint, vk.StdVideoAV1SeqEnableOrderHint},
			flagBit{q.EnableJntComp, vk.StdVideoAV1SeqEnableJntComp},
			flagBit{q.EnableRefFrameMVs, vk.StdVideoAV1SeqEnableRefFrameMvs},
			flagBit{q.FrameIDNumbersPresent, vk.StdVideoAV1SeqFrameIDNumbersPresent},
			flagBit{q.EnableSuperres, vk.StdVideoAV1SeqEnableSuperres},
			flagBit{q.EnableCDEF, vk.StdVideoAV1SeqEnableCdef},
			flagBit{q.EnableRestoration, vk.StdVideoAV1SeqEnableRestoration},
			flagBit{q.FilmGrainPresent, vk.StdVideoAV1SeqFilmGrainParamsPresent},
			flagBit{q.TimingInfoPresent, vk.StdVideoAV1SeqTimingInfoPresent},
			flagBit{q.InitialDisplayDelayPresent, vk.StdVideoAV1SeqInitialDisplayDelayPresent},
		),
		SeqProfile:           vk.StdVideoAV1Profile(q.Profile),
		FrameWidthBitsMinus1: intconv.Must[uint8](q.FrameWidthBits - 1), FrameHeightBitsMinus1: intconv.Must[uint8](q.FrameHeightBits - 1),
		MaxFrameWidthMinus1: intconv.Must[uint16](q.MaxFrameWidth - 1), MaxFrameHeightMinus1: intconv.Must[uint16](q.MaxFrameHeight - 1),
		DeltaFrameIDLengthMinus2: q.DeltaFrameIDLengthMinus2, AdditionalFrameIDLengthMinus1: q.AdditionalFrameIDLengthMinus1,
		OrderHintBitsMinus1: intconv.Must[uint8](max(q.OrderHintBits-1, 0)),
		SeqForceIntegerMv:   q.ForceIntegerMV, SeqForceScreenContentTools: q.ForceScreenContentTools,
		PColorConfig: &c.colorStd,
	}
	if q.TimingInfoPresent {
		c.seqStd.PTimingInfo = &c.timingStd
	}
	c.params = vk.VideoDecodeAV1SessionParametersCreateInfoKHR{SType: vk.StructureTypeVideoDecodeAv1SessionParametersCreateInfoKhr, PStdSequenceHeader: &c.seqStd}
	c.paramsChanged = false
	return unsafe.Pointer(&c.params)
}

func b2u8(b bool) uint8 {
	if b {
		return 1
	}
	return 0
}

// fill lays out the frame's picture info and the reference info of the
// DPB slot it sets up.
func (c *av1Decoder) fill(setup int) {
	h := &c.hdr
	t := &h.Tile
	c.tileStd = vk.StdVideoAV1TileInfo{
		Flags:    bitfield(flagBit{t.UniformSpacing, vk.StdVideoAV1TileInfoUniformTileSpacing}),
		TileCols: intconv.Must[uint8](t.Cols), TileRows: intconv.Must[uint8](t.Rows), ContextUpdateTileID: t.ContextUpdateTileID,
		TileSizeBytesMinus1: intconv.Must[uint8](t.TileSizeBytes - 1),
		PMiColStarts:        &t.MiColStarts[0], PMiRowStarts: &t.MiRowStarts[0], PWidthInSbsMinus1: &t.WidthInSbsMinus1[0], PHeightInSbsMinus1: &t.HeightInSbsMinus1[0],
	}
	q := &h.Quant
	c.quantStd = vk.StdVideoAV1Quantization{
		Flags:    bitfield(flagBit{q.UsingQmatrix, vk.StdVideoAV1QuantizationUsingQmatrix}, flagBit{q.DiffUVDelta, vk.StdVideoAV1QuantizationDiffUVDelta}),
		BaseQIdx: q.BaseQIdx, DeltaQYDc: q.DeltaQYDc, DeltaQUDc: q.DeltaQUDc, DeltaQUAc: q.DeltaQUAc, DeltaQVDc: q.DeltaQVDc, DeltaQVAc: q.DeltaQVAc,
		QmY: q.QmY, QmU: q.QmU, QmV: q.QmV,
	}
	c.segStd = vk.StdVideoAV1Segmentation{FeatureData: h.Seg.FeatureData}
	for i, features := range h.Seg.FeatureEnabled {
		var mask uint8
		for j, on := range features {
			if on {
				mask |= 1 << j
			}
		}
		c.segStd.FeatureEnabled[i] = mask
	}
	l := &h.LoopFilter
	c.lfStd = vk.StdVideoAV1LoopFilter{
		Flags:           bitfield(flagBit{l.DeltaEnabled, vk.StdVideoAV1LoopFilterDeltaEnabled}, flagBit{l.DeltaUpdate, vk.StdVideoAV1LoopFilterDeltaUpdate}),
		LoopFilterLevel: l.Level, LoopFilterSharpness: l.Sharpness, UpdateRefDelta: l.UpdateRefDelta, LoopFilterRefDeltas: l.RefDeltas,
		UpdateModeDelta: l.UpdateModeDelta, LoopFilterModeDeltas: l.ModeDeltas,
	}
	d := &h.CDEF
	c.cdefStd = vk.StdVideoAV1CDEF{
		CdefDampingMinus3: d.DampingMinus3, CdefBits: d.Bits,
		CdefYPriStrength: d.YPri, CdefYSecStrength: d.YSec, CdefUVPriStrength: d.UVPri, CdefUVSecStrength: d.UVSec,
	}
	c.lrStd = vk.StdVideoAV1LoopRestoration{LoopRestorationSize: h.LR.Size}
	for i, typ := range h.LR.Type {
		c.lrStd.FrameRestorationType[i] = int32(typ)
	}
	c.gmStd = vk.StdVideoAV1GlobalMotion{GmType: h.GlobalMotion.Type, GmParams: h.GlobalMotion.Params}
	c.fillGrain()
	c.pic = vk.StdVideoDecodeAV1PictureInfo{
		Flags: bitfield(
			flagBit{h.ErrorResilientMode, vk.StdVideoDecodeAV1PictureErrorResilientMode},
			flagBit{h.DisableCDFUpdate, vk.StdVideoDecodeAV1PictureDisableCdfUpdate},
			flagBit{h.UseSuperres, vk.StdVideoDecodeAV1PictureUseSuperres},
			flagBit{h.RenderAndFrameSizeDifferent, vk.StdVideoDecodeAV1PictureRenderAndFrameSizeDifferent},
			flagBit{h.AllowScreenContentTools, vk.StdVideoDecodeAV1PictureAllowScreenContentTools},
			flagBit{h.IsFilterSwitchable, vk.StdVideoDecodeAV1PictureIsFilterSwitchable},
			flagBit{h.ForceIntegerMV, vk.StdVideoDecodeAV1PictureForceIntegerMv},
			flagBit{h.FrameSizeOverride, vk.StdVideoDecodeAV1PictureFrameSizeOverride},
			flagBit{h.BufferRemovalTimePresent, vk.StdVideoDecodeAV1PictureBufferRemovalTimePresent},
			flagBit{h.AllowIntrabc, vk.StdVideoDecodeAV1PictureAllowIntrabc},
			flagBit{h.FrameRefsShortSignaling, vk.StdVideoDecodeAV1PictureFrameRefsShortSignaling},
			flagBit{h.AllowHighPrecisionMV, vk.StdVideoDecodeAV1PictureAllowHighPrecisionMv},
			flagBit{h.IsMotionModeSwitchable, vk.StdVideoDecodeAV1PictureIsMotionModeSwitchable},
			flagBit{h.UseRefFrameMVs, vk.StdVideoDecodeAV1PictureUseRefFrameMvs},
			flagBit{h.DisableFrameEndUpdateCDF, vk.StdVideoDecodeAV1PictureDisableFrameEndUpdateCdf},
			flagBit{h.AllowWarpedMotion, vk.StdVideoDecodeAV1PictureAllowWarpedMotion},
			flagBit{h.ReducedTxSet, vk.StdVideoDecodeAV1PictureReducedTxSet},
			flagBit{h.ReferenceSelect, vk.StdVideoDecodeAV1PictureReferenceSelect},
			flagBit{h.SkipModePresent, vk.StdVideoDecodeAV1PictureSkipModePresent},
			flagBit{h.DeltaQPresent, vk.StdVideoDecodeAV1PictureDeltaQPresent},
			flagBit{h.DeltaLFPresent, vk.StdVideoDecodeAV1PictureDeltaLfPresent},
			flagBit{h.DeltaLFMulti, vk.StdVideoDecodeAV1PictureDeltaLfMulti},
			flagBit{h.Seg.Enabled, vk.StdVideoDecodeAV1PictureSegmentationEnabled},
			flagBit{h.Seg.UpdateMap, vk.StdVideoDecodeAV1PictureSegmentationUpdateMap},
			flagBit{h.Seg.TemporalUpdate, vk.StdVideoDecodeAV1PictureSegmentationTemporalUpdate},
			flagBit{h.Seg.UpdateData, vk.StdVideoDecodeAV1PictureSegmentationUpdateData},
			flagBit{h.LR.UsesLr, vk.StdVideoDecodeAV1PictureUsesLr},
			flagBit{h.LR.UsesChromaLr, vk.StdVideoDecodeAV1PictureUsesChromaLr},
			flagBit{c.deviceGrain, vk.StdVideoDecodeAV1PictureApplyGrain},
		),
		FrameType: int32(h.FrameType), CurrentFrameID: h.CurrentFrameID, OrderHint: h.OrderHint,
		PrimaryRefFrame: h.PrimaryRefFrame, RefreshFrameFlags: h.RefreshFrameFlags,
		InterpolationFilter: int32(h.InterpolationFilter), TxMode: int32(h.TxMode),
		DeltaQRes: h.DeltaQRes, DeltaLfRes: h.DeltaLFRes, SkipModeFrame: h.SkipModeFrame, CodedDenom: h.CodedDenom,
		OrderHints: h.OrderHints,
		PTileInfo:  &c.tileStd, PQuantization: &c.quantStd, PSegmentation: &c.segStd, PLoopFilter: &c.lfStd,
		PCDEF: &c.cdefStd, PLoopRestoration: &c.lrStd, PGlobalMotion: &c.gmStd,
	}
	copy(c.pic.ExpectedFrameID[:], h.ExpectedFrameID[:])
	if c.deviceGrain {
		c.pic.PFilmGrain = &c.grainStd
	}
	c.offsets, c.sizes = c.offsets[:0], c.sizes[:0]
	for _, tile := range c.tiles {
		c.offsets = append(c.offsets, intconv.Must[uint32](tile.Offset))
		c.sizes = append(c.sizes, intconv.Must[uint32](tile.Size))
	}
	c.picInfo = vk.VideoDecodeAV1PictureInfoKHR{
		SType: vk.StructureTypeVideoDecodeAv1PictureInfoKhr, PStdPictureInfo: &c.pic,
		TileCount: intconv.Must[uint32](len(c.tiles)), PTileOffsets: &c.offsets[0], PTileSizes: &c.sizes[0],
	}
	for i := range c.picInfo.ReferenceNameSlotIndices {
		c.picInfo.ReferenceNameSlotIndices[i] = -1
	}
	var signBias uint8
	for i, on := range h.RefFrameSignBias {
		if on {
			signBias |= 1 << i
		}
	}
	c.refs[setup] = vk.StdVideoDecodeAV1ReferenceInfo{
		Flags: bitfield(
			flagBit{h.DisableFrameEndUpdateCDF, vk.StdVideoDecodeAV1ReferenceDisableFrameEndUpdateCdf},
			flagBit{h.Seg.Enabled, vk.StdVideoDecodeAV1ReferenceSegmentationEnabled},
		),
		FrameType: uint8(h.FrameType), RefFrameSignBias: signBias, OrderHint: h.OrderHint, SavedOrderHints: h.OrderHints,
	}
	c.setupInfo = vk.VideoDecodeAV1DpbSlotInfoKHR{SType: vk.StructureTypeVideoDecodeAv1DpbSlotInfoKhr, PStdReferenceInfo: &c.refs[setup]}
}

// fillGrain lays out the frame's film grain parameters.
func (c *av1Decoder) fillGrain() {
	g := &c.hdr.FilmGrain
	c.grainStd = vk.StdVideoAV1FilmGrain{
		Flags: bitfield(
			flagBit{g.ChromaScalingFromLuma, vk.StdVideoAV1FilmGrainChromaScalingFromLuma},
			flagBit{g.OverlapFlag, vk.StdVideoAV1FilmGrainOverlap},
			flagBit{g.ClipToRestrictedRange, vk.StdVideoAV1FilmGrainClipToRestrictedRange},
			flagBit{g.UpdateGrain, vk.StdVideoAV1FilmGrainUpdateGrain},
		),
		GrainScalingMinus8: g.ScalingMinus8, ArCoeffLag: g.ArCoeffLag, ArCoeffShiftMinus6: g.ArCoeffShiftMinus6, GrainScaleShift: g.GrainScaleShift,
		GrainSeed: g.GrainSeed, FilmGrainParamsRefIdx: g.ParamsRefIdx,
		NumYPoints: g.NumYPoints, PointYValue: g.PointYValue, PointYScaling: g.PointYScaling,
		NumCbPoints: g.NumCbPoints, PointCbValue: g.PointCbValue, PointCbScaling: g.PointCbScaling,
		NumCrPoints: g.NumCrPoints, PointCrValue: g.PointCrValue, PointCrScaling: g.PointCrScaling,
		CbMult: g.CbMult, CbLumaMult: g.CbLumaMult, CbOffset: g.CbOffset, CrMult: g.CrMult, CrLumaMult: g.CrLumaMult, CrOffset: g.CrOffset,
	}
	for i, v := range g.ArCoeffsYPlus128 {
		c.grainStd.ArCoeffsYPlus128[i] = rawInt8(v)
	}
	for i, v := range g.ArCoeffsCbPlus128 {
		c.grainStd.ArCoeffsCbPlus128[i] = rawInt8(v)
	}
	for i, v := range g.ArCoeffsCrPlus128 {
		c.grainStd.ArCoeffsCrPlus128[i] = rawInt8(v)
	}
}

// rawInt8 is the byte v as the int8 with its bits: the film grain
// coefficients, coded plus 128, go to the device as the bytes coded.
func rawInt8(v uint8) int8 { return intconv.Must[int8](int(v) - int(v>>7)<<8) }
