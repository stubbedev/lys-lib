package render

import (
	"bytes"
	"image"
	"image/color"
	"reflect"
	"testing"
)

// listScene paints one frame using every recordable call. kept is a
// layer held across frames: made in the first frame, composited again
// in the second without being repainted.
type listScene struct {
	face   *Typeface
	icon   *Icon
	img    *image.RGBA
	raster *Canvas
	kept   *Layer
	frame  int
}

func newListScene(t *testing.T) *listScene {
	t.Helper()
	face, err := NewFixtureTypeface()
	if err != nil {
		t.Fatal(err)
	}
	img := image.NewRGBA(image.Rect(0, 0, 6, 5))
	for y := range 5 {
		for x := range 6 {
			img.SetRGBA(x, y, color.RGBA{R: uint8(40 * x), G: uint8(50 * y), B: 90, A: 255})
		}
	}
	img.SetRGBA(2, 2, color.RGBA{R: 20, A: 128})
	icon, err := IconFromImage(img, 12, 10)
	if err != nil {
		t.Fatal(err)
	}
	raster := New(make([]byte, Stride(8)*8), Stride(8), 8, 8)
	raster.RoundedRect(raster.Rect(), 3, RGBA(200, 30, 60, 200))
	return &listScene{face: face, icon: icon, img: img, raster: raster}
}

func (s *listScene) paint(cv *Canvas) {
	s.frame++
	cv.Clear(cv.Rect(), RGBA(16, 18, 24, 255))
	red, blue := RGBA(220, 40, 50, 255), RGBA(30, 90, 220, 180)
	cv.FillRect(Rect{X: 2, Y: 2, W: 30, H: 12}, red)
	cv.BorderRect(Rect{X: 34, Y: 2, W: 20, H: 12}, 2, blue)
	cv.RoundedRect(Rect{X: 56, Y: 2, W: 24, H: 14}, 5, blue)
	cv.Line(2, 18, 40, 30, 2, red)
	cv.Shadow(Rect{X: 44, Y: 20, W: 20, H: 10}, 4, 3, RGBA(0, 0, 0, 120))
	cv.DrawImage(s.img, 70, 20)
	cv.DrawImageCover(s.img, Rect{X: 82, Y: 2, W: 20, H: 16}, UniformCorners(4))
	cv.DrawImageDevice(s.img, 104, 2)
	cv.DrawImageRect(s.img, Rect{X: 112, Y: 2, W: 14, H: 9})
	s.icon.DrawRotated(cv, 104, 12, 30)
	cv.Arc(20, 50, 10, 3, 20, 250, red)
	cv.BoxShadow(Rect{X: 36, Y: 40, W: 24, H: 16}, UniformCorners(4), BoxShadow{X: 2, Y: 3, Blur: 6, Spread: 1, Color: RGBA(0, 0, 0, 160)})
	cv.PaintGrain(Rect{X: 64, Y: 40, W: 20, H: 16}, UniformCorners(3), 0.4, 3, 5)
	cv.RoundedBorderSides(Rect{X: 88, Y: 40, W: 24, H: 16}, Corners{6, 2, 6, 0}, Insets{2, 3, 1, 4}, [4]Color{red, blue, red, blue})
	cv.RoundedRectCorners(Rect{X: 116, Y: 40, W: 12, H: 16}, Corners{0, 6, 2, 4}, blue)
	g := Linear(70, GradientStop{Pos: 0, Color: red}, GradientStop{Pos: 1, Color: blue})
	cv.PaintGradient(Rect{X: 2, Y: 64, W: 30, H: 14}, UniformCorners(4), g)
	cv.PaintGradientTiles(Radial(GradientStop{Color: red}, GradientStop{Pos: 1}), Tile{W: 7, H: 6, RepeatX: true, RepeatY: true}, Rect{X: 34, Y: 64, W: 28, H: 14}, UniformCorners(3))
	cv.PaintImageTiles(s.img, Tile{X: 1, W: 5, H: 4, GapX: 1, RepeatX: true, RepeatY: true}, Rect{X: 64, Y: 64, W: 28, H: 14}, Corners{})
	p := &Path{}
	p.MoveTo(96, 64)
	p.LineTo(126, 70)
	p.LineTo(100, 78)
	p.Close()
	cv.FillPath(p, red)
	cv.DrawCanvasDevice(s.raster, 120, 20)
	s.face.DrawAligned(cv, "Ag fx", Rect{X: 2, Y: 82, W: 60, H: 18}, 13, RGBA(240, 240, 240, 255), AlignStart)

	prev := cv.PushAlpha(0.7)
	bprev := cv.PushBrightness(1.3)
	clip := cv.PushClip(Rect{X: 64, Y: 80, W: 60, H: 40})
	region := cv.MapRect(Rect{X: 64, Y: 82, W: 60, H: 36})
	if s.frame == 1 {
		s.kept = cv.Layer(s.kept, region)
		kc := s.kept.Canvas()
		kc.RoundedRect(Rect{X: 66, Y: 84, W: 30, H: 20}, 6, red)
		s.face.DrawAligned(kc, "kept", Rect{X: 68, Y: 86, W: 40, H: 16}, 11, RGBA(255, 255, 255, 255), AlignStart)
	}
	cv.Composite(s.kept, region, Rotate(8).About(90, 100), 0.8)
	cv.CompositeProjective(s.kept, region, Perspective4(200).Mul(RotateY4(25)).Plane3(), 0.6)
	cv.PopClip(clip)
	cv.PopBrightness(bprev)
	cv.PopAlpha(prev)

	area := cv.MapRect(Rect{X: 2, Y: 100, W: 60, H: 30})
	snap := cv.ScratchSnapshot(area)
	a := cv.ScratchLayer(area)
	a.Canvas().DrawCanvasDevice(snap.Canvas(), 0, 0)
	b := cv.ScratchLayer(area)
	b.Canvas().FillRect(Rect{X: 2, Y: 100, W: 40, H: 30}, blue)
	a.CrossFade(b, area, 0.35)
	mask := cv.ScratchLayer(area)
	mask.Canvas().RoundedRect(Rect{X: 6, Y: 104, W: 50, H: 22}, 8, RGBA(255, 255, 255, 200))
	a.ApplyMaskLayer(mask, area, s.frame == 2)
	mp := &Path{}
	RoundedRectPath(mp, Rect{X: 4, Y: 102, W: 54, H: 26}, UniformCorners(9))
	a.ApplyPathMask(mp, area)
	var f Filters
	f.Append(FilterOp{Kind: FilterBlur, Amount: 2})
	f.Append(FilterOp{Kind: FilterSepia, Amount: 0.6})
	ApplyFilters(a, area, &f, FilterShape{Box: Rect{X: 2, Y: 100, W: 60, H: 30}, Radii: UniformCorners(4)})
	cv.DrawLayer(a, area, BlendMultiply)
	mask.Release()
	b.Release()
	a.Release()
	snap.Release()
}

// TestListPaintMatchesDirect pins the display list's contract: a frame
// recorded and rasterized gives the bytes painting it directly gives,
// at integer and fractional scales and for a layer kept across frames.
func TestListPaintMatchesDirect(t *testing.T) {
	for _, sc := range [][2]int{{1, 1}, {2, 1}, {3, 2}} {
		const lw, lh = 130, 132
		w, h := lw*sc[0]/sc[1], lh*sc[0]/sc[1]
		direct, recorded := newListScene(t), newListScene(t)
		rec := NewRecorder(w, h, sc[0], sc[1])
		seen := map[opKind]bool{}
		for frame := 1; frame <= 2; frame++ {
			want := make([]byte, Stride(w)*h)
			direct.paint(NewScaled(want, Stride(w), w, h, sc[0], sc[1]))
			rec.ResetRecorder(w, h, sc[0], sc[1])
			recorded.paint(rec)
			for _, o := range rec.Recording().ops {
				seen[o.kind] = true
			}
			got := make([]byte, Stride(w)*h)
			rec.Recording().Paint(NewScaled(got, Stride(w), w, h, sc[0], sc[1]))
			if !bytes.Equal(want, got) {
				t.Fatalf("scale %d/%d frame %d: replay differs from direct painting at %d pixels", sc[0], sc[1], frame, diffBytes(want, got))
			}
		}
		for k := range numOpKinds {
			if !seen[k] {
				t.Errorf("scale %d/%d: the scene never records op kind %d", sc[0], sc[1], k)
			}
		}
	}
}

// TestListBoundsHoldEveryPixel pins State.Bounds: replayed one call at
// a time, no call changes a pixel outside its bounds.
func TestListBoundsHoldEveryPixel(t *testing.T) {
	for _, sc := range [][2]int{{1, 1}, {3, 2}} {
		const lw, lh = 130, 132
		w, h := lw*sc[0]/sc[1], lh*sc[0]/sc[1]
		scene := newListScene(t)
		rec := NewRecorder(w, h, sc[0], sc[1])
		dst := NewScaled(make([]byte, Stride(w)*h), Stride(w), w, h, sc[0], sc[1])
		for range 2 {
			rec.ResetRecorder(w, h, sc[0], sc[1])
			scene.paint(rec)
			l := rec.Recording()
			for i := range l.ops {
				o := &l.ops[i]
				target := dst
				if o.state.Layer != nil && o.state.Layer.backing != nil {
					target = rasterOf(o.state.Layer).cv
				}
				before := bytes.Clone(target.data)
				(&List{ops: l.ops[i : i+1]}).Play(rasterPlayer{dst: dst})
				if o.kind >= opLayer && o.kind != opComposite && o.kind != opProjective && o.kind != opDrawLayer {
					continue
				}
				for y := range target.h {
					for x := range target.w {
						at := y*target.stride + x*4
						if !bytes.Equal(before[at:at+4], target.data[at:at+4]) && !o.state.Bounds.Contains(x, y) {
							t.Fatalf("scale %d/%d: op %d (kind %d) changed (%d, %d) outside its bounds %v", sc[0], sc[1], i, o.kind, x, y, o.state.Bounds)
						}
					}
				}
			}
		}
	}
}

// TestRecordedScratchLayersReuse pins that a released recorded scratch
// layer serves the next ScratchLayer instead of a new one.
func TestRecordedScratchLayersReuse(t *testing.T) {
	rec := NewRecorder(10, 10, 1, 1)
	a := rec.ScratchLayer(rec.Rect())
	a.Release()
	if b := rec.ScratchLayer(rec.Rect()); b != a {
		t.Fatal("ScratchLayer after Release made a new recorded layer")
	}
}

// TestKeepGradientKeepsEveryField pins that a recorded gradient
// carries every field of the one painted, a new field included.
func TestKeepGradientKeepsEveryField(t *testing.T) {
	g := Gradient{Stops: []GradientStop{{Pos: 0.25, Color: RGBA(1, 2, 3, 4)}}}
	v := reflect.ValueOf(&g).Elem()
	for i := range v.NumField() {
		f := v.Field(i)
		switch f.Kind() {
		case reflect.Slice:
		case reflect.Bool:
			f.SetBool(true)
		case reflect.Float64:
			f.SetFloat(float64(i) + 0.5)
		case reflect.Uint8:
			f.SetUint(uint64(i + 1))
		default:
			t.Fatalf("Gradient field %s has kind %s the test does not fill", v.Type().Field(i).Name, f.Kind())
		}
	}
	rec := NewRecorder(4, 4, 1, 1)
	if got := rec.keepGradient(&g); !reflect.DeepEqual(got, g) {
		t.Fatalf("keepGradient(%+v) = %+v", g, got)
	}
}

func diffBytes(a, b []byte) int {
	n := 0
	for i := 0; i+4 <= len(a); i += 4 {
		if !bytes.Equal(a[i:i+4], b[i:i+4]) {
			n++
		}
	}
	return n
}
