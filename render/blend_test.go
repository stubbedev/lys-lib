package render

import (
	"fmt"
	"image"
	"math"
	"os"
	"sync"
	"testing"

	"github.com/go-text/typesetting/font"
	"github.com/go-text/typesetting/fontscan"
	"golang.org/x/image/font/gofont/gomono"
	"golang.org/x/image/font/gofont/goregular"
)

// Source-over conformance. Every past rendering bug in this package was
// a blending bug: over() dropping alpha, wl_shm byte-order aliasing,
// RoundedRect halos (AA must scale premultiplied channels, not alpha),
// +/-1 premul drift, uint8 wraparound. This suite pins every primitive
// to one independent reference compositor over non-trivial backgrounds
// (an opaque mid-gray and a half-alpha underlay - never just black,
// which hides double multiplication), so the next blending regression
// fails a named row here instead of shipping as a halo.
//
// The table lives in blendRows. A new render primitive adds a row:
// paint + probe points + a tolerance with its rationale. The checks
// every row gets:
//
//   - source over background: out = src + dst*(1-srcA) per channel in
//     premultiplied space, against refOver (a float oracle sharing no
//     arithmetic with Color.over), at the row's per-primitive
//     tolerance;
//   - opaque source is a pure overwrite - dst channels must be
//     irrelevant, which catches accidental double blending;
//   - fully transparent source is a no-op, which catches
//     alpha-channel-only scaling;
//   - AA rows: every pixel is classified by the coverage the primitive
//     itself publishes (a calibration draw in opaque white over a
//     zeroed canvas - the alpha byte is the quantized coverage) and
//     must equal the reference composite at exactly that coverage;
//     full pixels are pure overwrites, empty pixels untouched. For the
//     two SDF primitives the published coverage is also cross-checked
//     against a 5x5 supersampled grid of the analytic distance field.
//
// TestBlendPropertyPairs then replays the whole table over hundreds of
// randomized color pairs (tiny xorshift RNG) to catch drift classes
// automatically.

// blendW, blendH is the shared canvas size for every row, in device
// pixels (rows at device scale 2 use the same buffer size).
const (
	blendW = 64
	blendH = 40
)

// The conformance backgrounds. bgGray is an opaque mid-gray; bgUnder
// sits at half alpha, as if something unseen were drawn beneath it, so
// blending must carry the destination's alpha through the keep term
// and not just its channels.
var (
	bgGray  = RGB(96, 96, 96)
	bgUnder = RGBA(200, 120, 40, 128)
)

// blendSrc is the curated source color for the table: translucent, with
// channels that sit far from both backgrounds so coverage errors cannot
// hide in a flat channel.
var blendSrc = RGBA(255, 255, 20, 200)

// --- reference compositors -------------------------------------------------
//
// These oracles deliberately share no arithmetic with Color.over and
// lerp: refOver works in float64 premultiplied space straight from the
// Porter-Duff definition out = src + dst*(1-srcA), rounding only at the
// final uint8 store.

func blendChannels(c Color) [4]float64 {
	return [4]float64{float64(c.A()), float64(c.R()), float64(c.G()), float64(c.B())}
}

func blendRound(v float64) uint32 {
	r := math.Round(v)
	if r < 0 {
		r = 0
	}
	if r > 255 {
		r = 255
	}
	return uint32(r)
}

func refOverChannels(src [4]float64, dst Color) Color {
	keep := 1 - src[0]/255
	a := [4]uint32{
		blendRound(src[0] + float64(dst.A())*keep),
		blendRound(src[1] + float64(dst.R())*keep),
		blendRound(src[2] + float64(dst.G())*keep),
		blendRound(src[3] + float64(dst.B())*keep),
	}
	return Color(a[0]<<24 | a[1]<<16 | a[2]<<8 | a[3])
}

// refOver is the suite's source-over reference: exactly the issue's
// formula, evaluated per channel in float premultiplied space. The
// production over() may land one count away - its only deviation is
// rounding the destination term to uint8 (+/-0.5) - so blended checks
// run at tolerance 1 unless a primitive composes more rounding.
func refOver(src, dst Color) Color {
	return refOverChannels(blendChannels(src), dst)
}

// refCover returns the expected pixel when src covers at cov in [0,1]:
// coverage scales all four premultiplied source channels - alpha
// included - before the composite. AA primitives quantize coverage to
// 1/255 steps, floor the scaled channels, and round the destination
// keep term, three composed roundings that bound the gap to this
// reference at 3 counts.
func refCover(src, dst Color, cov float64) Color {
	s := blendChannels(src)
	for i := range s {
		s[i] *= cov
	}
	return refOverChannels(s, dst)
}

// refLerp is the gradient ramp reference: linear interpolation of the
// premultiplied channels, half-up rounding, matching the gradient's
// pixel-center parameterization (t = (px+0.5-edge)/span).
func refLerp(a, b Color, t float64) Color {
	ch := func(x, y uint8) uint32 {
		return uint32(float64(x) + (float64(y)-float64(x))*t + 0.5)
	}
	return Color(ch(a.A(), b.A())<<24 |
		ch(a.R(), b.R())<<16 |
		ch(a.G(), b.G())<<8 |
		ch(a.B(), b.B()))
}

// complement is the gradient's far endpoint: the inverted straight
// color at the same alpha. Complementing through Straight() keeps the
// transparent variant of any row color a genuine no-op (complement of
// transparent is transparent).
func complement(c Color) Color {
	s := c.Straight()
	return RGBA(255-s[0], 255-s[1], 255-s[2], s[3])
}

// refOverStraight recomposites through the straight-alpha (PNG-style)
// boundary: un-premultiply to bytes, integer-composite, re-premultiply.
// It is a second independent path used to cross-check refOver itself;
// the double quantization widens its agreement window to 3.
func refOverStraight(src, dst Color) Color {
	s, d := src.Straight(), dst.Straight()
	ao := int(s[3]) + int(d[3])*(255-int(s[3]))/255
	if ao == 0 {
		return 0
	}
	mix := func(sc, dc uint8) uint8 {
		v := (int(sc)*int(s[3]) + int(dc)*int(d[3])*(255-int(s[3]))/255) / ao
		return uint8(min(255, max(0, v)))
	}
	return RGBA(mix(s[0], d[0]), mix(s[1], d[1]), mix(s[2], d[2]), uint8(ao))
}

// impliedCoverage recovers the coverage a pixel's channels agree on by
// inverting the reference model per channel:
//
//	out_c = dst_c + (a/255)*(src_c - dst_c*srcA/255)
//
// A channel is usable when its coefficient moves at least 96/255 per
// unit coverage - below that, uint8 rounding swamps the signal. Over
// an opaque background the alpha channel drops out on its own (the
// result is always 255 there); over the half-alpha underlay it is the
// strongest channel of all.
func impliedCoverage(got, src, dst Color) (float64, bool) {
	chans := [][3]float64{
		{float64(got.A()), float64(src.A()), float64(dst.A())},
		{float64(got.R()), float64(src.R()), float64(dst.R())},
		{float64(got.G()), float64(src.G()), float64(dst.G())},
		{float64(got.B()), float64(src.B()), float64(dst.B())},
	}
	sum, n := 0.0, 0
	for _, c := range chans {
		d := c[1] - c[2]*float64(src.A())/255
		if math.Abs(d) < 96 {
			continue
		}
		sum += 255 * (c[0] - c[2]) / d
		n++
	}
	if n == 0 {
		return 0, false
	}
	return sum / float64(n), true
}

// blendNear reports whether got and want agree per channel within tol.
func blendNear(got, want Color, tol int) bool {
	d := func(a, b uint8) bool { return abs(int(a)-int(b)) <= tol }
	return d(got.A(), want.A()) && d(got.R(), want.R()) &&
		d(got.G(), want.G()) && d(got.B(), want.B())
}

// --- table types ------------------------------------------------------------

type probeMode uint8

const (
	probeOver      probeMode = iota // pixel composites srcAt(paint) over the background
	probeUntouched                  // pixel must still be exactly the background
	probeOverwrite                  // pixel ignores what is underneath: Clear's contract
	probeOverlap                    // two strokes land here: src over (src over bg), as BorderRect corners do
)

// probePass selects the runner pass a probe is checked in.
type probePass uint8

const (
	passBlend     probePass = iota // the row's translucent source over the background
	passOverwrite                  // the opaque variant: pure overwrite expected
	passNoop                       // the transparent variant: nothing may change
)

type probe struct {
	x, y  int
	srcAt func(paint Color) Color
	mode  probeMode
}

func blendID(paint Color) Color { return paint }

// aaSpec drives the anti-aliasing stage of a row. Calibration draws the
// primitive alone in opaque white over a zeroed canvas: the alpha byte
// of each pixel is then the coverage the primitive itself publishes,
// and the painted canvas must agree with the reference composite at
// exactly that coverage - full pixels are pure overwrites, empty ones
// untouched, partial ones premultiplied-scaled. That per-pixel contract
// is the regression net for the halo class (alpha-only scaling cannot
// satisfy it: the channels would disagree with the published alpha).
type aaSpec struct {
	region Rect
	draw   func(cv *Canvas, col Color)
	// analytic, when set, is the primitive's ideal coverage field in
	// canvas pixels. The stage cross-checks published coverage against
	// a 5x5 supersampled grid of it - geometry the colors cannot move.
	analytic func(x, y float64) float64
	minFull  int
	minPart  int
}

type blendRow struct {
	name string
	// tol is the per-channel tolerance for blended assertions; why
	// documents what rounding justifies it (quoted on failure).
	tol int
	why string
	// paint draws the primitive with the given color - the row's source
	// color, its opaque variant, or transparent for the no-op pass.
	paint func(t *testing.T, cv *Canvas, col Color)
	// probes are sampled after every paint; AA rows may rely on the
	// region sweep alone and leave probes empty.
	probes []probe
	aa     *aaSpec
	// scale is the canvas device scale (0 means 1). Probe coordinates
	// are device pixels either way.
	scale int
	// custom rows run paint once per background and delegate all pixel
	// assertions to check - for primitives whose pixels the paint color
	// cannot modulate. They are skipped by the property replay, where
	// there is no color pair to vary.
	custom bool
	check  func(t *testing.T, cv *Canvas, data []byte, bg Color)
	// noTransparent / noOpaque opt a row out of the no-op or pure-
	// overwrite pass, with the reason. Clear overwrites by contract; a
	// color-emoji strike carries its own pixels, so it has no
	// transparent or opaque variant to draw.
	noTransparent string
	noOpaque      string
}

// --- the table ---------------------------------------------------------------

func blendRows(t *testing.T) []blendRow {
	t.Helper()
	return []blendRow{
		{
			name: "FillRect",
			tol:  1,
			why:  "over() rounds only the destination term (+/-0.5); the uint8 store is exact",
			paint: func(t *testing.T, cv *Canvas, col Color) {
				cv.FillRect(Rect{X: 6, Y: 6, W: 20, H: 14}, col)
			},
			probes: []probe{
				{6, 6, blendID, probeOver},
				{25, 19, blendID, probeOver},
				{16, 12, blendID, probeOver},
				{30, 30, blendID, probeUntouched},
			},
		},
		{
			name: "FillRectDevice",
			tol:  1,
			why:  "the device bridge shares FillRect's over() path; +/-1 for the destination rounding",
			paint: func(t *testing.T, cv *Canvas, col Color) {
				cv.FillRectDevice(Rect{X: 6, Y: 6, W: 20, H: 14}, col)
			},
			probes: []probe{
				{6, 6, blendID, probeOver},
				{25, 19, blendID, probeOver},
				{16, 12, blendID, probeOver},
				{30, 30, blendID, probeUntouched},
			},
		},
		{
			name: "BorderRect",
			tol:  1,
			why:  "four FillRectDevice strokes; +/-1 for the destination rounding",
			paint: func(t *testing.T, cv *Canvas, col Color) {
				cv.BorderRect(Rect{X: 6, Y: 6, W: 20, H: 14}, 2, col)
			},
			probes: []probe{
				{16, 6, blendID, probeOver},       // top stroke only
				{16, 19, blendID, probeOver},      // bottom stroke only
				{6, 12, blendID, probeOver},       // left stroke only
				{25, 12, blendID, probeOver},      // right stroke only
				{6, 6, blendID, probeOverlap},     // corner: top and left strokes both land
				{15, 12, blendID, probeUntouched}, // hollow center
			},
		},
		{
			name: "RoundedRect",
			tol:  1,
			why:  "full coverage reproduces col exactly, leaving over()'s +/-1 destination rounding",
			paint: func(t *testing.T, cv *Canvas, col Color) {
				cv.RoundedRect(Rect{X: 6, Y: 6, W: 26, H: 26}, 9, col)
			},
			probes: []probe{
				{19, 19, blendID, probeOver},
				{10, 19, blendID, probeOver},
				{4, 4, blendID, probeUntouched},
				{30, 3, blendID, probeUntouched},
			},
			aa: &aaSpec{
				region:   Rect{X: 5, Y: 5, W: 28, H: 28},
				draw:     func(cv *Canvas, col Color) { cv.RoundedRect(Rect{X: 6, Y: 6, W: 26, H: 26}, 9, col) },
				analytic: roundedCoverage(Rect{X: 6, Y: 6, W: 26, H: 26}, 9),
				minFull:  200,
				minPart:  16,
			},
		},
		{
			name: "LinearGradient",
			tol:  1,
			why:  "lerp is float with half-up rounding and matches refLerp bit for bit; over() adds +/-1",
			paint: func(t *testing.T, cv *Canvas, col Color) {
				cv.PaintGradient(Rect{X: 4, Y: 6, W: 28, H: 10}, Corners{}, Linear(90, GradientStop{Pos: 0, Color: col}, GradientStop{Pos: 1, Color: complement(col)}))
				cv.PaintGradient(Rect{X: 4, Y: 20, W: 28, H: 10}, Corners{}, Linear(180, GradientStop{Pos: 0, Color: complement(col)}, GradientStop{Pos: 1, Color: col}))
			},
			probes: append(rampProbes(), probe{40, 14, blendID, probeUntouched}),
		},
		{
			name: "RadialGradient",
			tol:  1,
			why:  "the radial position is the exact circle distance, the stop lerp matches refLerp; over() adds +/-1",
			paint: func(t *testing.T, cv *Canvas, col Color) {
				cv.PaintGradient(Rect{X: 4, Y: 4, W: 32, H: 32}, Corners{}, Gradient{
					Kind: GradientRadial, Circle: true, Size: ClosestSide, CenterX: 0.5, CenterY: 0.5,
					Stops: []GradientStop{{Pos: 0, Color: col}, {Pos: 1, Color: complement(col)}},
				})
			},
			probes: append(gradientProbes(func(x, y float64) float64 { return math.Hypot(x-20, y-20) / 16 }, [][2]int{{19, 19}, {27, 19}, {20, 33}}),
				probe{40, 20, blendID, probeUntouched}),
		},
		{
			name: "ConicGradient",
			tol:  1,
			why:  "the conic position is the exact clockwise turn from up, the stop lerp matches refLerp; over() adds +/-1",
			paint: func(t *testing.T, cv *Canvas, col Color) {
				cv.PaintGradient(Rect{X: 4, Y: 4, W: 32, H: 32}, Corners{}, Conic(0, GradientStop{Pos: 0, Color: col}, GradientStop{Pos: 1, Color: complement(col)}))
			},
			probes: append(gradientProbes(func(x, y float64) float64 {
				a := math.Atan2(x-20, -(y-20)) * 180 / math.Pi
				return math.Mod(a+360, 360) / 360
			}, [][2]int{{19, 8}, {30, 20}, {20, 31}, {8, 19}}), probe{40, 20, blendID, probeUntouched}),
		},
		{
			name: "RepeatingGradient",
			tol:  1,
			why:  "the repeat folds the line position into the stop range exactly; the lerp matches refLerp; over() adds +/-1",
			paint: func(t *testing.T, cv *Canvas, col Color) {
				cv.PaintGradient(Rect{X: 4, Y: 6, W: 28, H: 10}, Corners{}, Linear(90, GradientStop{Pos: 0, Color: col}, GradientStop{Pos: 0.25, Color: complement(col)}).Repeating())
			},
			probes: append(gradientProbes(func(x, _ float64) float64 {
				return math.Mod((x-4)/28, 0.25) / 0.25
			}, [][2]int{{6, 10}, {10, 10}, {20, 10}, {30, 10}}), probe{40, 10, blendID, probeUntouched}),
		},
		{
			name: "Line",
			tol:  1,
			why:  "full-coverage stroke centers reproduce col exactly, leaving over()'s +/-1",
			paint: func(t *testing.T, cv *Canvas, col Color) {
				cv.Line(4, 32, 60, 32, 5, col) // horizontal stroke, lower band
				cv.Line(4, 6, 28, 22, 3, col)  // diagonal stroke, disjoint from the band so probes see one stroke
			},
			probes: []probe{
				{20, 32, blendID, probeOver},
				{50, 32, blendID, probeOver},
				{16, 14, blendID, probeOver}, // diagonal midpoint, dead on the segment
				{40, 20, blendID, probeUntouched},
			},
			aa: &aaSpec{
				region: Rect{X: 2, Y: 4, W: 60, H: 33},
				draw: func(cv *Canvas, col Color) {
					cv.Line(4, 32, 60, 32, 5, col)
					cv.Line(4, 6, 28, 22, 3, col)
				},
				analytic: lineUnionCoverage(
					lineCoverage(4, 32, 60, 32, 5),
					lineCoverage(4, 6, 28, 22, 3),
				),
				minFull: 180,
				minPart: 60,
			},
		},
		{
			// Row shape: a shadow has no single "the" coverage — the field
			// is 1 inside the rect and falls off through the Gaussian
			// tail outside. The rect interior therefore plays the role the
			// flat interiors play for the other primitives, which keeps
			// the opaque-overwrite pass meaningful (full-coverage pixels
			// must still be pure overwrites) even though a soft shadow as
			// a whole has no opaque variant. Partial coverage is asserted
			// by the AA sweep at the coverage the cached raster publishes
			// (against the kernel tail field the raster is built from),
			// not by probes — a probe's refOver cannot see the primitive's
			// internal quantize+floor+round chain within tol 1.
			name: "Shadow",
			tol:  1,
			why:  "full-coverage interior reproduces col exactly, leaving over()'s +/-1; the falloff ring is held to the published coverage by the AA sweep",
			paint: func(t *testing.T, cv *Canvas, col Color) {
				cv.Shadow(shadowRowRect, shadowRowRadius, shadowRowBlur, col)
			},
			probes: []probe{
				{25, 19, blendID, probeOver}, // inside the rect: full shadow coverage
				{2, 2, blendID, probeUntouched},
				{60, 37, blendID, probeUntouched},
			},
			aa: &aaSpec{
				region:   Rect{X: 14, Y: 7, W: 36, H: 24},
				draw:     func(cv *Canvas, col Color) { cv.Shadow(shadowRowRect, shadowRowRadius, shadowRowBlur, col) },
				analytic: shadowRowCoverage(),
				minFull:  280,
				minPart:  240,
			},
		},
		{
			name: "IconSVG",
			tol:  1,
			why:  "Icon.Draw shares the DrawImage blit, whose 16-to-8 bit fetch is exact; +/-1 is over()'s rounding",
			paint: func(t *testing.T, cv *Canvas, col Color) {
				circleIcon(t).Tint(col).Draw(cv, 20, 8)
			},
			probes: []probe{
				{32, 20, blendID, probeOver},
				{32, 14, blendID, probeOver},
				{22, 10, blendID, probeUntouched}, // outside the circle, inside its box
			},
			aa: &aaSpec{
				region: Rect{X: 19, Y: 7, W: 26, H: 26},
				draw: func(cv *Canvas, col Color) {
					circleIcon(t).Tint(col).Draw(cv, 20, 8)
				},
				minFull: 250,
				minPart: 20,
			},
		},
		{
			name: "DrawImage",
			tol:  1,
			why:  "the blit fetches premultiplied pixels exactly; +/-1 is over()'s destination rounding",
			paint: func(t *testing.T, cv *Canvas, col Color) {
				cv.DrawImage(blendPattern(col), 24, 12)
			},
			probes: []probe{
				{24, 12, blendID, probeOver},
				{26, 12, blendID, probeUntouched}, // transparent source pixel: blit, not overwrite
				{27, 13, blendID, probeOver},
				{24, 13, blendID, probeUntouched},
				{24, 14, blendID, probeUntouched},
			},
		},
		{
			name:  "DrawImageScaled",
			scale: 2,
			tol:   1,
			why:   "nearest-neighbor mapping at 2x picks whole premultiplied source pixels; +/-1 is over()'s rounding",
			paint: func(t *testing.T, cv *Canvas, col Color) {
				cv.DrawImage(blendPatternTall(col), 8, 4)
			},
			probes: []probe{
				{16, 8, blendID, probeOver}, // device px of logical (8,4)
				{17, 9, blendID, probeOver}, // same source pixel, second device px
				{18, 8, blendID, probeOver}, // next source column
				{20, 10, blendID, probeUntouched},
				{16, 12, blendID, probeUntouched},
			},
		},
		{
			name: "DrawImageDevice",
			tol:  1,
			why:  "Resample preserves a constant premultiplied field exactly; the blit adds over()'s +/-1",
			paint: func(t *testing.T, cv *Canvas, col Color) {
				src := blendConstant(col, 3, 3)
				cv.DrawImageDevice(Resample(src, src.Bounds(), 5, 5), 24, 10)
			},
			probes: []probe{
				{24, 10, blendID, probeOver},
				{26, 12, blendID, probeOver},
				{28, 14, blendID, probeOver},
				{23, 10, blendID, probeUntouched},
			},
		},
		{
			name: "TypefaceDraw",
			tol:  1,
			why:  "glyph masks scale all premultiplied channels (alpha included) then over(); +/-1",
			paint: func(t *testing.T, cv *Canvas, col Color) {
				tf := blendTypeface(t)
				tf.Draw(cv, tf.Shape("gel", 20), 6, 30, col)
			},
			aa: &aaSpec{
				region: Rect{X: 0, Y: 0, W: blendW, H: blendH},
				draw: func(cv *Canvas, col Color) {
					tf := blendTypeface(t)
					tf.Draw(cv, tf.Shape("gel", 20), 6, 30, col)
				},
				minFull: 6,
				minPart: 60,
			},
		},
		{
			name: "ChainDraw",
			tol:  1,
			why:  "runs share ShapedText.Draw's blend path across faces; +/-1",
			paint: func(t *testing.T, cv *Canvas, col Color) {
				c := NewChain(blendTypeface(t)).WithResolver(func(rune) *Typeface { return blendMono(t) })
				s := c.Shape("a你", 20)
				if s.Runs() != 2 {
					t.Errorf("chain shaped %d runs, want 2 (latin + fallback)", s.Runs())
				}
				c.Draw(cv, s, 4, 30, col)
			},
			aa: &aaSpec{
				region: Rect{X: 0, Y: 0, W: blendW, H: blendH},
				draw: func(cv *Canvas, col Color) {
					c := NewChain(blendTypeface(t)).WithResolver(func(rune) *Typeface { return blendMono(t) })
					c.Draw(cv, c.Shape("a你", 20), 4, 30, col)
				},
				minFull: 4,
				minPart: 40,
			},
		},
		{
			name: "TypefaceDrawAligned",
			tol:  1,
			why:  "DrawAligned positions then shares ShapedText.Draw; +/-1",
			paint: func(t *testing.T, cv *Canvas, col Color) {
				blendTypeface(t).DrawAligned(cv, "gel", Rect{X: 4, Y: 5, W: 40, H: 30}, 18, col, AlignCenter)
			},
			aa: &aaSpec{
				region: Rect{X: 3, Y: 4, W: 42, H: 32},
				draw: func(cv *Canvas, col Color) {
					blendTypeface(t).DrawAligned(cv, "gel", Rect{X: 4, Y: 5, W: 40, H: 30}, 18, col, AlignCenter)
				},
				minFull: 6,
				minPart: 60,
			},
		},
		{
			name: "Clear",
			tol:  0,
			why:  "Clear overwrites raw: probes expect the exact color, not a composite",
			paint: func(t *testing.T, cv *Canvas, col Color) {
				cv.Clear(Rect{X: 6, Y: 6, W: 20, H: 14}, col)
			},
			probes: []probe{
				{6, 6, blendID, probeOverwrite},
				{25, 19, blendID, probeOverwrite},
				{30, 30, blendID, probeUntouched},
			},
			noTransparent: "Clear overwrites by contract: a transparent fill clears to transparent, it is not a blend",
		},
		{
			name:   "TypefaceBitmapEmoji",
			custom: true,
			paint: func(t *testing.T, cv *Canvas, col Color) {
				tf := blendEmoji(t)
				tf.Draw(cv, tf.Shape("\U0001F600", 16), 8, 30, col)
			},
			check: func(t *testing.T, cv *Canvas, data []byte, bg Color) {
				drawn := 0
				for y := range blendH {
					for x := range blendW {
						got := pxAt(data, Stride(blendW), x, y)
						if got == bg {
							continue
						}
						drawn++
						for _, c := range []uint8{got.R(), got.G(), got.B()} {
							if int(c) > int(got.A())+1 {
								t.Errorf("pixel (%d,%d) = %#08x: channel %d exceeds alpha %d (+1) - the CBDT blit wrote un-premultiplied pixels",
									x, y, uint32(got), c, got.A())
								break
							}
						}
					}
				}
				if drawn < 100 {
					t.Errorf("color emoji produced %d pixels, want a bitmap strike's worth", drawn)
				}
			},
			noTransparent: "color bitmaps paint their own pixels; the text color does not modulate them, so there is no transparent variant",
			noOpaque:      "same: the strike's pixels are color-independent, so a pure-overwrite pass has nothing to vary",
		},
	}
}

// rampProbes samples both gradient orientations: first and last pixel
// column/row (the extreme reachable endpoints of the ramp) and the
// midpoints, in premultiplied space.
func rampProbes() []probe {
	at := func(x int) float64 { return (float64(x) + 0.5 - 4) / 28 } // horizontal rect {4,...,28,...}
	atY := func(y int) float64 { return (float64(y) + 0.5 - 20) / 10 }
	h := func(x int) probe {
		return probe{x, 10, func(p Color) Color { return refLerp(p, complement(p), at(x)) }, probeOver}
	}
	v := func(y int) probe {
		return probe{10, y, func(p Color) Color { return refLerp(complement(p), p, atY(y)) }, probeOver}
	}
	return []probe{h(4), h(17), h(31), v(20), v(24), v(29)}
}

// gradientProbes probes a gradient row at pixel centers: t maps the
// center to the gradient position, the expected color is the reference
// lerp from the paint to its complement.
func gradientProbes(t func(x, y float64) float64, at [][2]int) []probe {
	out := make([]probe, len(at))
	for i, p := range at {
		f := t(float64(p[0])+0.5, float64(p[1])+0.5)
		out[i] = probe{p[0], p[1], func(c Color) Color { return refLerp(c, complement(c), f) }, probeOver}
	}
	return out
}

// --- geometry references for the SDF primitives -----------------------------

// shadowRowRect, shadowRowRadius, and shadowRowBlur are the Shadow
// row's geometry: a 24x12 rect with room for the blur ring inside the
// shared canvas.
var (
	shadowRowRect   = Rect{X: 20, Y: 13, W: 24, H: 12}
	shadowRowRadius = 4
	shadowRowBlur   = 5
)

// shadowRowCoverage mirrors the raster the Shadow row is painted from:
// the kernel tail of the rounded rect's signed distance, at scale 1.
func shadowRowCoverage() func(x, y float64) float64 {
	tail, half := shadowTail(shadowRowBlur)
	return func(x, y float64) float64 {
		return float64(tailCoverage(tail, half, sdRoundRect(x, y, shadowRowRect, float64(shadowRowRadius)))) / 255
	}
}

// roundedCoverage mirrors RoundedRect's distance field: clamped 0.5-d
// coverage at a point, used only through the supersampler.
func roundedCoverage(r Rect, radius int) func(x, y float64) float64 {
	rad := math.Min(float64(radius), math.Min(float64(r.W), float64(r.H))/2)
	return func(x, y float64) float64 {
		return blendClamp01(0.5 - sdRoundRect(x, y, r, rad))
	}
}

// lineCoverage mirrors Line's point-to-segment coverage.
func lineCoverage(x0, y0, x1, y1, width int) func(x, y float64) float64 {
	half := float64(width) / 2
	ax, ay := float64(x0)+0.5, float64(y0)+0.5
	dx := float64(x1) + 0.5 - ax
	dy := float64(y1) + 0.5 - ay
	len2 := dx*dx + dy*dy
	return func(x, y float64) float64 {
		px, py := x-ax, y-ay
		t := 0.0
		if len2 > 0 {
			t = math.Min(1, math.Max(0, (px*dx+py*dy)/len2))
		}
		d := math.Hypot(px-dx*t, py-dy*t)
		return blendClamp01(half + 0.5 - d)
	}
}

func lineUnionCoverage(fns ...func(x, y float64) float64) func(x, y float64) float64 {
	return func(x, y float64) float64 {
		best := 0.0
		for _, f := range fns {
			best = math.Max(best, f(x, y))
		}
		return best
	}
}

func blendClamp01(v float64) float64 { return math.Min(1, math.Max(0, v)) }

// supersampled averages an analytic coverage field over an n-by-n
// subgrid of the pixel - the independent estimate the published
// coverage is cross-checked against.
func supersampled(f func(x, y float64) float64, x, y, n int) float64 {
	sum := 0.0
	for j := range n {
		for i := range n {
			sx := float64(x) + (float64(i)+0.5)/float64(n)
			sy := float64(y) + (float64(j)+0.5)/float64(n)
			sum += f(sx, sy)
		}
	}
	return sum / float64(n*n)
}

// --- image helpers -----------------------------------------------------------

// blendImage builds a w-by-h RGBA of col with transparent holes at the
// given positions - the suite's blit sources. Drawing one must respect
// alpha: an overwrite blit would wipe the holes.
func blendImage(col Color, w, h int, holes ...[2]int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	skip := make(map[[2]int]bool, len(holes))
	for _, p := range holes {
		skip[p] = true
	}
	for y := range h {
		for x := range w {
			c := col
			if skip[[2]int{x, y}] {
				c = 0
			}
			o := img.PixOffset(x, y)
			img.Pix[o], img.Pix[o+1], img.Pix[o+2], img.Pix[o+3] = c.R(), c.G(), c.B(), c.A()
		}
	}
	return img
}

// blendPattern builds the 4x2 blit source: paint-colored pixels,
// transparent holes on both rows.
func blendPattern(col Color) *image.RGBA {
	return blendImage(col, 4, 2, [2]int{2, 0}, [2]int{0, 1})
}

// blendPatternTall is the 3x2 variant for the device-scale row.
func blendPatternTall(col Color) *image.RGBA {
	return blendImage(col, 3, 2, [2]int{2, 1})
}

// blendConstant tiles one color into a w-by-h RGBA.
func blendConstant(col Color, w, h int) *image.RGBA {
	return blendImage(col, w, h)
}

// --- cached fixtures ---------------------------------------------------------

var (
	blendFontOnce   sync.Once
	blendFontReg    *Typeface
	blendFontMono   *Typeface
	blendFontErr    error
	blendIconOnce   sync.Once
	blendCircleIcon *Icon
	blendIconErr    error
	blendEmojiOnce  sync.Once
	blendFontEmoji  *Typeface
)

func blendTypeface(t *testing.T) *Typeface {
	t.Helper()
	blendFontOnce.Do(func() {
		blendFontReg, blendFontErr = LoadFont(goregular.TTF)
		if blendFontErr == nil {
			blendFontMono, blendFontErr = LoadFont(gomono.TTF)
		}
	})
	if blendFontErr != nil {
		t.Fatalf("load fixture fonts: %v", blendFontErr)
	}
	return blendFontReg
}

func blendMono(t *testing.T) *Typeface {
	t.Helper()
	blendTypeface(t) // loads mono alongside regular
	return blendFontMono
}

func circleIcon(t *testing.T) *Icon {
	t.Helper()
	blendIconOnce.Do(func() {
		blendCircleIcon, blendIconErr = LoadSVG([]byte(testCircleSVG), 24, 24)
	})
	if blendIconErr != nil {
		t.Fatalf("load svg fixture: %v", blendIconErr)
	}
	return blendCircleIcon
}

// blendEmoji resolves the system color-emoji face for the bitmap-strike
// row, skipping with the reason when the machine has none - the same
// convention internal/sysfont's tests use.
func blendEmoji(t *testing.T) *Typeface {
	t.Helper()
	blendEmojiOnce.Do(func() {
		fm := fontscan.NewFontMap(quietScanLogger{})
		if err := fm.UseSystemFonts(""); err != nil {
			t.Skipf("no system font store: %v", err)
		}
		loc, ok := fm.FindSystemFont("Noto Color Emoji")
		if !ok {
			t.Skip("Noto Color Emoji not installed")
		}
		f, err := os.Open(loc.File)
		if err != nil {
			t.Skipf("emoji font unreadable: %v", err)
		}
		defer f.Close()
		faces, err := font.ParseTTC(f)
		if err != nil {
			t.Skipf("emoji font unparseable: %v", err)
		}
		if int(loc.Index) >= len(faces) || len(faces[loc.Index].BitmapSizes()) == 0 {
			t.Skip("emoji face has no embedded bitmap strikes to pin")
		}
		tf, err := NewTypeface(faces[loc.Index])
		if err != nil {
			t.Skipf("emoji face unusable: %v", err)
		}
		blendFontEmoji = tf
	})
	if blendFontEmoji == nil {
		t.Skip("no bitmap-strike emoji face available")
	}
	return blendFontEmoji
}

type quietScanLogger struct{}

func (quietScanLogger) Printf(string, ...any) {}

// --- the runner --------------------------------------------------------------

func rowCanvas(scale int) (*Canvas, []byte) {
	data := make([]byte, Stride(blendW)*blendH)
	if scale == 2 {
		return NewScaled(data, Stride(blendW), blendW, blendH, 2, 1), data
	}
	return New(data, Stride(blendW), blendW, blendH), data
}

// checkRow runs one row against one background: the source-over pass,
// the opaque pure-overwrite pass, the transparent no-op pass, and the
// AA region sweep. ctx prefixes every failure with row and background.
func checkRow(t *testing.T, row blendRow, src, bg Color, crossCheck bool, ctx string) {
	t.Helper()

	// Custom rows replace the standard passes with their own contract.
	if row.custom {
		cv, data := rowCanvas(row.scale)
		cv.Clear(cv.Rect(), bg)
		row.paint(t, cv, src)
		row.check(t, cv, data, bg)
		return
	}

	// Pass 1: the source color over the background.
	cv, data := rowCanvas(row.scale)
	cv.Clear(cv.Rect(), bg)
	row.paint(t, cv, src)
	checkProbes(t, row.probes, data, src, bg, row.tol, passBlend, row.why, ctx)
	if row.aa != nil {
		cal := calibrateCoverage(t, row.aa, crossCheck)
		sweepAA(t, row.aa, cal, data, src, bg, row.tol, false, row.why, ctx)
	}

	// Pass 2: the opaque variant must be a pure overwrite - dst channels
	// irrelevant. This is what catches accidental double blending.
	if row.noOpaque == "" {
		op := src | 0xFF000000
		cv, data := rowCanvas(row.scale)
		cv.Clear(cv.Rect(), bg)
		row.paint(t, cv, op)
		checkProbes(t, row.probes, data, op, bg, 0, passOverwrite, "", ctx)
		if row.aa != nil {
			cal := calibrateCoverage(t, row.aa, false)
			sweepAA(t, row.aa, cal, data, op, bg, 0, true, row.why, ctx)
		}
	}

	// Pass 3: the fully transparent color must be a no-op - what catches
	// alpha-channel-only scaling. The blended check at paint 0 is exact:
	// refOver(0, bg) == bg.
	if row.noTransparent == "" {
		cv, data := rowCanvas(row.scale)
		cv.Clear(cv.Rect(), bg)
		row.paint(t, cv, 0)
		checkProbes(t, row.probes, data, 0, bg, 0, passNoop, "", ctx)
		if row.aa != nil {
			sweepUntouched(t, row.aa, data, bg, ctx)
		}
	}
}

func checkProbes(t *testing.T, probes []probe, data []byte, paint, bg Color, tol int, pass probePass, why, ctx string) {
	t.Helper()
	for _, p := range probes {
		got := pxAt(data, Stride(blendW), p.x, p.y)
		switch p.mode {
		case probeUntouched:
			if got != bg {
				t.Errorf("%s: pixel (%d,%d) = %#08x, want untouched background %#08x",
					ctx, p.x, p.y, uint32(got), uint32(bg))
			}
		case probeOverwrite:
			if want := p.srcAt(paint); got != want {
				t.Errorf("%s: pixel (%d,%d) = %#08x, want the raw overwrite %#08x - the primitive must ignore what is underneath",
					ctx, p.x, p.y, uint32(got), uint32(want))
			}
		case probeOverlap:
			want := p.srcAt(paint)
			switch pass {
			case passNoop:
				want = bg
			case passBlend: // two translucent strokes stack: src over (src over bg)
				want = refOver(want, refOver(want, bg))
			}
			// passOverwrite keeps the single application: an opaque second
			// stroke cannot change the pixel.
			if !blendNear(got, want, blendOverlapTol(pass, tol)) {
				t.Errorf("%s: pixel (%d,%d) = %#08x, want two-stroke composite %#08x - overlapping strokes must compose predictably",
					ctx, p.x, p.y, uint32(got), uint32(want))
			}
		case probeOver:
			want := p.srcAt(paint)
			switch pass {
			case passOverwrite:
				if got != want {
					t.Errorf("%s: opaque source at (%d,%d) = %#08x, want pure overwrite %#08x (dst channels must be irrelevant)",
						ctx, p.x, p.y, uint32(got), uint32(want))
				}
			case passNoop:
				if got != bg {
					t.Errorf("%s: transparent source at (%d,%d) = %#08x, want untouched background %#08x",
						ctx, p.x, p.y, uint32(got), uint32(bg))
				}
			default:
				if ref := refOver(want, bg); !blendNear(got, ref, tol) {
					t.Errorf("%s: pixel (%d,%d) = %#08x, want src %#08x over bg %#08x = %#08x (+/-%d): %s",
						ctx, p.x, p.y, uint32(got), uint32(want), uint32(bg), uint32(ref), tol, why)
				}
			}
		}
	}
}

// blendOverlapTol picks the tolerance for an overlap probe: the exact
// overwrite and no-op passes compare exactly, the blend pass allows the
// row tolerance twice over - two strokes, two destination roundings.
func blendOverlapTol(pass probePass, tol int) int {
	if pass == passBlend {
		return 2 * tol
	}
	return 0
}

// partialTol bounds refCover against a partially covered pixel: the
// primitive quantizes coverage to 1/255 steps (+/-0.5), floors the
// scaled premultiplied channels (<=1), and over() rounds the
// destination term (+/-0.5) - three composed roundings.
const partialTol = 3

// calibrateCoverage publishes each pixel's coverage by drawing the
// primitive alone in opaque white over a zeroed canvas: the alpha byte
// is the quantized coverage. When the row carries an analytic field and
// crossCheck is set, the published coverage is also held against a 5x5
// supersampled grid of it - windows generous enough for the distance
// field's center-vs-area bias, tight enough to catch clamped coverage
// (no anti-aliasing at all).
func calibrateCoverage(t *testing.T, spec *aaSpec, crossCheck bool) map[[2]int]uint8 {
	t.Helper()
	zero := make([]byte, Stride(blendW)*blendH)
	spec.draw(New(zero, Stride(blendW), blendW, blendH), RGB(255, 255, 255))

	cov := make(map[[2]int]uint8)
	var sumPub, sumRef float64
	for y := spec.region.Y; y < spec.region.Y+spec.region.H; y++ {
		for x := spec.region.X; x < spec.region.X+spec.region.W; x++ {
			m := pxAt(zero, Stride(blendW), x, y).A()
			cov[[2]int{x, y}] = m
			if !crossCheck || spec.analytic == nil {
				continue
			}
			est := supersampled(spec.analytic, x, y, 5)
			sumPub += float64(m) / 255
			sumRef += est
			switch m {
			case 0:
				if est > 0.25 {
					t.Errorf("pixel (%d,%d) publishes no coverage, supersampled field says %.2f", x, y, est)
				}
			case 255:
				if est < 0.7 {
					t.Errorf("pixel (%d,%d) publishes full coverage, supersampled field says %.2f", x, y, est)
				}
			default:
				if math.Abs(float64(m)/255-est) > 0.35 {
					t.Errorf("pixel (%d,%d) publishes coverage %d/255, supersampled field says %.2f", x, y, m, est)
				}
			}
		}
	}
	if crossCheck && spec.analytic != nil {
		if n := float64(spec.region.W * spec.region.H); n > 0 && math.Abs(sumPub/n-sumRef/n) > 0.08 {
			t.Errorf("published coverage mean %.3f disagrees with the supersampled field mean %.3f", sumPub/n, sumRef/n)
		}
	}
	return cov
}

// sweepAA holds every pixel of an AA region against the reference
// composite at the coverage the primitive published for it: fully
// covered pixels are pure overwrites of col (or, in the overwrite
// pass, must equal col exactly), empty pixels stay background, and
// partial pixels must equal refCover at exactly that coverage - and
// their channels must agree with the published alpha on one coverage
// value, the regression net for the halo class.
func sweepAA(t *testing.T, spec *aaSpec, cov map[[2]int]uint8, data []byte, col, bg Color, tol int, overwrite bool, why, ctx string) {
	t.Helper()
	full, part := 0, 0
	for y := spec.region.Y; y < spec.region.Y+spec.region.H; y++ {
		for x := spec.region.X; x < spec.region.X+spec.region.W; x++ {
			m := cov[[2]int{x, y}]
			got := pxAt(data, Stride(blendW), x, y)
			switch m {
			case 0:
				if got != bg {
					t.Errorf("%s: pixel (%d,%d) = %#08x, want untouched background %#08x (published coverage 0)",
						ctx, x, y, uint32(got), uint32(bg))
				}
			case 255:
				full++
				if overwrite {
					if got != col {
						t.Errorf("%s: fully covered pixel (%d,%d) = %#08x, want pure overwrite %#08x",
							ctx, x, y, uint32(got), uint32(col))
					}
				} else if !blendNear(got, refOver(col, bg), tol) {
					t.Errorf("%s: fully covered pixel (%d,%d) = %#08x, want %#08x (+/-%d): %s",
						ctx, x, y, uint32(got), uint32(refOver(col, bg)), tol, why)
				}
			default:
				part++
				want := refCover(col, bg, float64(m)/255)
				if !blendNear(got, want, partialTol) {
					t.Errorf("%s: pixel (%d,%d) at published coverage %d/255 = %#08x, want %#08x (+/-%d): coverage rounding",
						ctx, x, y, m, uint32(got), uint32(want), partialTol)
				}
				if im, ok := impliedCoverage(got, col, bg); ok && math.Abs(im-float64(m)) > 12 {
					t.Errorf("%s: pixel (%d,%d) channels imply coverage %.0f/255 but the primitive published %d/255 - AA did not scale the premultiplied channels (halo class)",
						ctx, x, y, im, m)
				}
			}
		}
	}
	if full < spec.minFull {
		t.Errorf("%s: %d fully covered pixels, want >= %d - the primitive barely drew", ctx, full, spec.minFull)
	}
	if part < spec.minPart {
		t.Errorf("%s: %d anti-aliased pixels, want >= %d - coverage must be partial somewhere", ctx, part, spec.minPart)
	}
}

// sweepUntouched is the transparent-source pass over an AA region:
// every pixel must still be the background, exactly.
func sweepUntouched(t *testing.T, spec *aaSpec, data []byte, bg Color, ctx string) {
	t.Helper()
	for y := spec.region.Y; y < spec.region.Y+spec.region.H; y++ {
		for x := spec.region.X; x < spec.region.X+spec.region.W; x++ {
			if got := pxAt(data, Stride(blendW), x, y); got != bg {
				t.Errorf("%s: transparent source left pixel (%d,%d) = %#08x, want background %#08x",
					ctx, x, y, uint32(got), uint32(bg))
				return
			}
		}
	}
}

// --- the tests ---------------------------------------------------------------

// TestBlendConformance replays every row of the table over both
// conformance backgrounds, with the supersampled cross-check on.
func TestBlendConformance(t *testing.T) {
	for _, row := range blendRows(t) {
		t.Run(row.name, func(t *testing.T) {
			for _, bg := range []Color{bgGray, bgUnder} {
				checkRow(t, row, blendSrc, bg, true, fmt.Sprintf("%s: bg %#08x", row.name, uint32(bg)))
			}
		})
	}
}

// TestBlendAssociativity pins overlap composition: drawing C, then B,
// then A must land where grouping the draws differently - (A over B)
// composited over (C over bg) - lands, within the uint8 quantization
// the two groupings store differently.
func TestBlendAssociativity(t *testing.T) {
	var (
		a = RGBA(220, 40, 60, 180) // top draw: a Line
		b = RGBA(40, 200, 80, 140) // middle draw: a RoundedRect
		c = RGBA(60, 80, 220, 120) // bottom draw: a FillRect
	)
	line := lineCoverage(12, 16, 40, 24, 5)
	samples := [][2]int{{26, 20}, {22, 18}, {30, 22}}
	for _, bg := range []Color{bgGray, bgUnder} {
		// Left: one canvas, paint order C, B, A - i.e. A over (B over
		// (C over bg)).
		cv, data := newTestCanvas(blendW, blendH)
		cv.Clear(cv.Rect(), bg)
		cv.FillRect(Rect{X: 4, Y: 4, W: 44, H: 32}, c)
		cv.RoundedRect(Rect{X: 10, Y: 10, W: 28, H: 20}, 5, b)
		cv.Line(12, 16, 40, 24, 5, a)

		// Right: X = A over B composited alone on a transparent canvas,
		// then X over (C over bg) via a one-pixel blit.
		cvx, datax := newTestCanvas(blendW, blendH)
		cvx.FillRect(Rect{X: 10, Y: 10, W: 28, H: 20}, b)
		cvx.FillRect(Rect{X: 12, Y: 14, W: 30, H: 12}, a)
		cvc, datac := newTestCanvas(blendW, blendH)
		cvc.Clear(cvc.Rect(), bg)
		cvc.FillRect(Rect{X: 4, Y: 4, W: 44, H: 32}, c)
		one := image.NewRGBA(image.Rect(0, 0, 1, 1))
		for _, p := range samples {
			x := pxAt(datax, Stride(blendW), p[0], p[1])
			if line(float64(p[0])+0.5, float64(p[1])+0.5) < 1 { // sample must be fully covered
				t.Fatalf("sample (%d,%d) is not fully covered by the top stroke", p[0], p[1])
			}
			o := one.PixOffset(0, 0)
			one.Pix[o], one.Pix[o+1], one.Pix[o+2], one.Pix[o+3] = x.R(), x.G(), x.B(), x.A()
			cvc.DrawImage(one, p[0], p[1])
			left := pxAt(data, Stride(blendW), p[0], p[1])
			right := pxAt(datac, Stride(blendW), p[0], p[1])
			if !blendNear(left, right, 2) {
				t.Errorf("bg %#08x at (%d,%d): A over (B over (C over bg)) = %#08x, (A over B) over (C over bg) = %#08x - overlap composition drifted past uint8 quantization",
					uint32(bg), p[0], p[1], uint32(left), uint32(right))
			}
		}
	}
}

// TestGradientRampPremul pins the ramp itself: endpoint exactness at
// both extremes and midpoint interpolation in premultiplied space, not
// straight-alpha space.
func TestGradientRampPremul(t *testing.T) {
	from, to := RGBA(200, 40, 30, 160), RGBA(30, 90, 210, 255)
	for _, tt := range []struct {
		t    float64
		want Color
	}{
		{0, from},
		{1, to},
		{0.5, refLerp(from, to, 0.5)},
		{0.25, refLerp(from, to, 0.25)},
		{0.75, refLerp(from, to, 0.75)},
	} {
		if got := lerp(from, to, tt.t); !blendNear(got, tt.want, 1) {
			t.Errorf("lerp at t=%v = %#08x, want %#08x (+/-1) - the ramp must interpolate premultiplied channels",
				tt.t, uint32(got), uint32(tt.want))
		}
	}
}

// TestResamplePremultiplied pins the resampler half of the DrawImage
// cache story: a constant premultiplied field survives exactly, an
// edge interpolates in premultiplied space (channels never exceed
// alpha), and the blit of a resampled raster composites by the book.
func TestResamplePremultiplied(t *testing.T) {
	col := RGBA(200, 100, 50, 128)
	t.Run("constant field survives exactly", func(t *testing.T) {
		dst := Resample(blendConstant(col, 6, 6), image.Rect(0, 0, 6, 6), 3, 3)
		for y := range 3 {
			for x := range 3 {
				c := dst.RGBAAt(x, y)
				got := Color(uint32(c.A)<<24 | uint32(c.R)<<16 | uint32(c.G)<<8 | uint32(c.B))
				if got != col {
					t.Errorf("resampled (%d,%d) = %#08x, want the constant %#08x exactly", x, y, uint32(got), uint32(col))
				}
			}
		}
	})

	t.Run("opaque-to-transparent edge interpolates in premul space", func(t *testing.T) {
		src := image.NewRGBA(image.Rect(0, 0, 1, 2))
		set := func(y int, c Color) {
			o := src.PixOffset(0, y)
			src.Pix[o], src.Pix[o+1], src.Pix[o+2], src.Pix[o+3] = c.R(), c.G(), c.B(), c.A()
		}
		set(0, RGB(255, 0, 0))
		set(1, 0)
		dst := Resample(src, src.Bounds(), 1, 8)
		for y := range 8 {
			c := dst.RGBAAt(0, y)
			if int(c.R) > int(c.A)+1 {
				t.Errorf("resampled row %d has R=%d above alpha=%d - the kernel output is not premultiplied", y, c.R, c.A)
			}
			if y == 0 && c.A < 250 {
				t.Errorf("first resampled row alpha = %d, want the opaque end", c.A)
			}
			if y == 7 && c.A > 5 {
				t.Errorf("last resampled row alpha = %d, want the transparent end", c.A)
			}
		}
	})

	t.Run("blit of a resampled raster composites by the book", func(t *testing.T) {
		dst := Resample(blendConstant(col, 4, 4), image.Rect(0, 0, 4, 4), 6, 6)
		cv, data := newTestCanvas(16, 16)
		cv.Clear(cv.Rect(), bgUnder)
		cv.DrawImageDevice(dst, 4, 4)
		for _, p := range [][2]int{{4, 4}, {9, 9}, {6, 7}} {
			got := pxAt(data, Stride(16), p[0], p[1])
			if !blendNear(got, refOver(col, bgUnder), 1) {
				t.Errorf("pixel (%d,%d) = %#08x, want resampled source over bg = %#08x (+/-1)",
					p[0], p[1], uint32(got), uint32(refOver(col, bgUnder)))
			}
		}
		if got := pxAt(data, Stride(16), 3, 4); got != bgUnder {
			t.Errorf("outside the blit = %#08x, want untouched background", uint32(got))
		}
	})
}

// --- property replay ---------------------------------------------------------

// xorshift is the tiny deterministic RNG driving the property replay:
// 64-bit xorshift, seeded nonzero, no dependencies.
type xorshift uint64

func (x *xorshift) next() uint64 {
	v := uint64(*x)
	v ^= v << 13
	v ^= v >> 7
	v ^= v << 17
	*x = xorshift(v)
	return v
}

func (x *xorshift) u8() uint8 { return uint8(x.next() >> 56) }

func randomColor(x *xorshift) Color { return RGBA(x.u8(), x.u8(), x.u8(), x.u8()) }

// TestBlendPropertyPairs replays the whole table over N color pairs:
// a handful of hand-picked edge pairs (opaque on transparent, both
// transparent, near-equal channels) then hundreds of xorshift-random
// premultiplied pairs, each pair randomizing both source and
// background. Any drift class the table catches by hand, this catches
// automatically. It also cross-checks the oracle family itself:
// production over() against refOver, and refOver against the
// straight-alpha boundary path.
func TestBlendPropertyPairs(t *testing.T) {
	var rows []blendRow
	for _, row := range blendRows(t) {
		if !row.custom { // the emoji strike has no paint color to vary
			rows = append(rows, row)
		}
	}

	x := xorshift(0x2545F4914F6CDD1D)
	pair := func(i int, src, bg Color) {
		ctx := fmt.Sprintf("pair %d (src %#08x, bg %#08x)", i, uint32(src), uint32(bg))
		for _, row := range rows {
			checkRow(t, row, src, bg, false, row.name+": "+ctx)
		}
	}

	edges := []struct{ src, bg Color }{
		{RGB(255, 255, 255), 0},
		{0, RGB(255, 255, 255)},
		{0, 0},
		{RGB(1, 2, 3), RGB(254, 253, 252)},
		{RGBA(128, 128, 128, 128), bgUnder},
		{RGBA(255, 255, 255, 128), RGB(0, 0, 0)},
	}
	i := 0
	for _, e := range edges {
		pair(i, e.src, e.bg)
		i++
	}
	for ; i < 256; i++ {
		pair(i, randomColor(&x), randomColor(&x))
	}

	// Oracle hygiene: production over() must track the float reference
	// within its single rounding, and the float reference must track
	// the straight-alpha boundary path within its double quantization.
	x = xorshift(0x9E3779B97F4A7C15)
	for i := range 4096 {
		src, bg := randomColor(&x), randomColor(&x)
		if got := src.over(bg); !blendNear(got, refOver(src, bg), 1) {
			t.Errorf("pair %d: over(%#08x, %#08x) = %#08x, want %#08x (+/-1)",
				i, uint32(src), uint32(bg), uint32(got), uint32(refOver(src, bg)))
		}
		if got, want := refOver(src, bg), refOverStraight(src, bg); !blendNear(got, want, 3) {
			t.Errorf("pair %d: premul oracle %#08x vs straight-path oracle %#08x drifted past 3", i, uint32(got), uint32(want))
		}
	}
}
