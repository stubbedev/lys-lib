package gpu

import (
	"image"
	"image/color"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-lib/drm"
	"github.com/stubbedev/lys-lib/third_party/vk"
)

// vulkanClip is a clip encoding codec on d's Vulkan video queue; the
// test skips on a device without one.
func vulkanClip(t *testing.T, d *Device, codec Codec) *clip {
	t.Helper()
	if d.video == nil || !slices.Contains(d.video.codecs, codec) {
		t.Skipf("%s encodes no %s on a Vulkan video queue: %v", d.Name(), codec, d.videoWhy[codec])
	}
	cfg := EncoderConfig{Codec: codec, Width: 320, Height: 240, FrameRate: 30, KeyInterval: 4, QP: 20}
	c := openClip(t, d, cfg, func(cfg EncoderConfig) (*Encoder, error) { return newEncoderOn(d, cfg, newVulkanBackend) })
	c.buf.fill(t, encodeFrame(cfg.Width, cfg.Height, 0))
	return c
}

// Encode hands its buffer back behind a fence where the device attaches
// fences to dmabufs: the conversion that reads the buffer is a
// submission of the device, in flight when Encode returns, which waited
// for nothing, and its fence is on the buffer's dmabuf, what a
// compositor writing the next frame into it waits for; once that fence
// has signalled the conversion has finished. The video queue encodes
// after the conversion on the GPU, and the frames come out, the last
// one too, whose buffer and overlay were closed under its conversion:
// both live until it has read them.
func TestEncodeFencesItsBuffer(t *testing.T) {
	for _, codec := range []Codec{H264, H265} {
		t.Run(codec.String(), func(t *testing.T) {
			d := fencingDevice(t)
			c := vulkanClip(t, d, codec)
			plane := c.buf.Dmabuf().Planes[0].FD
			for frame := range encodeSlots {
				if err := d.Finish(); err != nil {
					t.Fatal(err)
				}
				waits, submitted := d.BlockingWaits(), d.Submissions()
				out, err := c.enc.Encode(c.buf, time.Duration(frame)*time.Second/30)
				if err != nil || len(out) != 0 {
					t.Fatalf("frame %d: %d packets, %v", frame, len(out), err)
				}
				stillFencing(t, d)
				if got := d.BlockingWaits(); got != waits {
					t.Errorf("frame %d: Encode waited here %d times, want none", frame, got-waits)
				}
				if got := d.Submissions(); got != submitted+1 || len(d.inflight) != 1 {
					t.Errorf("frame %d: Encode made %d submissions of the device, %d in flight, want its conversion", frame, got-submitted, len(d.inflight))
				}
				fence := dmabufFence(t, plane)
				waitReadable(t, fence)
				if signalled, err := drm.SyncFileSignalled(fence); err != nil || !signalled {
					t.Fatalf("frame %d: the buffer's fence: signalled %v, %v", frame, signalled, err)
				}
				if err := unix.Close(fence); err != nil {
					t.Fatal(err)
				}
				if done := d.finished(); done != len(d.inflight) {
					t.Errorf("frame %d: the buffer's fence signalled with %d of %d submissions finished", frame, done, len(d.inflight))
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

// Where no fence tells the buffer's writer (NVIDIA's driver, a device
// without sync_files), the conversion has finished when Encode returns:
// the buffer is free whoever writes it next.
func TestEncodeWithoutAFenceFinishesTheConversion(t *testing.T) {
	d := exportingDevice(t)
	d.implicitFences = false
	c := vulkanClip(t, d, H264)
	for frame := range 2 * encodeSlots {
		if _, err := c.enc.Encode(c.buf, time.Duration(frame)*time.Second/30); err != nil {
			t.Fatal(err)
		}
		if n := len(d.inflight); n != 0 {
			t.Fatalf("frame %d: %d submissions of the device in flight when Encode returned", frame, n)
		}
	}
}

// A packet Encode collected before its conversion failed is not lost:
// the next Encode returns it, ahead of the frames that follow.
func TestEncodeKeepsThePacketOfAFrameBeforeAFailure(t *testing.T) {
	d := exportingDevice(t)
	c := vulkanClip(t, d, H264)
	at := func(frame int) time.Duration { return time.Duration(frame) * time.Second / 30 }
	for frame := range encodeSlots {
		if out, err := c.enc.Encode(c.buf, at(frame)); err != nil || len(out) != 0 {
			t.Fatalf("frame %d: %d packets, %v", frame, len(out), err)
		}
	}
	d.fault = vk.ErrorOutOfDeviceMemory
	out, err := c.enc.Encode(c.buf, at(encodeSlots))
	if err == nil || !strings.Contains(err.Error(), "vkQueueSubmit") || len(out) != 0 {
		t.Fatalf("an Encode whose conversion is refused: %d packets, %v, want the submission's error", len(out), err)
	}
	out, err = c.enc.Encode(c.buf, at(encodeSlots))
	if err != nil || len(out) != 1 || out[0].PTS != at(0) || !out[0].Key || len(out[0].Data) == 0 {
		t.Fatalf("the Encode after the failed one returned %d packets, %v, want the first frame's", len(out), err)
	}
	rest, err := c.enc.Flush()
	if err != nil || len(rest) != encodeSlots {
		t.Fatalf("flushed %d packets, %v, want %d", len(rest), err, encodeSlots)
	}
	for i, p := range rest {
		if p.PTS != at(1+i) || len(p.Data) == 0 {
			t.Errorf("flushed packet %d presents at %v with %d bytes, want %v", i, p.PTS, len(p.Data), at(1+i))
		}
	}
}
