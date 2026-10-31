package gpu

import (
	"image"
	"image/color"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stubbedev/lys-lib/fourcc"
)

func TestMaskCoverage(t *testing.T) {
	r := RoundRect{Rect: image.Rect(10, 10, 30, 20), Radius: 4}
	keep := Mask{Keep: r}
	cases := []struct {
		m      Mask
		x, y   float32
		lo, hi float32
	}{
		{Mask{}, -100, -100, 1, 1},
		{keep, 20.5, 15.5, 1, 1},
		{keep, 10.5, 10.5, 0, 0},
		{keep, 9.5, 15.5, 0, 0},
		{keep, 10.5, 15.5, 1, 1},
		{keep, 10.5, 11.5, 0.1, 0.3},
		{Mask{Keep: RoundRect{Rect: r.Rect, Radius: 100}}, 20.5, 10.5, 0.5, 1},
		{Mask{Cut: r}, 20.5, 15.5, 0, 0},
		{Mask{Cut: r}, 5, 5, 1, 1},
		{Mask{Keep: RoundRect{Rect: image.Rect(8, 8, 32, 22), Radius: 6}, Cut: r}, 9.5, 15.5, 1, 1},
		{Mask{Keep: r, Falloff: Falloff{Range: 8, Power: 2}}, 20.5, 15.5, 1, 1},
		{Mask{Keep: r, Falloff: Falloff{Range: 8, Power: 2}}, 34, 15.5, 0.25, 0.25},
		{Mask{Keep: r, Falloff: Falloff{Range: 8, Power: 1}}, 36, 15.5, 0.25, 0.25},
		{Mask{Keep: r, Falloff: Falloff{Range: 8, Power: 2}}, 39, 15.5, 0, 0},
	}
	for _, c := range cases {
		if got := c.m.Coverage(c.x, c.y); got < c.lo || got > c.hi {
			t.Errorf("%+v at (%v, %v): %v, want %v to %v", c.m, c.x, c.y, got, c.lo, c.hi)
		}
	}
}

func premultiply(c color.RGBA, k float32) color.RGBA {
	s := func(v uint8) uint8 { return uint8(math.Round(float64(float32(v) * k))) }
	return color.RGBA{s(c.R), s(c.G), s(c.B), s(c.A)}
}

func TestPassMasksFillsAndTexturesLikeTheCoverage(t *testing.T) {
	d := openDevice(t)
	tg, err := d.NewOffscreen(48, 32)
	if err != nil {
		t.Fatal(err)
	}
	defer tg.Close()
	red := color.RGBA{200, 0, 0, 255}
	tx := newTexture(t, d, fourcc.XRGB8888, 1, 1, [][4]byte{{0, 0xc8, 0, 0}})
	window := Mask{Keep: RoundRect{Rect: image.Rect(4, 4, 24, 28), Radius: 6}}
	ring := Mask{Keep: RoundRect{Rect: image.Rect(26, 2, 46, 30), Radius: 8}, Cut: RoundRect{Rect: image.Rect(29, 5, 43, 27), Radius: 5}}
	shadow := Mask{Keep: RoundRect{Rect: image.Rect(36, 26, 44, 30), Radius: 2}, Cut: RoundRect{Rect: image.Rect(38, 27, 42, 29), Radius: 1}, Falloff: Falloff{Range: 3, Power: 2.5}}
	shadowArea := image.Rect(33, 23, 47, 32)
	runPass(t, d, tg, []image.Rectangle{{Max: tg.Size()}}, func(p *Pass) {
		p.Clear(color.RGBA{})
		p.DrawTexture(tx, FRect{W: 1, H: 1}, window.Keep.Rect, TransformNormal, 1, window)
		p.FillRect(ring.Keep.Rect, red, ring)
		p.FillRect(shadowArea, color.RGBA{0, 0, 0, 200}, shadow)
	})
	img := readTarget(t, tg)
	green := color.RGBA{0, 0xc8, 0, 255}
	bad := 0
	for y := range 32 {
		for x := range 48 {
			px, py := float32(x)+0.5, float32(y)+0.5
			want := color.RGBA{}
			switch pt := image.Pt(x, y); {
			case pt.In(window.Keep.Rect):
				want = premultiply(green, window.Coverage(px, py))
			case pt.In(ring.Keep.Rect):
				want = premultiply(red, ring.Coverage(px, py))
			}
			if image.Pt(x, y).In(shadowArea) {
				k := shadow.Coverage(px, py)
				s := premultiply(color.RGBA{0, 0, 0, 200}, k)
				inv := 255 - int(s.A)
				blendc := func(src, dst uint8) uint8 { return uint8(int(src) + (int(dst)*inv+127)/255) }
				want = color.RGBA{blendc(s.R, want.R), blendc(s.G, want.G), blendc(s.B, want.B), blendc(s.A, want.A)}
			}
			if got := img.RGBAAt(x, y); !within(got, want, 1) {
				if bad < 10 {
					t.Errorf("(%d,%d): %v, want %v", x, y, got, want)
				}
				bad++
			}
		}
	}
	if bad > 0 {
		t.Fatalf("%d pixels differ from Mask.Coverage", bad)
	}
}

func TestMaskCheckRefusesBadFalloffs(t *testing.T) {
	for _, f := range []Falloff{{Range: -1, Power: 1}, {Range: 4}, {Range: 4, Power: -2}, {Range: float32(math.Inf(1)), Power: 1}} {
		if err := (Mask{Falloff: f}).Check(); err == nil {
			t.Errorf("%+v accepted", f)
		}
	}
	if err := (Mask{Falloff: Falloff{Range: 4, Power: 3}}).Check(); err != nil {
		t.Error(err)
	}
}

func TestPassRefusesANegativeMaskRadius(t *testing.T) {
	d := openDevice(t)
	tg, err := d.NewOffscreen(1, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer tg.Close()
	p, err := d.Begin(tg, []image.Rectangle{{Max: tg.Size()}})
	if err != nil {
		t.Fatal(err)
	}
	p.FillRect(image.Rect(0, 0, 1, 1), color.RGBA{A: 255}, Mask{Keep: RoundRect{Rect: image.Rect(0, 0, 1, 1), Radius: -1}})
	if err := p.End(); err == nil {
		t.Fatal("a negative radius was accepted")
	}
}

func TestMaskGLSLIsGLSLES(t *testing.T) {
	if _, err := exec.LookPath("glslangValidator"); err != nil {
		t.Skip("no glslangValidator: run inside the dev shell")
	}
	src := "#version 100\nprecision highp float;\nuniform vec4 keep;\nuniform vec4 cut;\nuniform vec4 radii;\n" + MaskGLSL +
		"void main() { gl_FragColor = vec4(maskCoverage(gl_FragCoord.xy, keep, cut, radii)); }\n"
	path := filepath.Join(t.TempDir(), "mask.frag")
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("glslangValidator", path).CombinedOutput(); err != nil {
		t.Fatalf("MaskGLSL is not GLSL ES 1.00: %v\n%s", err, out)
	}
}
