package nal

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
)

// The parser reads back the parameter sets and slice headers the
// encoder's writer writes, with the values written.
func TestHEVCParsesWhatTheWriterWrites(t *testing.T) {
	h := HEVC{LevelIdc: 123, Width: 336, Height: 256, ConfRight: 1, ConfBottom: 7, Log2MaxPocLsb: 8, Log2MinCb: 3, Log2Ctb: 5, Log2MinTb: 2, Log2MaxTb: 5, TransformDepth: 2, AMP: true, SAO: true, TemporalMVP: true, InitQP: 30}
	var v HEVCVPS
	vps := h.VPS()
	if err := v.Parse(Unescape(nil, vps[2:])); err != nil {
		t.Fatal(err)
	}
	if v.PTL.ProfileIdc != 1 || v.PTL.LevelIdc != 123 || v.MaxSubLayersMinus1 != 0 || v.Ordering.MaxDecPicBufferingMinus1[0] != 1 {
		t.Errorf("VPS %+v", v)
	}
	var sps [16]*HEVCSPS
	s := &HEVCSPS{}
	unit := h.SPS()
	if err := s.Parse(Unescape(nil, unit[2:])); err != nil {
		t.Fatal(err)
	}
	if s.Width != 336 || s.Height != 256 || s.Log2MaxPocLsb != 8 || s.Log2MinCbMinus3+3+s.Log2DiffMaxMinCb != 5 || !s.AMP || !s.SAO || !s.TemporalMVP || s.NumShortTermRPS != 1 {
		t.Errorf("SPS %+v", s)
	}
	if x0, y0, x1, y1 := s.CropRect(); x0 != 0 || y0 != 0 || x1 != 334 || y1 != 242 {
		t.Errorf("crop rect %d,%d-%d,%d, want 0,0-334,242", x0, y0, x1, y1)
	}
	if rps := s.ShortTermRPS[0]; rps.NumNegative != 1 || rps.NumPositive != 0 || rps.DeltaPocS0[0] != -1 || rps.UsedS0 != 1 {
		t.Errorf("short-term set %+v, want the picture before", rps)
	}
	if !s.VUIPresent || s.VUI.MatrixCoeffs != 1 || s.VUI.FullRange {
		t.Errorf("VUI %+v", s.VUI)
	}
	sps[s.ID] = s
	var p HEVCPPS
	unit = h.PPS()
	if err := p.Parse(Unescape(nil, unit[2:]), &sps); err != nil {
		t.Fatal(err)
	}
	if p.InitQpMinus26 != 4 || !p.LoopFilterAcrossSlices {
		t.Errorf("PPS %+v", p)
	}
	var pps [64]*HEVCPPS
	pps[p.ID] = &p
	for _, c := range []struct {
		sinceIDR int
		nalType  uint8
		typ      uint8
	}{{0, HEVCNALIDRWRAD, HEVCSliceI}, {5, 1, HEVCSliceP}} {
		unit, _ := h.AppendSliceHeader(nil, new(Writer), c.sinceIDR, false, 20)
		var sh HEVCSliceHeader
		if HEVCType(unit) != c.nalType {
			t.Fatalf("frame %d: NAL unit type %d, want %d", c.sinceIDR, HEVCType(unit), c.nalType)
		}
		if err := sh.Parse(Unescape(nil, unit[2:]), HEVCType(unit), &pps, &sps); err != nil {
			t.Fatal(err)
		}
		if sh.SliceType != c.typ || sh.PicOrderCntLsb != uint32(c.sinceIDR) || c.sinceIDR > 0 && !sh.ShortTermRefPicSetSPS {
			t.Errorf("frame %d: slice header %+v", c.sinceIDR, sh)
		}
	}
}

// writeRPS writes an explicitly coded short-term set of the delta POCs
// given, all used.
func writeRPS(w *Writer, neg, pos []int) {
	w.UEInt(len(neg))
	w.UEInt(len(pos))
	prev := 0
	for _, d := range neg {
		w.UEInt(prev - d - 1)
		w.Flag(true)
		prev = d
	}
	prev = 0
	for _, d := range pos {
		w.UEInt(d - prev - 1)
		w.Flag(true)
		prev = d
	}
}

// A predicted short-term set works out its delta POCs from the set it
// predicts from as 7.4.8 says, in order: negative ones nearest first,
// then positive ones nearest first.
func TestHEVCPredictedReferencePictureSet(t *testing.T) {
	var w Writer
	writeRPS(&w, []int{-1, -3}, []int{2})
	w.Flag(true)
	w.Flag(true)
	w.UE(0)
	for _, used := range []bool{true, true, false, true} {
		w.Flag(used)
		if !used {
			w.Flag(true)
		}
	}
	w.Trailing()
	sets := make([]HEVCShortTermRPS, 2)
	r := NewReader(w.buf)
	for i := range sets {
		if err := sets[i].parse(&r, i, 2, sets); err != nil {
			t.Fatal(err)
		}
	}
	s := sets[1]
	neg, pos := s.DeltaPocS0[:s.NumNegative], s.DeltaPocS1[:s.NumPositive]
	if !slices.Equal(neg, []int32{-1, -2, -4}) || !slices.Equal(pos, []int32{1}) {
		t.Errorf("predicted set %v and %v, want [-1 -2 -4] and [1]", neg, pos)
	}
	if s.UsedS0 != 0b111 || s.UsedS1 != 0 {
		t.Errorf("used %03b and %b, want 111 and 0: the positive entry is kept but unused", s.UsedS0, s.UsedS1)
	}
}

// The parser reads x265's headers: B-frames in a pyramid with the
// reference picture sets in the SPS, and in the slice headers when
// told to.
func TestHEVCParsesX265(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("no ffmpeg to encode a stream")
	}
	for _, params := range []string{"keyint=12:bframes=4:b-pyramid=1:ref=4", "keyint=12:bframes=3:rps-in-sps=0:scaling-list=default"} {
		path := filepath.Join(t.TempDir(), "a.265")
		out, err := exec.Command(ffmpeg, "-v", "error", "-f", "lavfi", "-i", "testsrc2=size=176x144:rate=25:duration=1", "-pix_fmt", "yuv420p",
			"-c:v", "libx265", "-x265-params", "log-level=none:"+params, path).CombinedOutput()
		if err != nil {
			t.Fatalf("ffmpeg: %v: %s", err, out)
		}
		stream, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var vps [16]*HEVCVPS
		var sps [16]*HEVCSPS
		var pps [64]*HEVCPPS
		pictures, sliceSets := 0, 0
		for _, unit := range splitAnnexB(stream) {
			rbsp := Unescape(nil, unit[2:])
			switch typ := HEVCType(unit); {
			case typ == HEVCNALVPS:
				v := &HEVCVPS{}
				if err := v.Parse(rbsp); err != nil {
					t.Fatal(err)
				}
				vps[v.ID] = v
			case typ == HEVCNALSPS:
				s := &HEVCSPS{}
				if err := s.Parse(rbsp); err != nil {
					t.Fatal(err)
				}
				sps[s.ID] = s
			case typ == HEVCNALPPS:
				p := &HEVCPPS{}
				if err := p.Parse(rbsp, &sps); err != nil {
					t.Fatal(err)
				}
				pps[p.ID] = p
			case typ <= HEVCNALCRA && (typ <= HEVCNALRASLR || typ >= HEVCNALBLAWLP) && unit[2]&0x80 != 0:
				var h HEVCSliceHeader
				if err := h.Parse(rbsp, typ, &pps, &sps); err != nil {
					t.Fatalf("%s: picture %d: %v", params, pictures, err)
				}
				pictures++
				if !h.ShortTermRefPicSetSPS && typ != HEVCNALIDRWRAD && typ != HEVCNALIDRNLP {
					sliceSets++
				}
			}
		}
		s := sps[0]
		if vps[0] == nil || s == nil || pps[0] == nil {
			t.Fatalf("%s: no VPS, SPS or PPS 0", params)
		}
		if s.Width != 176 || s.Height != 144 || s.PTL.ProfileIdc != 1 || s.Ordering.MaxNumReorderPics[0] == 0 {
			t.Errorf("%s: SPS %+v", params, s)
		}
		if pictures != 25 {
			t.Errorf("%s: %d pictures, want 25", params, pictures)
		}
		if inSlices := sliceSets > 0; inSlices != (s.NumShortTermRPS == 0) {
			t.Errorf("%s: %d slice-coded sets with %d in the SPS", params, sliceSets, s.NumShortTermRPS)
		}
	}
}
