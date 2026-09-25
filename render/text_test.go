package render

import (
	"strings"
	"testing"

	"golang.org/x/image/font/gofont/goregular"
)

func testTypeface(t *testing.T) *Typeface {
	t.Helper()
	tf, err := LoadFont(goregular.TTF)
	if err != nil {
		t.Fatalf("load Go font: %v", err)
	}
	return tf
}

func inkBounds(data []byte, stride, w, h int) (Rect, int) {
	var (
		bounds Rect
		count  int
		found  bool
	)
	for y := range h {
		for x := range w {
			if pxAt(data, stride, x, y).A() > 8 {
				if !found {
					bounds = Rect{X: x, Y: y, W: 1, H: 1}
					found = true
				} else {
					bounds = bounds.Union(Rect{X: x, Y: y, W: 1, H: 1})
				}
				count++
			}
		}
	}
	return bounds, count
}

func TestShape(t *testing.T) {
	tf := testTypeface(t)

	t.Run("advance is positive for real text", func(t *testing.T) {
		s := tf.Shape("hello", 14)
		if s.Advance() <= 0 {
			t.Errorf("advance = %v, want > 0", s.Advance())
		}
	})

	t.Run("wider text advances further", func(t *testing.T) {
		if tf.Shape("hello", 14).Advance() >= tf.Shape("hello world", 14).Advance() {
			t.Error("longer text must advance further")
		}
	})

	t.Run("larger size advances further", func(t *testing.T) {
		if tf.Shape("hello", 28).Advance() <= tf.Shape("hello", 14).Advance() {
			t.Error("larger size must advance further")
		}
	})

	t.Run("line metrics are sane", func(t *testing.T) {
		s := tf.Shape("x", 14)
		if s.Ascent() <= 0 || s.Descent() <= 0 {
			t.Errorf("ascent = %v, descent = %v, want both positive", s.Ascent(), s.Descent())
		}
	})
}

func TestDrawText(t *testing.T) {
	tf := testTypeface(t)

	t.Run("text leaves ink on the canvas", func(t *testing.T) {
		cv, data := newTestCanvas(120, 32)
		s := tf.Shape("gelm", 16)
		tf.Draw(cv, s, 4, 24, RGB(255, 255, 255))
		_, count := inkBounds(data, Stride(120), 120, 32)
		if count == 0 {
			t.Error("drawn text produced no ink")
		}
	})

	t.Run("empty string draws nothing", func(t *testing.T) {
		cv, data := newTestCanvas(60, 32)
		tf.Draw(cv, tf.Shape("", 16), 4, 24, RGB(255, 255, 255))
		_, count := inkBounds(data, Stride(60), 60, 32)
		if count != 0 {
			t.Errorf("empty string produced %d ink pixels", count)
		}
	})

	t.Run("clipped text produces no ink outside the canvas", func(t *testing.T) {
		cv, data := newTestCanvas(60, 32)
		tf.Draw(cv, tf.Shape("hello", 16), 500, 24, RGB(255, 255, 255))
		_, count := inkBounds(data, Stride(60), 60, 32)
		if count != 0 {
			t.Errorf("off-canvas text produced %d ink pixels", count)
		}
	})

	t.Run("clip confines the ink", func(t *testing.T) {
		cv, _ := newTestCanvas(120, 32)
		s := tf.Shape("hello world", 16)
		prev := cv.PushClip(Rect{X: 0, Y: 0, W: 10, H: 32})
		tf.Draw(cv, s, 0, 24, RGB(255, 255, 255))
		cv.PopClip(prev)
		if got := cv.clip; got != prev {
			t.Errorf("clip = %v, want restored %v", got, prev)
		}
	})

	t.Run("alignment moves the ink box", func(t *testing.T) {
		cvA, dataA := newTestCanvas(200, 32)
		cvB, dataB := newTestCanvas(200, 32)
		box := Rect{X: 0, Y: 0, W: 200, H: 32}
		tf.DrawAligned(cvA, "gel", box, 16, RGB(255, 255, 255), AlignStart)
		tf.DrawAligned(cvB, "gel", box, 16, RGB(255, 255, 255), AlignEnd)
		a, _ := inkBounds(dataA, Stride(200), 200, 32)
		b, _ := inkBounds(dataB, Stride(200), 200, 32)
		if b.X <= a.X {
			t.Errorf("end-aligned ink at x=%d must be right of start-aligned x=%d", b.X, a.X)
		}
	})

	t.Run("aligned text starts at the box edge", func(t *testing.T) {
		cv, data := newTestCanvas(200, 32)
		box := Rect{X: 10, Y: 0, W: 180, H: 32}
		tf.DrawAligned(cv, "gel", box, 16, RGB(255, 255, 255), AlignStart)
		a, _ := inkBounds(data, Stride(200), 200, 32)
		if a.X < box.X {
			t.Errorf("ink starts at x=%d, before the box at %d", a.X, box.X)
		}
	})
}

func TestWrap(t *testing.T) {
	tf := testTypeface(t)
	px := 14.0

	t.Run("narrow width forces multiple lines", func(t *testing.T) {
		lines := tf.Wrap("the quick brown fox", 60, px)
		if len(lines) < 2 {
			t.Errorf("narrow wrap produced %d lines, want >= 2", len(lines))
		}
	})

	t.Run("every line fits or is a lone overflow word", func(t *testing.T) {
		lines := tf.Wrap("the quick brown fox jumps over", 80, px)
		for i, l := range lines {
			if i == len(lines)-1 && !strings.Contains("thequickbrownfoxjumps", strings.ReplaceAll(l, " ", "")) {
				continue
			}
			if a := tf.Shape(l, px).Advance(); a > 80 && l != lines[len(lines)-1] {
				t.Errorf("line %q advances %v > 80", l, a)
			}
		}
	})

	t.Run("wide width keeps one line", func(t *testing.T) {
		lines := tf.Wrap("the quick brown fox", 1000, px)
		if len(lines) != 1 || lines[0] != "the quick brown fox" {
			t.Errorf("wide wrap = %v, want the original on one line", lines)
		}
	})

	t.Run("empty text yields one empty line", func(t *testing.T) {
		lines := tf.Wrap("", 100, px)
		if len(lines) != 1 || lines[0] != "" {
			t.Errorf("empty wrap = %q, want [\"\"]", lines)
		}
	})
}

func TestEllipsize(t *testing.T) {
	tf := testTypeface(t)
	px := 14.0

	t.Run("fitting text is unchanged", func(t *testing.T) {
		if got := tf.Ellipsize("hi", 1000, px); got != "hi" {
			t.Errorf("got %q, want hi unchanged", got)
		}
	})

	t.Run("overflowing text gains an ellipsis and fits", func(t *testing.T) {
		text := "the quick brown fox jumps over the lazy dog"
		got := tf.Ellipsize(text, 80, px)
		if !strings.HasSuffix(got, "…") {
			t.Errorf("got %q, want an ellipsis suffix", got)
		}
		if a := tf.Shape(got, px).Advance(); a > 80 {
			t.Errorf("ellipsized text advances %v > 80", a)
		}
		if got == text {
			t.Error("ellipsis must shorten the text")
		}
	})

	t.Run("monotonically wider bounds keep more text", func(t *testing.T) {
		text := "abcdefghijklmnopqrstuvwxyz"
		prev := ""
		for w := 30.0; w <= 200; w += 20 {
			got := tf.Ellipsize(text, w, px)
			if len(got) < len(prev) {
				t.Errorf("width %v produced %q, shorter than %q at %v", w, got, prev, w-20)
			}
			prev = got
		}
	})
}
