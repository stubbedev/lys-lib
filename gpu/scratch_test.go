package gpu

import (
	"image"
	"testing"

	"github.com/stubbedev/lys-lib/render"
)

// A large upload grows the staging buffer for its frame, written
// straight into it, and a run of frames that need less shrinks it back:
// one wallpaper's upload does not keep its buffer for good (#17).
func TestStagingShrinksAfterALargeUpload(t *testing.T) {
	d := openDevice(t)
	tg := newTarget(t, d, 16, 16)
	rec := render.NewRecorder(16, 16, 1, 1)
	big := image.NewRGBA(image.Rect(0, 0, 600, 600))
	for i := 0; i < len(big.Pix); i += 4 {
		big.Pix[i], big.Pix[i+3] = 255, 255
	}
	drawImage(rec, render.NewStillImage(big))
	if px, _ := renderImage(t, d, tg, rec); px != red {
		t.Fatalf("the large upload drew %v, want %v", px, red)
	}
	if n := heldStaging(d); n < len(big.Pix) {
		t.Fatalf("staging holds %d bytes after a %d-byte upload", n, len(big.Pix))
	}
	for range stagingShrinkAfter {
		drawImage(rec, render.NewStillImage(rasterIn(make([]byte, 8*8*4), blue)))
		if px, _ := renderImage(t, d, tg, rec); px != blue {
			t.Fatalf("the small upload drew %v, want %v", px, blue)
		}
	}
	if n := heldStaging(d); n > 4*stagingMin {
		t.Errorf("staging kept %d bytes after a frame that staged 256", n)
	}
}

// heldStaging is the largest staging buffer finished segments keep.
func heldStaging(d *Device) int {
	held := 0
	for _, fb := range d.spareFrameBuffers {
		if fb.staging != nil {
			held = max(held, len(fb.staging.mapped))
		}
	}
	return held
}

// Trim frees the scratch canvases off the Go heap; the next frame that
// needs one allocates it again (#17).
func TestTrimFreesTheScratchCanvases(t *testing.T) {
	d := openDevice(t)
	newTarget(t, d, 64, 64)
	if err := d.ensureScratch(64, 64); err != nil {
		t.Fatal(err)
	}
	if _, err := d.scratchCanvas(0, 64, 64, 1, 1); err != nil {
		t.Fatal(err)
	}
	if st := d.pixels.Stats(); st.Live != 1 || st.Bytes < 64*64*4 {
		t.Fatalf("scratch pixels %+v, want one 64x64 canvas", st)
	}
	if err := d.Trim(); err != nil {
		t.Fatal(err)
	}
	if st := d.pixels.Stats(); st.Live != 0 || d.scratches != nil {
		t.Errorf("after Trim %d scratch canvases live, %d kept", st.Live, len(d.scratches))
	}
	if _, err := d.scratchCanvas(0, 64, 64, 1, 1); err != nil {
		t.Errorf("a scratch canvas after Trim: %v", err)
	}
}

func TestAResizeMakesALogarithmicNumberOfSurfaces(t *testing.T) {
	d := openDevice(t)
	rec := render.NewRecorder(1, 1, 1, 1)
	var fl render.Filters
	fl.Append(render.FilterOp{Kind: render.FilterBlur, Amount: 2})
	step := func(w, h int) {
		t.Helper()
		tg, err := d.NewOffscreen(w, h)
		if err != nil {
			t.Fatal(err)
		}
		defer tg.Close()
		box := render.Rect{W: w, H: h}
		rec.ResetRecorder(w, h, 1, 1)
		l := rec.ScratchLayer(box)
		l.Canvas().RoundedRect(box, 4, render.RGBA(16, 32, 48, 255))
		render.ApplyFilters(l, box, &fl, render.FilterShape{})
		rec.DrawLayer(l, box, render.BlendNormal)
		l.Release()
		if err := d.Render(rec.Recording(), tg); err != nil {
			t.Fatalf("%dx%d: %v", w, h, err)
		}
		if err := d.Finish(); err != nil {
			t.Fatal(err)
		}
	}
	step(100, 100)
	before := d.surfacesMade
	const steps = 100
	for i := 1; i <= steps; i++ {
		step(100+i*2, 100+i)
	}
	working := d.surfacesMade - before - steps
	const page, filters, layer = 1, 2, 1
	const widthGrowths, heightGrowths = 3, 2
	if want := uint64((page + filters + layer) * (widthGrowths + heightGrowths)); working > want {
		t.Errorf("a %d-step resize made %d working surfaces, want at most %d", steps, working, want)
	}
	t.Logf("%d working surfaces over %d steps", working, steps)
}

func TestThePoolOfScratchLayersIsBoundedAndTrimFreesIt(t *testing.T) {
	d := openDevice(t)
	rec := render.NewRecorder(1, 1, 1, 1)
	for i := range 300 {
		w, h := 16+i, 16
		tg, err := d.NewOffscreen(w, h)
		if err != nil {
			t.Fatal(err)
		}
		rec.ResetRecorder(w, h, 1, 1)
		var open []*render.Layer
		for range render.MaxPooledLayers + 2 {
			open = append(open, rec.ScratchLayer(render.Rect{W: w, H: h}))
		}
		for _, l := range open {
			l.Release()
		}
		if err := d.Render(rec.Recording(), tg); err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
		tg.Close()
		if n := len(d.freeLayers); n > render.MaxPooledLayers {
			t.Fatalf("step %d: %d pooled layer surfaces, want at most %d", i, n, render.MaxPooledLayers)
		}
	}
	if err := d.Trim(); err != nil {
		t.Fatal(err)
	}
	if n, live := len(d.freeLayers), len(d.layers); n != 0 || live != 0 {
		t.Errorf("after Trim %d pooled and %d live layer surfaces", n, live)
	}
}
