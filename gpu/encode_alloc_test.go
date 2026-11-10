package gpu

import (
	"slices"
	"testing"
	"time"

	"github.com/stubbedev/lys-lib/testbuild"
)

// Encoding a frame on the Vulkan video queue allocates only the packet
// it hands back, the slice and its bytes, once the encoder has a frame
// in each slot: the conversion's barriers and submission, the coding
// commands and the feedback query point at nothing on the heap. Key
// frames and predicted ones both run.
func TestEncodeAllocatesOnlyItsPacket(t *testing.T) {
	if testbuild.Race {
		t.Skip("the race detector allocates on its own")
	}
	for _, codec := range []Codec{H264, H265} {
		t.Run(codec.String(), func(t *testing.T) {
			d := exportingDevice(t)
			if d.video == nil || !slices.Contains(d.video.codecs, codec) {
				t.Skipf("%s encodes no %s on a Vulkan video queue: %v", d.Name(), codec, d.videoWhy[codec])
			}
			cfg := EncoderConfig{Codec: codec, Width: 320, Height: 240, FrameRate: 30, KeyInterval: 4, QP: 20}
			c := openClip(t, d, cfg, func(cfg EncoderConfig) (*Encoder, error) { return newEncoderOn(d, cfg, newVulkanBackend) })
			c.buf.fill(t, encodeFrame(cfg.Width, cfg.Height, 0))
			frames := 0
			frame := func() {
				out, err := c.enc.Encode(c.buf, time.Duration(frames)*time.Second/30)
				if err != nil {
					t.Fatalf("frame %d: %v", frames, err)
				}
				if frames >= encodeSlots && len(out) != 1 {
					t.Fatalf("frame %d handed back %d packets, want the one of the frame %d before", frames, len(out), encodeSlots)
				}
				frames++
			}
			for range 2 * cfg.KeyInterval {
				frame()
			}
			if n := testing.AllocsPerRun(4*cfg.KeyInterval, frame); n > 2 {
				t.Errorf("%.1f allocations a frame, want at most the packet's 2", n)
			}
		})
	}
}
