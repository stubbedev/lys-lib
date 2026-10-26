package gpu

import (
	"testing"

	"github.com/stubbedev/lys-ui/render"
)

// benchScene is a settings-like frame: a background, cards with
// borders and shadows, and rows of text.
func benchScene(b *testing.B) func(cv *render.Canvas) {
	b.Helper()
	face, err := render.NewFixtureTypeface()
	if err != nil {
		b.Fatal(err)
	}
	return func(cv *render.Canvas) {
		cv.Clear(cv.Rect(), render.RGBA(16, 18, 24, 255))
		for c := range 4 {
			card := render.Rect{X: 20 + 400*c, Y: 20, W: 380, H: 960}
			cv.BoxShadow(card, render.UniformCorners(12), render.BoxShadow{Y: 4, Blur: 16, Color: render.RGBA(0, 0, 0, 120)})
			cv.RoundedRectCorners(card, render.UniformCorners(12), render.RGBA(32, 34, 44, 255))
			cv.RoundedBorderSides(card, render.UniformCorners(12), render.Insets{Top: 1, Right: 1, Bottom: 1, Left: 1}, [4]render.Color{render.RGBA(60, 62, 74, 255), render.RGBA(60, 62, 74, 255), render.RGBA(60, 62, 74, 255), render.RGBA(60, 62, 74, 255)})
			for r := range 30 {
				row := render.Rect{X: card.X + 16, Y: card.Y + 16 + 31*r, W: card.W - 32, H: 26}
				face.DrawAligned(cv, "Setting row with a label", row, 14, render.RGBA(230, 230, 235, 255), render.AlignStart)
				cv.RoundedRect(render.Rect{X: row.X + row.W - 40, Y: row.Y + 4, W: 36, H: 18}, 9, render.RGBA(90, 120, 220, 255))
			}
		}
	}
}

const benchW, benchH = 1620, 1000

func BenchmarkFrameGPU(b *testing.B) {
	d := openDevice(b)
	paint := benchScene(b)
	tg, err := d.NewOffscreen(benchW, benchH)
	if err != nil {
		b.Fatal(err)
	}
	defer tg.Close()
	rec := render.NewRecorder(benchW, benchH, 1, 1)
	for b.Loop() {
		rec.ResetRecorder(benchW, benchH, 1, 1)
		paint(rec)
		if err := d.Render(rec.Recording(), tg); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkFrameCPU(b *testing.B) {
	paint := benchScene(b)
	cv := render.New(make([]byte, render.Stride(benchW)*benchH), render.Stride(benchW), benchW, benchH)
	for b.Loop() {
		cv.ResetTouched()
		paint(cv)
	}
}

// BenchmarkResizeGPU renders the scene at a new size each frame, onto a
// new target, as an interactive resize does.
func BenchmarkResizeGPU(b *testing.B) {
	d := openDevice(b)
	paint := benchScene(b)
	rec := render.NewRecorder(benchW, benchH, 1, 1)
	i := 0
	for b.Loop() {
		w, h := 1200+i%40*20, 900+i%40*5
		i++
		tg, err := d.NewOffscreen(w, h)
		if err != nil {
			b.Fatal(err)
		}
		rec.ResetRecorder(w, h, 1, 1)
		paint(rec)
		if err := d.Render(rec.Recording(), tg); err != nil {
			b.Fatal(err)
		}
		tg.Close()
	}
}

// BenchmarkResizeCPU is BenchmarkResizeGPU on the CPU, a new buffer per
// size.
func BenchmarkResizeCPU(b *testing.B) {
	paint := benchScene(b)
	i := 0
	for b.Loop() {
		w, h := 1200+i%40*20, 900+i%40*5
		i++
		paint(render.New(make([]byte, render.Stride(w)*h), render.Stride(w), w, h))
	}
}
