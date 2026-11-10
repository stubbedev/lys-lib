package gpu

import (
	"errors"
	"image"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stubbedev/lys-lib/media/demux"
)

// libvpx is ffmpeg's arguments for libvpx-vp9 at speed 4 with args.
func libvpx(args ...string) []string {
	return append([]string{"-c:v", "libvpx-vp9", "-cpu-used", "4"}, args...)
}

// libvpxTwoPass is libvpx's arguments for a two-pass encode with args,
// which is how libvpx codes hidden alternate reference frames.
func libvpxTwoPass(args ...string) [][]string {
	return [][]string{append(libvpx("-pass", "1"), args...), append(libvpx("-pass", "2"), args...)}
}

// The decoder decodes what libvpx writes bit for bit as ffmpeg does:
// hidden alternate reference frames in superframes, all three
// references, segmentation with temporal prediction and cyclic refresh,
// tile columns, error resilient frames, lossless frames, an odd size,
// and MP4.
func TestDecodeVP9MatchesFFmpeg(t *testing.T) {
	d := decodingDevice(t, demux.VP9)
	for _, c := range []struct {
		name, file string
		w, h       int
		passes     [][]string
	}{
		{"hidden alternate references", "a.webm", 192, 128, libvpxTwoPass("-b:v", "200k", "-g", "30", "-auto-alt-ref", "1", "-lag-in-frames", "16")},
		{"cyclic refresh, tile columns, error resilient", "b.webm", 640, 360, [][]string{libvpx("-deadline", "realtime", "-cpu-used", "8", "-aq-mode", "3", "-tile-columns", "2", "-error-resilient", "1", "-g", "30")}},
		{"variance segmentation", "c.webm", 256, 144, libvpxTwoPass("-b:v", "150k", "-aq-mode", "1", "-g", "25")},
		{"lossless", "d.webm", 192, 128, [][]string{libvpx("-lossless", "1", "-g", "10")}},
		{"odd size 271x153", "e.webm", 271, 153, libvpxTwoPass("-b:v", "200k", "-g", "20", "-auto-alt-ref", "1")},
		{"MP4", "f.mp4", 192, 128, [][]string{libvpx("-g", "12")}},
	} {
		t.Run(c.name, func(t *testing.T) {
			cl := ffmpegPasses(t, c.file, c.w, c.h, 2, c.passes...)
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

// After a seek, Reset and the packets from a keyframe decode the frames
// from it on as a decode from the start does; the frames hidden before
// the reset are not shown.
func TestDecodeVP9ResetsAtAKeyframe(t *testing.T) {
	d := decodingDevice(t, demux.VP9)
	cl := ffmpegPasses(t, "a.webm", 192, 128, 3, libvpxTwoPass("-b:v", "200k", "-g", "20", "-keyint_min", "20", "-auto-alt-ref", "1", "-lag-in-frames", "16")...)
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

// A frame's Rect is the frame, at an odd size too.
func TestDecodeVP9CropsToTheFrame(t *testing.T) {
	d := decodingDevice(t, demux.VP9)
	cl := ffmpegClip(t, "a.webm", 271, 153, 0.2, libvpx()...)
	track, pkts := packets(t, cl.path)
	v, err := d.NewVideoDecoder(track)
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()
	if err := v.Decode(pkts[0]); err != nil {
		t.Fatal(err)
	}
	f, err := v.Next()
	if err != nil || f == nil {
		t.Fatalf("frame %v, %v", f, err)
	}
	defer f.Release()
	if r := f.Rect(); r != image.Rect(0, 0, 271, 153) {
		t.Errorf("rect %v, want the stream's 271x153", r)
	}
}

// Decoding VP9 frames, hidden ones in superframes among them, handing
// them out and releasing them allocates nothing once the decoder has
// its frames.
func TestDecodeVP9DoesNotAllocate(t *testing.T) {
	d := decodingDevice(t, demux.VP9)
	cl := ffmpegPasses(t, "a.webm", 192, 128, 4, libvpxTwoPass("-b:v", "200k", "-g", "250", "-auto-alt-ref", "1", "-lag-in-frames", "16")...)
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
			f, err := v.Next()
			if err != nil {
				t.Fatal(err)
			}
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

// A 4:4:4 stream (profile 1) is refused, naming its profile.
func TestDecodeVP9Refuses444(t *testing.T) {
	d := decodingDevice(t, demux.VP9)
	cl := ffmpegClip(t, "a.webm", 192, 128, 0.2, libvpx("-pix_fmt", "yuv444p")...)
	track, pkts := packets(t, cl.path)
	v, err := d.NewVideoDecoder(track)
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()
	err = v.Decode(pkts[0])
	if !errors.Is(err, ErrNoVideoDecoder) || !strings.Contains(err.Error(), "profile 1") {
		t.Errorf("decoding a 4:4:4 stream: %v, want ErrNoVideoDecoder naming profile 1", err)
	}
}
