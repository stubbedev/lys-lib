package gpu

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"io"
	"math"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/stubbedev/lys-lib/media/demux"
	"github.com/stubbedev/lys-lib/third_party/vk"
)

// decodingDevice is a device that decodes c, with ffmpeg to make the
// streams and decode them as the reference; the test skips without
// either, after checking that NewVideoDecoder says ErrNoVideoDecoder on
// a device without.
func decodingDevice(t *testing.T, c demux.Codec) *Device {
	t.Helper()
	d := openDevice(t)
	if !slices.Contains(d.DecodeCodecs(), c) {
		_, err := d.NewVideoDecoder(demux.Track{Kind: demux.Video, Codec: c})
		if !errors.Is(err, ErrNoVideoDecoder) {
			t.Fatalf("NewVideoDecoder on a device that decodes no %s: %v, want ErrNoVideoDecoder", c, err)
		}
		t.Skipf("%s decodes no %s: %v", d.Name(), c, err)
	}
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("no ffmpeg to make and check the streams")
	}
	return d
}

// stream is a stream ffmpeg encoded and the frames ffmpeg decodes from
// it, NV12 at the stream's size in presentation order.
type stream struct {
	path   string
	w, h   int
	frames [][]byte
}

// ffmpegClip encodes a moving test pattern of w x h at 25 frames a second
// for secs seconds with args into dir/name, and decodes it back with
// ffmpeg.
func ffmpegClip(t *testing.T, name string, w, h int, secs float64, args ...string) stream {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	src := fmt.Sprintf("testsrc2=size=%dx%d:rate=25:duration=%g", w, h, secs)
	enc := append([]string{"-v", "error", "-y", "-f", "lavfi", "-i", src, "-pix_fmt", "yuv420p"}, args...)
	runFFmpeg(t, append(enc, path)...)
	raw := runFFmpeg(t, "-v", "error", "-i", path, "-fps_mode", "passthrough", "-f", "rawvideo", "-pix_fmt", "nv12", "-")
	size := w*h + w*h/2
	if len(raw)%size != 0 || len(raw) == 0 {
		t.Fatalf("ffmpeg decoded %d bytes of %dx%d NV12 frames", len(raw), w, h)
	}
	c := stream{path: path, w: w, h: h}
	for len(raw) > 0 {
		c.frames, raw = append(c.frames, raw[:size]), raw[size:]
	}
	return c
}

func runFFmpeg(t *testing.T, args ...string) []byte {
	t.Helper()
	cmd := exec.Command("ffmpeg", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("ffmpeg %v: %v: %s", args, err, stderr.Bytes())
	}
	return out
}

// packets opens path and reads its video track's packets, each a copy.
func packets(t *testing.T, path string) (demux.Track, []demux.Packet) {
	t.Helper()
	f, err := demux.OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	track := videoTrack(t, f)
	if err := f.Select(track.Index); err != nil {
		t.Fatal(err)
	}
	var out []demux.Packet
	for {
		p, err := f.ReadPacket()
		if errors.Is(err, io.EOF) {
			return track, out
		}
		if err != nil {
			t.Fatal(err)
		}
		p.Data = bytes.Clone(p.Data)
		out = append(out, p)
	}
}

func videoTrack(t *testing.T, f *demux.File) demux.Track {
	t.Helper()
	for _, tr := range f.Tracks() {
		if tr.Kind == demux.Video {
			return tr
		}
	}
	t.Fatalf("%s has no video track", f.Name())
	return demux.Track{}
}

// decoded is a frame read back: its PTS and NV12 picture, cropped.
type decoded struct {
	pts  time.Duration
	nv12 []byte
}

// decodeAll decodes pkts, flushes and reads every frame back.
func decodeAll(t *testing.T, v *VideoDecoder, pkts []demux.Packet) []decoded {
	t.Helper()
	out := decodeSome(t, v, pkts)
	v.Flush()
	return takeFrames(t, v, out)
}

// decodeSome decodes pkts and reads back the frames they complete.
func decodeSome(t *testing.T, v *VideoDecoder, pkts []demux.Packet) []decoded {
	t.Helper()
	var out []decoded
	for _, p := range pkts {
		if err := v.Decode(p); err != nil {
			t.Fatal(err)
		}
		out = takeFrames(t, v, out)
	}
	return out
}

// takeFrames reads back and releases the frames Next has, after out.
func takeFrames(t *testing.T, v *VideoDecoder, out []decoded) []decoded {
	t.Helper()
	for {
		f, err := v.Next()
		if err != nil {
			t.Fatal(err)
		}
		if f == nil {
			return out
		}
		out = append(out, decoded{f.PTS(), readNV12(t, f)})
		f.Release()
	}
}

// readNV12 copies f's picture back to the CPU through the graphics
// queue, as NV12 of its Rect.
func readNV12(t *testing.T, f *VideoFrame) []byte {
	t.Helper()
	d := f.v.d
	w, h := f.extent[0], f.extent[1]
	buf, err := d.newReadBuffer(w*h*3/2, vk.BufferUsageTransferDstBit, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer buf.close()
	img := f.tex.image
	err = d.submit(func(cb vk.CommandBuffer) {
		fullBarrier(d, cb, img, vk.ImageLayoutShaderReadOnlyOptimal, vk.ImageLayoutTransferSrcOptimal, vk.QueueFamilyIgnored, vk.QueueFamilyIgnored)
		regions := [2]vk.BufferImageCopy{
			{ImageSubresource: vk.ImageSubresourceLayers{AspectMask: vk.ImageAspectFlags(vk.ImageAspectPlane0Bit), LayerCount: 1}, ImageExtent: extent3D(w, h)},
			{BufferOffset: vk.DeviceSize(w * h), ImageSubresource: vk.ImageSubresourceLayers{AspectMask: vk.ImageAspectFlags(vk.ImageAspectPlane1Bit), LayerCount: 1}, ImageExtent: extent3D(w/2, h/2)},
		}
		d.cmds.CmdCopyImageToBuffer(cb, img, vk.ImageLayoutTransferSrcOptimal, buf.buf, 2, &regions[0])
		fullBarrier(d, cb, img, vk.ImageLayoutTransferSrcOptimal, vk.ImageLayoutShaderReadOnlyOptimal, vk.QueueFamilyIgnored, vk.QueueFamilyIgnored)
		d.hostReadBarrier(cb)
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := buf.invalidate(); err != nil {
		t.Fatal(err)
	}
	r := f.Rect()
	out := make([]byte, 0, r.Dx()*r.Dy()*3/2)
	for y := r.Min.Y; y < r.Max.Y; y++ {
		out = append(out, buf.mapped[y*w+r.Min.X:y*w+r.Max.X]...)
	}
	for y := r.Min.Y / 2; y < r.Max.Y/2; y++ {
		at := w*h + y*w
		out = append(out, buf.mapped[at+r.Min.X:at+r.Max.X]...)
	}
	return out
}

// checkFrames compares the decoded frames with ffmpeg's, which H.264
// decoding makes bit for bit the same, and checks they come in
// presentation order.
func checkFrames(t *testing.T, c stream, got []decoded, from int) {
	t.Helper()
	want := c.frames[from:]
	if len(got) != len(want) {
		t.Fatalf("decoded %d frames, ffmpeg %d", len(got), len(want))
	}
	for i, g := range got {
		if i > 0 && g.pts <= got[i-1].pts {
			t.Errorf("frame %d presents at %v, after frame %d at %v", i, g.pts, i-1, got[i-1].pts)
		}
		if !bytes.Equal(g.nv12, want[i]) {
			t.Errorf("frame %d (PTS %v) differs from ffmpeg's in %d of %d bytes", from+i, g.pts, differing(g.nv12, want[i]), len(want[i]))
		}
	}
}

func differing(a, b []byte) int {
	if len(a) != len(b) {
		return max(len(a), len(b))
	}
	n := 0
	for i := range a {
		if a[i] != b[i] {
			n++
		}
	}
	return n
}

// The decoder decodes what x264 writes in each of the ways it can,
// bit for bit as ffmpeg does, frames in presentation order: B-frames
// referencing B-frames, several reference frames and weighted
// prediction; Baseline without B-frames (picture order count type 2);
// CAVLC in several slices; custom scaling matrices; a size that crops;
// and Matroska.
func TestDecodeH264MatchesFFmpeg(t *testing.T) {
	d := decodingDevice(t, demux.H264)
	for _, c := range []struct {
		name, file string
		w, h       int
		args       []string
	}{
		{"high, B-pyramid, 4 references", "a.mp4", 192, 128, []string{"-c:v", "libx264", "-profile:v", "high", "-bf", "3", "-x264-params", "b-pyramid=normal:ref=4:keyint=30:weightp=2"}},
		{"baseline", "b.mp4", 160, 96, []string{"-c:v", "libx264", "-profile:v", "baseline", "-x264-params", "keyint=20"}},
		{"main, CAVLC, 4 slices", "c.mp4", 176, 144, []string{"-c:v", "libx264", "-profile:v", "main", "-bf", "2", "-x264-params", "cabac=0:slices=4:keyint=25"}},
		{"high, JVT scaling matrices", "d.mp4", 160, 128, []string{"-c:v", "libx264", "-profile:v", "high", "-bf", "2", "-x264-params", "cqm=jvt:8x8dct=1"}},
		{"cropped 174x98", "e.mp4", 174, 98, []string{"-c:v", "libx264", "-bf", "2"}},
		{"matroska", "f.mkv", 160, 96, []string{"-c:v", "libx264", "-bf", "2", "-x264-params", "keyint=12"}},
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

// The decoder decodes what x265 writes bit for bit as ffmpeg does,
// frames in presentation order: B-frames in a pyramid with several
// references, reference picture sets coded in slice headers, wavefront
// parallel processing, slices, tiles, the default scaling lists, a
// size that crops, and Matroska.
func TestDecodeH265MatchesFFmpeg(t *testing.T) {
	d := decodingDevice(t, demux.H265)
	x265 := func(params string) []string {
		return []string{"-c:v", "libx265", "-x265-params", "log-level=none:" + params}
	}
	for _, c := range []struct {
		name, file string
		w, h       int
		args       []string
	}{
		{"B-pyramid, 4 references", "a.mp4", 192, 144, x265("keyint=30:bframes=4:b-pyramid=1:ref=4")},
		{"slice reference picture sets, no wavefronts", "b.mp4", 160, 144, x265("keyint=24:bframes=3:wpp=0:rps-in-sps=0")},
		{"4 slices", "c.mp4", 176, 144, x265("keyint=25:slices=4")},
		{"default scaling lists", "d.mp4", 160, 160, x265("keyint=25:scaling-list=default")},
		{"cropped 270x150", "e.mp4", 270, 150, x265("keyint=25")},
		{"matroska", "f.mkv", 160, 144, x265("keyint=12:bframes=2")},
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

// After a seek, H.265 decoding starts again at a CRA picture, dropping
// the RASL pictures that lead it and predict from before it.
func TestDecodeH265ResetsAtACRA(t *testing.T) {
	d := decodingDevice(t, demux.H265)
	cl := ffmpegClip(t, "a.mp4", 160, 144, 3, "-c:v", "libx265", "-x265-params", "log-level=none:keyint=20:min-keyint=20:open-gop=1:bframes=3")
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
	got := decodeAll(t, v, pkts[key:])
	if len(got) > 0 && got[0].pts < start {
		t.Errorf("first frame after the reset presents at %v, before the CRA at %v", got[0].pts, start)
	}
	checkFrames(t, cl, got, int(start/(40*time.Millisecond)))
}

// After a seek, Reset and the packets from the keyframe decode the
// frames from it on as a decode from the start does, open-GOP keyframes
// that are not IDRs too, whose leading B-frames are dropped.
func TestDecodeH264ResetsAtAKeyframe(t *testing.T) {
	d := decodingDevice(t, demux.H264)
	for _, c := range []struct {
		name string
		args []string
	}{
		{"IDR", []string{"-c:v", "libx264", "-bf", "2", "-x264-params", "keyint=20"}},
		{"open GOP", []string{"-c:v", "libx264", "-bf", "2", "-x264-params", "keyint=20:open-gop=1"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			cl := ffmpegClip(t, "a.mp4", 160, 96, 3, c.args...)
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
			got := decodeAll(t, v, pkts[key:])
			got = slices.DeleteFunc(got, func(f decoded) bool { return f.pts < start })
			from := int(start / (40 * time.Millisecond))
			checkFrames(t, cl, got, from)
		})
	}
}

// A decoded frame draws through the renderer's YCbCr conversion: what
// a Pass makes of its texture is the picture's colours in the matrix
// and range its VUI names (BT.601 limited range unnamed, at this
// size), chroma reconstructed linearly between samples sited as H.264
// sites them: with the even luma columns, between the rows.
func TestDecodedFrameDraws(t *testing.T) {
	d := decodingDevice(t, demux.H264)
	for _, c := range []struct {
		name   string
		args   []string
		matrix YCbCrMatrix
		rng    YCbCrRange
	}{
		{"unnamed", nil, BT601, LimitedRange},
		{"BT.709 full range", []string{"-colorspace", "bt709", "-color_range", "pc"}, BT709, FullRange},
	} {
		t.Run(c.name, func(t *testing.T) {
			cl := ffmpegClip(t, "a.mp4", 174, 98, 0.2, append([]string{"-c:v", "libx264"}, c.args...)...)
			track, pkts := packets(t, cl.path)
			v, err := d.NewVideoDecoder(track)
			if err != nil {
				t.Fatal(err)
			}
			defer v.Close()
			if err := v.Decode(pkts[0]); err != nil {
				t.Fatal(err)
			}
			v.Flush()
			f, err := v.Next()
			if err != nil || f == nil {
				t.Fatalf("frame %v, %v", f, err)
			}
			defer f.Release()
			r := f.Rect()
			if r != image.Rect(0, 0, 174, 98) {
				t.Errorf("rect %v, want the stream's 174x98", r)
			}
			tg, err := d.NewOffscreen(r.Dx(), r.Dy())
			if err != nil {
				t.Fatal(err)
			}
			defer tg.Close()
			runPass(t, d, tg, []image.Rectangle{{Max: tg.Size()}}, func(p *Pass) {
				p.DrawTexture(f.Texture(), FRect{X: float64(r.Min.X), Y: float64(r.Min.Y), W: float64(r.Dx()), H: float64(r.Dy())}, image.Rectangle{Max: tg.Size()}, TransformNormal, 1, Mask{})
			})
			got := readTarget(t, tg)
			nv12 := cl.frames[0]
			w, h := r.Dx(), r.Dy()
			want := make([]byte, 0, w*h*3)
			for y := range h {
				for x := range w {
					u, v := chromaAt(nv12[w*h:], w/2, h/2, float64(x)/2, (float64(y)-0.5)/2)
					px := expectRGB(float64(nv12[y*w+x]), u, v, 8, c.matrix, c.rng)
					want = append(want, px.R, px.G, px.B)
				}
			}
			p := psnr(got, want)
			t.Logf("drawn frame is %.1f dB from the picture's colours", p)
			if p < 40 || math.IsNaN(p) {
				t.Errorf("drawn frame is %.1f dB from the picture's colours, want 40 or more", p)
			}
		})
	}
}

// Decoding a frame, handing it out and releasing it allocates nothing
// once the decoder has its frames.
func TestDecodeDoesNotAllocate(t *testing.T) {
	for _, c := range []struct {
		codec demux.Codec
		args  []string
	}{
		{demux.H264, []string{"-c:v", "libx264", "-bf", "2", "-x264-params", "keyint=250"}},
		{demux.H265, []string{"-c:v", "libx265", "-x265-params", "log-level=none:keyint=250:bframes=3"}},
	} {
		t.Run(c.codec.String(), func(t *testing.T) {
			d := decodingDevice(t, c.codec)
			cl := ffmpegClip(t, "a.mp4", 160, 144, 4, c.args...)
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
		})
	}
}

// chromaAt is the chroma of interleaved plane uv, cw x ch samples,
// interpolated linearly at sample coordinates x, y, clamped to its
// edge.
func chromaAt(uv []byte, cw, ch int, x, y float64) (u, v float64) {
	x, y = max(0, min(x, float64(cw-1))), max(0, min(y, float64(ch-1)))
	x0, y0 := int(x), int(y)
	x1, y1 := min(x0+1, cw-1), min(y0+1, ch-1)
	fx, fy := x-float64(x0), y-float64(y0)
	at := func(c, px, py int) float64 { return float64(uv[py*cw*2+px*2+c]) }
	lerp := func(c int) float64 {
		top := at(c, x0, y0)*(1-fx) + at(c, x1, y0)*fx
		bottom := at(c, x0, y1)*(1-fx) + at(c, x1, y1)*fx
		return top*(1-fy) + bottom*fy
	}
	return lerp(0), lerp(1)
}
