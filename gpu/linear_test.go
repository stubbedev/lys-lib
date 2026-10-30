package gpu

import (
	"image"
	"image/color"
	"slices"
	"testing"

	"github.com/stubbedev/lys-lib/dmabuf"
	"github.com/stubbedev/lys-lib/fourcc"
)

func TestCopyToLinearWritesAUdmabuf(t *testing.T) {
	d := exportingDevice(t)
	const w, h = 32, 16
	mods, err := d.Modifiers(fourcc.XRGB8888)
	if err != nil {
		t.Fatal(err)
	}
	tgtMods, err := d.TargetModifiers(fourcc.XRGB8888)
	if err != nil {
		t.Fatal(err)
	}
	mods = slices.DeleteFunc(mods, func(m fourcc.Modifier) bool { return !slices.Contains(tgtMods, m) })
	if len(mods) == 0 {
		t.Skip("no modifier the device both exports and imports to render into")
	}
	exported, err := d.NewTarget(w, h, fourcc.XRGB8888, mods)
	if err != nil {
		t.Fatal(err)
	}
	defer exported.Close()
	src, err := d.ImportTarget(exported.Dmabuf())
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	t.Logf("copying from a target in modifier %s", exported.Dmabuf().Modifier)
	runPass(t, d, src, []image.Rectangle{{Max: src.Size()}}, func(p *Pass) {
		p.Clear(color.RGBA{0, 0, 255, 255})
		p.FillRect(image.Rect(8, 4, 24, 12), color.RGBA{0, 255, 0, 255})
	})
	const stride = 256
	u := newUdmabuf(t, 1<<20)
	for i := range u.Mem {
		u.Mem[i] = 0x11
	}
	const offset = 64
	dst, err := d.ImportLinear(dmabuf.Buffer{Width: w, Height: h, Format: fourcc.XRGB8888, Modifier: fourcc.Linear, Planes: []dmabuf.Plane{{FD: u.FD, Offset: offset, Stride: stride}}})
	if err != nil {
		t.Fatal(err)
	}
	defer dst.Close()
	at := func(x, y int) [4]byte { return [4]byte(u.Mem[offset+y*stride+x*4:]) }
	part := image.Rect(4, 2, 12, 6)
	if err := d.CopyToLinear(dst, src, []image.Rectangle{part, image.Rect(40, 0, 50, 4)}); err != nil {
		t.Fatal(err)
	}
	if err := d.Finish(); err != nil {
		t.Fatal(err)
	}
	for y := range h {
		for x := range w {
			want := [4]byte{0x11, 0x11, 0x11, 0x11}
			switch pt := image.Pt(x, y); {
			case pt.In(part) && pt.In(image.Rect(8, 4, 24, 12)):
				want = [4]byte{0, 255, 0, 255}
			case pt.In(part):
				want = [4]byte{255, 0, 0, 255}
			}
			if got := at(x, y); got != want {
				t.Fatalf("after a partial copy, pixel %d,%d is %v, want %v", x, y, got, want)
			}
		}
	}
	if got := u.Mem[offset-1]; got != 0x11 {
		t.Errorf("the copy wrote before the buffer's offset: %#x", got)
	}
	if err := d.CopyToLinear(dst, src, []image.Rectangle{{Max: src.Size()}}); err != nil {
		t.Fatal(err)
	}
	if err := d.Finish(); err != nil {
		t.Fatal(err)
	}
	if got := at(20, 10); got != [4]byte{0, 255, 0, 255} {
		t.Errorf("filled pixel %v", got)
	}
	if got := at(31, 15); got != [4]byte{255, 0, 0, 255} {
		t.Errorf("cleared pixel %v", got)
	}
	if got := at(w, 0); got != [4]byte{0x11, 0x11, 0x11, 0x11} {
		t.Errorf("the copy wrote into the row padding: %v", got)
	}
}

func TestImportLinearRefusesBadBuffers(t *testing.T) {
	d := exportingDevice(t)
	u := newUdmabuf(t, 4096)
	ok := dmabuf.Buffer{Width: 8, Height: 8, Format: fourcc.ARGB8888, Modifier: fourcc.Linear, Planes: []dmabuf.Plane{{FD: u.FD, Stride: 64}}}
	with := func(f func(b *dmabuf.Buffer)) dmabuf.Buffer {
		b := ok
		b.Planes = slices.Clone(ok.Planes)
		f(&b)
		return b
	}
	l, err := d.ImportLinear(ok)
	if err != nil {
		t.Fatal(err)
	}
	tg, err := d.NewOffscreen(4, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer tg.Close()
	runPass(t, d, tg, []image.Rectangle{{Max: tg.Size()}}, func(p *Pass) { p.Clear(color.RGBA{}) })
	if err := d.CopyToLinear(l, tg, []image.Rectangle{{Max: tg.Size()}}); err == nil {
		t.Error("a 4x4 target copied into an 8x8 buffer")
	}
	l.Close()
	for name, b := range map[string]dmabuf.Buffer{
		"tiled":        with(func(b *dmabuf.Buffer) { b.Modifier = 0x0300000000606010 }),
		"two planes":   with(func(b *dmabuf.Buffer) { b.Planes = append(b.Planes, b.Planes[0]) }),
		"short stride": with(func(b *dmabuf.Buffer) { b.Planes[0].Stride = 16 }),
		"odd stride":   with(func(b *dmabuf.Buffer) { b.Planes[0].Stride = 66 }),
		"too small":    with(func(b *dmabuf.Buffer) { b.Height = 65 }),
		"YUV":          with(func(b *dmabuf.Buffer) { b.Format = fourcc.NV12 }),
	} {
		if l, err := d.ImportLinear(b); err == nil {
			l.Close()
			t.Errorf("%s: imported", name)
		}
	}
}
