package render

import (
	"os"
	"testing"

	"golang.org/x/image/font/gofont/goitalic"
	"golang.org/x/image/font/gofont/goregular"
)

func paintedBox(t *testing.T, s *ShapedText, x, baseline, w, h int) Rect {
	t.Helper()
	buf := make([]byte, Stride(w)*h)
	cv := New(buf, Stride(w), w, h)
	s.Draw(cv, x, baseline, RGB(255, 255, 255))
	box := Rect{}
	for y := range h {
		for px := range w {
			if buf[y*Stride(w)+px*4+3] != 0 {
				box = box.Union(Rect{X: px, Y: y, W: 1, H: 1})
			}
		}
	}
	return box
}

func TestTheInkOverhangHoldsEveryPaintedPixel(t *testing.T) {
	faces := map[string][]byte{"regular": goregular.TTF, "italic": goitalic.TTF}
	if cantarell, err := os.ReadFile("testdata/Cantarell-Regular.ttf"); err == nil {
		faces["cantarell"] = cantarell
	}
	for name, data := range faces {
		tf, err := LoadFont(data)
		if err != nil {
			t.Fatal(err)
		}
		for _, text := range []string{"row 12", "Åström Ǻ ÿ", "fjord ʄ", "Ĳ ŀ ǅ"} {
			s := tf.Shape(text, 18)
			reach, ok := s.InkOverhang()
			if !ok {
				t.Errorf("%s %q: no overhang for an outline face", name, text)
				continue
			}
			const x, baseline, w, h = 40, 60, 400, 120
			line := Rect{X: x, Y: baseline - int(s.Ascent()), W: int(s.Advance()) + 1, H: s.LineHeight() + 1}
			if painted := paintedBox(t, s, x, baseline, w, h); painted.Intersect(reach.Grow(line)) != painted {
				t.Errorf("%s %q painted %+v, outside the line box %+v grown by %+v", name, text, painted, line, reach)
			}
		}
	}
}

func TestTheInkOverhangOfPlainLatinIsSmall(t *testing.T) {
	tf, err := LoadFont(goregular.TTF)
	if err != nil {
		t.Fatal(err)
	}
	reach, ok := tf.Shape("row 12 of the list", 14).InkOverhang()
	if !ok {
		t.Fatal("no overhang for an outline face")
	}
	if max(reach.Top, reach.Right, reach.Bottom, reach.Left) > 2 {
		t.Errorf("plain latin overhangs %+v, want at most the antialiasing pixel and a rounding", reach)
	}
}
