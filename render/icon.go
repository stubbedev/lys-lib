package render

import (
	"bytes"
	"errors"
	"fmt"
	"image"
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
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	xdraw.CatmullRom.Scale(img, img.Bounds(), src, src.Bounds(), xdraw.Over, nil)
	return &Icon{img: img}, nil
}

// Size returns the rasterized icon size in pixels.
func (i *Icon) Size() (int, int) {
	b := i.img.Bounds()
	return b.Dx(), b.Dy()
}

// Draw blends the icon with its top-left corner at (x, y).
func (i *Icon) Draw(cv *Canvas, x, y int) {
	cv.DrawImage(i.img, x, y)
}
