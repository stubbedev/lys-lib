package gpu

import (
	"image"
	"strings"
	"testing"

	"github.com/stubbedev/lys-lib/media/demux"
)

// heldFrame decodes the first picture of a w x h H.264 clip on a device
// that decodes it and hands out its frame, with the decoder it is of.
func heldFrame(t *testing.T, w, h int) (*Device, *VideoDecoder, *VideoFrame) {
	t.Helper()
	d := decodingDevice(t, demux.H264)
	cl := ffmpegClip(t, "a.mp4", w, h, 0.2, "-c:v", "libx264")
	track, pkts := packets(t, cl.path)
	v, err := d.NewVideoDecoder(track)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(v.Close)
	if err := v.Decode(pkts[0]); err != nil {
		t.Fatal(err)
	}
	v.Flush()
	f, err := v.Next()
	if err != nil || f == nil {
		t.Fatalf("frame %v, %v", f, err)
	}
	return d, v, f
}

// endOf is what End says of a pass over all of a new w x h offscreen
// target that draw drew.
func endOf(t *testing.T, d *Device, w, h int, draw func(p *Pass)) error {
	t.Helper()
	tg := newTarget(t, d, w, h)
	p, err := d.Begin(tg, []image.Rectangle{{Max: tg.Size()}})
	if err != nil {
		t.Fatal(err)
	}
	draw(p)
	return p.End()
}

// A frame's texture stays the decoder's: a pass draws the frame itself,
// a region of its picture in the picture's own pixels, and refuses one
// reaching into the codec's padding, a frame handed back and a frame
// whose decoder is closed, each by name.
func TestDrawFrameRefusesWhatIsNotItsPicture(t *testing.T) {
	const w, h = 174, 98
	d, v, f := heldFrame(t, w, h)
	if f.extent == [2]int{w, h} {
		t.Fatalf("the %dx%d picture is coded without padding, which this test draws into", w, h)
	}
	whole := FRect{W: w, H: h}
	dst := image.Rect(0, 0, w, h)
	for _, c := range []struct {
		name string
		src  FRect
		want string
	}{
		{"the padding to the right", FRect{W: float64(f.extent[0]), H: h}, "outside a 174x98 picture"},
		{"the padding below", FRect{W: w, H: float64(f.extent[1])}, "outside a 174x98 picture"},
		{"before the picture", FRect{X: -1, W: w, H: h}, "outside a 174x98 picture"},
		{"nothing", FRect{}, "outside a 174x98 picture"},
	} {
		err := endOf(t, d, w, h, func(p *Pass) { p.DrawFrame(f, c.src, dst, TransformNormal, 1, Mask{}) })
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("drawing %s: %v, want an error saying %q", c.name, err, c.want)
		}
	}
	if err := endOf(t, d, w, h, func(p *Pass) { p.DrawFrame(f, whole, dst, TransformNormal, 1, Mask{}) }); err != nil {
		t.Errorf("drawing the picture: %v", err)
	}
	if err := endOf(t, d, w, h, func(p *Pass) { p.DrawFrame(nil, whole, dst, TransformNormal, 1, Mask{}) }); err == nil || !strings.Contains(err.Error(), "does not hold") {
		t.Errorf("drawing no frame: %v, want an error saying the caller does not hold it", err)
	}
	other := openDevice(t)
	if err := endOf(t, other, w, h, func(p *Pass) { p.DrawFrame(f, whole, dst, TransformNormal, 1, Mask{}) }); err == nil || !strings.Contains(err.Error(), "another device") {
		t.Errorf("drawing on another device: %v, want an error saying so", err)
	}
	f.Release()
	if err := endOf(t, d, w, h, func(p *Pass) { p.DrawFrame(f, whole, dst, TransformNormal, 1, Mask{}) }); err == nil || !strings.Contains(err.Error(), "does not hold") {
		t.Errorf("drawing a released frame: %v, want an error saying the caller does not hold it", err)
	}
	d, v, f = heldFrame(t, w, h)
	err := endOf(t, d, w, h, func(p *Pass) {
		p.DrawFrame(f, whole, dst, TransformNormal, 1, Mask{})
		v.Close()
	})
	if err == nil || !strings.Contains(err.Error(), "closed before End") {
		t.Errorf("closing the decoder before End: %v, want an error saying the pass drew what was closed", err)
	}
	if err := endOf(t, d, w, h, func(p *Pass) { p.DrawFrame(f, whole, dst, TransformNormal, 1, Mask{}) }); err == nil || !strings.Contains(err.Error(), "decoder is closed") {
		t.Errorf("drawing a closed decoder's frame: %v, want an error saying so", err)
	}
}

// A region of a frame draws as that region of the whole picture does:
// the source is in the picture's pixels.
func TestDrawFrameSamplesItsSource(t *testing.T) {
	const w, h = 174, 98
	d, _, f := heldFrame(t, w, h)
	defer f.Release()
	full := newTarget(t, d, w, h)
	runPass(t, d, full, []image.Rectangle{{Max: full.Size()}}, func(p *Pass) {
		p.DrawFrame(f, FRect{W: w, H: h}, image.Rect(0, 0, w, h), TransformNormal, 1, Mask{})
	})
	part := image.Rect(64, 32, 128, 80)
	crop := newTarget(t, d, part.Dx(), part.Dy())
	runPass(t, d, crop, []image.Rectangle{{Max: crop.Size()}}, func(p *Pass) {
		p.DrawFrame(f, FRect{X: float64(part.Min.X), Y: float64(part.Min.Y), W: float64(part.Dx()), H: float64(part.Dy())}, image.Rectangle{Max: crop.Size()}, TransformNormal, 1, Mask{})
	})
	want, got := readTarget(t, full), readTarget(t, crop)
	for y := range part.Dy() {
		for x := range part.Dx() {
			if a, b := got.RGBAAt(x, y), want.RGBAAt(part.Min.X+x, part.Min.Y+y); !within(a, b, 1) {
				t.Fatalf("pixel %d,%d of the region is %v, the picture's at %d,%d is %v", x, y, a, part.Min.X+x, part.Min.Y+y, b)
			}
		}
	}
}

// A frame released while the pass that drew it is still open is not
// decoded into before that pass's submission has sampled it.
func TestFrameReleasedInAPassOutlivesIt(t *testing.T) {
	const w, h = 174, 98
	d, _, f := heldFrame(t, w, h)
	if err := d.Finish(); err != nil {
		t.Fatal(err)
	}
	err := endOf(t, d, w, h, func(p *Pass) {
		p.DrawFrame(f, FRect{W: w, H: h}, image.Rect(0, 0, w, h), TransformNormal, 1, Mask{})
		f.Release()
	})
	if err != nil {
		t.Fatal(err)
	}
	if f.after <= d.submitted {
		t.Errorf("the frame is free after submission %d, and the pass that drew it is in the one after %d", f.after, d.submitted)
	}
	if err := d.Finish(); err != nil {
		t.Fatal(err)
	}
	if f.after > d.completed {
		t.Errorf("the frame waits for submission %d after the device finished %d", f.after, d.completed)
	}
}
