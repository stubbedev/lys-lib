package render

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"math"
	"regexp"

	xdraw "golang.org/x/image/draw"
)

// Icon is a rasterized icon ready to blend onto a canvas.
type Icon struct {
	img *image.RGBA
}

// LoadSVG rasterizes SVG icon data at w x h pixels. The icon's viewBox is
// scaled to fit inside that box with its aspect ratio kept and centered;
// the rest stays transparent. Unsupported SVG elements are an error, not
// silently dropped.
func LoadSVG(data []byte, w, h int) (*Icon, error) {
	if w <= 0 || h <= 0 {
		return nil, fmt.Errorf("render: invalid icon size %dx%d", w, h)
	}
	doc, err := parseSVG(data)
	if err != nil {
		return nil, err
	}
	vb := doc.icon.ViewBox
	if vb.W <= 0 || vb.H <= 0 {
		return nil, errors.New("render: svg has no usable viewBox (want w x h > 0)")
	}

	scale := math.Min(float64(w)/vb.W, float64(h)/vb.H)
	fitW := vb.W * scale
	fitH := vb.H * scale
	offX := (float64(w) - fitW) / 2
	offY := (float64(h) - fitH) / 2

	img := image.NewRGBA(image.Rect(0, 0, w, h))
	doc.draw(img, fitViewBox(vb.X, vb.Y, vb.W, vb.H, offX, offY, fitW, fitH))
	return &Icon{img: img}, nil
}

// rasterSVG draws doc's region (rx, ry, rw, rh in its user units)
// into a w x h image.
func rasterSVG(doc *svgDoc, rx, ry, rw, rh float64, w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	doc.draw(img, fitViewBox(rx, ry, rw, rh, 0, 0, float64(w), float64(h)))
	return img
}

// foreignAttr is an attribute in another vocabulary's namespace
// (prefix:name, the xml, xmlns and xlink prefixes excepted), such as
// GTK's symbolic-icon markup: gpa:fill='foreground'.
var foreignAttr = regexp.MustCompile(`\s(?:[A-Za-z_][\w.-]*):[A-Za-z_][\w.-]*\s*=\s*(?:'[^']*'|"[^"]*")`)

// dropForeignAttrs removes the attributes oksvg would misread as its
// own: it ignores namespaces, so gpa:fill='foreground' overrode the
// real fill and failed the parse. The SVG vocabulary keeps its
// meaning; GTK symbolic recoloring is the tint lys-ui applies anyway.
func dropForeignAttrs(data []byte) []byte {
	return foreignAttr.ReplaceAllFunc(data, func(m []byte) []byte {
		name := bytes.TrimSpace(m)
		for _, keep := range [][]byte{[]byte("xml:"), []byte("xmlns:"), []byte("xlink:")} {
			if bytes.HasPrefix(name, keep) {
				return m
			}
		}
		return nil
	})
}

// LoadPNG decodes PNG icon data and scales it to w x h pixels.
func LoadPNG(data []byte, w, h int) (*Icon, error) {
	if w <= 0 || h <= 0 {
		return nil, fmt.Errorf("render: invalid icon size %dx%d", w, h)
	}
	src, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("render: decode png: %w", err)
	}
	return IconFromImage(src, w, h)
}

// LoadJPEG decodes JPEG icon data and scales it to w x h pixels.
func LoadJPEG(data []byte, w, h int) (*Icon, error) {
	if w <= 0 || h <= 0 {
		return nil, fmt.Errorf("render: invalid icon size %dx%d", w, h)
	}
	src, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("render: decode jpeg: %w", err)
	}
	return IconFromImage(src, w, h)
}

// IconFromImage scales any decoded image to w x h pixels, the same
// resampling LoadPNG applies. It is the entry point for pixels that
// did not come from a file: a decoded image the caller already holds,
// or a raster protocol payload converted to an image.
func IconFromImage(src image.Image, w, h int) (*Icon, error) {
	if w <= 0 || h <= 0 {
		return nil, fmt.Errorf("render: invalid icon size %dx%d", w, h)
	}
	if src == nil || src.Bounds().Empty() {
		return nil, errors.New("render: icon image is empty")
	}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	xdraw.CatmullRom.Scale(img, img.Bounds(), src, src.Bounds(), xdraw.Over, nil)
	return &Icon{img: img}, nil
}

// IconFromARGB32 decodes a width x height raster of 32-bit ARGB pixels
// in network byte order (A, R, G, B per pixel, straight alpha, rows
// top to bottom with no padding) and scales it to w x h pixels. This
// is the StatusNotifierItem IconPixmap wire format (also the
// _NET_WM_ICON payload once byte-swapped to big-endian). data shorter
// than width*height*4 is an error, not a partial icon; trailing bytes
// past the raster are ignored.
func IconFromARGB32(width, height int, data []byte, w, h int) (*Icon, error) {
	if width <= 0 || height <= 0 {
		return nil, fmt.Errorf("render: invalid ARGB32 raster %dx%d", width, height)
	}
	need := width * height * 4
	if len(data) < need {
		return nil, fmt.Errorf("render: ARGB32 raster %dx%d needs %d bytes, got %d", width, height, need, len(data))
	}
	src := image.NewNRGBA(image.Rect(0, 0, width, height))
	for i := 0; i < need; i += 4 {
		src.Pix[i+0] = data[i+1]
		src.Pix[i+1] = data[i+2]
		src.Pix[i+2] = data[i+3]
		src.Pix[i+3] = data[i+0]
	}
	return IconFromImage(src, w, h)
}

// Size returns the rasterized icon size in pixels.
func (i *Icon) Size() (int, int) {
	b := i.img.Bounds()
	return b.Dx(), b.Dy()
}

// Tint returns a copy of the icon recolored to tint: every pixel keeps
// its alpha (anti-aliasing, opacity) and takes tint's color, so the
// source works as a mask. This is how symbolic icons follow the theme
// accent. The limits are the mask approach's limits: ink comes out one
// flat color, gradient hue variation collapses to its alpha ramp, and
// multi-color art goes monochrome - which is the symbolic-icon
// contract. A translucent tint yields a translucent result.
func (i *Icon) Tint(tint Color) *Icon {
	src := i.img
	w, h := src.Bounds().Dx(), src.Bounds().Dy()
	out := image.NewRGBA(image.Rect(0, 0, w, h))
	tr, tg, tb, ta := uint32(tint.R()), uint32(tint.G()), uint32(tint.B()), uint32(tint.A())
	for y := range h {
		for x := range w {
			a := uint32(src.RGBAAt(x, y).A)
			o := out.PixOffset(x, y)
			out.Pix[o+0] = uint8(tr * a / 255)
			out.Pix[o+1] = uint8(tg * a / 255)
			out.Pix[o+2] = uint8(tb * a / 255)
			out.Pix[o+3] = uint8(ta * a / 255)
		}
	}
	return &Icon{img: out}
}

// At returns the pixel at (x, y) in icon coordinates.
func (i *Icon) At(x, y int) color.Color { return i.img.At(x, y) }

// Draw blends the icon with its top-left corner at (x, y).
func (i *Icon) Draw(cv *Canvas, x, y int) {
	cv.DrawImage(i.img, x, y)
}

// DrawRotated blends the icon turned deg degrees clockwise about its
// center, the center staying where the unrotated draw's center is,
// sampled as DrawXformed samples.
func (i *Icon) DrawRotated(cv *Canvas, x, y int, deg float64) {
	i.DrawXformed(cv, x, y, Rotate(deg))
}

// xformSpan is the device rect DrawXformed visits for a non-identity
// m: the icon's corners mapped forward about its center, padded a
// device pixel, since bilinear sampling reaches one past the exact
// footprint at the edges and corners.
func (i *Icon) xformSpan(x, y int, m Affine, num, denom int) Rect {
	b := i.img.Bounds()
	w, h := b.Dx(), b.Dy()
	cx := float64(x) + float64(w)/2
	cy := float64(y) + float64(h)/2
	var x0, y0, x1, y1 float64
	for _, c := range [][2]float64{{0, 0}, {float64(w), 0}, {0, float64(h)}, {float64(w), float64(h)}} {
		px, py := m.Apply(c[0]-float64(w)/2, c[1]-float64(h)/2)
		x0 = math.Min(x0, px)
		y0 = math.Min(y0, py)
		x1 = math.Max(x1, px)
		y1 = math.Max(y1, py)
	}
	x0, y0, x1, y1 = x0-1, y0-1, x1+1, y1+1
	fx := func(l float64) int { return divFloor(int(l*float64(num)), denom) }
	l, t := fx(cx+x0), fx(cy+y0)
	return Rect{X: l, Y: t, W: fx(cx+x1) + 2 - l, H: fx(cy+y1) + 2 - t}
}

// DrawXformed blends the icon through the affine m, applied about the
// icon's center: each destination pixel's center inverse-maps into the
// icon and samples it bilinearly, so turned edges antialias and a
// symmetric icon turns about its exact center; past its edges it is
// transparent. The identity draws straight.
func (i *Icon) DrawXformed(cv *Canvas, x, y int, m Affine) {
	if cv.rec != nil {
		cv.record(op{kind: opIcon, icon: i, i: [5]int{x, y}, aff: m})
		return
	}
	if m == Identity {
		i.Draw(cv, x, y)
		return
	}
	inv, ok := m.Invert()
	if !ok {
		return
	}
	b := i.img.Bounds()
	w, h := b.Dx(), b.Dy()
	cx := float64(x) + float64(w)/2
	cy := float64(y) + float64(h)/2
	num, denom := cv.DeviceScale()
	span := i.xformSpan(x, y, m, num, denom)
	full, texel := Rect{W: w, H: h}, i.texel
	for py := span.Y; py < span.Y+span.H; py++ {
		for px := span.X; px < span.X+span.W; px++ {
			if !cv.clip.Contains(px, py) {
				continue
			}
			// The device pixel's logical center, offset from the icon
			// center and inverse-mapped into icon coordinates.
			lx := (float64(px)+0.5)*float64(denom)/float64(num) - cx
			ly := (float64(py)+0.5)*float64(denom)/float64(num) - cy
			sx, sy := inv.Apply(lx, ly)
			if col := bilinear(full, sx+float64(w)/2-0.5, sy+float64(h)/2-0.5, texel); col != 0 {
				cv.blend(px, py, col)
			}
		}
	}
}

// texel is the premultiplied pixel at (x, y) from the icon's origin.
func (i *Icon) texel(x, y int) Color {
	p := i.img.Pix[i.img.PixOffset(i.img.Rect.Min.X+x, i.img.Rect.Min.Y+y):]
	return Color(uint32(p[3])<<24 | uint32(p[0])<<16 | uint32(p[1])<<8 | uint32(p[2]))
}
