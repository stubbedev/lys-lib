package nal

// HEVC is what an H.265 stream's video, sequence and picture parameter
// sets say, for the streams the encoder writes: Main profile, frames
// only, one temporal layer, one short-term reference picture set (the
// picture before), no reordering, BT.709 limited range.
type HEVC struct {
	// LevelIdc is the level times thirty.
	LevelIdc uint8
	// Width and Height are the coded size in luma samples.
	Width, Height int
	// ConfRight and ConfBottom crop the coded size back to the frame's,
	// in chroma samples.
	ConfRight, ConfBottom int
	// Log2MaxPocLsb is log2 of MaxPicOrderCntLsb.
	Log2MaxPocLsb int
	// Log2MinCb, Log2Ctb, Log2MinTb and Log2MaxTb are the coding block,
	// coding tree block and transform block sizes, as log2.
	Log2MinCb, Log2Ctb, Log2MinTb, Log2MaxTb int
	// TransformDepth is max_transform_hierarchy_depth, inter and intra.
	TransformDepth int
	// AMP, SAO and TemporalMVP are the SPS's tools of those names.
	AMP, SAO, TemporalMVP bool
	// InitQP is the picture's initial quantiser, 26 + init_qp_minus26.
	InitQP int
	// CuQPDelta is cu_qp_delta_enabled_flag, which rate control needs.
	CuQPDelta bool
}

// profileTierLevel writes profile_tier_level(1, 0) (7.3.3): Main,
// main tier, progressive frames.
func (h *HEVC) profileTierLevel(w *Writer) {
	w.U(2, 0)
	w.Flag(false)
	w.U(5, 1)
	w.U(32, 0x60000000)
	w.Flag(true)
	w.Flag(false)
	w.Flag(false)
	w.Flag(true)
	w.U(32, 0)
	w.U(12, 0)
	w.U(8, uint32(h.LevelIdc))
}

// subLayerOrdering writes the one temporal layer's DPB size: two
// pictures (the current and its reference), nothing reordered.
func subLayerOrdering(w *Writer) {
	w.Flag(true)
	w.UE(1)
	w.UE(0)
	w.UE(0)
}

// VPS is the video parameter set NAL unit (7.3.2.1).
func (h *HEVC) VPS() []byte {
	var w Writer
	w.U(4, 0)
	w.Flag(true)
	w.Flag(true)
	w.U(6, 0)
	w.U(3, 0)
	w.Flag(true)
	w.U(16, 0xffff)
	h.profileTierLevel(&w)
	subLayerOrdering(&w)
	w.U(6, 0)
	w.UE(0)
	w.Flag(false)
	w.Flag(false)
	w.Trailing()
	return w.Unit(32<<1, 1)
}

// SPS is the sequence parameter set NAL unit (7.3.2.2.1).
func (h *HEVC) SPS() []byte {
	var w Writer
	w.U(4, 0)
	w.U(3, 0)
	w.Flag(true)
	h.profileTierLevel(&w)
	w.UE(0)
	w.UE(1)
	w.UE(uint32(h.Width))
	w.UE(uint32(h.Height))
	conf := h.ConfRight != 0 || h.ConfBottom != 0
	w.Flag(conf)
	if conf {
		w.UE(0)
		w.UE(uint32(h.ConfRight))
		w.UE(0)
		w.UE(uint32(h.ConfBottom))
	}
	w.UE(0)
	w.UE(0)
	w.UE(uint32(h.Log2MaxPocLsb - 4))
	subLayerOrdering(&w)
	w.UE(uint32(h.Log2MinCb - 3))
	w.UE(uint32(h.Log2Ctb - h.Log2MinCb))
	w.UE(uint32(h.Log2MinTb - 2))
	w.UE(uint32(h.Log2MaxTb - h.Log2MinTb))
	w.UE(uint32(h.TransformDepth))
	w.UE(uint32(h.TransformDepth))
	w.Flag(false)
	w.Flag(h.AMP)
	w.Flag(h.SAO)
	w.Flag(false)
	w.UE(1)
	w.UE(1)
	w.UE(0)
	w.UE(0)
	w.Flag(true)
	w.Flag(false)
	w.Flag(h.TemporalMVP)
	w.Flag(false)
	w.Flag(true)
	h.vui(&w)
	w.Flag(false)
	w.Trailing()
	return w.Unit(33<<1, 1)
}

// vui writes the VUI (E.2.1): BT.709 limited range, and a bitstream
// restriction with the usual motion vector bounds.
func (h *HEVC) vui(w *Writer) {
	w.Flag(false)
	w.Flag(false)
	w.Flag(true)
	w.U(3, 5)
	w.Flag(false)
	w.Flag(true)
	w.U(8, 1)
	w.U(8, 1)
	w.U(8, 1)
	w.Flag(false)
	w.Flag(false)
	w.Flag(false)
	w.Flag(false)
	w.Flag(false)
	w.Flag(false)
	w.Flag(true)
	w.Flag(false)
	w.Flag(true)
	w.Flag(true)
	w.UE(0)
	w.UE(0)
	w.UE(0)
	w.UE(15)
	w.UE(15)
}

// SliceHeader is the slice segment header (7.3.6.1) of the picture
// sinceIDR frames into its key interval at quantiser qp: one IDR_W_RADL
// I slice, or one TRAIL_R slice predicting from the picture before
// through the sequence's short-term reference set, a P slice or, with
// gpb, a B slice with the same picture in both lists. It ends
// byte-aligned, where the slice data starts.
func (h *HEVC) SliceHeader(sinceIDR int, gpb bool, qp int) (unit []byte, bits int) {
	var w Writer
	idr := sinceIDR == 0
	w.Flag(true)
	if idr {
		w.Flag(false)
	}
	w.UE(0)
	switch {
	case idr:
		w.UE(2)
	case gpb:
		w.UE(0)
	default:
		w.UE(1)
	}
	if !idr {
		w.U(uint(h.Log2MaxPocLsb), uint32(sinceIDR%(1<<h.Log2MaxPocLsb)))
		w.Flag(true)
		if h.TemporalMVP {
			w.Flag(true)
		}
	}
	if h.SAO {
		w.Flag(true)
		w.Flag(true)
	}
	if !idr {
		w.Flag(false)
		if gpb {
			w.Flag(false)
		}
		if h.TemporalMVP && gpb {
			w.Flag(true)
		}
		w.UE(0)
	}
	w.SE(int32(qp - h.InitQP))
	w.Flag(false)
	w.Trailing()
	if idr {
		return w.Packed(19<<1, 1)
	}
	return w.Packed(1<<1, 1)
}

// PPS is the picture parameter set NAL unit (7.3.2.3.1).
func (h *HEVC) PPS() []byte {
	var w Writer
	w.UE(0)
	w.UE(0)
	w.Flag(false)
	w.Flag(false)
	w.U(3, 0)
	w.Flag(false)
	w.Flag(false)
	w.UE(0)
	w.UE(0)
	w.SE(int32(h.InitQP - 26))
	w.Flag(false)
	w.Flag(false)
	w.Flag(h.CuQPDelta)
	if h.CuQPDelta {
		w.UE(0)
	}
	w.SE(0)
	w.SE(0)
	w.Flag(false)
	w.Flag(false)
	w.Flag(false)
	w.Flag(false)
	w.Flag(false)
	w.Flag(false)
	w.Flag(true)
	w.Flag(false)
	w.Flag(false)
	w.Flag(false)
	w.UE(0)
	w.Flag(false)
	w.Flag(false)
	w.Trailing()
	return w.Unit(34<<1, 1)
}
