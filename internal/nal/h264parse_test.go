package nal

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// parseSPS reads unit, an SPS NAL unit.
func parseSPS(t *testing.T, unit []byte) *H264SPS {
	t.Helper()
	s := &H264SPS{}
	if err := s.Parse(Unescape(nil, unit[1:])); err != nil {
		t.Fatal(err)
	}
	return s
}

// The parser reads back the parameter sets and slice headers the
// encoder's writer writes, with the values written.
func TestH264ParsesWhatTheWriterWrites(t *testing.T) {
	h := H264{ProfileIdc: 100, LevelIdc: 41, WidthMbs: 21, HeightMbs: 16, CropRight: 1, CropBottom: 7, Log2MaxFrameNum: 6, MaxNumRefFrames: 1, CABAC: true, Transform8x8: true, InitQP: 20}
	s := parseSPS(t, h.SPS())
	if s.ProfileIdc != 100 || s.LevelIdc != 41 || s.WidthMbs != 21 || s.HeightMapUnits != 16 || !s.FrameMbsOnly || s.Log2MaxFrameNum != 6 || s.PicOrderCntType != 2 || s.MaxNumRefFrames != 1 {
		t.Errorf("SPS %+v", s)
	}
	if x0, y0, x1, y1 := s.CropRect(); x0 != 0 || y0 != 0 || x1 != 21*16-2 || y1 != 16*16-14 {
		t.Errorf("crop rect %d,%d-%d,%d, want 0,0-334,242", x0, y0, x1, y1)
	}
	if !s.VUIPresent || s.VUI.MatrixCoefficients != 1 || s.VUI.FullRange || !s.VUI.BitstreamRestriction || s.VUI.MaxNumReorderFrames != 0 || s.VUI.MaxDecFrameBuffering != 1 {
		t.Errorf("VUI %+v", s.VUI)
	}
	if s.ScalingMatrixPresent || s.Scaling.List8x8[5][63] != 16 {
		t.Errorf("SPS without a matrix has scaling lists %v", s.Scaling)
	}
	var sps [32]*H264SPS
	sps[0] = s
	pps := h.PPS()
	var p H264PPS
	if err := p.Parse(Unescape(nil, pps[1:]), &sps); err != nil {
		t.Fatal(err)
	}
	if !p.EntropyCodingMode || !p.Transform8x8Mode || p.PicInitQpMinus26 != -6 || p.ScalingMatrixPresent {
		t.Errorf("PPS %+v", p)
	}
	var ppss [256]*H264PPS
	ppss[0] = &p
	for _, c := range []struct {
		sinceIDR int
		idr      uint16
		typ      uint8
	}{{0, 7, H264SliceI}, {5, 0, H264SliceP}} {
		unit, _ := h.AppendSliceHeader(nil, new(Writer), c.sinceIDR, c.idr, 20)
		var sh H264SliceHeader
		if err := sh.Parse(Unescape(nil, unit[1:]), unit[0], &ppss, &sps); err != nil {
			t.Fatal(err)
		}
		if sh.SliceType != c.typ || sh.FrameNum != uint32(c.sinceIDR) || sh.IDRPicID != c.idr || sh.AdaptiveRefPicMarking {
			t.Errorf("frame %d: slice header %+v", c.sinceIDR, sh)
		}
	}
}

// A PPS naming an SPS the stream has not sent, slice groups, or a
// truncated SPS are errors that say so: the slice groups an unsupported
// stream, the others a malformed one.
func TestH264RefusesWhatItCannotRead(t *testing.T) {
	h := H264{ProfileIdc: 77, LevelIdc: 41, WidthMbs: 40, HeightMbs: 23, Log2MaxFrameNum: 4, MaxNumRefFrames: 1, InitQP: 26}
	var sps [32]*H264SPS
	var p H264PPS
	pps := h.PPS()
	if err := p.Parse(Unescape(nil, pps[1:]), &sps); !errors.Is(err, ErrH264) || errors.Is(err, ErrUnsupported) {
		t.Errorf("a PPS of an SPS not sent: %v, want ErrH264 alone", err)
	}
	unit := h.SPS()
	var s H264SPS
	if err := s.Parse(Unescape(nil, unit[1:8])); !errors.Is(err, ErrH264) || errors.Is(err, ErrUnsupported) {
		t.Errorf("a truncated SPS: %v, want ErrH264 alone", err)
	}
	sps[0] = parseSPS(t, unit)
	var w Writer
	w.UE(0)
	w.UE(0)
	w.Flag(false)
	w.Flag(false)
	w.UE(2)
	w.Trailing()
	if err := p.Parse(w.buf, &sps); !errors.Is(err, ErrH264) || !errors.Is(err, ErrUnsupported) {
		t.Errorf("a PPS with slice groups: %v, want ErrH264 and ErrUnsupported", err)
	}
}

// writeList writes values as a scaling_list's deltas.
func writeList(w *Writer, values []int) {
	last := 8
	for _, v := range values {
		w.SEInt((v-last+384)%256 - 128)
		last = v
	}
}

// Absent scaling lists fall back as Table 7-2 says: rule A, from the
// defaults and the list before, in an SPS and in a PPS whose SPS has
// no matrix; rule B, from the SPS's lists, in a PPS whose SPS has one.
// A list coded as its first delta reaching zero is its default.
func TestH264ScalingListsFallBack(t *testing.T) {
	ramp := make([]int, 16)
	for i := range ramp {
		ramp[i] = 4 + i
	}
	var w Writer
	for i := range 8 {
		switch i {
		case 0:
			w.Flag(true)
			writeList(&w, ramp)
		case 3:
			w.Flag(true)
			w.SE(-8)
		default:
			w.Flag(false)
		}
	}
	w.Trailing()
	var a H264ScalingLists
	r := NewReader(w.buf)
	a.parse(&r, 8, nil)
	for i, want := range [][16]uint8{0: [16]uint8(toBytes(ramp)), 1: [16]uint8(toBytes(ramp)), 2: [16]uint8(toBytes(ramp)), 3: h264Default4x4Inter, 4: h264Default4x4Inter, 5: h264Default4x4Inter} {
		if a.List4x4[i] != want {
			t.Errorf("rule A 4x4 list %d = %v, want %v", i, a.List4x4[i], want)
		}
	}
	if a.List8x8[0] != h264Default8x8Intra || a.List8x8[1] != h264Default8x8Inter || a.List8x8[4] != h264Default8x8Intra || a.List8x8[5] != h264Default8x8Inter {
		t.Error("rule A 8x8 lists are not the defaults")
	}
	var none Writer
	for range 8 {
		none.Flag(false)
	}
	none.Trailing()
	var b H264ScalingLists
	r = NewReader(none.buf)
	b.parse(&r, 8, &a)
	if b != a {
		t.Errorf("rule B lists %v, want the SPS's %v", b, a)
	}
}

func toBytes(v []int) []uint8 {
	out := make([]uint8, len(v))
	for i, x := range v {
		out[i] = uint8(x)
	}
	return out
}

// The parser reads x264's headers as ffmpeg does: High with B-frames,
// four references, weighted prediction, the JVT scaling matrices in
// the PPS (resolved from the defaults, the SPS having none) and memory
// management control operations.
func TestH264ParsesX264(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("no ffmpeg to encode a stream")
	}
	path := filepath.Join(t.TempDir(), "a.264")
	out, err := exec.Command(ffmpeg, "-v", "error", "-f", "lavfi", "-i", "testsrc2=size=176x144:rate=25:duration=1", "-pix_fmt", "yuv420p",
		"-c:v", "libx264", "-profile:v", "high", "-bf", "3", "-x264-params", "b-pyramid=normal:ref=4:cqm=jvt:weightp=2", path).CombinedOutput()
	if err != nil {
		t.Fatalf("ffmpeg: %v: %s", err, out)
	}
	stream, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var sps [32]*H264SPS
	var pps [256]*H264PPS
	slices, mmcos := 0, 0
	for _, unit := range splitAnnexB(stream) {
		rbsp := Unescape(nil, unit[1:])
		switch H264Type(unit) {
		case H264NALSPS:
			s := &H264SPS{}
			if err := s.Parse(rbsp); err != nil {
				t.Fatal(err)
			}
			sps[s.ID] = s
		case H264NALPPS:
			p := &H264PPS{}
			if err := p.Parse(rbsp, &sps); err != nil {
				t.Fatal(err)
			}
			pps[p.ID] = p
		case H264NALSlice, H264NALIDR:
			var h H264SliceHeader
			if err := h.Parse(rbsp, unit[0], &pps, &sps); err != nil {
				t.Fatal(err)
			}
			slices++
			mmcos += h.NumMMCO
		}
	}
	s, p := sps[0], pps[0]
	if s == nil || p == nil {
		t.Fatal("no SPS 0 or PPS 0")
	}
	if s.ProfileIdc != 100 || s.WidthMbs != 11 || s.HeightMapUnits != 9 || s.PicOrderCntType != 0 || s.MaxNumRefFrames != 4 || s.VUI.MaxNumReorderFrames != 2 {
		t.Errorf("SPS %+v", s)
	}
	if !p.Transform8x8Mode || !p.WeightedPred || p.WeightedBipredIdc != 2 || !p.ScalingMatrixPresent {
		t.Errorf("PPS %+v", p)
	}
	if p.Scaling.List4x4[0] != h264Default4x4Intra || p.Scaling.List4x4[5] != h264Default4x4Inter || p.Scaling.List8x8[0] != h264Default8x8Intra || p.Scaling.List8x8[1] != h264Default8x8Inter {
		t.Errorf("JVT scaling lists %v, want the defaults", p.Scaling)
	}
	if slices != 25 {
		t.Errorf("%d slices, want 25", slices)
	}
	if mmcos == 0 {
		t.Error("no memory management control operations read; x264's B-pyramid writes them")
	}
}

// splitAnnexB splits an Annex-B stream into its NAL units.
func splitAnnexB(b []byte) [][]byte {
	var out [][]byte
	start := -1
	for i := 0; i+2 < len(b); i++ {
		if b[i] != 0 || b[i+1] != 0 || b[i+2] != 1 {
			continue
		}
		if start >= 0 {
			end := i
			for end > start && b[end-1] == 0 {
				end--
			}
			out = append(out, b[start:end])
		}
		start = i + 3
		i += 2
	}
	if start >= 0 && start < len(b) {
		out = append(out, b[start:])
	}
	return out
}
