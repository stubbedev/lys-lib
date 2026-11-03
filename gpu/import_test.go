package gpu

import (
	"errors"
	"image"
	"image/color"
	"slices"
	"strings"
	"testing"

	"github.com/stubbedev/lys-lib/dmabuf"
	"github.com/stubbedev/lys-lib/fourcc"
	"github.com/stubbedev/lys-lib/render"
	"github.com/stubbedev/lys-lib/third_party/vk"
	"github.com/stubbedev/lys-lib/udmabuf"
)

func newUdmabuf(t *testing.T, size int) *udmabuf.Buffer {
	t.Helper()
	u, err := udmabuf.New(size)
	if err != nil {
		t.Skipf("no udmabuf: %v", err)
	}
	t.Cleanup(func() { u.Close() })
	return u
}

func linearOrSkip(t *testing.T, d *Device, mods func(fourcc.Format) ([]fourcc.Modifier, error), f fourcc.Format) {
	t.Helper()
	ms, err := mods(f)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(ms, fourcc.Linear) {
		t.Skipf("%s takes no linear %s (%v)", d.Name(), f, ms)
	}
}

const linearStride = 256

func TestImportTargetDrawsIntoAUdmabuf(t *testing.T) {
	d := exportingDevice(t)
	linearOrSkip(t, d, d.TargetModifiers, fourcc.XRGB8888)
	const w, h = 16, 8
	u := newUdmabuf(t, linearStride*h)
	for i := range u.Mem {
		u.Mem[i] = 0x11
	}
	tg, err := d.ImportTarget(dmabuf.Buffer{Width: w, Height: h, Format: fourcc.XRGB8888, Modifier: fourcc.Linear, Planes: []dmabuf.Plane{{FD: u.FD, Stride: linearStride}}})
	if err != nil {
		t.Fatal(err)
	}
	defer tg.Close()
	damage := image.Rect(2, 2, 6, 5)
	runPass(t, d, tg, []image.Rectangle{damage}, func(p *Pass) {
		p.Clear(color.RGBA{0, 0, 255, 255})
		p.FillRect(image.Rect(4, 0, 16, 8), color.RGBA{0, 255, 0, 255}, Mask{})
	})
	at := func(x, y int) [4]byte {
		if err := d.Finish(); err != nil {
			t.Fatal(err)
		}
		return [4]byte(u.Mem[y*linearStride+x*4:])
	}
	for y := range h {
		for x := range w {
			want := [4]byte{0x11, 0x11, 0x11, 0x11}
			switch pt := image.Pt(x, y); {
			case pt.In(damage) && x >= 4:
				want = [4]byte{0, 255, 0, 255}
			case pt.In(damage):
				want = [4]byte{255, 0, 0, 255}
			}
			if got := at(x, y); got != want {
				t.Fatalf("udmabuf pixel %d,%d is %v, want %v", x, y, got, want)
			}
		}
	}
	runPass(t, d, tg, []image.Rectangle{{Max: tg.Size()}}, func(p *Pass) {
		p.FillRect(image.Rect(0, 0, 1, 1), color.RGBA{0, 0, 0, 255}, Mask{})
	})
	if got := at(3, 3); got != [4]byte{255, 0, 0, 255} {
		t.Errorf("a second pass lost the first's pixels: %v", got)
	}
	if got := at(0, 0); got != [4]byte{0, 0, 0, 255} {
		t.Errorf("the second pass drew %v", got)
	}
	rgba, err := d.ImportTarget(dmabuf.Buffer{Width: w, Height: h, Format: fourcc.XBGR8888, Modifier: fourcc.Linear, Planes: []dmabuf.Plane{{FD: u.FD, Stride: linearStride}}})
	if err != nil {
		t.Fatal(err)
	}
	defer rgba.Close()
	if err := d.Render(render.NewRecorder(w, h, 1, 1).Recording(), rgba); err == nil {
		t.Error("a render.List rendered into an XBGR8888 target")
	}
	runPass(t, d, rgba, []image.Rectangle{image.Rect(0, 0, 1, 1)}, func(p *Pass) { p.Clear(color.RGBA{255, 0, 0, 255}) })
	if got := at(0, 0); got != [4]byte{255, 0, 0, 255} {
		t.Errorf("red cleared into XBGR8888 is %v in memory", got)
	}
	if got := readTarget(t, rgba).RGBAAt(0, 0); got != (color.RGBA{255, 0, 0, 255}) {
		t.Errorf("XBGR8888 reads back %v", got)
	}
}

func TestImportTextureSamplesAUdmabuf(t *testing.T) {
	d := exportingDevice(t)
	linearOrSkip(t, d, d.TextureModifiers, fourcc.ABGR8888)
	const w, h = 4, 2
	u := newUdmabuf(t, linearStride*h)
	paint := func(c [4]byte) {
		for y := range h {
			for x := range w {
				copy(u.Mem[y*linearStride+x*4:], c[:])
			}
		}
	}
	paint([4]byte{10, 20, 30, 255})
	tx, err := d.ImportTexture(dmabuf.Buffer{Width: w, Height: h, Format: fourcc.ABGR8888, Modifier: fourcc.Linear, Planes: []dmabuf.Plane{{FD: u.FD, Stride: linearStride}}})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Close()
	if err := tx.Update(image.Rect(0, 0, 1, 1), 4, make([]byte, 4)); err == nil {
		t.Error("an imported texture took an upload")
	}
	tg, err := d.NewOffscreen(w, h)
	if err != nil {
		t.Fatal(err)
	}
	defer tg.Close()
	draw := func() color.RGBA {
		runPass(t, d, tg, []image.Rectangle{{Max: tg.Size()}}, func(p *Pass) {
			p.DrawTexture(tx, FRect{W: w, H: h}, image.Rect(0, 0, w, h), TransformNormal, 1, Mask{})
		})
		return readTarget(t, tg).RGBAAt(3, 1)
	}
	if got := draw(); got != (color.RGBA{10, 20, 30, 255}) {
		t.Errorf("sampled %v from the udmabuf", got)
	}
	paint([4]byte{200, 100, 50, 255})
	if got := draw(); got != (color.RGBA{200, 100, 50, 255}) {
		t.Errorf("after the producer wrote again, sampled %v", got)
	}
}

func TestImportRoundTripsAnExportedTarget(t *testing.T) {
	d := exportingDevice(t)
	mods, err := d.Modifiers(fourcc.ARGB8888)
	if err != nil {
		t.Fatal(err)
	}
	texMods, err := d.TextureModifiers(fourcc.ARGB8888)
	if err != nil {
		t.Fatal(err)
	}
	tgtMods, err := d.TargetModifiers(fourcc.ARGB8888)
	if err != nil {
		t.Fatal(err)
	}
	mods = slices.DeleteFunc(mods, func(m fourcc.Modifier) bool { return !slices.Contains(texMods, m) || !slices.Contains(tgtMods, m) })
	if len(mods) == 0 {
		t.Skip("no modifier both exports and imports")
	}
	for _, m := range mods {
		src, err := d.NewTarget(8, 8, fourcc.ARGB8888, []fourcc.Modifier{m})
		if err != nil {
			t.Fatal(err)
		}
		runPass(t, d, src, []image.Rectangle{{Max: src.Size()}}, func(p *Pass) {
			p.Clear(color.RGBA{255, 0, 0, 255})
			p.FillRect(image.Rect(0, 4, 8, 8), color.RGBA{0, 0, 255, 255}, Mask{})
		})
		into, err := d.ImportTarget(src.Dmabuf())
		if err != nil {
			t.Fatal(err)
		}
		runPass(t, d, into, []image.Rectangle{image.Rect(0, 0, 2, 2)}, func(p *Pass) { p.Clear(color.RGBA{0, 255, 0, 255}) })
		tx, err := d.ImportTexture(src.Dmabuf())
		if err != nil {
			t.Fatal(err)
		}
		dst, err := d.NewOffscreen(8, 8)
		if err != nil {
			t.Fatal(err)
		}
		runPass(t, d, dst, []image.Rectangle{{Max: dst.Size()}}, func(p *Pass) {
			p.DrawTexture(tx, FRect{W: 8, H: 8}, image.Rect(0, 0, 8, 8), TransformNormal, 1, Mask{})
		})
		img := readTarget(t, dst)
		for _, c := range []struct {
			x, y int
			want color.RGBA
		}{{1, 1, color.RGBA{0, 255, 0, 255}}, {5, 1, color.RGBA{255, 0, 0, 255}}, {5, 6, color.RGBA{0, 0, 255, 255}}} {
			if got := img.RGBAAt(c.x, c.y); got != c.want {
				t.Errorf("modifier %s: pixel %d,%d is %v, want %v", m, c.x, c.y, got, c.want)
			}
		}
		for _, c := range []interface{ Close() }{dst, tx, into, src} {
			c.Close()
		}
	}
}

func TestImportRefusesBadBuffers(t *testing.T) {
	d := exportingDevice(t)
	u := newUdmabuf(t, linearStride*4)
	ok := dmabuf.Buffer{Width: 4, Height: 4, Format: fourcc.ARGB8888, Modifier: fourcc.Linear, Planes: []dmabuf.Plane{{FD: u.FD, Stride: linearStride}}}
	with := func(f func(b *dmabuf.Buffer)) dmabuf.Buffer {
		b := ok
		b.Planes = slices.Clone(ok.Planes)
		f(&b)
		return b
	}
	for name, b := range map[string]dmabuf.Buffer{
		"implicit modifier": with(func(b *dmabuf.Buffer) { b.Modifier = fourcc.Invalid }),
		"unknown modifier":  with(func(b *dmabuf.Buffer) { b.Modifier = 0x0abc000000000001 }),
		"two planes":        with(func(b *dmabuf.Buffer) { b.Planes = append(b.Planes, b.Planes[0]) }),
		"format":            with(func(b *dmabuf.Buffer) { b.Format = fourcc.YUV420 }),
		"too small":         with(func(b *dmabuf.Buffer) { b.Height = 64 }),
		"no planes":         with(func(b *dmabuf.Buffer) { b.Planes = nil }),
	} {
		if tx, err := d.ImportTexture(b); err == nil {
			tx.Close()
			t.Errorf("%s: imported as a texture", name)
		}
		if tg, err := d.ImportTarget(b); err == nil {
			tg.Close()
			t.Errorf("%s: imported as a target", name)
		}
	}
}

func TestImportNeedsDmabufs(t *testing.T) {
	d := openDevice(t)
	if d.Exports() {
		t.Skip("the device handles dmabufs")
	}
	if _, err := d.ImportTexture(dmabuf.Buffer{}); !errors.Is(err, ErrNoDmabuf) {
		t.Errorf("ImportTexture on a device without dmabufs: %v", err)
	}
}

func TestSharedPlaneMappable(t *testing.T) {
	req := vk.MemoryRequirements{Size: 128 << 10, Alignment: 64 << 10}
	for _, c := range []struct {
		name   string
		offset vk.DeviceSize
		ok     bool
	}{
		{"first page", 0, true},
		{"second page", 64 << 10, true},
		{"inside a page", 16 << 10, false},
		{"one small page past a page", 68 << 10, false},
		{"past the image", 128 << 10, false},
	} {
		err := sharedPlaneMappable(1, vk.SubresourceLayout{Offset: c.offset, Size: 64 << 10}, req)
		if (err == nil) != c.ok {
			t.Errorf("%s: %v", c.name, err)
		}
	}
}

func nv12Layout(w, h int, stride uint32, fds [2]int, offsets [2]uint32) dmabuf.Buffer {
	return dmabuf.Buffer{Width: w, Height: h, Format: fourcc.NV12, Modifier: fourcc.Linear, Planes: []dmabuf.Plane{
		{FD: fds[0], Offset: offsets[0], Stride: stride},
		{FD: fds[1], Offset: offsets[1], Stride: stride},
	}}
}

func TestImportRefusesPlanesNVIDIACannotMap(t *testing.T) {
	d := exportingDevice(t)
	if !d.mapsPlanesWhole {
		t.Skipf("%s maps planes at any offset", d.Name())
	}
	linearOrSkip(t, d, d.TextureModifiers, fourcc.NV12)
	newCarrier := func() int {
		c, err := d.NewBuffer(2048, 2048, fourcc.XRGB8888, []fourcc.Modifier{fourcc.Linear})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(c.Close)
		return c.Dmabuf().Planes[0].FD
	}
	fd, other := newCarrier(), newCarrier()
	shared := [2]int{fd, fd}
	for name, b := range map[string]dmabuf.Buffer{
		"chroma right after luma":   nv12Layout(16, 16, 1024, shared, [2]uint32{0, 16 << 10}),
		"packed 1080p":              nv12Layout(1920, 1080, 1920, shared, [2]uint32{0, 1920 * 1080}),
		"chroma overruns the image": nv12Layout(16, 16, 1024, shared, [2]uint32{0, 128 << 10}),
		"luma inside a page":        nv12Layout(16, 16, 1024, shared, [2]uint32{4096, 68 << 10}),
		"disjoint inside a page":    nv12Layout(16, 16, 1024, [2]int{fd, other}, [2]uint32{0, 4096}),
	} {
		tx, err := d.ImportTexture(b)
		if err == nil {
			tx.Close()
			t.Errorf("%s: imported", name)
		} else if !strings.Contains(err.Error(), "cannot map") {
			t.Errorf("%s: %v", name, err)
		}
	}
	for name, b := range map[string]dmabuf.Buffer{
		"chroma on the next page": nv12Layout(16, 16, 1024, shared, [2]uint32{0, 64 << 10}),
		"luma on a later page":    nv12Layout(16, 16, 1024, shared, [2]uint32{64 << 10, 64 << 10}),
		"1080p on pages":          nv12Layout(1920, 1080, 1920, shared, [2]uint32{0, 2 << 20}),
	} {
		tx, err := d.ImportTexture(b)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		tx.Close()
	}
}

func TestImportTakesPackedPlanesElsewhere(t *testing.T) {
	d := exportingDevice(t)
	if d.mapsPlanesWhole {
		t.Skipf("%s cannot map packed planes", d.Name())
	}
	linearOrSkip(t, d, d.TextureModifiers, fourcc.NV12)
	u := newUdmabuf(t, 1<<20)
	tx, err := d.ImportTexture(nv12Layout(16, 16, linearStride, [2]int{u.FD, u.FD}, [2]uint32{0, 16 * linearStride}))
	if err != nil {
		t.Fatal(err)
	}
	tx.Close()
}
