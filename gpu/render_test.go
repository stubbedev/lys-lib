package gpu

import (
	"image"
	"image/color"
	"testing"

	"github.com/stubbedev/lys-ui/dmabuf"
	"github.com/stubbedev/lys-ui/render"
)

// newTarget is a w x h offscreen target on d.
func newTarget(t *testing.T, d *Device, w, h int) *Target {
	t.Helper()
	tg, err := d.NewOffscreen(w, h)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(tg.Close)
	return tg
}

// A frame renders into a dmabuf target, handed back to its consumer,
// and the next frame draws over what the last left.
func TestRenderIntoDmabuf(t *testing.T) {
	d := exportingDevice(t)
	mods, err := d.Modifiers(dmabuf.FormatARGB8888)
	if err != nil {
		t.Fatal(err)
	}
	tg, err := d.NewTarget(32, 16, dmabuf.FormatARGB8888, mods)
	if err != nil {
		t.Fatal(err)
	}
	defer tg.Close()
	rec := render.NewRecorder(32, 16, 1, 1)
	rec.Clear(rec.Rect(), render.RGBA(10, 20, 30, 255))
	if err := d.Render(rec.Recording(), tg); err != nil {
		t.Fatal(err)
	}
	rec.ResetRecorder(32, 16, 1, 1)
	rec.FillRect(render.Rect{X: 16, W: 16, H: 16}, render.RGBA(200, 0, 0, 255))
	if err := d.Render(rec.Recording(), tg); err != nil {
		t.Fatal(err)
	}
	img, err := tg.ReadPixels()
	if err != nil {
		t.Fatal(err)
	}
	if c := img.RGBAAt(4, 4); c != (color.RGBA{10, 20, 30, 255}) {
		t.Errorf("the first frame's pixel reads %v after the second", c)
	}
	if c := img.RGBAAt(20, 4); c != (color.RGBA{200, 0, 0, 255}) {
		t.Errorf("the second frame's pixel reads %v", c)
	}
}

// parity renders paint's recording on the GPU and on the CPU and
// fails where a channel differs by more than tol.
func parity(t *testing.T, d *Device, w, h, num, denom int, frames int, paint func(cv *render.Canvas, frame int), tol int) {
	t.Helper()
	tg := newTarget(t, d, w, h)
	rec := render.NewRecorder(w, h, num, denom)
	buf := make([]byte, render.Stride(w)*h)
	direct := render.NewScaled(buf, render.Stride(w), w, h, num, denom)
	for frame := range frames {
		rec.ResetRecorder(w, h, num, denom)
		paint(rec, frame)
		if err := d.Render(rec.Recording(), tg); err != nil {
			t.Fatalf("frame %d: %v", frame, err)
		}
		paint(direct, frame)
		got, err := tg.ReadPixels()
		if err != nil {
			t.Fatal(err)
		}
		want := render.NRGBA(buf, render.Stride(w), w, h)
		bad, worst, at := 0, 0, image.Point{}
		for y := range h {
			for x := range w {
				g := premul(got.RGBAAt(x, y))
				c := want.NRGBAAt(x, y)
				wc := premul(nrgbaToRGBA(c))
				diff := maxDiff(g, wc)
				if diff > tol {
					bad++
				}
				if diff > worst {
					worst, at = diff, image.Pt(x, y)
				}
			}
		}
		if bad > 0 {
			t.Errorf("frame %d at %d/%d: %d pixels differ by more than %d, worst %d at %v: gpu %v cpu %v", frame, num, denom, bad, tol, worst, at, got.RGBAAt(at.X, at.Y), nrgbaToRGBA(want.NRGBAAt(at.X, at.Y)))
		}
	}
}

func premul(c color.RGBA) [4]int { return [4]int{int(c.R), int(c.G), int(c.B), int(c.A)} }

func nrgbaToRGBA(c color.NRGBA) color.RGBA {
	r, g, b, a := c.RGBA()
	return color.RGBA{uint8(r >> 8), uint8(g >> 8), uint8(b >> 8), uint8(a >> 8)}
}

func maxDiff(a, b [4]int) int {
	m := 0
	for i := range a {
		m = max(m, a[i]-b[i], b[i]-a[i])
	}
	return m
}

// TestRenderNativeShapes pins the shapes the GPU draws itself against
// the CPU's pixels.
func TestRenderNativeShapes(t *testing.T) {
	d := openDevice(t)
	red, blue := render.RGBA(220, 40, 50, 255), render.RGBA(30, 90, 220, 180)
	for _, sc := range [][2]int{{1, 1}, {3, 2}} {
		w, h := 160*sc[0]/sc[1], 120*sc[0]/sc[1]
		parity(t, d, w, h, sc[0], sc[1], 1, func(cv *render.Canvas, _ int) {
			cv.Clear(cv.Rect(), render.RGBA(16, 18, 24, 255))
			cv.FillRect(render.Rect{X: 2, Y: 2, W: 30, H: 12}, red)
			cv.FillRect(render.Rect{X: 20, Y: 8, W: 30, H: 12}, blue)
			cv.RoundedRect(render.Rect{X: 56, Y: 2, W: 24, H: 14}, 5, blue)
			cv.RoundedRectCorners(render.Rect{X: 84, Y: 2, W: 40, H: 30}, render.Corners{TopLeft: 12, TopRight: 2, BottomRight: 8}, red)
			cv.RoundedBorderSides(render.Rect{X: 2, Y: 40, W: 60, H: 40}, render.UniformCorners(10), render.Insets{Top: 2, Right: 3, Bottom: 4, Left: 1}, [4]render.Color{red, blue, red, blue})
			prev := cv.PushAlpha(0.6)
			b := cv.PushBrightness(1.4)
			cv.RoundedRectCorners(render.Rect{X: 70, Y: 40, W: 40, H: 30}, render.UniformCorners(9), blue)
			cv.PopBrightness(b)
			cv.PopAlpha(prev)
			clip := cv.PushClip(render.Rect{X: 115, Y: 45, W: 20, H: 20})
			cv.RoundedRect(render.Rect{X: 110, Y: 40, W: 40, H: 40}, 12, red)
			cv.PopClip(clip)
		}, 1)
	}
}

// TestRenderFallbacksAndLayers pins the calls drawn as CPU sprites,
// the layer composites and the layer calls run on the CPU.
func TestRenderFallbacksAndLayers(t *testing.T) {
	d := openDevice(t)
	face, err := render.NewFixtureTypeface()
	if err != nil {
		t.Fatal(err)
	}
	red, blue := render.RGBA(220, 40, 50, 255), render.RGBA(30, 90, 220, 180)
	for _, sc := range [][2]int{{1, 1}, {2, 1}} {
		w, h := 160*sc[0]/sc[1], 140*sc[0]/sc[1]
		keptBy := map[bool]*render.Layer{}
		parity(t, d, w, h, sc[0], sc[1], 2, func(cv *render.Canvas, frame int) {
			recorded := cv.Recording() != nil
			kept := keptBy[recorded]
			cv.Clear(cv.Rect(), render.RGBA(16, 18, 24, 255))
			cv.Line(2, 4, 60, 20, 2, red)
			cv.BoxShadow(render.Rect{X: 70, Y: 6, W: 40, H: 20}, render.UniformCorners(6), render.BoxShadow{Y: 3, Blur: 8, Color: render.RGBA(0, 0, 0, 160)})
			cv.PaintGradient(render.Rect{X: 2, Y: 30, W: 60, H: 20}, render.UniformCorners(4), render.Linear(90, render.GradientStop{Color: red}, render.GradientStop{Pos: 1, Color: blue}))
			face.DrawAligned(cv, "GPU text", render.Rect{X: 70, Y: 30, W: 80, H: 20}, 14, render.RGBA(240, 240, 240, 255), render.AlignStart)
			region := cv.MapRect(render.Rect{X: 2, Y: 56, W: 80, H: 40})
			if frame == 0 || kept == nil {
				kept = cv.Layer(kept, region)
				keptBy[recorded] = kept
				kc := kept.Canvas()
				kc.RoundedRectCorners(render.Rect{X: 6, Y: 60, W: 50, H: 30}, render.UniformCorners(8), red)
				face.DrawAligned(kc, "kept", render.Rect{X: 10, Y: 64, W: 40, H: 16}, 11, render.RGBA(255, 255, 255, 255), render.AlignStart)
			}
			cv.Composite(kept, region, render.Rotate(10).About(float64(region.X+region.W/2), float64(region.Y+region.H/2)), 0.8)
			cv.DrawLayer(kept, region, render.BlendNormal)
			area := cv.MapRect(render.Rect{X: 90, Y: 56, W: 60, H: 40})
			snap := cv.ScratchSnapshot(area)
			a := cv.ScratchLayer(area)
			a.Canvas().DrawCanvasDevice(snap.Canvas(), 0, 0)
			var fl render.Filters
			fl.Append(render.FilterOp{Kind: render.FilterBlur, Amount: 3})
			render.ApplyFilters(a, area, &fl, render.FilterShape{})
			cv.DrawLayer(a, area, render.BlendMultiply)
			a.Release()
			snap.Release()
		}, 1)
	}
}

// A glyph atlas that fills mid-frame draws what it holds, empties and
// refills: text needing more than a page still matches the CPU.
func TestRenderGlyphAtlasRefills(t *testing.T) {
	d := openDevice(t)
	d.masks.size = 48
	face, err := render.NewFixtureTypeface()
	if err != nil {
		t.Fatal(err)
	}
	parity(t, d, 200, 120, 1, 1, 2, func(cv *render.Canvas, _ int) {
		cv.Clear(cv.Rect(), render.RGBA(16, 18, 24, 255))
		for i, line := range []string{"The quick brown fox", "jumps over the lazy", "dog 0123456789 QWXZ"} {
			face.DrawAligned(cv, line, render.Rect{X: 4, Y: 4 + 34*i, W: 190, H: 30}, float64(18+4*i), render.RGBA(240, 240, 240, 255), render.AlignStart)
		}
	}, 1)
	if d.masks.py == 0 && d.masks.px == 0 {
		t.Error("the atlas holds nothing after drawing text")
	}
}

// Images draw from uploaded textures: scaled, one to one and into a
// rect, clipped and faded, as the CPU draws them.
func TestRenderImages(t *testing.T) {
	d := openDevice(t)
	img := image.NewRGBA(image.Rect(0, 0, 7, 5))
	for y := range 5 {
		for x := range 7 {
			a := uint8(255 - 30*x)
			img.SetRGBA(x, y, color.RGBA{R: uint8(int(a) * x / 7), G: uint8(int(a) * y / 5), B: a / 2, A: a})
		}
	}
	sub := img.SubImage(image.Rect(2, 1, 6, 4)).(*image.RGBA)
	ic, err := render.IconFromImage(img, 7, 5)
	if err != nil {
		t.Fatal(err)
	}
	for _, sc := range [][2]int{{1, 1}, {3, 2}} {
		w, h := 120*sc[0]/sc[1], 60*sc[0]/sc[1]
		parity(t, d, w, h, sc[0], sc[1], 2, func(cv *render.Canvas, _ int) {
			cv.Clear(cv.Rect(), render.RGBA(16, 18, 24, 255))
			cv.DrawImage(img, 2, 2)
			cv.DrawImage(sub, 14, 2)
			cv.DrawImageDevice(img, 30, 4)
			cv.DrawImageRect(img, render.Rect{X: 44, Y: 2, W: 30, H: 20})
			ic.Draw(cv, 80, 2)
			prev := cv.PushAlpha(0.5)
			clip := cv.PushClip(render.Rect{X: 0, Y: 30, W: 60, H: 14})
			cv.DrawImageRect(img, render.Rect{X: 4, Y: 28, W: 50, H: 25})
			cv.PopClip(clip)
			cv.PopAlpha(prev)
		}, 1)
	}
}
