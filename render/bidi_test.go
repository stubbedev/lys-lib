package render

// The bidirectional shaping tests: mixed Hebrew/Latin lines resolve
// into visual-order pieces the shaper draws reading-correct, the caret
// table follows the line visually, and the base direction is part of
// the shaping cache key. Hebrew shapes through the fixture chain —
// Cantarell covers no Hebrew (see render/testdata/README.md).

import (
	"math"
	"testing"

	"github.com/stubbedev/gelm/internal/text"
)

func testChain(t testing.TB) *Chain {
	t.Helper()
	c, err := FixtureChain()
	if err != nil {
		t.Fatalf("load fixture chain: %v", err)
	}
	return c
}

func TestShapeBidiRuns(t *testing.T) {
	c := testChain(t)

	t.Run("mixed line shapes one piece per directional run and face", func(t *testing.T) {
		s := c.ShapeDir("hello אבג 123", 14, text.DirectionAuto)
		// Visual order: the Latin head, then the digits run (even level
		// 2, placed left of the Hebrew), then the Hebrew run — its
		// trailing space shapes Latin-side but carries its direction.
		if s.Runs() != 4 {
			t.Fatalf("runs = %d, want 4", s.Runs())
		}
		wantRTL := []bool{false, false, true, true}
		for i, r := range s.runs {
			if r.rtl != wantRTL[i] {
				t.Errorf("run %d rtl = %v, want %v", i, r.rtl, wantRTL[i])
			}
		}
	})
	t.Run("RTL base puts the Latin island after the Hebrew", func(t *testing.T) {
		s := c.ShapeDir("אבג hello", 14, text.DirectionAuto)
		if s.Runs() != 3 {
			t.Fatalf("runs = %d, want 3", s.Runs())
		}
		// Visual order: the Latin island (even level) draws leftmost,
		// then the Hebrew block — its trailing space shapes Latin-side
		// but carries the block's direction.
		wantRTL := []bool{false, true, true}
		for i, r := range s.runs {
			if r.rtl != wantRTL[i] {
				t.Errorf("run %d rtl = %v, want %v", i, r.rtl, wantRTL[i])
			}
		}
	})
	t.Run("pure LTR text still shapes one run", func(t *testing.T) {
		if s := c.Shape("hello world", 14); s.Runs() != 1 {
			t.Errorf("runs = %d, want 1", s.Runs())
		}
	})
	t.Run("explicit direction is part of the cache key", func(t *testing.T) {
		a := c.ShapeDir("אבג 123", 14, text.DirectionLTR)
		b := c.ShapeDir("אבג 123", 14, text.DirectionRTL)
		if a == b {
			t.Error("same text under different directions shared one shaped line")
		}
		if again := c.ShapeDir("אבג 123", 14, text.DirectionRTL); again != b {
			t.Error("repeat ShapeDir missed the cache")
		}
	})
}

func TestShapeBidiCarets(t *testing.T) {
	tf, err := NewFixtureTypeface()
	if err != nil {
		t.Fatalf("load fixture face: %v", err)
	}
	s := tf.ShapeDir("abc אבג", 14, text.DirectionAuto)
	carets := s.CaretPositions()
	n := len([]rune(s.Text()))
	if len(carets) != n+1 {
		t.Fatalf("caret table has %d entries, want %d", len(carets), n+1)
	}
	t.Run("the LTR head is monotone", func(t *testing.T) {
		for i := 1; i < 4; i++ {
			if carets[i] <= carets[i-1] {
				t.Errorf("carets[%d] = %v not after %v", i, carets[i], carets[i-1])
			}
		}
	})
	t.Run("the junction maps both runs onto one x", func(t *testing.T) {
		// Boundary 4 (before the Hebrew run's first rune) and boundary 7
		// (after its last) are the two views of the LTR|RTL junction:
		// one x.
		if math.Abs(carets[4]-carets[7]) > 0.01 {
			t.Errorf("junction carets = %v vs %v, want equal", carets[4], carets[7])
		}
	})
	t.Run("the Hebrew boundaries run right to left inside the block", func(t *testing.T) {
		// The block reads right to left: the caret before ב sits left
		// of the caret before א, and the whole block spans right of the
		// junction.
		if carets[6] >= carets[5] {
			t.Errorf("carets[6] = %v not left of %v", carets[6], carets[5])
		}
		if carets[5] <= carets[7] {
			t.Errorf("carets[5] = %v not right of the junction %v", carets[5], carets[7])
		}
	})
	t.Run("CaretAt inverts CaretX for either direction", func(t *testing.T) {
		for i, x := range carets {
			got := s.CaretAt(x)
			if math.Abs(carets[got]-x) > 0.01 {
				t.Errorf("CaretAt(%v) = %d at %v, want a boundary at x", x, got, carets[got])
			}
			_ = i
		}
	})
}

func TestAppendCaretBands(t *testing.T) {
	c := testChain(t)
	s := c.ShapeDir("abc אבג def", 14, text.DirectionAuto)

	t.Run("one band per run the selection touches", func(t *testing.T) {
		var bands [][2]float64
		bands = s.AppendCaretBands(bands, 2, 6) // crosses into the Hebrew run
		if len(bands) != 2 {
			t.Fatalf("bands = %v, want two", bands)
		}
		if bands[0][1] <= bands[0][0] || bands[1][1] <= bands[1][0] {
			t.Errorf("bands not positive-spanning: %v", bands)
		}
		for i := 1; i < len(bands); i++ {
			if bands[i][0] < bands[i-1][1] {
				t.Errorf("band %d starts %v before band %d ends %v", i, bands[i][0], i-1, bands[i-1][1])
			}
		}
	})
	t.Run("an LTR-only selection is one band", func(t *testing.T) {
		var bands [][2]float64
		bands = s.AppendCaretBands(bands, 1, 3)
		if len(bands) != 1 {
			t.Fatalf("bands = %v, want one", bands)
		}
	})
	t.Run("an RTL-only selection is one positive band", func(t *testing.T) {
		var bands [][2]float64
		bands = s.AppendCaretBands(bands, 4, 7)
		if len(bands) != 1 {
			t.Fatalf("bands = %v, want one", bands)
		}
		if bands[0][1] <= bands[0][0] {
			t.Errorf("RTL band inverted: %v", bands)
		}
	})
	t.Run("empty and clamped ranges add nothing", func(t *testing.T) {
		var bands [][2]float64
		if got := s.AppendCaretBands(bands, 3, 3); len(got) != 0 {
			t.Errorf("empty range bands = %v", got)
		}
		if got := s.AppendCaretBands(bands, 99, 100); len(got) != 0 {
			t.Errorf("clamped-out bands = %v", got)
		}
	})
	t.Run("the buffer is reused", func(t *testing.T) {
		buf := make([][2]float64, 0, 4)
		buf = s.AppendCaretBands(buf, 0, 11)
		capBefore := cap(buf)
		buf = buf[:0]
		buf = s.AppendCaretBands(buf, 0, 11)
		if cap(buf) != capBefore {
			t.Error("AppendCaretBands reallocated the caller's buffer")
		}
	})
}

// TestGoldenBidiLine pins the pixels of mixed-direction lines: a
// Latin+Hebrew+numbers line under auto resolution, an RTL paragraph
// with a Latin island, and start alignment mirrored by an explicit RTL
// base — the same pixels at one device scale.
func TestGoldenBidiLine(t *testing.T) {
	c := testChain(t)
	goldenCanvas(t, "bidi-line", 260, 96, func(cv *Canvas) {
		col := RGB(0xCD, 0xD6, 0xF4)
		auto := c.ShapeDir("hello אבג 123!", 15, text.DirectionAuto)
		auto.Draw(cv, 8, 24, col)
		rtl := c.ShapeDir("אבג hello אבג", 15, text.DirectionAuto)
		rtl.Draw(cv, 8, 48, col)
		c.DrawAlignedDir(cv, "start-aligned RTL", Rect{X: 8, Y: 60, W: 244, H: 28}, 15, col, AlignStart, text.DirectionRTL)
	})
}
