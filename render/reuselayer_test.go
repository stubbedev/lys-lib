package render

import (
	"bytes"
	"testing"
)

func reusedScene(cv *Canvas, kept **Layer) bool {
	cv.Clear(cv.Rect(), RGBA(16, 18, 24, 255))
	region := cv.MapRect(Rect{X: 4, Y: 4, W: 40, H: 30})
	reused := cv.ReuseLayer(*kept, region)
	if !reused {
		*kept = cv.Layer(*kept, region)
		kc := (*kept).Canvas()
		kc.RoundedRect(Rect{X: 6, Y: 6, W: 30, H: 20}, 6, RGBA(220, 40, 50, 255))
		kc.FillRect(Rect{X: 20, Y: 14, W: 20, H: 16}, RGBA(30, 90, 220, 180))
	}
	cv.Composite(*kept, region, Rotate(9).About(24, 20), 0.8)
	area := cv.MapRect(Rect{X: 2, Y: 30, W: 56, H: 28})
	s := cv.ScratchLayer(area)
	s.Canvas().Composite(*kept, region, Translate(0, 26*float64(cv.num)/float64(cv.denom)), 1)
	cv.DrawLayer(s, cv.MapRect(Rect{X: 2, Y: 40, W: 56, H: 10}), BlendNormal)
	s.Release()
	return reused
}

func TestALayerReusedInTheNextListCompositesAsFirstRecorded(t *testing.T) {
	for _, sc := range [][2]int{{1, 1}, {3, 2}} {
		w, h := 60*sc[0]/sc[1], 60*sc[0]/sc[1]
		rec := NewRecorder(w, h, sc[0], sc[1])
		var kept, direct *Layer
		var frames [2][]byte
		for frame := range frames {
			rec.ResetRecorder(w, h, sc[0], sc[1])
			reused := reusedScene(rec, &kept)
			if reused != (frame == 1) {
				t.Fatalf("scale %d/%d frame %d: ReuseLayer = %v", sc[0], sc[1], frame, reused)
			}
			for _, o := range rec.Recording().ops {
				if o.kind == opLayer && reused && rec.Recording().args[o.args].layer == kept {
					t.Errorf("scale %d/%d: the reused layer was recorded again", sc[0], sc[1])
				}
				if o.kind == opComposite && o.skip {
					t.Errorf("scale %d/%d frame %d: a composite was culled", sc[0], sc[1], frame)
				}
			}
			frames[frame] = make([]byte, Stride(w)*h)
			rec.Recording().Paint(NewScaled(frames[frame], Stride(w), w, h, sc[0], sc[1]))
			want := make([]byte, Stride(w)*h)
			if reusedScene(NewScaled(want, Stride(w), w, h, sc[0], sc[1]), &direct) != (frame == 1) {
				t.Fatalf("scale %d/%d frame %d: the raster ReuseLayer disagrees", sc[0], sc[1], frame)
			}
			if !bytes.Equal(want, frames[frame]) {
				t.Errorf("scale %d/%d frame %d: the List differs from direct painting at %d pixels", sc[0], sc[1], frame, diffBytes(want, frames[frame]))
			}
		}
		if !bytes.Equal(frames[0], frames[1]) {
			t.Errorf("scale %d/%d: the reused layer composites differently at %d pixels", sc[0], sc[1], diffBytes(frames[0], frames[1]))
		}
	}
}

func TestReuseLayerOnARasterCanvasNeedsTheSameLiveLayer(t *testing.T) {
	c := layerCanvas(8, 8, RGB(0, 0, 0))
	region := Rect{X: 1, Y: 1, W: 4, H: 4}
	if c.ReuseLayer(nil, region) {
		t.Error("reused no layer")
	}
	l := c.Layer(nil, region)
	l.Canvas().FillRect(Rect{W: 8, H: 8}, RGB(255, 0, 0))
	l.Canvas().PushAlpha(0.5)
	if !c.ReuseLayer(l, Rect{X: 2, Y: 2, W: 2, H: 2}) {
		t.Fatal("a live layer of the canvas's geometry was not reused")
	}
	if got := l.Canvas().get(1, 1); got != RGB(255, 0, 0) {
		t.Errorf("reuse cleared the layer: (1,1) = %v", got)
	}
	if lc := l.Canvas(); lc.alpha != 1 || lc.clip != (Rect{X: 2, Y: 2, W: 2, H: 2}) {
		t.Errorf("the reused layer's canvas has alpha %v, clip %v", lc.alpha, lc.clip)
	}
	if layerCanvas(9, 8, 0).ReuseLayer(l, region) {
		t.Error("a layer of another size was reused")
	}
	if NewScaled(make([]byte, Stride(8)*8), Stride(8), 8, 8, 2, 1).ReuseLayer(l, region) {
		t.Error("a layer of another scale was reused")
	}
	if New(make([]byte, Stride(9)*8), Stride(9), 8, 8).ReuseLayer(l, region) {
		t.Error("a layer of another stride was reused")
	}
	s := c.ScratchLayer(region)
	if c.ReuseLayer(s, region) {
		t.Error("a scratch layer was reused")
	}
	s.Release()
	l.Free()
	if c.ReuseLayer(l, region) {
		t.Error("a freed layer was reused")
	}
}

func TestReuseLayerOnARecordingNeedsAPlayedBacking(t *testing.T) {
	const w, h = 16, 16
	region := Rect{X: 2, Y: 2, W: 8, H: 8}
	rec := NewRecorder(w, h, 1, 1)
	play := func() {
		rec.Recording().Paint(New(make([]byte, Stride(w)*h), Stride(w), w, h))
	}
	l := rec.Layer(nil, region)
	if rec.ReuseLayer(l, region) {
		t.Error("a layer no List has played was reused")
	}
	play()
	rec.ResetRecorder(w, h, 1, 1)
	if !rec.ReuseLayer(l, region) {
		t.Fatal("a played layer was not reused")
	}
	if n := rec.Recording().Len(); n != 0 {
		t.Errorf("reuse recorded %d calls", n)
	}
	rec.ResetRecorder(w, h, 2, 1)
	if rec.ReuseLayer(l, region) {
		t.Error("a layer played at another scale was reused")
	}
	rec.ResetRecorder(w+1, h, 1, 1)
	if rec.ReuseLayer(l, region) {
		t.Error("a layer played at another size was reused")
	}
	rec.ResetRecorder(w, h, 1, 1)
	l = rec.Layer(l, region)
	rec.ResetRecorder(w, h, 1, 1)
	if rec.ReuseLayer(l, region) {
		t.Error("a layer recorded again but not played was reused")
	}
	l = rec.Layer(l, region)
	play()
	backing := rasterOf(l)
	l.Free()
	if backing.buf != nil || l.Backing() != nil {
		t.Error("Free kept the recorded layer's raster backing")
	}
	rec.ResetRecorder(w, h, 1, 1)
	if rec.ReuseLayer(l, region) {
		t.Error("a freed layer was reused")
	}
	if New(make([]byte, Stride(w)*h), Stride(w), w, h).ReuseLayer(l, region) {
		t.Error("a raster canvas reused a recorded layer")
	}
}
