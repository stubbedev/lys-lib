package gpu

import (
	"errors"
	"image"
	"image/color"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stubbedev/lys-lib/fourcc"
	"github.com/stubbedev/lys-lib/render"
)

func awaitPrepared(t *testing.T, d *Device) {
	t.Helper()
	deadline := time.Now().Add(time.Minute)
	for {
		ok, err := d.Prepared()
		if err != nil {
			t.Fatal(err)
		}
		if ok {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the pipelines were not built within a minute")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestAfterPrepareNoPassBuildsAPipeline(t *testing.T) {
	d := openDevice(t)
	if err := d.Prepare(fourcc.ARGB8888, fourcc.XRGB8888); err != nil {
		t.Fatal(err)
	}
	awaitPrepared(t, d)
	tg := newTarget(t, d, 32, 32)
	src := newTarget(t, d, 8, 8)
	runPass(t, d, src, []image.Rectangle{{Max: src.Size()}}, func(p *Pass) { p.Clear(color.RGBA{A: 0xff}) })
	tex, err := src.Texture()
	if err != nil {
		t.Fatal(err)
	}
	stops, err := d.NewTexture(fourcc.ARGB8888, 2, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer stops.Close()
	before := d.prep.builds.Load()
	ct := ColorTransform{Matrix: [9]float32{1, 0, 0, 0, 1, 0, 0, 0, 1}, Scale: 1}
	for _, colored := range []bool{false, true} {
		runPass(t, d, tg, []image.Rectangle{{Max: tg.Size()}}, func(p *Pass) {
			if colored {
				p.SetColor(&ct)
			}
			r := image.Rect(0, 0, 16, 16)
			p.FillRect(r, color.RGBA{A: 0xff}, Mask{})
			p.DrawTexture(tex, FRect{W: 8, H: 8}, r, TransformNormal, 1, Mask{})
			p.FillGradient(r, Gradient{Stops: stops, To: [2]float32{16, 0}, Alpha: 1}, Mask{})
			p.DrawGlass(tex, FRect{W: 8, H: 8}, r, 1, Mask{}, Glass{Saturation: 1, Brightness: 1, Contrast: 1})
			if !colored {
				p.Kawase(tex, r, 1, false)
				p.FillGoo(r, color.RGBA{A: 0xff}, Goo{A: RoundRect{Rect: r}, B: RoundRect{Rect: r}})
			}
			if p.Incomplete() {
				t.Error("a prepared pass skipped a draw")
			}
		})
	}
	if n := d.prep.builds.Load() - before; n != 0 {
		t.Errorf("%d pipelines built while passes recorded after Prepare", n)
	}
}

func TestAPassBeforeItsPipelineExistsSkipsTheDrawAndSaysSo(t *testing.T) {
	d := openDevice(t)
	if err := d.Prepare(); err != nil {
		t.Fatal(err)
	}
	tg := newTarget(t, d, 8, 8)
	red := color.RGBA{R: 0xff, A: 0xff}
	draw := func() bool {
		p, err := d.Begin(tg, []image.Rectangle{{Max: tg.Size()}})
		if err != nil {
			t.Fatal(err)
		}
		p.Clear(color.RGBA{A: 0xff})
		p.FillRect(image.Rect(0, 0, 8, 8), red, Mask{})
		incomplete := p.Incomplete()
		if err := p.End(); err != nil {
			t.Fatal(err)
		}
		return incomplete
	}
	builds := d.prep.builds.Load()
	if !draw() && d.prep.builds.Load() == builds {
		t.Log("the worker beat the first draw")
	}
	awaitPrepared(t, d)
	if draw() {
		t.Fatal("a pass after the worker finished skipped a draw")
	}
	px, err := tg.ReadPixels()
	if err != nil {
		t.Fatal(err)
	}
	if got := px.RGBAAt(4, 4); got != red {
		t.Errorf("drew %v, want %v", got, red)
	}
}

func TestThePipelineCachePersistsAcrossDevices(t *testing.T) {
	d := openDevice(t)
	dir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", dir)
	path, ok := pipelineCachePath(d.cacheUUID)
	if !ok {
		t.Fatal("no cache path with XDG_CACHE_HOME set")
	}
	d.prep.path = path
	if err := d.Prepare(fourcc.ARGB8888); err != nil {
		t.Fatal(err)
	}
	awaitPrepared(t, d)
	if err := d.savePipelineCache(); err != nil {
		t.Fatal(err)
	}
	first, err := os.Stat(path)
	if err != nil || first.Size() == 0 {
		t.Fatalf("the cache file: %v, %v", first, err)
	}
	if rel, err := filepath.Rel(dir, path); err != nil || filepath.Dir(rel) != "lys-lib" {
		t.Errorf("the cache lands at %s", path)
	}
	leftovers, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(leftovers) != 1 {
		t.Errorf("%d files beside the cache: a temporary was left behind", len(leftovers)-1)
	}
}

func TestTargetsPreparedOnAWorkerReachFramesWithoutStoppingThem(t *testing.T) {
	d := openDevice(t)
	tg := newTarget(t, d, 32, 32)
	rec := render.NewRecorder(32, 32, 1, 1)
	t.Cleanup(rec.Recording().End)
	rec.Clear(rec.Rect(), render.RGB(1, 2, 3))
	if err := d.Render(rec.Recording(), tg); err != nil {
		t.Fatal(err)
	}
	if err := d.Finish(); err != nil {
		t.Fatal(err)
	}
	const count = 16
	made := make(chan *Target, count)
	errs := make(chan error, 1)
	go func() {
		for range count {
			p, err := d.PrepareOffscreen(32, 32)
			if err != nil {
				errs <- err
				return
			}
			made <- p
		}
		close(made)
	}()
	waits := d.BlockingWaits()
	var got []*Target
	deadline := time.Now().Add(time.Minute)
	for open := true; open; {
		select {
		case err := <-errs:
			t.Fatal(err)
		case p, more := <-made:
			if !more {
				open = false
				break
			}
			got = append(got, p)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("the worker never finished")
		}
		err := d.Render(rec.Recording(), tg)
		if err != nil && !errors.Is(err, ErrBusy) {
			t.Fatal(err)
		}
	}
	if n := d.BlockingWaits() - waits; n != 0 {
		t.Errorf("%d blocking waits while the worker allocated", n)
	}
	if len(got) != count {
		t.Fatalf("%d targets of %d", len(got), count)
	}
	if err := d.Finish(); err != nil {
		t.Fatal(err)
	}
	for _, p := range got {
		if p.s.view == 0 || p.s.fb == 0 {
			t.Fatal("a prepared target left its view and framebuffer for its first draw")
		}
		if err := d.Render(rec.Recording(), p); err != nil {
			t.Fatal(err)
		}
		px, err := p.ReadPixels()
		if err != nil {
			t.Fatal(err)
		}
		if c := px.RGBAAt(1, 1); c != (color.RGBA{1, 2, 3, 255}) {
			t.Errorf("a prepared target drew %v", c)
		}
		if _, ok := d.targets[p]; !ok {
			t.Error("a prepared target is not registered after a frame")
		}
		p.Close()
	}
	last, err := d.PrepareOffscreen(8, 8)
	if err != nil {
		t.Fatal(err)
	}
	last.Close()
	if _, ok := d.targets[last]; ok {
		t.Error("a closed prepared target stayed registered")
	}
}
