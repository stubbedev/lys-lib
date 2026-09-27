package render

import (
	"testing"
)

// The display side of grapheme editing (#57): shaping treats a ZWJ
// family emoji and a base rune with its combining marks as single
// units, so an emoji family paints as one visible glyph group and the
// caret table offers no position inside it. The test face has no emoji
// glyphs — the .notdef boxes are beside the point — cluster merging is
// a text-processing property and shows in the shaped output.
func TestShapedTextKeepsGraphemeClustersWhole(t *testing.T) {
	tf := testTypeface(t)

	t.Run("a ZWJ family shapes as one cluster", func(t *testing.T) {
		const family = "👨‍👩‍👧" // 5 runes
		s := tf.Shape(family, 14)
		distinct := map[int]bool{}
		for i := range s.runs {
			for j := range s.runs[i].out.Glyphs {
				distinct[s.runs[i].out.Glyphs[j].TextIndex()] = true
			}
		}
		if len(distinct) != 1 {
			t.Errorf("family shaped as %d clusters, want 1 (one visible glyph group)", len(distinct))
		}
		x := s.CaretX(0)
		for c := 1; c <= 4; c++ {
			if s.CaretX(c) != x {
				t.Errorf("CaretX(%d) = %.2f, want snapped to the family start %.2f", c, s.CaretX(c), x)
			}
		}
		if s.CaretX(5) <= x {
			t.Errorf("CaretX(5) = %.2f, want past the family start %.2f", s.CaretX(5), x)
		}
		if got := s.CaretAt(x / 2); got != 0 && got != 5 {
			t.Errorf("caret inside the family resolved to %d, want a cluster edge", got)
		}
	})

	t.Run("e plus combining acute shapes as one cluster", func(t *testing.T) {
		s := tf.Shape("e\u0301", 14)
		distinct := map[int]bool{}
		for i := range s.runs {
			for j := range s.runs[i].out.Glyphs {
				distinct[s.runs[i].out.Glyphs[j].TextIndex()] = true
			}
		}
		if len(distinct) != 1 {
			t.Errorf("accented pair shaped as %d clusters, want 1", len(distinct))
		}
		if s.CaretX(1) != s.CaretX(0) {
			t.Errorf("caret before the mark = %.2f, want snapped to the base %.2f",
				s.CaretX(1), s.CaretX(0))
		}
	})

	t.Run("clusters do not leak across neighbors", func(t *testing.T) {
		s := tf.Shape("a👨‍👩‍👧b", 14)
		if got := s.CaretX(6); got <= s.CaretX(1) {
			t.Errorf("caret after the family = %.2f, want past the family start %.2f", got, s.CaretX(1))
		}
		if got := s.CaretX(7); got <= s.CaretX(6) {
			t.Errorf("caret after b = %.2f, want past the family %.2f", got, s.CaretX(6))
		}
	})
}
