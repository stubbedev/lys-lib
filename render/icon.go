package render

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"math"
	"regexp"
	"strings"

	"github.com/srwiley/oksvg"
	"github.com/srwiley/rasterx"
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
	icon, err := oksvg.ReadIconStream(bytes.NewReader(dropForeignAttrs(data)), oksvg.StrictErrorMode)
	if err != nil {
		return nil, fmt.Errorf("render: parse svg: %w", err)
	}
	if icon.ViewBox.W <= 0 || icon.ViewBox.H <= 0 {
		return nil, errors.New("render: svg has no usable viewBox (want w x h > 0)")
	}
	applyFillRules(icon, data)

	scale := math.Min(float64(w)/icon.ViewBox.W, float64(h)/icon.ViewBox.H)
	fitW := icon.ViewBox.W * scale
	fitH := icon.ViewBox.H * scale
	offX := (float64(w) - fitW) / 2
	offY := (float64(h) - fitH) / 2

	img := image.NewRGBA(image.Rect(0, 0, w, h))
	dasher := rasterx.NewDasher(w, h, newSVGScanner(w, h, img))
	icon.SetTarget(offX, offY, fitW, fitH)
	icon.Draw(dasher, 1.0)
	return &Icon{img: img}, nil
}

// foreignAttr is an attribute in another vocabulary's namespace
// (prefix:name, the xml, xmlns and xlink prefixes excepted), such as
// GTK's symbolic-icon markup: gpa:fill='foreground'.
var foreignAttr = regexp.MustCompile(`\s(?:[A-Za-z_][\w.-]*):[A-Za-z_][\w.-]*\s*=\s*(?:'[^']*'|"[^"]*")`)

// dropForeignAttrs removes the attributes oksvg would misread as its
// own: it ignores namespaces, so gpa:fill='foreground' overrode the
// real fill and failed the parse. The SVG vocabulary keeps its
// meaning; GTK symbolic recoloring is the tint gelm applies anyway.
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

// svgShapes are the elements oksvg draws as one path each.
var svgShapes = map[string]bool{
	"path": true, "rect": true, "circle": true, "ellipse": true,
	"polygon": true, "polyline": true, "line": true,
}

// applyFillRules sets the even-odd rule on the paths whose element
// asks for it: oksvg parses no fill-rule, so a ring drawn as an outer
// and an inner subpath (an icon frame) filled solid. The shapes are
// matched to oksvg's paths in document order; a document whose shapes
// do not line up one to one (a <use>, an empty shape) keeps the
// nonzero rule throughout.
func applyFillRules(icon *oksvg.SvgIcon, data []byte) {
	rules, ok := evenOddShapes(data)
	if !ok || len(rules) != len(icon.SVGPaths) {
		return
	}
	for i, evenOdd := range rules {
		if evenOdd {
			icon.SVGPaths[i].UseNonZeroWinding = false
		}
	}
}

// evenOddShapes lists, in document order, whether each drawn shape
// fills even-odd: its own fill-rule (attribute or style) or the one it
// inherits. Shapes inside <defs> are not drawn; a <use> makes the
// order unknowable (ok false).
func evenOddShapes(data []byte) (rules []bool, ok bool) {
	d := xml.NewDecoder(bytes.NewReader(data))
	d.Strict = false
	var stack []bool
	defs := 0
	for {
		tok, err := d.Token()
		if errors.Is(err, io.EOF) {
			return rules, true
		}
		if err != nil {
			return nil, false
		}
		switch t := tok.(type) {
		case xml.StartElement:
			rule := len(stack) > 0 && stack[len(stack)-1]
			if r, set := fillRuleOf(t.Attr); set {
				rule = r
			}
			stack = append(stack, rule)
			switch name := t.Name.Local; {
			case name == "use":
				return nil, false
			case name == "defs":
				defs++
			case defs == 0 && svgShapes[name]:
				rules = append(rules, rule)
			}
		case xml.EndElement:
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
			if t.Name.Local == "defs" {
				defs--
			}
		}
	}
}

// fillRuleOf reads an element's fill-rule: the style declaration wins
// over the presentation attribute, as in CSS.
func fillRuleOf(attrs []xml.Attr) (evenOdd, set bool) {
	for _, a := range attrs {
		if a.Name.Space == "" && a.Name.Local == "fill-rule" {
			evenOdd, set = strings.TrimSpace(a.Value) == "evenodd", true
		}
	}
	for _, a := range attrs {
		if a.Name.Local != "style" {
			continue
		}
		for decl := range strings.SplitSeq(a.Value, ";") {
			k, v, found := strings.Cut(decl, ":")
			if found && strings.TrimSpace(k) == "fill-rule" {
				evenOdd, set = strings.TrimSpace(v) == "evenodd", true
			}
		}
	}
	return evenOdd, set
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
// center, the center staying where the unrotated draw's center is.
// Destination pixels inverse-map into the icon with nearest sampling;
// the corners past the rotated bounds stay transparent.
func (i *Icon) DrawRotated(cv *Canvas, x, y int, deg float64) {
	i.DrawXformed(cv, x, y, Rotate(deg))
}

// DrawXformed blends the icon through the affine m, applied about the
// icon's center: destination pixels inverse-map into the icon with
// nearest sampling; corners past the transformed bounds stay
// transparent. The identity draws straight.
func (i *Icon) DrawXformed(cv *Canvas, x, y int, m Affine) {
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
	// The transformed footprint: the icon's corners mapped forward,
	// padded a device pixel.
	var x0, y0, x1, y1 float64
	for _, c := range [][2]float64{{0, 0}, {float64(w), 0}, {0, float64(h)}, {float64(w), float64(h)}} {
		px, py := m.Apply(c[0]-float64(w)/2, c[1]-float64(h)/2)
		x0 = math.Min(x0, px)
		y0 = math.Min(y0, py)
		x1 = math.Max(x1, px)
		y1 = math.Max(y1, py)
	}
	// A pixel of padding: nearest sampling reaches one device pixel
	// past the exact footprint at the edges and corners.
	x0, y0, x1, y1 = x0-1, y0-1, x1+1, y1+1
	num, denom := cv.DeviceScale()
	fx := func(l float64) int { return divFloor(int(l*float64(num)), denom) }
	for py := fx(cy + y0); py <= fx(cy+y1)+1; py++ {
		for px := fx(cx + x0); px <= fx(cx+x1)+1; px++ {
			if !cv.clip.Contains(px, py) {
				continue
			}
			// The device pixel's logical center, offset from the icon
			// center and inverse-mapped into icon coordinates.
			lx := (float64(px)+0.5)*float64(denom)/float64(num) - cx
			ly := (float64(py)+0.5)*float64(denom)/float64(num) - cy
			sx, sy := inv.Apply(lx, ly)
			ix, iy := int(sx)+w/2, int(sy)+h/2
			if ix < 0 || iy < 0 || ix >= w || iy >= h {
				continue
			}
			sr, sg, sb, sa := i.img.At(b.Min.X+ix, b.Min.Y+iy).RGBA()
			if sa == 0 {
				continue
			}
			cv.blend(px, py, Color(sa>>8<<24|sr>>8<<16|sg>>8<<8|sb>>8))
		}
	}
}
