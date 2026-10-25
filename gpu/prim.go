package gpu

import (
	"image"
	"math"

	"github.com/stubbedev/lys-ui/render"
)

// prim is one drawn primitive, prim.glsl's Prim: a device-pixel quad
// and what fills it. Its layout is std430's, all 16-byte vectors.
type prim struct {
	quad, box, radii, inner, iradii, widths, m0, m1, m2 [4]float32
	cols                                                [4]uint32
	// info is the kind, the opacity factor (0..255), the brightness
	// as float bits, and the flags.
	info [4]uint32
}

// primSize is a prim's size in bytes.
const primSize = 176

// The primitive kinds, prim.frag's.
const (
	kindRect uint32 = iota
	kindRRect
	kindRounded
	kindBorder
	kindTexture
	kindMask
	kindComposite
	kindStretch
	kindImage
	kindGradient
	kindLine
	kindArc
	kindCover
	kindMatrix
	kindCrossFade
	kindMaskLayer
	kindPathMask
	kindBoxH
	kindBoxV
	kindDownsample
	kindUpsample
	kindSilhouette
	kindShadow
	kindGrain
)

// The primitive flags, prim.frag's.
const (
	// flagRaw writes the color as is: no brightness, no opacity.
	flagRaw uint32 = 1 << iota
	// flagOpaqueStores stores an opaque texel unshaded at full
	// opacity, as render's canvas-to-canvas copies do.
	flagOpaqueStores
	// flagNoBright skips the brightness, as render's DrawLayer does.
	flagNoBright
)

// alphaScale is render's opacity factor for a: round-half-up to
// 0..255.
func alphaScale(a float64) uint32 {
	return uint32(min(255, max(0, math.Round(a*255))))
}

// shaded is a prim of kind under s's opacity and brightness.
func shaded(kind uint32, s render.State, flags uint32) prim {
	return prim{info: [4]uint32{kind, alphaScale(s.Alpha), math.Float32bits(float32(s.Bright)), flags}}
}

func quadOf(r render.Rect) [4]float32 {
	return [4]float32{float32(r.X), float32(r.Y), float32(r.X + r.W), float32(r.Y + r.H)}
}

func vec(v [4]float64) [4]float32 {
	return [4]float32{float32(v[0]), float32(v[1]), float32(v[2]), float32(v[3])}
}

// mappedRows are m's inverse rows and forward last row as prim
// vectors, the last component zero.
func mappedRows(m *render.Mapped) (r0, r1, r2, fwd [4]float32) {
	v := func(r [3]float32) [4]float32 { return [4]float32{r[0], r[1], r[2]} }
	return v(m.Inv[0]), v(m.Inv[1]), v(m.Inv[2]), v(m.Fwd)
}

func rectOf(r render.Rect) image.Rectangle { return image.Rect(r.X, r.Y, r.X+r.W, r.Y+r.H) }
