package gpu

import (
	"bytes"
	"errors"
	"image"
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
		toDst := imageBarrier(b.e.image, colorRange, vk.ImageLayoutUndefined, vk.ImageLayoutTransferDstOptimal)
		toDst.DstStageMask, toDst.DstAccessMask = vk.PipelineStageFlags2(vk.PipelineStage2CopyBit), vk.AccessFlags2(vk.Access2TransferWriteBit)
		barrier2(d, cb, nil, toDst)
		region := vk.BufferImageCopy{
			ImageSubresource: vk.ImageSubresourceLayers{AspectMask: vk.ImageAspectFlags(vk.ImageAspectColorBit), LayerCount: 1},
			ImageExtent:      vk.Extent3D{Width: uint32(w), Height: uint32(h), Depth: 1},
		}
		d.cmds.CmdCopyBufferToImage(cb, staging.buf, b.e.image, vk.ImageLayoutTransferDstOptimal, 1, &region)
		release := imageBarrier(b.e.image, colorRange, vk.ImageLayoutTransferDstOptimal, vk.ImageLayoutGeneral)
		release.SrcQueueFamilyIndex, release.DstQueueFamilyIndex = d.family, vk.QueueFamilyForeignExt
		release.SrcStageMask, release.SrcAccessMask = vk.PipelineStageFlags2(vk.PipelineStage2CopyBit), vk.AccessFlags2(vk.Access2TransferWriteBit)
		barrier2(d, cb, nil, release)
	})
	if err != nil {
		t.Fatal(err)
	}
}

// encodeFrame is frame i of the test clip: a diagonal gradient that
// drifts, with a white square crossing it and a red one bouncing, so
// P frames have motion to predict and edges to keep.
func encodeFrame(w, h, i int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	sq := 24 + i%7
	sx, sy := (i*9)%max(w-sq, 1), (i*5)%max(h-sq, 1)
	rx := w - sq - (i*6)%max(w-sq, 1)
	for y := range h {
		for x := range w {
			o := y*img.Stride + x*4
			img.Pix[o], img.Pix[o+1], img.Pix[o+2], img.Pix[o+3] = uint8((x+2*i)*255/w), uint8(y*255/h), uint8(((x+y)/2+3*i)%256), 0xff
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

// A clip encoded from GPU buffers decodes in ffmpeg to every frame at
// its size, its keyframes where the key interval puts them, close to
// the source: at even and odd sizes, the odd one coded a pixel larger
// and cropped back. 4:2:0 alone holds this clip's hard colored edges
// to about 34 dB; a wrong matrix, crop or reference falls far below
// the 28 asked.
func TestEncoderH264Decodes(t *testing.T) {
	d := exportingDevice(t)
	if !slices.Contains(d.Codecs(), H264) {
		_, err := d.NewEncoder(EncoderConfig{Codec: H264, Width: 64, Height: 64, FrameRate: 30, KeyInterval: 30, QP: 20})
		if !errors.Is(err, ErrNoVideoEncoder) {
			t.Fatalf("NewEncoder on %s, which encodes no H.264: %v, want ErrNoVideoEncoder", d.Name(), err)
		}
		t.Skipf("%s encodes no H.264", d.Name())
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("no ffmpeg to decode with")
	}
	for _, size := range []image.Point{{640, 360}, {333, 241}} {
		t.Run(strconv.Itoa(size.X)+"x"+strconv.Itoa(size.Y), func(t *testing.T) {
			const frames, keyInterval = 40, 15
			mods, err := d.ExportModifiers(dmabuf.FormatXRGB8888)
			if err != nil {
				t.Fatal(err)
			}
			buf, err := d.NewBuffer(size.X, size.Y, dmabuf.FormatXRGB8888, mods)
			if err != nil {
				t.Fatal(err)
			}
			defer buf.Close()
			enc, err := d.NewEncoder(EncoderConfig{Codec: H264, Width: size.X, Height: size.Y, FrameRate: 30, KeyInterval: keyInterval, QP: 20})
			if err != nil {
				t.Fatal(err)
			}
			defer enc.Close()
			var pkts []Packet
			src := make([]*image.RGBA, frames)
			for i := range frames {
				src[i] = encodeFrame(size.X, size.Y, i)
				buf.fill(t, src[i])
				out, err := enc.Encode(buf, time.Duration(i)*time.Second/30)
				if err != nil {
					t.Fatalf("frame %d: %v", i, err)
				}
				pkts = append(pkts, out...)
			}
			out, err := enc.Flush()
			if err != nil {
				t.Fatal(err)
			}
			pkts = append(pkts, out...)
			if len(pkts) != frames {
				t.Fatalf("%d packets for %d frames", len(pkts), frames)
			}
			var stream bytes.Buffer
			for _, ps := range enc.ParameterSets() {
				stream.Write([]byte{0, 0, 0, 1})
				stream.Write(ps)
			}
			for i, p := range pkts {
				if want := time.Duration(i) * time.Second / 30; p.PTS != want || p.Key != (i%keyInterval == 0) {
					t.Errorf("packet %d: pts %v key %v, want %v and %v", i, p.PTS, p.Key, want, i%keyInterval == 0)
				}
				stream.Write(p.Data)
			}
			path := filepath.Join(t.TempDir(), "clip.h264")
			if err := os.WriteFile(path, stream.Bytes(), 0o600); err != nil {
				t.Fatal(err)
			}
			var stderr bytes.Buffer
			cmd := exec.Command(ffmpeg, "-v", "error", "-f", "h264", "-i", path, "-f", "rawvideo", "-pix_fmt", "rgb24", "-")
			cmd.Stderr = &stderr
			rgb, err := cmd.Output()
			if err != nil || stderr.Len() > 0 {
				t.Fatalf("ffmpeg decode: %v: %s", err, strings.TrimSpace(stderr.String()))
			}
			fw, fh := evenUp(size.X), evenUp(size.Y)
			if len(rgb) != frames*fw*fh*3 {
				t.Fatalf("decoded %d bytes, want %d frames of %dx%d", len(rgb), frames, fw, fh)
			}
			worst := math.Inf(1)
			for i := range frames {
				frame := rgb[i*fw*fh*3 : (i+1)*fw*fh*3]
				crop := make([]byte, 0, size.X*size.Y*3)
				for y := range size.Y {
					crop = append(crop, frame[y*fw*3:y*fw*3+size.X*3]...)
				}
				worst = min(worst, psnr(src[i], crop))
			}
			t.Logf("%d bytes for %d frames, worst frame %.1f dB", stream.Len(), frames, worst)
			if worst < 28 {
				t.Errorf("worst frame decodes at %.1f dB PSNR, want at least 28 at QP 20", worst)
			}
		})
	}
}
