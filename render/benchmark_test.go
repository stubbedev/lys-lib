// Text pipeline benchmarks: shaping and glyph rasterization. The
// repeated-input case is the one the shaping cache serves - every
// frame's second Shape of the same (font, size, string) should be a
// map hit - and the draw case is the one the glyph atlas serves. CI
// archives these alongside the widget tree benchmarks (go test -bench .
// -run '^$' -benchmem); none of it gates.
package render

import (
	"strings"
	"testing"

	"github.com/stubbedev/gelm/internal/text"
)

// benchText200 is a 200-rune line: an entry's worst realistic paste.
var benchText200 = strings.Repeat("the quick brown fox jumps over the lazy dog. ", 5)[:200]

// BenchmarkShape200 shapes the same 200-character string at the same
// size every op - the steady state of a text widget repainting across
// frames, and the path the process-wide shaping cache must turn into a
// map hit.
func BenchmarkShape200(b *testing.B) {
	tf := testTypeface(b)
	b.ReportAllocs()
	for b.Loop() {
		if s := tf.Shape(benchText200, 14); s == nil {
			b.Fatal("nil shape")
		}
	}
}

// BenchmarkDrawAligned draws a line of text into a 200x40 box every
// op - the Label/Entry alignment path. Steady state serves the shape
// from the cache and the glyph from the atlas, so the number the
// budget gates (TestAllocBudget) is the per-draw allocation count.
func BenchmarkDrawAligned(b *testing.B) {
	tf := testTypeface(b)
	cv, _ := newTestCanvas(240, 40)
	box := Rect{W: 200, H: 32}
	col := RGB(255, 255, 255)
	b.ReportAllocs()
	for b.Loop() {
		if s := tf.DrawAligned(cv, benchText200[:43], box, 14, col, AlignStart); s == nil {
			b.Fatal("nil shape")
		}
	}
}

// BenchmarkCanvasPaint repaints the primitive mix one widget frame
// uses - clear, filled rects, a border, a rounded fill, and a gradient
// strip - over a 320x200 canvas. The steady-state number is the pixel
// work; allocations must stay at zero (the budget gates it).
func BenchmarkCanvasPaint(b *testing.B) {
	cv, _ := newTestCanvas(320, 200)
	full := cv.Rect()
	bg := RGB(24, 24, 28)
	fg := RGB(200, 200, 200)
	b.ReportAllocs()
	for b.Loop() {
		cv.Clear(full, bg)
		cv.FillRect(Rect{X: 8, Y: 8, W: 120, H: 24}, fg)
		cv.BorderRect(Rect{X: 8, Y: 40, W: 120, H: 24}, 2, fg)
		cv.RoundedRect(Rect{X: 8, Y: 72, W: 120, H: 24}, 8, fg)
		cv.PaintGradient(Rect{X: 8, Y: 104, W: 120, H: 24}, Corners{}, Linear(90, GradientStop{Pos: 0, Color: bg}, GradientStop{Pos: 1, Color: fg}))
	}
}

// Cold-path benchmark lands with the shaping cache implementation.

// BenchmarkShape200Cold shapes the same string with the cache bypassed
// via shapeUncached: the number a first shape pays, and the before
// number of BenchmarkShape200.
func BenchmarkShape200Cold(b *testing.B) {
	tf := testTypeface(b)
	b.ReportAllocs()
	for b.Loop() {
		if s := tf.shapeUncached(benchText200, 14, text.DirectionAuto); s == nil {
			b.Fatal("nil shape")
		}
	}
}

// BenchmarkChainShape200 shapes a 200-character string through a
// fallback chain, the mixed-script path dropdowns and toasts take.
func BenchmarkChainShape200(b *testing.B) {
	tf := testTypeface(b)
	c := NewChain(tf)
	b.ReportAllocs()
	for b.Loop() {
		if s := c.Shape(benchText200, 14); s == nil {
			b.Fatal("nil shape")
		}
	}
}

// BenchmarkDrawText draws one shaped line of text repeatedly: warm
// shaping plus the glyph atlas steady state - the per-frame cost of
// repainting a label or entry.
func BenchmarkDrawText(b *testing.B) {
	tf := testTypeface(b)
	cv, _ := newTestCanvas(400, 32)
	s := tf.Shape("the quick brown fox jumps over the lazy dog", 14)
	b.ReportAllocs()
	for b.Loop() {
		tf.Draw(cv, s, 0, 24, RGB(255, 255, 255))
	}
}

// BenchmarkDrawTextCold draws one shaped line with the glyph atlas
// drained every op: the rasterization cost a cache-miss draw pays, the
// before number of BenchmarkDrawText.
func BenchmarkDrawTextCold(b *testing.B) {
	tf := testTypeface(b)
	cv, _ := newTestCanvas(400, 32)
	s := tf.Shape("the quick brown fox jumps over the lazy dog", 14)
	b.ReportAllocs()
	for b.Loop() {
		glyphs.dropAll()
		tf.Draw(cv, s, 0, 24, RGB(255, 255, 255))
	}
}
