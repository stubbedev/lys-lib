package render

import "math"

// The CSS filter effects: the color-matrix family (brightness,
// contrast, grayscale, hue-rotate, invert, opacity, saturate, sepia)
// composed into one 5x4 run, blur as a separable three-pass box
// convolution, and drop-shadow as a blurred, tinted silhouette
// composited under the layer's own pixels. Everything applies to a
// Layer's device region in place, premultiplied pixels un-straightened
// only inside the matrix multiply.

// FilterKind names one filter function.
type FilterKind uint8

// The filter functions.
const (
	FilterBlur FilterKind = iota
	FilterBrightness
	FilterContrast
	FilterGrayscale
	FilterHueRotate
	FilterInvert
	FilterOpacity
	FilterSaturate
	FilterSepia
	FilterDropShadow
)

// DropShadow is one drop-shadow() shadow: the offset in device
// pixels, the blur radius, and the tint.
type DropShadow struct {
	DX, DY, Blur float64
	Color        Color
}

// FilterOp is one parsed filter function: its kind and amount — the
// blur radius, the brightness/contrast/opacity/saturate/grayscale/
// invert/sepia factor, or the hue-rotate degrees; Shadow carries
// drop-shadow()'s geometry.
type FilterOp struct {
	Kind   FilterKind
	Amount float64
	Shadow DropShadow
}

// MaxFilters bounds a filter list; later functions past it are
// dropped with a parse warning.
const MaxFilters = 8

// Filters is a fixed-capacity filter list, comparable so a restyle
// can diff it.
type Filters struct {
	N   int
	Ops [MaxFilters]FilterOp
}

// Append adds op, ignoring the overflow past MaxFilters.
func (f *Filters) Append(op FilterOp) {
	if f.N < MaxFilters {
		f.Ops[f.N] = op
		f.N++
	}
}

// Empty reports a list that filters nothing.
func (f *Filters) Empty() bool { return f.N == 0 }

// BrightnessOnly reports a list made solely of brightness() factors
// and their product: the PushBrightness fast path paints those
// without an offscreen layer.
func (f *Filters) BrightnessOnly() (float64, bool) {
	b := 1.0
	for i := range f.N {
		if f.Ops[i].Kind != FilterBrightness {
			return 0, false
		}
		b *= f.Ops[i].Amount
	}
	return b, f.N > 0
}

// Extent is how far the filters paint outside the box they apply to:
// blur's 3-sigma skirt and every drop-shadow's offset, skirt and
// spread. Damage rects and effect layers grow by it.
func (f *Filters) Extent() Insets {
	var out Insets
	for i := range f.N {
		op := &f.Ops[i]
		switch op.Kind {
		case FilterBlur:
			r := int(math.Ceil(op.Amount * 1.5))
			out.Top, out.Right = max(out.Top, r), max(out.Right, r)
			out.Bottom, out.Left = max(out.Bottom, r), max(out.Left, r)
		case FilterDropShadow:
			s := op.Shadow
			r := int(math.Ceil(s.Blur * 1.5))
			out.Top = max(out.Top, r-int(s.DY))
			out.Bottom = max(out.Bottom, r+int(s.DY))
			out.Left = max(out.Left, r-int(s.DX))
			out.Right = max(out.Right, r+int(s.DX))
		}
	}
	return out
}

// colorMatrix is a 5x4 filter color matrix in row-major order: four
// rows of R, G, B, A contributors plus the offset column.
type colorMatrix [20]float64

// matrixIdentity is the identity color matrix.
var matrixIdentity = colorMatrix{
	1, 0, 0, 0, 0,
	0, 1, 0, 0, 0,
	0, 0, 1, 0, 0,
	0, 0, 0, 1, 0,
}

// apply maps one straight color through the matrix.
func (m colorMatrix) apply(c [4]byte) [4]byte {
	var out [4]byte
	for r := range 4 {
		v := m[r*5+4]
		for k := range 4 {
			v += m[r*5+k] * float64(c[k])
		}
		out[r] = uint8(min(255, max(0, math.Round(v))))
	}
	return out
}

// filterMatrix builds one function's color matrix; Amount's meaning
// follows the kind. The formulas are the filter-effects-1 spec's.
func filterMatrix(kind FilterKind, amount float64) colorMatrix {
	m := matrixIdentity
	set := func(rows [3][5]float64) {
		copy(m[0:], rows[0][:])
		copy(m[5:], rows[1][:])
		copy(m[10:], rows[2][:])
	}
	switch kind {
	case FilterBrightness:
		set([3][5]float64{
			{amount, 0, 0, 0, 0},
			{0, amount, 0, 0, 0},
			{0, 0, amount, 0, 0},
		})
	case FilterContrast:
		set([3][5]float64{
			{amount, 0, 0, 0, 255 * (1 - amount) / 2},
			{0, amount, 0, 0, 255 * (1 - amount) / 2},
			{0, 0, amount, 0, 255 * (1 - amount) / 2},
		})
	case FilterGrayscale:
		a := 1 - amount
		set([3][5]float64{
			{0.2126 + 0.7874*a, 0.7152 - 0.7152*a, 0.0722 - 0.0722*a, 0, 0},
			{0.2126 - 0.2126*a, 0.7152 + 0.2848*a, 0.0722 - 0.0722*a, 0, 0},
			{0.2126 - 0.2126*a, 0.7152 - 0.7152*a, 0.0722 + 0.9278*a, 0, 0},
		})
	case FilterSaturate:
		set([3][5]float64{
			{0.213 + 0.787*amount, 0.715 - 0.715*amount, 0.072 - 0.072*amount, 0, 0},
			{0.213 - 0.213*amount, 0.715 + 0.285*amount, 0.072 - 0.072*amount, 0, 0},
			{0.213 - 0.213*amount, 0.715 - 0.715*amount, 0.072 + 0.928*amount, 0, 0},
		})
	case FilterSepia:
		a := 1 - amount
		set([3][5]float64{
			{0.393 + 0.607*a, 0.769 - 0.769*a, 0.189 - 0.189*a, 0, 0},
			{0.349 - 0.349*a, 0.686 + 0.314*a, 0.168 - 0.168*a, 0, 0},
			{0.272 - 0.272*a, 0.534 - 0.534*a, 0.131 + 0.869*a, 0, 0},
		})
	case FilterHueRotate:
		s, c := math.Sincos(amount * math.Pi / 180)
		set([3][5]float64{
			{0.213 + c*0.787 - s*0.213, 0.715 - c*0.715 - s*0.715, 0.072 - c*0.072 + s*0.928, 0, 0},
			{0.213 - c*0.213 + s*0.143, 0.715 + c*0.285 + s*0.140, 0.072 - c*0.072 - s*0.283, 0, 0},
			{0.213 - c*0.213 - s*0.787, 0.715 - c*0.715 + s*0.715, 0.072 + c*0.928 + s*0.072, 0, 0},
		})
	case FilterInvert:
		i := amount
		set([3][5]float64{
			{1 - 2*i, 0, 0, 0, 255 * i},
			{0, 1 - 2*i, 0, 0, 255 * i},
			{0, 0, 1 - 2*i, 0, 255 * i},
		})
	case FilterOpacity:
		m[18] = amount
	}
	return m
}

// ApplyFilters applies the list to l's device region in place. The
// region must already cover every op's Extent; blur and drop-shadow
// read transparent outside it.
func ApplyFilters(l *Layer, region Rect, f *Filters) {
	if l == nil || f.Empty() {
		return
	}
	region = region.Intersect(l.cv.Rect())
	if region.Empty() {
		return
	}
	for i := range f.N {
		op := &f.Ops[i]
		switch op.Kind {
		case FilterBlur:
			blurRegion(l, region, op.Amount)
		case FilterDropShadow:
			dropShadowRegion(l, region, &op.Shadow)
		default:
			// Each color function applies on its own: the spec clamps
			// every function's output, and composing the matrices would
			// let a saturating function pass its overshoot to the next.
			applyMatrixRegion(l, region, filterMatrix(op.Kind, op.Amount))
		}
	}
}

// applyMatrixRegion maps region's pixels through the color matrix,
// un-premultiplying before and re-premultiplying after.
func applyMatrixRegion(l *Layer, region Rect, m colorMatrix) {
	if m == matrixIdentity {
		return
	}
	for y := region.Y; y < region.Y+region.H; y++ {
		for x := region.X; x < region.X+region.W; x++ {
			col := l.cv.get(x, y)
			if col == 0 {
				continue
			}
			out := m.apply(col.Straight())
			// Re-premultiply: the straight channels scale by the new
			// alpha, the opacity() case's own move.
			pm := float64(out[3]) / 255
			l.cv.set(x, y, Color(uint32(out[3])<<24|
				clamp8(math.Round(float64(out[0])*pm))<<16|
				clamp8(math.Round(float64(out[1])*pm))<<8|
				clamp8(math.Round(float64(out[2])*pm))))
		}
	}
}

// blurBoxes returns the three integer box radii whose passes
// approximate a Gaussian of sigma — Kovesi's ideal-box sizes with the
// remainder folded into the last pass.
func blurBoxes(sigma float64) [3]int {
	if sigma <= 0 {
		return [3]int{}
	}
	n := 3.0
	wIdeal := math.Sqrt(12*sigma*sigma/n + 1)
	wl := int(math.Floor(wIdeal))
	if wl%2 == 0 {
		wl--
	}
	if wl < 1 {
		wl = 1
	}
	wu := wl + 2
	mIdeal := (12*sigma*sigma - n*float64(wl*wl) - 4*n*float64(wl) - 3*n) / (-4*float64(wl) - 4)
	m := min(3, max(0, int(math.Round(mIdeal))))
	var out [3]int
	for i := range out {
		size := wl
		if i >= 3-m {
			size = wu
		}
		out[i] = (size - 1) / 2
	}
	return out
}

// blurRegion blurs region in place with a three-pass separable box
// convolution whose sigma is radius/2, CSS blur()'s definition. The
// passes run over premultiplied channels, so translucent edges blur
// without picking up color from nothing; pixels outside the region
// read as transparent.
func blurRegion(l *Layer, region Rect, radius float64) {
	if radius <= 0 {
		return
	}
	boxes := blurBoxes(radius / 2)
	buf := make([]Color, region.W*region.H)
	scratch := make([]Color, region.W*region.H)
	for y := region.Y; y < region.Y+region.H; y++ {
		for x := region.X; x < region.X+region.W; x++ {
			buf[(y-region.Y)*region.W+(x-region.X)] = l.cv.get(x, y)
		}
	}
	for _, r := range boxes {
		if r < 1 {
			continue
		}
		boxPassH(scratch, buf, region, r)
		boxPassV(buf, scratch, region, r)
	}
	for y := region.Y; y < region.Y+region.H; y++ {
		for x := region.X; x < region.X+region.W; x++ {
			l.cv.set(x, y, buf[(y-region.Y)*region.W+(x-region.X)])
		}
	}
}

// boxPassH is one horizontal box pass over integer radius r: dst is
// the running average of src's window, transparent outside.
func boxPassH(dst, src []Color, region Rect, r int) {
	for y := range region.H {
		row := y * region.W
		var acc [4]int64
		for k := -r; k <= r; k++ {
			if k >= 0 && k < region.W {
				addAcc(&acc, src[row+k])
			}
		}
		den := int64(2*r + 1)
		for x := range region.W {
			dst[row+x] = accColor(acc, den)
			out, in := x-r, x+r+1
			if out >= 0 && out < region.W {
				subAcc(&acc, src[row+out])
			}
			if in >= 0 && in < region.W {
				addAcc(&acc, src[row+in])
			}
		}
	}
}

// boxPassV is boxPassH's vertical twin.
func boxPassV(dst, src []Color, region Rect, r int) {
	for x := range region.W {
		var acc [4]int64
		for k := -r; k <= r; k++ {
			if k >= 0 && k < region.H {
				addAcc(&acc, src[k*region.W+x])
			}
		}
		den := int64(2*r + 1)
		for y := range region.H {
			dst[y*region.W+x] = accColor(acc, den)
			out, in := y-r, y+r+1
			if out >= 0 && out < region.H {
				subAcc(&acc, src[out*region.W+x])
			}
			if in >= 0 && in < region.H {
				addAcc(&acc, src[in*region.W+x])
			}
		}
	}
}

// addAcc adds a color's premultiplied channels to an accumulator.
func addAcc(acc *[4]int64, c Color) {
	acc[0] += int64(c.A())
	acc[1] += int64(c.R())
	acc[2] += int64(c.G())
	acc[3] += int64(c.B())
}

// subAcc subtracts one.
func subAcc(acc *[4]int64, c Color) {
	acc[0] -= int64(c.A())
	acc[1] -= int64(c.R())
	acc[2] -= int64(c.G())
	acc[3] -= int64(c.B())
}

// accColor rounds the averaged channels back to a Color.
func accColor(acc [4]int64, den int64) Color {
	q := func(v int64) uint32 {
		return uint32(min(255, max(0, (v+den/2)/den)))
	}
	return Color(q(acc[0])<<24 | q(acc[1])<<16 | q(acc[2])<<8 | q(acc[3]))
}

// dropShadowRegion composites a blurred, tinted copy of region's
// alpha silhouette at the shadow's offset under region's pixels.
func dropShadowRegion(l *Layer, region Rect, s *DropShadow) {
	w := region.W + int(math.Ceil(s.Blur*1.5))*2 + max(0, int(math.Abs(s.DX)))*2
	h := region.H + int(math.Ceil(s.Blur*1.5))*2 + max(0, int(math.Abs(s.DY)))*2
	if w <= 0 || h <= 0 {
		return
	}
	sil := make([]Color, w*h)
	for y := range region.H {
		for x := range region.W {
			if a := l.cv.get(region.X+x, region.Y+y).A(); a != 0 {
				sil[(y+int(math.Ceil(s.Blur*1.5)))*w+x+int(math.Ceil(s.Blur*1.5))] = Color(uint32(a) << 24)
			}
		}
	}
	tmp := make([]Color, w*h)
	copy(tmp, sil)
	if s.Blur > 0 {
		silRect := Rect{W: w, H: h}
		for _, r := range blurBoxes(s.Blur / 2) {
			if r < 1 {
				continue
			}
			boxPassH(tmp, sil, silRect, r)
			boxPassV(sil, tmp, silRect, r)
		}
	}
	tint := s.Color
	for y := range region.H {
		for x := range region.W {
			dx := x + int(math.Ceil(s.Blur*1.5)) - int(s.DX)
			dy := y + int(math.Ceil(s.Blur*1.5)) - int(s.DY)
			if dx < 0 || dy < 0 || dx >= w || dy >= h {
				continue
			}
			a := uint32(sil[dy*w+dx].A())
			if a == 0 {
				continue
			}
			// The tint is premultiplied like every Color here; scaling its
			// channels by the silhouette's coverage keeps it that way.
			shadow := Color(uint32(tint.A())*a/255<<24 |
				uint32(tint.R())*a/255<<16 |
				uint32(tint.G())*a/255<<8 |
				uint32(tint.B())*a/255)
			l.cv.set(region.X+x, region.Y+y, shadow.over(l.cv.get(region.X+x, region.Y+y)))
		}
	}
}
