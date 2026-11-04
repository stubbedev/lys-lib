package gpu

import (
	"image"
	"math"
	"testing"

	"github.com/stubbedev/lys-lib/render"
)

func TestGlassAdjustIsTheFilterFunctions(t *testing.T) {
	g := Glass{Saturation: 1.7, Brightness: 0.85, Contrast: 1.25}
	var f render.Filters
	f.Append(render.FilterOp{Kind: render.FilterSaturate, Amount: float64(g.Saturation)})
	f.Append(render.FilterOp{Kind: render.FilterBrightness, Amount: float64(g.Brightness)})
	f.Append(render.FilterOp{Kind: render.FilterContrast, Amount: float64(g.Contrast)})
	steps := f.Steps(nil, render.FilterShape{}, 1, 1)
	apply := func(c [3]float64) [3]float64 {
		for _, s := range steps {
			var out [3]float64
			for r := range 3 {
				v := float64(s.Matrix.M[r*5+4])
				for k := range 3 {
					v += float64(s.Matrix.M[r*5+k]) * c[k]
				}
				out[r] = min(max(v, 0), 255)
			}
			c = out
		}
		return c
	}
	for _, c := range [][3]float64{{200, 100, 50}, {10, 20, 240}, {128, 128, 128}, {255, 255, 255}, {0, 0, 0}, {255, 0, 0}, {30, 200, 90}} {
		for _, a := range []float64{255, 128} {
			want := apply(c)
			k := a / 255
			got := g.Adjust([4]float32{float32(c[0] * k), float32(c[1] * k), float32(c[2] * k), float32(a)})
			for i := range 3 {
				if d := math.Abs(float64(got[i]) - want[i]*k); d > 1 {
					t.Errorf("%v at alpha %v, channel %d: %v, the filter functions give %v", c, a, i, got[i], want[i]*k)
				}
			}
			if float64(got[3]) != a {
				t.Errorf("%v: alpha became %v, want %v", c, got[3], a)
			}
		}
	}
}

func TestAdjustOfAnIdentityGlassKeepsTheColour(t *testing.T) {
	g := Glass{Saturation: 1, Brightness: 1, Contrast: 1}
	c := [4]float32{10, 120, 200, 200}
	got := g.Adjust(c)
	for i := range c {
		if d := math.Abs(float64(got[i] - c[i])); d > 0.01 {
			t.Errorf("channel %d: %v, want %v", i, got[i], c[i])
		}
	}
	if clear := g.Adjust([4]float32{}); clear != ([4]float32{}) {
		t.Errorf("a transparent pixel became %v", clear)
	}
}

func TestGlassRefractPlanBendsOutwardAlongTheEdge(t *testing.T) {
	g := Glass{Bezel: 8, Amount: 4}
	p := g.RefractPlan(RoundRect{Rect: image.Rect(0, 0, 100, 60), Radius: 10})
	dx, dy, ok := p.At(0, 30)
	if !ok || dx >= 0 || dy != 0 {
		t.Errorf("at the left edge: %d, %d, %v, want a bend to the left", dx, dy, ok)
	}
	dx, dy, ok = p.At(50, 59)
	if !ok || dx != 0 || dy <= 0 {
		t.Errorf("at the bottom edge: %d, %d, %v, want a bend down", dx, dy, ok)
	}
	if _, _, ok := p.At(50, 30); ok {
		t.Error("the middle bends")
	}
}
