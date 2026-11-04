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
		on := l.states[o.state].layer
		if o.kind == opGlyph {
			l.cullDraw(o, on)
			continue
		}
		a := &l.args[o.args]
		switch o.kind {
		case opRelease:
			a.layer.needing, a.layer.need = true, Rect{}
		case opLayer:
		case opScratch:
			a.r = a.layer.wanted().Intersect(a.layer.cv.Rect())
			a.layer.needing = false
		case opSnapshot:
			a.r = a.r.Intersect(a.layer.wanted())
			o.skip = a.r.Empty()
			on.want(a.r)
		case opComposite, opProjective:
			o.bounds = o.bounds.Intersect(on.wanted())
			o.skip = o.bounds.Empty()
			if !o.skip {
				a.layer.want(a.r.Intersect(a.layer.cv.Rect()))
			}
		case opLayerAt, opDrawLayer:
			o.bounds = o.bounds.Intersect(on.wanted())
			o.skip = o.bounds.Empty()
			a.layer.want(o.bounds)
		case opCrossFade, opMaskLayer:
			a.r = a.r.Intersect(a.layer.wanted())
			o.skip = a.r.Empty()
			a.other.want(a.r)
		case opPathMask:
			a.r = a.r.Intersect(a.layer.wanted())
			o.skip = a.r.Empty()
		case opFilters:
			region := a.r.Intersect(a.layer.cv.Rect())
			reach, align := a.filt.reach(a.layer.cv.num, a.layer.cv.denom)
			a.r = filterRegion(a.layer.wanted(), region, reach, align)
			o.skip = a.r.Empty()
			a.layer.want(a.r)
		default:
			l.cullDraw(o, on)
		}
	}
}

// cullDraw trims a drawing call on layer on to what is read of it, its clip
// with it: the clip narrows in a state of its own.
func (l *List) cullDraw(o *op, on *Layer) {
	need := on.wanted()
	o.bounds = o.bounds.Intersect(need)
	o.skip = o.bounds.Empty()
	st := l.states[o.state]
	if clip := st.clip.Intersect(need); clip != st.clip {
		st.clip = clip
		l.states = append(l.states, st)
		o.state = index(len(l.states) - 1)
	}
}
