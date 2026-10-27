package gpu

import (
	"image"
	"testing"

	"github.com/stubbedev/lys-lib/render"
)

// A large upload grows the staging buffer for its frame, written
// straight into it, and the next frame that needs less shrinks it back:
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
	if n := len(d.staging.mapped); n < len(big.Pix) {
		t.Fatalf("staging holds %d bytes after a %d-byte upload", n, len(big.Pix))
	}
	drawImage(rec, render.NewStillImage(rasterIn(make([]byte, 8*8*4), blue)))
	if px, _ := renderImage(t, d, tg, rec); px != blue {
		t.Fatalf("the small upload drew %v, want %v", px, blue)
	}
	if n := len(d.staging.mapped); n > 4*stagingMin {
		t.Errorf("staging kept %d bytes after a frame that staged 256", n)
	}
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
