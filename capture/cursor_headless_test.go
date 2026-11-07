package capture_test

import (
	"errors"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/stubbedev/lys-lib/capture"
	"github.com/stubbedev/lys-lib/vinput"
	"github.com/stubbedev/lys-lib/wltest"
)

func TestHeadlessCursorFollowsThePointerAndCopiesItsImageOnce(t *testing.T) {
	wltest.Require(t)
	pointer, err := vinput.Dial("", vinput.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pointer.Close)
	out := captureClient(t).Outputs()[0]
	w, h := uint32(out.Width), uint32(out.Height)
	if err := errors.Join(pointer.MotionAbsolute(w/2, h/2, w, h), pointer.Roundtrip()); err != nil {
		t.Fatal(err)
	}
	k, err := capture.OpenOutputCursor(out.Name)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = k.Close() })
	var changes atomic.Int64
	k.OnChange(func() { changes.Add(1) })
	wltest.Eventually(t, "the pointer's image", func() error {
		if s := k.State(); !s.Inside || s.Shape == 0 {
			return fmt.Errorf("state %+v", s)
		}
		return nil
	})
	first := k.State()
	if first.Width <= 0 || first.Height <= 0 || first.Stride < first.Width*first.Format.BytesPerPixel() {
		t.Fatalf("image layout %+v", first)
	}
	if first.HotspotX < 0 || first.HotspotY < 0 || first.HotspotX >= first.Width || first.HotspotY >= first.Height {
		t.Fatalf("hotspot outside the %dx%d image: %+v", first.Width, first.Height, first)
	}
	img := make([]byte, first.Width*first.Height*4)
	gotW, gotH, ok := k.ReadImage(first.Shape, img, first.Width, 2)
	if !ok || gotW != first.Width || gotH != 2 {
		t.Fatalf("a 2-row crop read %dx%d, ok %v", gotW, gotH, ok)
	}
	if _, _, ok := k.ReadImage(first.Shape+1, img, first.Width, first.Height); ok {
		t.Fatal("an image not captured yet was read")
	}
	const moves = 20
	for i := range moves {
		x, y := uint32(10+i*7), uint32(20+i*5)
		if err := errors.Join(pointer.MotionAbsolute(x, y, w, h), pointer.Roundtrip()); err != nil {
			t.Fatal(err)
		}
		wltest.Eventually(t, fmt.Sprintf("the pointer at %d,%d", x, y), func() error {
			if s := k.State(); !s.Inside || s.X != int(x) || s.Y != int(y) {
				return fmt.Errorf("state %+v", s)
			}
			return nil
		})
	}
	if s := k.State(); s.Shape != first.Shape {
		t.Fatalf("moving the pointer copied its image again: shape %d, was %d", s.Shape, first.Shape)
	}
	if changes.Load() < moves {
		t.Fatalf("%d moves woke OnChange %d times", moves, changes.Load())
	}
	if err := k.Close(); err != nil {
		t.Fatal(err)
	}
	if err := k.Err(); err != nil {
		t.Fatalf("a closed cursor reports %v", err)
	}
	if _, _, ok := k.ReadImage(first.Shape, img, first.Width, first.Height); ok {
		t.Fatal("a closed cursor's image was read")
	}
	if _, err := capture.OpenOutputCursor("NOPE-1"); err == nil {
		t.Fatal("a cursor opened on a missing output")
	}
}

// A new pointer image over the source is copied once, at its own size:
// the image buffers follow it from shape to shape.
func TestHeadlessCursorCopiesEachNewImage(t *testing.T) {
	wltest.Require(t)
	p := wltest.NewPainter(t)
	p.Layer(wltest.AnchorTop|wltest.AnchorBottom|wltest.AnchorLeft|wltest.AnchorRight, 0, 0, fillGreen)
	p.Run()
	pointer, err := vinput.Dial("", vinput.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pointer.Close)
	out := captureClient(t).Outputs()[0]
	w, h := uint32(out.Width), uint32(out.Height)
	k, err := capture.OpenOutputCursor(out.Name)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = k.Close() })
	if err := errors.Join(pointer.MotionAbsolute(w/3, h/3, w, h), pointer.Roundtrip()); err != nil {
		t.Fatal(err)
	}
	p.Cursor(16, fillRed)
	wltest.Eventually(t, "the painter's 16x16 image", func() error {
		if s := k.State(); s.Width != 16 || s.Height != 16 {
			return fmt.Errorf("state %+v", s)
		}
		return nil
	})
	first := k.State()
	for i, side := range []int32{24, 32, 20} {
		p.Cursor(side, fillRed)
		want := first.Shape + uint64(i) + 1
		wltest.Eventually(t, fmt.Sprintf("the %dx%d image", side, side), func() error {
			if s := k.State(); s.Shape != want || s.Width != int(side) || s.Height != int(side) || s.HotspotX != int(side)/2 {
				return fmt.Errorf("state %+v, want shape %d", s, want)
			}
			return nil
		})
		img := make([]byte, side*side*4)
		if gotW, gotH, ok := k.ReadImage(want, img, int(side), int(side)); !ok || gotW != int(side) || gotH != int(side) {
			t.Fatalf("read the %dx%d image as %dx%d, ok %v", side, side, gotW, gotH, ok)
		}
		if px := uint32(img[0]) | uint32(img[1])<<8 | uint32(img[2])<<16 | uint32(img[3])<<24; px != 0xffd02030 {
			t.Fatalf("the image's first pixel %#x, want the painted red", px)
		}
	}
}
