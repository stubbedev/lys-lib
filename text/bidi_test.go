package text

// The bidirectional resolution tests: level and reordering vectors from
// UAX #9 (uppercase letters stand for right-to-left letters, the spec's
// own convention, realized here as Hebrew), a cross-check against
// x/text's independent resolution of the same paragraphs, and the
// visual caret mapping the editable widgets move by.

import (
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"golang.org/x/text/unicode/bidi"
)

// expand repeats each run's direction over its span: the per-rune
// levels as parity, the only part UAX #9 conformance requires.
func expand(rs []rune, runs []Run) []int {
	levels := make([]int, len(rs))
	for _, r := range runs {
		lvl := 0
		if r.RTL {
			lvl = 1
		}
		for i := r.Start; i < r.End; i++ {
			levels[i] = lvl
		}
	}
	return levels
}

// upperIsRTL replaces uppercase ASCII with Hebrew letters, standing in
// for the spec's uppercase-means-RTL convention so the classic vectors
// read like the spec.
func upperIsRTL(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= 'A' && r <= 'Z' {
			b.WriteRune(r - 'A' + '\u05d0')
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// xtextLevels resolves s through x/text's own UAX #9 — the independent
// reference implementation this package's levels must agree with — and
// returns the per-rune level parity.
func xtextLevels(t *testing.T, s string, d Direction) []int {
	t.Helper()
	p := new(bidi.Paragraph)
	if d == DirectionRTL {
		if _, err := p.SetString(s, bidi.DefaultDirection(bidi.RightToLeft)); err != nil {
			t.Fatalf("x/text SetString: %v", err)
		}
	} else {
		if _, err := p.SetString(s); err != nil {
			t.Fatalf("x/text SetString: %v", err)
		}
	}
	o, err := p.Order()
	if err != nil {
		t.Fatalf("x/text Order: %v", err)
	}
	var levels []int
	for i := range o.NumRuns() {
		r := o.Run(i)
		lvl := 0
		if r.Direction() == bidi.RightToLeft {
			lvl = 1
		}
		for range utf8.RuneCountInString(r.String()) {
			levels = append(levels, lvl)
		}
	}
	return levels
}

func TestBidiRunsLevels(t *testing.T) {
	tests := []struct {
		name string
		text string
		dir  Direction
		want string // per-rune level parity
		rtl  bool
	}{
		{"pure LTR is one even run", "car means car.", DirectionAuto, "00000000000000", false},
		{"pure RTL is one odd run", "אבג אבג.", DirectionAuto, "11111111", true},
		{
			"UAX #9 3.1.1: car is THE CAR in arabic",
			upperIsRTL("car is THE CAR in arabic"),
			DirectionAuto,
			"0000000" + "1111111" + "0000000000",
			false,
		},
		{
			"weak types: digits stay even inside RTL",
			"hello אבג 123",
			DirectionAuto,
			"000000" + "1111" + "000",
			false,
		},
		{
			"punctuation takes the surrounding RTL",
			"שלום, world!",
			DirectionAuto,
			"111111" + "00000" + "1",
			true,
		},
		{
			"forced RTL keeps Latin at an even level",
			"car means car.",
			DirectionRTL,
			"0000000000000" + "1",
			true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rs := []rune(tt.text)
			runs := BidiRuns(tt.text, tt.dir)
			if len(runs) == 0 && tt.text != "" {
				t.Fatalf("no runs for %q", tt.text)
			}
			if levelsString(expand(rs, runs)) != tt.want {
				t.Errorf("levels = %s, want %s", levelsString(expand(rs, runs)), tt.want)
			}
			if got := RTL(tt.text, tt.dir); got != tt.rtl {
				t.Errorf("RTL = %v, want %v", got, tt.rtl)
			}
			for _, r := range runs {
				if (r.Level%2 == 1) != r.RTL {
					t.Errorf("run %+v: level parity and RTL disagree", r)
				}
				if r.Start >= r.End {
					t.Errorf("run %+v: empty span", r)
				}
			}
		})
	}
}

func levelsRunes(levels []int) []rune {
	out := make([]rune, len(levels))
	for i, l := range levels {
		out[i] = rune('0' + l)
	}
	return out
}

func levelsString(levels []int) string { return string(levelsRunes(levels)) }

// TestBidiRunsAutoDetect pins the first-strong rule the Direction API
// documents: forced directions always answer, auto reads the first
// strong character, and neutrals-only text never flips.
func TestBidiRunsAutoDetect(t *testing.T) {
	cases := []struct {
		text string
		want bool
	}{
		{"hello", false},
		{"אבג", true},
		{"123 - 456", false}, // neutrals only: rule P3 defaults LTR
		{"... !!!", false},   // neutrals only
		{"      אבג", true},  // leading whitespace skipped
		{"אבג hello", true},  // first strong wins
		{"hello אבג", false}, // first strong wins
		{"aאb", false},       // first strong character decides
		{"", false},          // empty: LTR, no runs
	}
	for _, c := range cases {
		if got := RTL(c.text, DirectionAuto); got != c.want {
			t.Errorf("RTL(%q, auto) = %v, want %v", c.text, got, c.want)
		}
	}
	if RTL("אבג", DirectionLTR) {
		t.Error("forced LTR must resolve left to right even for RTL text")
	}
	if !RTL("hello", DirectionRTL) {
		t.Error("forced RTL must resolve right to left even for LTR text")
	}
	if runs := BidiRuns("", DirectionAuto); len(runs) != 0 {
		t.Errorf("empty text runs = %v, want none", runs)
	}
}

// TestBidiRunsMatchesXText cross-checks the per-rune level parity
// against x/text's resolution of the same paragraphs — two ports of the
// reference implementation agreeing rule by rule.
func TestBidiRunsMatchesXText(t *testing.T) {
	cases := []struct {
		text string
		dir  Direction
	}{
		{"car means CAR.", DirectionAuto},
		{"hello אבג 123", DirectionAuto},
		{"שלום, world!", DirectionAuto},
		{"he said \"I NEED WATER!\", and expired.", DirectionAuto},
		{"ערכים 123, 456, 789, בסדר?", DirectionAuto},
		{"אבג hello אבג", DirectionAuto},
		{"car means CAR.", DirectionRTL},
		{"hello אבג 123", DirectionRTL},
		{"123 - 456", DirectionRTL},
	}
	for _, c := range cases {
		rs := []rune(c.text)
		mine := expand(rs, BidiRuns(c.text, c.dir))
		ref := xtextLevels(t, c.text, c.dir)
		if !slices.Equal(mine, ref) {
			t.Errorf("%q (dir %d): levels %v, x/text %v", c.text, c.dir, levelsString(mine), levelsString(ref))
		}
	}
}

// TestBidiRunsDisplayOrder pins the visual composition: runs in the
// order BidiRuns returns them, RTL pieces reversed the way the shaper
// draws them, read off the classic UAX #9 §3.4 displays and their
// weak-type refinements.
func TestBidiRunsDisplayOrder(t *testing.T) {
	cases := []struct {
		name string
		text string
		dir  Direction
		want string
	}{
		{
			// §3.4 example 1: levels 00000000001110, display car means
			// RAC — the Hebrew word reversed, the period left alone.
			"UAX #9 3.4 example 1", upperIsRTL("car means CAR."), DirectionAuto,
			"car means סאג.",
		},
		{
			// The digits keep their order and sit between the Latin and
			// the reversed Hebrew.
			"mixed line with weak types", "hello אבג 123", DirectionAuto,
			"hello 123 גבא",
		},
		{
			// An RTL paragraph reads right to left: the last word comes
			// back leftmost, the Latin island keeps its order, and the
			// neutral spaces ride with the Hebrew runs.
			"RTL base with Latin island", "אבג hello אבג", DirectionAuto,
			"גבא hello גבא",
		},
		{
			// Forced RTL: the trailing period takes the paragraph
			// direction and lands leftmost.
			"forced RTL over Latin", "car means car.", DirectionRTL,
			".car means car",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rs := []rune(c.text)
			var b strings.Builder
			for _, r := range BidiRuns(c.text, c.dir) {
				piece := string(rs[r.Start:r.End])
				if r.RTL {
					b.Write(bidi.AppendReverse(nil, []byte(piece)))
					continue
				}
				b.WriteString(piece)
			}
			if got := b.String(); got != c.want {
				t.Errorf("visual = %q, want %q", got, c.want)
			}
		})
	}
}

// TestVisualOrder pins the caret boundary sequence the arrow keys walk.
func TestVisualOrder(t *testing.T) {
	t.Run("pure LTR is the logical order", func(t *testing.T) {
		rs := []rune("abc def")
		order := VisualOrder(rs, BidiRuns(string(rs), DirectionAuto))
		want := []int{0, 1, 2, 3, 4, 5, 6, 7}
		if !slices.Equal(order, want) {
			t.Errorf("order = %v, want %v", order, want)
		}
	})
	t.Run("pure RTL reads right to left", func(t *testing.T) {
		rs := []rune("אבג")
		order := VisualOrder(rs, BidiRuns(string(rs), DirectionAuto))
		want := []int{3, 2, 1, 0}
		if !slices.Equal(order, want) {
			t.Errorf("order = %v, want %v", order, want)
		}
	})
	t.Run("mixed line crosses at the run junction", func(t *testing.T) {
		rs := []rune("abc אבג")
		order := VisualOrder(rs, BidiRuns(string(rs), DirectionAuto))
		want := []int{0, 1, 2, 3, 4, 7, 6, 5}
		if !slices.Equal(order, want) {
			t.Errorf("order = %v, want %v", order, want)
		}
	})
	t.Run("numbers stay left to right inside RTL", func(t *testing.T) {
		rs := []rune("אבג 123")
		order := VisualOrder(rs, BidiRuns(string(rs), DirectionAuto))
		// Visual: the digits leftmost (logical 4..6 in order), then the
		// Hebrew word reversed.
		want := []int{4, 5, 6, 7, 3, 2, 1, 0}
		if !slices.Equal(order, want) {
			t.Errorf("order = %v, want %v", order, want)
		}
	})
	t.Run("every boundary appears exactly once", func(t *testing.T) {
		rs := []rune("hello אבג 123 world")
		order := VisualOrder(rs, BidiRuns(string(rs), DirectionAuto))
		got := slices.Clone(order)
		slices.Sort(got)
		want := make([]int, len(rs)+1)
		for i := range want {
			want[i] = i
		}
		if !slices.Equal(got, want) {
			t.Errorf("order is not a permutation of 0..%d: %v", len(rs), order)
		}
	})
}

func TestVisualStep(t *testing.T) {
	rs := []rune("abc אבג")
	order := VisualOrder(rs, BidiRuns(string(rs), DirectionAuto))
	t.Run("LTR side steps logically", func(t *testing.T) {
		if got := VisualStep(rs, order, 1, 1); got != 2 {
			t.Errorf("right from 1 = %d, want 2", got)
		}
		if got := VisualStep(rs, order, 1, -1); got != 0 {
			t.Errorf("left from 1 = %d, want 0", got)
		}
	})
	t.Run("crossing the junction walks around the Hebrew block", func(t *testing.T) {
		if got := VisualStep(rs, order, 3, 1); got != 4 {
			t.Errorf("right from 3 = %d, want 4", got)
		}
		// Boundary 4 sits at the Hebrew block's left edge; the next
		// boundary to the right is the block's other edge, logical 7.
		if got := VisualStep(rs, order, 4, 1); got != 7 {
			t.Errorf("right from 4 = %d, want 7", got)
		}
		if got := VisualStep(rs, order, 4, -1); got != 3 {
			t.Errorf("left from 4 = %d, want 3", got)
		}
	})
	t.Run("inside the Hebrew run arrows move visually", func(t *testing.T) {
		// Boundaries 5 and 6 sit between the Hebrew letters, visually
		// ordered after 7: right from 6 climbs toward א, left from 5
		// drops toward ג.
		if got := VisualStep(rs, order, 5, -1); got != 6 {
			t.Errorf("left from 5 = %d, want 6", got)
		}
		if got := VisualStep(rs, order, 6, 1); got != 5 {
			t.Errorf("right from 6 = %d, want 5", got)
		}
		if got := VisualStep(rs, order, 7, 1); got != 6 {
			t.Errorf("right from 7 = %d, want 6", got)
		}
	})
	t.Run("the visual ends come back unchanged", func(t *testing.T) {
		if got := VisualStep(rs, order, 0, -1); got != 0 {
			t.Errorf("left from 0 = %d, want 0", got)
		}
		if got := VisualStep(rs, order, 5, 1); got != 5 {
			t.Errorf("right from the visual end = %d, want 5", got)
		}
	})
	t.Run("positions inside a cluster snap first", func(t *testing.T) {
		marked := []rune("a\u0301ב")
		mOrder := VisualOrder(marked, BidiRuns(string(marked), DirectionAuto))
		// Rune 1 is the combining mark inside "á": stepping right
		// crosses the whole cluster to the next boundary.
		if got := VisualStep(marked, mOrder, 1, 1); got != 2 {
			t.Errorf("right from the mark = %d, want 2", got)
		}
		if got := VisualStep(marked, mOrder, 1, -1); got != 0 {
			t.Errorf("left from the mark = %d, want 0", got)
		}
	})
}

func TestVisualWordStep(t *testing.T) {
	newOrder := func(s string) ([]rune, []int) {
		rs := []rune(s)
		return rs, VisualOrder(rs, BidiRuns(string(rs), DirectionAuto))
	}
	t.Run("word ends rightward, stops at the end", func(t *testing.T) {
		rs, order := newOrder("alpha beta")
		for _, c := range []struct{ at, want int }{{0, 5}, {5, 10}, {10, 10}, {3, 5}, {6, 10}} {
			if got := VisualWordStep(rs, order, c.at, 1); got != c.want {
				t.Errorf("word right from %d = %d, want %d", c.at, got, c.want)
			}
		}
	})
	t.Run("word starts leftward, stops at the start", func(t *testing.T) {
		rs, order := newOrder("alpha beta")
		for _, c := range []struct{ at, want int }{{10, 6}, {6, 0}, {0, 0}, {8, 6}} {
			if got := VisualWordStep(rs, order, c.at, -1); got != c.want {
				t.Errorf("word left from %d = %d, want %d", c.at, got, c.want)
			}
		}
	})
	t.Run("mixed line steps through the Hebrew block", func(t *testing.T) {
		rs, order := newOrder("alpha אבג beta")
		// From after "alpha" (5, the Hebrew block's left edge): word
		// right steps across the block to its far edge (9); word right
		// again lands at beta's end.
		if got := VisualWordStep(rs, order, 5, 1); got != 9 {
			t.Errorf("word right from 5 = %d, want 9 (far edge of the Hebrew word)", got)
		}
		if got := VisualWordStep(rs, order, 9, 1); got != 14 {
			t.Errorf("word right from 9 = %d, want 14 (end of beta)", got)
		}
		// Word left from beta's start (10) crosses the block to its
		// start boundary (6); from there on to the line start.
		if got := VisualWordStep(rs, order, 10, -1); got != 6 {
			t.Errorf("word left from 10 = %d, want 6 (start of the Hebrew word)", got)
		}
		if got := VisualWordStep(rs, order, 6, -1); got != 0 {
			t.Errorf("word left from 6 = %d, want 0", got)
		}
	})
}

// TestBidiCache pins the (text, direction) keyed reuse: the second
// resolution of the same paragraph is the identical, shared result,
// and a direction change resolves separately.
func TestBidiCache(t *testing.T) {
	first := BidiRuns("hello אבג 123", DirectionAuto)
	second := BidiRuns("hello אבג 123", DirectionAuto)
	if &first[0] != &second[0] {
		t.Error("second resolution returned a fresh slice, want the cached one")
	}
	if forced := BidiRuns("hello אבג 123", DirectionRTL); &forced[0] == &first[0] {
		t.Error("a direction change must not serve the auto resolution")
	}
	if other := BidiRuns("hello אבג 456", DirectionAuto); &other[0] == &first[0] {
		t.Error("a text change must not serve the cached resolution")
	}
}
