package gpu

import (
	"image"
	"image/color"
	"testing"

	"github.com/stubbedev/lys-lib/fourcc"
	"github.com/stubbedev/lys-lib/render"
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

func BenchmarkTextureUpdate(b *testing.B) {
	d := openDevice(b)
	const w, h = 1280, 800
	textures := make([]*Texture, 4)
	for i := range textures {
		tx, err := d.NewTexture(fourcc.ARGB8888, w, h)
		if err != nil {
			b.Fatal(err)
		}
		defer tx.Close()
		textures[i] = tx
	}
	pix := make([]byte, w*h*4)
	b.ReportAllocs()
	b.SetBytes(int64(len(pix)))
	i := 0
	for b.Loop() {
		if err := textures[i%len(textures)].Update(image.Rect(0, 0, w, h), w*4, pix); err != nil {
			b.Fatal(err)
		}
		i++
	}
}

// benchCompositor is one output's frame in a compositor: the windows
// sit as textures, some have new pixels, two rectangles of damage are
// redrawn, a client's acquire fence is waited on when the device can,
// the release fence is exported, and the latch budget samples the
// previous frame's GPU time.
func benchCompositor(b *testing.B, windows, updates int) {
	d := openDevice(b)
	const w, h = 256, 256
	tg, err := d.NewOffscreen(w, h)
	if err != nil {
		b.Fatal(err)
	}
	defer tg.Close()
	textures := make([]*Texture, windows)
	for i := range textures {
		tx, err := d.NewTexture(fourcc.ARGB8888, 64, 64)
		if err != nil {
			b.Fatal(err)
		}
		defer tx.Close()
		textures[i] = tx
	}
	pix := make([]byte, 64*64*4)
	damage := []image.Rectangle{image.Rect(0, 0, 64, 64), image.Rect(128, 128, 192, 192)}
	syncFiles := d.SyncFiles()
	b.ReportAllocs()
	i := 0
	for b.Loop() {
		i++
		for j := range updates {
			if err := textures[(i+j)%windows].Update(image.Rect(0, 0, 64, 64), 64*4, pix); err != nil {
				b.Fatal(err)
			}
		}
		p, err := d.Begin(tg, damage)
		if err != nil {
			b.Fatal(err)
		}
		p.Clear(color.RGBA{A: 255})
		for j, tx := range textures {
			dst := image.Rect(j*16, j*16, j*16+64, j*16+64)
			p.DrawTexture(tx, FRect{W: 64, H: 64}, dst, TransformNormal, 1, Mask{})
		}
		if err := p.End(); err != nil {
			b.Fatal(err)
		}
		if syncFiles {
			fd, err := d.SyncFile()
			if err != nil {
				b.Fatal(err)
			}
			if err := d.WaitSyncFile(fd); err != nil {
				b.Fatal(err)
			}
		}
		if d.calibrated {
			if _, _, err := d.FrameDone(); err != nil {
				b.Fatal(err)
			}
		}
	}
	if err := d.Finish(); err != nil {
		b.Fatal(err)
	}
}

func BenchmarkCompositorFrame(b *testing.B) { benchCompositor(b, 6, 1) }

func BenchmarkCompositorFrameFourUploads(b *testing.B) { benchCompositor(b, 6, 4) }
