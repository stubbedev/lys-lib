// The allocation budgets (#77): hard ceilings derived from the measured
// baseline in bench/baseline.md, gated here so a regression fails `just
// check`. Each budget carries 1.2x headroom over the baseline number,
// not invented room: a fix that lowers the baseline should tighten it
// here in the same commit.
package render

import "testing"

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
		cv.LinearGradient(Rect{X: 8, Y: 104, W: 120, H: 24}, bg, fg, true)
	})
	if allocs > budget {
		t.Errorf("canvas paint allocates %v allocs/op, budget %d", allocs, budget)
	}
}
