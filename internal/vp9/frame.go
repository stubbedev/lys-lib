// Package vp9 reads the VP9 bitstream (VP9 Bitstream & Decoding Process
// Specification, version 0.6) as far as a decoder that keeps its own
// reference state needs: superframes and the uncompressed header, with
// the state its syntax and semantics predict from (the reference frames'
// sizes, the loop filter deltas, the segmentation features and whether
// a frame reuses the previous frame's motion vectors). Decoding the
// compressed header and the tiles is the hardware's (Vulkan Video's).
package vp9

import (
	"errors"
	"fmt"

	"github.com/stubbedev/lys-lib/intconv"
	"github.com/stubbedev/lys-lib/internal/nal"
)

// ErrSyntax is a VP9 stream a decoder cannot read: malformed, or using
// syntax outside what it reads. Errors wrap it with what was read.
var ErrSyntax = errors.New("vp9: stream")

// The constants of the syntax (3).
const (
	NumRefFrames = 8
	RefsPerFrame = 3
	MaxSegments  = 8
	SegLvlMax    = 4
	// MaxRefFrames counts the reference names, intra included: the loop
	// filter's reference deltas are by them.
	MaxRefFrames = 4
	// LastFrame is the first reference name of an inter frame, the one
	// ref_frame_idx[0] names.
	LastFrame = 1

	minTileWidthB64 = 4
	maxTileWidthB64 = 64
	syncCode        = 0x498342
)

// FrameType is a frame's frame_type.
type FrameType uint8

// The frame types.
const (
	KeyFrame FrameType = iota
	NonKeyFrame
)

func (t FrameType) String() string {
	if t == KeyFrame {
		return "key frame"
	}
	return "non-key frame"
}

// ColorSpace is a color_space.
type ColorSpace uint8

// The colour spaces.
const (
	CSUnknown ColorSpace = iota
	CSBT601
	CSBT709
	CSSMPTE170
	CSSMPTE240
	CSBT2020
	CSReserved
	CSRGB
)

// ColorConfig is a color_config(), or what a frame that codes none
// takes from the frames before it.
type ColorConfig struct {
	BitDepth                   int
	ColorSpace                 ColorSpace
	FullRange                  bool
	SubsamplingX, SubsamplingY bool
}

// The interpolation filters, numbered as interp_filter is (and as
// StdVideoVP9InterpolationFilter numbers them).
const (
	EightTap uint8 = iota
	EightTapSmooth
	EightTapSharp
	Bilinear
	Switchable
)

// literalToType maps raw_interpolation_filter to interp_filter.
var literalToType = [4]uint8{EightTapSmooth, EightTap, EightTapSharp, Bilinear}

// LoopFilter is a loop_filter_params(), the deltas it leaves out kept
// from the frames before. UpdateRefDelta and UpdateModeDelta have bit
// i set for each delta i coded.
type LoopFilter struct {
	Level, Sharpness uint8
	DeltaEnabled     bool
	DeltaUpdate      bool
	UpdateRefDelta   uint8
	RefDeltas        [MaxRefFrames]int8
	UpdateModeDelta  uint8
	ModeDeltas       [2]int8
}

// Quantization is a quantization_params().
type Quantization struct {
	BaseQIdx                          uint8
	DeltaQYDc, DeltaQUVDc, DeltaQUVAc int8
}

// Segmentation is a segmentation_params(), the features it leaves out
// kept from the frames before. The probabilities are 255 where not
// coded.
type Segmentation struct {
	Enabled, UpdateMap, TemporalUpdate bool
	UpdateData, AbsOrDeltaUpdate       bool
	TreeProbs                          [7]uint8
	PredProbs                          [3]uint8
	FeatureEnabled                     [MaxSegments][SegLvlMax]bool
	FeatureData                        [MaxSegments][SegLvlMax]int16
}

// FrameHeader is an uncompressed_header() (6.2), its values as its
// semantics (7.2) and the decoding process define them.
type FrameHeader struct {
	Profile           uint8
	ShowExistingFrame bool
	FrameToShowMapIdx uint8

	FrameType          FrameType
	ShowFrame          bool
	ErrorResilientMode bool
	IntraOnly          bool
	ResetFrameContext  uint8
	Color              ColorConfig
	RefreshFrameFlags  uint8
	// RefFrameIdx is ref_frame_idx: the slot of each reference name
	// from LastFrame on. RefFrameSignBias is by reference name.
	RefFrameIdx      [RefsPerFrame]uint8
	RefFrameSignBias [MaxRefFrames]bool

	FrameWidth, FrameHeight     int
	RenderAndFrameSizeDifferent bool
	RenderWidth, RenderHeight   int

	AllowHighPrecisionMV      bool
	InterpolationFilter       uint8
	RefreshFrameContext       bool
	FrameParallelDecodingMode bool
	FrameContextIdx           uint8

	LoopFilter LoopFilter
	Quant      Quantization
	Seg        Segmentation

	TileColsLog2, TileRowsLog2 uint8

	// UsePrevFrameMVs is UsePrevFrameMvs: whether the frame predicts
	// motion vectors from the frame decoded before it.
	UsePrevFrameMVs bool

	// HeaderBytes is the uncompressed header's size, its trailing bits
	// included: where the compressed header starts. CompressedBytes is
	// the compressed header's, header_size_in_bytes; the tiles follow.
	HeaderBytes, CompressedBytes int
}

// FrameIsIntra is FrameIsIntra.
func (h *FrameHeader) FrameIsIntra() bool { return h.FrameType == KeyFrame || h.IntraOnly }

// refFrame is what the reference frame update process (8.10) saves of
// a frame in a slot, as far as later headers read it.
type refFrame struct {
	valid         bool
	width, height int
	color         ColorConfig
}

// Stream is the state a stream's frame headers are read with.
type Stream struct {
	ref [NumRefFrames]refFrame
	// color is the colour config the last intra frame coded.
	color      ColorConfig
	refDeltas  [MaxRefFrames]int8
	modeDeltas [2]int8
	seg        Segmentation
	// last is the frame decoded before, its size and whether it showed.
	last struct {
		valid         bool
		width, height int
		show          bool
	}
}

// Reset forgets the reference frames and the frame decoded before:
// what decoding from a key frame after a seek does.
func (s *Stream) Reset() { *s = Stream{} }

// ParseFrame reads the uncompressed header of b, a frame, into h with
// the stream's state, which it moves on as decoding the frame does.
func (s *Stream) ParseFrame(h *FrameHeader, b []byte) error {
	*h = FrameHeader{}
	r := nal.NewReader(b)
	if err := s.uncompressedHeader(h, &r); err != nil {
		return err
	}
	if r.Short() {
		return fmt.Errorf("%w: uncompressed header ends early, in %d bytes", ErrSyntax, len(b))
	}
	if h.ShowExistingFrame {
		return nil
	}
	h.HeaderBytes = (r.Pos() + 7) / 8
	if h.CompressedBytes == 0 {
		return fmt.Errorf("%w: the %s codes no compressed header", ErrSyntax, h.FrameType)
	}
	if end := h.HeaderBytes + h.CompressedBytes; end >= len(b) {
		return fmt.Errorf("%w: the %s's headers take %d of its %d bytes, leaving no tiles", ErrSyntax, h.FrameType, end, len(b))
	}
	s.update(h)
	return nil
}

func (s *Stream) uncompressedHeader(h *FrameHeader, r *nal.Reader) error {
	if marker := r.U(2); marker != 2 {
		return fmt.Errorf("%w: frame_marker %d", ErrSyntax, marker)
	}
	h.Profile = r.U8(1) | r.U8(1)<<1
	if h.Profile == 3 && r.Flag() {
		return fmt.Errorf("%w: profile 3 reserved_zero is 1", ErrSyntax)
	}
	if h.ShowExistingFrame = r.Flag(); h.ShowExistingFrame {
		h.FrameToShowMapIdx = r.U8(3)
		if !s.ref[h.FrameToShowMapIdx].valid {
			return fmt.Errorf("%w: a frame shows slot %d, which holds no frame", ErrSyntax, h.FrameToShowMapIdx)
		}
		return nil
	}
	h.FrameType = FrameType(r.U8(1))
	h.ShowFrame = r.Flag()
	h.ErrorResilientMode = r.Flag()
	if h.FrameType == KeyFrame {
		if err := syncCodeAt(r); err != nil {
			return err
		}
		if err := s.colorConfig(h, r); err != nil {
			return err
		}
		frameSize(h, r)
		renderSize(h, r)
		h.RefreshFrameFlags = 0xff
	} else {
		if !h.ShowFrame {
			h.IntraOnly = r.Flag()
		}
		if !h.ErrorResilientMode {
			h.ResetFrameContext = r.U8(2)
		}
		if h.IntraOnly {
			if err := syncCodeAt(r); err != nil {
				return err
			}
			if h.Profile > 0 {
				if err := s.colorConfig(h, r); err != nil {
					return err
				}
			} else {
				s.color.BitDepth, s.color.ColorSpace, s.color.SubsamplingX, s.color.SubsamplingY = 8, CSBT601, true, true
				h.Color = s.color
			}
			h.RefreshFrameFlags = r.U8(8)
			frameSize(h, r)
			renderSize(h, r)
		} else {
			if !s.last.valid {
				return fmt.Errorf("%w: a non-key frame before any key frame", ErrSyntax)
			}
			h.RefreshFrameFlags = r.U8(8)
			for i := range h.RefFrameIdx {
				h.RefFrameIdx[i] = r.U8(3)
				h.RefFrameSignBias[LastFrame+i] = r.Flag()
				if !s.ref[h.RefFrameIdx[i]].valid {
					return fmt.Errorf("%w: a non-key frame references slot %d, which holds no frame", ErrSyntax, h.RefFrameIdx[i])
				}
			}
			s.frameSizeWithRefs(h, r)
			h.AllowHighPrecisionMV = r.Flag()
			if r.Flag() {
				h.InterpolationFilter = Switchable
			} else {
				h.InterpolationFilter = literalToType[r.U(2)]
			}
			last := &s.last
			h.UsePrevFrameMVs = !h.ErrorResilientMode && last.show && last.width == h.FrameWidth && last.height == h.FrameHeight
		}
	}
	if !h.ErrorResilientMode {
		h.RefreshFrameContext = r.Flag()
		h.FrameParallelDecodingMode = r.Flag()
	} else {
		h.FrameParallelDecodingMode = true
	}
	h.FrameContextIdx = r.U8(2)
	if h.FrameIsIntra() || h.ErrorResilientMode {
		s.setupPastIndependence()
	}
	s.loopFilter(h, r)
	quantization(h, r)
	s.segmentation(h, r)
	tileInfo(h, r)
	h.CompressedBytes = int(r.U(16))
	return nil
}

func syncCodeAt(r *nal.Reader) error {
	if c := r.U(24); c != syncCode {
		return fmt.Errorf("%w: frame_sync_code %#06x", ErrSyntax, c)
	}
	return nil
}

func (s *Stream) colorConfig(h *FrameHeader, r *nal.Reader) error {
	c := ColorConfig{BitDepth: 8}
	if h.Profile >= 2 {
		c.BitDepth = 10
		if r.Flag() {
			c.BitDepth = 12
		}
	}
	c.ColorSpace = ColorSpace(r.U8(3))
	odd := h.Profile == 1 || h.Profile == 3
	if c.ColorSpace != CSRGB {
		c.FullRange = r.Flag()
		c.SubsamplingX, c.SubsamplingY = true, true
		if odd {
			c.SubsamplingX, c.SubsamplingY = r.Flag(), r.Flag()
		}
	} else {
		c.FullRange = true
		if !odd {
			return fmt.Errorf("%w: profile %d codes RGB, which only profiles 1 and 3 code", ErrSyntax, h.Profile)
		}
	}
	if odd && r.Flag() {
		return fmt.Errorf("%w: color_config reserved_zero is 1", ErrSyntax)
	}
	s.color, h.Color = c, c
	return nil
}

func frameSize(h *FrameHeader, r *nal.Reader) {
	h.FrameWidth = int(r.U(16)) + 1
	h.FrameHeight = int(r.U(16)) + 1
}

func renderSize(h *FrameHeader, r *nal.Reader) {
	h.RenderWidth, h.RenderHeight = h.FrameWidth, h.FrameHeight
	if h.RenderAndFrameSizeDifferent = r.Flag(); h.RenderAndFrameSizeDifferent {
		h.RenderWidth = int(r.U(16)) + 1
		h.RenderHeight = int(r.U(16)) + 1
	}
}

// frameSizeWithRefs reads frame_size_with_refs(): the size of the first
// reference found, else one coded. The frame's colour config is the
// reference's, as far as a reference has one.
func (s *Stream) frameSizeWithRefs(h *FrameHeader, r *nal.Reader) {
	h.Color = s.color
	found := false
	for _, idx := range h.RefFrameIdx {
		if found = r.Flag(); found {
			ref := &s.ref[idx]
			h.FrameWidth, h.FrameHeight = ref.width, ref.height
			h.Color.BitDepth, h.Color.SubsamplingX, h.Color.SubsamplingY = ref.color.BitDepth, ref.color.SubsamplingX, ref.color.SubsamplingY
			break
		}
	}
	if !found {
		frameSize(h, r)
	}
	renderSize(h, r)
}

// setupPastIndependence resets the loop filter deltas and segmentation
// features (setup_past_independence).
func (s *Stream) setupPastIndependence() {
	s.refDeltas = [MaxRefFrames]int8{1, 0, -1, -1}
	s.modeDeltas = [2]int8{}
	s.seg.FeatureEnabled = [MaxSegments][SegLvlMax]bool{}
	s.seg.FeatureData = [MaxSegments][SegLvlMax]int16{}
	s.seg.AbsOrDeltaUpdate = false
}

// su reads su(n): n bits of magnitude, then the sign.
func su(r *nal.Reader, n int) int8 {
	v := intconv.Must[int8](r.U(n))
	if r.Flag() {
		return -v
	}
	return v
}

func (s *Stream) loopFilter(h *FrameHeader, r *nal.Reader) {
	l := &h.LoopFilter
	l.Level = r.U8(6)
	l.Sharpness = r.U8(3)
	if l.DeltaEnabled = r.Flag(); l.DeltaEnabled {
		if l.DeltaUpdate = r.Flag(); l.DeltaUpdate {
			for i := range s.refDeltas {
				if r.Flag() {
					l.UpdateRefDelta |= 1 << i
					s.refDeltas[i] = su(r, 6)
				}
			}
			for i := range s.modeDeltas {
				if r.Flag() {
					l.UpdateModeDelta |= 1 << i
					s.modeDeltas[i] = su(r, 6)
				}
			}
		}
	}
	l.RefDeltas, l.ModeDeltas = s.refDeltas, s.modeDeltas
}

func readDeltaQ(r *nal.Reader) int8 {
	if r.Flag() {
		return su(r, 4)
	}
	return 0
}

func quantization(h *FrameHeader, r *nal.Reader) {
	q := &h.Quant
	q.BaseQIdx = r.U8(8)
	q.DeltaQYDc = readDeltaQ(r)
	q.DeltaQUVDc = readDeltaQ(r)
	q.DeltaQUVAc = readDeltaQ(r)
}

// The segmentation features' coding (6.2.11).
var (
	segFeatureBits   = [SegLvlMax]int{8, 6, 2, 0}
	segFeatureSigned = [SegLvlMax]bool{true, true, false, false}
)

func readProb(r *nal.Reader) uint8 {
	if r.Flag() {
		return r.U8(8)
	}
	return 255
}

func (s *Stream) segmentation(h *FrameHeader, r *nal.Reader) {
	g := &s.seg
	g.UpdateMap, g.TemporalUpdate, g.UpdateData = false, false, false
	if g.Enabled = r.Flag(); g.Enabled {
		if g.UpdateMap = r.Flag(); g.UpdateMap {
			for i := range g.TreeProbs {
				g.TreeProbs[i] = readProb(r)
			}
			g.TemporalUpdate = r.Flag()
			for i := range g.PredProbs {
				g.PredProbs[i] = 255
				if g.TemporalUpdate {
					g.PredProbs[i] = readProb(r)
				}
			}
		}
		if g.UpdateData = r.Flag(); g.UpdateData {
			g.AbsOrDeltaUpdate = r.Flag()
			for i := range MaxSegments {
				for j := range SegLvlMax {
					g.FeatureData[i][j] = 0
					if g.FeatureEnabled[i][j] = r.Flag(); !g.FeatureEnabled[i][j] {
						continue
					}
					v := intconv.Must[int16](r.U(segFeatureBits[j]))
					if segFeatureSigned[j] && r.Flag() {
						v = -v
					}
					g.FeatureData[i][j] = v
				}
			}
		}
	}
	h.Seg = *g
}

// tileInfo reads tile_info(), which bounds the tile columns by the
// frame's width in superblocks.
func tileInfo(h *FrameHeader, r *nal.Reader) {
	sb64Cols := ((h.FrameWidth+7)>>3 + 7) >> 3
	minLog2 := 0
	for maxTileWidthB64<<minLog2 < sb64Cols {
		minLog2++
	}
	maxLog2 := 1
	for sb64Cols>>maxLog2 >= minTileWidthB64 {
		maxLog2++
	}
	maxLog2--
	h.TileColsLog2 = uint8(minLog2)
	for int(h.TileColsLog2) < maxLog2 && r.Flag() {
		h.TileColsLog2++
	}
	if r.Flag() {
		h.TileRowsLog2 = 1 + r.U8(1)
	}
}

// update takes the frame h heads into the slots its refresh_frame_flags
// name (8.10) and notes it as the frame decoded before the next.
func (s *Stream) update(h *FrameHeader) {
	ref := refFrame{valid: true, width: h.FrameWidth, height: h.FrameHeight, color: h.Color}
	for i := range s.ref {
		if h.RefreshFrameFlags>>i&1 != 0 {
			s.ref[i] = ref
		}
	}
	s.last.valid, s.last.width, s.last.height, s.last.show = true, h.FrameWidth, h.FrameHeight, h.ShowFrame
}
