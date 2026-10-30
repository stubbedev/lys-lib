package gpu

import (
	"image"
	"image/color"
	"math"
	"testing"

	"github.com/stubbedev/lys-lib/fourcc"
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
			p.DrawTexture(tx, FRect{W: 3, H: 2}, image.Rect(1, 1, 1+w, 1+h), tr, 1)
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
		p.DrawTexture(tx, FRect{X: 0.5, W: 1, H: 1}, image.Rect(0, 0, 1, 1), TransformNormal, 1)
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
		p.DrawTexture(tx, FRect{W: 1, H: 1}, image.Rect(0, 0, 1, 1), TransformNormal, 0.5)
		p.DrawTexture(tx, FRect{W: 1, H: 1}, image.Rect(1, 0, 2, 1), TransformNormal, 1)
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
		p.FillRect(image.Rect(0, 0, 8, 8), color.RGBA{0, 0, 0, 128})
		p.DrawTexture(tx, FRect{W: 1, H: 1}, image.Rect(0, 0, 8, 8), TransformNormal, 1)
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
			p.FillRect(image.Rect(4, 4, 8, 8), color.RGBA{255, 255, 255, 255})
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
		"unpremultiplied fill":  func(p *Pass) { p.FillRect(image.Rect(0, 0, 1, 1), color.RGBA{0, 9, 0, 8}) },
		"transform":             func(p *Pass) { p.DrawTexture(tx, FRect{W: 1, H: 1}, image.Rect(0, 0, 1, 1), 8, 1) },
		"alpha":                 func(p *Pass) { p.DrawTexture(tx, FRect{W: 1, H: 1}, image.Rect(0, 0, 1, 1), 0, 1.5) },
		"NaN alpha":             func(p *Pass) { p.DrawTexture(tx, FRect{W: 1, H: 1}, image.Rect(0, 0, 1, 1), 0, float32(math.NaN())) },
		"source outside":        func(p *Pass) { p.DrawTexture(tx, FRect{X: 1.5, W: 1, H: 1}, image.Rect(0, 0, 1, 1), 0, 1) },
		"empty source":          func(p *Pass) { p.DrawTexture(tx, FRect{W: 0, H: 1}, image.Rect(0, 0, 1, 1), 0, 1) },
		"closed texture":        func(p *Pass) { p.DrawTexture(closed, FRect{W: 1, H: 1}, image.Rect(0, 0, 1, 1), 0, 1) },
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
