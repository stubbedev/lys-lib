package render

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"testing"

	"github.com/srwiley/rasterx"
	"golang.org/x/image/math/fixed"
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

// GTK's symbolic markup (wayle's icon migration writes it) parses: the
// gpa attributes drop, the SVG paint stays, the xlink ones survive.
func TestLoadSVGGrappaSymbolic(t *testing.T) {
	const grappa = `<svg width='16' height='16'
     xmlns:gpa='https://www.gtk.org/grappa'
     gpa:version='2'>
  <path d='M0 0L16 0L16 16L0 16Z'
stroke='none'
fill='rgb(0,0,0)'
gpa:fill='foreground'/>
</svg>`
	ic, err := LoadSVG([]byte(grappa), 16, 16)
	if err != nil {
		t.Fatalf("grappa icon: %v", err)
	}
	if _, _, _, a := ic.img.At(8, 8).RGBA(); a == 0 {
		t.Error("the grappa path painted nothing")
	}
	kept := string(dropForeignAttrs([]byte(`<use xlink:href="#a" xml:space="preserve" gpa:stroke="x" fill="red"/>`)))
	if kept != `<use xlink:href="#a" xml:space="preserve" fill="red"/>` {
		t.Errorf("dropForeignAttrs = %q", kept)
	}
}

// fill-rule: an even-odd ring (outer and inner subpaths wound alike)
// leaves its hole; nonzero, the default, fills it. The rule comes from
// the attribute, a style declaration or a group, and a document whose
// shapes oksvg cannot be matched to keeps nonzero.
func TestLoadSVGFillRule(t *testing.T) {
	const ring = `M0 0L16 0L16 16L0 16ZM4 4L12 4L12 12L4 12Z`
	hole := func(t *testing.T, svg string) bool {
		t.Helper()
		ic, err := LoadSVG([]byte(svg), 16, 16)
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		if _, _, _, a := ic.img.At(1, 1).RGBA(); a == 0 {
			t.Fatal("the ring painted nothing")
		}
		_, _, _, a := ic.img.At(8, 8).RGBA()
		return a == 0
	}
	for name, tc := range map[string]struct {
		svg  string
		hole bool
	}{
		"nonzero default":        {`<svg width='16' height='16'><path d='` + ring + `'/></svg>`, false},
		"evenodd attribute":      {`<svg width='16' height='16'><path fill-rule='evenodd' d='` + ring + `'/></svg>`, true},
		"explicit nonzero":       {`<svg width='16' height='16'><path fill-rule='nonzero' d='` + ring + `'/></svg>`, false},
		"evenodd style":          {`<svg width='16' height='16'><path style='fill:#000;fill-rule:evenodd' d='` + ring + `'/></svg>`, true},
		"style over attribute":   {`<svg width='16' height='16'><path fill-rule='evenodd' style='fill-rule: nonzero' d='` + ring + `'/></svg>`, false},
		"inherited from a group": {`<svg width='16' height='16'><g fill-rule='evenodd'><path d='` + ring + `'/></g></svg>`, true},
		"second shape":           {`<svg width='16' height='16'><rect x='0' y='0' width='1' height='1'/><path fill-rule='evenodd' d='` + ring + `'/></svg>`, true},
		"a use keeps nonzero": {`<svg width='16' height='16' xmlns:xlink='http://www.w3.org/1999/xlink'><defs><path id='p' d='M0 0L1 0L1 1Z'/></defs>` +
			`<path fill-rule='evenodd' d='` + ring + `'/><use xlink:href='#p'/></svg>`, false},
		"a defs shape is not drawn":    {`<svg width='16' height='16'><defs><path id='p' d='M0 0L1 0L1 1Z'/></defs><path fill-rule='evenodd' d='` + ring + `'/></svg>`, true},
		"an empty shape keeps nonzero": {`<svg width='16' height='16'><path d=''/><path fill-rule='evenodd' d='` + ring + `'/></svg>`, false},
		"defs are not drawn":           {`<svg width='16' height='16'><defs><linearGradient id='g'><stop offset='0' stop-color='#000'/></linearGradient></defs><path fill-rule='evenodd' d='` + ring + `'/></svg>`, true},
	} {
		t.Run(name, func(t *testing.T) {
			if got := hole(t, tc.svg); got != tc.hole {
				t.Errorf("hole = %v, want %v", got, tc.hole)
			}
		})
	}
}

// The even-odd scanner anti-aliases like rasterx's vector one: fed
// the same points, a shape without overlaps rasterizes the same under
// either rule, edges included, at any position and slope (on a target
// past 512px, where the vector scanner uses the floating-point math
// the even-odd one ports); and a later nonzero path on the same
// scanner still fills solid.
func TestSVGScannerEvenOddMatchesTheVectorCoverage(t *testing.T) {
	const w, h = 600, 40
	shapes := [][][2]float64{
		{{2.6, 5.4}, {28.4, 2.2}, {19.2, 30.8}},
		{{-6, 8}, {40, 12}, {16, 36}},
		{{100.3, 0.5}, {590.9, 20.25}, {300, 39.9}, {120.1, 30}},
	}
	for _, pts := range shapes {
		paint := func(evenOdd bool) *image.RGBA {
			img := image.NewRGBA(image.Rect(0, 0, w, h))
			var sc rasterx.Scanner = rasterx.NewScannerGV(w, h, img, img.Bounds())
			if evenOdd {
				sc = newSVGScanner(w, h, img)
			}
			sc.Clear()
			sc.SetWinding(!evenOdd)
			sc.SetColor(color.NRGBA{0xff, 0xff, 0xff, 0xff})
			for i, p := range append(pts, pts[0]) {
				fp := fixed.Point26_6{X: fixed.Int26_6(p[0] * 64), Y: fixed.Int26_6(p[1] * 64)}
				if i == 0 {
					sc.Start(fp)
				} else {
					sc.Line(fp)
				}
			}
			sc.Draw()
			return img
		}
		nz, eo := paint(false), paint(true)
		worst := 0
		for i := range nz.Pix {
			worst = max(worst, abs(int(nz.Pix[i])-int(eo.Pix[i])))
		}
		if worst > 1 {
			t.Errorf("%v: even-odd coverage differs from the vector scanner's by %d", pts, worst)
		}
	}
	ic, err := LoadSVG([]byte(`<svg width='16' height='16'><path fill-rule='evenodd' d='M0 0L8 0L8 8L0 8ZM2 2L6 2L6 6L2 6Z'/>`+
		`<path d='M8 8L16 8L16 16L8 16ZM10 10L14 10L14 14L10 14Z'/></svg>`), 16, 16)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, a := ic.img.At(4, 4).RGBA(); a != 0 {
		t.Error("the even-odd ring filled its hole")
	}
	if _, _, _, a := ic.img.At(12, 12).RGBA(); a == 0 {
		t.Error("the nonzero ring after it left a hole")
	}
}

// Even-odd edges inside an overlap anti-alias: where the inner edge
// covers a quarter of a pixel the winding sum is 1.25, the pixel three
// quarters covered.
func TestSVGEvenOddOverlapEdgeAntialiases(t *testing.T) {
	ic, err := LoadSVG([]byte(`<svg width='16' height='16'><path fill-rule='evenodd' fill='#000' d='M0 0L16 0L16 16L0 16ZM4.75 4.75L11.25 4.75L11.25 11.25L4.75 11.25Z'/></svg>`), 16, 16)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, a := ic.img.At(4, 8).RGBA(); a>>8 < 181 || a>>8 > 201 {
		t.Errorf("quarter-covered hole edge alpha %d, want about 191", a>>8)
	}
	if _, _, _, a := ic.img.At(2, 8).RGBA(); a>>8 != 255 {
		t.Errorf("ring alpha %d, want opaque", a>>8)
	}
}

// Each even-odd path draws alone: a second one on the scanner neither
// repaints the first in its color nor needs closing by hand, and the
// extent is the path's.
func TestSVGScannerEvenOddPathsAreIndependent(t *testing.T) {
	ic, err := LoadSVG([]byte(`<svg width='16' height='16'>`+
		`<path fill-rule='evenodd' fill='#ff0000' d='M0 0L6 0L6 6L0 6Z'/>`+
		`<path fill-rule='evenodd' fill='#0000ff' d='M10 10L16 10L16 16L10 16Z'/></svg>`), 16, 16)
	if err != nil {
		t.Fatal(err)
	}
	if r, _, b, _ := ic.img.At(3, 3).RGBA(); r>>8 != 255 || b != 0 {
		t.Errorf("first path pixel r=%d b=%d, want red only", r>>8, b>>8)
	}
	if _, _, b, _ := ic.img.At(13, 13).RGBA(); b>>8 != 255 {
		t.Errorf("second path pixel b=%d, want blue", b>>8)
	}

	img := image.NewRGBA(image.Rect(0, 0, 20, 20))
	sc := newSVGScanner(20, 20, img)
	sc.Clear()
	sc.SetWinding(false)
	sc.SetColor(color.NRGBA{0xff, 0xff, 0xff, 0xff})
	pt := func(x, y int) fixed.Point26_6 { return fixed.Point26_6{X: fixed.I(x), Y: fixed.I(y)} }
	sc.Start(pt(2, 3))
	sc.Line(pt(15, 3))
	sc.Line(pt(15, 12)) // left open: the fill closes it
	if ext := sc.GetPathExtent(); ext != (fixed.Rectangle26_6{Min: pt(2, 3), Max: pt(15, 12)}) {
		t.Errorf("extent %v, want the points' bounds", ext)
	}
	sc.Draw()
	if _, _, _, a := img.At(13, 5).RGBA(); a>>8 != 255 {
		t.Errorf("an open even-odd path filled %d inside, want closed and filled", a>>8)
	}
	if _, _, _, a := img.At(3, 10).RGBA(); a != 0 {
		t.Errorf("an open path filled %d outside its closing edge", a>>8)
	}
}
