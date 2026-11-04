package gpu

import (
	"image"
	"image/color"
	"testing"

	"github.com/stubbedev/lys-lib/render"
)

func effectFrame(rec *render.Canvas, region render.Rect, amount float64, path *render.Path) {
	rec.Clear(rec.Rect(), render.RGB(200, 210, 220))
	var fl render.Filters
	if amount > 0 {
		fl.Append(render.FilterOp{Kind: render.FilterBlur, Amount: amount})
	}
	l := rec.ScratchLayer(region)
	for i := range 40 {
		l.Canvas().RoundedRect(render.Rect{X: region.X + 4 + (i%8)*40, Y: region.Y + 4 + (i/8)*40, W: 32, H: 32}, 4, render.RGBA(uint8(i*6), 80, uint8(200-i*4), 240))
	}
	if amount > 0 {
		render.ApplyFilters(l, region, &fl, render.FilterShape{})
	}
	if path != nil {
		l.ApplyPathMask(path, region)
	}
	rec.DrawLayer(l, region, render.BlendNormal)
	l.Release()
}

func TestDamagedEffectPixelsMatchTheFullFrameAndCostOnlyTheirArea(t *testing.T) {
	d := openDevice(t)
	const w, h = 400, 300
	tg := newTarget(t, d, w, h)
	region := render.Rect{X: 10, Y: 10, W: 360, H: 270}
	damage := render.Rect{X: 137, Y: 91, W: 9, H: 7}
	var rrect, curve render.Path
	render.RoundedRectPath(&rrect, region, render.Corners{TopLeft: 24, TopRight: 24, BottomLeft: 24, BottomRight: 24})
	render.EllipsePath(&curve, 200, 150, 170, 120)
	for _, amount := range []float64{0, 3, 26} {
		for _, path := range []*render.Path{nil, &rrect, &curve} {
			rec := render.NewRecorder(w, h, 1, 1)
			effectFrame(rec, region, amount, path)
			if err := d.Render(rec.Recording(), tg); err != nil {
				t.Fatal(err)
			}
			full, err := tg.ReadPixels()
			if err != nil {
				t.Fatal(err)
			}
			rec.Recording().End()

			rec = render.NewRecorder(w, h, 1, 1)
			prev := rec.PushClipDevice(damage)
			effectFrame(rec, region, amount, path)
			rec.PopClip(prev)
			if err := d.Render(rec.Recording(), tg); err != nil {
				t.Fatal(err)
			}
			if amount == 0 && d.frame.stats.prims > 8 {
				t.Errorf("mask %v: %d prims under a 9x7 damage", path != nil, d.frame.stats.prims)
			}
			got, err := tg.ReadPixels()
			if err != nil {
				t.Fatal(err)
			}
			rec.Recording().End()
			for y := damage.Y; y < damage.Y+damage.H; y++ {
				for x := damage.X; x < damage.X+damage.W; x++ {
					if a, b := got.RGBAAt(x, y), full.RGBAAt(x, y); a != b {
						t.Fatalf("blur %v mask %v: (%d,%d) is %v damaged, %v in the full frame", amount, path != nil, x, y, a, b)
					}
				}
			}
		}
	}
}

func TestDrawsSamplingNothingJoinTheStepAround(t *testing.T) {
	d := openDevice(t)
	tg := newTarget(t, d, 64, 64)
	rec := render.NewRecorder(64, 64, 1, 1)
	img := render.NewStillImage(image.NewRGBA(image.Rect(0, 0, 8, 8)))
	for i := range 8 {
		rec.FillRectDevice(render.Rect{X: i, Y: i, W: 4, H: 4}, render.RGB(1, 2, 3))
		rec.DrawImageDevice(img, i, i)
	}
	if err := d.Render(rec.Recording(), tg); err != nil {
		t.Fatal(err)
	}
	if err := d.ensureScratch(64, 64); err != nil {
		t.Fatal(err)
	}
	if err := d.takeFrameBuffers(); err != nil {
		t.Fatal(err)
	}
	f := &d.frame
	f.d, f.target, f.num, f.denom, f.err = d, tg, 1, 1, nil
	f.clear()
	f.stats.reset()
	rec.Recording().Play(f)
	if n := len(f.steps); n != 1 {
		t.Errorf("%d steps for rects and one image's draws interleaved, want 1", n)
	}
	f.clear()
}

func TestARenderPassCoversTheDamageAlone(t *testing.T) {
	d := openDevice(t)
	tg := newTarget(t, d, 64, 64)
	rec := render.NewRecorder(64, 64, 1, 1)
	prev := rec.PushClipDevice(render.Rect{X: 8, Y: 9, W: 4, H: 5})
	rec.Clear(rec.Rect(), render.RGB(1, 2, 3))
	rec.PopClip(prev)
	if err := d.Render(rec.Recording(), tg); err != nil {
		t.Fatal(err)
	}
	if a := d.args.passBegin.RenderArea; a.Offset.X != 8 || a.Offset.Y != 9 || a.Extent.Width != 4 || a.Extent.Height != 5 {
		t.Errorf("a frame's render area is %+v", a)
	}
	damage := []image.Rectangle{image.Rect(3, 4, 7, 8), image.Rect(20, 30, 21, 31)}
	runPass(t, d, tg, damage, func(p *Pass) { p.Clear(color.RGBA{A: 0xff}) })
	if a := d.args.passBegin.RenderArea; a.Offset.X != 3 || a.Offset.Y != 4 || a.Extent.Width != 18 || a.Extent.Height != 27 {
		t.Errorf("a pass's render area is %+v", a)
	}
}

func TestATextureOutsideTheDamageGetsNoBarrier(t *testing.T) {
	d := openDevice(t)
	tg := newTarget(t, d, 64, 64)
	src := newTarget(t, d, 8, 8)
	runPass(t, d, src, []image.Rectangle{{Max: src.Size()}}, func(p *Pass) { p.Clear(color.RGBA{A: 0xff}) })
	tex, err := src.Texture()
	if err != nil {
		t.Fatal(err)
	}
	before := src.s.layout
	runPass(t, d, tg, []image.Rectangle{image.Rect(40, 40, 48, 48)}, func(p *Pass) {
		p.DrawTexture(tex, FRect{W: 8, H: 8}, image.Rect(0, 0, 8, 8), TransformNormal, 1, Mask{})
	})
	if src.s.layout != before {
		t.Errorf("a texture drawn outside the damage moved from layout %d to %d", before, src.s.layout)
	}
}
