package render

// Color is a premultiplied ARGB8888 pixel value, laid out as 0xAARRGGBB.
// Premultiplied is the native format of both the wl_shm buffers and Go's
// image.RGBA, so colors pass through blending without conversion.
type Color uint32

// RGB builds an opaque color from non-premultiplied components.
func RGB(r, g, b uint8) Color {
	return RGBA(r, g, b, 255)
}

// RGBA builds a color from non-premultiplied components and premultiplies
// them.
func RGBA(r, g, b, a uint8) Color {
	rr, gg, bb := uint32(r), uint32(g), uint32(b)
	aa := uint32(a)
	rr = rr * aa / 255
	gg = gg * aa / 255
	bb = bb * aa / 255
	return Color(aa<<24 | rr<<16 | gg<<8 | bb)
}

// A returns the alpha channel.
func (c Color) A() uint8 { return uint8(c >> 24) }

// R returns the premultiplied red channel.
func (c Color) R() uint8 { return uint8(c >> 16) }

// G returns the premultiplied green channel.
func (c Color) G() uint8 { return uint8(c >> 8) }

// B returns the premultiplied blue channel.
func (c Color) B() uint8 { return uint8(c) }

// ColorFromBytes decodes a premultiplied color from the wl_shm byte
// order: little-endian ARGB8888, that is bytes B, G, R, A.
func ColorFromBytes(b []byte) Color {
	return Color(uint32(b[3])<<24 | uint32(b[2])<<16 | uint32(b[1])<<8 | uint32(b[0]))
}

// Straight returns the color as non-premultiplied bytes in R, G, B, A
// order, as PNG and most image code expect.
func (c Color) Straight() [4]byte {
	a := uint32(c.A())
	if a == 0 {
		return [4]byte{}
	}
	unpremul := func(p uint8) uint8 {
		return uint8((uint32(p)*255 + a/2) / a)
	}
	return [4]byte{unpremul(c.R()), unpremul(c.G()), unpremul(c.B()), uint8(a)}
}

// over returns the source-over composition of src onto dst. Both colors
// are premultiplied; the result is exact integer math with rounding.
func (src Color) over(dst Color) Color {
	sa := uint32(src.A())
	keep := 255 - sa
	da := (uint32(dst.A())*keep + 127) / 255
	dr := (uint32(dst.R())*keep + 127) / 255
	dg := (uint32(dst.G())*keep + 127) / 255
	db := (uint32(dst.B())*keep + 127) / 255
	return Color((sa+da)<<24 |
		(uint32(src.R())+dr)<<16 |
		(uint32(src.G())+dg)<<8 |
		(uint32(src.B()) + db))
}

// lerp returns the premultiplied interpolation of a and b at t in [0, 1].
// Interpolating premultiplied channels is the correct gradient arithmetic.
func lerp(a, b Color, t float64) Color {
	lerpCh := func(x, y uint8) uint32 {
		v := float64(x) + (float64(y)-float64(x))*t
		return uint32(v + 0.5)
	}
	return Color(lerpCh(a.A(), b.A())<<24 |
		lerpCh(a.R(), b.R())<<16 |
		lerpCh(a.G(), b.G())<<8 |
		lerpCh(a.B(), b.B()))
}
