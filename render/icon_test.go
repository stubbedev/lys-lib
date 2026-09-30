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

func TestIconFromImage(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 4, 2))
	for i := 0; i < len(src.Pix); i += 4 {
		src.Pix[i], src.Pix[i+1], src.Pix[i+2], src.Pix[i+3] = 0, 0, 255, 255
	}

	t.Run("scales to the requested box", func(t *testing.T) {
		ic, err := IconFromImage(src, 8, 8)
		if err != nil {
			t.Fatal(err)
		}
		if w, h := ic.Size(); w != 8 || h != 8 {
			t.Fatalf("size = %dx%d, want 8x8", w, h)
		}
		r, g, b, a := ic.At(4, 4).RGBA()
		if a == 0 || b < 0xf000 || r > 0x1000 || g > 0x1000 {
			t.Errorf("center = (%d, %d, %d, %d), want opaque blue", r, g, b, a)
		}
	})

	t.Run("invalid size and empty images error", func(t *testing.T) {
		if _, err := IconFromImage(src, 0, 8); err == nil {
			t.Error("zero width must error")
		}
		if _, err := IconFromImage(nil, 8, 8); err == nil {
			t.Error("nil image must error")
		}
		if _, err := IconFromImage(image.NewRGBA(image.Rectangle{}), 8, 8); err == nil {
			t.Error("empty image must error")
		}
	})
}

func TestIconFromARGB32(t *testing.T) {
	// Two pixels in network byte order: opaque red, then half-alpha
	// green (straight alpha).
	data := []byte{
		0xff, 0xff, 0x00, 0x00,
		0x80, 0x00, 0xff, 0x00,
	}

	t.Run("decodes A,R,G,B byte order", func(t *testing.T) {
		ic, err := IconFromARGB32(2, 1, data, 2, 1)
		if err != nil {
			t.Fatal(err)
		}
		r, g, b, a := ic.At(0, 0).RGBA()
		if a>>8 != 0xff || r>>8 != 0xff || g != 0 || b != 0 {
			t.Errorf("pixel 0 = (%d, %d, %d, %d), want opaque red", r>>8, g>>8, b>>8, a>>8)
		}
		// The icon stores premultiplied RGBA: half-alpha full green
		// comes back as green at half intensity.
		r, g, b, a = ic.At(1, 0).RGBA()
		if a>>8 < 0x70 || a>>8 > 0x90 || g>>8 < 0x70 || g>>8 > 0x90 || r != 0 || b != 0 {
			t.Errorf("pixel 1 = (%d, %d, %d, %d), want half-alpha green", r>>8, g>>8, b>>8, a>>8)
		}
	})

	t.Run("scales like any image", func(t *testing.T) {
		ic, err := IconFromARGB32(2, 1, data, 16, 16)
		if err != nil {
			t.Fatal(err)
		}
		if w, h := ic.Size(); w != 16 || h != 16 {
			t.Fatalf("size = %dx%d, want 16x16", w, h)
		}
	})

	t.Run("short data and bad dimensions error", func(t *testing.T) {
		if _, err := IconFromARGB32(2, 2, data, 4, 4); err == nil {
			t.Error("a 2x2 raster from 8 bytes must error")
		}
		if _, err := IconFromARGB32(0, 1, data, 4, 4); err == nil {
			t.Error("zero raster width must error")
		}
		if _, err := IconFromARGB32(2, 1, data, 0, 4); err == nil {
			t.Error("zero target width must error")
		}
	})

	t.Run("trailing bytes are ignored", func(t *testing.T) {
		if _, err := IconFromARGB32(1, 1, data, 1, 1); err != nil {
			t.Errorf("one pixel with trailing bytes: %v", err)
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
