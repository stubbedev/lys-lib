package gpu

import (
	"testing"
	"time"

	"github.com/stubbedev/lys-lib/testbuild"
)

// Encoding a frame allocates only the packet it hands back, the slice
// and its bytes, once the encoder has a frame in each slot, on every
// backend the device encodes the codec on: the Vulkan video queue's
// barriers, submissions and feedback query, and the VA-API driver's
// calls, parameter buffers and coded buffer, point at nothing on the
// heap. Key frames and predicted ones both run.
func TestEncodeAllocatesOnlyItsPacket(t *testing.T) {
	if testbuild.Race {
		t.Skip("the race detector allocates on its own")
	}
	for _, codec := range []Codec{H264, H265} {
		t.Run(codec.String(), func(t *testing.T) {
			d := exportingDevice(t)
			for _, b := range encodingBackends(t, d, codec) {
				t.Run(b.kind.String(), func(t *testing.T) {
					cfg := EncoderConfig{Codec: codec, Width: 320, Height: 240, FrameRate: 30, KeyInterval: 4, QP: 20}
					c := openClip(t, d, cfg, b.open)
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
		})
	}
}
