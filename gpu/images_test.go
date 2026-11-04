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

func TestAVisibleImageOutsideTheDamageKeepsItsTexture(t *testing.T) {
	d := openDevice(t)
	const w, h = 200, 100
	tg := newTarget(t, d, w, h)
	rec := render.NewRecorder(w, h, 1, 1)
	img := render.NewStillImage(image.NewRGBA(image.Rect(0, 0, 64, 64)))
	draw := func(clip render.Rect) int {
		t.Helper()
		rec.ResetRecorder(w, h, 1, 1)
		prev := rec.PushClipDevice(clip)
		rec.DrawImage(img, 0, 0)
		rec.FillRectDevice(clip, render.RGBA(16, 32, 48, 255))
		rec.PopClip(prev)
		if err := d.Render(rec.Recording(), tg); err != nil {
			t.Fatal(err)
		}
		if err := d.Finish(); err != nil {
			t.Fatal(err)
		}
		return d.frame.stats.textureUploads
	}
	draw(render.Rect{W: w, H: h})
	for range 400 {
		if n := draw(render.Rect{X: 150, Y: 50, W: 10, H: 10}); n != 0 {
			t.Fatalf("a frame damaged elsewhere uploaded %d textures", n)
		}
	}
	if n := len(d.images.byImage); n != 1 {
		t.Errorf("%d textures after the damaged-elsewhere frames, want 1", n)
	}
	if n := draw(render.Rect{W: w, H: h}); n != 0 {
		t.Errorf("damage over the image again uploaded %d textures, want 0", n)
	}
}

func TestTrimFreesTheImagesNotDrawnSinceTheLastTrim(t *testing.T) {
	d := openDevice(t)
	tg := newTarget(t, d, 16, 16)
	rec := render.NewRecorder(16, 16, 1, 1)
	drawImage(rec, render.NewStillImage(rasterIn(make([]byte, 8*8*4), red)))
	renderImage(t, d, tg, rec)
	if err := d.Trim(); err != nil {
		t.Fatal(err)
	}
	if n := len(d.images.byImage); n != 1 {
		t.Fatalf("Trim freed an image drawn since the last one: %d left", n)
	}
	if err := d.Trim(); err != nil {
		t.Fatal(err)
	}
	if n := len(d.images.byImage); n != 0 {
		t.Errorf("%d textures after a Trim with no draw since the last", n)
	}
}

func TestAResizingShadowNeverEmptiesTheAtlas(t *testing.T) {
	d := openDevice(t)
	rec := render.NewRecorder(1, 1, 1, 1)
	uploads := 0
	for i := range 100 {
		w, h := 200+i*8, 150+i*6
		tg, err := d.NewOffscreen(w, h)
		if err != nil {
			t.Fatal(err)
		}
		rec.ResetRecorder(w, h, 1, 1)
		rec.Shadow(render.Rect{X: 32, Y: 32, W: w - 64, H: h - 64}, 8, 24, render.RGBA(0, 0, 0, 128))
		if err := d.Render(rec.Recording(), tg); err != nil {
			t.Fatal(err)
		}
		uploads += d.frame.stats.textureUploads
		tg.Close()
		if err := d.Finish(); err != nil {
			t.Fatal(err)
		}
	}
	if d.masks.resets != 0 {
		t.Errorf("the atlas reset %d times", d.masks.resets)
	}
	if uploads != 1 {
		t.Errorf("%d mask uploads over the resize, want 1", uploads)
	}
}

func TestAnImageUploadIsACopyOfWhatChanged(t *testing.T) {
	d := openDevice(t)
	tg := newTarget(t, d, 64, 64)
	rec := render.NewRecorder(64, 64, 1, 1)
	img := image.NewRGBA(image.Rect(0, 0, 64, 64))
	id := render.NewImageID()
	draw := func(v render.VersionedImage) int {
		t.Helper()
		rec.ResetRecorder(64, 64, 1, 1)
		rec.DrawImageDevice(v, 0, 0)
		if err := d.Render(rec.Recording(), tg); err != nil {
			t.Fatal(err)
		}
		return d.frame.stats.imageBytes
	}
	if n := draw(render.VersionedImage{RGBA: img, ID: id, Version: 1}); n != 64*64*4 {
		t.Errorf("the first upload staged %d bytes", n)
	}
	img.SetRGBA(10, 10, color.RGBA{R: 200, G: 100, B: 50, A: 255})
	dirty := image.Rect(8, 8, 16, 12)
	if n := draw(render.VersionedImage{RGBA: img, ID: id, Version: 2, DirtySince: 1, Dirty: dirty}); n != 8*4*4 {
		t.Errorf("an 8x4 dirty rect staged %d bytes", n)
	}
	got, err := tg.ReadPixels()
	if err != nil {
		t.Fatal(err)
	}
	if c := got.RGBAAt(10, 10); c != (color.RGBA{R: 200, G: 100, B: 50, A: 255}) {
		t.Errorf("the dirty pixel drew %v", c)
	}
	if n := draw(render.VersionedImage{RGBA: img, ID: id, Version: 4, DirtySince: 3, Dirty: dirty}); n != 64*64*4 {
		t.Errorf("a dirty rect from a version the device does not hold staged %d bytes, want the whole image", n)
	}
}

func TestALargeStillUploadsOffTheFrameAndDrawsWhenItIsThere(t *testing.T) {
	d := openDevice(t)
	tg := newTarget(t, d, 16, 16)
	rec := render.NewRecorder(16, 16, 1, 1)
	big := image.NewRGBA(image.Rect(0, 0, 1600, 1400))
	for i := 0; i < len(big.Pix); i += 4 {
		big.Pix[i], big.Pix[i+3] = 255, 255
	}
	still := render.NewStillImage(big)
	if n := len(big.Pix); n < stillWorkerBytes {
		t.Fatalf("%d bytes do not leave the frame path", n)
	}
	drawImage(rec, still)
	if err := d.Render(rec.Recording(), tg); err != nil {
		t.Fatal(err)
	}
	if !d.FrameIncomplete() {
		t.Fatal("the first frame reports nothing left out")
	}
	if d.frame.stats.imageBytes != 0 {
		t.Errorf("the frame staged %d bytes of the still", d.frame.stats.imageBytes)
	}
	deadline := time.Now().Add(time.Minute)
	for d.FrameIncomplete() {
		if time.Now().After(deadline) {
			t.Fatal("the still never arrived")
		}
		time.Sleep(time.Millisecond)
		if err := d.Finish(); err != nil {
			t.Fatal(err)
		}
		if err := d.Render(rec.Recording(), tg); err != nil {
			t.Fatal(err)
		}
	}
	if px, n := renderImage(t, d, tg, rec); px != red || n != 0 {
		t.Errorf("drew %v with %d uploads, want %v from the worker's", px, n, red)
	}
}

func TestAForgottenStillOnItsWayIsDropped(t *testing.T) {
	d := openDevice(t)
	tg := newTarget(t, d, 16, 16)
	rec := render.NewRecorder(16, 16, 1, 1)
	still := render.NewStillImage(image.NewRGBA(image.Rect(0, 0, 1600, 1400)))
	drawImage(rec, still)
	if err := d.Render(rec.Recording(), tg); err != nil {
		t.Fatal(err)
	}
	d.Forget(still.ID)
	rec.ResetRecorder(16, 16, 1, 1)
	if err := d.Render(rec.Recording(), tg); err != nil {
		t.Fatal(err)
	}
	d.images.stills.wait()
	if err := d.Render(rec.Recording(), tg); err != nil {
		t.Fatal(err)
	}
	if n := len(d.images.byImage); n != 0 {
		t.Errorf("%d textures for a forgotten raster", n)
	}
}

func TestAMaskStagesOneByteAPixel(t *testing.T) {
	d := openDevice(t)
	tg := newTarget(t, d, 64, 64)
	rec := render.NewRecorder(64, 64, 1, 1)
	t.Cleanup(rec.Recording().End)
	rec.Shadow(render.Rect{X: 20, Y: 20, W: 20, H: 20}, 4, 6, render.RGBA(0, 0, 0, 200))
	rec.Shadow(render.Rect{X: 21, Y: 20, W: 3, H: 20}, 1, 2, render.RGBA(0, 0, 0, 200))
	if err := d.Render(rec.Recording(), tg); err != nil {
		t.Fatal(err)
	}
	want := 0
	for _, r := range []render.Rect{{X: 20, Y: 20, W: 20, H: 20}, {X: 21, Y: 20, W: 3, H: 20}} {
		m, _, _, _, _ := render.RectShadow(r, map[int]int{20: 4, 3: 1}[r.W], map[int]int{20: 6, 3: 2}[r.W], 1, 1)
		w, h := m.Size()
		want += w * h
	}
	if got := d.frame.stats.maskBytes; got != want {
		t.Errorf("masks staged %d bytes, want their %d bytes of coverage", got, want)
	}
	got, err := tg.ReadPixels()
	if err != nil {
		t.Fatal(err)
	}
	if c := got.RGBAAt(30, 30); c.A < 200 {
		t.Errorf("the shadow's inside drew %v", c)
	}
}

type testMask struct {
	id   render.ImageID
	w, h int
}

func (m testMask) ID() render.ImageID { return m.id }
func (m testMask) Pix() []uint8       { return make([]uint8, m.w*m.h) }
func (m testMask) Size() (int, int)   { return m.w, m.h }

func TestAFullAtlasGivesUpItsLeastRecentlyDrawnShelf(t *testing.T) {
	d := openDevice(t)
	d.masks.size = 64
	tg := newTarget(t, d, 64, 64)
	if err := d.ensureScratch(64, 64); err != nil {
		t.Fatal(err)
	}
	if err := d.takeFrameBuffers(); err != nil {
		t.Fatal(err)
	}
	f := &d.frame
	f.d, f.target, f.num, f.denom, f.err = d, tg, 1, 1, nil
	f.clear()
	f.mapSegment()
	f.stats.reset()
	masks := make([]testMask, 17)
	for i := range masks {
		masks[i] = testMask{id: render.NewImageID(), w: 16, h: 16}
	}
	for _, m := range masks[:16] {
		if _, ok := f.maskSlot(m); !ok {
			t.Fatal(f.err)
		}
	}
	for _, m := range masks[4:16] {
		f.maskSlot(m)
	}
	if err := f.flush(false); err != nil {
		t.Fatal(err)
	}
	for _, m := range masks[4:16] {
		f.maskSlot(m)
	}
	uploads := f.stats.textureUploads
	if _, ok := f.maskSlot(masks[16]); !ok {
		t.Fatal(f.err)
	}
	for _, m := range masks[4:16] {
		f.maskSlot(m)
	}
	if n := f.stats.textureUploads - uploads; n != 1 {
		t.Errorf("%d uploads for one new mask, want 1 and the drawn shelves kept", n)
	}
	for _, m := range masks[:4] {
		if _, kept := d.masks.slots[m.id]; kept {
			t.Errorf("a mask of the evicted shelf stayed")
		}
	}
	if d.masks.resets != 0 || d.masks.evictions != 1 {
		t.Errorf("%d resets and %d evictions, want 0 and 1", d.masks.resets, d.masks.evictions)
	}
	if err := f.flush(true); err != nil {
		t.Fatal(err)
	}
}

func TestSteadyTextNeverResetsTheAtlas(t *testing.T) {
	d := openDevice(t)
	tg := newTarget(t, d, 320, 200)
	face, err := render.NewFixtureTypeface()
	if err != nil {
		t.Fatal(err)
	}
	rec := render.NewRecorder(320, 200, 1, 1)
	t.Cleanup(rec.Recording().End)
	for frame := range 30 {
		rec.ResetRecorder(320, 200, 1, 1)
		for i := range 8 {
			face.Shape("steady text, frame after frame", float64(10+i)).Draw(rec, 4, 20+22*i, render.RGB(0, 0, 0))
		}
		if err := d.Render(rec.Recording(), tg); err != nil {
			t.Fatal(err)
		}
		if err := d.Finish(); err != nil {
			t.Fatal(err)
		}
		if frame > 0 && d.frame.stats.textureUploads != 0 {
			t.Errorf("frame %d uploaded %d masks", frame, d.frame.stats.textureUploads)
		}
	}
	if d.masks.resets != 0 || d.masks.evictions != 0 {
		t.Errorf("%d resets and %d evictions", d.masks.resets, d.masks.evictions)
	}
}
