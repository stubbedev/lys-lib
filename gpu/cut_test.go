package gpu

import (
	"image"
	"image/color"
	"testing"

	"github.com/stubbedev/lys-lib/fourcc"
)

func TestCutUniformsFollowTheTransform(t *testing.T) {
	size := image.Pt(4, 2)
	for _, c := range []struct {
		tr     Transform
		x, y   float32
		s, tex float32
	}{
		{TransformNormal, 10.5, 20.5, 0.5 / 8, 0.5 / 4},
		{Transform180, 10.5, 20.5, 1 - 0.5/8, 1 - 0.5/4},
		{TransformFlipped, 10.5, 20.5, 1 - 0.5/8, 0.5 / 4},
	} {
		u := AlphaCut{Src: FRect{W: 4, H: 2}, Dst: image.Rect(10, 20, 18, 24), Transform: c.tr, Threshold: 0.5}.Uniforms(size, false)
		s, tt, on := u.UV(c.x, c.y)
		if !on || abs32(s-c.s) > 1e-6 || abs32(tt-c.tex) > 1e-6 {
			t.Errorf("%v at (%v, %v): uv (%v, %v) on %v, want (%v, %v)", c.tr, c.x, c.y, s, tt, on, c.s, c.tex)
		}
	}
	u := AlphaCut{Src: FRect{W: 4, H: 2}, Dst: image.Rect(10, 20, 18, 24), Threshold: 0.5}.Uniforms(size, false)
	if _, _, on := u.UV(9.5, 21); on {
		t.Error("a point left of the cut lies on its texture")
	}
	if u.Keeps(0.5) || !u.Keeps(0.51) {
		t.Error("the cut keeps alpha 0.5 or drops 0.51 at threshold 0.5")
	}
	opaque := AlphaCut{Src: FRect{W: 4, H: 2}, Dst: image.Rect(0, 0, 4, 2), Threshold: 0.5}.Uniforms(size, true)
	if !opaque.Keeps(0) {
		t.Error("an opaque texture's cut drops a texel")
	}
}

func abs32(f float32) float32 {
	if f < 0 {
		return -f
	}
	return f
}

func TestPassCutsFillsTexturesAndGlassByAlpha(t *testing.T) {
	d := openDevice(t)
	tg, err := d.NewOffscreen(4, 8)
	if err != nil {
		t.Fatal(err)
	}
	defer tg.Close()
	shape := newTexture(t, d, fourcc.ARGB8888, 4, 1, [][4]byte{{0, 0, 0, 0}, {0, 0, 0, 0x30}, {0, 0, 0, 0x80}, {0, 0, 0, 0xff}})
	opaque := newTexture(t, d, fourcc.XRGB8888, 4, 1, [][4]byte{{0, 0, 0, 0}, {0, 0, 0, 0}, {0, 0, 0, 0}, {0, 0, 0, 0}})
	green := newTexture(t, d, fourcc.ARGB8888, 1, 1, [][4]byte{{0, 0xff, 0, 0xff}})
	red := color.RGBA{0xff, 0, 0, 0xff}
	identity := ColorTransform{Matrix: [9]float32{1, 0, 0, 0, 1, 0, 0, 0, 1}, Scale: 1}
	row := func(y int) image.Rectangle { return image.Rect(0, y, 4, y+1) }
	cut := func(y int, tr Transform, threshold float32) AlphaCut {
		return AlphaCut{Src: FRect{W: 4, H: 1}, Dst: row(y), Transform: tr, Threshold: threshold}
	}
	glass := Glass{Saturation: 1, Brightness: 1, Contrast: 1}
	runPass(t, d, tg, []image.Rectangle{{Max: tg.Size()}}, func(p *Pass) {
		p.Clear(color.RGBA{})
		p.SetCut(shape, cut(0, TransformNormal, 0.25))
		p.FillRect(row(0), red, Mask{})
		p.SetCut(shape, cut(1, Transform180, 0.25))
		p.FillRect(row(1), red, Mask{})
		p.SetCut(shape, cut(2, TransformNormal, 0.1))
		p.DrawTexture(green, FRect{W: 1, H: 1}, row(2), TransformNormal, 1, Mask{})
		p.SetCut(shape, cut(3, TransformNormal, 0.6))
		p.DrawGlass(green, FRect{W: 1, H: 1}, row(3), 1, Mask{}, glass)
		p.SetColor(&identity)
		p.SetCut(shape, cut(4, TransformNormal, 0.25))
		p.DrawTexture(green, FRect{W: 1, H: 1}, row(4), TransformNormal, 1, Mask{})
		p.SetCut(shape, cut(5, TransformNormal, 0.6))
		p.DrawGlass(green, FRect{W: 1, H: 1}, row(5), 1, Mask{}, glass)
		p.SetColor(nil)
		p.SetCut(opaque, cut(6, TransformNormal, 0.9))
		p.FillRect(row(6), red, Mask{})
		p.ClearCut()
		p.FillRect(row(7), red, Mask{})
	})
	img := readTarget(t, tg)
	g := color.RGBA{0, 0xff, 0, 0xff}
	none := color.RGBA{}
	want := [8][4]color.RGBA{
		{none, none, red, red},
		{red, red, none, none},
		{none, g, g, g},
		{none, none, none, g},
		{none, none, g, g},
		{none, none, none, g},
		{red, red, red, red},
		{red, red, red, red},
	}
	for y, cols := range want {
		for x, w := range cols {
			if got := img.RGBAAt(x, y); !within(got, w, 1) {
				t.Errorf("row %d, pixel %d: %v, want %v", y, x, got, w)
			}
		}
	}
}

func TestSetCutRejectsWhatItCannotCut(t *testing.T) {
	d := openDevice(t)
	tg, err := d.NewOffscreen(4, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer tg.Close()
	shape := newTexture(t, d, fourcc.ARGB8888, 4, 1, make([][4]byte, 4))
	for name, c := range map[string]AlphaCut{
		"an empty destination":     {Src: FRect{W: 4, H: 1}},
		"a source off the texture": {Src: FRect{W: 5, H: 1}, Dst: image.Rect(0, 0, 4, 1)},
		"a threshold above 1":      {Src: FRect{W: 4, H: 1}, Dst: image.Rect(0, 0, 4, 1), Threshold: 1.5},
	} {
		p, err := d.Begin(tg, []image.Rectangle{{Max: tg.Size()}})
		if err != nil {
			t.Fatal(err)
		}
		p.SetCut(shape, c)
		if err := p.End(); err == nil {
			t.Errorf("a cut with %s ended without an error", name)
		}
	}
}
