package gpu

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stubbedev/lys-lib/internal/nal"
	"github.com/stubbedev/lys-lib/media/demux"
)

// annexBUnits splits an Annex B byte stream into its NAL units.
func annexBUnits(b []byte) [][]byte {
	var units [][]byte
	start := -1
	for i := 0; i+2 < len(b); i++ {
		if b[i] != 0 || b[i+1] != 0 || b[i+2] != 1 {
			continue
		}
		if start >= 0 {
			units = append(units, bytes.TrimRight(b[start:i], "\x00"))
		}
		start = i + 3
		i += 2
	}
	if start >= 0 {
		units = append(units, b[start:])
	}
	return units
}

// allowGaps is sps, a Baseline SPS NAL unit with pic_order_cnt_type 2,
// with gaps_in_frame_num_value_allowed_flag set.
func allowGaps(t *testing.T, sps []byte) []byte {
	t.Helper()
	rbsp := nal.Unescape(nil, sps[1:])
	r := nal.NewReader(rbsp)
	if profile := r.U(8); profile != 66 {
		t.Fatalf("SPS of profile_idc %d, want Baseline", profile)
	}
	r.Skip(16)
	r.UE()
	r.UE()
	if typ := r.UE(); typ != 2 {
		t.Fatalf("SPS of pic_order_cnt_type %d, want 2", typ)
	}
	r.UE()
	at := r.Pos()
	rbsp[at/8] |= 0x80 >> (at % 8)
	var w nal.Writer
	for _, b := range rbsp {
		w.U(8, uint32(b))
	}
	return w.Unit(sps[0])
}

// A stream whose SPS allows gaps in frame_num, with reference frames
// missing, decodes bit for bit as ffmpeg decodes it: the frames the
// gaps leave non-existing are inferred, in the reference lists where
// the standard puts them, each standing in with the reference frame
// before it, which is how ffmpeg conceals them too. The frames after
// a gap predict from the frames missing; the stream is coded without
// the deblocking filter, which ffmpeg runs as if each inferred frame
// were the frame it copies.
func TestDecodeH264FrameNumGap(t *testing.T) {
	d := decodingDevice(t, demux.H264)
	dir := t.TempDir()
	src := filepath.Join(dir, "a.h264")
	runFFmpeg(t, "-v", "error", "-y", "-f", "lavfi", "-i", "testsrc2=size=160x96:rate=25:duration=2", "-pix_fmt", "yuv420p",
		"-c:v", "libx264", "-profile:v", "baseline", "-x264-params", "keyint=100:ref=3:no-deblock=1", src)
	b, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	drop := map[int]bool{10: true, 20: true, 21: true, 22: true}
	var units [][]byte
	pictures := 0
	for _, u := range annexBUnits(b) {
		switch nal.H264Type(u) {
		case nal.H264NALSPS:
			u = allowGaps(t, u)
		case nal.H264NALSlice, nal.H264NALIDR:
			pictures++
			if drop[pictures-1] {
				continue
			}
		}
		units = append(units, u)
	}
	gapped := filepath.Join(dir, "gapped.h264")
	if err := os.WriteFile(gapped, nal.AnnexB(units...), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "gapped.mp4")
	runFFmpeg(t, "-v", "error", "-y", "-framerate", "25", "-i", gapped, "-c", "copy", path)
	cl := ffmpegDecoded(t, path, 160, 96, nv12Samples)
	if want := pictures - len(drop); len(cl.frames) != want {
		t.Fatalf("ffmpeg decodes %d frames, want the %d left", len(cl.frames), want)
	}
	track, pkts := packets(t, path)
	v, err := d.NewVideoDecoder(track)
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()
	checkFrames(t, cl, decodeAll(t, v, pkts), 0)
}
