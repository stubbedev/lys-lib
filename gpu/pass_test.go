package gpu

import (
	"image"
	"image/color"
	"math"
	"slices"
	"testing"

	"github.com/stubbedev/lys-lib/fourcc"
	"github.com/stubbedev/lys-lib/testbuild"
	"github.com/stubbedev/lys-lib/third_party/vk"
)

func newTexture(t *testing.T, d *Device, f fourcc.Format, w, h int, px [][4]byte) *Texture {
	t.Helper()
	tx, err := d.NewTexture(f, w, h)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(tx.Close)
	b := make([]byte, 0, w*h*4)
	for _, p := range px {
		b = append(b, p[:]...)
	}
	if err := tx.Update(image.Rect(0, 0, w, h), w*4, b); err != nil {
		t.Fatal(err)
	}
	return tx
}

func runPass(t *testing.T, d *Device, tg *Target, damage []image.Rectangle, draw func(p *Pass)) {
	t.Helper()
	p, err := d.Begin(tg, damage)
	if err != nil {
		t.Fatal(err)
	}
	draw(p)
	if err := p.End(); err != nil {
		t.Fatal(err)
	}
}

func readTarget(t *testing.T, tg *Target) *image.RGBA {
	t.Helper()
	img, err := tg.ReadPixels()
	if err != nil {
		t.Fatal(err)
	}
	return img
}

func within(a, b color.RGBA, tol int) bool {
	d := func(x, y uint8) bool { return int(x)-int(y) <= tol && int(y)-int(x) <= tol }
	return d(a.R, b.R) && d(a.G, b.G) && d(a.B, b.B) && d(a.A, b.A)
}

func TestPassDrawsThroughEveryTransform(t *testing.T) {
	d := openDevice(t)
	names := map[byte]color.RGBA{}
	var px [][4]byte
	for i, n := range []byte("abcdef") {
		c := color.RGBA{uint8(40 * (i + 1)), uint8(200 - 30*i), uint8(i * 7), 255}
		names[n] = c
		px = append(px, [4]byte{c.B, c.G, c.R, c.A})
	}
	tx := newTexture(t, d, fourcc.ARGB8888, 3, 2, px)
	for tr, want := range map[Transform][]string{
		TransformNormal:     {"abc", "def"},
		Transform90:         {"cf", "be", "ad"},
		Transform180:        {"fed", "cba"},
		Transform270:        {"da", "eb", "fc"},
		TransformFlipped:    {"cba", "fed"},
		TransformFlipped90:  {"ad", "be", "cf"},
		TransformFlipped180: {"def", "abc"},
		TransformFlipped270: {"fc", "eb", "da"},
	} {
		w, h := len(want[0]), len(want)
		tg, err := d.NewOffscreen(w+2, h+2)
		if err != nil {
			t.Fatal(err)
		}
		runPass(t, d, tg, []image.Rectangle{{Max: tg.Size()}}, func(p *Pass) {
			p.Clear(color.RGBA{})
			p.DrawTexture(tx, FRect{W: 3, H: 2}, image.Rect(1, 1, 1+w, 1+h), tr, 1, Mask{})
		})
		img := readTarget(t, tg)
		for y, row := range want {
			for x := range row {
				if got := img.RGBAAt(1+x, 1+y); got != names[row[x]] {
					t.Errorf("transform %d: pixel %d,%d is %v, want %c %v", tr, x, y, got, row[x], names[row[x]])
				}
			}
		}
		if got := img.RGBAAt(0, 0); got != (color.RGBA{}) {
			t.Errorf("transform %d drew outside its rect: %v", tr, got)
		}
		tg.Close()
	}
}

func TestPassSamplesAFractionalSourceBilinearly(t *testing.T) {
	d := openDevice(t)
	tx := newTexture(t, d, fourcc.ARGB8888, 2, 1, [][4]byte{{0, 0, 0, 255}, {255, 255, 255, 255}})
	tg, err := d.NewOffscreen(1, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer tg.Close()
	runPass(t, d, tg, []image.Rectangle{{Max: tg.Size()}}, func(p *Pass) {
		p.DrawTexture(tx, FRect{X: 0.5, W: 1, H: 1}, image.Rect(0, 0, 1, 1), TransformNormal, 1, Mask{})
	})
	if got := readTarget(t, tg).RGBAAt(0, 0); !within(got, color.RGBA{128, 128, 128, 255}, 1) {
		t.Errorf("the fractional source sampled %v, want midway grey", got)
	}
}

func TestPassBlendsAtAlpha(t *testing.T) {
	d := openDevice(t)
	tx := newTexture(t, d, fourcc.XRGB8888, 1, 1, [][4]byte{{0, 0, 255, 0}})
	tg, err := d.NewOffscreen(2, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer tg.Close()
	runPass(t, d, tg, []image.Rectangle{{Max: tg.Size()}}, func(p *Pass) {
		p.Clear(color.RGBA{0, 0, 255, 255})
		p.DrawTexture(tx, FRect{W: 1, H: 1}, image.Rect(0, 0, 1, 1), TransformNormal, 0.5, Mask{})
		p.DrawTexture(tx, FRect{W: 1, H: 1}, image.Rect(1, 0, 2, 1), TransformNormal, 1, Mask{})
	})
	img := readTarget(t, tg)
	if got := img.RGBAAt(0, 0); !within(got, color.RGBA{128, 0, 127, 255}, 1) {
		t.Errorf("half-alpha opaque red over blue is %v", got)
	}
	if got := img.RGBAAt(1, 0); got != (color.RGBA{255, 0, 0, 255}) {
		t.Errorf("XRGB8888 with a zero padding byte drew %v, want opaque red", got)
	}
}

func TestPassChangesOnlyTheDamage(t *testing.T) {
	d := openDevice(t)
	tg, err := d.NewOffscreen(8, 8)
	if err != nil {
		t.Fatal(err)
	}
	defer tg.Close()
	green := color.RGBA{0, 255, 0, 255}
	runPass(t, d, tg, []image.Rectangle{{Max: tg.Size()}}, func(p *Pass) { p.Clear(green) })
	a, b := image.Rect(1, 1, 5, 5), image.Rect(3, 3, 7, 7)
	tx := newTexture(t, d, fourcc.ARGB8888, 1, 1, [][4]byte{{0, 0, 0, 128}})
	runPass(t, d, tg, []image.Rectangle{a, b, image.Rect(-4, -4, 100, 0)}, func(p *Pass) {
		p.Clear(color.RGBA{255, 0, 0, 255})
		p.FillRect(image.Rect(0, 0, 8, 8), color.RGBA{0, 0, 0, 128}, Mask{})
		p.DrawTexture(tx, FRect{W: 1, H: 1}, image.Rect(0, 0, 8, 8), TransformNormal, 1, Mask{})
	})
	img := readTarget(t, tg)
	inside := color.RGBA{63, 0, 0, 255}
	for y := range 8 {
		for x := range 8 {
			pt := image.Pt(x, y)
			want := green
			if pt.In(a) || pt.In(b) {
				want = inside
			}
			if got := img.RGBAAt(x, y); !within(got, want, 1) {
				t.Fatalf("pixel %d,%d is %v, want %v", x, y, got, want)
			}
		}
	}
}

func TestPassIntoADmabufTarget(t *testing.T) {
	d := exportingDevice(t)
	mods, err := d.Modifiers(fourcc.XRGB8888)
	if err != nil {
		t.Fatal(err)
	}
	tg, err := d.NewTarget(16, 16, fourcc.XRGB8888, mods)
	if err != nil {
		t.Fatal(err)
	}
	defer tg.Close()
	for range 2 {
		runPass(t, d, tg, []image.Rectangle{{Max: tg.Size()}}, func(p *Pass) {
			p.Clear(color.RGBA{0, 0, 255, 255})
			p.FillRect(image.Rect(4, 4, 8, 8), color.RGBA{255, 255, 255, 255}, Mask{})
		})
	}
	img := readTarget(t, tg)
	if got := img.RGBAAt(5, 5); got != (color.RGBA{255, 255, 255, 255}) {
		t.Errorf("filled pixel %v", got)
	}
	if got := img.RGBAAt(0, 0); got != (color.RGBA{0, 0, 255, 255}) {
		t.Errorf("cleared pixel %v", got)
	}
}

func TestABatchHoldsItsTargetUntilItEnds(t *testing.T) {
	d := exportingDevice(t)
	mods, err := d.Modifiers(fourcc.XRGB8888)
	if err != nil {
		t.Fatal(err)
	}
	tg, err := d.NewTarget(16, 16, fourcc.XRGB8888, mods)
	if err != nil {
		t.Fatal(err)
	}
	defer tg.Close()
	for range 2 {
		runPass(t, d, tg, []image.Rectangle{{Max: tg.Size()}}, func(p *Pass) {
			p.Clear(color.RGBA{0, 0, 255, 255})
		})
		if !tg.held {
			t.Fatal("a pass handed its target back before the batch ended")
		}
	}
	if err := d.Flush(); err != nil {
		t.Fatal(err)
	}
	if tg.held || tg.s.layout != vk.ImageLayoutGeneral {
		t.Fatalf("after Flush the target is held %v in layout %d", tg.held, tg.s.layout)
	}
}

func TestPassFailsOnBadCalls(t *testing.T) {
	d := openDevice(t)
	tg, err := d.NewOffscreen(4, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer tg.Close()
	tx := newTexture(t, d, fourcc.ARGB8888, 2, 2, make([][4]byte, 4))
	closed, err := d.NewTexture(fourcc.ARGB8888, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	closed.Close()
	whole := []image.Rectangle{{Max: tg.Size()}}
	for name, call := range map[string]func(p *Pass){
		"unpremultiplied clear": func(p *Pass) { p.Clear(color.RGBA{255, 0, 0, 128}) },
		"unpremultiplied fill":  func(p *Pass) { p.FillRect(image.Rect(0, 0, 1, 1), color.RGBA{0, 9, 0, 8}, Mask{}) },
		"transform":             func(p *Pass) { p.DrawTexture(tx, FRect{W: 1, H: 1}, image.Rect(0, 0, 1, 1), 8, 1, Mask{}) },
		"alpha":                 func(p *Pass) { p.DrawTexture(tx, FRect{W: 1, H: 1}, image.Rect(0, 0, 1, 1), 0, 1.5, Mask{}) },
		"NaN alpha": func(p *Pass) {
			p.DrawTexture(tx, FRect{W: 1, H: 1}, image.Rect(0, 0, 1, 1), 0, float32(math.NaN()), Mask{})
		},
		"source outside": func(p *Pass) { p.DrawTexture(tx, FRect{X: 1.5, W: 1, H: 1}, image.Rect(0, 0, 1, 1), 0, 1, Mask{}) },
		"empty source":   func(p *Pass) { p.DrawTexture(tx, FRect{W: 0, H: 1}, image.Rect(0, 0, 1, 1), 0, 1, Mask{}) },
		"closed texture": func(p *Pass) { p.DrawTexture(closed, FRect{W: 1, H: 1}, image.Rect(0, 0, 1, 1), 0, 1, Mask{}) },
	} {
		p, err := d.Begin(tg, whole)
		if err != nil {
			t.Fatal(err)
		}
		call(p)
		if err := p.End(); err == nil {
			t.Errorf("%s: the pass ended without an error", name)
		}
	}
	p, err := d.Begin(tg, whole)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Begin(tg, whole); err == nil {
		t.Error("a second pass began while one was open")
	}
	if err := p.End(); err != nil {
		t.Fatal(err)
	}
	if err := p.End(); err == nil {
		t.Error("a pass ended twice")
	}
	p.Clear(color.RGBA{})
	if p.err == nil {
		t.Error("a call after End went unnoticed")
	}
}

func TestDisjointCoversTheDamageOnce(t *testing.T) {
	var s rectScratch
	bounds := image.Rect(0, 0, 40, 40)
	damage := []image.Rectangle{
		image.Rect(0, 0, 20, 20), image.Rect(10, 10, 30, 30), image.Rect(12, 12, 14, 14),
		image.Rect(35, 35, 90, 90), image.Rect(50, 50, 60, 60), image.Rect(5, 25, 35, 28),
	}
	rects := s.disjoint(damage, bounds)
	for y := range bounds.Dy() {
		for x := range bounds.Dx() {
			p := image.Pt(x, y)
			want := 0
			if slices.ContainsFunc(damage, p.In) {
				want = 1
			}
			got := 0
			for _, r := range rects {
				if p.In(r) {
					got++
				}
			}
			if got != want {
				t.Fatalf("pixel %v is covered %d times, want %d", p, got, want)
			}
		}
	}
}

func TestASteadyPassDoesNotAllocate(t *testing.T) {
	if testbuild.Race {
		t.Skip("the race detector allocates on its own")
	}
	d := openDevice(t)
	tg, err := d.NewOffscreen(32, 32)
	if err != nil {
		t.Fatal(err)
	}
	defer tg.Close()
	tx := newTexture(t, d, fourcc.ARGB8888, 2, 2, [][4]byte{{1, 2, 3, 255}, {1, 2, 3, 255}, {1, 2, 3, 255}, {1, 2, 3, 255}})
	damage := []image.Rectangle{image.Rect(0, 0, 16, 16), image.Rect(8, 8, 32, 32)}
	pass := func() {
		p, err := d.Begin(tg, damage)
		if err != nil {
			t.Fatal(err)
		}
		p.Clear(color.RGBA{A: 255})
		p.FillRect(image.Rect(0, 0, 32, 32), color.RGBA{A: 255}, Mask{})
		p.DrawTexture(tx, FRect{W: 2, H: 2}, image.Rect(0, 0, 32, 32), TransformNormal, 1, Mask{})
		if err := p.End(); err != nil {
			t.Fatal(err)
		}
	}
	pass()
	if n := testing.AllocsPerRun(100, pass); n != 0 {
		t.Fatalf("a steady pass allocated %v times", n)
	}
}

func TestAPassRecordsNothingForDrawsOutsideTheDamage(t *testing.T) {
	d := openDevice(t)
	tg, err := d.NewOffscreen(32, 32)
	if err != nil {
		t.Fatal(err)
	}
	defer tg.Close()
	tx := newTexture(t, d, fourcc.ARGB8888, 1, 1, [][4]byte{{1, 2, 3, 255}})
	inside, outside := image.Rect(0, 0, 8, 8), image.Rect(16, 16, 24, 24)
	runPass(t, d, tg, []image.Rectangle{inside}, func(p *Pass) {
		p.DrawTexture(tx, FRect{W: 1, H: 1}, outside, TransformNormal, 1, Mask{})
		p.FillRect(inside, color.RGBA{A: 255}, Mask{})
		p.DrawTexture(tx, FRect{W: 1, H: 1}, outside, TransformNormal, 1, Mask{})
	})
	if err := d.Finish(); err != nil {
		t.Fatal(err)
	}
	if want := (passWork{binds: 1, pushes: 1, scissors: 1, draws: 1}); d.passWork != want {
		t.Errorf("one draw in the damage recorded %+v, want %+v", d.passWork, want)
	}
}

func TestAPassSetsTheScissorOncePerDamageRect(t *testing.T) {
	d := openDevice(t)
	tg, err := d.NewOffscreen(32, 32)
	if err != nil {
		t.Fatal(err)
	}
	defer tg.Close()
	all := image.Rect(0, 0, 32, 32)
	runPass(t, d, tg, []image.Rectangle{all}, func(p *Pass) {
		for range 5 {
			p.FillRect(image.Rect(4, 4, 12, 12), color.RGBA{A: 255}, Mask{})
		}
	})
	if err := d.Finish(); err != nil {
		t.Fatal(err)
	}
	if want := (passWork{binds: 1, pushes: 5, scissors: 1, draws: 5}); d.passWork != want {
		t.Errorf("five draws under one damage rect recorded %+v, want %+v", d.passWork, want)
	}
}

func TestAScissorOnTheDamageStillClipsToTheDraw(t *testing.T) {
	d := openDevice(t)
	tg, err := d.NewOffscreen(16, 16)
	if err != nil {
		t.Fatal(err)
	}
	defer tg.Close()
	black, white := color.RGBA{A: 255}, color.RGBA{255, 255, 255, 255}
	runPass(t, d, tg, []image.Rectangle{{Max: tg.Size()}}, func(p *Pass) {
		p.Clear(black)
		p.FillRect(image.Rect(4, 4, 8, 8), white, Mask{})
		p.FillRect(image.Rect(10, 10, 12, 12), white, Mask{})
	})
	img := readTarget(t, tg)
	for y := range 16 {
		for x := range 16 {
			pt := image.Pt(x, y)
			want := black
			if pt.In(image.Rect(4, 4, 8, 8)) || pt.In(image.Rect(10, 10, 12, 12)) {
				want = white
			}
			if got := img.RGBAAt(x, y); got != want {
				t.Fatalf("pixel %d,%d is %v, want %v", x, y, got, want)
			}
		}
	}
}

func TestOpaqueDrawsRunWithBlendingOffAndTranslucentOnesBlend(t *testing.T) {
	d := openDevice(t)
	tg, err := d.NewOffscreen(4, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer tg.Close()
	rgbx := newTexture(t, d, fourcc.XRGB8888, 1, 1, [][4]byte{{0, 255, 0, 0}})
	argb := newTexture(t, d, fourcc.ARGB8888, 1, 1, [][4]byte{{0, 0, 128, 128}})
	before := d.OpaqueRuns()
	runPass(t, d, tg, []image.Rectangle{{Max: tg.Size()}}, func(p *Pass) {
		p.Clear(color.RGBA{0, 0, 255, 255})
		p.FillRect(image.Rect(0, 0, 1, 1), color.RGBA{255, 0, 0, 255}, Mask{})
		p.DrawTexture(rgbx, FRect{W: 1, H: 1}, image.Rect(1, 0, 2, 1), TransformNormal, 1, Mask{})
		p.DrawTexture(argb, FRect{W: 1, H: 1}, image.Rect(2, 0, 3, 1), TransformNormal, 1, Mask{})
		p.DrawTexture(rgbx, FRect{W: 1, H: 1}, image.Rect(3, 0, 4, 1), TransformNormal, 0.5, Mask{})
	})
	if got := d.OpaqueRuns() - before; got != 1 {
		t.Errorf("%d runs with blending off, want one: the opaque fill and the RGBX texture at full alpha", got)
	}
	img := readTarget(t, tg)
	for x, want := range []color.RGBA{{255, 0, 0, 255}, {0, 255, 0, 255}, {128, 0, 127, 255}, {0, 128, 127, 255}} {
		if got := img.RGBAAt(x, 0); !within(got, want, 1) {
			t.Errorf("pixel %d is %v, want %v", x, got, want)
		}
	}
}

func TestAMaskedOpaqueFillStillBlends(t *testing.T) {
	d := openDevice(t)
	tg, err := d.NewOffscreen(8, 8)
	if err != nil {
		t.Fatal(err)
	}
	defer tg.Close()
	before := d.OpaqueRuns()
	runPass(t, d, tg, []image.Rectangle{{Max: tg.Size()}}, func(p *Pass) {
		p.Clear(color.RGBA{0, 0, 255, 255})
		p.FillRect(image.Rect(0, 0, 8, 8), color.RGBA{255, 0, 0, 255}, Mask{Keep: RoundRect{Rect: image.Rect(0, 0, 8, 8), Radius: 4}})
	})
	if got := d.OpaqueRuns() - before; got != 0 {
		t.Errorf("a rounded fill ran with blending off %d times", got)
	}
	if got := readTarget(t, tg).RGBAAt(0, 0); got.B == 0 {
		t.Errorf("the rounded corner replaced the background: %v", got)
	}
}

func TestAKawaseLevelOverwritesItsTargetWithoutClearOrLoad(t *testing.T) {
	d := openDevice(t)
	src, err := d.NewOffscreen(8, 8)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	blue := color.RGBA{0, 0, 0x40, 0x80}
	runPass(t, d, src, []image.Rectangle{{Max: src.Size()}}, func(p *Pass) { p.Clear(blue) })
	tx, err := src.Texture()
	if err != nil {
		t.Fatal(err)
	}
	level, err := d.NewOffscreen(4, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer level.Close()
	all := []image.Rectangle{{Max: level.Size()}}
	for _, c := range []struct {
		name  string
		clear bool
	}{{"after a clear", true}, {"alone", false}} {
		runPass(t, d, level, all, func(p *Pass) { p.Clear(color.RGBA{255, 0, 0, 255}) })
		runPass(t, d, level, all, func(p *Pass) {
			if c.clear {
				p.Clear(color.RGBA{})
			}
			p.Kawase(tx, image.Rectangle{Max: level.Size()}, 1, false)
		})
		if err := d.Finish(); err != nil {
			t.Fatal(err)
		}
		if d.passWork.clears != 0 || d.passWork.discards != 1 {
			t.Errorf("a Kawase level %s recorded %+v, want no clear and a discarding render pass", c.name, d.passWork)
		}
		img := readTarget(t, level)
		for y := range 4 {
			for x := range 4 {
				if got := img.RGBAAt(x, y); !within(got, blue, 1) {
					t.Fatalf("a Kawase level %s: pixel %d,%d is %v, want %v", c.name, x, y, got, blue)
				}
			}
		}
	}
}

func TestAPassThatLeavesPixelsUndrawnLoadsTheTarget(t *testing.T) {
	d := openDevice(t)
	tg, err := d.NewOffscreen(8, 8)
	if err != nil {
		t.Fatal(err)
	}
	defer tg.Close()
	red, white := color.RGBA{255, 0, 0, 255}, color.RGBA{255, 255, 255, 255}
	runPass(t, d, tg, []image.Rectangle{{Max: tg.Size()}}, func(p *Pass) { p.Clear(red) })
	for _, c := range []struct {
		name   string
		damage image.Rectangle
		draw   func(p *Pass)
	}{
		{"a whole-target pass of a small fill", image.Rect(0, 0, 8, 8), func(p *Pass) { p.FillRect(image.Rect(0, 0, 4, 4), white, Mask{}) }},
		{"a partial clear", image.Rect(0, 0, 4, 4), func(p *Pass) { p.Clear(white) }},
	} {
		runPass(t, d, tg, []image.Rectangle{c.damage}, c.draw)
		if err := d.Finish(); err != nil {
			t.Fatal(err)
		}
		if d.passWork.discards != 0 {
			t.Errorf("%s discarded the target", c.name)
		}
		img := readTarget(t, tg)
		if got := img.RGBAAt(6, 6); got != red {
			t.Errorf("%s lost an undrawn pixel: %v", c.name, got)
		}
		if got := img.RGBAAt(1, 1); got != white {
			t.Errorf("%s did not draw: %v", c.name, got)
		}
	}
}

func TestAPassUnderPartialDamageDrawsNothingAFullOpaqueFillHides(t *testing.T) {
	d := openDevice(t)
	tg, err := d.NewOffscreen(16, 16)
	if err != nil {
		t.Fatal(err)
	}
	defer tg.Close()
	red, green, blue := color.RGBA{255, 0, 0, 255}, color.RGBA{0, 255, 0, 255}, color.RGBA{0, 0, 255, 255}
	runPass(t, d, tg, []image.Rectangle{{Max: tg.Size()}}, func(p *Pass) { p.Clear(red) })
	damage := image.Rect(0, 0, 8, 8)
	runPass(t, d, tg, []image.Rectangle{damage}, func(p *Pass) {
		p.FillRect(image.Rect(0, 0, 6, 6), blue, Mask{})
		p.FillRect(image.Rect(2, 2, 8, 8), color.RGBA{A: 128}, Mask{})
		p.FillRect(image.Rectangle{Max: tg.Size()}, green, Mask{})
		p.FillRect(image.Rect(0, 0, 2, 2), color.RGBA{0, 0, 128, 128}, Mask{})
	})
	if err := d.Finish(); err != nil {
		t.Fatal(err)
	}
	if d.passWork.draws != 2 || d.passWork.discards != 0 {
		t.Errorf("a pass under partial damage recorded %+v, want the hiding fill and the one after it, loading the target", d.passWork)
	}
	img := readTarget(t, tg)
	for _, c := range []struct {
		at   image.Point
		want color.RGBA
	}{{image.Pt(1, 1), color.RGBA{0, 127, 128, 255}}, {image.Pt(4, 4), green}, {image.Pt(12, 12), red}} {
		if got := img.RGBAAt(c.at.X, c.at.Y); !within(got, c.want, 1) {
			t.Errorf("pixel %v is %v, want %v", c.at, got, c.want)
		}
	}
}
