package gpu

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"slices"
	"strings"
	"testing"

	"github.com/stubbedev/lys-lib/dmabuf"
	"github.com/stubbedev/lys-lib/fourcc"
	"github.com/stubbedev/lys-lib/third_party/vk"
)

func targetBytes(t *testing.T, tg *Target) []byte {
	t.Helper()
	d := tg.s.d
	n := tg.s.w * tg.s.h * 4
	staging, err := d.newReadBuffer(n, vk.BufferUsageTransferDstBit, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer staging.close()
	err = d.submit(func(cb vk.CommandBuffer) {
		tg.acquire(cb)
		tg.s.download(cb, image.Rect(0, 0, tg.s.w, tg.s.h), staging, 0)
		tg.release(cb)
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := staging.invalidate(); err != nil {
		t.Fatal(err)
	}
	return bytes.Clone(staging.mapped[:n])
}

func pack1010102(a, x, y, z uint32) [4]byte {
	var b [4]byte
	binary.LittleEndian.PutUint32(b[:], a<<30|x<<20|y<<10|z)
	return b
}

func tenBitTarget(t *testing.T, d *Device, f fourcc.Format, w, h int) *Target {
	t.Helper()
	mods, err := d.Modifiers(f)
	if err != nil {
		t.Fatal(err)
	}
	if len(mods) == 0 {
		t.Skipf("%s renders no %s target it exports", d.Name(), f)
	}
	tg, err := d.NewTarget(w, h, f, mods)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(tg.Close)
	return tg
}

func TestTenBitTexturesDrawUnrounded(t *testing.T) {
	d := exportingDevice(t)
	for _, c := range []struct {
		tex, target fourcc.Format
		px, want    [4]byte
	}{
		{fourcc.ARGB2101010, fourcc.ARGB2101010, pack1010102(3, 513, 2, 1021), pack1010102(3, 513, 2, 1021)},
		{fourcc.XRGB2101010, fourcc.XRGB2101010, pack1010102(0, 513, 2, 1021), pack1010102(3, 513, 2, 1021)},
		{fourcc.ARGB2101010, fourcc.ABGR2101010, pack1010102(3, 513, 2, 1021), pack1010102(3, 1021, 2, 513)},
		{fourcc.XBGR2101010, fourcc.XRGB2101010, pack1010102(0, 1021, 2, 513), pack1010102(3, 513, 2, 1021)},
	} {
		tx := newTexture(t, d, c.tex, 1, 1, [][4]byte{c.px})
		tg := tenBitTarget(t, d, c.target, 2, 1)
		runPass(t, d, tg, []image.Rectangle{{Max: tg.Size()}}, func(p *Pass) {
			p.Clear(color.RGBA{})
			p.DrawTexture(tx, FRect{W: 1, H: 1}, image.Rect(0, 0, 1, 1), TransformNormal, 1, Mask{})
		})
		got := targetBytes(t, tg)
		if [4]byte(got) != c.want {
			t.Errorf("%s drawn into %s is %#x, want %#x", c.tex, c.target, binary.LittleEndian.Uint32(got), binary.LittleEndian.Uint32(c.want[:]))
		}
		if px := readTarget(t, tg).RGBAAt(0, 0); px != (color.RGBA{128, 0, 255, 255}) {
			t.Errorf("%s reads back as %v", c.target, px)
		}
	}
}

func TestTenBitImportedTexture(t *testing.T) {
	d := exportingDevice(t)
	linearOrSkip(t, d, d.TextureModifiers, fourcc.ABGR2101010)
	u := newUdmabuf(t, linearStride)
	px := pack1010102(3, 1, 700, 1022)
	copy(u.Mem, px[:])
	tx, err := d.ImportTexture(dmabuf.Buffer{Width: 1, Height: 1, Format: fourcc.ABGR2101010, Modifier: fourcc.Linear, Planes: []dmabuf.Plane{{FD: u.FD, Stride: linearStride}}})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Close()
	tg := tenBitTarget(t, d, fourcc.ABGR2101010, 1, 1)
	runPass(t, d, tg, []image.Rectangle{{Max: tg.Size()}}, func(p *Pass) {
		p.DrawTexture(tx, FRect{W: 1, H: 1}, image.Rect(0, 0, 1, 1), TransformNormal, 1, Mask{})
	})
	if got := targetBytes(t, tg); [4]byte(got) != px {
		t.Errorf("imported %#x drew %#x", binary.LittleEndian.Uint32(px[:]), binary.LittleEndian.Uint32(got))
	}
}

// bufferBytes is what b's image holds, row after row, as its consumer
// left it.
func bufferBytes(t *testing.T, b *Buffer) []byte {
	t.Helper()
	d := b.d
	w, h := b.e.buf.Width, b.e.buf.Height
	n := w * h * 4
	staging, err := d.newReadBuffer(n, vk.BufferUsageTransferDstBit, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer staging.close()
	err = d.submit(func(cb vk.CommandBuffer) {
		fullBarrier(d, cb, b.e.image, vk.ImageLayoutGeneral, vk.ImageLayoutTransferSrcOptimal, vk.QueueFamilyForeignExt, d.family)
		region := vk.BufferImageCopy{
			ImageSubresource: vk.ImageSubresourceLayers{AspectMask: vk.ImageAspectFlags(vk.ImageAspectColorBit), LayerCount: 1},
			ImageExtent:      extent3D(w, h),
		}
		d.cmds.CmdCopyImageToBuffer(cb, b.e.image, vk.ImageLayoutTransferSrcOptimal, staging.buf, 1, &region)
		fullBarrier(d, cb, b.e.image, vk.ImageLayoutTransferSrcOptimal, vk.ImageLayoutGeneral, d.family, vk.QueueFamilyForeignExt)
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := staging.invalidate(); err != nil {
		t.Fatal(err)
	}
	return bytes.Clone(staging.mapped[:n])
}

// A Buffer comes in every format a Target does, the 10-bit ones too, in
// each modifier the device exports the format in, and in no other
// format. NVIDIA, which renders into no linear layout, exports the
// linear 10-bit buffers a card scanning out its frames takes.
func TestBuffersComeInEveryTextureFormat(t *testing.T) {
	d := exportingDevice(t)
	for _, f := range d.TextureFormats() {
		mods, err := d.ExportModifiers(f)
		if err != nil {
			t.Errorf("%s: %v", f, err)
			continue
		}
		t.Logf("%s exports %s buffers in %v", d.Name(), f, mods)
		if strings.Contains(d.Name(), "NVIDIA") && !slices.Contains(mods, fourcc.Linear) {
			t.Errorf("NVIDIA exports no linear %s buffer: %v", f, mods)
		}
		for _, m := range mods {
			b, err := d.NewBuffer(64, 16, f, []fourcc.Modifier{m})
			if err != nil {
				t.Errorf("%s in modifier %s: %v", f, m, err)
				continue
			}
			if buf := b.Dmabuf(); buf.Format != f || buf.Modifier != m || len(buf.Planes) != 1 || (m == fourcc.Linear && buf.Planes[0].Stride < 64*4) {
				t.Errorf("%s in modifier %s: allocated %+v", f, m, buf)
			}
			b.Close()
		}
	}
	for _, f := range []fourcc.Format{fourcc.NV12, fourcc.ABGR16161616F} {
		if _, err := d.ExportModifiers(f); err == nil || !strings.Contains(err.Error(), "no buffer in DRM format") {
			t.Errorf("modifiers of %s buffers: %v, want an error naming the formats a buffer comes in", f, err)
		}
		if _, err := d.NewBuffer(64, 16, f, []fourcc.Modifier{fourcc.Linear}); err == nil || !strings.Contains(err.Error(), "no buffer in DRM format") {
			t.Errorf("a %s buffer: %v, want an error naming the formats a buffer comes in", f, err)
		}
	}
}

// The linear copy of a 10-bit target keeps its ten bits: a linear
// Buffer in the target's format, imported as a Linear, takes
// CopyToLinear's copy of values eight bits cannot hold (a red of
// 513/1023) and holds the packed words unchanged, which ReadPixels
// rounds to eight bits. It is how a card scans out the frames of a
// primary GPU that renders into no linear layout.
func TestTenBitTargetCopiesIntoALinearBuffer(t *testing.T) {
	for _, c := range []struct {
		format fourcc.Format
		want   color.RGBA
	}{
		{fourcc.XRGB2101010, color.RGBA{128, 0, 255, 255}},
		{fourcc.ARGB2101010, color.RGBA{128, 0, 255, 255}},
		{fourcc.XBGR2101010, color.RGBA{255, 0, 128, 255}},
		{fourcc.ABGR2101010, color.RGBA{255, 0, 128, 255}},
	} {
		t.Run(c.format.String(), func(t *testing.T) {
			d := exportingDevice(t)
			linearOrSkip(t, d, d.ExportModifiers, c.format)
			const w, h = 8, 4
			buf, err := d.NewBuffer(w, h, c.format, []fourcc.Modifier{fourcc.Linear})
			if err != nil {
				t.Fatal(err)
			}
			defer buf.Close()
			lin, err := d.ImportLinear(buf.Dmabuf())
			if err != nil {
				t.Fatal(err)
			}
			defer lin.Close()
			px := pack1010102(3, 513, 2, 1021)
			tx := newTexture(t, d, c.format, 1, 1, [][4]byte{px})
			tg := tenBitTarget(t, d, c.format, w, h)
			all := []image.Rectangle{{Max: tg.Size()}}
			runPass(t, d, tg, all, func(p *Pass) {
				p.DrawTexture(tx, FRect{W: 1, H: 1}, all[0], TransformNormal, 1, Mask{})
			})
			if err := d.CopyToLinear(lin, tg, all); err != nil {
				t.Fatal(err)
			}
			if err := d.Finish(); err != nil {
				t.Fatal(err)
			}
			got := bufferBytes(t, buf)
			for i := 0; i < len(got); i += 4 {
				if [4]byte(got[i:]) != px {
					t.Fatalf("pixel %d of the linear copy is %#x, want %#x", i/4, binary.LittleEndian.Uint32(got[i:]), binary.LittleEndian.Uint32(px[:]))
				}
			}
			img, err := buf.ReadPixels()
			if err != nil {
				t.Fatal(err)
			}
			if got := img.RGBAAt(w-1, h-1); got != c.want {
				t.Errorf("the buffer reads back as %v, want %v", got, c.want)
			}
		})
	}
}
