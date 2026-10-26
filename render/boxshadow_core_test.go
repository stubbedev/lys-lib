package render

import (
	"bytes"
	"testing"
)

// A long box's shadow repeats its core's straight middle: painting it
// from the core raster matches building the whole box's raster pixel
// for pixel, and the raster no longer depends on the box's length, so
// a resize rebuilds nothing.
func TestBoxShadowStretchesItsCore(t *testing.T) {
	for _, tc := range []struct {
		name string
		s    BoxShadow
	}{
		{"outer", BoxShadow{X: 0, Y: 4, Blur: 12, Spread: 2, Color: RGBA(0, 0, 0, 120)}},
		{"offset", BoxShadow{X: -6, Y: 9, Blur: 6, Color: RGBA(20, 0, 60, 200)}},
		{"inset", BoxShadow{X: 2, Y: 3, Blur: 8, Spread: 1, Color: RGBA(0, 0, 0, 160), Inset: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const w, h = 420, 300
			box := Rect{X: 60, Y: 50, W: 300, H: 180}
			radii := Corners{TopLeft: 12, TopRight: 6, BottomRight: 18, BottomLeft: 0}
			got := New(make([]byte, Stride(w)*h), Stride(w), w, h)
			got.BoxShadow(box, radii, tc.s)

			want := New(make([]byte, Stride(w)*h), Stride(w), w, h)
			ras := buildBoxShadow(box.W, box.H, radii, tc.s, 1, 1)
			for y := range ras.h {
				for x := range ras.w {
					px, py := box.X+ras.x0+x, box.Y+ras.y0+y
					if q := uint32(ras.cov[y*ras.w+x]); q != 0 && want.clip.Contains(px, py) {
						want.blend(px, py, coverageScale(tc.s.Color, q))
					}
				}
			}
			if !bytes.Equal(got.data, want.data) {
				t.Error("the stretched shadow differs from the whole box's")
			}
			if kw, kh := shadowCore(box, radii, tc.s, 1, 1); kw >= box.W || kh >= box.H {
				t.Errorf("core %dx%d, want smaller than the %dx%d box", kw, kh, box.W, box.H)
			}
		})
	}
}
