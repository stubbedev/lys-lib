package render

// The CSS text property tests at the render seam: word spacing rides
// the shaped advances, feature specs pick the OpenType features, the
// wrap options trade UAX #14 for break-all/keep-all/hyphens/space
// preservation, and justify knows its separator count.

import (
	"strings"
	"testing"
)

func fixtureFace(t *testing.T) *Typeface {
	t.Helper()
	f, err := LoadFont(FixtureFontData())
	if err != nil {
		t.Fatalf("load fixture font: %v", err)
	}
	return f
}

func TestWordSpacingAdvances(t *testing.T) {
	f := fixtureFace(t)
	plain := f.Shape("a b c", 20)
	wide := f.WordSpaced(4).Shape("a b c", 20)
	if got := wide.Advance() - plain.Advance(); got < 7.9 || got > 8.1 {
		t.Errorf("word spacing 4px over 2 separators grew %v, want 8", got)
	}
	if f.WordSpaced(0) != f {
		t.Error("WordSpaced(0) is not the face itself")
	}
	if f.WordSpaced(4).WordSpaced(4) != f.WordSpaced(4) {
		t.Error("WordSpaced is not memoized")
	}
	noSpaces := f.WordSpaced(4).Shape("abc", 20)
	if noSpaces.Advance() != f.Shape("abc", 20).Advance() {
		t.Error("word spacing changed a run without separators")
	}
}

func TestFeatureSpec(t *testing.T) {
	f := fixtureFace(t)
	tab := f.WithFeatureSpec("tnum=1")
	if tab == f {
		t.Fatal("tnum spec returned the base face")
	}
	a := tab.Shape("1111", 20).Advance()
	b := tab.Shape("9999", 20).Advance()
	if a != b {
		t.Errorf("tnum digits advance %v vs %v, want uniform", a, b)
	}
	if f.WithFeatureSpec("") != f {
		t.Error("empty spec is not the base face")
	}
	if f.WithFeatureSpec("bad") != f {
		t.Error("malformed spec is not the base face")
	}
	if f.WithFeatureSpec("tnum=1") != tab {
		t.Error("WithFeatureSpec is not memoized")
	}
	if c := testChain(t); c.WithFeatureSpec("tnum=1") == c {
		t.Error("chain spec returned the base chain")
	}
}

func TestWrapOptions(t *testing.T) {
	f := fixtureFace(t)
	t.Run("hyphens none hides the soft hyphen", func(t *testing.T) {
		rows := WrapTextOpts(f, "auto\u00admatic", 40, 12, WrapOptions{Hyphens: HyphensOff})
		for _, r := range rows {
			if strings.Contains(r, "\u00ad") {
				t.Errorf("row %q kept a soft hyphen", r)
			}
		}
	})
	t.Run("hyphens manual renders the hyphen at the break", func(t *testing.T) {
		rows := WrapTextOpts(f, "photo\u00adgrapher", 40, 12, WrapOptions{Hyphens: HyphensManual})
		if len(rows) < 2 || !strings.HasSuffix(rows[0], "-") {
			t.Errorf("rows = %q, want a first row ending in -", rows)
		}
		for _, r := range rows[1:] {
			if strings.Contains(r, "\u00ad") {
				t.Errorf("row %q kept an interior soft hyphen", r)
			}
		}
	})
	t.Run("break-all breaks inside the long word", func(t *testing.T) {
		rows := WrapTextOpts(f, "abcdefghijklm", 40, 12, WrapOptions{Break: WrapWordBreakAll})
		if len(rows) < 2 {
			t.Fatalf("rows = %q, want the word split", rows)
		}
		for _, r := range rows {
			if f.Shape(r, 12).Advance() > 40.5 {
				t.Errorf("row %q overflows", r)
			}
		}
	})
	t.Run("keep-all keeps the words whole", func(t *testing.T) {
		rows := WrapTextOpts(f, "one two three", 40, 12, WrapOptions{Break: WrapWordKeepAll})
		whole := map[string]bool{"one": true, "two": true, "three": true}
		for _, r := range rows {
			for w := range strings.SplitSeq(strings.TrimRight(r, " "), " ") {
				if !whole[w] {
					t.Errorf("row %q broke the word %q", r, w)
				}
			}
		}
	})
	t.Run("preserved spaces ride the rows", func(t *testing.T) {
		rows := WrapTextOpts(f, "aa bb", 100, 12, WrapOptions{PreserveSpaces: true})
		if len(rows) != 1 || rows[0] != "aa bb" {
			t.Errorf("rows = %q, want the untouched row", rows)
		}
	})
	t.Run("plain WrapText is the zero options", func(t *testing.T) {
		a := WrapText(f, "one two three", 40, 12)
		b := WrapTextOpts(f, "one two three", 40, 12, WrapOptions{})
		if strings.Join(a, "|") != strings.Join(b, "|") {
			t.Errorf("default rows %q != %q", a, b)
		}
	})
}

func TestJustifySeparatorCount(t *testing.T) {
	f := fixtureFace(t)
	s := f.Shape("a b c", 12)
	if got := s.SeparatorCount(); got != 2 {
		t.Errorf("separators = %d, want 2", got)
	}
	if got := f.Shape("abc", 12).SeparatorCount(); got != 0 {
		t.Errorf("separators without spaces = %d", got)
	}
}

func TestDrawJustifiedPaints(t *testing.T) {
	f := fixtureFace(t)
	s := f.Shape("a b", 12)
	white := Color(0xffffffff)
	cvData := make([]byte, Stride(60)*20)
	cv := New(cvData, Stride(60), 60, 20)
	s.Draw(cv, 0, 14, white)
	blankData := make([]byte, Stride(60)*20)
	s.DrawJustified(New(blankData, Stride(60), 60, 20), 0, 14, white, 6)
	if string(cvData) == string(blankData) {
		t.Error("justify with a gap painted the plain draw")
	}
	zeroData := make([]byte, Stride(60)*20)
	s.DrawJustified(New(zeroData, Stride(60), 60, 20), 0, 14, white, 0)
	if string(cvData) != string(zeroData) {
		t.Error("justify with zero gap differs from Draw")
	}
}
