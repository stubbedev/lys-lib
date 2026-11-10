package gpu

import (
	"image"
	"image/color"
	"slices"
	"testing"
	"time"
)

// Encode hands a frame's conversion to the video queue on the GPU: the
// conversion is a submission of the device, in flight when Encode
// returns, and nothing waited for it here. Finish waits for it, the
// buffer free for its next frame after, and the frames so encoded come
// out, the last one too, whose buffer and overlay were closed under its
// conversion: both live until it has read them.
func TestEncodeDoesNotWaitForItsConversion(t *testing.T) {
	for _, codec := range []Codec{H264, H265} {
		t.Run(codec.String(), func(t *testing.T) {
			d := exportingDevice(t)
			if d.video == nil || !slices.Contains(d.video.codecs, codec) {
				t.Skipf("%s encodes no %s on a Vulkan video queue: %v", d.Name(), codec, d.videoWhy[codec])
			}
			cfg := EncoderConfig{Codec: codec, Width: 320, Height: 240, FrameRate: 30, KeyInterval: 4, QP: 20}
			c := openClip(t, d, cfg, func(cfg EncoderConfig) (*Encoder, error) { return newEncoderOn(d, cfg, newVulkanBackend) })
			c.buf.fill(t, encodeFrame(cfg.Width, cfg.Height, 0))
			for frame := range encodeSlots {
				if err := d.Finish(); err != nil {
					t.Fatal(err)
				}
				waits, submitted := d.BlockingWaits(), d.Submissions()
				out, err := c.enc.Encode(c.buf, time.Duration(frame)*time.Second/30)
				if err != nil || len(out) != 0 {
					t.Fatalf("frame %d: %d packets, %v", frame, len(out), err)
				}
				if got := d.BlockingWaits(); got != waits {
					t.Errorf("frame %d: Encode waited here %d times, want none", frame, got-waits)
				}
				if got := d.Submissions(); got != submitted+1 || len(d.inflight) != 1 {
					t.Errorf("frame %d: Encode made %d submissions of the device, %d in flight, want its conversion", frame, got-submitted, len(d.inflight))
				}
			}
			if err := c.enc.SetOverlay(solid(8, 8, color.RGBA{255, 0, 0, 255}), image.Rect(0, 0, 8, 8)); err != nil {
				t.Fatal(err)
			}
			if _, err := c.enc.Encode(c.buf, time.Second); err != nil {
				t.Fatal(err)
			}
			if err := c.enc.SetOverlay(nil, image.Rectangle{}); err != nil {
				t.Fatal(err)
			}
			c.buf.Close()
			out, err := c.enc.Flush()
			if err != nil || len(out) != encodeSlots {
				t.Fatalf("flushed %d packets, %v, want the last %d", len(out), err, encodeSlots)
			}
			for i, p := range out {
				if len(p.Data) == 0 {
					t.Errorf("packet %d is empty", i)
				}
			}
		})
	}
}
