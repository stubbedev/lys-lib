package gpu

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"io"
	"math"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
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
// it, NV12 (P010 for a 10-bit stream) at the stream's size in
// presentation order.
type stream struct {
	path   string
	w, h   int
	frames [][]byte
}

// p010Samples are 10-bit 4:2:0 pictures.
var p010Samples = videoSamples{bits: 10, chroma: chroma420}

// rawFormat is ffmpeg's name of the format the frames it decodes are
// asked for in, for pictures of samples: NV12 itself, and for every
// other kind the planar format the streams are coded in, which
// interleaved lays out as the decoder's pictures. ffmpeg's own
// conversion to P010 leaves the last chroma column of an odd width
// zero.
func rawFormat(s videoSamples) string {
	if s == nv12Samples {
		return "nv12"
	}
	name := "yuv" + strings.ReplaceAll(s.chroma.String(), ":", "") + "p"
	if s.bits > 8 {
		name += strconv.Itoa(int(s.bits)) + "le"
	}
	return name
}

// interleaved lays a w x h planar picture of samples out as the
// decoder's pictures are: luma, then the chroma planes interleaved,
// each sample of more than 8 bits in the high bits of its 16.
func interleaved(planar []byte, w, h int, s videoSamples) []byte {
	cw, ch := s.chromaSize(w, h)
	luma, chroma, bytes := w*h, cw*ch, s.bytes()
	out := make([]byte, len(planar))
	put := func(at, from int) {
		if bytes == 1 {
			out[at] = planar[from]
			return
		}
		binary.LittleEndian.PutUint16(out[2*at:], binary.LittleEndian.Uint16(planar[2*from:])<<(16-s.bits))
	}
	for i := range luma {
		put(i, i)
	}
	for i := range chroma {
		put(luma+2*i, luma+i)
		put(luma+2*i+1, luma+chroma+i)
	}
	return out
}

// ffmpegClip encodes a moving test pattern of w x h (an odd size cropped
// from the even size past it) at 25 frames a second for secs seconds
// with args into a file called name, and decodes it back with ffmpeg.
func ffmpegClip(t *testing.T, name string, w, h int, secs float64, args ...string) stream {
	t.Helper()
	return ffmpegPasses(t, name, w, h, secs, args)
}

// ffmpegPasses is ffmpegClip for an encode in passes, each with its
// arguments in turn; the last pass writes the file.
func ffmpegPasses(t *testing.T, name string, w, h int, secs float64, passes ...[]string) stream {
	t.Helper()
	return encodeClip(t, name, w, h, secs, nv12Samples, passes...)
}

// encodeClip is ffmpegPasses with the frames decoded back as format.
func encodeClip(t *testing.T, name string, w, h int, secs float64, format videoSamples, passes ...[]string) stream {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	src := fmt.Sprintf("testsrc2=size=%dx%d:rate=25:duration=%g,crop=%d:%d:0:0:exact=1", w+w%2, h+h%2, secs, w, h)
	for i, args := range passes {
		enc := append([]string{"-v", "error", "-y", "-f", "lavfi", "-i", src, "-pix_fmt", "yuv420p", "-passlogfile", filepath.Join(dir, "pass")}, args...)
		if i < len(passes)-1 {
			enc = append(enc, "-f", "null", "-")
		} else {
			enc = append(enc, path)
		}
		runFFmpeg(t, enc...)
	}
	return ffmpegDecoded(t, path, w, h, format)
}

// ffmpegDecoded is the w x h stream at path with the frames ffmpeg
// decodes from it as format.
func ffmpegDecoded(t *testing.T, path string, w, h int, format videoSamples) stream {
	t.Helper()
	raw := runFFmpeg(t, "-v", "error", "-i", path, "-fps_mode", "passthrough", "-f", "rawvideo", "-pix_fmt", rawFormat(format), "-")
	size := pictureSize(w, h, format)
	if len(raw)%size != 0 || len(raw) == 0 {
		t.Fatalf("ffmpeg decoded %d bytes of %dx%d %s frames", len(raw), w, h, format)
	}
	c := stream{path: path, w: w, h: h}
	for len(raw) > 0 {
		frame := raw[:size]
		if format != nv12Samples {
			frame = interleaved(frame, w, h, format)
		}
		c.frames, raw = append(c.frames, frame), raw[size:]
	}
	return c
}

func runFFmpeg(t *testing.T, args ...string) []byte {
	t.Helper()
	return runCommand(t, "ffmpeg", args...)
}

// runCommand runs name, one of ffmpeg's programs, with args, and
// returns what it writes.
func runCommand(t *testing.T, name string, args ...string) []byte {
	t.Helper()
	cmd := exec.Command(name, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("%s %v: %v: %s", name, args, err, stderr.Bytes())
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

// decoded is a frame read back: its PTS, its size and its picture,
// cropped to it.
type decoded struct {
	pts     time.Duration
	size    image.Point
	picture []byte
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
		f := v.Next()
		if f == nil {
			return out
		}
		out = append(out, decoded{f.PTS(), f.Size(), readPicture(t, f)})
		f.Release()
	}
}

// readPicture copies f's picture back to the CPU through the graphics
// queue, which waits for its decode: its luma, then its interleaved
// chroma, as the frame's format holds its samples.
func readPicture(t *testing.T, f *VideoFrame) []byte {
	t.Helper()
	d := f.v.d
	w, h := f.extent[0], f.extent[1]
	bps := f.samples.bytes()
	cw, ch := f.samples.chromaSize(w, h)
	buf, err := d.newReadBuffer(pictureSize(w, h, f.samples), vk.BufferUsageTransferDstBit, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer buf.close()
	img := f.tex.image
	d.awaitTimeline(f.decoding())
	err = d.submit(func(cb vk.CommandBuffer) {
		fullBarrier(d, cb, img, vk.ImageLayoutShaderReadOnlyOptimal, vk.ImageLayoutTransferSrcOptimal, vk.QueueFamilyIgnored, vk.QueueFamilyIgnored)
		regions := [2]vk.BufferImageCopy{
			{ImageSubresource: vk.ImageSubresourceLayers{AspectMask: vk.ImageAspectFlags(vk.ImageAspectPlane0Bit), LayerCount: 1}, ImageExtent: extent3D(w, h)},
			{BufferOffset: vk.DeviceSize(w * h * bps), ImageSubresource: vk.ImageSubresourceLayers{AspectMask: vk.ImageAspectFlags(vk.ImageAspectPlane1Bit), LayerCount: 1}, ImageExtent: extent3D(cw, ch)},
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
	r := f.rect
	if r.Min.X%2 != 0 || r.Min.Y%2 != 0 {
		t.Fatalf("rect %v starts within a chroma sample", r)
	}
	out := make([]byte, 0, pictureSize(r.Dx(), r.Dy(), f.samples))
	stride := w * bps
	for y := r.Min.Y; y < r.Max.Y; y++ {
		at := y*stride + r.Min.X*bps
		out = append(out, buf.mapped[at:at+r.Dx()*bps]...)
	}
	sx, sy := w/cw, h/ch
	chromaStride := 2 * cw * bps
	chromaW, chromaH := f.samples.chromaSize(r.Dx(), r.Dy())
	for y := range chromaH {
		at := h*stride + (r.Min.Y/sy+y)*chromaStride + 2*(r.Min.X/sx)*bps
		out = append(out, buf.mapped[at:at+2*chromaW*bps]...)
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
		if !bytes.Equal(g.picture, want[i]) {
			t.Errorf("frame %d (PTS %v) differs from ffmpeg's in %d of %d bytes", from+i, g.pts, differing(g.picture, want[i]), len(want[i]))
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
// a Pass makes of it (DrawFrame) is the picture's colours in the matrix
// and range its VUI names (BT.601 limited range unnamed, at this
// size), chroma reconstructed linearly between samples sited as H.264
// and H.265 site them: with the even luma columns, between the rows. A
// 10-bit or 12-bit picture draws at its depth, and a 4:4:4 one with
// the chroma of each pixel its own. Only its picture draws, not the
// padding the codec adds around it.
func TestDecodedFrameDraws(t *testing.T) {
	for _, c := range []struct {
		name   string
		codec  demux.Codec
		w, h   int
		args   []string
		format videoSamples
		matrix YCbCrMatrix
		rng    YCbCrRange
	}{
		{"unnamed", demux.H264, 174, 98, []string{"-c:v", "libx264"}, nv12Samples, BT601, LimitedRange},
		{"BT.709 full range", demux.H264, 174, 98, []string{"-c:v", "libx264", "-colorspace", "bt709", "-color_range", "pc"}, nv12Samples, BT709, FullRange},
		{"10-bit BT.2020", demux.H265, 174, 144, []string{"-c:v", "libx265", "-pix_fmt", "yuv420p10le", "-colorspace", "bt2020nc", "-x265-params", "log-level=none"}, p010Samples, BT2020, LimitedRange},
		{"12-bit", demux.H265, 174, 144, []string{"-c:v", "libx265", "-pix_fmt", "yuv420p12le", "-colorspace", "bt709", "-x265-params", "log-level=none"}, videoSamples{12, chroma420}, BT709, LimitedRange},
		{"4:4:4", demux.H265, 174, 144, []string{"-c:v", "libx265", "-pix_fmt", "yuv444p", "-colorspace", "bt709", "-x265-params", "log-level=none"}, videoSamples{8, chroma444}, BT709, LimitedRange},
		{"10-bit 4:4:4 full range", demux.H265, 174, 144, []string{"-c:v", "libx265", "-pix_fmt", "yuv444p10le", "-colorspace", "bt709", "-color_range", "pc", "-x265-params", "log-level=none"}, videoSamples{10, chroma444}, BT709, FullRange},
	} {
		t.Run(c.name, func(t *testing.T) {
			d := decodingDevice(t, c.codec)
			cl := encodeClip(t, "a.mp4", c.w, c.h, 0.2, c.format, c.args)
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
			f := v.Next()
			if f == nil {
				t.Fatal("the first packet completed no frame")
			}
			defer f.Release()
			if f.samples != c.format {
				t.Errorf("a frame of %s samples, want %s", f.samples, c.format)
			}
			size := f.Size()
			if size != image.Pt(c.w, c.h) {
				t.Errorf("size %v, want the stream's %dx%d", size, c.w, c.h)
			}
			tg, err := d.NewOffscreen(size.X, size.Y)
			if err != nil {
				t.Fatal(err)
			}
			defer tg.Close()
			runPass(t, d, tg, []image.Rectangle{{Max: tg.Size()}}, func(p *Pass) {
				p.DrawFrame(f, FRect{W: float64(size.X), H: float64(size.Y)}, image.Rectangle{Max: tg.Size()}, TransformNormal, 1, Mask{})
			})
			got := readTarget(t, tg)
			pic := newSamples(cl.frames[0], c.format)
			w, h := size.X, size.Y
			want := make([]byte, 0, w*h*3)
			cw, ch := c.format.chromaSize(w, h)
			for y := range h {
				for x := range w {
					cx, cy := float64(x), float64(y)
					if c.format.chroma == chroma420 {
						cx, cy = cx/2, (cy-0.5)/2
					}
					u, v := chromaAt(pic, w*h, cw, ch, cx, cy)
					px := expectRGB(pic.at(y*w+x), u, v, pic.bits, c.matrix, c.rng)
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

// samples reads the samples of a picture by index, at their depth.
type samples struct {
	b     []byte
	bytes int
	bits  int
}

func newSamples(b []byte, format videoSamples) samples {
	return samples{b, format.bytes(), int(format.bits)}
}

// at is sample i.
func (s samples) at(i int) float64 {
	if s.bytes == 2 {
		return float64(binary.LittleEndian.Uint16(s.b[2*i:]) >> (16 - s.bits))
	}
	return float64(s.b[i])
}

// chromaAt is the chroma of the interleaved plane of pic from sample
// from on, cw x ch samples, interpolated linearly at sample coordinates
// x, y, clamped to its edge.
func chromaAt(pic samples, from, cw, ch int, x, y float64) (u, v float64) {
	x, y = max(0, min(x, float64(cw-1))), max(0, min(y, float64(ch-1)))
	x0, y0 := int(x), int(y)
	x1, y1 := min(x0+1, cw-1), min(y0+1, ch-1)
	fx, fy := x-float64(x0), y-float64(y0)
	at := func(c, px, py int) float64 { return pic.at(from + py*cw*2 + px*2 + c) }
	lerp := func(c int) float64 {
		top := at(c, x0, y0)*(1-fx) + at(c, x1, y0)*fx
		bottom := at(c, x0, y1)*(1-fx) + at(c, x1, y1)*fx
		return top*(1-fy) + bottom*fy
	}
	return lerp(0), lerp(1)
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
		})
	}
}

// pictureSize is the bytes of a w x h picture of format: its luma and a
// pair of samples for each chroma sample, the odd last column and row
// of subsampled chroma included.
func pictureSize(w, h int, format videoSamples) int {
	cw, ch := format.chromaSize(w, h)
	return (w*h + 2*cw*ch) * format.bytes()
}
