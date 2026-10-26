package nal

// H264 is what an H.264 stream's sequence and picture parameter sets
// say, for the streams the encoder writes: frames only, 4:2:0 at 8
// bits, picture order count type 2 (output order is decode order),
// no reordering, BT.709 limited range.
type H264 struct {
	// ProfileIdc is 77 (Main) or 100 (High).
	ProfileIdc uint8
	// LevelIdc is the level times ten.
	LevelIdc uint8
	// WidthMbs and HeightMbs are the coded size in macroblocks.
	WidthMbs, HeightMbs int
	// CropRight and CropBottom crop the coded size back to the
	// frame's, in pairs of pixels.
	CropRight, CropBottom int
	// Log2MaxFrameNum is log2 of MaxFrameNum.
	Log2MaxFrameNum int
	// MaxNumRefFrames is how many reference frames the stream keeps.
	MaxNumRefFrames int
	// CABAC is entropy_coding_mode_flag.
	CABAC bool
	// Transform8x8 is transform_8x8_mode_flag (High only).
	Transform8x8 bool
	// InitQP is the picture's initial quantiser, 26 + pic_init_qp_minus26.
	InitQP int
}

// highProfile reports whether the profile carries the chroma format
// and 8x8 transform syntax (High and the profiles above it).
func (h *H264) highProfile() bool { return h.ProfileIdc >= 100 }

// SPS is the sequence parameter set NAL unit (7.3.2.1.1).
func (h *H264) SPS() []byte {
	var w Writer
	w.U(8, uint32(h.ProfileIdc))
	w.U(8, 0)
	w.U(8, uint32(h.LevelIdc))
	w.UE(0)
	if h.highProfile() {
		w.UE(1)
		w.UE(0)
		w.UE(0)
		w.Flag(false)
		w.Flag(false)
	}
	w.UE(uint32(h.Log2MaxFrameNum - 4))
	w.UE(2)
	w.UE(uint32(h.MaxNumRefFrames))
	w.Flag(false)
	w.UE(uint32(h.WidthMbs - 1))
	w.UE(uint32(h.HeightMbs - 1))
	w.Flag(true)
	w.Flag(true)
	crop := h.CropRight != 0 || h.CropBottom != 0
	w.Flag(crop)
	if crop {
		w.UE(0)
		w.UE(uint32(h.CropRight))
		w.UE(0)
		w.UE(uint32(h.CropBottom))
	}
	w.Flag(true)
	h.vui(&w)
	w.Trailing()
	return w.Unit(0x67)
}

// vui writes the VUI (E.1.1): BT.709 limited range, and a bitstream
// restriction saying frames come out as they decode.
func (h *H264) vui(w *Writer) {
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
	w.Flag(true)
	w.Flag(true)
	w.UE(0)
	w.UE(0)
	w.UE(15)
	w.UE(15)
	w.UE(0)
	w.UE(uint32(h.MaxNumRefFrames))
}

// SliceHeader is the slice header (7.3.3) of the picture sinceIDR
// frames into its key interval, the idrPicID-th IDR when sinceIDR is
// 0, at quantiser qp: one I slice for the IDR, one P slice predicting
// from the picture before otherwise, the reference list and marking as
// the parameter sets imply. It is a Packed unit, stopping where the
// slice data starts.
func (h *H264) SliceHeader(sinceIDR, idrPicID, qp int) (unit []byte, bits int) {
	var w Writer
	idr := sinceIDR == 0
	w.UE(0)
	if idr {
		w.UE(7)
	} else {
		w.UE(5)
	}
	w.UE(0)
	w.U(uint(h.Log2MaxFrameNum), uint32(sinceIDR))
	if idr {
		w.UE(uint32(idrPicID))
		w.Flag(false)
		w.Flag(false)
	} else {
		w.Flag(false)
		w.Flag(false)
		w.Flag(false)
		if h.CABAC {
			w.UE(0)
		}
	}
	w.SE(int32(qp - h.InitQP))
	if idr {
		return w.Packed(0x65)
	}
	return w.Packed(0x21)
}

// PPS is the picture parameter set NAL unit (7.3.2.2).
func (h *H264) PPS() []byte {
	var w Writer
	w.UE(0)
	w.UE(0)
	w.Flag(h.CABAC)
	w.Flag(false)
	w.UE(0)
	w.UE(0)
	w.UE(0)
	w.Flag(false)
	w.U(2, 0)
	w.SE(int32(h.InitQP - 26))
	w.SE(0)
	w.SE(0)
	w.Flag(false)
	w.Flag(false)
	w.Flag(false)
	if h.Transform8x8 {
		w.Flag(true)
		w.Flag(false)
		w.SE(0)
	}
	w.Trailing()
	return w.Unit(0x68)
}
