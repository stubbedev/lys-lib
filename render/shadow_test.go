// Direct pixel assertions for the Shadow primitive and its raster
// cache: falloff shape, clip respect, PushAlpha composition, kernel
// bounds, and the cache-hit counter that pins "a repaint of the same
// shadow re-blends but never re-Gaussians". The source-over
// conformance itself lives in the Shadow row of blend_test.go; golden
// snapshots for the shadow on/off/blur variants belong in the #45
// golden harness once it lands.
package render

import (
	"bytes"
	"testing"
)

// paintShadow runs one Shadow draw over bg and returns the buffer.
func paintShadow(rect Rect, radius, blur int, col, bg Color) (*Canvas, []byte) {
	cv, data := newTestCanvas(80, 60)
	cv.Clear(cv.Rect(), bg)
	cv.Shadow(rect, radius, blur, col)
	return cv, data
}

func TestShadowFalloff(t *testing.T) {
	rect := Rect{X: 10, Y: 10, W: 30, H: 20}
	_, data := paintShadow(rect, 5, 8, RGB(0, 0, 0), RGB(250, 250, 250))

	t.Run("interior is full strength", func(t *testing.T) {
		// A black shadow over near-white bg: full coverage darkens to
		// the shadow color; alpha must have saturated.
		if got := pxAt(data, Stride(80), 25, 20); got.A() != 255 {
			t.Errorf("interior alpha = %d, want 255", got.A())
		}
	})

	t.Run("falloff is monotone outward", func(t *testing.T) {
		prev := 256
		for x := rect.X + rect.W; x < rect.X+rect.W+10; x++ { // through the ring past the right edge
			a := int(pxAt(data, Stride(80), x, 20).A())
			if a > prev {
				t.Fatalf("coverage rose from %d to %d at x=%d — the falloff must be monotone", prev, a, x)
			}
			prev = a
		}
		if prev == 256 {
			t.Fatal("no ring pixels sampled past the edge")
		}
	})

	t.Run("the tail is gone past the kernel extent", func(t *testing.T) {
		// blur 8: kernel extent 8 + AA rim 1; d=15 is clear.
		if got := pxAt(data, Stride(80), rect.X+rect.W+15, 20); got != RGB(250, 250, 250) {
			t.Errorf("pixel past the tail = %#08x, want untouched bg", uint32(got))
		}
	})

	t.Run("corners fall off with the radius", func(t *testing.T) {
		// Diagonally out of a rounded corner is farther than straight
		// out of an edge at the same offset, so its coverage must not
		// exceed the edge's.
		edge := pxAt(data, Stride(80), rect.X+rect.W+2, 20).A()
		corner := pxAt(data, Stride(80), rect.X+rect.W+2, rect.Y+rect.H+2).A()
		if corner > edge {
			t.Errorf("corner coverage %d exceeds edge coverage %d at the same offset", corner, edge)
		}
	})
}

func TestShadowRespectsClip(t *testing.T) {
	cv, data := newTestCanvas(80, 60)
	cv.Clear(cv.Rect(), RGB(250, 250, 250))
	prev := cv.PushClip(Rect{X: 0, Y: 0, W: 30, H: 60})
	cv.Shadow(Rect{X: 10, Y: 10, W: 30, H: 20}, 5, 8, RGB(0, 0, 0))
	cv.PopClip(prev)

	if got := pxAt(data, Stride(80), 20, 20); got.A() == 0 {
		t.Error("clipped-in region painted nothing")
	}
	if got := pxAt(data, Stride(80), 50, 20); got != RGB(250, 250, 250) {
		t.Errorf("pixel beyond the clip = %#08x, want untouched bg — Shadow must clip like every primitive", uint32(got))
	}
}

func TestShadowDisabledShorthand(t *testing.T) {
	cv, data := newTestCanvas(40, 30)
	cv.Clear(cv.Rect(), RGB(250, 250, 250))
	before := cv.Touched()
	cv.Shadow(Rect{X: 10, Y: 10, W: 10, H: 8}, 3, 0, RGB(0, 0, 0))       // blur 0: disabled
	cv.Shadow(Rect{X: 10, Y: 10, W: 10, H: 8}, 3, 8, Color(0))           // transparent color
	cv.Shadow(Rect{X: 10, Y: 10, W: 10, H: 8}, 3, 8, RGBA(0, 0, 0, 128)) // over a clipped-out... no: still paints
	if cv.Touched() == before {
		t.Error("the valid shadow painted nothing")
	}
	for _, p := range [][2]int{{2, 2}, {37, 27}} {
		if got := pxAt(data, Stride(40), p[0], p[1]); got != RGB(250, 250, 250) {
			t.Errorf("pixel (%d,%d) = %#08x, want untouched", p[0], p[1], uint32(got))
		}
	}
}

func TestShadowUnderPushAlpha(t *testing.T) {
	rect := Rect{X: 10, Y: 10, W: 30, H: 20}
	col, bg := RGB(0, 0, 0), RGB(250, 250, 250)
	_, full := paintShadow(rect, 5, 8, col, bg)

	cv, data := newTestCanvas(80, 60)
	cv.Clear(cv.Rect(), bg)
	prev := cv.PushAlpha(0.5)
	cv.Shadow(rect, 5, 8, col)
	cv.PopAlpha(prev)

	for _, p := range [][2]int{{25, 20}, {42, 20}, {44, 21}} {
		gotPx := pxAt(data, Stride(80), p[0], p[1])
		// The PushAlpha factor (128) multiplies the premultiplied
		// source channels — at the coverage the raster publishes for
		// the pixel — before the composite, exactly like a faded
		// RoundedRect edge; the destination is untouched.
		tail, half := shadowTail(8)
		cov := tailCoverage(tail, half, sdRoundRect(float64(p[0])+0.5, float64(p[1])+0.5, rect, 5))
		want := refOverChannels(blendChannels(modulate(coverageScale(col, uint32(cov)), 128)), bg)
		if !blendNear(gotPx, want, 1) {
			t.Errorf("faded shadow at (%d,%d) = %#08x, want the alpha-scaled composite %#08x (+/-1)",
				p[0], p[1], uint32(gotPx), uint32(want))
		}
		wantFull := pxAt(full, Stride(80), p[0], p[1])
		if gotPx == wantFull {
			t.Errorf("faded shadow at (%d,%d) identical to the unfaded one — PushAlpha did not reach the shadow", p[0], p[1])
		}
	}
}

func TestShadowCacheReuse(t *testing.T) {
	shadowMu.Lock()
	shadowHits, shadowMisses = 0, 0
	shadowMu.Unlock()
	t.Cleanup(func() {
		shadowMu.Lock()
		shadowHits, shadowMisses = 0, 0
		shadowMu.Unlock()
	})

	rect := Rect{X: 10, Y: 10, W: 20, H: 10}
	cv1, d1 := newTestCanvas(60, 40)
	cv1.Clear(cv1.Rect(), RGB(250, 250, 250))
	cv1.Shadow(rect, 4, 6, RGB(0, 0, 0))

	shadowMu.Lock()
	misses, hits := shadowMisses, shadowHits
	shadowMu.Unlock()
	if misses != 1 || hits != 0 {
		t.Fatalf("first paint: misses=%d hits=%d, want 1 miss, 0 hits", misses, hits)
	}

	// The same geometry on a fresh canvas: a repaint of the identical
	// shadow must hit the cache and still blend every pixel — the
	// cache skips the Gaussian, never the paint.
	cv2, d2 := newTestCanvas(60, 40)
	cv2.Clear(cv2.Rect(), RGB(250, 250, 250))
	cv2.Shadow(rect, 4, 6, RGB(0, 0, 0))

	shadowMu.Lock()
	misses, hits = shadowMisses, shadowHits
	shadowMu.Unlock()
	if misses != 1 || hits != 1 {
		t.Errorf("second paint: misses=%d hits=%d, want the cached raster (1 miss, 1 hit)", misses, hits)
	}
	if cv2.Touched() == 0 {
		t.Error("cache hit painted nothing; the shadow must still be blended per frame")
	}
	if !bytes.Equal(d1, d2) {
		t.Error("cached and freshly built rasters produced different pixels")
	}

	// A different color reuses the same coverage raster.
	cv3, _ := newTestCanvas(60, 40)
	cv3.Clear(cv3.Rect(), RGB(250, 250, 250))
	cv3.Shadow(rect, 4, 6, RGB(255, 0, 0))
	shadowMu.Lock()
	misses, hits = shadowMisses, shadowHits
	shadowMu.Unlock()
	if misses != 1 || hits != 2 {
		t.Errorf("recolored paint rebuilt the raster: misses=%d hits=%d, want 1 miss, 2 hits", misses, hits)
	}
}

func TestShadowKernelBounded(t *testing.T) {
	// An absurd blur must clamp to the cap, not sweep a giant kernel:
	// the painted box stays within the rect grown by maxShadowBlur+1.
	rect := Rect{X: 20, Y: 20, W: 10, H: 8}
	cv, data := newTestCanvas(200, 100)
	cv.Clear(cv.Rect(), RGB(250, 250, 250))
	cv.Shadow(rect, 3, 1_000_000, RGB(0, 0, 0))

	if got := pxAt(data, Stride(200), 25, 24); got.A() != 255 {
		t.Errorf("interior alpha = %d, want 255", got.A())
	}
	far := maxShadowBlur + 2
	if got := pxAt(data, Stride(200), rect.X-far, 24); got != RGB(250, 250, 250) {
		t.Errorf("pixel %d px out = %#08x, want untouched — the kernel did not clamp", far, uint32(got))
	}
	if got := pxAt(data, Stride(200), rect.X+rect.W+maxShadowBlur-1, 24); got.A() == 0 {
		t.Error("no falloff at the clamp edge; the kernel was cut shorter than the cap")
	}
}
