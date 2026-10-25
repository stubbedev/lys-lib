package render

import (
	"encoding/binary"
	"math"
)

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
	// FilterRefract is lys-ui's -lys-refract(): the backdrop bends
	// through a glass bezel along the shape's edge.
	FilterRefract
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
	// Bezel is -lys-refract()'s bezel width; Amount is then its
	// displacement at the edge. Both CSS pixels.
	Bezel float64
}

// FilterShape is the box a filter list applies to, in logical pixels:
// the border box and its corner radii, what -lys-refract() bends
// along. The zero shape has no edge, so nothing bends.
type FilterShape struct {
	Box   Rect
	Radii Corners
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
		case FilterRefract:
			// The bezel samples up to the displacement outside the box.
			r := int(math.Ceil(op.Amount))
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
// apply maps a straight color through the float32 matrix, each row
// its offset plus the channels' weighted products in order, each
// product rounded on its own, rounded half up and clamped.
func (st *MatrixStep) apply(c [4]byte) [4]byte {
	m := &st.M
	var out [4]byte
	for r := range 4 {
		v := m[r*5+4]
		for k := range 4 {
			v += float32(m[r*5+k] * float32(c[k]))
		}
		out[r] = uint8(min(255, max(0, math.Floor(float64(v+0.5)))))
	}
	return out
}

// premultiply is a straight color premultiplied by its alpha in
// float32, rounding half up, as the GPU's shader computes it.
func premultiply(c [4]byte) Color {
	pm := float32(c[3]) * Inv255
	ch := func(v byte) uint32 {
		return uint32(min(255, max(0, math.Floor(float64(float32(float32(v)*pm)+0.5)))))
	}
	return Color(uint32(c[3])<<24 | ch(c[0])<<16 | ch(c[1])<<8 | ch(c[2]))
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
// ops' lengths are logical (CSS) pixels, scaled here by the layer's
// device scale; shape is the box the list applies to. The region must
// already cover every op's Extent; blur, drop-shadow and refract read
// transparent outside it.
func ApplyFilters(l *Layer, region Rect, f *Filters, shape FilterShape) {
	if l == nil || f.Empty() {
		return
	}
	if l.cv.rec != nil {
		l.cv.record(op{kind: opFilters, layer: l, r: region, filt: f, shape: shape})
		return
	}
	region = region.Intersect(l.cv.Rect())
	if region.Empty() {
		return
	}
	// The lengths are CSS pixels; the layer is device pixels.
	scale := float64(l.cv.num) / float64(l.cv.denom)
	for i := range f.N {
		op := &f.Ops[i]
		switch op.Kind {
		case FilterBlur:
			blurRegion(l, region, op.Amount*scale)
		case FilterDropShadow:
			s := op.Shadow
			s.DX, s.DY, s.Blur = s.DX*scale, s.DY*scale, s.Blur*scale
			dropShadowRegion(l, region, &s)
		case FilterRefract:
			refractRegion(l, region, shape, op.Bezel*scale, op.Amount*scale)
		default:
			// Each color function applies on its own: the spec clamps
			// every function's output, and composing the matrices would
			// let a saturating function pass its overshoot to the next.
			applyMatrixRegion(l, region, stepOf(filterMatrix(op.Kind, op.Amount)))
		}
	}
}

// FilterStep is one function of a filter list as a GPU renderer
// applies it: a color matrix step or a blur.
type FilterStep struct {
	Matrix MatrixStep
	// Blur is the plan of a blur step; IsBlur marks one.
	Blur   BlurPlan
	IsBlur bool
}

// MatrixStep is one color function of a filter list as both renderers
// apply it. A Linear step only mixes the color channels, so it runs on
// the premultiplied pixels in 16.16 fixed point, K row by row, each
// channel clamped to its pixel's alpha; any other runs the float32
// matrix M (4 rows of 4 channel weights and an offset) on the straight
// color and re-premultiplies. An Identity step changes nothing.
type MatrixStep struct {
	Identity, Linear bool
	K                [9]int32
	M                [20]float32
}

// stepOf is m as a MatrixStep.
func stepOf(m colorMatrix) MatrixStep {
	st := MatrixStep{Identity: m == matrixIdentity, Linear: m.colorLinear()}
	for i, v := range m {
		st.M[i] = float32(v)
	}
	for row := range 3 {
		for col := range 3 {
			st.K[row*3+col] = int32(math.Round(m[row*5+col] * 65536))
		}
	}
	return st
}

// Steps is f's functions as filter steps, in order, at device scale
// (device pixels per CSS pixel, scale); false when f holds one a GPU
// does not run (drop-shadow, refract).
func (f *Filters) Steps(scale float64) ([]FilterStep, bool) {
	steps := make([]FilterStep, 0, f.N)
	for i := range f.N {
		op := &f.Ops[i]
		switch op.Kind {
		case FilterDropShadow, FilterRefract:
			return nil, false
		case FilterBlur:
			if pl, ok := BlurPlanOf(op.Amount * scale); ok {
				steps = append(steps, FilterStep{Blur: pl, IsBlur: true})
			}
		default:
			steps = append(steps, FilterStep{Matrix: stepOf(filterMatrix(op.Kind, op.Amount))})
		}
	}
	return steps, true
}

// Inv255 is 1/255 in float32, what re-premultiplying scales by.
var Inv255 = Reciprocal(float32(255))

// applyMatrixRegion maps region's pixels through the color matrix,
// un-premultiplying before and re-premultiplying after. A matrix that
// mixes only the color channels, without offsets, commutes with
// premultiplication, so it runs on the premultiplied pixels directly in
// fixed point (applyLinearRegion): saturate, grayscale, sepia,
// brightness and hue-rotate, the backdrop's usual functions.
func applyMatrixRegion(l *Layer, region Rect, st MatrixStep) {
	if st.Identity {
		return
	}
	if st.Linear {
		applyLinearRegion(l.cv, region, &st.K)
		return
	}
	for y := region.Y; y < region.Y+region.H; y++ {
		for x := region.X; x < region.X+region.W; x++ {
			col := l.cv.get(x, y)
			if col == 0 {
				continue
			}
			// Re-premultiply: the straight channels scale by the new
			// alpha, the opacity() case's own move.
			l.cv.set(x, y, premultiply(st.apply(col.Straight())))
		}
	}
}

// colorLinear reports whether m only mixes the color channels: no
// offsets, no alpha feeding a color, alpha passed through unchanged.
func (m colorMatrix) colorLinear() bool {
	for _, row := range [3]int{0, 5, 10} {
		if m[row+3] != 0 || m[row+4] != 0 {
			return false
		}
	}
	return m[15] == 0 && m[16] == 0 && m[17] == 0 && m[18] == 1 && m[19] == 0
}

// applyLinearRegion applies a colorLinear matrix to region's
// premultiplied pixels in 16.16 fixed point, each channel clamped to
// its pixel's alpha (the premultiplied form of clamping to 255).
func applyLinearRegion(c *Canvas, region Rect, k *[9]int32) {
	ch := func(row int, r, g, b, a int64) uint32 {
		v := (int64(k[row*3])*r + int64(k[row*3+1])*g + int64(k[row*3+2])*b + 32768) >> 16
		return uint32(min(a, max(0, v)))
	}
	for y := range region.H {
		px := c.data[(region.Y+y)*c.stride+region.X*4:]
		for x := range region.W {
			col := Color(binary.LittleEndian.Uint32(px[x*4:]))
			if col == 0 {
				continue
			}
			a, r, g, b := int64(col.A()), int64(col.R()), int64(col.G()), int64(col.B())
			out := uint32(a)<<24 | ch(0, r, g, b, a)<<16 | ch(1, r, g, b, a)<<8 | ch(2, r, g, b, a)
			binary.LittleEndian.PutUint32(px[x*4:], out)
		}
	}
	c.touched += region.W * region.H
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
// read as transparent. A heavy blur (blurScale) runs on an
// area-averaged copy at a fraction of the resolution and is sampled
// back bilinearly: at that radius the difference is below what the
// blur leaves visible, and the passes cost a fraction.
func blurRegion(l *Layer, region Rect, radius float64) {
	pl, ok := BlurPlanOf(radius)
	if !ok {
		return
	}
	n := region.W * region.H
	l.blurA = growColors(l.blurA, n)
	full := l.blurA[:n]
	readRegion(l.cv, region, full)
	if pl.D == 1 {
		l.blurPasses(full, region.W, region.H, pl.Boxes)
		writeRegion(l.cv, region, full)
		return
	}
	sw, sh := (region.W+pl.D-1)/pl.D, (region.H+pl.D-1)/pl.D
	l.blurS = growColors(l.blurS, sw*sh)
	small := l.blurS[:sw*sh]
	downsample(small, full, region.W, region.H, pl.D)
	l.blurPasses(small, sw, sh, pl.Boxes)
	l.upsample(full, small, region.W, region.H, sw, sh, pl.D)
	writeRegion(l.cv, region, full)
}

// BlurPlan is how a blur of a device radius runs: at 1/D resolution
// (D 1 runs at full), area-averaged down by D x D blocks and sampled
// back bilinearly in 1/256 steps, through three separable box passes of
// Boxes' radii (a pass of radius 0 is skipped). Every step is integer
// arithmetic, so a GPU repeats it exactly.
type BlurPlan struct {
	D     int
	Boxes [3]int
}

// BlurPlanOf is the plan for a blur of device radius radius; false for
// none.
func BlurPlanOf(radius float64) (BlurPlan, bool) {
	if radius <= 0 {
		return BlurPlan{}, false
	}
	sigma := radius / 2
	d := blurScale(sigma)
	if d == 1 {
		return BlurPlan{D: 1, Boxes: blurBoxes(sigma)}, true
	}
	// The area average and the bilinear sampling blur too (variances
	// (d²-1)/12 and d²/6 in full-resolution pixels): the passes make
	// up only the rest, so the total stays sigma.
	fd := float64(d)
	rest := math.Max(0, sigma*sigma-(fd*fd-1)/12-fd*fd/6)
	return BlurPlan{D: d, Boxes: blurBoxes(math.Sqrt(rest) / fd)}, true
}

// BoxDivisor is how a box pass of 2r+1 taps, or a downsample block of
// n pixels, divides its channel sums: (v + Half) * Recip >> 32, a
// rounded division a shader repeats with a 32x32-bit multiply's high
// word (Recip fits 32 bits from n 2 on; n 1 divides by nothing).
func BoxDivisor(n int) (half, recip uint32) {
	d := newBoxDiv(n)
	return uint32(d.half), uint32(d.recip)
}

// blurScale is the downsampling factor for a blur of sigma: 1 (exact)
// below sigma 6, then one step per 3 of sigma, at most 4.
func blurScale(sigma float64) int {
	return min(4, max(1, int(sigma/3)))
}

// blurPasses runs the three box passes over a w x h buffer in place.
func (l *Layer) blurPasses(buf []Color, w, h int, boxes [3]int) {
	l.blurB = growColors(l.blurB, w*h)
	scratch := l.blurB[:w*h]
	if cap(l.blurAcc) < 4*w {
		l.blurAcc = make([]uint32, 4*w)
	}
	acc := l.blurAcc[:4*w]
	for _, r := range boxes {
		if r < 1 {
			continue
		}
		boxPassH(scratch, buf, w, h, r)
		boxPassV(buf, scratch, w, h, r, acc)
	}
}

// downsample area-averages src (w x h) into dst by d x d blocks; a
// block cut by the edge averages the pixels it has.
func downsample(dst, src []Color, w, h, d int) {
	sw := (w + d - 1) / d
	for sy := range (h + d - 1) / d {
		for sx := range sw {
			var acc [4]uint32
			n := 0
			for y := sy * d; y < min(h, sy*d+d); y++ {
				for x := sx * d; x < min(w, sx*d+d); x++ {
					addAcc(&acc, src[y*w+x])
					n++
				}
			}
			dst[sy*sw+sx] = newBoxDiv(n).color(&acc)
		}
	}
}

// upCol is one destination column's bilinear source: the left
// source column and the weight of the one right of it, in 1/256.
type upCol struct {
	x0 int
	t  uint32
}

// upsample samples the sw x sh buffer src back over w x h dst
// bilinearly, at each destination pixel's center: per row, the two
// source rows blend once into row, then each column interpolates
// across it.
func (l *Layer) upsample(dst, src []Color, w, h, sw, sh, d int) {
	if cap(l.blurCols) < w {
		l.blurCols = make([]upCol, w)
	}
	cols := l.blurCols[:w]
	for x := range cols {
		x0, t := upSource(x, d)
		cols[x] = upCol{x0: min(sw-1, max(0, x0)), t: t}
		if x0 < 0 {
			cols[x].t = 0
		}
	}
	l.blurRow = growColors(l.blurRow, sw)
	row := l.blurRow[:sw]
	for y := range h {
		y0, ty := upSource(y, d)
		a := src[min(sh-1, max(0, y0))*sw:][:sw]
		b := src[min(sh-1, max(0, y0+1))*sw:][:sw]
		for x := range row {
			row[x] = lerp256(a[x], b[x], ty)
		}
		out := dst[y*w : (y+1)*w]
		for x, c := range cols {
			out[x] = lerp256(row[c.x0], row[min(sw-1, c.x0+1)], c.t)
		}
	}
}

// upSource maps a destination pixel to its source cell at scale d: the
// lower source index and the weight of the next one, in 1/256.
func upSource(i, d int) (int, uint32) {
	// The center of i in source units, less the half pixel to the
	// source centers, in 1/256 fixed point: ((2i+1) - d) * 128 / d.
	f := ((2*i+1)*128 - d*128) / d
	i0 := f >> 8 // arithmetic: floors negative positions too
	return i0, uint32(f - i0*256)
}

// lerp256 interpolates two premultiplied colors at t/256.
func lerp256(a, b Color, t uint32) Color {
	ch := func(s uint) uint32 {
		x, y := uint32(a>>s)&0xff, uint32(b>>s)&0xff
		return (x*(256-t) + y*t + 128) >> 8
	}
	return Color(ch(24)<<24 | ch(16)<<16 | ch(8)<<8 | ch(0))
}

// refractRegion bends region's pixels through a glass bezel along
// shape's edge: inside the shape, within bezel device pixels of the
// edge, each pixel samples the backdrop displaced outward along the
// edge normal, by disp at the edge falling off quadratically to none
// at the bezel's inner side - the rim of a lens, which shows what lies
// just beyond it. Samples are bilinear; past the bezel, and outside
// the shape, pixels stay as they are.
func refractRegion(l *Layer, region Rect, shape FilterShape, bezel, disp float64) {
	if bezel <= 0 || disp <= 0 || shape.Box.Empty() {
		return
	}
	b, rc := l.cv.devBox(shape.Box, shape.Radii)
	n := region.W * region.H
	l.blurA = growColors(l.blurA, n)
	src := l.blurA[:n]
	readRegion(l.cv, region, src)
	at := func(x, y int) Color {
		if x < 0 || y < 0 || x >= region.W || y >= region.H {
			return 0
		}
		return src[y*region.W+x]
	}
	sample := func(fx, fy float64) Color {
		x0, y0 := int(math.Floor(fx)), int(math.Floor(fy))
		tx, ty := uint32((fx-float64(x0))*256), uint32((fy-float64(y0))*256)
		top := lerp256(at(x0, y0), at(x0+1, y0), tx)
		bot := lerp256(at(x0, y0+1), at(x0+1, y0+1), tx)
		return lerp256(top, bot, ty)
	}
	// Deeper than the bezel nothing moves: that run skips the field.
	deep := frect{b.x0 + bezel, b.y0 + bezel, b.x1 - bezel, b.y1 - bezel}
	var drc fcorners
	for i := range rc {
		drc[i] = math.Max(0, rc[i]-bezel)
	}
	deepOK := deep.x1 > deep.x0 && deep.y1 > deep.y0
	rows := region.Intersect(l.cv.span(b))
	for y := rows.Y; y < rows.Y+rows.H; y++ {
		py := float64(y) + 0.5
		xa, xb, skip := solidRun(deep, drc, py)
		skip = skip && deepOK
		out := l.cv.data[y*l.cv.stride:]
		for x := rows.X; x < rows.X+rows.W; x++ {
			px := float64(x) + 0.5
			if skip && px >= xa && px <= xb {
				continue
			}
			d := sdBox(px, py, b, rc)
			if d > 0 || d <= -bezel {
				continue
			}
			nx := sdBox(px+0.5, py, b, rc) - sdBox(px-0.5, py, b, rc)
			ny := sdBox(px, py+0.5, b, rc) - sdBox(px, py-0.5, b, rc)
			norm := math.Hypot(nx, ny)
			if norm == 0 {
				continue
			}
			k := 1 + d/bezel // 1 at the edge, 0 at the bezel's inner side
			m := disp * k * k / norm
			c := sample(px-0.5-float64(region.X)+nx*m, py-0.5-float64(region.Y)+ny*m)
			binary.LittleEndian.PutUint32(out[x*4:], uint32(c))
		}
	}
	l.cv.touched += rows.W * rows.H
}

// growColors returns s with room for n colors, reusing its storage.
func growColors(s []Color, n int) []Color {
	if cap(s) >= n {
		return s[:n]
	}
	return make([]Color, n)
}

// readRegion copies region's pixels row by row into dst.
func readRegion(c *Canvas, region Rect, dst []Color) {
	for y := range region.H {
		row := c.data[(region.Y+y)*c.stride+region.X*4:]
		out := dst[y*region.W : (y+1)*region.W]
		for x := range out {
			out[x] = Color(binary.LittleEndian.Uint32(row[x*4:]))
		}
	}
}

// writeRegion copies src back over region's pixels row by row.
func writeRegion(c *Canvas, region Rect, src []Color) {
	for y := range region.H {
		row := c.data[(region.Y+y)*c.stride+region.X*4:]
		in := src[y*region.W : (y+1)*region.W]
		for x, v := range in {
			binary.LittleEndian.PutUint32(row[x*4:], uint32(v))
		}
	}
	c.touched += region.W * region.H
}

// boxDiv divides a box window's channel sums by its width, rounded, as
// a multiply: recip is 2^32/den rounded up, which is exact for every
// sum a window of 8-bit channels reaches (sum < 2^32/den).
type boxDiv struct{ half, recip uint64 }

func newBoxDiv(den int) boxDiv {
	return boxDiv{half: uint64(den / 2), recip: (1<<32)/uint64(den) + 1}
}

func (d boxDiv) q(v uint32) uint32 { return uint32(((uint64(v) + d.half) * d.recip) >> 32) }

// color rounds the averaged channels back to a Color.
func (d boxDiv) color(acc *[4]uint32) Color {
	return Color(d.q(acc[0])<<24 | d.q(acc[1])<<16 | d.q(acc[2])<<8 | d.q(acc[3]))
}

// boxPassH is one horizontal box pass over integer radius r on a w x
// h buffer: dst is the running average of src's window, transparent
// outside.
func boxPassH(dst, src []Color, w, h, r int) {
	div := newBoxDiv(2*r + 1)
	for y := range h {
		row := y * w
		var acc [4]uint32
		for k := 0; k <= r && k < w; k++ {
			addAcc(&acc, src[row+k])
		}
		for x := range w {
			dst[row+x] = div.color(&acc)
			if out := x - r; out >= 0 {
				subAcc(&acc, src[row+out])
			}
			if in := x + r + 1; in < w {
				addAcc(&acc, src[row+in])
			}
		}
	}
}

// boxPassV is boxPassH's vertical twin. It sweeps rows in memory order
// with one accumulator per column (acc, 4 per column) instead of
// walking columns, which strided through the buffer.
func boxPassV(dst, src []Color, w, h, r int, acc []uint32) {
	div := newBoxDiv(2*r + 1)
	clear(acc)
	addRow := func(y int, sign bool) {
		row := src[y*w : (y+1)*w]
		for x, c := range row {
			a := (*[4]uint32)(acc[x*4 : x*4+4])
			if sign {
				addAcc(a, c)
			} else {
				subAcc(a, c)
			}
		}
	}
	for k := 0; k <= r && k < h; k++ {
		addRow(k, true)
	}
	for y := range h {
		out := dst[y*w : (y+1)*w]
		for x := range out {
			out[x] = div.color((*[4]uint32)(acc[x*4 : x*4+4]))
		}
		if o := y - r; o >= 0 {
			addRow(o, false)
		}
		if in := y + r + 1; in < h {
			addRow(in, true)
		}
	}
}

// addAcc adds a color's premultiplied channels to an accumulator.
func addAcc(acc *[4]uint32, c Color) {
	acc[0] += uint32(c.A())
	acc[1] += uint32(c.R())
	acc[2] += uint32(c.G())
	acc[3] += uint32(c.B())
}

// subAcc subtracts one.
func subAcc(acc *[4]uint32, c Color) {
	acc[0] -= uint32(c.A())
	acc[1] -= uint32(c.R())
	acc[2] -= uint32(c.G())
	acc[3] -= uint32(c.B())
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
		acc := make([]uint32, 4*w)
		for _, r := range blurBoxes(s.Blur / 2) {
			if r < 1 {
				continue
			}
			boxPassH(tmp, sil, w, h, r)
			boxPassV(sil, tmp, w, h, r, acc)
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
			l.cv.set(region.X+x, region.Y+y, l.cv.get(region.X+x, region.Y+y).over(shadow))
		}
	}
}
