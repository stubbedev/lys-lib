package av1

import (
	"bufio"
	"bytes"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// traced is an OBU as ffmpeg's trace_headers bitstream filter prints
// it: its syntax elements by name, the bit position where its frame
// header ends, and the frame size line it prints after one.
type traced struct {
	typ       OBUType
	values    map[string]int64
	headerEnd int
	frame     string
}

// traceOBUs encodes a test pattern with args into an OBU stream and
// reads it back with ffmpeg's trace of its syntax.
func traceOBUs(t *testing.T, src string, args ...string) ([]byte, []traced) {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("no ffmpeg to make and trace the streams")
	}
	path := filepath.Join(t.TempDir(), "a.obu")
	enc := append([]string{"-v", "error", "-y", "-f", "lavfi", "-i", src, "-pix_fmt", "yuv420p"}, args...)
	run(t, append(enc, "-f", "obu", path)...)
	trace := run(t, "-v", "trace", "-i", path, "-c", "copy", "-bsf:v", "trace_headers", "-f", "null", "-")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b, parseTrace(t, trace)
}

func run(t *testing.T, args ...string) []byte {
	t.Helper()
	cmd := exec.Command("ffmpeg", args...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		if strings.Contains(out.String(), "Unknown encoder") {
			t.Skipf("ffmpeg lacks an encoder of %v", args)
		}
		t.Fatalf("ffmpeg %v: %v: %s", args, err, out.Bytes())
	}
	return out.Bytes()
}

// parseTrace reads trace_headers' lines from the first packet on.
func parseTrace(t *testing.T, trace []byte) []traced {
	t.Helper()
	var out []traced
	var cur *traced
	inPackets, inTiles := false, false
	sc := bufio.NewScanner(bytes.NewReader(trace))
	for sc.Scan() {
		line := sc.Text()
		at := strings.Index(line, "[trace_headers @ ")
		if at < 0 {
			continue
		}
		line = line[at:]
		line = line[strings.IndexByte(line, ']')+2:]
		switch {
		case strings.HasPrefix(line, "Packet:"):
			inPackets = true
			continue
		case !inPackets:
			continue
		case line == "OBU header":
			out = append(out, traced{values: map[string]int64{}})
			cur, inTiles = &out[len(out)-1], false
			continue
		case line == "Tile Group":
			inTiles = true
			continue
		case strings.HasPrefix(line, "Frame "):
			cur.frame = line[strings.IndexByte(line, ':')+1:]
			continue
		}
		f := strings.Fields(line)
		if len(f) != 5 || f[3] != "=" || cur == nil {
			continue
		}
		pos, err1 := strconv.Atoi(f[0])
		v, err2 := strconv.ParseInt(f[4], 10, 64)
		if err1 != nil || err2 != nil {
			t.Fatalf("trace line %q", line)
		}
		name := f[1]
		if name == "obu_type" {
			cur.typ = OBUType(v)
		}
		if inTiles || name == "zero_bit" || strings.HasPrefix(name, "trailing_") || strings.HasPrefix(name, "obu_") {
			continue
		}
		cur.values[name] = v
		cur.headerEnd = pos + len(f[2])
	}
	return out
}

func b2i(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

// syntax is h's syntax elements under trace_headers' names, as far as
// the parser keeps them as coded.
func syntax(h *FrameHeader) map[string]int64 {
	m := map[string]int64{
		"show_existing_frame": b2i(h.ShowExistingFrame), "frame_to_show_map_idx": int64(h.FrameToShowMapIdx),
		"frame_type": int64(h.FrameType), "show_frame": b2i(h.ShowFrame), "showable_frame": b2i(h.ShowableFrame),
		"error_resilient_mode": b2i(h.ErrorResilientMode), "disable_cdf_update": b2i(h.DisableCDFUpdate),
		"allow_screen_content_tools": b2i(h.AllowScreenContentTools), "frame_size_override_flag": b2i(h.FrameSizeOverride),
		"order_hint": int64(h.OrderHint), "primary_ref_frame": int64(h.PrimaryRefFrame), "refresh_frame_flags": int64(h.RefreshFrameFlags),
		"frame_refs_short_signaling": b2i(h.FrameRefsShortSignaling), "render_and_frame_size_different": b2i(h.RenderAndFrameSizeDifferent),
		"allow_intrabc": b2i(h.AllowIntrabc), "allow_high_precision_mv": b2i(h.AllowHighPrecisionMV),
		"is_filter_switchable": b2i(h.IsFilterSwitchable), "interpolation_filter": int64(h.InterpolationFilter),
		"is_motion_mode_switchable": b2i(h.IsMotionModeSwitchable), "use_ref_frame_mvs": b2i(h.UseRefFrameMVs),
		"disable_frame_end_update_cdf": b2i(h.DisableFrameEndUpdateCDF), "uniform_tile_spacing_flag": b2i(h.Tile.UniformSpacing),
		"context_update_tile_id": int64(h.Tile.ContextUpdateTileID), "tile_size_bytes_minus1": int64(h.Tile.TileSizeBytes - 1),
		"base_q_idx": int64(h.Quant.BaseQIdx), "using_qmatrix": b2i(h.Quant.UsingQmatrix),
		"delta_q_y_dc.delta_q": int64(h.Quant.DeltaQYDc), "delta_q_u_dc.delta_q": int64(h.Quant.DeltaQUDc), "delta_q_u_ac.delta_q": int64(h.Quant.DeltaQUAc),
		"qm_y": int64(h.Quant.QmY), "qm_u": int64(h.Quant.QmU), "segmentation_enabled": b2i(h.Seg.Enabled),
		"segmentation_update_map": b2i(h.Seg.UpdateMap), "segmentation_temporal_update": b2i(h.Seg.TemporalUpdate), "segmentation_update_data": b2i(h.Seg.UpdateData),
		"delta_q_present": b2i(h.DeltaQPresent), "delta_q_res": int64(h.DeltaQRes), "delta_lf_present": b2i(h.DeltaLFPresent),
		"delta_lf_res": int64(h.DeltaLFRes), "delta_lf_multi": b2i(h.DeltaLFMulti),
		"loop_filter_sharpness": int64(h.LoopFilter.Sharpness), "loop_filter_delta_enabled": b2i(h.LoopFilter.DeltaEnabled),
		"loop_filter_delta_update": b2i(h.LoopFilter.DeltaUpdate), "cdef_damping_minus_3": int64(h.CDEF.DampingMinus3), "cdef_bits": int64(h.CDEF.Bits),
		"tx_mode": int64(h.TxMode), "reference_select": b2i(h.ReferenceSelect), "skip_mode_present": b2i(h.SkipModePresent),
		"allow_warped_motion": b2i(h.AllowWarpedMotion), "reduced_tx_set": b2i(h.ReducedTxSet),
	}
	g := &h.FilmGrain
	maps.Copy(m, map[string]int64{
		"apply_grain": b2i(g.ApplyGrain), "grain_seed": int64(g.GrainSeed), "update_grain": b2i(g.UpdateGrain), "film_grain_params_ref_idx": int64(g.ParamsRefIdx),
		"num_y_points": int64(g.NumYPoints), "chroma_scaling_from_luma": b2i(g.ChromaScalingFromLuma), "num_cb_points": int64(g.NumCbPoints), "num_cr_points": int64(g.NumCrPoints),
		"grain_scaling_minus_8": int64(g.ScalingMinus8), "ar_coeff_lag": int64(g.ArCoeffLag), "ar_coeff_shift_minus_6": int64(g.ArCoeffShiftMinus6),
		"grain_scale_shift": int64(g.GrainScaleShift), "cb_mult": int64(g.CbMult), "cb_luma_mult": int64(g.CbLumaMult), "cb_offset": int64(g.CbOffset),
		"cr_mult": int64(g.CrMult), "cr_luma_mult": int64(g.CrLumaMult), "cr_offset": int64(g.CrOffset),
		"overlap_flag": b2i(g.OverlapFlag), "clip_to_restricted_range": b2i(g.ClipToRestrictedRange),
	})
	arrays := map[string][]uint8{
		"cdef_y_pri_strength": h.CDEF.YPri[:], "cdef_y_sec_strength": h.CDEF.YSec[:], "cdef_uv_pri_strength": h.CDEF.UVPri[:], "cdef_uv_sec_strength": h.CDEF.UVSec[:],
		"loop_filter_level": h.LoopFilter.Level[:], "ref_frame_idx": h.RefFrameIdx[:],
		"point_y_value": g.PointYValue[:], "point_y_scaling": g.PointYScaling[:], "point_cb_value": g.PointCbValue[:], "point_cb_scaling": g.PointCbScaling[:],
		"point_cr_value": g.PointCrValue[:], "point_cr_scaling": g.PointCrScaling[:],
		"ar_coeffs_y_plus_128": g.ArCoeffsYPlus128[:], "ar_coeffs_cb_plus_128": g.ArCoeffsCbPlus128[:], "ar_coeffs_cr_plus_128": g.ArCoeffsCrPlus128[:],
	}
	for name, a := range arrays {
		for i, v := range a {
			m[fmt.Sprintf("%s[%d]", name, i)] = int64(v)
		}
	}
	for i, v := range h.LoopFilter.RefDeltas {
		m[fmt.Sprintf("loop_filter_ref_deltas[%d]", i)] = int64(v)
	}
	for i, v := range h.LoopFilter.ModeDeltas {
		m[fmt.Sprintf("loop_filter_mode_deltas[%d]", i)] = int64(v)
	}
	for ref := LastFrame; ref <= AltrefFrame; ref++ {
		m[fmt.Sprintf("is_global[%d]", ref)] = b2i(h.GlobalMotion.Type[ref] != Identity)
	}
	for i, typ := range h.LR.Type {
		for coded, v := range remapLrType {
			if v == typ {
				m[fmt.Sprintf("lr_type[%d]", i)] = int64(coded)
			}
		}
	}
	return m
}

// checkStream parses every OBU of b, a stream ffmpeg traced, as a
// decoder does, and checks the values it reads against the trace's.
// It returns the frame headers.
func checkStream(t *testing.T, b []byte, trace []traced) []FrameHeader {
	t.Helper()
	var s Stream
	var headers []FrameHeader
	var h FrameHeader
	var tiles []Tile
	walk := NewOBUs(b)
	i := 0
	for u, ok := walk.Next(); ok; u, ok = walk.Next() {
		if i >= len(trace) {
			t.Fatalf("%d OBUs traced, more parsed", len(trace))
		}
		tr := trace[i]
		i++
		if u.Type != tr.typ {
			t.Fatalf("OBU %d is type %d, trace says %d", i-1, u.Type, tr.typ)
		}
		hdrLen := u.PayloadOffset - u.Offset
		switch u.Type {
		case OBUSequenceHeader:
			if err := s.Seq.Parse(u.Payload); err != nil {
				t.Fatal(err)
			}
			q := &s.Seq
			for name, v := range map[string]int64{
				"max_frame_width_minus_1": int64(q.MaxFrameWidth - 1), "max_frame_height_minus_1": int64(q.MaxFrameHeight - 1),
				"enable_order_hint": b2i(q.EnableOrderHint), "order_hint_bits_minus_1": int64(q.OrderHintBits - 1),
				"enable_cdef": b2i(q.EnableCDEF), "enable_restoration": b2i(q.EnableRestoration), "film_grain_params_present": b2i(q.FilmGrainPresent),
				"use_128x128_superblock": b2i(q.Use128x128Superblock), "enable_superres": b2i(q.EnableSuperres), "separate_uv_delta_q": b2i(q.Color.SeparateUVDeltaQ),
			} {
				if want, ok := tr.values[name]; ok && want != v {
					t.Errorf("sequence header %s = %d, trace says %d", name, v, want)
				}
			}
		case OBUFrameHeader, OBUFrame:
			showExisting, typ := s.Peek(u.Payload)
			if err := s.ParseFrameHeader(&h, &u); err != nil {
				t.Fatalf("OBU %d: %v", i-1, err)
			}
			if showExisting != h.ShowExistingFrame || !showExisting && typ != h.FrameType {
				t.Errorf("frame header %d peeks as showing an existing frame %v, %s; parses as %v, %s", len(headers), showExisting, typ, h.ShowExistingFrame, h.FrameType)
			}
			got := syntax(&h)
			for name, want := range tr.values {
				if v, ok := got[name]; ok && v != want {
					t.Errorf("frame header %d (order hint %d): %s = %d, trace says %d", len(headers), h.OrderHint, name, v, want)
				}
			}
			if end := (tr.headerEnd + 7) / 8; !h.ShowExistingFrame && end != hdrLen+h.HeaderBytes {
				t.Errorf("frame header %d ends at byte %d of its OBU, trace says %d", len(headers), hdrLen+h.HeaderBytes, end)
			}
			if !h.ShowExistingFrame {
				want := fmt.Sprintf("  size %dx%d  upscaled %d  render %dx%d  subsample 2x2  bitdepth 8  tiles %dx%d.",
					h.FrameWidth, h.FrameHeight, h.UpscaledWidth, h.RenderWidth, h.RenderHeight, h.Tile.Rows, h.Tile.Cols)
				if tr.frame != want {
					t.Errorf("frame header %d is %q, trace says %q", len(headers), want, tr.frame)
				}
			}
			headers = append(headers, h)
			tiles = tiles[:0]
			if h.ShowExistingFrame {
				s.Update(&h)
				continue
			}
			if u.Type == OBUFrame {
				tiles = checkTiles(t, &s, &h, u.Payload[h.HeaderBytes:], tiles)
			}
		case OBUTileGroup:
			tiles = checkTiles(t, &s, &h, u.Payload, tiles)
		}
	}
	if err := walk.Err(); err != nil {
		t.Fatal(err)
	}
	if i != len(trace) {
		t.Fatalf("parsed %d OBUs, trace has %d", i, len(trace))
	}
	return headers
}

// checkTiles reads a tile group and, at the frame's last tile, takes
// the frame into the reference slots.
func checkTiles(t *testing.T, s *Stream, h *FrameHeader, b []byte, tiles []Tile) []Tile {
	t.Helper()
	tiles, last, err := ParseTileGroup(h, b, tiles)
	if err != nil {
		t.Fatal(err)
	}
	for j, tile := range tiles[1:] {
		if prev := tiles[j]; tile.Offset < prev.Offset+prev.Size {
			t.Errorf("tile %d at %d overlaps tile %d at %d+%d", j+1, tile.Offset, j, prev.Offset, prev.Size)
		}
	}
	if last {
		if len(tiles) != h.Tile.Cols*h.Tile.Rows {
			t.Errorf("%d tiles, want %dx%d", len(tiles), h.Tile.Cols, h.Tile.Rows)
		}
		if end := tiles[len(tiles)-1]; end.Offset+end.Size != len(b) {
			t.Errorf("last tile ends at %d of the group's %d bytes", end.Offset+end.Size, len(b))
		}
		s.Update(h)
	}
	return tiles
}

// The parser reads what libaom and SVT-AV1 write as ffmpeg's own parser
// does, element for element: hidden frames shown later, tiles of each
// spacing, film grain, global motion, loop restoration, error
// resilience, screen content tools and 128x128 superblocks. Each
// stream has what it is made to test.
func TestParsesWhatEncodersWrite(t *testing.T) {
	src := func(size string, secs float64) string {
		return fmt.Sprintf("testsrc2=size=%s:rate=25:duration=%g", size, secs)
	}
	any := func(hs []FrameHeader, f func(h *FrameHeader) bool) bool {
		for i := range hs {
			if f(&hs[i]) {
				return true
			}
		}
		return false
	}
	for _, c := range []struct {
		name, src string
		args      []string
		has       map[string]func(h *FrameHeader) bool
	}{
		{
			"libaom, hidden frames, 2x2 tiles, film grain", src("256x144", 1.2),
			[]string{"-c:v", "libaom-av1", "-cpu-used", "6", "-g", "15", "-lag-in-frames", "16", "-tile-columns", "1", "-tile-rows", "1", "-denoise-noise-level", "8"},
			map[string]func(h *FrameHeader) bool{
				"a shown existing frame": func(h *FrameHeader) bool { return h.ShowExistingFrame },
				"2x2 tiles":              func(h *FrameHeader) bool { return h.Tile.Cols == 2 && h.Tile.Rows == 2 },
				"film grain":             func(h *FrameHeader) bool { return h.FilmGrain.ApplyGrain && h.FilmGrain.NumYPoints > 0 },
			},
		},
		{
			"libaom, global motion, restoration", src("256x144", 1.2) + ",scroll=h=0.004:v=0.002",
			[]string{"-c:v", "libaom-av1", "-cpu-used", "4", "-g", "20", "-aom-params", "enable-global-motion=1:enable-restoration=1"},
			map[string]func(h *FrameHeader) bool{
				"global motion": func(h *FrameHeader) bool {
					return slices.ContainsFunc(h.GlobalMotion.Type[:], func(t uint8) bool { return t != Identity })
				},
				"loop restoration": func(h *FrameHeader) bool { return h.LR.UsesLr },
			},
		},
		{
			"libaom, error resilient, screen content", src("192x128", 0.8),
			[]string{"-c:v", "libaom-av1", "-cpu-used", "8", "-error-resilience", "1", "-aom-params", "tune-content=screen"},
			map[string]func(h *FrameHeader) bool{
				"an error resilient inter frame": func(h *FrameHeader) bool { return h.ErrorResilientMode && h.FrameType == InterFrame },
				"screen content tools":           func(h *FrameHeader) bool { return h.AllowScreenContentTools },
			},
		},
		{
			"libaom, uneven tiles, 128x128 superblocks", src("400x200", 0.6),
			[]string{"-c:v", "libaom-av1", "-cpu-used", "8", "-aom-params", "sb-size=128:tile-width=1,2:tile-height=1"},
			map[string]func(h *FrameHeader) bool{
				"uneven 3x2 tiles": func(h *FrameHeader) bool {
					return !h.Tile.UniformSpacing && h.Tile.Cols == 3 && h.Tile.Rows == 2 && h.Tile.WidthInSbsMinus1[1] == 1
				},
			},
		},
		{
			"libaom, segmentation, delta q and loop filter", src("192x128", 1.2),
			[]string{"-c:v", "libaom-av1", "-cpu-used", "4", "-aq-mode", "1", "-aom-params", "deltaq-mode=3:delta-lf-mode=1"},
			map[string]func(h *FrameHeader) bool{
				"segment data loaded from the primary reference": func(h *FrameHeader) bool {
					return h.Seg.Enabled && !h.Seg.UpdateData && slices.Contains(h.Seg.FeatureEnabled[1][:], true)
				},
				"delta loop filter": func(h *FrameHeader) bool { return h.DeltaLFPresent },
			},
		},
		{
			"SVT-AV1", src("320x180", 1.2),
			[]string{"-c:v", "libsvtav1", "-preset", "10", "-g", "24", "-svtav1-params", "film-grain=4"},
			map[string]func(h *FrameHeader) bool{
				"a shown existing frame": func(h *FrameHeader) bool { return h.ShowExistingFrame },
				"film grain":             func(h *FrameHeader) bool { return h.FilmGrain.ApplyGrain },
				"compound references":    func(h *FrameHeader) bool { return h.ReferenceSelect },
			},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			b, trace := traceOBUs(t, c.src, c.args...)
			headers := checkStream(t, b, trace)
			for what, f := range c.has {
				if !any(headers, f) {
					t.Errorf("none of %d frame headers has %s", len(headers), what)
				}
			}
		})
	}
}
