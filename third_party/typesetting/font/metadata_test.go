package font

import (
	"testing"

	"github.com/stubbedev/lys-lib/third_party/typesetting/font/opentype/tables"
)

func lysAdvances(histogram map[int16]int) []tables.LongHorMetric {
	var out []tables.LongHorMetric
	for advance, count := range histogram {
		for range count {
			out = append(out, tables.LongHorMetric{AdvanceWidth: advance})
		}
	}
	return out
}

func TestMonospaceAdvancesAreCellMultiples(t *testing.T) {
	for _, tc := range []struct {
		name      string
		advances  map[int16]int
		monospace bool
	}{
		{"single width", map[int16]int{600: 500, 0: 40}, true},
		{"single width within rounding", map[int16]int{600: 500, 595: 3, 605: 2}, true},
		{"Noto Sans Mono", map[int16]int{600: 3309, 0: 321, 1200: 241, 1800: 9}, true},
		{"fontconfig dual width", map[int16]int{500: 200, 1000: 800}, true},
		{"proportional", map[int16]int{278: 10, 333: 8, 500: 20, 556: 60, 611: 12, 667: 15, 722: 18, 778: 9, 833: 4, 944: 2}, false},
		{"mixed non-multiples", map[int16]int{600: 300, 900: 40}, false},
		{"wider than three cells", map[int16]int{600: 300, 2400: 2}, false},
		{"narrow outlier", map[int16]int{600: 300, 1: 1}, false},
		{"no advance at all", map[int16]int{0: 10}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := advancesAreCellMultiples(lysAdvances(tc.advances)); got != tc.monospace {
				t.Errorf("monospace = %v, want %v", got, tc.monospace)
			}
		})
	}
}

func TestIsMonospaceIgnoresTheNotdefGlyph(t *testing.T) {
	f := &Font{hmtx: tables.Hmtx{Metrics: append(
		[]tables.LongHorMetric{{AdvanceWidth: 1000}},
		lysAdvances(map[int16]int{600: 20, 1200: 4})...,
	)}}
	if !f.IsMonospace() {
		t.Error("a dual-width font with a wider .notdef is not monospace")
	}
	f.hmtx.Metrics = append(f.hmtx.Metrics, tables.LongHorMetric{AdvanceWidth: 700})
	if f.IsMonospace() {
		t.Error("a font with a 700 advance among 600s is monospace")
	}
}
