package gpu

import (
	"image"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/stubbedev/lys-lib/fourcc"
	"github.com/stubbedev/lys-lib/media/demux"
	"github.com/stubbedev/lys-lib/testbuild"
)

// libaom is ffmpeg's arguments for libaom at speed 6 with params, if
// any, and args.
func libaom(params string, args ...string) []string {
	out := []string{"-c:v", "libaom-av1", "-cpu-used", "6"}
	if params != "" {
		out = append(out, "-aom-params", params)
	}
	return append(out, args...)
}

// The decoder decodes what libaom and SVT-AV1 write bit for bit as
// ffmpeg (dav1d) does, frames in presentation order: hidden alternate
// reference frames shown by later frame headers, several references
// and compound prediction, tiles, CDEF, loop restoration and global
// motion, error resilient frames with screen content tools,
// segmentation, delta q and loop filter deltas, a size that crops, and
// Matroska.
func TestDecodeAV1MatchesFFmpeg(t *testing.T) {
	d := decodingDevice(t, demux.AV1)
	for _, c := range []struct {
		name, file string
		w, h       int
		args       []string
	}{
		{"hidden frames, 7 references", "a.mp4", 192, 128, libaom("enable-tpl-model=1", "-g", "30", "-lag-in-frames", "19", "-auto-alt-ref", "1")},
		{"2x2 tiles, restoration, global motion", "b.mp4", 256, 144, libaom("enable-restoration=1:enable-global-motion=1:tile-columns=1:tile-rows=1", "-g", "25")},
		{"error resilient, screen content", "c.mp4", 192, 128, libaom("tune-content=screen", "-error-resilience", "1", "-g", "12")},
		{"cropped 270x150", "d.mp4", 270, 150, libaom("enable-cdef=1", "-g", "20")},
		{"128x128 superblocks, matroska", "e.mkv", 192, 128, libaom("sb-size=128", "-g", "12")},
		{"segmentation, segment data of the primary reference", "g.mp4", 192, 128, libaom("", "-cpu-used", "4", "-aq-mode", "1", "-g", "30")},
		{"realtime, cyclic refresh segmentation", "h.mp4", 192, 128, libaom("", "-usage", "realtime", "-cpu-used", "8", "-aq-mode", "3", "-g", "30")},
		{"delta q and loop filter deltas", "i.mp4", 192, 128, libaom("deltaq-mode=3:delta-lf-mode=1", "-g", "30")},
		{"SVT-AV1", "f.mp4", 192, 128, []string{"-c:v", "libsvtav1", "-preset", "10", "-g", "24", "-svtav1-params", "film-grain=0"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			cl := ffmpegClip(t, c.file, c.w, c.h, 2, c.args...)
			track, pkts := packets(t, cl.path)
			v, err := d.NewVideoDecoder(track)
			if err != nil {
				t.Fatal(err)
			}
			defer v.Close()
			checkFrames(t, cl, decodeAll(t, v, pkts), 0)
		})
	}
}

// Film grain comes out as ffmpeg (dav1d) synthesizes it, sample for
// sample, whether the device applies it into its output pictures or
// decodes into its reference pictures alone and the decoder synthesizes
// it in a pass of the graphics queue: the grain libaom estimates, on
// hidden frames shown later too, and libaom's test vectors, which cover
// grain in every plane, chroma scaled from luma, each autoregression
// lag, overlapped and clipped noise, at 8 and 10 bits and at an odd
// size.
func TestDecodeAV1FilmGrain(t *testing.T) {
	d := decodingDevice(t, demux.AV1)
	type grained struct {
		name   string
		w, h   int
		format fourcc.Format
		args   []string
	}
	clips := []grained{
		{"estimated, hidden frames", 192, 128, fourcc.NV12, libaom("", "-g", "12", "-lag-in-frames", "16", "-denoise-noise-level", "10")},
		{"estimated, 10 bits, odd size", 271, 153, fourcc.P010, libaom("", "-g", "12", "-pix_fmt", "yuv420p10le", "-denoise-noise-level", "10")},
	}
	for n := 1; n <= 16; n++ {
		c := grained{"test vector " + strconv.Itoa(n), 192, 128, fourcc.NV12, libaom("film-grain-test="+strconv.Itoa(n), "-g", "6")}
		if n%2 == 0 {
			c.format, c.args = fourcc.P010, append(c.args, "-pix_fmt", "yuv420p10le")
		}
		clips = append(clips, c)
	}
	for _, c := range clips {
		t.Run(c.name, func(t *testing.T) {
			secs := 0.4
			if c.name == clips[0].name {
				secs = 1
			}
			cl := encodeClip(t, "a.mp4", c.w, c.h, secs, c.format, c.args)
			track, pkts := packets(t, cl.path)
			v, err := d.NewVideoDecoder(track)
			if err != nil {
				t.Fatal(err)
			}
			defer v.Close()
			checkFrames(t, cl, decodeAll(t, v, pkts), 0)
			if v.copies != (v.grain != nil) {
				t.Errorf("the device decodes into its reference pictures alone: %v; the decoder synthesized grain: %v", v.copies, v.grain != nil)
			}
		})
	}
}

// Decoding a frame and synthesizing its film grain allocates nothing
// once the decoder has its frames and its grain pass, and a frame has
// not finished decoding before its grain is in it.
func TestDecodeAV1FilmGrainDoesNotAllocate(t *testing.T) {
	if testbuild.Race {
		t.Skip("the race detector allocates on its own")
	}
	d := decodingDevice(t, demux.AV1)
	cl := ffmpegClip(t, "a.mp4", 192, 128, 4, libaom("film-grain-test=1", "-g", "250", "-lag-in-frames", "0")...)
	track, pkts := packets(t, cl.path)
	v, err := d.NewVideoDecoder(track)
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()
	i := 0
	step := func() {
		if err := v.Decode(pkts[i]); err != nil {
			t.Fatal(err)
		}
		i++
		for f := v.Next(); f != nil; f = v.Next() {
			if done, err := f.Decoded(); err != nil || done && f.post > d.completed {
				t.Fatalf("frame at %v: decoded %v, %v, with its grain pass %d past the device's %d", f.PTS(), done, err, f.post, d.completed)
			}
			f.Release()
		}
		if err := d.Finish(); err != nil {
			t.Fatal(err)
		}
	}
	for range 20 {
		step()
	}
	if v.copies && v.grain == nil {
		t.Fatal("the clip has no film grain for the decoder to synthesize")
	}
	if n := testing.AllocsPerRun(len(pkts)-i-1, step); n != 0 {
		t.Errorf("%.1f allocations a frame, want 0", n)
	}
}

// After a seek, Reset and the packets from a keyframe decode the
// frames from it on as a decode from the start does; the frames hidden
// before the reset are not shown.
func TestDecodeAV1ResetsAtAKeyframe(t *testing.T) {
	d := decodingDevice(t, demux.AV1)
	cl := ffmpegClip(t, "a.mp4", 192, 128, 3, libaom("", "-g", "20", "-keyint_min", "20", "-lag-in-frames", "19")...)
	track, pkts := packets(t, cl.path)
	v, err := d.NewVideoDecoder(track)
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()
	decodeSome(t, v, pkts[:15])
	if err := v.Reset(); err != nil {
		t.Fatal(err)
	}
	key := slices.IndexFunc(pkts[1:], func(p demux.Packet) bool { return p.Key }) + 1
	if key == 0 {
		t.Fatal("no keyframe after the first")
	}
	start := pkts[key].PTS
	got := decodeAll(t, v, pkts[key-3:])
	if len(got) > 0 && got[0].pts != start {
		t.Errorf("first frame after the reset presents at %v, want the keyframe's %v", got[0].pts, start)
	}
	checkFrames(t, cl, got, int(start/(40*time.Millisecond)))
}

// A frame's Rect is the frame, the 8x8 blocks it is coded in cropped.
func TestDecodeAV1CropsToTheFrame(t *testing.T) {
	d := decodingDevice(t, demux.AV1)
	cl := ffmpegClip(t, "a.mp4", 270, 150, 0.2, libaom("")...)
	track, pkts := packets(t, cl.path)
	v, err := d.NewVideoDecoder(track)
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()
	if err := v.Decode(pkts[0]); err != nil {
		t.Fatal(err)
	}
	f := v.Next()
	if f == nil {
		t.Fatal("the first packet completed no frame")
	}
	defer f.Release()
	if s := f.Size(); s != image.Pt(270, 150) {
		t.Errorf("size %v, want the stream's 270x150", s)
	}
}

// Decoding AV1 frames, hidden ones among them, handing them out and
// releasing them allocates nothing once the decoder has its frames.
func TestDecodeAV1DoesNotAllocate(t *testing.T) {
	d := decodingDevice(t, demux.AV1)
	cl := ffmpegClip(t, "a.mp4", 192, 128, 4, libaom("", "-g", "250", "-lag-in-frames", "19")...)
	track, pkts := packets(t, cl.path)
	v, err := d.NewVideoDecoder(track)
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()
	i := 0
	step := func() {
		if err := v.Decode(pkts[i]); err != nil {
			t.Fatal(err)
		}
		i++
		for {
			f := v.Next()
			if f == nil {
				return
			}
			f.Release()
		}
	}
	for range 20 {
		step()
	}
	if n := testing.AllocsPerRun(len(pkts)-i-1, step); n != 0 {
		t.Errorf("%.1f allocations a frame, want 0", n)
	}
}
