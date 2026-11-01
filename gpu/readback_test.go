package gpu

import (
	"errors"
	"image"
	"image/color"
	"testing"
)

func filled(t *testing.T, d *Device, size image.Point, c color.RGBA) *Target {
	t.Helper()
	tg, err := d.NewOffscreen(size.X, size.Y)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(tg.Close)
	runPass(t, d, tg, []image.Rectangle{{Max: size}}, func(p *Pass) { p.Clear(c) })
	return tg
}

// The readback is the path a device without dmabufs or sync_files takes,
// lavapipe without /dev/udmabuf among them, so the test takes both away.
func TestAReadbackNeedsNoDmabufAndSeesTheFrameCopied(t *testing.T) {
	d := openDevice(t)
	d.exports, d.syncFiles = false, false
	size := image.Pt(5, 3)
	blue := color.RGBA{B: 0xff, A: 0xff}
	tg := filled(t, d, size, blue)
	r, err := d.NewReadback(size.X, size.Y)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	for _, c := range []color.RGBA{blue, {R: 0xff, A: 0xff}} {
		runPass(t, d, tg, []image.Rectangle{{Max: size}}, func(p *Pass) { p.Clear(c) })
		if err := r.Copy(tg); err != nil {
			t.Fatal(err)
		}
		runPass(t, d, tg, []image.Rectangle{{Max: size}}, func(p *Pass) { p.Clear(color.RGBA{G: 0xff, A: 0xff}) })
		done := make(chan error, 1)
		go func() { done <- r.Wait() }()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		pix, stride, err := r.Pixels()
		if err != nil {
			t.Fatal(err)
		}
		if stride != size.X*4 || len(pix) < size.Y*stride {
			t.Fatalf("%d bytes, stride %d, for %v", len(pix), stride, size)
		}
		for i := 0; i < size.Y*stride; i += 4 {
			if got := (color.RGBA{pix[i+2], pix[i+1], pix[i], pix[i+3]}); got != c {
				t.Fatalf("byte %d reads %v, want the frame copied, %v", i, got, c)
			}
		}
	}
}

func TestAReadbackRefusesWhatItCannotCopy(t *testing.T) {
	d := openDevice(t)
	size := image.Pt(4, 4)
	r, err := d.NewReadback(size.X, size.Y)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if err := r.Wait(); err == nil {
		t.Error("Wait with no copy took")
	}
	if _, _, err := r.Pixels(); err == nil {
		t.Error("Pixels with no copy took")
	}
	if err := r.Copy(filled(t, d, image.Pt(8, 4), color.RGBA{A: 0xff})); err == nil {
		t.Error("a copy of another size took")
	}
	blank, err := d.NewOffscreen(size.X, size.Y)
	if err != nil {
		t.Fatal(err)
	}
	defer blank.Close()
	if err := r.Copy(blank); err == nil {
		t.Error("a copy of a target never drawn took")
	}
	r.Close()
	if err := r.Copy(filled(t, d, size, color.RGBA{A: 0xff})); err == nil {
		t.Error("a copy into a closed readback took")
	}
}

func TestAReadbackOnALostDeviceReportsIt(t *testing.T) {
	d := openDevice(t)
	size := image.Pt(4, 4)
	tg := filled(t, d, size, color.RGBA{A: 0xff})
	r, err := d.NewReadback(size.X, size.Y)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if err := r.Copy(tg); err != nil {
		t.Fatal(err)
	}
	if err := r.Wait(); err != nil {
		t.Fatal(err)
	}
	loseDevice(t, d, "waitForFences", "getFenceStatus")
	if err := r.Wait(); !errors.Is(err, ErrDeviceLost) {
		t.Errorf("Wait: %v, want ErrDeviceLost", err)
	}
	if _, _, err := r.Pixels(); !errors.Is(err, ErrDeviceLost) {
		t.Errorf("Pixels: %v, want ErrDeviceLost", err)
	}
}

func TestDeviceCloseClosesReadbacks(t *testing.T) {
	d := openDevice(t)
	r, err := d.NewReadback(4, 4)
	if err != nil {
		t.Fatal(err)
	}
	d.Close()
	r.Close()
	if err := r.Wait(); err == nil {
		t.Error("Wait on a readback closed with its device took")
	}
}
