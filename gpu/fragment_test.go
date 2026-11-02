package gpu

import (
	_ "embed"
	"image"
	"image/color"
	"testing"
)

//go:embed testdata/fragment_test.frag.spv
var fragmentTestSPV []byte

func TestAFragmentShaderDrawsATextureThroughItself(t *testing.T) {
	d := openDevice(t)
	src, err := d.NewOffscreen(8, 8)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	dst, err := d.NewOffscreen(8, 8)
	if err != nil {
		t.Fatal(err)
	}
	defer dst.Close()
	whole := []image.Rectangle{{Max: src.Size()}}
	runPass(t, d, src, whole, func(p *Pass) { p.Clear(color.RGBA{0x40, 0, 0, 0xff}) })
	tx, err := src.Texture()
	if err != nil {
		t.Fatal(err)
	}
	s, err := d.NewFragmentShader(fragmentTestSPV)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Prepare(dst); err != nil {
		t.Fatal(err)
	}
	runPass(t, d, dst, whole, func(p *Pass) {
		p.Clear(color.RGBA{A: 0xff})
		p.DrawFragmentShader(s, tx, image.Rectangle{Max: dst.Size()}, [4]float32{0.5})
	})
	img := readTarget(t, dst)
	for _, x := range []int{0, 7} {
		want := color.RGBA{0xbf, uint8((float32(x) + 0.5) / 8 * 255), 0x80, 0xff}
		if got := img.RGBAAt(x, 3); !within(got, want, 2) {
			t.Errorf("pixel %d,3: %v, want %v: the shader's colour over the target's alpha", x, got, want)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("a second Close: %v", err)
	}
	if err := s.Prepare(dst); err == nil {
		t.Error("Prepare of a closed shader passed")
	}
}

func TestAnEmptyFragmentShaderIsRefused(t *testing.T) {
	if _, err := openDevice(t).NewFragmentShader(nil); err == nil {
		t.Error("an empty module was accepted")
	}
}
