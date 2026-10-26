package render

import (
	"testing"

	"golang.org/x/image/font/gofont/gomono"
	"golang.org/x/image/font/gofont/goregular"
)

// chainTestTypefaces loads two distinct fixture faces: the Go regular
// as the primary and the Go mono as the stand-in fallback face.
func chainTestTypefaces(t *testing.T) (*Typeface, *Typeface) {
	t.Helper()
	primary, err := LoadFont(goregular.TTF)
	if err != nil {
		t.Fatalf("load Go regular: %v", err)
	}
	mono, err := LoadFont(gomono.TTF)
	if err != nil {
		t.Fatalf("load Go mono: %v", err)
	}
	return primary, mono
}

// hanPicker returns a resolver standing in for a font store: it offers
// fallback for the Han and emoji test runes only, and counts its calls.
func hanPicker(mono *Typeface, calls *int) func(rune) *Typeface {
	return func(r rune) *Typeface {
		*calls++
		switch r {
		case '你', '好', '😀':
			return mono
		}
		return nil
	}
}

func TestCovers(t *testing.T) {
	primary, _ := chainTestTypefaces(t)
	if !primary.Covers('a') {
		t.Error("Go regular must cover latin a")
	}
	if primary.Covers('你') {
		t.Error("Go regular must not cover Han")
	}
}

func TestChain(t *testing.T) {
	primary, mono := chainTestTypefaces(t)

	t.Run("covered runes stay on the primary in one run", func(t *testing.T) {
		c := NewChain(primary)
		s := c.Shape("hello", 14)
		if s.Runs() != 1 {
			t.Errorf("Runs = %d, want 1", s.Runs())
		}
		if s.NotDefCount() != 0 {
			t.Errorf("NotDefCount = %d, want 0", s.NotDefCount())
		}
	})

	t.Run("uncovered runes stay on the primary when nothing covers", func(t *testing.T) {
		c := NewChain(primary)
		s := c.Shape("a你", 14)
		if s.Runs() != 1 {
			t.Errorf("Runs = %d, want 1 (no fallback installed)", s.Runs())
		}
		if s.NotDefCount() != 1 {
			t.Errorf("NotDefCount = %d, want 1 (.notdef for the Han rune)", s.NotDefCount())
		}
	})

	t.Run("mixed text splits into runs per face", func(t *testing.T) {
		var calls int
		c := NewChain(primary).WithResolver(hanPicker(mono, &calls))
		s := c.Shape("a你b😀c", 14)
		if s.Runs() != 5 {
			t.Errorf("Runs = %d, want 5 (latin/Han/latin/emoji/latin)", s.Runs())
		}
		if s.NotDefCount() != 2 {
			// The mono stand-in does not cover Han or emoji; the
			// pick machinery is what this pins, coverage comes
			// from the system store.
			t.Errorf("NotDefCount = %d, want 2", s.NotDefCount())
		}
		if calls != 2 {
			t.Errorf("resolver consulted %d times, want once per distinct fallback rune (2)", calls)
		}
	})

	t.Run("picks cache per rune", func(t *testing.T) {
		var calls int
		c := NewChain(primary).WithResolver(hanPicker(mono, &calls))
		c.faceFor('你')
		c.faceFor('你')
		if calls != 1 {
			t.Errorf("resolver called %d times for one rune, want 1", calls)
		}
	})

	t.Run("advance sums the runs", func(t *testing.T) {
		var calls int
		c := NewChain(primary).WithResolver(hanPicker(mono, &calls))
		s := c.Shape("a你b", 14)
		want := primary.Shape("a", 14).Advance() +
			mono.Shape("你", 14).Advance() +
			primary.Shape("b", 14).Advance()
		if diff := s.Advance() - want; diff < -0.01 || diff > 0.01 {
			t.Errorf("chain advance %.3f, want run sum %.3f", s.Advance(), want)
		}
	})

	t.Run("line metrics take the tallest run", func(t *testing.T) {
		var calls int
		c := NewChain(primary).WithResolver(hanPicker(mono, &calls))
		s := c.Shape("a你", 14)
		wantAsc := max(primary.Shape("a", 14).Ascent(), mono.Shape("你", 14).Ascent())
		if diff := s.Ascent() - wantAsc; diff < -0.01 || diff > 0.01 {
			t.Errorf("ascent %.3f, want max over runs %.3f", s.Ascent(), wantAsc)
		}
	})

	t.Run("empty text still reserves the primary's line height", func(t *testing.T) {
		c := NewChain(primary)
		s := c.Shape("", 14)
		if got := s.LineHeight(); got != primary.Shape("", 14).LineHeight() {
			t.Errorf("empty chain line height %d, want the primary's %d", got, primary.Shape("", 14).LineHeight())
		}
	})

	t.Run("carets stay monotone across run boundaries", func(t *testing.T) {
		var calls int
		c := NewChain(primary).WithResolver(hanPicker(mono, &calls))
		s := c.Shape("a你b", 14)
		prev := 0.0
		for i, x := range s.CaretPositions() {
			if x < prev-0.001 {
				t.Fatalf("caret %d at %.2f went backwards (prev %.2f)", i, x, prev)
			}
			prev = x
		}
	})

	t.Run("draw leaves ink from every run", func(t *testing.T) {
		var calls int
		c := NewChain(primary).WithResolver(hanPicker(mono, &calls))
		s := c.Shape("a你b", 16)
		cv, data := newTestCanvas(120, 32)
		c.Draw(cv, s, 4, 24, RGB(255, 255, 255))
		xs := s.CaretPositions()
		for r := range 3 {
			lo, hi := int(xs[r]+1), int(xs[r+1])
			count := 0
			for y := range 32 {
				for x := lo; x < hi && x < 120; x++ {
					if pxAt(data, Stride(120), x, y).A() > 8 {
						count++
					}
				}
			}
			if count == 0 {
				t.Errorf("run %d (x %d..%d) produced no ink", r, lo, hi)
			}
		}
	})

	t.Run("drawaligned and wrap work through the chain", func(t *testing.T) {
		var calls int
		c := NewChain(primary).WithResolver(hanPicker(mono, &calls))
		cv, data := newTestCanvas(120, 32)
		if s := c.DrawAligned(cv, "a你", Rect{X: 0, Y: 0, W: 120, H: 32}, 14, RGB(255, 255, 255), AlignStart); s == nil {
			t.Fatal("chain DrawAligned returned nil in a fitting box")
		}
		if _, count := inkBounds(data, Stride(120), 120, 32); count == 0 {
			t.Error("chain DrawAligned produced no ink")
		}
		lines := c.Wrap("aa 你 bb", 40, 14)
		if len(lines) < 2 {
			t.Errorf("chain Wrap produced %d lines, want >= 2", len(lines))
		}
		if got := c.Ellipsize("aa 你 bb cc dd", 60, 14); len(got) >= len("aa 你 bb cc dd") {
			t.Errorf("chain Ellipsize returned %q, want shortened", got)
		}
	})

	t.Run("chain text rasterizes at the device scale", func(t *testing.T) {
		var calls int
		c := NewChain(primary).WithResolver(hanPicker(mono, &calls))
		shape := c.Shape("H", 20) // shaped once, at the logical size
		col := RGB(255, 255, 255)
		inkHeight := func(cv *Canvas) int {
			c.Draw(cv, shape, 5, 40, col)
			minY, maxY := -1, -1
			for y := range cv.h {
				for x := range cv.w {
					if cv.get(x, y).A() > 0 {
						if minY < 0 {
							minY = y
						}
						maxY = y
					}
				}
			}
			if minY < 0 {
				t.Fatal("glyph painted nothing")
			}
			return maxY - minY + 1
		}
		one := NewScaled(make([]byte, Stride(100)*100), Stride(100), 100, 100, 120, 120)
		two := NewScaled(make([]byte, Stride(200)*200), Stride(200), 200, 200, 240, 120)
		if h1, h2 := inkHeight(one), inkHeight(two); h2 < h1*18/10 {
			t.Errorf("chain glyph ink height at 2x = %d px vs %d px at 1x, want roughly double", h2, h1)
		}
	})
}
