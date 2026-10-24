package render

import (
	"math"
	"testing"
)

// A run shaped at a fractional size measures at that size, not at the
// next whole pixel go-text scales the font to: half the width and
// ascent of the same run at twice the (whole) size.
func TestShapeAtAFractionalSizeIsExact(t *testing.T) {
	tf, err := NewFixtureTypeface()
	if err != nil {
		t.Fatal(err)
	}
	const text = "fractional sizes stay exact"
	for _, px := range []float64{11.5, 12.5, 13.5} {
		half, whole := tf.Shape(text, px), tf.Shape(text, 2*px)
		if math.Mod(2*px, 1) != 0 {
			t.Fatalf("test bug: %v is not whole", 2*px)
		}
		if got, want := half.Advance(), whole.Advance()/2; math.Abs(got-want) > 0.05*float64(len(text))/2 {
			t.Errorf("%vpx advance %.2f, want %.2f (half of %vpx)", px, got, want, 2*px)
		}
		if got, want := half.Ascent(), whole.Ascent()/2; math.Abs(got-want) > 0.05 {
			t.Errorf("%vpx ascent %.2f, want %.2f", px, got, want)
		}
	}
}
