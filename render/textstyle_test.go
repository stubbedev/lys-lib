package render

import (
	"testing"

	itext "github.com/stubbedev/gelm/internal/text"
)

// Letter spacing widens every cluster's advance by the spacing, and the
// caret table follows: measuring and painting agree by construction.
func TestLetterSpacing(t *testing.T) {
	f, err := NewFixtureTypeface()
	if err != nil {
		t.Fatal(err)
	}
	base := f.Shape("Hello", 20)
	sp := f.Spaced(3)
	spaced := sp.Shape("Hello", 20)
	if d := spaced.Advance() - base.Advance(); d < 14.9 || d > 15.1 {
		t.Errorf("five clusters at 3px added %v, want 15", d)
	}
	if spaced.CaretX(2)-base.CaretX(2) < 5.9 || spaced.CaretX(2)-base.CaretX(2) > 6.1 {
		t.Errorf("caret 2 moved %v, want 6", spaced.CaretX(2)-base.CaretX(2))
	}
	if f.Spaced(3) != sp || f.Spaced(0) != f || sp.Spaced(3) != sp {
		t.Error("Spaced is not memoized")
	}
	chain := NewChain(f).Spaced(3)
	if d := chain.Shape("Hello", 20).Advance() - base.Advance(); d < 14.9 || d > 15.1 {
		t.Errorf("chain spacing added %v", d)
	}
}

// rowInk reports which canvas rows inside [x0, x1) carry ink.
func rowInk(buf []byte, stride, x0, x1, h int) []bool {
	out := make([]bool, h)
	for y := range h {
		for x := x0; x < x1; x++ {
			if buf[y*stride+x*4+3] > 0 {
				out[y] = true
				break
			}
		}
	}
	return out
}

// near reports ink within a row of y (a thin line rounds to one side).
func near(rows []bool, y float64) bool {
	r := int(y)
	return rows[r-1] || rows[r] || rows[r+1]
}

// Each decoration line lands where the face's metrics put it: the
// underline below the baseline, the line-through above it, the
// overline at the ascent - spanning the whole advance, left to right
// or right to left alike.
func TestDecorationPlacement(t *testing.T) {
	f, _ := NewFixtureTypeface()
	const w, h = 160, 50
	draw := func(s string, d Decoration) ([]byte, *ShapedText) {
		buf := make([]byte, Stride(w)*h)
		cv := New(buf, Stride(w), w, h)
		sh := f.ShapeDir(s, 20, itext.DirectionAuto)
		sh.DrawDecoration(cv, 10, 30, d, RGB(255, 255, 255))
		return buf, sh
	}
	buf, sh := draw("Under", Decoration{Lines: Underline})
	rows := rowInk(buf, Stride(w), 10, 10+int(sh.Advance()), h)
	m := decorationMetrics(f, 20)
	if !near(rows, 30+m.under) || rows[20] {
		t.Errorf("underline rows: %v (metric %v)", rows, m.under)
	}
	buf, _ = draw("Strike", Decoration{Lines: LineThrough})
	if rows := rowInk(buf, Stride(w), 12, 40, h); !near(rows, 30-m.strike) || rows[33] {
		t.Error("line-through misplaced")
	}
	buf, sh = draw("Over", Decoration{Lines: Overline})
	if rows := rowInk(buf, Stride(w), 12, 40, h); !near(rows, 30-sh.Ascent()+m.thick/2) || rows[30] {
		t.Error("overline misplaced")
	}
	// A right-to-left line decorates its full visual extent.
	hb, _ := NewFixtureHebrewTypeface()
	rtl := hb.ShapeDir("שלום עולם", 20, itext.DirectionRTL)
	buf = make([]byte, Stride(w)*h)
	cv := New(buf, Stride(w), w, h)
	rtl.DrawDecoration(cv, 10, 30, Decoration{Lines: Underline}, RGB(255, 255, 255))
	y := 30 + int(decorationMetrics(hb, 20).under+0.5)
	row := buf[y*Stride(w):]
	if row[11*4+3] == 0 || row[(10+int(rtl.Advance())-2)*4+3] == 0 {
		t.Error("the RTL underline does not span the line")
	}
}

// TestGoldenDecorations pins the decoration styles and letter spacing.
func TestGoldenDecorations(t *testing.T) {
	f, _ := NewFixtureTypeface()
	white := RGB(0xee, 0xee, 0xff)
	goldenCanvas(t, "decorations", 300, 170, func(cv *Canvas) {
		lines := []struct {
			s string
			d Decoration
		}{
			{"Underline solid", Decoration{Lines: Underline}},
			{"Double over", Decoration{Lines: Overline | Underline, Style: DecorationDouble}},
			{"Dotted strike", Decoration{Lines: LineThrough, Style: DecorationDotted, Color: RGB(0xf3, 0x8b, 0xa8)}},
			{"Dashed under", Decoration{Lines: Underline, Style: DecorationDashed}},
			{"Wavy spelling", Decoration{Lines: Underline, Style: DecorationWavy, Color: RGB(0xf3, 0x8b, 0xa8)}},
		}
		for i, l := range lines {
			sh := f.Shape(l.s, 16)
			sh.Draw(cv, 8, 24+i*26, white)
			sh.DrawDecoration(cv, 8, 24+i*26, l.d, white)
		}
		sp := f.Spaced(4)
		sp.Draw(cv, sp.Shape("spaced", 16), 170, 24, white)
	})
}
