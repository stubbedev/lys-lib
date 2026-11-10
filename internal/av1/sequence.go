package av1

import "fmt"

// The values the syntax selects the frame header's choice with (6.4).
const (
	// SelectScreenContentTools and SelectIntegerMV are
	// seq_force_screen_content_tools and seq_force_integer_mv leaving
	// the choice to each frame.
	SelectScreenContentTools = 2
	SelectIntegerMV          = 2
)

// MaxOperatingPoints are the operating points a sequence header can
// describe.
const MaxOperatingPoints = 32

// SequenceHeader is a sequence_header_obu (5.5), its values as its
// semantics (6.4) define them.
type SequenceHeader struct {
	Profile                   uint8
	StillPicture              bool
	ReducedStillPictureHeader bool

	TimingInfoPresent bool
	Timing            TimingInfo

	DecoderModelInfoPresent bool
	DecoderModel            DecoderModelInfo

	InitialDisplayDelayPresent bool
	OperatingPoints            int
	OperatingPointIdc          [MaxOperatingPoints]uint16
	LevelIdx                   [MaxOperatingPoints]uint8
	Tier                       [MaxOperatingPoints]bool
	DecoderModelPresentForOp   [MaxOperatingPoints]bool

	FrameWidthBits, FrameHeightBits int
	MaxFrameWidth, MaxFrameHeight   int

	FrameIDNumbersPresent         bool
	DeltaFrameIDLengthMinus2      uint8
	AdditionalFrameIDLengthMinus1 uint8

	Use128x128Superblock     bool
	EnableFilterIntra        bool
	EnableIntraEdgeFilter    bool
	EnableInterintraCompound bool
	EnableMaskedCompound     bool
	EnableWarpedMotion       bool
	EnableDualFilter         bool
	EnableOrderHint          bool
	EnableJntComp            bool
	EnableRefFrameMVs        bool
	// ForceScreenContentTools and ForceIntegerMV are
	// seq_force_screen_content_tools and seq_force_integer_mv: 0, 1 or
	// the Select value.
	ForceScreenContentTools uint8
	ForceIntegerMV          uint8
	// OrderHintBits is 0 without order hints.
	OrderHintBits int

	EnableSuperres    bool
	EnableCDEF        bool
	EnableRestoration bool
	Color             ColorConfig
	FilmGrainPresent  bool
}

// TimingInfo is a timing_info().
type TimingInfo struct {
	NumUnitsInDisplayTick    uint32
	TimeScale                uint32
	EqualPictureInterval     bool
	NumTicksPerPictureMinus1 uint32
}

// DecoderModelInfo is a decoder_model_info(): the lengths of the
// fields frame headers code their times in.
type DecoderModelInfo struct {
	BufferDelayLengthMinus1           uint8
	NumUnitsInDecodingTick            uint32
	BufferRemovalTimeLengthMinus1     uint8
	FramePresentationTimeLengthMinus1 uint8
}

// The colour description values the syntax names (6.4.2).
const (
	ColorPrimariesBT709           = 1
	ColorUnspecified              = 2
	TransferSRGB                  = 13
	MatrixIdentity                = 0
	ChromaSamplePositionUnknown   = 0
	ChromaSamplePositionVertical  = 1
	ChromaSamplePositionColocated = 2
)

// ColorConfig is a color_config().
type ColorConfig struct {
	BitDepth                   int
	MonoChrome                 bool
	ColorDescriptionPresent    bool
	ColorPrimaries             uint8
	TransferCharacteristics    uint8
	MatrixCoefficients         uint8
	ColorRange                 bool
	SubsamplingX, SubsamplingY bool
	ChromaSamplePosition       uint8
	SeparateUVDeltaQ           bool
}

// NumPlanes is how many planes the frames have.
func (c *ColorConfig) NumPlanes() int {
	if c.MonoChrome {
		return 1
	}
	return 3
}

// Parse reads a sequence header OBU's payload.
func (s *SequenceHeader) Parse(b []byte) error {
	*s = SequenceHeader{}
	r := newReader(b)
	s.Profile = r.f8(3)
	if s.Profile > 2 {
		return fmt.Errorf("%w: seq_profile %d", ErrSyntax, s.Profile)
	}
	s.StillPicture = r.Flag()
	s.ReducedStillPictureHeader = r.Flag()
	if s.ReducedStillPictureHeader {
		s.OperatingPoints = 1
		s.LevelIdx[0] = r.f8(5)
	} else {
		s.parseOperatingPoints(&r)
	}
	s.FrameWidthBits, s.FrameHeightBits = int(r.U(4))+1, int(r.U(4))+1
	s.MaxFrameWidth, s.MaxFrameHeight = int(r.U(s.FrameWidthBits))+1, int(r.U(s.FrameHeightBits))+1
	if !s.ReducedStillPictureHeader {
		s.FrameIDNumbersPresent = r.Flag()
	}
	if s.FrameIDNumbersPresent {
		s.DeltaFrameIDLengthMinus2 = r.f8(4)
		s.AdditionalFrameIDLengthMinus1 = r.f8(3)
	}
	s.Use128x128Superblock = r.Flag()
	s.EnableFilterIntra = r.Flag()
	s.EnableIntraEdgeFilter = r.Flag()
	s.ForceScreenContentTools, s.ForceIntegerMV = SelectScreenContentTools, SelectIntegerMV
	if !s.ReducedStillPictureHeader {
		s.EnableInterintraCompound = r.Flag()
		s.EnableMaskedCompound = r.Flag()
		s.EnableWarpedMotion = r.Flag()
		s.EnableDualFilter = r.Flag()
		s.EnableOrderHint = r.Flag()
		if s.EnableOrderHint {
			s.EnableJntComp = r.Flag()
			s.EnableRefFrameMVs = r.Flag()
		}
		if !r.Flag() {
			s.ForceScreenContentTools = r.f8(1)
		}
		if s.ForceScreenContentTools > 0 {
			if !r.Flag() {
				s.ForceIntegerMV = r.f8(1)
			}
		}
		if s.EnableOrderHint {
			s.OrderHintBits = int(r.U(3)) + 1
		}
	}
	s.EnableSuperres = r.Flag()
	s.EnableCDEF = r.Flag()
	s.EnableRestoration = r.Flag()
	s.parseColorConfig(&r)
	s.FilmGrainPresent = r.Flag()
	if r.Short() {
		return fmt.Errorf("%w: sequence header ends early, in %d bytes", ErrSyntax, len(b))
	}
	return nil
}

func (s *SequenceHeader) parseOperatingPoints(r *reader) {
	if s.TimingInfoPresent = r.Flag(); s.TimingInfoPresent {
		t := &s.Timing
		t.NumUnitsInDisplayTick, t.TimeScale = r.U(32), r.U(32)
		if t.EqualPictureInterval = r.Flag(); t.EqualPictureInterval {
			t.NumTicksPerPictureMinus1 = r.uvlc()
		}
		if s.DecoderModelInfoPresent = r.Flag(); s.DecoderModelInfoPresent {
			m := &s.DecoderModel
			m.BufferDelayLengthMinus1 = r.f8(5)
			m.NumUnitsInDecodingTick = r.U(32)
			m.BufferRemovalTimeLengthMinus1 = r.f8(5)
			m.FramePresentationTimeLengthMinus1 = r.f8(5)
		}
	}
	s.InitialDisplayDelayPresent = r.Flag()
	s.OperatingPoints = int(r.U(5)) + 1
	for i := range s.OperatingPoints {
		s.OperatingPointIdc[i] = r.U16(12)
		s.LevelIdx[i] = r.f8(5)
		if s.LevelIdx[i] > 7 {
			s.Tier[i] = r.Flag()
		}
		if s.DecoderModelInfoPresent {
			if s.DecoderModelPresentForOp[i] = r.Flag(); s.DecoderModelPresentForOp[i] {
				n := int(s.DecoderModel.BufferDelayLengthMinus1) + 1
				r.Skip(2*n + 1)
			}
		}
		if s.InitialDisplayDelayPresent && r.Flag() {
			r.Skip(4)
		}
	}
}

func (s *SequenceHeader) parseColorConfig(r *reader) {
	c := &s.Color
	high := r.Flag()
	switch {
	case s.Profile == 2 && high && r.Flag():
		c.BitDepth = 12
	case high:
		c.BitDepth = 10
	default:
		c.BitDepth = 8
	}
	if s.Profile != 1 {
		c.MonoChrome = r.Flag()
	}
	c.ColorPrimaries, c.TransferCharacteristics, c.MatrixCoefficients = ColorUnspecified, ColorUnspecified, ColorUnspecified
	if c.ColorDescriptionPresent = r.Flag(); c.ColorDescriptionPresent {
		c.ColorPrimaries, c.TransferCharacteristics, c.MatrixCoefficients = r.f8(8), r.f8(8), r.f8(8)
	}
	switch {
	case c.MonoChrome:
		c.ColorRange = r.Flag()
		c.SubsamplingX, c.SubsamplingY = true, true
		return
	case c.ColorPrimaries == ColorPrimariesBT709 && c.TransferCharacteristics == TransferSRGB && c.MatrixCoefficients == MatrixIdentity:
		c.ColorRange = true
	default:
		c.ColorRange = r.Flag()
		switch s.Profile {
		case 0:
			c.SubsamplingX, c.SubsamplingY = true, true
		case 1:
		default:
			if c.BitDepth == 12 {
				if c.SubsamplingX = r.Flag(); c.SubsamplingX {
					c.SubsamplingY = r.Flag()
				}
			} else {
				c.SubsamplingX = true
			}
		}
		if c.SubsamplingX && c.SubsamplingY {
			c.ChromaSamplePosition = r.f8(2)
		}
	}
	c.SeparateUVDeltaQ = r.Flag()
}

// IDLen is idLen, the bits of a frame ID.
func (s *SequenceHeader) IDLen() int {
	return int(s.AdditionalFrameIDLengthMinus1) + int(s.DeltaFrameIDLengthMinus2) + 3
}
