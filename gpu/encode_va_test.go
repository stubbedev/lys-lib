package gpu

import (
	"image"
	"image/color"
	"syscall"
	"testing"

	"github.com/stubbedev/lys-lib/third_party/vk"
)

// nv12Of is c in BT.709 limited range, as the conversion writes it.
func nv12Of(c color.RGBA) (y, u, v byte) {
	r, g, b := float64(c.R)/255, float64(c.G)/255, float64(c.B)/255
	l := 0.2126*r + 0.7152*g + 0.0722*b
	round := func(f float64) byte { return byte(min(max(f+0.5, 0), 255)) }
	return round(16 + 219*l), round(128 + 224*(b-l)/1.8556), round(128 + 224*(r-l)/1.5748)
}

// readExported copies slot's buffer back through Vulkan, taking it
// from the driver first, and returns its planes tight.
func readExported(t *testing.T, vb *vaBackend, slot int) []byte {
	t.Helper()
	d := vb.d
	staging, err := d.newReadBuffer(vb.planeSize, vk.BufferUsageTransferDstBit, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer staging.close()
	err = d.submit(func(cb vk.CommandBuffer) {
		b := vb.ownership(slot)
		b.SrcQueueFamilyIndex, b.DstQueueFamilyIndex = vk.QueueFamilyForeignExt, d.family
		b.DstStageMask, b.DstAccessMask = vk.PipelineStageFlags2(vk.PipelineStage2CopyBit), vk.AccessFlags2(vk.Access2TransferReadBit)
		bufferBarrier2(d, cb, b)
		region := vk.BufferCopy{Size: vk.DeviceSize(vb.planeSize)}
		d.cmds.CmdCopyBuffer(cb, vb.slots[slot].planes.buf, staging.buf, 1, &region)
		b = vb.ownership(slot)
		b.SrcQueueFamilyIndex, b.DstQueueFamilyIndex = d.family, vk.QueueFamilyForeignExt
		b.SrcStageMask, b.SrcAccessMask = vk.PipelineStageFlags2(vk.PipelineStage2CopyBit), vk.AccessFlags2(vk.Access2TransferReadBit)
		bufferBarrier2(d, cb, b)
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := staging.invalidate(); err != nil {
		t.Fatal(err)
	}
	w, h := vb.coded[0], vb.coded[1]
	out := make([]byte, 0, w*h*3/2)
	for y := range h {
		out = append(out, staging.mapped[y*vb.pitch:][:w]...)
	}
	for y := range h / 2 {
		out = append(out, staging.mapped[vb.uvOffset+y*vb.pitch:][:w]...)
	}
	return out
}

// planesDiffer reports whether an NV12 picture strays from y, u and v
// by more than one, and at which byte first.
func planesDiffer(pix []byte, w, h int, y, u, v byte) (int, bool) {
	near := func(a, b byte) bool { return a+1 >= b && b+1 >= a }
	for i, p := range pix {
		want := y
		if i >= w*h {
			want = [2]byte{u, v}[(i-w*h)%2]
		}
		if !near(p, want) {
			return i, true
		}
	}
	return 0, false
}

// The surface the VA-API driver encodes from holds what the conversion
// wrote: the slot's buffer, read back through Vulkan and through the
// driver (vaGetImage), holds the frame's colour in NV12, and a
// patterned frame reads back from the driver byte for byte as Vulkan
// wrote it, which a solid one cannot show of a tiling. It logs how
// the driver lays out its own surfaces, the layout to import in where
// it reads ours wrong.
func TestVASurfaceHoldsTheConversion(t *testing.T) {
	d := exportingDevice(t)
	vaCodecs := d.vaCodecs()
	if len(vaCodecs) == 0 {
		t.Skipf("%s's VA-API driver encodes nothing: %v", d.Name(), d.vaWhy(H264))
	}
	for _, codec := range vaCodecs {
		t.Run(codec.String(), func(t *testing.T) {
			cfg := EncoderConfig{Codec: codec, Width: 320, Height: 240, FrameRate: 30, KeyInterval: 30, QP: 20}
			c := openClip(t, d, cfg, func(cfg EncoderConfig) (*Encoder, error) { return newEncoderOn(d, cfg, newVABackend) })
			vb := c.enc.backend.(*vaBackend)
			if desc, err := vb.dpy.ExportSurface(vb.recon[0]); err != nil {
				t.Logf("the driver exports no surface: %v", err)
			} else {
				for _, o := range desc.Objects[:desc.NumObjects] {
					_ = syscall.Close(int(o.Fd))
				}
				l := desc.Layers[0]
				t.Logf("the driver's %dx%d surface: modifier %#x, size %d, fourcc %#x, pitches %v, offsets %v; ours: linear, pitch %d, chroma at %d",
					desc.Width, desc.Height, desc.Objects[0].DrmFormatModifier, desc.Objects[0].Size, l.DrmFormat, l.Pitch[:l.NumPlanes], l.Offset[:l.NumPlanes], vb.pitch, vb.uvOffset)
			}
			col := color.RGBA{200, 50, 100, 255}
			c.buf.fill(t, solid(cfg.Width, cfg.Height, col))
			if err := c.enc.convert(c.buf, image.Point{}, 0); err != nil {
				t.Fatal(err)
			}
			if r := d.cmds.WaitForFences(d.device, 1, &c.enc.gfxDone, vk.True, ^uint64(0)); r != vk.Success {
				t.Fatalf("wait for the conversion: %d", r)
			}
			y, u, v := nv12Of(col)
			w, h := vb.coded[0], vb.coded[1]
			if i, bad := planesDiffer(readExported(t, vb, 0), w, h, y, u, v); bad {
				t.Fatalf("the buffer read back through Vulkan strays at byte %d of %dx%d NV12 from Y %d U %d V %d: the conversion is wrong", i, w, h, y, u, v)
			}
			pix, err := vb.dpy.ReadNV12(vb.slots[0].surface, w, h)
			if err != nil {
				t.Fatal(err)
			}
			if i, bad := planesDiffer(pix, w, h, y, u, v); bad {
				t.Errorf("the driver reads byte %d of the imported %dx%d NV12 as %d, want Y %d U %d V %d: it does not see what Vulkan wrote", i, w, h, pix[i], y, u, v)
			}
			c.buf.fill(t, encodeFrame(cfg.Width, cfg.Height, 7))
			if err := c.enc.convert(c.buf, image.Point{}, 0); err != nil {
				t.Fatal(err)
			}
			if r := d.cmds.WaitForFences(d.device, 1, &c.enc.gfxDone, vk.True, ^uint64(0)); r != vk.Success {
				t.Fatalf("wait for the conversion: %d", r)
			}
			written := readExported(t, vb, 0)
			if pix, err = vb.dpy.ReadNV12(vb.slots[0].surface, w, h); err != nil {
				t.Fatal(err)
			}
			wrong, first := 0, -1
			for i := range written {
				if pix[i] != written[i] {
					wrong++
					if first < 0 {
						first = i
					}
				}
			}
			if wrong > 0 {
				t.Errorf("the driver reads %d of %d bytes of a patterned %dx%d NV12 picture otherwise than Vulkan wrote them, the first at (%d, %d) of plane %d: it lays the import out otherwise",
					wrong, len(written), w, h, first%w, first/w%h, first/(w*h))
			}
		})
	}
}
