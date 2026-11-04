package render

import "math"

var everywhere = Rect{X: -1 << 30, Y: -1 << 30, W: 1 << 31, H: 1 << 31}

func (l *Layer) wanted() Rect {
	if l == nil || !l.needing {
		return everywhere
	}
	return l.need
}

func (l *Layer) want(r Rect) {
	if l != nil && l.needing {
		l.need = l.need.Union(r)
	}
}

// reach is how far a pixel the filters write depends on pixels away from
// it, and the grid a blur's downsampling tiles a region on.
func (f *Filters) reach(num, denom int) (px, align int) {
	scale := float64(num) / float64(denom)
	align = 1
	for i := range f.N {
		op := &f.Ops[i]
		switch op.Kind {
		case FilterBlur:
			if pl, ok := BlurPlanOf(op.Amount * scale); ok {
				px += pl.D * (pl.Boxes[0] + pl.Boxes[1] + pl.Boxes[2] + 3)
				if align%pl.D != 0 {
					align *= pl.D
				}
			}
		case FilterDropShadow:
			pl := ShadowPlanOf(op.Shadow, scale)
			px += max(abs(pl.DX), abs(pl.DY)) + pl.Boxes[0] + pl.Boxes[1] + pl.Boxes[2] + 1
		case FilterRefract:
			px += int(math.Ceil(op.Amount*scale)) + 1
		}
	}
	return px, align
}

func snapOut(lo, hi, base, end, align int) (int, int) {
	lo = base + (lo-base)/align*align
	hi = min(end, base+(hi-base+align-1)/align*align)
	return lo, hi
}

func filterRegion(need, region Rect, reach, align int) Rect {
	in := need.Intersect(region)
	if in.Empty() {
		return Rect{}
	}
	g := Insets{reach, reach, reach, reach}.Grow(in).Intersect(region)
	x0, x1 := snapOut(g.X, g.X+g.W, region.X, region.X+region.W, align)
	y0, y1 := snapOut(g.Y, g.Y+g.H, region.Y, region.Y+region.H, align)
	return Rect{X: x0, Y: y0, W: x1 - x0, H: y1 - y0}
}

func (l *List) cull() {
	if l.culled || !l.scratchLayers {
		return
	}
	l.culled = true
	for i := len(l.ops) - 1; i >= 0; i-- {
		o := &l.ops[i]
		s := &o.state
		switch o.kind {
		case opRelease:
			o.layer.needing, o.layer.need = true, Rect{}
		case opLayer:
		case opScratch:
			o.r = o.layer.wanted().Intersect(o.layer.cv.Rect())
			o.layer.needing = false
		case opSnapshot:
			o.r = o.r.Intersect(o.layer.wanted())
			o.skip = o.r.Empty()
			s.Layer.want(o.r)
		case opComposite, opProjective:
			s.Bounds = s.Bounds.Intersect(s.Layer.wanted())
			o.skip = s.Bounds.Empty()
			if !o.skip {
				o.layer.want(o.r.Intersect(o.layer.cv.Rect()))
			}
		case opLayerAt, opDrawLayer:
			s.Bounds = s.Bounds.Intersect(s.Layer.wanted())
			o.skip = s.Bounds.Empty()
			o.layer.want(s.Bounds)
		case opCrossFade, opMaskLayer:
			o.r = o.r.Intersect(o.layer.wanted())
			o.skip = o.r.Empty()
			o.other.want(o.r)
		case opPathMask:
			o.r = o.r.Intersect(o.layer.wanted())
			o.skip = o.r.Empty()
		case opFilters:
			region := o.r.Intersect(o.layer.cv.Rect())
			reach, align := o.filt.reach(o.layer.cv.num, o.layer.cv.denom)
			o.r = filterRegion(o.layer.wanted(), region, reach, align)
			o.skip = o.r.Empty()
			o.layer.want(o.r)
		default:
			need := s.Layer.wanted()
			s.Bounds, s.Clip = s.Bounds.Intersect(need), s.Clip.Intersect(need)
			o.skip = s.Bounds.Empty()
		}
	}
}
