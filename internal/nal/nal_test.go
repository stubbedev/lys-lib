package nal

import (
	"bytes"
	"math"
	"strconv"
	"strings"
	"testing"

	"github.com/stubbedev/lys-lib/internal/nal/naltest"
)

// Exp-Golomb codes come out as the spec's table 9-2 lists them.
func TestExpGolomb(t *testing.T) {
	for _, c := range []struct {
		ue   uint32
		bits string
	}{{0, "1"}, {1, "010"}, {2, "011"}, {3, "00100"}, {6, "00111"}, {7, "0001000"}, {255, "00000000100000000"}} {
		var w Writer
		w.UE(c.ue)
		if got := writtenBits(&w); got != c.bits {
			t.Errorf("ue(%d) = %s, want %s", c.ue, got, c.bits)
		}
	}
	for _, c := range []struct {
		se   int32
		bits string
	}{{0, "1"}, {1, "010"}, {-1, "011"}, {2, "00100"}, {-2, "00101"}} {
		var w Writer
		w.SE(c.se)
		if got := writtenBits(&w); got != c.bits {
			t.Errorf("se(%d) = %s, want %s", c.se, got, c.bits)
		}
	}
}

// writtenBits is what w holds, as 0s and 1s.
func writtenBits(w *Writer) string {
	var s strings.Builder
	for _, b := range w.buf {
		s.WriteString(strconv.FormatUint(uint64(b)|0x100, 2)[1:])
	}
	if w.bits > 0 {
		s.WriteString(strconv.FormatUint(w.cur&(1<<w.bits-1)|1<<w.bits, 2)[1:])
	}
	return s.String()
}

// A run of two zero bytes before a byte of 3 or less gets an
// emulation prevention byte, so no start code appears inside a unit.
func TestEmulationPrevention(t *testing.T) {
	var w Writer
	for _, b := range []byte{0, 0, 1, 0, 0, 0, 0, 0, 3, 0, 0, 4} {
		w.U(8, uint32(b))
	}
	got := w.Unit(0x67)
	want := []byte{0x67, 0, 0, 3, 1, 0, 0, 3, 0, 0, 3, 0, 3, 0, 0, 4}
	if !bytes.Equal(got, want) {
		t.Errorf("unit %x, want %x", got, want)
	}
}

// ffmpeg reads the SPS and PPS back with the values they were written
// with: High at level 4.1, a 333x241 frame coded as 21x16 macroblocks
// and cropped, BT.709 limited range, CABAC and the 8x8 transform.
func TestH264ParameterSets(t *testing.T) {
	h := H264{ProfileIdc: 100, LevelIdc: 41, WidthMbs: 21, HeightMbs: 16, CropRight: 1, CropBottom: 7, Log2MaxFrameNum: 6, MaxNumRefFrames: 1, CABAC: true, Transform8x8: true, InitQP: 20}
	fields := naltest.Trace(t, "h264", AnnexB(h.SPS(), h.PPS()))
	naltest.Want(t, fields, map[string]int{
		"profile_idc": 100, "level_idc": 41, "chroma_format_idc": 1, "log2_max_frame_num_minus4": 2, "pic_order_cnt_type": 2,
		"max_num_ref_frames": 1, "pic_width_in_mbs_minus1": 20, "pic_height_in_map_units_minus1": 15, "frame_mbs_only_flag": 1,
		"frame_cropping_flag": 1, "frame_crop_right_offset": 1, "frame_crop_bottom_offset": 7,
		"video_full_range_flag": 0, "colour_primaries": 1, "transfer_characteristics": 1, "matrix_coefficients": 1,
		"max_num_reorder_frames": 0, "max_dec_frame_buffering": 1,
		"entropy_coding_mode_flag": 1, "pic_init_qp_minus26": -6, "transform_8x8_mode_flag": 1,
	})
	main := H264{ProfileIdc: 77, LevelIdc: 41, WidthMbs: 40, HeightMbs: 23, Log2MaxFrameNum: 4, MaxNumRefFrames: 1, CABAC: true, InitQP: 26}
	fields = naltest.Trace(t, "h264", AnnexB(main.SPS(), main.PPS()))
	naltest.Want(t, fields, map[string]int{"profile_idc": 77, "frame_cropping_flag": 0, "pic_width_in_mbs_minus1": 39, "pic_init_qp_minus26": 0})
	if _, ok := fields["transform_8x8_mode_flag"]; ok {
		t.Error("a Main PPS carries transform_8x8_mode_flag")
	}
}

// ffmpeg reads the VPS, SPS and PPS back with the values they were
// written with: Main at level 4.1, a 333x241 frame coded 336x248 and
// cropped, 32x32 coding trees, BT.709 limited range, the one
// short-term reference set.
func TestHEVCParameterSets(t *testing.T) {
	h := HEVC{LevelIdc: 123, Width: 336, Height: 248, ConfRight: 1, ConfBottom: 3, Log2MaxPocLsb: 8, Log2MinCb: 3, Log2Ctb: 5, Log2MinTb: 2, Log2MaxTb: 5, TransformDepth: 2, AMP: true, SAO: true, InitQP: 30, CuQPDelta: true}
	fields := naltest.Trace(t, "hevc", AnnexB(h.VPS(), h.SPS(), h.PPS()))
	naltest.Want(t, fields, map[string]int{
		"general_profile_idc": 1, "general_level_idc": 123, "general_progressive_source_flag": 1, "general_frame_only_constraint_flag": 1,
		"vps_max_dec_pic_buffering_minus1[0]": 1, "sps_max_dec_pic_buffering_minus1[0]": 1, "sps_max_num_reorder_pics[0]": 0,
		"chroma_format_idc": 1, "pic_width_in_luma_samples": 336, "pic_height_in_luma_samples": 248,
		"conformance_window_flag": 1, "conf_win_right_offset": 1, "conf_win_bottom_offset": 3,
		"log2_max_pic_order_cnt_lsb_minus4": 4, "log2_min_luma_coding_block_size_minus3": 0, "log2_diff_max_min_luma_coding_block_size": 2,
		"log2_min_luma_transform_block_size_minus2": 0, "log2_diff_max_min_luma_transform_block_size": 3,
		"amp_enabled_flag": 1, "sample_adaptive_offset_enabled_flag": 1, "num_short_term_ref_pic_sets": 1,
		"num_negative_pics": 1, "num_positive_pics": 0, "used_by_curr_pic_s0_flag[0]": 1,
		"colour_primaries": 1, "transfer_characteristics": 1, "matrix_coefficients": 1, "video_full_range_flag": 0,
		"init_qp_minus26": 4, "cu_qp_delta_enabled_flag": 1, "pps_loop_filter_across_slices_enabled_flag": 1,
	})
}

// A packed unit pads its last byte with zeros and counts only the bits
// written, escaped as a whole unit is.
func TestPacked(t *testing.T) {
	var w Writer
	w.U(8, 0)
	w.U(8, 0)
	w.U(3, 0)
	unit, bits := w.Packed(0x65)
	if want := []byte{0x65, 0, 0, 3, 0}; !bytes.Equal(unit, want) || bits != 8*len(want)-5 {
		t.Errorf("packed %x of %d bits, want %x of %d", unit, bits, want, 8*len(want)-5)
	}
}

// asDriver finishes a packed slice header as a driver does before the
// slice data: cabac_alignment_one_bits to the byte, then a byte of data.
func asDriver(unit []byte, bits int) []byte {
	out := append([]byte(nil), unit...)
	if pad := len(out)*8 - bits; pad > 0 {
		out[len(out)-1] |= 1<<pad - 1
	}
	return append(out, 0x11, 0x80)
}

// ffmpeg reads the slice headers back behind the parameter sets as
// written: an IDR's I slice, then a P slice predicting from the picture
// before, frame_num counting from the IDR, the quantiser as an offset
// from the picture's initial one.
func TestH264SliceHeaders(t *testing.T) {
	h := H264{ProfileIdc: 100, LevelIdc: 41, WidthMbs: 40, HeightMbs: 23, CropBottom: 4, Log2MaxFrameNum: 4, MaxNumRefFrames: 1, CABAC: true, Transform8x8: true, InitQP: 26}
	idr := asDriver(h.SliceHeader(0, 3, 20))
	fields := naltest.Trace(t, "h264", AnnexB(h.SPS(), h.PPS(), idr))
	naltest.Want(t, fields, map[string]int{"nal_unit_type": 5, "slice_type": 7, "frame_num": 0, "idr_pic_id": 3, "no_output_of_prior_pics_flag": 0, "long_term_reference_flag": 0, "slice_qp_delta": -6})
	p := asDriver(h.SliceHeader(9, 0, 30))
	fields = naltest.Trace(t, "h264", AnnexB(h.SPS(), h.PPS(), idr, p))
	naltest.Want(t, fields, map[string]int{"nal_unit_type": 1, "slice_type": 5, "frame_num": 9, "num_ref_idx_active_override_flag": 0, "ref_pic_list_modification_flag_l0": 0, "adaptive_ref_pic_marking_mode_flag": 0, "cabac_init_idc": 0, "slice_qp_delta": 4})
}

func TestH264SliceHeaderCarriesTheLargestIdrPicID(t *testing.T) {
	h := H264{ProfileIdc: 100, LevelIdc: 41, WidthMbs: 40, HeightMbs: 23, CropBottom: 4, Log2MaxFrameNum: 4, MaxNumRefFrames: 1, CABAC: true, Transform8x8: true, InitQP: 26}
	idr := asDriver(h.SliceHeader(0, math.MaxUint16, 26))
	fields := naltest.Trace(t, "h264", AnnexB(h.SPS(), h.PPS(), idr))
	naltest.Want(t, fields, map[string]int{"nal_unit_type": 5, "idr_pic_id": math.MaxUint16})
}

// ffmpeg reads the slice segment headers back behind the parameter sets
// as written: the IDR's I slice, a P slice, and a B slice predicting
// from the same picture in both lists, each naming the sequence's
// short-term reference set and its picture order count.
func TestHEVCSliceHeaders(t *testing.T) {
	h := HEVC{LevelIdc: 93, Width: 640, Height: 360, Log2MaxPocLsb: 8, Log2MinCb: 3, Log2Ctb: 6, Log2MinTb: 2, Log2MaxTb: 5, TransformDepth: 2, SAO: true, InitQP: 20}
	sets := [][]byte{h.VPS(), h.SPS(), h.PPS()}
	idr, bits := h.SliceHeader(0, false, 20)
	if bits != 8*len(idr) {
		t.Errorf("an H.265 slice header of %d bytes is %d bits, want it byte-aligned", len(idr), bits)
	}
	idr = append(idr, 0x11, 0x80)
	fields := naltest.Trace(t, "hevc", AnnexB(append(sets, idr)...))
	naltest.Want(t, fields, map[string]int{"nal_unit_type": 19, "first_slice_segment_in_pic_flag": 1, "slice_type": 2, "slice_sao_luma_flag": 1, "slice_sao_chroma_flag": 1, "slice_qp_delta": 0})
	for _, c := range []struct {
		gpb  bool
		kind int
	}{{false, 1}, {true, 0}} {
		p, _ := h.SliceHeader(5, c.gpb, 23)
		p = append(p, 0x11, 0x80)
		fields = naltest.Trace(t, "hevc", AnnexB(append(sets, idr, p)...))
		naltest.Want(t, fields, map[string]int{"nal_unit_type": 1, "slice_type": c.kind, "slice_pic_order_cnt_lsb": 5, "short_term_ref_pic_set_sps_flag": 1, "num_ref_idx_active_override_flag": 0, "five_minus_max_num_merge_cand": 0, "slice_qp_delta": 3})
	}
}
