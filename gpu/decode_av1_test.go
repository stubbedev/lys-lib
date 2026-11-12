package gpu

import (
	"bytes"
	"image"
	"math/bits"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/stubbedev/lys-lib/internal/av1"
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
		format videoSamples
		args   []string
	}
	clips := []grained{
		{"estimated, hidden frames", 192, 128, nv12Samples, libaom("", "-g", "12", "-lag-in-frames", "16", "-denoise-noise-level", "10")},
		{"estimated, 10 bits, odd size", 271, 153, p010Samples, libaom("", "-g", "12", "-pix_fmt", "yuv420p10le", "-denoise-noise-level", "10")},
	}
	for n := 1; n <= 16; n++ {
		c := grained{"test vector " + strconv.Itoa(n), 192, 128, nv12Samples, libaom("film-grain-test="+strconv.Itoa(n), "-g", "6")}
		if n%2 == 0 {
			c.format, c.args = p010Samples, append(c.args, "-pix_fmt", "yuv420p10le")
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

// av1Headers reads the frame headers in each of pkts, an AV1 stream
// whose sequence header is in config, the track's av1C, or in the
// packets.
func av1Headers(t *testing.T, config []byte, pkts []demux.Packet) [][]av1.FrameHeader {
	t.Helper()
	var s av1.Stream
	out := make([][]av1.FrameHeader, len(pkts))
	read := func(b []byte, into *[]av1.FrameHeader) {
		walk := av1.NewOBUs(b)
		for u, ok := walk.Next(); ok; u, ok = walk.Next() {
			switch u.Type {
			case av1.OBUSequenceHeader:
				if err := s.Seq.Parse(u.Payload); err != nil {
					t.Fatal(err)
				}
			case av1.OBUFrameHeader, av1.OBUFrame:
				var h av1.FrameHeader
				if err := s.ParseFrameHeader(&h, &u); err != nil {
					t.Fatal(err)
				}
				s.Update(&h)
				*into = append(*into, h)
			}
		}
		if err := walk.Err(); err != nil {
			t.Fatal(err)
		}
	}
	if len(config) > 0 {
		obus, err := av1.ConfigOBUs(config)
		if err != nil {
			t.Fatal(err)
		}
		var none []av1.FrameHeader
		read(obus, &none)
	}
	for i, p := range pkts {
		read(p.Data, &out[i])
	}
	return out
}

// av1TemporalDelimiter is a temporal delimiter OBU, which starts each
// temporal unit of an IVF file and which MP4 leaves out.
var av1TemporalDelimiter = []byte{0x12, 0x00}

// av1ShowExisting is a temporal unit whose frame header OBU shows the
// frame in reference frame slot, in a stream that codes neither frame
// ids nor a decoder model: show_existing_frame, the slot and the
// trailing bit.
func av1ShowExisting(slot int) []byte {
	return append(slices.Clone(av1TemporalDelimiter), 0x1a, 0x01, 0x88|byte(slot)<<4)
}

// A frame header that shows a frame shown before shows it again, as
// ffmpeg (dav1d) does, with its film grain, which is in the frame the
// picture went out in and not in the reference picture it is copied
// out of again.
func TestDecodeAV1ShowsAFrameAgain(t *testing.T) {
	d := decodingDevice(t, demux.AV1)
	for _, c := range []struct {
		name  string
		args  []string
		grain bool
	}{
		{"without film grain", libaom("", "-g", "12"), false},
		{"with film grain", libaom("film-grain-test=1", "-g", "12"), true},
	} {
		t.Run(c.name, func(t *testing.T) {
			cl := ffmpegClip(t, "a.mp4", 192, 128, 0.4, c.args...)
			track, pkts := packets(t, cl.path)
			hdrs := av1Headers(t, track.Config, pkts)
			at := slices.IndexFunc(hdrs, func(h []av1.FrameHeader) bool {
				last := &h[len(h)-1]
				return last.FrameType == av1.InterFrame && last.ShowFrame && !last.ShowExistingFrame && last.RefreshFrameFlags != 0 && last.FilmGrain.ApplyGrain == c.grain
			})
			if at < 0 {
				t.Fatal("libaom coded no shown inter frame that a reference slot holds")
			}
			var cut [][]byte
			for i, p := range pkts {
				cut = append(cut, slices.Concat(av1TemporalDelimiter, p.Data))
				if i == at {
					cut = append(cut, av1ShowExisting(bits.TrailingZeros8(hdrs[at][len(hdrs[at])-1].RefreshFrameFlags)))
				}
			}
			again := apart(cut)
			v, err := d.NewVideoDecoder(track)
			if err != nil {
				t.Fatal(err)
			}
			defer v.Close()
			got := decodeShownAgain(t, v, again)
			checkFrames(t, ffmpegDecoded(t, writeIVF(t, "AV01", cl.w, cl.h, again), cl.w, cl.h, nv12Samples), got, 0)
			if want := len(cl.frames) + 1; len(got) != want {
				t.Errorf("%d frames, want the stream's %d and the one shown again", len(got), len(cl.frames))
			}
			if !bytes.Equal(got[at].picture, got[at+1].picture) {
				t.Errorf("the frame shown again differs from its first showing in %d bytes", differing(got[at].picture, got[at+1].picture))
			}
		})
	}
}

func TestDecodeAV1RetriesATemporalUnitWhoseLastFrameFailed(t *testing.T) {
	d := decodingDevice(t, demux.AV1)
	cl := ffmpegClip(t, "a.mp4", 192, 128, 1.2, libaom("enable-tpl-model=1", "-g", "30", "-lag-in-frames", "19", "-auto-alt-ref", "1")...)
	track, pkts := packets(t, cl.path)
	hdrs := av1Headers(t, track.Config, pkts)
	at := 1 + slices.IndexFunc(hdrs[1:], func(h []av1.FrameHeader) bool {
		return len(h) > 1 && !slices.ContainsFunc(h, func(f av1.FrameHeader) bool { return f.ShowExistingFrame })
	})
	if at == 0 {
		t.Fatal("libaom coded no temporal unit of several decoded frames after the first")
	}
	v, err := d.NewVideoDecoder(track)
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()
	checkFrames(t, cl, decodeRetryingTheLastPicture(t, v, pkts, at, len(hdrs[at]), &v.codec.(*av1Decoder).resume), 0)
}
