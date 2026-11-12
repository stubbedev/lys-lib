package vp9

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// tracedPacket is a packet as ffmpeg's trace_headers bitstream filter
// prints it: its superframe index's frame sizes, when it has one, and
// its frames.
type tracedPacket struct {
	sizes  []int
	frames []tracedFrame
}

// tracedFrame is a frame's syntax elements by name, the bit where its
// uncompressed header's elements end and the frame size line printed
// after them.
type tracedFrame struct {
	values    map[string]int64
	headerEnd int
	frame     string
}

// traceIVF encodes a test pattern with each argument list in turn (a
// two-pass encode's passes; the last writes the stream) into an IVF
// file, and reads back its packets and ffmpeg's trace of their syntax.
func traceIVF(t *testing.T, src string, passes ...[]string) ([][]byte, []tracedPacket) {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("no ffmpeg to make and trace the streams")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "a.ivf")
	for i, args := range passes {
		enc := append([]string{"-v", "error", "-y", "-f", "lavfi", "-i", src, "-c:v", "libvpx-vp9", "-passlogfile", filepath.Join(dir, "pass")}, args...)
		if i < len(passes)-1 {
			enc = append(enc, "-f", "null", "-")
		} else {
			enc = append(enc, "-f", "ivf", path)
		}
		run(t, enc...)
	}
	trace := run(t, "-v", "trace", "-i", path, "-c", "copy", "-bsf:v", "trace_headers", "-f", "null", "-")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return readIVF(t, b), parseTrace(t, trace)
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

// readIVF is the frames of an IVF file.
func readIVF(t *testing.T, b []byte) [][]byte {
	t.Helper()
	if len(b) < 32 || string(b[:4]) != "DKIF" {
		t.Fatalf("not an IVF file: % x", b[:min(len(b), 32)])
	}
	b = b[binary.LittleEndian.Uint16(b[6:]):]
	var out [][]byte
	for len(b) >= 12 {
		n := int(binary.LittleEndian.Uint32(b))
		if 12+n > len(b) {
			t.Fatalf("IVF frame of %d bytes in %d", n, len(b)-12)
		}
		out, b = append(out, b[12:12+n]), b[12+n:]
	}
	return out
}

// parseTrace reads trace_headers' lines from the first packet on.
func parseTrace(t *testing.T, trace []byte) []tracedPacket {
	t.Helper()
	var out []tracedPacket
	var frame *tracedFrame
	inIndex := false
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
			out = append(out, tracedPacket{})
			frame, inIndex = nil, false
			continue
		case len(out) == 0:
			continue
		case line == "Superframe Index":
			inIndex = true
			continue
		case line == "Frame":
			p := &out[len(out)-1]
			p.frames = append(p.frames, tracedFrame{values: map[string]int64{}})
			frame, inIndex = &p.frames[len(p.frames)-1], false
			continue
		case strings.HasPrefix(line, "Frame:"):
			frame.frame = strings.Join(strings.Fields(line[len("Frame:"):]), " ")
			continue
		}
		f := strings.Fields(line)
		if len(f) == 4 {
			f = []string{f[0], f[1], "", f[2], f[3]}
		}
		if len(f) != 5 || f[3] != "=" {
			continue
		}
		pos, err1 := strconv.Atoi(f[0])
		v, err2 := strconv.ParseInt(f[4], 10, 64)
		if err1 != nil || err2 != nil {
			t.Fatalf("trace line %q", line)
		}
		name := f[1]
		if inIndex {
			if strings.HasPrefix(name, "frame_sizes[") {
				out[len(out)-1].sizes = append(out[len(out)-1].sizes, int(v))
			}
			continue
		}
		if frame == nil || name == "zero_bit" {
			continue
		}
		frame.values[name] = v
		frame.headerEnd = pos + len(f[2])
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
	context := h.FrameContextIdx
	if h.FrameIsIntra() || h.ErrorResilientMode {
		context = h.ResetContextIdx
	}
	m := map[string]int64{
		"show_existing_frame": b2i(h.ShowExistingFrame), "frame_to_show_map_idx": int64(h.FrameToShowMapIdx),
		"profile_low_bit": int64(h.Profile & 1), "profile_high_bit": int64(h.Profile >> 1),
		"frame_type": int64(h.FrameType), "show_frame": b2i(h.ShowFrame), "error_resilient_mode": b2i(h.ErrorResilientMode),
		"intra_only": b2i(h.IntraOnly), "reset_frame_context": int64(h.ResetFrameContext),
		"ten_or_twelve_bit": b2i(h.Color.BitDepth == 12), "color_space": int64(h.Color.ColorSpace), "color_range": b2i(h.Color.FullRange),
		"refresh_frame_flags": int64(h.RefreshFrameFlags), "frame_width_minus_1": int64(h.FrameWidth - 1), "frame_height_minus_1": int64(h.FrameHeight - 1),
		"render_and_frame_size_different": b2i(h.RenderAndFrameSizeDifferent),
		"render_width_minus_1":            int64(h.RenderWidth - 1), "render_height_minus_1": int64(h.RenderHeight - 1),
		"allow_high_precision_mv": b2i(h.AllowHighPrecisionMV), "is_filter_switchable": b2i(h.InterpolationFilter == Switchable),
		"refresh_frame_context": b2i(h.RefreshFrameContext), "frame_parallel_decoding_mode": b2i(h.FrameParallelDecodingMode),
		"frame_context_idx": int64(context),
		"loop_filter_level": int64(h.LoopFilter.Level), "loop_filter_sharpness": int64(h.LoopFilter.Sharpness),
		"loop_filter_delta_enabled": b2i(h.LoopFilter.DeltaEnabled), "loop_filter_delta_update": b2i(h.LoopFilter.DeltaUpdate),
		"base_q_idx": int64(h.Quant.BaseQIdx), "delta_q_y_dc.delta_q": int64(h.Quant.DeltaQYDc),
		"delta_q_uv_dc.delta_q": int64(h.Quant.DeltaQUVDc), "delta_q_uv_ac.delta_q": int64(h.Quant.DeltaQUVAc),
		"segmentation_enabled": b2i(h.Seg.Enabled), "segmentation_update_map": b2i(h.Seg.UpdateMap),
		"segmentation_temporal_update": b2i(h.Seg.TemporalUpdate), "segmentation_update_data": b2i(h.Seg.UpdateData),
		"segmentation_abs_or_delta_update": b2i(h.Seg.AbsOrDeltaUpdate),
		"tile_cols_log2":                   int64(h.TileColsLog2), "tile_rows_log2": int64(h.TileRowsLog2),
		"header_size_in_bytes": int64(h.CompressedBytes),
	}
	if h.InterpolationFilter != Switchable {
		m["raw_interpolation_filter_type"] = int64(slices.Index(literalToType[:], h.InterpolationFilter))
	}
	for i, idx := range h.RefFrameIdx {
		m[fmt.Sprintf("ref_frame_idx[%d]", i)] = int64(idx)
		m[fmt.Sprintf("ref_frame_sign_bias[%d]", LastFrame+i)] = b2i(h.RefFrameSignBias[LastFrame+i])
	}
	l := &h.LoopFilter
	for i, v := range l.RefDeltas {
		m[fmt.Sprintf("update_ref_delta[%d]", i)] = int64(l.UpdateRefDelta >> i & 1)
		m[fmt.Sprintf("loop_filter_ref_deltas[%d]", i)] = int64(v)
	}
	for i, v := range l.ModeDeltas {
		m[fmt.Sprintf("update_mode_delta[%d]", i)] = int64(l.UpdateModeDelta >> i & 1)
		m[fmt.Sprintf("loop_filter_mode_deltas[%d]", i)] = int64(v)
	}
	g := &h.Seg
	for i, p := range g.TreeProbs {
		m[fmt.Sprintf("segmentation_tree_probs[%d].prob", i)] = int64(p)
	}
	for i, p := range g.PredProbs {
		m[fmt.Sprintf("segmentation_pred_prob[%d].prob", i)] = int64(p)
	}
	for i := range MaxSegments {
		for j := range SegLvlMax {
			v := g.FeatureData[i][j]
			m[fmt.Sprintf("feature_enabled[%d][%d]", i, j)] = b2i(g.FeatureEnabled[i][j])
			m[fmt.Sprintf("feature_value[%d][%d]", i, j)] = int64(max(v, -v))
			m[fmt.Sprintf("feature_sign[%d][%d]", i, j)] = b2i(v < 0)
		}
	}
	return m
}

// checkStream parses every frame of pkts, a stream ffmpeg traced, as a
// decoder does, and checks the values it reads against the trace's. It
// returns the frame headers and how many packets were superframes.
func checkStream(t *testing.T, pkts [][]byte, trace []tracedPacket) ([]FrameHeader, int) {
	t.Helper()
	if len(pkts) != len(trace) {
		t.Fatalf("%d packets, %d traced", len(pkts), len(trace))
	}
	var s Stream
	var headers []FrameHeader
	var frames [MaxFrames][]byte
	supers := 0
	for i, p := range pkts {
		tr := trace[i]
		fs, err := Frames(p, &frames)
		if err != nil {
			t.Fatalf("packet %d: %v", i, err)
		}
		if tr.sizes != nil {
			supers++
			sizes := make([]int, len(fs))
			for j, f := range fs {
				sizes[j] = len(f)
			}
			if !slices.Equal(sizes, tr.sizes) {
				t.Errorf("packet %d splits into frames of %v bytes, trace says %v", i, sizes, tr.sizes)
			}
		}
		if len(fs) != len(tr.frames) {
			t.Fatalf("packet %d has %d frames, trace says %d", i, len(fs), len(tr.frames))
		}
		for j, f := range fs {
			var h, again FrameHeader
			before := s
			if err := s.ParseFrame(&h, f); err != nil {
				t.Fatalf("packet %d frame %d: %v", i, j, err)
			}
			if s != before {
				t.Fatalf("packet %d frame %d: parsing moved the stream state before Update", i, j)
			}
			if err := s.ParseFrame(&again, f); err != nil || again != h {
				t.Fatalf("packet %d frame %d: parsed again before Update, reads %+v (%v), want %+v", i, j, again, err, h)
			}
			s.Update(&h)
			ft := tr.frames[j]
			got := syntax(&h)
			for name, want := range ft.values {
				if v, ok := got[name]; ok && v != want {
					t.Errorf("packet %d frame %d: %s = %d, trace says %d", i, j, name, v, want)
				}
			}
			if h.ShowExistingFrame {
				headers = append(headers, h)
				continue
			}
			if end := (ft.headerEnd + 7) / 8; end != h.HeaderBytes {
				t.Errorf("packet %d frame %d: uncompressed header ends at byte %d, trace says %d", i, j, h.HeaderBytes, end)
			}
			sub := func(b bool) int { return int(b2i(b)) }
			want := fmt.Sprintf("size %dx%d subsample %dx%d bit_depth %d tiles %dx%d.", h.FrameWidth, h.FrameHeight,
				sub(h.Color.SubsamplingX), sub(h.Color.SubsamplingY), h.Color.BitDepth, 1<<h.TileColsLog2, 1<<h.TileRowsLog2)
			if ft.frame != want {
				t.Errorf("packet %d frame %d is %q, trace says %q", i, j, want, ft.frame)
			}
			headers = append(headers, h)
		}
	}
	return headers, supers
}

// The parser reads what libvpx writes as ffmpeg's own parser does,
// element for element: hidden alternate reference frames in superframes,
// segmentation with temporal prediction, tile columns, error resilient frames,
// odd sizes, lossless frames and 10-bit profile 2. Each stream has what
// it is made to test.
func TestParsesWhatLibvpxWrites(t *testing.T) {
	src := func(w, h int, secs float64) string {
		return fmt.Sprintf("testsrc2=size=%dx%d:rate=25:duration=%g,crop=%d:%d:0:0:exact=1", w+w%2, h+h%2, secs, w, h)
	}
	any := func(hs []FrameHeader, f func(h *FrameHeader) bool) bool {
		return slices.ContainsFunc(hs, func(h FrameHeader) bool { return f(&h) })
	}
	twoPass := func(args ...string) [][]string {
		return [][]string{append([]string{"-pass", "1"}, args...), append([]string{"-pass", "2"}, args...)}
	}
	for _, c := range []struct {
		name, src string
		passes    [][]string
		supers    bool
		has       map[string]func(h *FrameHeader) bool
	}{
		{
			"two-pass, hidden alternate references", src(352, 288, 2),
			twoPass("-pix_fmt", "yuv420p", "-b:v", "300k", "-g", "25", "-auto-alt-ref", "1", "-lag-in-frames", "16"),
			true,
			map[string]func(h *FrameHeader) bool{
				"a hidden frame":       func(h *FrameHeader) bool { return !h.ShowFrame && !h.ShowExistingFrame },
				"a sign biased golden": func(h *FrameHeader) bool { return h.RefFrameSignBias[3] },
			},
		},
		{
			"realtime, cyclic refresh, tiles, error resilient", src(640, 360, 0.6),
			[][]string{{"-pix_fmt", "yuv420p", "-deadline", "realtime", "-cpu-used", "8", "-aq-mode", "3", "-tile-columns", "2", "-error-resilient", "1"}},
			false,
			map[string]func(h *FrameHeader) bool{
				"temporally predicted segmentation": func(h *FrameHeader) bool { return h.Seg.UpdateMap && h.Seg.TemporalUpdate },
				"segment features": func(h *FrameHeader) bool {
					return h.Seg.UpdateData && h.Seg.FeatureEnabled[1][0] && h.Seg.FeatureData[1][0] < 0
				},
				"2 tile columns":                 func(h *FrameHeader) bool { return h.TileColsLog2 == 1 },
				"an error resilient inter frame": func(h *FrameHeader) bool { return h.ErrorResilientMode && h.FrameType == NonKeyFrame },
			},
		},
		{
			"odd size, lossless", src(175, 99, 0.4),
			[][]string{{"-pix_fmt", "yuv420p", "-lossless", "1", "-g", "5"}},
			false,
			map[string]func(h *FrameHeader) bool{
				"lossless": func(h *FrameHeader) bool { return h.Quant.BaseQIdx == 0 && h.FrameType == NonKeyFrame },
			},
		},
		{
			"10-bit profile 2", src(192, 128, 0.4),
			[][]string{{"-pix_fmt", "yuv420p10le", "-color_range", "pc", "-colorspace", "bt709"}},
			false,
			map[string]func(h *FrameHeader) bool{
				"10 bits, BT.709 full range": func(h *FrameHeader) bool {
					return h.Profile == 2 && h.Color.BitDepth == 10 && h.Color.ColorSpace == CSBT709 && h.Color.FullRange
				},
			},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			pkts, trace := traceIVF(t, c.src, c.passes...)
			headers, supers := checkStream(t, pkts, trace)
			if c.supers && supers == 0 {
				t.Error("no packet is a superframe")
			}
			for what, f := range c.has {
				if !any(headers, f) {
					t.Errorf("none of %d frame headers has %s", len(headers), what)
				}
			}
		})
	}
}

// A packet without a superframe index is one frame; one whose index
// sizes run past it is an error.
func TestFrames(t *testing.T) {
	var frames [MaxFrames][]byte
	fs, err := Frames([]byte{0x82, 0x49, 0x83}, &frames)
	if err != nil || len(fs) != 1 || len(fs[0]) != 3 {
		t.Errorf("a plain frame splits into %d frames, %v", len(fs), err)
	}
	index := []byte{0xc1, 2, 3, 0xc1}
	fs, err = Frames(append([]byte{1, 2, 3, 4, 5}, index...), &frames)
	if err != nil || len(fs) != 2 || !bytes.Equal(fs[0], []byte{1, 2}) || !bytes.Equal(fs[1], []byte{3, 4, 5}) {
		t.Errorf("a superframe of 2 and 3 bytes splits into %v, %v", fs, err)
	}
	if _, err = Frames(append([]byte{1, 2, 3, 4}, index...), &frames); err == nil {
		t.Error("a superframe index running past its data splits")
	}
}

// Decoding from a seek starts afresh: a non-key frame before any key
// frame is an error.
func TestNonKeyFrameFirstIsAnError(t *testing.T) {
	var s Stream
	var h FrameHeader
	if err := s.ParseFrame(&h, []byte{0x86, 0x00, 0x40, 0x00, 0x00, 0x00}); err == nil {
		t.Error("a non-key frame parsed with no key frame before it")
	}
}
