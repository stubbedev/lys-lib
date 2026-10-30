package gpu

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
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
			p.DrawTexture(tx, FRect{W: 1, H: 1}, image.Rect(0, 0, 1, 1), TransformNormal, 1)
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
		p.DrawTexture(tx, FRect{W: 1, H: 1}, image.Rect(0, 0, 1, 1), TransformNormal, 1)
	})
	if got := targetBytes(t, tg); [4]byte(got) != px {
		t.Errorf("imported %#x drew %#x", binary.LittleEndian.Uint32(px[:]), binary.LittleEndian.Uint32(got))
	}
}

func TestBuffersStayEightBit(t *testing.T) {
	d := exportingDevice(t)
	if _, err := d.ExportModifiers(fourcc.ARGB2101010); err == nil {
		t.Error("a 10-bit Buffer is on offer")
	}
}
