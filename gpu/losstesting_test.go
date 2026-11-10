package gpu

import (
	"errors"
	"image"
	"image/color"
	"slices"
	"testing"

	"github.com/stubbedev/lys-lib/media/demux"
)

// A device works until a test arms a loss, then fails its next
// submission with ErrDeviceLost and everything after it that submits
// or waits for the GPU, as after a GPU reset; closing it and opening
// another recovers, the GPU itself untouched.
func TestLoseForTestingLosesTheDeviceAtItsNextSubmission(t *testing.T) {
	d := openDevice(t)
	tg := newTarget(t, d, 8, 8)
	all := []image.Rectangle{{Max: tg.Size()}}
	draw := func(d *Device, tg *Target) error {
		p, err := d.Begin(tg, all)
		if err != nil {
			return err
		}
		p.Clear(color.RGBA{0, 255, 0, 255})
		if err := p.End(); err != nil {
			return err
		}
		return d.Flush()
	}
	if err := errors.Join(draw(d, tg), d.Finish()); err != nil {
		t.Fatalf("before a loss is armed: %v", err)
	}
	d.LoseForTesting()
	if err := draw(d, tg); !errors.Is(err, ErrDeviceLost) {
		t.Fatalf("the submission after a loss was armed: %v, want ErrDeviceLost", err)
	}
	for name, call := range map[string]func() error{
		"a pass":   func() error { return draw(d, tg) },
		"Finish":   d.Finish,
		"Publish":  func() error { return errors.Join(draw(d, tg), d.Publish(tg)) },
		"a read":   func() error { _, err := tg.ReadPixels(); return err },
		"a second": d.Finish,
	} {
		if err := call(); !errors.Is(err, ErrDeviceLost) {
			t.Errorf("%s on the lost device: %v, want ErrDeviceLost", name, err)
		}
	}
	d.Close()
	next := openDevice(t)
	fresh := newTarget(t, next, 8, 8)
	if err := errors.Join(draw(next, fresh), next.Finish()); err != nil {
		t.Fatalf("the device opened after the lost one: %v", err)
	}
	if got := readTarget(t, fresh).RGBAAt(0, 0); got != (color.RGBA{0, 255, 0, 255}) {
		t.Errorf("the device opened after the lost one drew %v", got)
	}
}

// An armed loss reaches a video decoder through its device: the decode
// that is the next submission fails with ErrDeviceLost, and so do the
// decoder's waits, the frames it handed out and the passes that draw
// them.
func TestLoseForTestingFailsADecoder(t *testing.T) {
	d := decodingDevice(t, demux.H264)
	cl := ffmpegClip(t, "a.mp4", 174, 98, 0.2, "-c:v", "libx264")
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
	d.LoseForTesting()
	if err := v.Decode(pkts[1]); !errors.Is(err, ErrDeviceLost) {
		t.Fatalf("the decode after a loss was armed: %v, want ErrDeviceLost", err)
	}
	if _, err := f.Decoded(); !errors.Is(err, ErrDeviceLost) {
		t.Errorf("Decoded on the lost device: %v, want ErrDeviceLost", err)
	}
	if err := v.Reset(); !errors.Is(err, ErrDeviceLost) {
		t.Errorf("Reset on the lost device: %v, want ErrDeviceLost", err)
	}
	err = endOf(t, d, 64, 36, func(p *Pass) {
		p.DrawFrame(f, FRect{W: 174, H: 98}, image.Rect(0, 0, 64, 36), TransformNormal, 1, Mask{})
	})
	if !errors.Is(err, ErrDeviceLost) {
		t.Errorf("a pass drawing the frame on the lost device: %v, want ErrDeviceLost", err)
	}
}

// An armed loss reaches an encoder through its device: Encode fails
// with ErrDeviceLost.
func TestLoseForTestingFailsAnEncoder(t *testing.T) {
	d := exportingDevice(t)
	if d.video == nil || !slices.Contains(d.video.codecs, H264) {
		t.Skipf("%s encodes no H.264 on a Vulkan video queue: %v", d.Name(), d.videoWhy[H264])
	}
	cfg := EncoderConfig{Codec: H264, Width: 320, Height: 240, FrameRate: 30, KeyInterval: 4, QP: 20}
	c := openClip(t, d, cfg, func(cfg EncoderConfig) (*Encoder, error) { return newEncoderOn(d, cfg, newVulkanBackend) })
	c.buf.fill(t, encodeFrame(cfg.Width, cfg.Height, 0))
	if _, err := c.enc.Encode(c.buf, 0); err != nil {
		t.Fatal(err)
	}
	d.LoseForTesting()
	for frame := 1; frame <= encodeSlots; frame++ {
		if _, err := c.enc.Encode(c.buf, 0); !errors.Is(err, ErrDeviceLost) {
			t.Errorf("Encode %d on the lost device: %v, want ErrDeviceLost", frame, err)
		}
	}
}
