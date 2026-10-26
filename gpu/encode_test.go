package gpu

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/draw"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stubbedev/lys-ui/dmabuf"
	"github.com/stubbedev/lys-ui/media/mux"
	"github.com/stubbedev/lys-ui/third_party/vk"
)

// fill writes img's pixels into b as a compositor's copy would leave
// them, and hands b back to its foreign owner.
func (b *Buffer) fill(t *testing.T, img *image.RGBA) {
	t.Helper()
	d := b.d
	w, h := b.e.buf.Width, b.e.buf.Height
	staging, err := d.newBuffer(w*h*4, vk.BufferUsageTransferSrcBit)
	if err != nil {
		t.Fatal(err)
	}
	defer staging.close()
	for i := 0; i < w*h*4; i += 4 {
		staging.mapped[i], staging.mapped[i+1], staging.mapped[i+2], staging.mapped[i+3] = img.Pix[i+2], img.Pix[i+1], img.Pix[i], 0xff
	}
	err = d.submit(func(cb vk.CommandBuffer) {
		fullBarrier(d, cb, b.e.image, vk.ImageLayoutUndefined, vk.ImageLayoutTransferDstOptimal, vk.QueueFamilyIgnored, vk.QueueFamilyIgnored)
		region := vk.BufferImageCopy{
			ImageSubresource: vk.ImageSubresourceLayers{AspectMask: vk.ImageAspectFlags(vk.ImageAspectColorBit), LayerCount: 1},
			ImageExtent:      vk.Extent3D{Width: uint32(w), Height: uint32(h), Depth: 1},
		}
		d.cmds.CmdCopyBufferToImage(cb, staging.buf, b.e.image, vk.ImageLayoutTransferDstOptimal, 1, &region)
		fullBarrier(d, cb, b.e.image, vk.ImageLayoutTransferDstOptimal, vk.ImageLayoutGeneral, d.family, vk.QueueFamilyForeignExt)
	})
	if err != nil {
		t.Fatal(err)
	}
}

// encodeFrame is frame i of the test clip: smooth gradients that
// drift, with a white square crossing them and a red one bouncing, so
// P frames have motion to predict and edges to keep.
func encodeFrame(w, h, i int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	sq := 24 + i%7
	sx, sy := (i*9)%max(w-sq, 1), (i*5)%max(h-sq, 1)
	rx := w - sq - (i*6)%max(w-sq, 1)
	for y := range h {
		for x := range w {
			o := y*img.Stride + x*4
			img.Pix[o], img.Pix[o+1], img.Pix[o+2], img.Pix[o+3] = uint8(min((x+2*i)*255/w, 255)), uint8(y*255/h), uint8(255-abs(((x+y)/2+3*i)%510-255)), 0xff
			if x >= sx && x < sx+sq && y >= sy && y < sy+sq {
				img.Pix[o], img.Pix[o+1], img.Pix[o+2] = 0xff, 0xff, 0xff
			}
			if x >= rx && x < rx+sq && y >= h/2 && y < h/2+sq {
				img.Pix[o], img.Pix[o+1], img.Pix[o+2] = 0xe0, 0x20, 0x20
			}
		}
	}
	return img
}

// psnr is the peak signal-to-noise ratio of decoded rgb24 against src.
func psnr(src *image.RGBA, rgb []byte) float64 {
	b := src.Bounds()
	var sum float64
	for y := range b.Dy() {
		for x := range b.Dx() {
			for c := range 3 {
				e := float64(src.Pix[y*src.Stride+x*4+c]) - float64(rgb[(y*b.Dx()+x)*3+c])
				sum += e * e
			}
		}
	}
	mse := sum / float64(b.Dx()*b.Dy()*3)
	if mse == 0 {
		return math.Inf(1)
	}
	return 10 * math.Log10(255*255/mse)
}

// encodingDevice is an exporting device that encodes c, with ffmpeg
// to decode what it writes; the test skips without either, after
// checking that NewEncoder says ErrNoVideoEncoder on a device without.
func encodingDevice(t *testing.T, c Codec) (*Device, string) {
	t.Helper()
	d := exportingDevice(t)
	if !slices.Contains(d.Codecs(), c) {
		_, err := d.NewEncoder(EncoderConfig{Codec: c, Width: 64, Height: 64, FrameRate: 30, KeyInterval: 30, QP: 20})
		if !errors.Is(err, ErrNoVideoEncoder) {
			t.Fatalf("NewEncoder on %s, which encodes no %s: %v, want ErrNoVideoEncoder", d.Name(), c, err)
		}
		t.Skipf("%s encodes no %s", d.Name(), c)
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("no ffmpeg to decode with")
	}
	return d, ffmpeg
}

// ffmpegFormats name each codec's elementary stream for ffmpeg.
var ffmpegFormats = map[Codec]string{H264: "h264", H265: "hevc"}

// clip is an encoder, the buffer it encodes from and what it wrote.
type clip struct {
	t      *testing.T
	cfg    EncoderConfig
	enc    *Encoder
	buf    *Buffer
	pkts   []Packet
	frames int
}

func newClip(t *testing.T, d *Device, cfg EncoderConfig) *clip {
	t.Helper()
	mods, err := d.ExportModifiers(dmabuf.FormatXRGB8888)
	if err != nil {
		t.Fatal(err)
	}
	buf, err := d.NewBuffer(cfg.Width, cfg.Height, dmabuf.FormatXRGB8888, mods)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(buf.Close)
	enc, err := d.NewEncoder(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(enc.Close)
	return &clip{t: t, cfg: cfg, enc: enc, buf: buf}
}

// frame encodes the buffer's pixels as the next frame, img written
// into it first unless nil.
func (c *clip) frame(img *image.RGBA) {
	c.t.Helper()
	if img != nil {
		c.buf.fill(c.t, img)
	}
	out, err := c.enc.Encode(c.buf, time.Duration(c.frames)*time.Second/30)
	if err != nil {
		c.t.Fatalf("frame %d: %v", c.frames, err)
	}
	c.pkts = append(c.pkts, out...)
	c.frames++
}

// decode flushes the encoder, checks every frame's packet, its PTS and
// keyframe flag, and decodes the stream with ffmpeg into rgb24 frames
// cropped to the encoder's size.
func (c *clip) decode(ffmpeg string) [][]byte {
	t := c.t
	t.Helper()
	out, err := c.enc.Flush()
	if err != nil {
		t.Fatal(err)
	}
	c.pkts = append(c.pkts, out...)
	if len(c.pkts) != c.frames {
		t.Fatalf("%d packets for %d frames", len(c.pkts), c.frames)
	}
	var stream bytes.Buffer
	for _, ps := range c.enc.ParameterSets() {
		stream.Write([]byte{0, 0, 0, 1})
		stream.Write(ps)
	}
	for i, p := range c.pkts {
		key := i%c.cfg.KeyInterval == 0
		if want := time.Duration(i) * time.Second / 30; p.PTS != want || p.Key != key {
			t.Errorf("packet %d: pts %v key %v, want %v and %v", i, p.PTS, p.Key, want, key)
		}
		stream.Write(p.Data)
	}
	path := filepath.Join(t.TempDir(), "clip")
	if err := os.WriteFile(path, stream.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd := exec.Command(ffmpeg, "-v", "error", "-f", ffmpegFormats[c.cfg.Codec], "-i", path, "-f", "rawvideo", "-pix_fmt", "rgb24", "-")
	cmd.Stderr = &stderr
	rgb, err := cmd.Output()
	if err != nil || stderr.Len() > 0 {
		t.Fatalf("ffmpeg decode: %v: %s", err, strings.TrimSpace(stderr.String()))
	}
	w, h := c.cfg.Width, c.cfg.Height
	fw, fh := evenUp(w), evenUp(h)
	if len(rgb) != c.frames*fw*fh*3 {
		t.Fatalf("decoded %d bytes, want %d frames of %dx%d", len(rgb), c.frames, fw, fh)
	}
	t.Logf("%d bytes for %d frames", stream.Len(), c.frames)
	frames := make([][]byte, c.frames)
	for i := range frames {
		frame := rgb[i*fw*fh*3 : (i+1)*fw*fh*3]
		crop := make([]byte, 0, w*h*3)
		for y := range h {
			crop = append(crop, frame[y*fw*3:y*fw*3+w*3]...)
		}
		frames[i] = crop
	}
	return frames
}

// subsampled is each frame's PSNR through 4:2:0 alone (ffmpeg's BT.709
// limited-range round trip), the most an encoder of NV12 keeps.
func subsampled(t *testing.T, ffmpeg string, src []*image.RGBA) []float64 {
	t.Helper()
	b := src[0].Rect
	var raw bytes.Buffer
	for _, f := range src {
		for y := range b.Dy() {
			for x := range b.Dx() {
				raw.Write(f.Pix[y*f.Stride+x*4 : y*f.Stride+x*4+3])
			}
		}
	}
	cmd := exec.Command(ffmpeg, "-v", "error", "-f", "rawvideo", "-pix_fmt", "rgb24", "-s", strconv.Itoa(b.Dx())+"x"+strconv.Itoa(b.Dy()), "-i", "-",
		"-vf", "scale=out_color_matrix=bt709:out_range=tv,format=yuv420p,scale=in_color_matrix=bt709:in_range=tv,format=rgb24", "-f", "rawvideo", "-")
	cmd.Stdin = &raw
	back, err := cmd.Output()
	if err != nil {
		t.Fatalf("ffmpeg 4:2:0 round trip: %v", err)
	}
	n := b.Dx() * b.Dy() * 3
	out := make([]float64, len(src))
	for i := range src {
		out[i] = psnr(src[i], back[i*n:(i+1)*n])
	}
	return out
}

// A clip encoded from GPU buffers decodes in ffmpeg to every frame at
// its size, its keyframes where the key interval puts them, close to
// the source: at even and odd sizes, the odd one coded a pixel larger
// and cropped back, and at one below the device's smallest coded size
// (160x64 on NVIDIA), padded up to it. Each frame comes within 6 dB
// of what 4:2:0 alone keeps of it, or of 40 dB where 4:2:0 keeps more
// (QP 20 costs about that much on smooth content); a wrong matrix, crop
// or reference falls far below.
func TestEncoderDecodes(t *testing.T) {
	for _, codec := range []Codec{H264, H265} {
		t.Run(codec.String(), func(t *testing.T) {
			d, ffmpeg := encodingDevice(t, codec)
			for _, size := range []image.Point{{640, 360}, {333, 241}, {64, 48}} {
				t.Run(strconv.Itoa(size.X)+"x"+strconv.Itoa(size.Y), func(t *testing.T) {
					c := newClip(t, d, EncoderConfig{Codec: codec, Width: size.X, Height: size.Y, FrameRate: 30, KeyInterval: 15, QP: 20})
					src := make([]*image.RGBA, 40)
					for i := range src {
						src[i] = encodeFrame(size.X, size.Y, i)
						c.frame(src[i])
					}
					ceiling := subsampled(t, ffmpeg, src)
					worst := math.Inf(1)
					for i, f := range c.decode(ffmpeg) {
						got := psnr(src[i], f)
						worst = min(worst, got)
						if want := min(ceiling[i], 40) - 6; got < want {
							t.Errorf("frame %d decodes at %.1f dB PSNR, want %.1f at QP 20 (4:2:0 alone keeps %.1f)", i, got, want, ceiling[i])
						}
					}
					t.Logf("worst frame %.1f dB", worst)
				})
			}
		})
	}
}

// solid is a w x h picture of one color.
func solid(w, h int, c color.RGBA) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(img, img.Rect, image.NewUniform(c), image.Point{}, draw.Src)
	return img
}

// near reports whether the rgb24 pixel at (x, y) of a w-wide frame is
// within 40 of c in every channel.
func near(frame []byte, w, x, y int, c color.RGBA) bool {
	p := frame[(y*w+x)*3:]
	for i, v := range []uint8{c.R, c.G, c.B} {
		if d := int(p[i]) - int(v); d < -40 || d > 40 {
			return false
		}
	}
	return true
}

// An overlay shows in the frames after SetOverlay, scaled to its
// rectangle: a two-pixel picture fills its rectangle's halves; a new
// picture of another size replaces it; nil takes it away again.
func TestEncoderOverlay(t *testing.T) {
	d, ffmpeg := encodingDevice(t, H264)
	const w, h = 320, 192
	c := newClip(t, d, EncoderConfig{Codec: H264, Width: w, Height: h, FrameRate: 30, KeyInterval: 30, QP: 20})
	blue, green, red, white := color.RGBA{30, 60, 200, 255}, color.RGBA{20, 200, 40, 255}, color.RGBA{220, 30, 30, 255}, color.RGBA{250, 250, 250, 255}
	c.frame(solid(w, h, blue))
	c.frame(nil)
	halves := image.NewRGBA(image.Rect(0, 0, 2, 1))
	halves.SetRGBA(0, 0, green)
	halves.SetRGBA(1, 0, red)
	if err := c.enc.SetOverlay(halves, image.Rect(64, 32, 192, 128)); err != nil {
		t.Fatal(err)
	}
	c.frame(nil)
	c.frame(nil)
	if err := c.enc.SetOverlay(solid(8, 8, white), image.Rect(200, 100, 360, 240)); err != nil {
		t.Fatal(err)
	}
	c.frame(nil)
	c.frame(nil)
	if err := c.enc.SetOverlay(nil, image.Rectangle{}); err != nil {
		t.Fatal(err)
	}
	c.frame(nil)
	c.frame(nil)
	frames := c.decode(ffmpeg)
	for _, p := range []struct {
		frame, x, y int
		want        color.RGBA
	}{
		{1, 100, 64, blue},
		{1, 250, 150, blue},
		{3, 76, 80, green},
		{3, 180, 80, red},
		{3, 40, 80, blue},
		{3, 128, 140, blue},
		{5, 260, 150, white},
		{5, 310, 185, white},
		{5, 76, 80, blue},
		{7, 260, 150, blue},
		{7, 76, 80, blue},
	} {
		if !near(frames[p.frame], w, p.x, p.y, p.want) {
			q := frames[p.frame][(p.y*w+p.x)*3:]
			t.Errorf("frame %d at (%d, %d) is %v, want about %v", p.frame, p.x, p.y, q[:3], p.want)
		}
	}
}

// SetOverlay refuses an empty rectangle and one off the frame, and
// works on no closed encoder.
func TestEncoderOverlayRefuses(t *testing.T) {
	d, _ := encodingDevice(t, H264)
	c := newClip(t, d, EncoderConfig{Codec: H264, Width: 128, Height: 64, FrameRate: 30, KeyInterval: 30, QP: 20})
	img := solid(4, 4, color.RGBA{A: 255})
	for _, r := range []image.Rectangle{{}, image.Rect(10, 10, 10, 40), image.Rect(128, 0, 160, 32), image.Rect(-40, -40, 0, 0)} {
		if err := c.enc.SetOverlay(img, r); err == nil {
			t.Errorf("SetOverlay into %v succeeded", r)
		}
	}
	if err := c.enc.SetOverlay(img, image.Rect(-8, -8, 8, 8)); err != nil {
		t.Errorf("an overlay over the frame's corner: %v", err)
	}
	c.enc.Close()
	if err := c.enc.SetOverlay(img, image.Rect(0, 0, 8, 8)); err == nil {
		t.Error("SetOverlay on a closed encoder succeeded")
	}
}

func abs(n int) int { return max(n, -n) }

// A Buffer reads back what was copied into it, opaque in XRGB8888.
func TestBufferReadsBack(t *testing.T) {
	d := exportingDevice(t)
	mods, err := d.ExportModifiers(dmabuf.FormatXRGB8888)
	if err != nil {
		t.Fatal(err)
	}
	buf, err := d.NewBuffer(48, 20, dmabuf.FormatXRGB8888, mods)
	if err != nil {
		t.Fatal(err)
	}
	defer buf.Close()
	src := encodeFrame(48, 20, 3)
	buf.fill(t, src)
	got, err := buf.ReadPixels()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.Pix, src.Pix) {
		t.Errorf("read back %v..., want %v...", got.Pix[:16], src.Pix[:16])
	}
	buf.Close()
	if _, err := buf.ReadPixels(); err == nil {
		t.Error("a closed buffer read back")
	}
}

// What the encoder writes muxes as the recorder muxes it: its
// parameter sets describe the track and its packets are the samples,
// in MP4 and in Matroska, and ffprobe reads every frame back.
func TestEncoderMuxes(t *testing.T) {
	for _, codec := range []Codec{H264, H265} {
		t.Run(codec.String(), func(t *testing.T) {
			d, ffmpeg := encodingDevice(t, codec)
			ffprobe := filepath.Join(filepath.Dir(ffmpeg), "ffprobe")
			const frames = 20
			c := newClip(t, d, EncoderConfig{Codec: codec, Width: 320, Height: 180, FrameRate: 30, KeyInterval: 10, QP: 24})
			for i := range frames {
				c.frame(encodeFrame(320, 180, i))
			}
			out, err := c.enc.Flush()
			if err != nil {
				t.Fatal(err)
			}
			c.pkts = append(c.pkts, out...)
			ps := c.enc.ParameterSets()
			track := mux.Track(mux.H264{SPS: ps[0], PPS: ps[1]})
			if codec == H265 {
				track = mux.H265{VPS: ps[0], SPS: ps[1], PPS: ps[2]}
			}
			for _, kind := range []string{"mp4", "mkv"} {
				var file bytes.Buffer
				var m interface {
					WriteSample(mux.Sample) error
					Close() error
				}
				if kind == "mp4" {
					m, err = mux.NewMP4(&file, track)
				} else {
					m, err = mux.NewMKV(&file, track)
				}
				if err != nil {
					t.Fatal(err)
				}
				for _, p := range c.pkts {
					if err := m.WriteSample(mux.Sample{PTS: p.PTS, Data: p.Data}); err != nil {
						t.Fatal(err)
					}
				}
				if err := m.Close(); err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(t.TempDir(), "clip."+kind)
				if err := os.WriteFile(path, file.Bytes(), 0o600); err != nil {
					t.Fatal(err)
				}
				probe, err := exec.Command(ffprobe, "-v", "error", "-count_frames", "-select_streams", "v:0",
					"-show_entries", "stream=codec_name,width,height,nb_read_frames", "-of", "csv=p=0", path).Output()
				if err != nil {
					t.Fatalf("ffprobe %s: %v", kind, err)
				}
				want := map[Codec]string{H264: "h264", H265: "hevc"}[codec] + ",320,180," + strconv.Itoa(frames)
				if got := strings.TrimSpace(string(probe)); got != want {
					t.Errorf("%s probes as %q, want %q", kind, got, want)
				}
			}
		})
	}
}
