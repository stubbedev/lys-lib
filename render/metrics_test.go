package render

import (
	"math"
	"os"
	"testing"
)

// loadCantarell loads the Cantarell test face.
func loadCantarell(tb testing.TB) *Typeface {
	tb.Helper()
	data, err := os.ReadFile("testdata/Cantarell-Regular.ttf")
	if err != nil {
		tb.Fatalf("read Cantarell: %v", err)
	}
	tf, err := LoadFont(data)
	if err != nil {
		tb.Fatalf("load Cantarell: %v", err)
	}
	return tf
}

// TestFaceMetrics pins Metrics against the shaper's own numbers: ch is
// the shaped advance of "0" per font size, and the vertical ratios sit
// in their natural order (x-height below cap height below the line
// height).
func TestFaceMetrics(t *testing.T) {
	tf := loadCantarell(t)
	m := tf.Metrics()
	if m != tf.Metrics() {
		t.Fatal("Metrics not memoized")
	}
	const px = 64.0
	adv := tf.ShapeRune('0', px).Advance()
	if want := adv / px; !floatNear(m.Ch, want, 1e-9) {
		t.Errorf("Ch = %v, want the shaped advance ratio %v", m.Ch, want)
	}
	if m.Ch <= 0 || m.Ch >= 1 {
		t.Errorf("Ch = %v, want a fraction of the em", m.Ch)
	}
	if !(m.Ex > 0 && m.Ex < m.Cap && m.Cap <= m.LineHeight) {
		t.Errorf("metric order: ex %v cap %v line %v", m.Ex, m.Cap, m.LineHeight)
	}
	if !(m.LineHeight > 1 && m.LineHeight < 2) {
		t.Errorf("LineHeight = %v, want a plausible natural line box", m.LineHeight)
	}
	if m.Ic != 0 {
		t.Errorf("Ic = %v, want 0: Cantarell has no ideographic glyph, the CSS fallback applies", m.Ic)
	}
}

// TestChainMetrics pins that a chain reports its primary face's
// metrics.
func TestChainMetrics(t *testing.T) {
	primary := loadCantarell(t)
	c := NewChain(primary)
	if c.Metrics() != primary.Metrics() {
		t.Error("chain metrics differ from the primary face")
	}
}

func floatNear(a, b, tol float64) bool {
	return math.Abs(a-b) <= tol
}
