package render

import (
	"bytes"
	"image"
	"image/png"
	"testing"
)

func newTestCanvas(w, h int) (*Canvas, []byte) {
	data := make([]byte, Stride(w)*h)
	return New(data, Stride(w), w, h), data
}

func pxAt(data []byte, stride, x, y int) Color {
	o := y*stride + x*4
	b := [4]byte{data[o], data[o+1], data[o+2], data[o+3]}
	return Color(uint32(b[3])<<24 | uint32(b[2])<<16 | uint32(b[1])<<8 | uint32(b[0]))
}

func TestRGBAConstruction(t *testing.T) {
	t.Run("opaque color is unchanged", func(t *testing.T) {
		if got := RGB(0x10, 0x20, 0x30); got != Color(0xFF102030) {
			t.Errorf("RGB = %#08x, want 0xff102030", got)
		}
	})

	t.Run("channels are premultiplied", func(t *testing.T) {
		c := RGBA(200, 100, 50, 128)
		if c.A() != 128 {
			t.Errorf("A = %d, want 128", c.A())
		}
		if want := uint8(uint32(200) * 128 / 255); c.R() != want {
			t.Errorf("R = %d, want %d", c.R(), want)
		}
	})

	t.Run("transparent color premultiplies to zero", func(t *testing.T) {
		if got := RGBA(255, 255, 255, 0); got != 0 {
			t.Errorf("fully transparent = %#08x, want 0", got)
		}
	})
}

func TestOver(t *testing.T) {
	t.Run("opaque source wins", func(t *testing.T) {
		src := RGB(1, 2, 3)
		dst := RGB(254, 253, 252)
		if got := src.over(dst); got != src {
			t.Errorf("opaque over dst = %v, want src", got)
		}
	})

	t.Run("transparent source is identity", func(t *testing.T) {
		dst := RGB(10, 20, 30)
		if got := Color(0).over(dst); got != dst {
			t.Errorf("transparent over dst = %v, want dst", got)
		}
	})

	t.Run("half source over white", func(t *testing.T) {
		src := Color(0x80 << 24) // half alpha, black
		dst := RGB(255, 255, 255)
		got := src.over(dst)
		if got.R() != 127 || got.G() != 127 || got.B() != 127 {
			t.Errorf("half black over white = %v, want channels 127", got)
		}
	})
}

func TestColorFromBytesStraightRoundTrip(t *testing.T) {
	t.Run("wl_shm byte order round-trips within premul rounding", func(t *testing.T) {
		want := RGBA(200, 100, 50, 128)
		straight := want.Straight()
		got := RGBA(straight[0], straight[1], straight[2], straight[3])
		for _, ch := range []struct {
			name      string
			got, want uint8
		}{
			{"A", got.A(), want.A()},
			{"R", got.R(), want.R()},
			{"G", got.G(), want.G()},
			{"B", got.B(), want.B()},
		} {
			if d := int(ch.got) - int(ch.want); d < -1 || d > 1 {
				t.Errorf("channel %s drifted %d: got %d, want %d", ch.name, d, ch.got, ch.want)
			}
		}
	})

	t.Run("fully transparent stays zero", func(t *testing.T) {
		if got := Color(0).Straight(); got != [4]byte{} {
			t.Errorf("transparent straight = %v, want zero", got)
		}
	})

	t.Run("opaque color passes channels through", func(t *testing.T) {
		got := RGB(10, 20, 30).Straight()
		if got != [4]byte{10, 20, 30, 255} {
			t.Errorf("opaque straight = %v, want [10 20 30 255]", got)
		}
	})
}

func TestClear(t *testing.T) {
	cv, data := newTestCanvas(10, 10)
	cv.FillRect(cv.Rect(), RGB(255, 255, 255))
	cv.Clear(Rect{X: 2, Y: 2, W: 3, H: 3}, RGB(1, 2, 3))

	t.Run("cleared region is overwritten, not blended", func(t *testing.T) {
		if got := pxAt(data, Stride(10), 3, 3); got != RGB(1, 2, 3) {
			t.Errorf("cleared pixel = %v, want RGB(1,2,3)", got)
		}
	})

	t.Run("outside the rect survives", func(t *testing.T) {
		if got := pxAt(data, Stride(10), 7, 7); got != RGB(255, 255, 255) {
			t.Errorf("pixel outside clear = %v, want white", got)
		}
	})
}

func TestFillRectClipsToCanvas(t *testing.T) {
	cv, data := newTestCanvas(8, 8)
	cv.FillRect(Rect{X: -5, Y: -5, W: 20, H: 20}, RGB(9, 9, 9))

	if got := pxAt(data, Stride(8), 0, 0); got != RGB(9, 9, 9) {
		t.Errorf("corner = %v, want filled", got)
	}
}

func TestBorderRect(t *testing.T) {
	cv, data := newTestCanvas(10, 10)
	col := RGB(1, 2, 3)
	cv.Clear(cv.Rect(), RGB(255, 255, 255))
	cv.BorderRect(cv.Rect(), 2, col)

	t.Run("edges are painted", func(t *testing.T) {
		for _, p := range [][2]int{{0, 0}, {9, 0}, {0, 9}, {9, 9}, {5, 0}, {5, 9}, {0, 5}, {9, 5}} {
			if got := pxAt(data, Stride(10), p[0], p[1]); got != col {
				t.Errorf("edge pixel %v = %v, want border color", p, got)
			}
		}
	})

	t.Run("center is untouched", func(t *testing.T) {
		if got := pxAt(data, Stride(10), 5, 5); got != RGB(255, 255, 255) {
			t.Errorf("center = %v, want background", got)
		}
	})

	t.Run("interior ring respects thickness", func(t *testing.T) {
		if got := pxAt(data, Stride(10), 1, 1); got != col {
			t.Errorf("inner ring pixel = %v, want border color (thickness 2)", got)
		}
		if got := pxAt(data, Stride(10), 2, 2); got != RGB(255, 255, 255) {
			t.Errorf("pixel at thickness boundary = %v, want background", got)
		}
	})
}

func TestRoundedRectEdgeBlending(t *testing.T) {
	cv, data := newTestCanvas(40, 40)
	cv.Clear(cv.Rect(), RGB(255, 255, 255))
	src := RGB(137, 180, 250)
	cv.RoundedRect(Rect{X: 5, Y: 5, W: 30, H: 30}, 15, src)

	for _, p := range [][2]int{{9, 9}, {30, 9}, {9, 30}, {30, 30}, {12, 8}} {
		got := pxAt(data, Stride(40), p[0], p[1])
		for _, ch := range []struct {
			name     string
			got, src uint8
		}{
			{"R", got.R(), src.R()},
			{"G", got.G(), src.G()},
			{"B", got.B(), src.B()},
		} {
			// A partially covered pixel must blend src and bg in premul
			// space, so every channel stays within [bg-or-src] bounds.
			lo := int(min(ch.src, 255)) - 1
			hi := int(max(ch.src, 255)) + 1
			if int(ch.got) < lo || int(ch.got) > hi {
				t.Errorf("corner pixel %v channel %s = %d, outside [src..bg] = [%d, %d] (coverage must scale the premultiplied channels)",
					p, ch.name, ch.got, lo, hi)
			}
		}
	}
}

func TestRoundedRect(t *testing.T) {
	cv, data := newTestCanvas(40, 40)
	cv.Clear(cv.Rect(), RGB(255, 255, 255))
	col := RGB(0, 0, 255)
	r := Rect{X: 5, Y: 5, W: 30, H: 30}
	cv.RoundedRect(r, 10, col)

	t.Run("center is opaque", func(t *testing.T) {
		if got := pxAt(data, Stride(40), 20, 20); got != col {
			t.Errorf("center = %v, want solid", got)
		}
	})

	t.Run("far corner stays background", func(t *testing.T) {
		if got := pxAt(data, Stride(40), 1, 1); got != RGB(255, 255, 255) {
			t.Errorf("outside corner = %v, want background", got)
		}
	})

	t.Run("square corner inside radius region is background", func(t *testing.T) {
		if got := pxAt(data, Stride(40), 6, 6); got != RGB(255, 255, 255) {
			t.Errorf("cut corner = %v, want background", got)
		}
	})

	t.Run("edge midway along a side is painted", func(t *testing.T) {
		if got := pxAt(data, Stride(40), 20, 6); got != col {
			t.Errorf("top edge center = %v, want solid", got)
		}
	})
}

func TestLinearGradient(t *testing.T) {
	cv, data := newTestCanvas(10, 10)
	from, to := RGB(0, 0, 0), RGB(100, 100, 100)
	r := Rect{X: 0, Y: 0, W: 10, H: 10}

	t.Run("vertical rows interpolate top to bottom", func(t *testing.T) {
		cv.LinearGradient(r, from, to, false)
		// Pixel centers sample the gradient at row+0.5, so the first and
		// last rows are strictly inside the ramp.
		if got := pxAt(data, Stride(10), 5, 0); got.R() > 25 {
			t.Errorf("top row = %v, want near from", got)
		}
		if got := pxAt(data, Stride(10), 5, 9); got.R() < 75 {
			t.Errorf("bottom row = %v, want near to", got)
		}
	})

	t.Run("rows are constant, columns interpolate", func(t *testing.T) {
		if pxAt(data, Stride(10), 0, 5) != pxAt(data, Stride(10), 9, 5) {
			t.Error("vertical gradient rows must be constant")
		}
		mid := pxAt(data, Stride(10), 5, 4)
		if mid.R() <= from.R() || mid.R() >= to.R() {
			t.Errorf("middle = %v, want between from and to", mid)
		}
	})

	t.Run("horizontal columns interpolate left to right", func(t *testing.T) {
		cv.Clear(cv.Rect(), 0)
		cv.LinearGradient(r, from, to, true)
		if got := pxAt(data, Stride(10), 0, 5); got.R() > 25 {
			t.Errorf("left column = %v, want near from", got)
		}
		if got := pxAt(data, Stride(10), 9, 5); got.R() < 75 {
			t.Errorf("right column = %v, want near to", got)
		}
	})
}

func TestDrawImage(t *testing.T) {
	cv, data := newTestCanvas(10, 10)
	src := image.NewRGBA(image.Rect(0, 0, 2, 2))
	for i := range src.Pix {
		src.Pix[i] = 0xFF
	}
	cv.DrawImage(src, 4, 4)
	if got := pxAt(data, Stride(10), 5, 5); got != RGB(255, 255, 255) {
		t.Errorf("drawn pixel = %v, want white", got)
	}
	if got := pxAt(data, Stride(10), 3, 3); got != 0 {
		t.Errorf("untouched pixel = %v, want zero", got)
	}
}

func TestDrawPNGRoundTrip(t *testing.T) {
	cv, data := newTestCanvas(8, 8)
	src := image.NewNRGBA(image.Rect(0, 0, 4, 4))
	for i := 0; i < len(src.Pix); i += 4 {
		src.Pix[i], src.Pix[i+1], src.Pix[i+2], src.Pix[i+3] = 200, 100, 50, 255
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, src); err != nil {
		t.Fatal(err)
	}
	decoded, err := png.Decode(&buf)
	if err != nil {
		t.Fatal(err)
	}
	cv.DrawImage(decoded, 2, 2)
	if got := pxAt(data, Stride(8), 3, 3); got != RGB(200, 100, 50) {
		t.Errorf("png pixel = %v, want RGB(200,100,50)", got)
	}
}
