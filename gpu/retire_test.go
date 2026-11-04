package gpu

import (
	"image"
	"image/color"
	"runtime"
	"testing"

	"github.com/stubbedev/lys-lib/fourcc"
	"github.com/stubbedev/lys-lib/render"
)

func TestFreeingResourcesNeverWaitsForTheGPU(t *testing.T) {
	d := openDevice(t)
	tg := newTarget(t, d, 64, 64)
	rec := render.NewRecorder(64, 64, 1, 1)
	frame := func() {
		t.Helper()
		rec.ResetRecorder(tg.Size().X, tg.Size().Y, 1, 1)
		rec.Clear(rec.Rect(), render.RGBA(0, 0, 0, 255))
		if err := d.Render(rec.Recording(), tg); err != nil {
			t.Fatal(err)
		}
	}
	frame()
	cases := []struct {
		name string
		run  func(t *testing.T)
	}{
		{"Forget", func(t *testing.T) {
			img := render.NewStillImage(image.NewRGBA(image.Rect(0, 0, 8, 8)))
			drawImage(rec, img)
			if err := d.Render(rec.Recording(), tg); err != nil {
				t.Fatal(err)
			}
			d.Forget(img.ID)
			frame()
		}},
		{"image LRU", func(t *testing.T) {
			d.images.limit = 1
			defer func() { d.images.limit = 0 }()
			for range 3 {
				drawImage(rec, render.NewStillImage(image.NewRGBA(image.Rect(0, 0, 8, 8))))
				if err := d.Render(rec.Recording(), tg); err != nil {
					t.Fatal(err)
				}
			}
		}},
		{"layer GC", func(t *testing.T) {
			keep := func() {
				rec.ResetRecorder(64, 64, 1, 1)
				l := rec.Layer(nil, rec.Rect())
				rec.DrawLayer(l, rec.Rect(), render.BlendNormal)
				if err := d.Render(rec.Recording(), tg); err != nil {
					t.Fatal(err)
				}
			}
			keep()
			frame()
			rec.Recording().End()
			runtime.GC()
			runtime.GC()
			frame()
		}},
		{"Target.Close", func(t *testing.T) {
			other, err := d.NewOffscreen(32, 32)
			if err != nil {
				t.Fatal(err)
			}
			rec.ResetRecorder(32, 32, 1, 1)
			rec.Clear(rec.Rect(), render.RGBA(0, 0, 0, 255))
			if err := d.Render(rec.Recording(), other); err != nil {
				t.Fatal(err)
			}
			other.Close()
		}},
		{"NewTexture and Texture.Close", func(t *testing.T) {
			for _, size := range []int{16, 32, 64} {
				tex, err := d.NewTexture(fourcc.ARGB8888, size, size)
				if err != nil {
					t.Fatal(err)
				}
				pix := make([]byte, size*size*4)
				if err := tex.Update(image.Rect(0, 0, size, size), size*4, pix); err != nil {
					t.Fatal(err)
				}
				p, err := d.Begin(tg, []image.Rectangle{image.Rect(0, 0, 8, 8)})
				if err != nil {
					t.Fatal(err)
				}
				p.DrawTexture(tex, FRect{W: float64(size), H: float64(size)}, image.Rect(0, 0, 8, 8), TransformNormal, 1, Mask{})
				if err := p.End(); err != nil {
					t.Fatal(err)
				}
				tex.Close()
			}
		}},
		{"upload ring growth", func(t *testing.T) {
			tex, err := d.NewTexture(fourcc.ARGB8888, 8, 8)
			if err != nil {
				t.Fatal(err)
			}
			defer tex.Close()
			big, err := d.NewTexture(fourcc.ARGB8888, 512, 512)
			if err != nil {
				t.Fatal(err)
			}
			defer big.Close()
			if err := tex.Update(image.Rect(0, 0, 8, 8), 32, make([]byte, 8*8*4)); err != nil {
				t.Fatal(err)
			}
			if err := big.Update(image.Rect(0, 0, 512, 512), 512*4, make([]byte, 512*512*4)); err != nil {
				t.Fatal(err)
			}
		}},
		{"scratch and filter growth", func(t *testing.T) {
			bigger, err := d.NewOffscreen(256, 256)
			if err != nil {
				t.Fatal(err)
			}
			defer bigger.Close()
			var fl render.Filters
			fl.Append(render.FilterOp{Kind: render.FilterBlur, Amount: 2})
			rec.ResetRecorder(256, 256, 1, 1)
			l := rec.ScratchLayer(rec.Rect())
			render.ApplyFilters(l, rec.Rect(), &fl, render.FilterShape{})
			rec.DrawLayer(l, rec.Rect(), render.BlendNormal)
			l.Release()
			if err := d.Render(rec.Recording(), bigger); err != nil {
				t.Fatal(err)
			}
		}},
		{"colour slots", func(t *testing.T) {
			for _, n := range []int{colorSlots/2 + 8, 8} {
				p, err := d.Begin(tg, []image.Rectangle{image.Rect(0, 0, 8, 8)})
				if err != nil {
					t.Fatal(err)
				}
				for i := range n {
					ct := ColorTransform{Matrix: [9]float32{1 + float32(i)/1000, 0, 0, 0, 1, 0, 0, 0, 1}, Scale: 1}
					p.SetColor(&ct)
				}
				p.Clear(color.RGBA{})
				if err := p.End(); err != nil {
					t.Fatal(err)
				}
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			before := d.BlockingWaits()
			c.run(t)
			if n := d.BlockingWaits() - before; n != 0 {
				t.Errorf("%d blocking waits", n)
			}
		})
	}
	if err := d.Finish(); err != nil {
		t.Fatal(err)
	}
	if n := len(d.graves); n != 0 {
		t.Errorf("%d resources still queued after Finish", n)
	}
}

func TestAResourceFreedWithNothingInFlightGoesAtOnce(t *testing.T) {
	d := openDevice(t)
	tex, err := d.NewTexture(fourcc.ARGB8888, 8, 8)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Finish(); err != nil {
		t.Fatal(err)
	}
	tex.Close()
	if n := len(d.graves); n != 0 {
		t.Errorf("%d resources queued with nothing in flight", n)
	}
}

func TestAClosedTexturesUpdateFinishesBeforeItIsDestroyed(t *testing.T) {
	d := openDevice(t)
	tex, err := d.NewTexture(fourcc.ARGB8888, 8, 8)
	if err != nil {
		t.Fatal(err)
	}
	if err := tex.Update(image.Rect(0, 0, 8, 8), 32, make([]byte, 8*8*4)); err != nil {
		t.Fatal(err)
	}
	tex.Close()
	if len(d.graves) != 1 {
		t.Fatalf("%d queued while a batch names the texture, want 1", len(d.graves))
	}
	if err := d.Finish(); err != nil {
		t.Fatal(err)
	}
	if len(d.graves) != 0 {
		t.Errorf("%d queued after the batch finished", len(d.graves))
	}
}
