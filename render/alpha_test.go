// PushAlpha goldens: subtree opacity modulation must land on the
// mathematically expected premultiplied pixels for fills, rounded
// rects, text, and image blits; nested pushes must compose
// multiplicatively; a zero push must paint nothing. The rounding rule
// throughout is: scale = round(a*255), then every channel
// (ch*scale+127)/255 - round-half-up uint8, the same shape the
// source-over op uses.
package render

import (
	"image"
	"image/color"
	"testing"
)

// alphaScale256 is the uint8 blend factor PushAlpha derives for a, the
// test-side copy of the documented rounding rule.
func alphaScale256(a float64) uint32 {
	s := a * 255
	if s < 0 {
		return 0
	}
	if s > 255 {
		return 255
	}
	if s-float64(int(s)) >= 0.5 {
		return uint32(s) + 1
	}
	return uint32(s)
}

// modulateRef is the test-side copy of modulate: every premultiplied
// channel - color with alpha - scaled by scale/255, round-half-up.
func modulateRef(c Color, scale uint32) Color {
	sc := func(ch uint8) uint32 { return (uint32(ch)*scale + 127) / 255 }
	return Color(sc(c.A())<<24 | sc(c.R())<<16 | sc(c.G())<<8 | sc(c.B()))
}

func TestPushAlphaFillGolden(t *testing.T) {
	const (
		bg  = Color(0xFF0A141E) // RGB(10, 20, 30)
		col = Color(0xFFC86432) // RGB(200, 100, 50)
	)

	t.Run("quarter opacity lands on the hand-derived pixel", func(t *testing.T) {
		cv, data := newTestCanvas(4, 4)
		cv.Clear(cv.Rect(), bg)
		cv.PushAlpha(0.25)
		cv.FillRect(cv.Rect(), col)

		// scale 64: modulated source = a 64, r (200*64+127)/255 = 50,
		// g (100*64+127)/255 = 25, b (50*64+127)/255 = 13.
		// Over the background with keep = 191: r 50+7, g 25+15,
		// b 13+22, a 64+191.
		want := Color(0xFF392823)
		if got := pxAt(data, Stride(4), 2, 2); got != want {
			t.Errorf("pixel = %#08x, want %#08x", got, want)
		}
	})

	t.Run("nested pushes multiply to the single push", func(t *testing.T) {
		cv, data := newTestCanvas(4, 4)
		cv.Clear(cv.Rect(), bg)
		cv.PushAlpha(0.5)
		cv.PushAlpha(0.5)
		cv.FillRect(cv.Rect(), col)
		if got := pxAt(data, Stride(4), 2, 2); got != Color(0xFF392823) {
			t.Errorf("nested pixel = %#08x, want the 0.25 golden %#08x", got, Color(0xFF392823))
		}
	})

	t.Run("rounded rect center is the modulated color exactly", func(t *testing.T) {
		cv, data := newTestCanvas(16, 16)
		cv.PushAlpha(0.25)
		cv.RoundedRect(Rect{X: 0, Y: 0, W: 16, H: 16}, 4, col)
		// The center is fully covered, so only the alpha factor applies.
		if got := pxAt(data, Stride(16), 8, 8); got != Color(0x4032190D) {
			t.Errorf("center = %#08x, want %#08x", got, Color(0x4032190D))
		}
	})
}

// TestPushAlphaComposesOnEveryPrimitive pins nested == single for the
// whole blend surface, pixel for pixel.
func TestPushAlphaComposesOnEveryPrimitive(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.SetRGBA(0, 0, color.RGBA{R: 200, G: 100, B: 50, A: 255})
	img.SetRGBA(1, 0, color.RGBA{R: 10, G: 20, B: 30, A: 128})
	face := testTypeface(t)
	shaped := face.Shape("gap", 20)

	painters := map[string]func(cv *Canvas){
		"fill": func(cv *Canvas) { cv.FillRect(Rect{X: 0, Y: 0, W: 16, H: 16}, RGB(200, 100, 50)) },
		"text": func(cv *Canvas) { face.Draw(cv, shaped, 0, 14, RGB(255, 255, 255)) },
		"image": func(cv *Canvas) {
			cv.DrawImageDevice(img, 0, 0)
		},
	}
	for name, paint := range painters {
		t.Run(name, func(t *testing.T) {
			single, singleData := newTestCanvas(16, 16)
			single.PushAlpha(0.25)
			paint(single)

			nested, nestedData := newTestCanvas(16, 16)
			nested.PushAlpha(0.5)
			nested.PushAlpha(0.5)
			paint(nested)

			for y := range 16 {
				for x := range 16 {
					if got, want := pxAt(nestedData, Stride(16), x, y), pxAt(singleData, Stride(16), x, y); got != want {
						t.Fatalf("nested(%d,%d) = %#08x, want single-push %#08x", x, y, got, want)
					}
				}
			}
			if single.Touched() == 0 {
				t.Fatal("nothing was painted")
			}
		})
	}
}

func TestPushAlphaImageGolden(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 2, 1))
	img.SetRGBA(0, 0, color.RGBA{R: 200, G: 100, B: 50, A: 255})
	img.SetRGBA(1, 0, color.RGBA{R: 10, G: 20, B: 30, A: 128})

	cv, data := newTestCanvas(2, 1)
	cv.PushAlpha(0.25)
	cv.DrawImageDevice(img, 0, 0)

	// scale 64: (200,100,50,255) -> a 64, r 50, g 25, b 13;
	// (10,20,30,128) -> a (128*64+127)/255 = 32, r 3, g 5, b 8.
	for _, want := range []struct {
		x     int
		pixel Color
	}{{0, Color(0x4032190D)}, {1, Color(0x20030508)}} {
		if got := pxAt(data, Stride(2), want.x, 0); got != want.pixel {
			t.Errorf("pixel %d = %#08x, want %#08x", want.x, got, want.pixel)
		}
	}
}

func TestPushAlphaTextGolden(t *testing.T) {
	face := testTypeface(t)
	shaped := face.Shape("gap", 20)

	ref, refData := newTestCanvas(40, 24)
	face.Draw(ref, shaped, 1, 18, RGB(255, 255, 255))

	cv, data := newTestCanvas(40, 24)
	cv.PushAlpha(0.25)
	face.Draw(cv, shaped, 1, 18, RGB(255, 255, 255))

	// Every pixel of the faded draw is the reference pixel with all
	// four channels scaled by 64/255, round-half-up: modulation hits
	// the AA coverage ramps exactly one level up.
	scale := alphaScale256(0.25)
	if scale != 64 {
		t.Fatalf("test scale = %d, want 64", scale)
	}
	ink := 0
	for y := range 24 {
		for x := range 40 {
			want := modulateRef(pxAt(refData, Stride(40), x, y), scale)
			got := pxAt(data, Stride(40), x, y)
			if got != want {
				t.Fatalf("pixel (%d,%d) = %#08x, want modulated reference %#08x", x, y, got, want)
			}
			if got.A() > 0 {
				ink++
				// Premultiplied white ink keeps its channels at the alpha.
				for ch, v := range map[string]uint8{"R": got.R(), "G": got.G(), "B": got.B()} {
					if v > got.A() {
						t.Fatalf("channel %s = %d exceeds alpha %d at (%d,%d): not premultiplied", ch, v, got.A(), x, y)
					}
				}
			}
		}
	}
	if ink == 0 {
		t.Fatal("faded text produced no ink")
	}
}

func TestPushAlphaZeroPaintsNothing(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	img.SetRGBA(0, 0, color.RGBA{R: 200, G: 100, B: 50, A: 255})
	face := testTypeface(t)

	cv, data := newTestCanvas(16, 16)
	prev := cv.PushAlpha(0)
	cv.FillRect(cv.Rect(), RGB(255, 255, 255))
	cv.RoundedRect(cv.Rect(), 4, RGB(255, 0, 0))
	cv.LinearGradient(cv.Rect(), RGB(1, 2, 3), RGB(4, 5, 6), true)
	cv.Line(0, 0, 15, 15, 2, RGB(7, 8, 9))
	face.Draw(cv, face.Shape("ink", 12), 0, 12, RGB(255, 255, 255))
	cv.DrawImageDevice(img, 4, 4)

	if got := cv.Touched(); got != 0 {
		t.Errorf("zero-alpha paint touched %d pixels, want 0", got)
	}
	if got := pxAt(data, Stride(16), 5, 5); got != 0 {
		t.Errorf("pixel = %#08x, want untouched black", got)
	}

	cv.PopAlpha(prev)
	cv.FillRect(Rect{X: 0, Y: 0, W: 1, H: 1}, RGB(255, 255, 255))
	if got := cv.Touched(); got != 1 {
		t.Errorf("after PopAlpha the fill touched %d pixels, want 1", got)
	}
}

func TestPopAlphaRestores(t *testing.T) {
	col := Color(0xFFC86432)
	bg := Color(0xFF0A141E)

	ref, refData := newTestCanvas(4, 4)
	ref.Clear(ref.Rect(), bg)
	ref.FillRect(ref.Rect(), col)

	cv, data := newTestCanvas(4, 4)
	cv.Clear(cv.Rect(), bg)
	prev := cv.PushAlpha(0.25)
	cv.FillRect(cv.Rect(), RGB(9, 9, 9))
	cv.PopAlpha(prev)
	cv.FillRect(cv.Rect(), col)

	for y := range 4 {
		for x := range 4 {
			if got, want := pxAt(data, Stride(4), x, y), pxAt(refData, Stride(4), x, y); got != want {
				t.Fatalf("pixel (%d,%d) = %#08x, want unmodulated reference %#08x", x, y, got, want)
			}
		}
	}
}

func TestPushAlphaClamps(t *testing.T) {
	col := Color(0xFFC86432)

	t.Run("over one is opaque, not over-bright", func(t *testing.T) {
		cv, data := newTestCanvas(2, 2)
		cv.PushAlpha(2)
		cv.PushAlpha(2)
		cv.FillRect(cv.Rect(), col)
		if got := pxAt(data, Stride(2), 0, 0); got != col {
			t.Errorf("pixel = %#08x, want the unmodulated color %#08x", got, col)
		}
	})

	t.Run("under zero paints nothing", func(t *testing.T) {
		cv, _ := newTestCanvas(2, 2)
		cv.PushAlpha(-1)
		cv.FillRect(cv.Rect(), col)
		if got := cv.Touched(); got != 0 {
			t.Errorf("negative alpha touched %d pixels, want 0", got)
		}
	})
}
