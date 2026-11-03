// The allocation budgets (#77): hard ceilings derived from the measured
// baseline in bench/baseline.md, gated here so a regression fails `just
// check`. Each budget carries 1.2x headroom over the baseline number,
// not invented room: a fix that lowers the baseline should tighten it
// here in the same commit.
package render

import (
	"bytes"
	"image"
	"image/color"
	"testing"
)

// TestAllocBudgetShape pins the warm text path: one Shape of a stable
// line and one DrawAligned over a fixed box must both be map hits, so
// the baseline measured 0 allocs/op and the budget stays at 0.
func TestAllocBudgetShape(t *testing.T) {
	tf := testTypeface(t)
	const budget = 0
	tf.Shape(benchText200, 14) // warm the shaping cache
	allocs := testing.AllocsPerRun(100, func() {
		if tf.Shape(benchText200, 14) == nil {
			panic("nil shape")
		}
	})
	if allocs > budget {
		t.Errorf("Shape allocates %v allocs/op, budget %d", allocs, budget)
	}
}

// TestAllocBudgetDrawAligned pins the aligned-draw path the Label and
// Entry paint through: warm shape, warm atlas, zero allocations.
func TestAllocBudgetDrawAligned(t *testing.T) {
	tf := testTypeface(t)
	const budget = 0
	cv, _ := newTestCanvas(240, 40)
	box := Rect{W: 200, H: 32}
	line := benchText200[:43]
	tf.DrawAligned(cv, line, box, 14, RGB(255, 255, 255), AlignStart) // warm
	allocs := testing.AllocsPerRun(100, func() {
		if tf.DrawAligned(cv, line, box, 14, RGB(255, 255, 255), AlignStart) == nil {
			panic("nil shape")
		}
	})
	if allocs > budget {
		t.Errorf("DrawAligned allocates %v allocs/op, budget %d", allocs, budget)
	}
}

// TestAllocBudgetShapeRunes pins the rune-holding widgets' warm path:
// one ShapeRunes of a stable rune slice must be a table hit, like the
// string-keyed Shape beside it.
func TestAllocBudgetShapeRunes(t *testing.T) {
	tf := testTypeface(t)
	const budget = 0
	rs := []rune(benchText200)
	line := rs[:43]
	tf.ShapeRunes(line, 14) // warm the rune-keyed table
	allocs := testing.AllocsPerRun(100, func() {
		if tf.ShapeRunes(line, 14) == nil {
			panic("nil shape")
		}
	})
	if allocs > budget {
		t.Errorf("ShapeRunes allocates %v allocs/op, budget %d", allocs, budget)
	}
}

// TestAllocBudgetCanvasPaint pins the canvas primitive mix: clear,
// fill, border, rounded fill, gradient. Pure pixel work; the baseline
// measured 0 allocs/op.
func TestAllocBudgetCanvasPaint(t *testing.T) {
	const budget = 0
	cv, _ := newTestCanvas(320, 200)
	full := cv.Rect()
	bg, fg := RGB(24, 24, 28), RGB(200, 200, 200)
	allocs := testing.AllocsPerRun(50, func() {
		cv.Clear(full, bg)
		cv.FillRect(Rect{X: 8, Y: 8, W: 120, H: 24}, fg)
		cv.BorderRect(Rect{X: 8, Y: 40, W: 120, H: 24}, 2, fg)
		cv.RoundedRect(Rect{X: 8, Y: 72, W: 120, H: 24}, 8, fg)
		cv.PaintGradient(Rect{X: 8, Y: 104, W: 120, H: 24}, Corners{}, Linear(90, GradientStop{Pos: 0, Color: bg}, GradientStop{Pos: 1, Color: fg}))
	})
	if allocs > budget {
		t.Errorf("canvas paint allocates %v allocs/op, budget %d", allocs, budget)
	}
}

func TestAllocBudgetIconDraw(t *testing.T) {
	icon, err := LoadSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 8 8"><rect width="8" height="8"/></svg>`), 8, 8)
	if err != nil {
		t.Fatal(err)
	}
	cv, _ := newTestCanvas(16, 16)
	for name, ic := range map[string]*Icon{"logical": icon, "native": icon.AtScale(1, 1)} {
		if allocs := testing.AllocsPerRun(100, func() { ic.Draw(cv, 2, 2) }); allocs > 0 {
			t.Errorf("drawing a %s icon allocates %v allocs/op", name, allocs)
		}
	}
}

func TestAllocBudgetDrawImageRect(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 16, 16))
	cv, _ := newTestCanvas(8, 8)
	var img image.Image = NewStillImage(src)
	if allocs := testing.AllocsPerRun(100, func() { cv.DrawImageRect(img, Rect{W: 8, H: 8}) }); allocs > 0 {
		t.Errorf("DrawImageRect of an RGBA raster allocates %v allocs/op", allocs)
	}
}

func TestDrawImageRectSamplesTheSamePixelsThroughTheFastPath(t *testing.T) {
	src := image.NewNRGBA(image.Rect(2, 3, 18, 19))
	premul := image.NewRGBA(src.Rect)
	for y := src.Rect.Min.Y; y < src.Rect.Max.Y; y++ {
		for x := src.Rect.Min.X; x < src.Rect.Max.X; x++ {
			c := color.RGBA{R: uint8(x * 12), G: uint8(y * 11), B: uint8(x + y), A: 255}
			src.Set(x, y, c)
			premul.Set(x, y, c)
		}
	}
	for _, r := range []Rect{{W: 8, H: 8}, {X: 1, Y: 2, W: 32, H: 21}} {
		slow, slowData := newTestCanvas(40, 40)
		fast, fastData := newTestCanvas(40, 40)
		slow.DrawImageRect(src, r)
		fast.DrawImageRect(premul, r)
		if !bytes.Equal(slowData, fastData) {
			t.Errorf("%+v: the RGBA path differs from the generic one", r)
		}
	}
}
