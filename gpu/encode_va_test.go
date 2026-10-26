package gpu

import (
	"errors"
	"image"
	"image/color"
	"os/exec"
	"slices"
	"syscall"
	"testing"
	"time"

	"github.com/stubbedev/lys-lib/dmabuf"
	"github.com/stubbedev/lys-lib/internal/va"
	"github.com/stubbedev/lys-lib/third_party/vk"
)

// nv12Of is c in BT.709 limited range, as the conversion writes it.
func nv12Of(c color.RGBA) (y, u, v byte) {
	r, g, b := float64(c.R)/255, float64(c.G)/255, float64(c.B)/255
	l := 0.2126*r + 0.7152*g + 0.0722*b
	round := func(f float64) byte { return byte(min(max(f+0.5, 0), 255)) }
	return round(16 + 219*l), round(128 + 224*(b-l)/1.8556), round(128 + 224*(r-l)/1.5748)
}

// readPlanes copies back the planes the encoder's last conversion
// wrote, tight.
func readPlanes(t *testing.T, e *Encoder) []byte {
	t.Helper()
	d, size := e.d, nv12Bytes(e.coded)
	staging, err := d.newReadBuffer(size, vk.BufferUsageTransferDstBit, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer staging.close()
	err = d.submit(func(cb vk.CommandBuffer) {
		region := vk.BufferCopy{Size: vk.DeviceSize(size)}
		d.cmds.CmdCopyBuffer(cb, e.planes.buf, staging.buf, 1, &region)
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := staging.invalidate(); err != nil {
		t.Fatal(err)
	}
	return slices.Clone(staging.mapped[:size])
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
// wrote: the planes, read back through Vulkan and through the
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
				ours := vb.slots[0].picture.buf
				t.Logf("the driver's %dx%d surface: modifier %#x, size %d, fourcc %#x, pitches %v, offsets %v; ours: modifier %#x, planes %+v",
					desc.Width, desc.Height, desc.Objects[0].DrmFormatModifier, desc.Objects[0].Size, l.DrmFormat, l.Pitch[:l.NumPlanes], l.Offset[:l.NumPlanes], ours.Modifier, ours.Planes)
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
			if i, bad := planesDiffer(readPlanes(t, c.enc), w, h, y, u, v); bad {
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
			written := readPlanes(t, c.enc)
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

// An NV12 picture exports in every layout the device offers it for the
// encoder in as two planes of one dmabuf, luma rows at least the width
// apart, then chroma past them, or not at all where the driver lays
// the planes over each other.
func TestNV12PicturesExport(t *testing.T) {
	d := exportingDevice(t)
	mods, err := d.modifiers(dmabuf.FormatNV12, encodeUse)
	if err != nil {
		t.Fatal(err)
	}
	if len(mods) == 0 {
		t.Skipf("%s exports no NV12 pictures", d.Name())
	}
	for _, mod := range mods {
		e, err := d.export(330, 240, dmabuf.FormatNV12, []uint64{mod}, encodeUse)
		if errors.Is(err, errPlanesOverlap) {
			t.Logf("modifier %#x: %v", mod, err)
			continue
		}
		if err != nil {
			t.Fatalf("modifier %#x: %v", mod, err)
		}
		b := e.buf
		e.close(d)
		if len(b.Planes) != 2 || b.Modifier != mod {
			t.Fatalf("modifier %#x: a %#x picture of %d planes", mod, b.Modifier, len(b.Planes))
		}
		if b.Planes[0].Stride < 330 || b.Planes[1].Stride < 330 || b.Planes[1].Offset < b.Planes[0].Stride*240 {
			t.Errorf("modifier %#x: planes %+v do not hold 330x240 NV12", mod, b.Planes)
		}
		t.Logf("modifier %#x: planes %+v", mod, b.Planes)
	}
}

// The VA-API backend's parameters code a clip that decodes close to the
// source when the driver encodes from surfaces it made itself, filled
// through vaPutImage with what the conversion wrote: everything the
// backend does but the zero-copy import, which this sets apart.
func TestVAEncodesFromItsOwnSurfaces(t *testing.T) {
	d := exportingDevice(t)
	codecs := d.vaCodecs()
	if len(codecs) == 0 {
		t.Skipf("%s's VA-API driver encodes nothing: %v", d.Name(), d.vaWhy(H264))
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("no ffmpeg to decode with")
	}
	for _, codec := range codecs {
		t.Run(codec.String(), func(t *testing.T) {
			cfg := EncoderConfig{Codec: codec, Width: 640, Height: 360, FrameRate: 30, KeyInterval: 15, QP: 20}
			c := openClip(t, d, cfg, func(cfg EncoderConfig) (*Encoder, error) { return newEncoderOn(d, cfg, newVABackend) })
			vb := c.enc.backend.(*vaBackend)
			w, h := vb.coded[0], vb.coded[1]
			own, err := vb.dpy.CreateSurfaces(va.RTFormatYUV420, w, h, encodeSlots)
			if err != nil {
				t.Fatal(err)
			}
			var imported [encodeSlots]uint32
			for i := range vb.slots {
				imported[i], vb.slots[i].surface = vb.slots[i].surface, own[i]
			}
			t.Cleanup(func() {
				for i := range vb.slots {
					vb.slots[i].surface = imported[i]
				}
				vb.dpy.DestroySurfaces(own)
			})
			src := make([]*image.RGBA, 40)
			var idrs uint16
			for i := range src {
				src[i] = encodeFrame(cfg.Width, cfg.Height, i)
				c.buf.fill(t, src[i])
				slot, sinceIDR := i%encodeSlots, i%cfg.KeyInterval
				if err := c.enc.convert(c.buf, image.Point{}, slot); err != nil {
					t.Fatal(err)
				}
				if r := d.cmds.WaitForFences(d.device, 1, &c.enc.gfxDone, vk.True, ^uint64(0)); r != vk.Success {
					t.Fatalf("wait for the conversion: %d", r)
				}
				if err := vb.dpy.WriteNV12(own[slot], w, h, readPlanes(t, c.enc)); err != nil {
					t.Fatal(err)
				}
				if err := vb.encode(slot, i, sinceIDR, idrs); err != nil {
					t.Fatalf("frame %d: %v", i, err)
				}
				if sinceIDR == 0 {
					idrs++
				}
				data, err := vb.collect(slot)
				if err != nil {
					t.Fatalf("frame %d: %v", i, err)
				}
				c.pkts = append(c.pkts, Packet{Data: data, PTS: time.Duration(i) * time.Second / 30, Key: sinceIDR == 0})
				c.frames++
			}
			checkDecodes(t, ffmpeg, c, src)
		})
	}
}
