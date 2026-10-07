package render

import (
	"os"
	"testing"
)

// loadVF loads the Quicksand variable test face (wght 300-700).
func loadVF(t *testing.T) *Typeface {
	t.Helper()
	data, err := os.ReadFile("testdata/Quicksand-VF.ttf")
	if err != nil {
		t.Fatal(err)
	}
	tf, err := LoadFont(data)
	if err != nil {
		t.Fatal(err)
	}
	return tf
}

// TestVariations pins instancing: the axis probe, memoized instances
// over the default (not over each other), unknown axes ignored, and a
// static face untouched.
func TestVariations(t *testing.T) {
	vf := loadVF(t)
	if !vf.HasAxis("wght") || vf.HasAxis("wdth") || vf.HasAxis("bad") {
		t.Fatal("axis probe wrong")
	}
	bold := vf.Weighted(700)
	if bold == vf || vf.Weighted(700) != bold || bold.Weighted(700) != bold {
		t.Error("instances are not memoized per setting")
	}
	if vf.Weighted(400) == bold || vf.WithVariations(Variation{"wdth", 50}) != vf {
		t.Error("an unknown axis made an instance")
	}
	if got := bold.Variations(); len(got) != 1 || got[0].Value != 700 {
		t.Errorf("Variations() = %v", got)
	}
	light := bold.WithVariations(Variation{"wght", 300})
	if light.base != vf {
		t.Error("an instance of an instance did not start from the default")
	}
	// Heavier is wider: the advance grows with the weight.
	w := func(f *Typeface) float64 { return f.Shape("Weight", 40).Advance() }
	if regular := vf.Weighted(400); w(light) >= w(regular) || w(regular) >= w(bold) {
		t.Errorf("advances %v %v %v do not grow with weight", w(light), w(regular), w(bold))
	}
	if vf.Tabular().Weighted(700) == bold {
		t.Error("the tabular twin shares the plain instance")
	}
	static, _ := NewFixtureTypeface()
	if static.Weighted(700) != static || static.HasAxis("wght") {
		t.Error("a static face varied")
	}
}

func TestParseVariations(t *testing.T) {
	got, err := ParseVariations(`"wght" 650, 'wdth' 80.5`)
	if err != nil || len(got) != 2 || got[0] != (Variation{"wght", 650}) || got[1] != (Variation{"wdth", 80.5}) {
		t.Errorf("ParseVariations = %v %v", got, err)
	}
	if got, err := ParseVariations("normal"); got != nil || err != nil {
		t.Error("normal is not empty")
	}
	for _, bad := range []string{`"wg" 1`, `"wght"`, `"wght" x`} {
		if _, err := ParseVariations(bad); err == nil {
			t.Errorf("%q parsed", bad)
		}
	}
}

// ink sums the coverage a face lays down for s.
func ink(t *Typeface, s string) int {
	const w, h = 300, 60
	buf := make([]byte, Stride(w)*h)
	cv := New(buf, Stride(w), w, h)
	t.Draw(cv, t.Shape(s, 32), 4, 44, RGB(255, 255, 255))
	n := 0
	for i := 0; i < len(buf); i += 4 {
		n += int(buf[i])
	}
	return n
}

// The outlines vary, not just the advances: more weight, more ink.
func TestVariationOutlines(t *testing.T) {
	vf := loadVF(t)
	l, r, b := ink(vf.Weighted(300), "Weight"), ink(vf.Weighted(400), "Weight"), ink(vf.Weighted(700), "Weight")
	if l >= r || r >= b {
		t.Errorf("ink %d %d %d does not grow with weight", l, r, b)
	}
}

// TestGoldenVariableWeights pins the variable face at wght 300, 400
// and 700.
func TestGoldenVariableWeights(t *testing.T) {
	vf := loadVF(t)
	goldenCanvas(t, "variable-weights", 260, 110, func(cv *Canvas) {
		cv.Clear(cv.Rect(), RGB(0x1e, 0x1e, 0x2e))
		for i, w := range []int{300, 400, 700} {
			f := vf.Weighted(w)
			f.Draw(cv, f.Shape("Variable weight", 24), 8, 30+i*34, RGB(0xee, 0xee, 0xff))
		}
	})
}
