package gpu

import (
	"image"
	"testing"

	"github.com/stubbedev/lys-lib/render"
)

func TestAFramesAllocations(t *testing.T) {
	if raceEnabled {
		t.Skip("the race detector allocates on its own")
	}
	d := openDevice(t)
	const w, h = 640, 480
	tg := newTarget(t, d, w, h)
	face, err := render.NewFixtureTypeface()
	if err != nil {
		t.Fatal(err)
	}
	rec := render.NewRecorder(w, h, 1, 1)
	t.Cleanup(rec.Recording().End)
	box := render.Rect{X: 10, Y: 10, W: 500, H: 400}
	var fl render.Filters
	fl.Append(render.FilterOp{Kind: render.FilterBlur, Amount: 8})
	var color render.Filters
	color.Append(render.FilterOp{Kind: render.FilterGrayscale, Amount: 1})
	var rounded, curve render.Path
	render.RoundedRectPath(&rounded, box, render.Corners{TopLeft: 16, TopRight: 16, BottomLeft: 16, BottomRight: 16})
	render.EllipsePath(&curve, 200, 200, 60, 40)
	shaped := face.Shape("the quick brown fox jumps over the lazy dog", 14)
	var version uint64
	pixels := image.NewRGBA(image.Rect(0, 0, 32, 32))
	versioned := render.VersionedImage{RGBA: pixels, ID: render.NewImageID()}
	cases := []struct {
		name  string
		limit float64
		paint func(cv *render.Canvas)
	}{
		{"empty", 0, func(*render.Canvas) {}},
		{"rects", 0, func(cv *render.Canvas) {
			for i := range 100 {
				cv.RoundedRect(render.Rect{X: i, Y: i, W: 50, H: 50}, 4, render.RGB(16, 32, 48))
			}
		}},
		{"text", 0, func(cv *render.Canvas) {
			for y := 20; y < 400; y += 20 {
				shaped.Draw(cv, 10, y, render.RGB(0, 0, 0))
			}
		}},
		{"shadow", 0, func(cv *render.Canvas) { cv.Shadow(box, 8, 24, render.RGBA(0, 0, 0, 128)) }},
		{"path", 0, func(cv *render.Canvas) { cv.FillPath(&curve, render.RGB(16, 32, 48)) }},
		{"image upload, the caller boxing one image", 1, func(cv *render.Canvas) {
			version++
			versioned.Version = version
			cv.DrawImageDevice(versioned, 4, 4)
		}},
		{"blur and mask", 0, func(cv *render.Canvas) {
			l := cv.ScratchLayer(box)
			l.Canvas().RoundedRect(box, 4, render.RGB(16, 32, 48))
			render.ApplyFilters(l, box, &fl, render.FilterShape{})
			l.ApplyPathMask(&rounded, box)
			cv.DrawLayer(l, box, render.BlendNormal)
			l.Release()
		}},
		{"colour filter", 0, func(cv *render.Canvas) {
			l := cv.ScratchLayer(box)
			l.Canvas().RoundedRect(box, 4, render.RGB(16, 32, 48))
			render.ApplyFilters(l, box, &color, render.FilterShape{})
			cv.DrawLayer(l, box, render.BlendNormal)
			l.Release()
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			frame := func() {
				rec.ResetRecorder(w, h, 1, 1)
				c.paint(rec)
				if err := d.Render(rec.Recording(), tg); err != nil {
					t.Fatal(err)
				}
				if err := d.Finish(); err != nil {
					t.Fatal(err)
				}
			}
			frame()
			frame()
			if n := testing.AllocsPerRun(50, frame); n > c.limit {
				t.Errorf("%v allocations a frame, want at most %v", n, c.limit)
			}
		})
	}
}
