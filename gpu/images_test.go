package gpu

import (
	"image"
	"image/color"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stubbedev/lys-lib/render"
)

// rasterIn is an 8x8 raster over buf, memory its owner manages and
// hands out again: the same address, rect and stride every time.
func rasterIn(buf []byte, c color.RGBA) *image.RGBA {
	for i := 0; i < len(buf); i += 4 {
		buf[i], buf[i+1], buf[i+2], buf[i+3] = c.R, c.G, c.B, c.A
	}
	return &image.RGBA{Pix: buf, Stride: 8 * 4, Rect: image.Rect(0, 0, 8, 8)}
}

// renderImage renders the frame rec recorded onto tg and is the pixel
// at (6, 6), inside the image drawImage puts at (4, 4), with the
// textures and masks the frame uploaded.
func renderImage(t *testing.T, d *Device, tg *Target, rec *render.Canvas) (color.RGBA, int) {
	t.Helper()
	if err := d.Render(rec.Recording(), tg); err != nil {
		t.Fatal(err)
	}
	got, err := tg.ReadPixels()
	if err != nil {
		t.Fatal(err)
	}
	return got.RGBAAt(6, 6), d.frame.stats.textureUploads
}

// drawImage records a frame that clears to black and draws img at
// (4, 4).
func drawImage(rec *render.Canvas, img image.Image) {
	rec.ResetRecorder(16, 16, 1, 1)
	rec.Clear(rec.Rect(), render.RGBA(0, 0, 0, 255))
	rec.DrawImageDevice(img, 4, 4)
}

var (
	red  = color.RGBA{R: 255, A: 255}
	blue = color.RGBA{B: 255, A: 255}
)

// A raster made in memory an earlier raster was freed from, at the same
// address, rect and stride, draws its own pixels, not the texture the
// earlier one left, and uploads once.
func TestRenderImageInReusedMemoryDrawsItsPixels(t *testing.T) {
	d := openDevice(t)
	tg := newTarget(t, d, 16, 16)
	rec := render.NewRecorder(16, 16, 1, 1)
	buf := make([]byte, 8*8*4)
	for _, tc := range []struct {
		name string
		img  func(*image.RGBA) image.Image
	}{
		{"still", func(r *image.RGBA) image.Image { return render.NewStillImage(r) }},
		{"versioned", func(r *image.RGBA) image.Image { return render.VersionedImage{RGBA: r, ID: render.NewImageID()} }},
	} {
		drawImage(rec, tc.img(rasterIn(buf, red)))
		if px, _ := renderImage(t, d, tg, rec); px != red {
			t.Fatalf("%s: first raster drew %v, want %v", tc.name, px, red)
		}
		drawImage(rec, tc.img(rasterIn(buf, blue)))
		if px, n := renderImage(t, d, tg, rec); px != blue || n != 1 {
			t.Errorf("%s: raster in reused memory drew %v with %d uploads, want %v with 1", tc.name, px, n, blue)
		}
	}
}

// A still image and a shadow mask drawn frame after frame upload on
// their first frame only, a sub-image taken anew each frame included.
func TestRenderUnchangedImagesUploadOnce(t *testing.T) {
	d := openDevice(t)
	tg := newTarget(t, d, 16, 16)
	rec := render.NewRecorder(16, 16, 1, 1)
	still := render.NewStillImage(rasterIn(make([]byte, 8*8*4), red))
	for frame := range 4 {
		drawImage(rec, still.SubImage(image.Rect(1, 1, 7, 7)))
		rec.Shadow(render.Rect{X: 2, Y: 2, W: 6, H: 6}, 2, 3, render.RGBA(0, 0, 0, 128))
		want := 0
		if frame == 0 {
			want = 2
		}
		if _, n := renderImage(t, d, tg, rec); n != want {
			t.Errorf("frame %d uploaded %d textures and masks, want %d", frame, n, want)
		}
	}
}

// An owner evicting a raster a recorded List still draws forgets it and
// retires its memory: the List draws the raster's own pixels from the
// texture, the memory is freed only once the List ends, the texture
// goes with the frame, and a raster made in the freed memory draws its
// own pixels.
func TestForgetAndRetireWaitForThePendingList(t *testing.T) {
	d := openDevice(t)
	tg := newTarget(t, d, 16, 16)
	rec := render.NewRecorder(16, 16, 1, 1)
	buf := make([]byte, 8*8*4)
	old := render.NewStillImage(rasterIn(buf, red))
	drawImage(rec, old)
	renderImage(t, d, tg, rec)

	drawImage(rec, old)
	d.Forget(old.ID)
	var freed atomic.Bool
	render.Retire(func() {
		rasterIn(buf, color.RGBA{G: 255, A: 255})
		freed.Store(true)
	})
	if freed.Load() {
		t.Fatal("the raster was freed while a recorded List draws it")
	}
	if px, n := renderImage(t, d, tg, rec); px != red || n != 0 {
		t.Errorf("the pending List drew %v with %d uploads, want %v from the kept texture", px, n, red)
	}
	for key := range d.images.byImage {
		if key.id == old.ID {
			t.Error("the forgotten raster's texture outlived the frame")
		}
	}
	if freed.Load() {
		t.Fatal("the raster was freed before its List ended")
	}
	rec.Recording().End()
	awaitRetired(t, &freed)

	drawImage(rec, render.NewStillImage(rasterIn(buf, blue)))
	if px, _ := renderImage(t, d, tg, rec); px != blue {
		t.Errorf("a raster in the freed memory drew %v, want %v", px, blue)
	}
}

// awaitRetired waits for freed: the List a test ends releases it, unless
// an earlier test's recorder, dropped without End, is still open until
// the garbage collector ends it.
func awaitRetired(t *testing.T, freed *atomic.Bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !freed.Load() {
		if time.Now().After(deadline) {
			t.Fatal("the raster was not freed once its List ended")
		}
		runtime.GC()
		time.Sleep(time.Millisecond)
	}
}

// Forget of a raster without an identity panics instead of matching
// every raster kept by address.
func TestForgetWithoutAnIdentityPanics(t *testing.T) {
	d := openDevice(t)
	defer func() {
		if recover() == nil {
			t.Error("Forget(0) did not panic")
		}
	}()
	d.Forget(0)
}
