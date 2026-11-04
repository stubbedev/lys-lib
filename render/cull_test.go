package render

import (
	"bytes"
	"testing"
)

type countingPlayer struct {
	Player
	calls int
}

func (p *countingPlayer) RoundedRect(s State, r Rect, radius int, col Color) {
	p.calls++
	p.Player.RoundedRect(s, r, radius, col)
}

func effectScene(cv *Canvas, region Rect, fl *Filters, path *Path) {
	cv.Clear(cv.Rect(), RGB(200, 210, 220))
	l := cv.ScratchLayer(region)
	for i := range 40 {
		l.Canvas().RoundedRect(Rect{X: region.X + 4 + (i%8)*40, Y: region.Y + 4 + (i/8)*40, W: 32, H: 32}, 4, RGBA(uint8(i*6), 80, uint8(200-i*4), 240))
	}
	if fl != nil {
		ApplyFilters(l, region, fl, FilterShape{Box: region})
	}
	if path != nil {
		l.ApplyPathMask(path, region)
	}
	cv.DrawLayer(l, region, BlendNormal)
	l.Release()
}

func TestAnEffectLayerPaintsOnlyWhatTheDamageReads(t *testing.T) {
	const w, h = 400, 300
	region := Rect{X: 10, Y: 10, W: 360, H: 270}
	damage := Rect{X: 137, Y: 91, W: 9, H: 7}
	var rrect Path
	RoundedRectPath(&rrect, region, Corners{TopLeft: 24, TopRight: 24, BottomLeft: 24, BottomRight: 24})
	filters := map[string]func() *Filters{
		"none": func() *Filters { return nil },
		"blur small": func() *Filters {
			var f Filters
			f.Append(FilterOp{Kind: FilterBlur, Amount: 3})
			return &f
		},
		"blur downsampled": func() *Filters {
			var f Filters
			f.Append(FilterOp{Kind: FilterBlur, Amount: 26})
			return &f
		},
		"blur and shadow": func() *Filters {
			var f Filters
			f.Append(FilterOp{Kind: FilterBlur, Amount: 14})
			f.Append(FilterOp{Kind: FilterDropShadow, Shadow: DropShadow{DX: 3, DY: 5, Blur: 6, Color: RGBA(0, 0, 0, 120)}})
			return &f
		},
	}
	for name, fl := range filters {
		for _, path := range []*Path{nil, &rrect} {
			full := New(make([]byte, Stride(w)*h), Stride(w), w, h)
			rec := NewRecorder(w, h, 1, 1)
			effectScene(rec, region, fl(), path)
			rec.Recording().Paint(full)
			rec.Recording().End()

			damaged := New(make([]byte, Stride(w)*h), Stride(w), w, h)
			rec = NewRecorder(w, h, 1, 1)
			prev := rec.PushClipDevice(damage)
			effectScene(rec, region, fl(), path)
			rec.PopClip(prev)
			rec.Recording().Paint(damaged)
			rec.Recording().End()

			for y := damage.Y; y < damage.Y+damage.H; y++ {
				a := full.data[y*Stride(w)+damage.X*4 : y*Stride(w)+(damage.X+damage.W)*4]
				b := damaged.data[y*Stride(w)+damage.X*4 : y*Stride(w)+(damage.X+damage.W)*4]
				if !bytes.Equal(a, b) {
					t.Errorf("%s (mask %v): row %d of the damage differs from the full frame", name, path != nil, y)
					break
				}
			}
		}
	}
}

func TestDrawsInAScratchLayerOutsideWhatIsReadAreNotPlayed(t *testing.T) {
	const w, h = 400, 300
	region := Rect{X: 10, Y: 10, W: 360, H: 270}
	rec := NewRecorder(w, h, 1, 1)
	defer rec.Recording().End()
	prev := rec.PushClipDevice(Rect{X: 137, Y: 91, W: 9, H: 7})
	effectScene(rec, region, nil, nil)
	rec.PopClip(prev)
	raster := New(make([]byte, Stride(w)*h), Stride(w), w, h)
	p := &countingPlayer{Player: rasterPlayer{dst: raster}}
	rec.Recording().Play(p)
	if p.calls > 2 {
		t.Errorf("%d of 40 layer draws played under a damage one tile wide, want at most 2", p.calls)
	}
}
