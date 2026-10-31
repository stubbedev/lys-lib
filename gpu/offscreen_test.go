package gpu

import (
	"image"
	"image/color"
	"testing"

	"github.com/stubbedev/lys-lib/fourcc"
)

func TestAnOffscreenTargetIsSampledByALaterPass(t *testing.T) {
	d := openDevice(t)
	src, err := d.NewOffscreen(4, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	dst, err := d.NewOffscreen(8, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer dst.Close()
	tx, err := src.Texture()
	if err != nil {
		t.Fatal(err)
	}
	if again, err := src.Texture(); err != nil || again != tx {
		t.Fatalf("a second Texture is %p, %v, want the first %p", again, err, tx)
	}
	red := newTexture(t, d, fourcc.ARGB8888, 1, 1, [][4]byte{{0, 0, 200, 255}})
	draw := func(left, right color.RGBA) {
		runPass(t, d, src, []image.Rectangle{{Max: src.Size()}}, func(p *Pass) {
			p.Clear(left)
			p.FillRect(image.Rect(2, 0, 4, 2), right, Mask{})
		})
		runPass(t, d, dst, []image.Rectangle{{Max: dst.Size()}}, func(p *Pass) {
			p.Clear(color.RGBA{0, 0, 0, 255})
			p.DrawTexture(tx, FRect{W: 4, H: 2}, image.Rect(0, 0, 4, 2), TransformNormal, 1, Mask{})
			p.DrawTexture(tx, FRect{W: 4, H: 2}, image.Rect(4, 0, 8, 2), TransformNormal, 0.5, Mask{})
		})
	}
	check := func(left, right color.RGBA) {
		t.Helper()
		img := readTarget(t, dst)
		half := func(c color.RGBA) color.RGBA { return color.RGBA{c.R / 2, c.G / 2, c.B / 2, 255} }
		for _, c := range []struct {
			x    int
			want color.RGBA
		}{{0, left}, {3, right}, {4, half(left)}, {7, half(right)}} {
			if got := img.RGBAAt(c.x, 1); !within(got, c.want, 1) {
				t.Errorf("pixel %d is %v, want %v", c.x, got, c.want)
			}
		}
	}
	draw(color.RGBA{0, 200, 0, 255}, color.RGBA{0, 0, 200, 255})
	check(color.RGBA{0, 200, 0, 255}, color.RGBA{0, 0, 200, 255})
	draw(color.RGBA{200, 200, 0, 255}, color.RGBA{0, 100, 100, 255})
	check(color.RGBA{200, 200, 0, 255}, color.RGBA{0, 100, 100, 255})
	runPass(t, d, src, []image.Rectangle{{Max: src.Size()}}, func(p *Pass) {
		p.DrawTexture(red, FRect{W: 1, H: 1}, image.Rect(0, 0, 4, 2), TransformNormal, 1, Mask{})
	})
	if err := tx.Update(image.Rect(0, 0, 1, 1), 4, make([]byte, 4)); err == nil {
		t.Error("a target's texture took an upload")
	}
	p, err := d.Begin(src, []image.Rectangle{{Max: src.Size()}})
	if err != nil {
		t.Fatal(err)
	}
	p.DrawTexture(tx, FRect{W: 4, H: 2}, image.Rect(0, 0, 4, 2), TransformNormal, 1, Mask{})
	if err := p.End(); err == nil {
		t.Error("a pass drew its own target")
	}
	tx.Close()
	if again, err := src.Texture(); err != nil || again != tx || tx.closed {
		t.Error("closing a target's texture released it under its target")
	}
}

func TestADmabufTargetIsNotSampled(t *testing.T) {
	d := exportingDevice(t)
	mods, err := d.Modifiers(fourcc.ARGB8888)
	if err != nil || len(mods) == 0 {
		t.Fatalf("modifiers %v, %v", mods, err)
	}
	tg, err := d.NewTarget(4, 4, fourcc.ARGB8888, mods)
	if err != nil {
		t.Fatal(err)
	}
	defer tg.Close()
	if _, err := tg.Texture(); err == nil {
		t.Fatal("a dmabuf target gave a texture")
	}
}

func bgra(img *image.RGBA) []byte {
	out := make([]byte, len(img.Pix))
	for i := 0; i < len(out); i += 4 {
		out[i], out[i+1], out[i+2], out[i+3] = img.Pix[i+2], img.Pix[i+1], img.Pix[i], img.Pix[i+3]
	}
	return out
}

func TestKawasePassesMatchKawaseAt(t *testing.T) {
	d := openDevice(t)
	src, err := d.NewOffscreen(32, 24)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	runPass(t, d, src, []image.Rectangle{{Max: src.Size()}}, func(p *Pass) {
		p.Clear(color.RGBA{0x10, 0x20, 0x30, 0xff})
		p.FillRect(image.Rect(4, 4, 14, 20), color.RGBA{0xe0, 0x40, 0, 0xff}, Mask{})
		p.FillRect(image.Rect(18, 2, 30, 10), color.RGBA{0, 0x60, 0x80, 0x80}, Mask{})
	})
	prev := src
	for i, step := range []struct {
		size image.Point
		up   bool
	}{{image.Pt(16, 12), false}, {image.Pt(8, 6), false}, {image.Pt(16, 12), true}, {image.Pt(32, 24), true}} {
		tx, err := prev.Texture()
		if err != nil {
			t.Fatal(err)
		}
		want := readTarget(t, prev)
		next, err := d.NewOffscreen(step.size.X, step.size.Y)
		if err != nil {
			t.Fatal(err)
		}
		defer next.Close()
		runPass(t, d, next, []image.Rectangle{{Max: step.size}}, func(p *Pass) {
			p.Clear(color.RGBA{})
			p.Kawase(tx, image.Rectangle{Max: step.size}, 1.5, step.up)
		})
		got := readTarget(t, next)
		w, h := want.Rect.Dx(), want.Rect.Dy()
		pix := bgra(want)
		sample := func(u, v float32) [4]float32 { return Bilinear(pix, w*4, w, h, u, v) }
		hx, hy := 0.5/float32(w)*1.5, 0.5/float32(h)*1.5
		for y := range step.size.Y {
			for x := range step.size.X {
				u, v := (float32(x)+0.5)/float32(step.size.X), (float32(y)+0.5)/float32(step.size.Y)
				c := KawaseAt(sample, step.up, u, v, hx, hy)
				exp := color.RGBA{uint8(c[2] + 0.5), uint8(c[1] + 0.5), uint8(c[0] + 0.5), uint8(c[3] + 0.5)}
				if g := got.RGBAAt(x, y); !within(g, exp, 3) {
					t.Fatalf("pass %d (%v up %v) pixel %d,%d: %v, want %v", i, step.size, step.up, x, y, g, exp)
				}
			}
		}
		prev = next
	}
}
