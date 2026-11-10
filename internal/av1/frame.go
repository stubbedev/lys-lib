package av1

import (
	"fmt"
	"slices"

	"github.com/stubbedev/lys-lib/intconv"
)

// FrameType is a frame's frame_type.
type FrameType uint8

// The frame types.
const (
	KeyFrame FrameType = iota
	InterFrame
	IntraOnlyFrame
	SwitchFrame
)

func (t FrameType) String() string {
	switch t {
	case KeyFrame:
		return "key frame"
	case InterFrame:
		return "inter frame"
	case IntraOnlyFrame:
		return "intra-only frame"
	}
	return "switch frame"
}

// The constants of the syntax (3).
const (
	NumRefFrames      = 8
	RefsPerFrame      = 7
	TotalRefsPerFrame = 8
	PrimaryRefNone    = 7
	MaxSegments       = 8
	SegLvlMax         = 8
	MaxTileCols       = 64
	MaxTileRows       = 64
	// LastFrame is the first reference name of an inter frame, the
	// reference frame ref_frame_idx[0] names.
	LastFrame = 1
	// The reference names of the frames frame_refs_short_signaling
	// codes and the loop filter's defaults name.
	Last2Frame   = 2
	Last3Frame   = 3
	GoldenFrame  = 4
	BwdrefFrame  = 5
	Altref2Frame = 6
	AltrefFrame  = 7
	// SwitchableFilter is interpolation_filter's switchable value.
	SwitchableFilter = 4
	// TxModeOnly4x4, TxModeLargest and TxModeSelect are the TxModes.
	TxModeOnly4x4 = 0
	TxModeLargest = 1
	TxModeSelect  = 2

	superresNum         = 8
	superresDenomMin    = 9
	superresDenomBits   = 3
	maxTileWidth        = 4096
	maxTileArea         = 4096 * 2304
	warpedModelPrecBits = 16
	maxLoopFilter       = 63
)

// The restoration types of FrameRestorationType.
const (
	RestoreNone       = 0
	RestoreWiener     = 1
	RestoreSgrproj    = 2
	RestoreSwitchable = 3
)

// The global motion types of GmType.
const (
	Identity    = 0
	Translation = 1
	RotZoom     = 2
	Affine      = 3
)

// FrameHeader is a frame header (5.9), its values as its semantics
// (6.8) and the decoding process define them.
type FrameHeader struct {
	ShowExistingFrame bool
	FrameToShowMapIdx uint8
	FrameType         FrameType
	FrameIsIntra      bool
	ShowFrame         bool
	ShowableFrame     bool

	ErrorResilientMode       bool
	DisableCDFUpdate         bool
	AllowScreenContentTools  bool
	ForceIntegerMV           bool
	CurrentFrameID           uint32
	FrameSizeOverride        bool
	OrderHint                uint8
	PrimaryRefFrame          uint8
	BufferRemovalTimePresent bool
	RefreshFrameFlags        uint8

	FrameRefsShortSignaling bool
	// RefFrameIdx is ref_frame_idx: the slot of each reference name
	// from LastFrame on.
	RefFrameIdx [RefsPerFrame]uint8
	// ExpectedFrameID is expectedFrameId, by RefFrameIdx's index.
	ExpectedFrameID [RefsPerFrame]uint32

	FrameWidth, FrameHeight     int
	UpscaledWidth               int
	RenderWidth, RenderHeight   int
	RenderAndFrameSizeDifferent bool
	UseSuperres                 bool
	CodedDenom                  uint8
	MiCols, MiRows              int

	AllowIntrabc           bool
	AllowHighPrecisionMV   bool
	IsFilterSwitchable     bool
	InterpolationFilter    uint8
	IsMotionModeSwitchable bool
	UseRefFrameMVs         bool
	// OrderHints and RefFrameSignBias are by reference name.
	OrderHints       [TotalRefsPerFrame]uint8
	RefFrameSignBias [TotalRefsPerFrame]bool

	DisableFrameEndUpdateCDF bool

	Tile  TileInfo
	Quant Quantization
	Seg   Segmentation

	DeltaQPresent  bool
	DeltaQRes      uint8
	DeltaLFPresent bool
	DeltaLFRes     uint8
	DeltaLFMulti   bool
	CodedLossless  bool
	AllLossless    bool

	LoopFilter LoopFilter
	CDEF       CDEF
	LR         LoopRestoration

	TxMode          uint8
	ReferenceSelect bool
	SkipModePresent bool
	// SkipModeFrame are the reference names skip mode predicts from.
	SkipModeFrame     [2]uint8
	AllowWarpedMotion bool
	ReducedTxSet      bool
	GlobalMotion      GlobalMotion
	FilmGrain         FilmGrain

	// HeaderBytes is how many bytes of its OBU's payload the header
	// takes, up to the byte boundary a frame OBU's tile group starts at.
	HeaderBytes int
}

// TileInfo is a tile_info().
type TileInfo struct {
	UniformSpacing      bool
	Cols, Rows          int
	ColsLog2, RowsLog2  int
	MiColStarts         [MaxTileCols + 1]uint16
	MiRowStarts         [MaxTileRows + 1]uint16
	WidthInSbsMinus1    [MaxTileCols]uint16
	HeightInSbsMinus1   [MaxTileRows]uint16
	ContextUpdateTileID uint16
	// TileSizeBytes is TileSizeBytes, 4 where the header codes none.
	TileSizeBytes int
}

// Quantization is a quantization_params().
type Quantization struct {
	BaseQIdx                                              uint8
	DeltaQYDc, DeltaQUDc, DeltaQUAc, DeltaQVDc, DeltaQVAc int8
	DiffUVDelta                                           bool
	UsingQmatrix                                          bool
	QmY, QmU, QmV                                         uint8
}

// Segmentation is a segmentation_params(), the features it leaves out
// loaded from the primary reference frame.
type Segmentation struct {
	Enabled, UpdateMap, TemporalUpdate, UpdateData bool
	FeatureEnabled                                 [MaxSegments][SegLvlMax]bool
	FeatureData                                    [MaxSegments][SegLvlMax]int16
}

// LoopFilter is a loop_filter_params(), the deltas it leaves out loaded
// from the primary reference frame. UpdateRefDelta and UpdateModeDelta
// have bit i set for each delta i coded.
type LoopFilter struct {
	Level           [4]uint8
	Sharpness       uint8
	DeltaEnabled    bool
	DeltaUpdate     bool
	UpdateRefDelta  uint8
	RefDeltas       [TotalRefsPerFrame]int8
	UpdateModeDelta uint8
	ModeDeltas      [2]int8
}

// CDEF is a cdef_params(), its secondary strengths as coded: 3 stands
// for 4.
type CDEF struct {
	DampingMinus3            uint8
	Bits                     uint8
	YPri, YSec, UVPri, UVSec [8]uint8
}

// LoopRestoration is an lr_params(): FrameRestorationType and
// LoopRestorationSize in samples, by plane.
type LoopRestoration struct {
	Type         [3]uint8
	Size         [3]uint16
	UsesLr       bool
	UsesChromaLr bool
}

// GlobalMotion is a global_motion_params(): GmType and gm_params by
// reference name.
type GlobalMotion struct {
	Type   [TotalRefsPerFrame]uint8
	Params [TotalRefsPerFrame][6]int32
}

// FilmGrain is a film_grain_params(), loaded from the reference frame
// it names when it updates none.
type FilmGrain struct {
	ApplyGrain            bool
	GrainSeed             uint16
	UpdateGrain           bool
	ParamsRefIdx          uint8
	NumYPoints            uint8
	PointYValue           [14]uint8
	PointYScaling         [14]uint8
	ChromaScalingFromLuma bool
	NumCbPoints           uint8
	PointCbValue          [10]uint8
	PointCbScaling        [10]uint8
	NumCrPoints           uint8
	PointCrValue          [10]uint8
	PointCrScaling        [10]uint8
	ScalingMinus8         uint8
	ArCoeffLag            uint8
	ArCoeffsYPlus128      [24]uint8
	ArCoeffsCbPlus128     [25]uint8
	ArCoeffsCrPlus128     [25]uint8
	ArCoeffShiftMinus6    uint8
	GrainScaleShift       uint8
	CbMult, CbLumaMult    uint8
	CbOffset              uint16
	CrMult, CrLumaMult    uint8
	CrOffset              uint16
	OverlapFlag           bool
	ClipToRestrictedRange bool
}

// RefFrame is what the reference frame update process (7.20) saves of
// a frame in a slot, as far as later frame headers read it.
type RefFrame struct {
	Valid                     bool
	FrameID                   uint32
	FrameType                 FrameType
	Showable                  bool
	UpscaledWidth             int
	FrameWidth, FrameHeight   int
	RenderWidth, RenderHeight int
	MiCols, MiRows            int
	OrderHint                 uint8
	SavedOrderHints           [TotalRefsPerFrame]uint8
	SavedSignBias             [TotalRefsPerFrame]bool
	GmParams                  [TotalRefsPerFrame][6]int32
	LoopFilterRefDeltas       [TotalRefsPerFrame]int8
	LoopFilterModeDeltas      [2]int8
	FeatureEnabled            [MaxSegments][SegLvlMax]bool
	FeatureData               [MaxSegments][SegLvlMax]int16
	FilmGrain                 FilmGrain
	// What a reference picture's decoding reads of the frame.
	DisableFrameEndUpdateCDF bool
	SegmentationEnabled      bool
}

// Stream is the state a stream's frame headers are read with: its
// sequence header and the eight reference frame slots.
type Stream struct {
	Seq SequenceHeader
	Ref [NumRefFrames]RefFrame
	// orderHints and signBias are the OrderHints and RefFrameSignBias
	// the frame headers leave as they were; prevGm is PrevGmParams.
	orderHints [TotalRefsPerFrame]uint8
	signBias   [TotalRefsPerFrame]bool
	prevGm     [TotalRefsPerFrame][6]int32
	// temporalID and spatialID are the frame header OBU's.
	temporalID, spatialID uint8
}

// Reset forgets the reference frames: what decoding from a random
// access point does.
func (s *Stream) Reset() {
	s.Ref = [NumRefFrames]RefFrame{}
	s.orderHints, s.signBias = [TotalRefsPerFrame]uint8{}, [TotalRefsPerFrame]bool{}
}

// ParseFrameHeader reads the frame header u, a frame header or frame
// OBU, into h with the stream's state, which it moves on as the
// header's decoding process does; Update takes the frame into the
// reference slots once it is decoded.
func (s *Stream) ParseFrameHeader(h *FrameHeader, u *OBU) error {
	*h = FrameHeader{}
	s.temporalID, s.spatialID = u.TemporalID, u.SpatialID
	r := newReader(u.Payload)
	if err := s.uncompressedHeader(h, &r); err != nil {
		return err
	}
	if r.Short() {
		return fmt.Errorf("%w: frame header ends early, in %d bytes", ErrSyntax, len(u.Payload))
	}
	r.align()
	h.HeaderBytes = r.bytePos()
	return nil
}

func (s *Stream) uncompressedHeader(h *FrameHeader, r *reader) error {
	q := &s.Seq
	idLen := 0
	if q.FrameIDNumbersPresent {
		idLen = q.IDLen()
	}
	const allFrames = 1<<NumRefFrames - 1
	if q.ReducedStillPictureHeader {
		h.FrameType, h.FrameIsIntra, h.ShowFrame = KeyFrame, true, true
	} else {
		if h.ShowExistingFrame = r.Flag(); h.ShowExistingFrame {
			return s.showExisting(h, r, idLen)
		}
		h.FrameType = FrameType(r.f8(2))
		h.FrameIsIntra = h.FrameType == IntraOnlyFrame || h.FrameType == KeyFrame
		h.ShowFrame = r.Flag()
		if h.ShowFrame && q.DecoderModelInfoPresent && !q.Timing.EqualPictureInterval {
			r.Skip(int(q.DecoderModel.FramePresentationTimeLengthMinus1) + 1)
		}
		if h.ShowFrame {
			h.ShowableFrame = h.FrameType != KeyFrame
		} else {
			h.ShowableFrame = r.Flag()
		}
		if h.FrameType == SwitchFrame || h.FrameType == KeyFrame && h.ShowFrame {
			h.ErrorResilientMode = true
		} else {
			h.ErrorResilientMode = r.Flag()
		}
	}
	refs := &s.Ref
	if h.FrameType == KeyFrame && h.ShowFrame {
		for i := range refs {
			refs[i].Valid, refs[i].OrderHint = false, 0
		}
		for i := range RefsPerFrame {
			s.orderHints[LastFrame+i] = 0
		}
	}
	h.DisableCDFUpdate = r.Flag()
	if q.ForceScreenContentTools == SelectScreenContentTools {
		h.AllowScreenContentTools = r.Flag()
	} else {
		h.AllowScreenContentTools = q.ForceScreenContentTools == 1
	}
	if h.AllowScreenContentTools {
		if q.ForceIntegerMV == SelectIntegerMV {
			h.ForceIntegerMV = r.Flag()
		} else {
			h.ForceIntegerMV = q.ForceIntegerMV == 1
		}
	}
	if h.FrameIsIntra {
		h.ForceIntegerMV = true
	}
	if q.FrameIDNumbersPresent {
		h.CurrentFrameID = r.U(idLen)
		markRefFrames(refs, q, h.CurrentFrameID, idLen)
	}
	switch {
	case h.FrameType == SwitchFrame:
		h.FrameSizeOverride = true
	case q.ReducedStillPictureHeader:
	default:
		h.FrameSizeOverride = r.Flag()
	}
	h.OrderHint = r.f8(q.OrderHintBits)
	if h.FrameIsIntra || h.ErrorResilientMode {
		h.PrimaryRefFrame = PrimaryRefNone
	} else {
		h.PrimaryRefFrame = r.f8(3)
	}
	if q.DecoderModelInfoPresent {
		s.bufferRemovalTimes(h, r)
	}
	if h.FrameType == SwitchFrame || h.FrameType == KeyFrame && h.ShowFrame {
		h.RefreshFrameFlags = allFrames
	} else {
		h.RefreshFrameFlags = r.f8(8)
	}
	if h.FrameType == IntraOnlyFrame && h.RefreshFrameFlags == allFrames {
		return fmt.Errorf("%w: an intra-only frame refreshes every slot", ErrSyntax)
	}
	if (!h.FrameIsIntra || h.RefreshFrameFlags != allFrames) && h.ErrorResilientMode && q.EnableOrderHint {
		for i := range refs {
			hint := r.f8(q.OrderHintBits)
			if hint != refs[i].OrderHint {
				refs[i] = RefFrame{OrderHint: hint}
			}
		}
	}
	if h.FrameIsIntra {
		if err := s.frameSize(h, r); err != nil {
			return err
		}
		renderSize(h, r)
		if h.AllowScreenContentTools && h.UpscaledWidth == h.FrameWidth {
			h.AllowIntrabc = r.Flag()
		}
	} else {
		if err := s.interRefs(h, r, refs, idLen); err != nil {
			return err
		}
		for i := range RefsPerFrame {
			ref := LastFrame + i
			hint := refs[h.RefFrameIdx[i]].OrderHint
			s.orderHints[ref] = hint
			s.signBias[ref] = q.EnableOrderHint && s.relativeDist(hint, h.OrderHint) > 0
		}
	}
	h.OrderHints, h.RefFrameSignBias = s.orderHints, s.signBias
	if q.ReducedStillPictureHeader || h.DisableCDFUpdate {
		h.DisableFrameEndUpdateCDF = true
	} else {
		h.DisableFrameEndUpdateCDF = r.Flag()
	}
	var prev *RefFrame
	if h.PrimaryRefFrame != PrimaryRefNone {
		prev = &refs[h.RefFrameIdx[h.PrimaryRefFrame]]
		if !prev.Valid {
			return fmt.Errorf("%w: primary_ref_frame %d names slot %d, which holds no frame", ErrSyntax, h.PrimaryRefFrame, h.RefFrameIdx[h.PrimaryRefFrame])
		}
	}
	s.pastState(h, prev)
	if err := s.tileInfo(h, r); err != nil {
		return err
	}
	s.quantization(h, r)
	segmentation(h, r)
	h.deltaParams(r)
	h.lossless()
	s.loopFilter(h, r)
	s.cdef(h, r)
	s.lr(h, r)
	switch {
	case h.CodedLossless:
		h.TxMode = TxModeOnly4x4
	case r.Flag():
		h.TxMode = TxModeSelect
	default:
		h.TxMode = TxModeLargest
	}
	if !h.FrameIsIntra {
		h.ReferenceSelect = r.Flag()
	}
	s.skipMode(h, r, refs)
	if !h.FrameIsIntra && !h.ErrorResilientMode && q.EnableWarpedMotion {
		h.AllowWarpedMotion = r.Flag()
	}
	h.ReducedTxSet = r.Flag()
	s.globalMotion(h, r)
	return s.filmGrain(h, r, refs)
}

// showExisting reads the rest of a header that shows the frame in a
// slot, and when it is a key frame, loads the frame's state into h as
// the reference frame loading process (7.21) does.
func (s *Stream) showExisting(h *FrameHeader, r *reader, idLen int) error {
	q := &s.Seq
	h.FrameToShowMapIdx = r.f8(3)
	if q.DecoderModelInfoPresent && !q.Timing.EqualPictureInterval {
		r.Skip(int(q.DecoderModel.FramePresentationTimeLengthMinus1) + 1)
	}
	if q.FrameIDNumbersPresent {
		r.Skip(idLen)
	}
	ref := &s.Ref[h.FrameToShowMapIdx]
	if !ref.Valid {
		return fmt.Errorf("%w: show_existing_frame of slot %d, which holds no frame", ErrSyntax, h.FrameToShowMapIdx)
	}
	h.FrameType, h.ShowFrame = ref.FrameType, true
	h.FrameIsIntra = h.FrameType == KeyFrame || h.FrameType == IntraOnlyFrame
	if q.FilmGrainPresent {
		h.FilmGrain = ref.FilmGrain
	}
	h.CurrentFrameID, h.OrderHint = ref.FrameID, ref.OrderHint
	h.UpscaledWidth, h.FrameWidth, h.FrameHeight = ref.UpscaledWidth, ref.FrameWidth, ref.FrameHeight
	h.RenderWidth, h.RenderHeight, h.MiCols, h.MiRows = ref.RenderWidth, ref.RenderHeight, ref.MiCols, ref.MiRows
	if h.FrameType != KeyFrame {
		return nil
	}
	h.RefreshFrameFlags = 1<<NumRefFrames - 1
	h.OrderHints, h.RefFrameSignBias = ref.SavedOrderHints, ref.SavedSignBias
	h.GlobalMotion.Params = ref.GmParams
	h.LoopFilter.RefDeltas, h.LoopFilter.ModeDeltas = ref.LoopFilterRefDeltas, ref.LoopFilterModeDeltas
	h.Seg.FeatureEnabled, h.Seg.FeatureData = ref.FeatureEnabled, ref.FeatureData
	h.Seg.Enabled, h.DisableFrameEndUpdateCDF = ref.SegmentationEnabled, ref.DisableFrameEndUpdateCDF
	return nil
}

func (s *Stream) bufferRemovalTimes(h *FrameHeader, r *reader) {
	q := &s.Seq
	if h.BufferRemovalTimePresent = r.Flag(); !h.BufferRemovalTimePresent {
		return
	}
	for op := range q.OperatingPoints {
		if !q.DecoderModelPresentForOp[op] {
			continue
		}
		idc := q.OperatingPointIdc[op]
		inTemporal := idc>>s.temporalID&1 != 0
		inSpatial := idc>>(s.spatialID+8)&1 != 0
		if idc == 0 || inTemporal && inSpatial {
			r.Skip(int(q.DecoderModel.BufferRemovalTimeLengthMinus1) + 1)
		}
	}
}

// markRefFrames is mark_ref_frames: the slots whose frame IDs are too
// far from the frame's to be referenced are invalid.
func markRefFrames(refs *[NumRefFrames]RefFrame, q *SequenceHeader, id uint32, idLen int) {
	diff := uint32(1) << (int(q.DeltaFrameIDLengthMinus2) + 2)
	for i := range refs {
		ref := refs[i].FrameID
		if id > diff {
			if ref > id || ref < id-diff {
				refs[i].Valid = false
			}
		} else if ref > id && ref < 1<<idLen+id-diff {
			refs[i].Valid = false
		}
	}
}

// interRefs reads an inter frame's references and size: from
// ref_frame_idx through use_ref_frame_mvs.
func (s *Stream) interRefs(h *FrameHeader, r *reader, refs *[NumRefFrames]RefFrame, idLen int) error {
	q := &s.Seq
	if q.EnableOrderHint {
		h.FrameRefsShortSignaling = r.Flag()
	}
	if h.FrameRefsShortSignaling {
		last, gold := r.f8(3), r.f8(3)
		h.RefFrameIdx = s.setFrameRefs(refs, h.OrderHint, last, gold)
	}
	for i := range RefsPerFrame {
		if !h.FrameRefsShortSignaling {
			h.RefFrameIdx[i] = r.f8(3)
		}
		if q.FrameIDNumbersPresent {
			delta := r.U(int(q.DeltaFrameIDLengthMinus2)+2) + 1
			h.ExpectedFrameID[i] = (h.CurrentFrameID + 1<<idLen - delta) % (1 << idLen)
		}
	}
	for i, slot := range h.RefFrameIdx {
		if !refs[slot].Valid {
			return fmt.Errorf("%w: %s reference %d names slot %d, which holds no frame", ErrSyntax, h.FrameType, LastFrame+i, slot)
		}
	}
	if h.FrameSizeOverride && !h.ErrorResilientMode {
		if err := s.frameSizeWithRefs(h, r, refs); err != nil {
			return err
		}
	} else {
		if err := s.frameSize(h, r); err != nil {
			return err
		}
		renderSize(h, r)
	}
	if !h.ForceIntegerMV {
		h.AllowHighPrecisionMV = r.Flag()
	}
	if h.IsFilterSwitchable = r.Flag(); h.IsFilterSwitchable {
		h.InterpolationFilter = SwitchableFilter
	} else {
		h.InterpolationFilter = r.f8(2)
	}
	h.IsMotionModeSwitchable = r.Flag()
	if !h.ErrorResilientMode && q.EnableRefFrameMVs {
		h.UseRefFrameMVs = r.Flag()
	}
	return nil
}

// relativeDist is get_relative_dist.
func (s *Stream) relativeDist(a, b uint8) int {
	if !s.Seq.EnableOrderHint {
		return 0
	}
	diff := int(a) - int(b)
	m := 1 << (s.Seq.OrderHintBits - 1)
	return diff&(m-1) - diff&m
}

// setFrameRefs is set_frame_refs (7.8): the slots of the references
// frame_refs_short_signaling leaves out.
func (s *Stream) setFrameRefs(refs *[NumRefFrames]RefFrame, orderHint, last, gold uint8) [RefsPerFrame]uint8 {
	var idx [RefsPerFrame]int
	for i := range idx {
		idx[i] = -1
	}
	idx[0], idx[GoldenFrame-LastFrame] = int(last), int(gold)
	var used [NumRefFrames]bool
	used[last], used[gold] = true, true
	cur := 1 << (s.Seq.OrderHintBits - 1)
	var shifted [NumRefFrames]int
	for i := range shifted {
		shifted[i] = cur + s.relativeDist(refs[i].OrderHint, orderHint)
	}
	find := func(backward, latest bool) int {
		ref, best := -1, 0
		for i, hint := range shifted {
			if used[i] || hint >= cur != backward {
				continue
			}
			if ref < 0 || latest && hint >= best || !latest && hint < best {
				ref, best = i, hint
			}
		}
		return ref
	}
	take := func(name, ref int) {
		if ref >= 0 {
			idx[name-LastFrame], used[ref] = ref, true
		}
	}
	take(AltrefFrame, find(true, true))
	take(BwdrefFrame, find(true, false))
	take(Altref2Frame, find(true, false))
	for _, name := range [...]int{Last2Frame, Last3Frame, BwdrefFrame, Altref2Frame, AltrefFrame} {
		if idx[name-LastFrame] < 0 {
			take(name, find(false, true))
		}
	}
	earliest, at := 0, -1
	for i, hint := range shifted {
		if at < 0 || hint < earliest {
			at, earliest = i, hint
		}
	}
	var out [RefsPerFrame]uint8
	for i, v := range idx {
		if v < 0 {
			v = at
		}
		out[i] = uint8(v)
	}
	return out
}

// frameSize reads frame_size(): the frame size and its superres
// scaling.
func (s *Stream) frameSize(h *FrameHeader, r *reader) error {
	q := &s.Seq
	if h.FrameSizeOverride {
		h.FrameWidth, h.FrameHeight = int(r.U(q.FrameWidthBits))+1, int(r.U(q.FrameHeightBits))+1
	} else {
		h.FrameWidth, h.FrameHeight = q.MaxFrameWidth, q.MaxFrameHeight
	}
	return s.superres(h, r)
}

// superres reads superres_params() and works out the frame's mode info
// size, compute_image_size().
func (s *Stream) superres(h *FrameHeader, r *reader) error {
	denom := superresNum
	if s.Seq.EnableSuperres {
		h.UseSuperres = r.Flag()
	}
	if h.UseSuperres {
		h.CodedDenom = r.f8(superresDenomBits)
		denom = int(h.CodedDenom) + superresDenomMin
	}
	h.UpscaledWidth = h.FrameWidth
	h.FrameWidth = (h.UpscaledWidth*superresNum + denom/2) / denom
	if h.UpscaledWidth > s.Seq.MaxFrameWidth || h.FrameHeight > s.Seq.MaxFrameHeight {
		return fmt.Errorf("%w: frame of %dx%d past the sequence's %dx%d", ErrSyntax, h.UpscaledWidth, h.FrameHeight, s.Seq.MaxFrameWidth, s.Seq.MaxFrameHeight)
	}
	h.MiCols, h.MiRows = 2*((h.FrameWidth+7)>>3), 2*((h.FrameHeight+7)>>3)
	return nil
}

func renderSize(h *FrameHeader, r *reader) {
	if h.RenderAndFrameSizeDifferent = r.Flag(); h.RenderAndFrameSizeDifferent {
		h.RenderWidth, h.RenderHeight = int(r.U(16))+1, int(r.U(16))+1
	} else {
		h.RenderWidth, h.RenderHeight = h.UpscaledWidth, h.FrameHeight
	}
}

// frameSizeWithRefs reads frame_size_with_refs(): a size of a reference
// frame's, or one coded.
func (s *Stream) frameSizeWithRefs(h *FrameHeader, r *reader, refs *[NumRefFrames]RefFrame) error {
	for i := range RefsPerFrame {
		if !r.Flag() {
			continue
		}
		ref := &refs[h.RefFrameIdx[i]]
		h.FrameWidth, h.FrameHeight = ref.UpscaledWidth, ref.FrameHeight
		h.RenderWidth, h.RenderHeight = ref.RenderWidth, ref.RenderHeight
		return s.superres(h, r)
	}
	if err := s.frameSize(h, r); err != nil {
		return err
	}
	renderSize(h, r)
	return nil
}

// defaultRefDeltas are loop_filter_ref_deltas as
// setup_past_independence sets them.
var defaultRefDeltas = [TotalRefsPerFrame]int8{1, 0, 0, 0, -1, 0, -1, -1}

// pastState sets the state the rest of the header predicts from: the
// primary reference frame's (load_previous) or, without one, its
// defaults (setup_past_independence).
func (s *Stream) pastState(h *FrameHeader, prev *RefFrame) {
	if prev == nil {
		for ref := range s.prevGm {
			s.prevGm[ref] = identityParams()
		}
		h.LoopFilter.RefDeltas = defaultRefDeltas
		h.LoopFilter.DeltaEnabled = true
		return
	}
	s.prevGm = prev.GmParams
	h.LoopFilter.RefDeltas, h.LoopFilter.ModeDeltas = prev.LoopFilterRefDeltas, prev.LoopFilterModeDeltas
	h.Seg.FeatureEnabled, h.Seg.FeatureData = prev.FeatureEnabled, prev.FeatureData
}

func identityParams() [6]int32 {
	return [6]int32{0, 0, 1 << warpedModelPrecBits, 0, 0, 1 << warpedModelPrecBits}
}

// tileInfo reads tile_info().
func (s *Stream) tileInfo(h *FrameHeader, r *reader) error {
	t := &h.Tile
	sbShift := 4
	if s.Seq.Use128x128Superblock {
		sbShift = 5
	}
	sbCols := (h.MiCols + 1<<sbShift - 1) >> sbShift
	sbRows := (h.MiRows + 1<<sbShift - 1) >> sbShift
	sbSize := sbShift + 2
	maxTileWidthSb := maxTileWidth >> sbSize
	maxTileAreaSb := maxTileArea >> (2 * sbSize)
	minLog2TileCols := tileLog2(maxTileWidthSb, sbCols)
	maxLog2TileCols := tileLog2(1, min(sbCols, MaxTileCols))
	maxLog2TileRows := tileLog2(1, min(sbRows, MaxTileRows))
	minLog2Tiles := max(minLog2TileCols, tileLog2(maxTileAreaSb, sbRows*sbCols))
	if t.UniformSpacing = r.Flag(); t.UniformSpacing {
		t.ColsLog2 = minLog2TileCols
		for t.ColsLog2 < maxLog2TileCols && r.Flag() {
			t.ColsLog2++
		}
		widthSb := (sbCols + 1<<t.ColsLog2 - 1) >> t.ColsLog2
		t.Cols = uniformTiles(t.MiColStarts[:], t.WidthInSbsMinus1[:], sbCols, widthSb, sbShift, h.MiCols)
		t.RowsLog2 = max(minLog2Tiles-t.ColsLog2, 0)
		for t.RowsLog2 < maxLog2TileRows && r.Flag() {
			t.RowsLog2++
		}
		heightSb := (sbRows + 1<<t.RowsLog2 - 1) >> t.RowsLog2
		t.Rows = uniformTiles(t.MiRowStarts[:], t.HeightInSbsMinus1[:], sbRows, heightSb, sbShift, h.MiRows)
	} else {
		widest := 0
		n, err := explicitTiles(r, t.MiColStarts[:], t.WidthInSbsMinus1[:], sbCols, maxTileWidthSb, sbShift, h.MiCols, &widest)
		if err != nil {
			return err
		}
		t.Cols, t.ColsLog2 = n, tileLog2(1, n)
		if minLog2Tiles > 0 {
			maxTileAreaSb = sbRows * sbCols >> (minLog2Tiles + 1)
		} else {
			maxTileAreaSb = sbRows * sbCols
		}
		n, err = explicitTiles(r, t.MiRowStarts[:], t.HeightInSbsMinus1[:], sbRows, max(maxTileAreaSb/widest, 1), sbShift, h.MiRows, nil)
		if err != nil {
			return err
		}
		t.Rows, t.RowsLog2 = n, tileLog2(1, n)
	}
	t.TileSizeBytes = 4
	if t.ColsLog2 > 0 || t.RowsLog2 > 0 {
		t.ContextUpdateTileID = r.U16(t.RowsLog2 + t.ColsLog2)
		t.TileSizeBytes = int(r.U(2)) + 1
		if int(t.ContextUpdateTileID) >= t.Cols*t.Rows {
			return fmt.Errorf("%w: context_update_tile_id %d of %d tiles", ErrSyntax, t.ContextUpdateTileID, t.Cols*t.Rows)
		}
	}
	return nil
}

// uniformTiles lays out tiles of size superblocks each over sbs
// superblocks, how many there are.
func uniformTiles(starts, sizes []uint16, sbs, size, sbShift, mis int) int {
	i := 0
	for start := 0; start < sbs; start += size {
		starts[i] = intconv.Must[uint16](start << sbShift)
		sizes[i] = intconv.Must[uint16](min(size, sbs-start) - 1)
		i++
	}
	starts[i] = intconv.Must[uint16](mis)
	return i
}

// explicitTiles reads the sizes of tiles coded one by one over sbs
// superblocks, each at most maxSize, and how many there are; widest,
// when not nil, is the widest's.
func explicitTiles(r *reader, starts, sizes []uint16, sbs, maxSize, sbShift, mis int, widest *int) (int, error) {
	i := 0
	for start := 0; start < sbs; i++ {
		if i == len(sizes) {
			return 0, fmt.Errorf("%w: more than %d tiles across the frame", ErrSyntax, len(sizes))
		}
		starts[i] = intconv.Must[uint16](start << sbShift)
		size := int(r.ns(intconv.Must[uint32](min(sbs-start, maxSize)))) + 1
		sizes[i] = intconv.Must[uint16](size - 1)
		if widest != nil {
			*widest = max(*widest, size)
		}
		start += size
	}
	starts[i] = intconv.Must[uint16](mis)
	return i, nil
}

func tileLog2(blkSize, target int) int {
	k := 0
	for blkSize<<k < target {
		k++
	}
	return k
}

// quantization reads quantization_params().
func (s *Stream) quantization(h *FrameHeader, r *reader) {
	q := &h.Quant
	c := &s.Seq.Color
	q.BaseQIdx = r.f8(8)
	q.DeltaQYDc = readDeltaQ(r)
	if c.NumPlanes() > 1 {
		if c.SeparateUVDeltaQ {
			q.DiffUVDelta = r.Flag()
		}
		q.DeltaQUDc, q.DeltaQUAc = readDeltaQ(r), readDeltaQ(r)
		if q.DiffUVDelta {
			q.DeltaQVDc, q.DeltaQVAc = readDeltaQ(r), readDeltaQ(r)
		} else {
			q.DeltaQVDc, q.DeltaQVAc = q.DeltaQUDc, q.DeltaQUAc
		}
	}
	if q.UsingQmatrix = r.Flag(); q.UsingQmatrix {
		q.QmY, q.QmU = r.f8(4), r.f8(4)
		if c.SeparateUVDeltaQ {
			q.QmV = r.f8(4)
		} else {
			q.QmV = q.QmU
		}
	}
}

func readDeltaQ(r *reader) int8 {
	if r.Flag() {
		return r.su8(7)
	}
	return 0
}

// The segmentation features' coding (5.9.14).
var (
	segFeatureBits   = [SegLvlMax]int{8, 6, 6, 6, 6, 3, 0, 0}
	segFeatureSigned = [SegLvlMax]bool{true, true, true, true, true, false, false, false}
	segFeatureMax    = [SegLvlMax]int16{255, maxLoopFilter, maxLoopFilter, maxLoopFilter, maxLoopFilter, 7, 0, 0}
)

// segmentation reads segmentation_params().
func segmentation(h *FrameHeader, r *reader) {
	g := &h.Seg
	if g.Enabled = r.Flag(); !g.Enabled {
		g.FeatureEnabled, g.FeatureData = [MaxSegments][SegLvlMax]bool{}, [MaxSegments][SegLvlMax]int16{}
		return
	}
	if h.PrimaryRefFrame == PrimaryRefNone {
		g.UpdateMap, g.UpdateData = true, true
	} else {
		if g.UpdateMap = r.Flag(); g.UpdateMap {
			g.TemporalUpdate = r.Flag()
		}
		g.UpdateData = r.Flag()
	}
	if !g.UpdateData {
		return
	}
	for i := range MaxSegments {
		for j := range SegLvlMax {
			g.FeatureEnabled[i][j] = r.Flag()
			g.FeatureData[i][j] = 0
			if !g.FeatureEnabled[i][j] {
				continue
			}
			limit := segFeatureMax[j]
			if segFeatureSigned[j] {
				g.FeatureData[i][j] = intconv.Must[int16](max(-int32(limit), min(int32(limit), r.su(1+segFeatureBits[j]))))
			} else {
				g.FeatureData[i][j] = intconv.Must[int16](min(int32(limit), intconv.Must[int32](r.U(segFeatureBits[j]))))
			}
		}
	}
}

// deltaParams reads delta_q_params() and delta_lf_params().
func (h *FrameHeader) deltaParams(r *reader) {
	if h.Quant.BaseQIdx > 0 {
		h.DeltaQPresent = r.Flag()
	}
	if !h.DeltaQPresent {
		return
	}
	h.DeltaQRes = r.f8(2)
	if !h.AllowIntrabc {
		h.DeltaLFPresent = r.Flag()
	}
	if h.DeltaLFPresent {
		h.DeltaLFRes = r.f8(2)
		h.DeltaLFMulti = r.Flag()
	}
}

// lossless works out CodedLossless and AllLossless: every segment's
// qindex zero, with no DC or chroma deltas.
func (h *FrameHeader) lossless() {
	q := &h.Quant
	h.CodedLossless = q.DeltaQYDc == 0 && q.DeltaQUAc == 0 && q.DeltaQUDc == 0 && q.DeltaQVAc == 0 && q.DeltaQVDc == 0
	for seg := range MaxSegments {
		if h.qindex(seg) != 0 {
			h.CodedLossless = false
		}
	}
	h.AllLossless = h.CodedLossless && h.FrameWidth == h.UpscaledWidth
}

// qindex is get_qindex(1, segment).
func (h *FrameHeader) qindex(segment int) int {
	base := int(h.Quant.BaseQIdx)
	if !h.Seg.Enabled || !h.Seg.FeatureEnabled[segment][0] {
		return base
	}
	return max(0, min(255, base+int(h.Seg.FeatureData[segment][0])))
}

// loopFilter reads loop_filter_params().
func (s *Stream) loopFilter(h *FrameHeader, r *reader) {
	l := &h.LoopFilter
	if h.CodedLossless || h.AllowIntrabc {
		l.RefDeltas = defaultRefDeltas
		l.ModeDeltas = [2]int8{}
		return
	}
	l.Level[0], l.Level[1] = r.f8(6), r.f8(6)
	if s.Seq.Color.NumPlanes() > 1 && (l.Level[0] != 0 || l.Level[1] != 0) {
		l.Level[2], l.Level[3] = r.f8(6), r.f8(6)
	}
	l.Sharpness = r.f8(3)
	if l.DeltaEnabled = r.Flag(); !l.DeltaEnabled {
		return
	}
	if l.DeltaUpdate = r.Flag(); !l.DeltaUpdate {
		return
	}
	for i := range TotalRefsPerFrame {
		if r.Flag() {
			l.UpdateRefDelta |= 1 << i
			l.RefDeltas[i] = r.su8(7)
		}
	}
	for i := range 2 {
		if r.Flag() {
			l.UpdateModeDelta |= 1 << i
			l.ModeDeltas[i] = r.su8(7)
		}
	}
}

// cdef reads cdef_params().
func (s *Stream) cdef(h *FrameHeader, r *reader) {
	c := &h.CDEF
	if h.CodedLossless || h.AllowIntrabc || !s.Seq.EnableCDEF {
		return
	}
	c.DampingMinus3 = r.f8(2)
	c.Bits = r.f8(2)
	for i := range 1 << c.Bits {
		c.YPri[i], c.YSec[i] = r.f8(4), r.f8(2)
		if s.Seq.Color.NumPlanes() > 1 {
			c.UVPri[i], c.UVSec[i] = r.f8(4), r.f8(2)
		}
	}
}

// remapLrType is Remap_Lr_Type, lr_type's coding of
// FrameRestorationType.
var remapLrType = [4]uint8{RestoreNone, RestoreSwitchable, RestoreWiener, RestoreSgrproj}

// restorationTileSizeMax is RESTORATION_TILESIZE_MAX.
const restorationTileSizeMax = 256

// lr reads lr_params().
func (s *Stream) lr(h *FrameHeader, r *reader) {
	l := &h.LR
	q := &s.Seq
	if h.AllLossless || h.AllowIntrabc || !q.EnableRestoration {
		return
	}
	for i := range q.Color.NumPlanes() {
		l.Type[i] = remapLrType[r.U(2)]
		if l.Type[i] != RestoreNone {
			l.UsesLr = true
			l.UsesChromaLr = l.UsesChromaLr || i > 0
		}
	}
	if !l.UsesLr {
		return
	}
	shift := int(r.U(1))
	if q.Use128x128Superblock {
		shift++
	} else if shift == 1 {
		shift += int(r.U(1))
	}
	l.Size[0] = restorationTileSizeMax >> (2 - shift)
	uvShift := 0
	if q.Color.SubsamplingX && q.Color.SubsamplingY && l.UsesChromaLr {
		uvShift = int(r.U(1))
	}
	l.Size[1], l.Size[2] = l.Size[0]>>uvShift, l.Size[0]>>uvShift
}

// skipMode works out skip_mode_params()'s reference frames and reads
// skip_mode_present where skip mode is allowed.
func (s *Stream) skipMode(h *FrameHeader, r *reader, refs *[NumRefFrames]RefFrame) {
	if h.FrameIsIntra || !h.ReferenceSelect || !s.Seq.EnableOrderHint {
		return
	}
	forward, backward := -1, -1
	var forwardHint, backwardHint uint8
	for i := range RefsPerFrame {
		hint := refs[h.RefFrameIdx[i]].OrderHint
		switch d := s.relativeDist(hint, h.OrderHint); {
		case d < 0:
			if forward < 0 || s.relativeDist(hint, forwardHint) > 0 {
				forward, forwardHint = i, hint
			}
		case d > 0:
			if backward < 0 || s.relativeDist(hint, backwardHint) < 0 {
				backward, backwardHint = i, hint
			}
		}
	}
	if forward < 0 {
		return
	}
	second := backward
	if second < 0 {
		var secondHint uint8
		for i := range RefsPerFrame {
			hint := refs[h.RefFrameIdx[i]].OrderHint
			if s.relativeDist(hint, forwardHint) < 0 && (second < 0 || s.relativeDist(hint, secondHint) > 0) {
				second, secondHint = i, hint
			}
		}
		if second < 0 {
			return
		}
	}
	h.SkipModeFrame = [2]uint8{uint8(LastFrame + min(forward, second)), uint8(LastFrame + max(forward, second))}
	h.SkipModePresent = r.Flag()
}

// The global motion parameters' coding (5.9.25).
const (
	gmAbsAlphaBits      = 12
	gmAlphaPrecBits     = 15
	gmAbsTransOnlyBits  = 9
	gmTransOnlyPrecBits = 3
	gmAbsTransBits      = 12
	gmTransPrecBits     = 6
)

// globalMotion reads global_motion_params().
func (s *Stream) globalMotion(h *FrameHeader, r *reader) {
	g := &h.GlobalMotion
	for ref := range g.Params {
		g.Type[ref], g.Params[ref] = Identity, identityParams()
	}
	if h.FrameIsIntra {
		return
	}
	for ref := LastFrame; ref <= AltrefFrame; ref++ {
		typ := uint8(Identity)
		if r.Flag() {
			switch {
			case r.Flag():
				typ = RotZoom
			case r.Flag():
				typ = Translation
			default:
				typ = Affine
			}
		}
		g.Type[ref] = typ
		p := &g.Params[ref]
		if typ >= RotZoom {
			p[2], p[3] = s.globalParam(h, r, typ, ref, 2), s.globalParam(h, r, typ, ref, 3)
			if typ == Affine {
				p[4], p[5] = s.globalParam(h, r, typ, ref, 4), s.globalParam(h, r, typ, ref, 5)
			} else {
				p[4], p[5] = -p[3], p[2]
			}
		}
		if typ >= Translation {
			p[0], p[1] = s.globalParam(h, r, typ, ref, 0), s.globalParam(h, r, typ, ref, 1)
		}
	}
}

// globalParam reads read_global_param(type, ref, idx).
func (s *Stream) globalParam(h *FrameHeader, r *reader, typ uint8, ref, idx int) int32 {
	absBits, precBits := gmAbsAlphaBits, gmAlphaPrecBits
	if idx < 2 {
		if typ == Translation {
			hp := 0
			if !h.AllowHighPrecisionMV {
				hp = 1
			}
			absBits, precBits = gmAbsTransOnlyBits-hp, gmTransOnlyPrecBits-hp
		} else {
			absBits, precBits = gmAbsTransBits, gmTransPrecBits
		}
	}
	precDiff := warpedModelPrecBits - precBits
	round, sub := int32(0), int32(0)
	if idx%3 == 2 {
		round, sub = 1<<warpedModelPrecBits, 1<<precBits
	}
	mx := int32(1) << absBits
	ref0 := s.prevGm[ref][idx]>>precDiff - sub
	return signedSubexpWithRef(r, -mx, mx+1, ref0)<<precDiff + round
}

// signedSubexpWithRef is decode_signed_subexp_with_ref.
func signedSubexpWithRef(r *reader, low, high, ref int32) int32 {
	return unsignedSubexpWithRef(r, high-low, ref-low) + low
}

func unsignedSubexpWithRef(r *reader, mx, ref int32) int32 {
	v := subexp(r, mx)
	if ref<<1 <= mx {
		return inverseRecenter(ref, v)
	}
	return mx - 1 - inverseRecenter(mx-1-ref, v)
}

// subexp is decode_subexp(numSyms).
func subexp(r *reader, numSyms int32) int32 {
	i, mk, k := 0, int32(0), 3
	for {
		b2 := k
		if i > 0 {
			b2 = k + i - 1
		}
		a := int32(1) << b2
		if numSyms <= mk+3*a {
			return intconv.Must[int32](r.ns(intconv.Must[uint32](numSyms-mk))) + mk
		}
		if !r.Flag() {
			return intconv.Must[int32](r.U(b2)) + mk
		}
		i++
		mk += a
		if r.Short() {
			return mk
		}
	}
}

func inverseRecenter(r, v int32) int32 {
	switch {
	case v > 2*r:
		return v
	case v&1 != 0:
		return r - (v+1)>>1
	}
	return r + v>>1
}

// filmGrain reads film_grain_params().
func (s *Stream) filmGrain(h *FrameHeader, r *reader, refs *[NumRefFrames]RefFrame) error {
	q := &s.Seq
	g := &h.FilmGrain
	if !q.FilmGrainPresent || !h.ShowFrame && !h.ShowableFrame {
		return nil
	}
	if g.ApplyGrain = r.Flag(); !g.ApplyGrain {
		return nil
	}
	g.GrainSeed = r.U16(16)
	g.UpdateGrain = true
	if h.FrameType == InterFrame {
		g.UpdateGrain = r.Flag()
	}
	if !g.UpdateGrain {
		idx := r.f8(3)
		if !slices.Contains(h.RefFrameIdx[:], idx) {
			return fmt.Errorf("%w: film_grain_params_ref_idx %d names none of the frame's references", ErrSyntax, idx)
		}
		seed := g.GrainSeed
		*g = refs[idx].FilmGrain
		g.GrainSeed, g.UpdateGrain, g.ParamsRefIdx = seed, false, idx
		return nil
	}
	g.NumYPoints = r.f8(4)
	if g.NumYPoints > 14 {
		return fmt.Errorf("%w: num_y_points %d", ErrSyntax, g.NumYPoints)
	}
	for i := range g.NumYPoints {
		g.PointYValue[i], g.PointYScaling[i] = r.f8(8), r.f8(8)
	}
	c := &q.Color
	if !c.MonoChrome {
		g.ChromaScalingFromLuma = r.Flag()
	}
	if !c.MonoChrome && !g.ChromaScalingFromLuma && (!c.SubsamplingX || !c.SubsamplingY || g.NumYPoints != 0) {
		g.NumCbPoints = r.f8(4)
		if g.NumCbPoints > 10 {
			return fmt.Errorf("%w: num_cb_points %d", ErrSyntax, g.NumCbPoints)
		}
		for i := range g.NumCbPoints {
			g.PointCbValue[i], g.PointCbScaling[i] = r.f8(8), r.f8(8)
		}
		g.NumCrPoints = r.f8(4)
		if g.NumCrPoints > 10 {
			return fmt.Errorf("%w: num_cr_points %d", ErrSyntax, g.NumCrPoints)
		}
		for i := range g.NumCrPoints {
			g.PointCrValue[i], g.PointCrScaling[i] = r.f8(8), r.f8(8)
		}
	}
	g.ScalingMinus8 = r.f8(2)
	g.ArCoeffLag = r.f8(2)
	numPosLuma := 2 * int(g.ArCoeffLag) * (int(g.ArCoeffLag) + 1)
	numPosChroma := numPosLuma
	if g.NumYPoints != 0 {
		numPosChroma++
		for i := range numPosLuma {
			g.ArCoeffsYPlus128[i] = r.f8(8)
		}
	}
	if g.ChromaScalingFromLuma || g.NumCbPoints != 0 {
		for i := range numPosChroma {
			g.ArCoeffsCbPlus128[i] = r.f8(8)
		}
	}
	if g.ChromaScalingFromLuma || g.NumCrPoints != 0 {
		for i := range numPosChroma {
			g.ArCoeffsCrPlus128[i] = r.f8(8)
		}
	}
	g.ArCoeffShiftMinus6 = r.f8(2)
	g.GrainScaleShift = r.f8(2)
	if g.NumCbPoints != 0 {
		g.CbMult, g.CbLumaMult, g.CbOffset = r.f8(8), r.f8(8), r.U16(9)
	}
	if g.NumCrPoints != 0 {
		g.CrMult, g.CrLumaMult, g.CrOffset = r.f8(8), r.f8(8), r.U16(9)
	}
	g.OverlapFlag = r.Flag()
	g.ClipToRestrictedRange = r.Flag()
	return nil
}

// Update takes h, decoded or shown, into the slots its
// refresh_frame_flags name: the reference frame update process (7.20).
func (s *Stream) Update(h *FrameHeader) {
	if h.ShowExistingFrame && h.FrameType != KeyFrame {
		return
	}
	if h.ShowExistingFrame {
		s.orderHints = h.OrderHints
		src := s.Ref[h.FrameToShowMapIdx]
		for i := range s.Ref {
			s.Ref[i] = src
		}
		return
	}
	ref := RefFrame{
		Valid: true, FrameID: h.CurrentFrameID, FrameType: h.FrameType, Showable: h.ShowableFrame,
		UpscaledWidth: h.UpscaledWidth, FrameWidth: h.FrameWidth, FrameHeight: h.FrameHeight,
		RenderWidth: h.RenderWidth, RenderHeight: h.RenderHeight, MiCols: h.MiCols, MiRows: h.MiRows,
		OrderHint: h.OrderHint, SavedOrderHints: h.OrderHints, SavedSignBias: h.RefFrameSignBias,
		GmParams: h.GlobalMotion.Params, LoopFilterRefDeltas: h.LoopFilter.RefDeltas, LoopFilterModeDeltas: h.LoopFilter.ModeDeltas,
		FeatureEnabled: h.Seg.FeatureEnabled, FeatureData: h.Seg.FeatureData, FilmGrain: h.FilmGrain,
		DisableFrameEndUpdateCDF: h.DisableFrameEndUpdateCDF, SegmentationEnabled: h.Seg.Enabled,
	}
	for i := range s.Ref {
		if h.RefreshFrameFlags>>i&1 != 0 {
			s.Ref[i] = ref
		}
	}
}

// Peek is what the frame header OBU payload b starts with: whether it
// shows an existing frame, and the type of the frame it codes when not.
// It reads no state but the sequence header, so it tells which frame
// a decoder starting at a random access point can begin with.
func (s *Stream) Peek(b []byte) (showExisting bool, t FrameType) {
	if s.Seq.ReducedStillPictureHeader {
		return false, KeyFrame
	}
	r := newReader(b)
	if r.Flag() {
		return true, KeyFrame
	}
	return false, FrameType(r.f8(2))
}
