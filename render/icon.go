package render

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"

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
	icon, err := oksvg.ReadIconStream(bytes.NewReader(data), oksvg.StrictErrorMode)
	if err != nil {
		return nil, fmt.Errorf("render: parse svg: %w", err)
	}
	if icon.ViewBox.W <= 0 || icon.ViewBox.H <= 0 {
		return nil, errors.New("render: svg has no usable viewBox (want w x h > 0)")
	}

	scale := math.Min(float64(w)/icon.ViewBox.W, float64(h)/icon.ViewBox.H)
	fitW := icon.ViewBox.W * scale
	fitH := icon.ViewBox.H * scale
	offX := (float64(w) - fitW) / 2
	offY := (float64(h) - fitH) / 2

	img := image.NewRGBA(image.Rect(0, 0, w, h))
	scanner := rasterx.NewScannerGV(w, h, img, img.Bounds())
	dasher := rasterx.NewDasher(w, h, scanner)
	icon.SetTarget(offX, offY, fitW, fitH)
	icon.Draw(dasher, 1.0)
	return &Icon{img: img}, nil
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
