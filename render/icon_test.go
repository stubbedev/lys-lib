package render

import (
	"bytes"
	"image"
	"image/png"
	"testing"
)

const testCircleSVG = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 20 20">
  <circle cx="10" cy="10" r="10" fill="#ff0000"/>
</svg>`

const testWideRectSVG = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 20 10">
  <rect x="0" y="0" width="20" height="10" fill="#000000"/>
</svg>`

func TestLoadSVG(t *testing.T) {
	t.Run("renders ink at the center", func(t *testing.T) {
		ic, err := LoadSVG([]byte(testCircleSVG), 20, 20)
		if err != nil {
			t.Fatal(err)
		}
		r, g, b, a := ic.img.At(10, 10).RGBA()
		if a == 0 {
			t.Fatal("center pixel is transparent, want opaque red")
		}
		if r < 0xf000 || g > 0x1000 || b > 0x1000 {
			t.Errorf("center pixel = (%d, %d, %d, %d), want red", r, g, b, a)
		}
	})

	t.Run("corners of a circle icon stay transparent", func(t *testing.T) {
		ic, err := LoadSVG([]byte(testCircleSVG), 20, 20)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, _, a := ic.img.At(0, 0).RGBA(); a != 0 {
			t.Errorf("corner alpha = %d, want 0", a)
		}
	})

	t.Run("viewBox aspect is kept and centered", func(t *testing.T) {
		ic, err := LoadSVG([]byte(testWideRectSVG), 20, 20)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, _, a := ic.img.At(10, 2).RGBA(); a != 0 {
			t.Errorf("letterboxed top band alpha = %d, want 0", a)
		}
		if _, _, _, a := ic.img.At(10, 10).RGBA(); a == 0 {
			t.Error("center of the fitted band is transparent, want ink")
		}
		if _, _, _, a := ic.img.At(10, 18).RGBA(); a != 0 {
			t.Errorf("letterboxed bottom band alpha = %d, want 0", a)
		}
	})

	t.Run("invalid size is rejected before parsing", func(t *testing.T) {
		if _, err := LoadSVG([]byte(testCircleSVG), 0, 10); err == nil {
			t.Error("zero width must error")
		}
		if _, err := LoadSVG([]byte(testCircleSVG), 10, -1); err == nil {
			t.Error("negative height must error")
		}
	})

	t.Run("garbage data errors", func(t *testing.T) {
		if _, err := LoadSVG([]byte("this is not svg"), 10, 10); err == nil {
			t.Error("garbage must error")
		}
	})

	t.Run("svg without a usable viewBox errors", func(t *testing.T) {
		noBox := `<svg xmlns="http://www.w3.org/2000/svg"></svg>`
		if _, err := LoadSVG([]byte(noBox), 10, 10); err == nil {
			t.Error("missing viewBox must error")
		}
	})
}

func TestLoadPNG(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	for i := 0; i < len(src.Pix); i += 4 {
		src.Pix[i], src.Pix[i+1], src.Pix[i+2], src.Pix[i+3] = 255, 0, 0, 255
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, src); err != nil {
		t.Fatal(err)
	}

	t.Run("scales up and draws", func(t *testing.T) {
		ic, err := LoadPNG(buf.Bytes(), 8, 8)
		if err != nil {
			t.Fatal(err)
		}
		w, h := ic.Size()
		if w != 8 || h != 8 {
			t.Fatalf("size = %dx%d, want 8x8", w, h)
		}
		cv, data := newTestCanvas(12, 12)
		ic.Draw(cv, 2, 2)
		if got := pxAt(data, Stride(12), 5, 5); got.B() != 0 || got.R() == 0 {
			t.Errorf("drawn pixel = %v, want red", got)
		}
		if got := pxAt(data, Stride(12), 0, 0); got != 0 {
			t.Errorf("outside pixel = %v, want untouched", got)
		}
	})

	t.Run("invalid size is rejected before decoding", func(t *testing.T) {
		if _, err := LoadPNG(buf.Bytes(), 0, 4); err == nil {
			t.Error("zero width must error")
		}
	})

	t.Run("garbage data errors", func(t *testing.T) {
		if _, err := LoadPNG([]byte("not a png"), 4, 4); err == nil {
			t.Error("garbage must error")
		}
	})
}

func TestIconDrawClips(t *testing.T) {
	ic, err := LoadSVG([]byte(testCircleSVG), 20, 20)
	if err != nil {
		t.Fatal(err)
	}
	cv, data := newTestCanvas(30, 30)
	cv.Clear(cv.Rect(), RGB(255, 255, 255))
	prev := cv.PushClip(Rect{X: 0, Y: 0, W: 10, H: 30})
	ic.Draw(cv, 5, 5)
	cv.PopClip(prev)
	if got := pxAt(data, Stride(30), 25, 15); got != RGB(255, 255, 255) {
		t.Errorf("pixel beyond clip = %v, want background", got)
	}
	if got := pxAt(data, Stride(30), 9, 15); got.B() != 0 || got.R() == 0 {
		t.Errorf("pixel inside clip = %v, want icon ink", got)
	}
}
