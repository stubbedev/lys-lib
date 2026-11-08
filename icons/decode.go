package icons

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"image"
	"io"
	"path/filepath"
	"strings"

	"github.com/stubbedev/lys-lib/render"
)

// FileLimit is the most an icon file or a theme index may hold.
const FileLimit = 8 << 20

// currentColor is the SVG paint keyword that means "the context's
// foreground". oksvg rejects it in strict mode, so DecodeImage rewrites
// it to opaque white before parsing; recoloring goes through
// render.Icon.Tint, which keeps only the alpha.
const currentColor = "currentColor"

// DecodeImage decodes icon data named by fileName into a w x h pixel
// box. The extension picks the codec: .png, .svg, or .svgz (gzipped
// SVG); anything else sniffs the PNG magic and falls back to SVG. A
// currentColor paint decodes as white - pass the result through
// render.Icon.Tint, Cache.SymbolicIcon, or Cache.Tinted to color it.
func DecodeImage(data []byte, fileName string, w, h int) (*render.Icon, error) {
	ext := strings.ToLower(filepath.Ext(fileName))
	if ext == ".svgz" || ext == ".gz" {
		unzipped, err := gunzip(data)
		if err != nil {
			return nil, fmt.Errorf("icons: decode %s: %w", fileName, err)
		}
		data, ext = unzipped, ".svg"
	}
	if bytes.Contains(data, []byte(currentColor)) {
		data = bytes.ReplaceAll(data, []byte(currentColor), []byte("#ffffff"))
	}
	if ext == ".png" || (ext != ".svg" && hasPNGMagic(data)) {
		return render.LoadPNG(data, w, h)
	}
	if ext == ".jpg" || ext == ".jpeg" || hasJPEGMagic(data) {
		return render.LoadJPEG(data, w, h)
	}
	return render.LoadSVG(data, w, h)
}

// DecodeIcon decodes icon data named by fileName for a size x size
// logical box on a surface at the 120-based scale frac120: rasterized
// into the DeviceBox(size, frac120) pixel box and marked with that
// scale, so it draws one to one there and covers size logical pixels
// anywhere.
func DecodeIcon(data []byte, fileName string, size int, frac120 uint32) (*render.Icon, error) {
	if frac120 == 0 {
		frac120 = fracDenom
	}
	dev := DeviceBox(size, frac120)
	ic, err := DecodeImage(data, fileName, dev, dev)
	if err != nil {
		return nil, err
	}
	return ic.AtScale(int(frac120), fracDenom), nil
}

// TintInto writes src recolored to tint into dst, a raster of its size:
// the pixels render.Icon.Tint makes, without a raster of their own.
func TintInto(dst, src *image.RGBA, tint render.Color) {
	tr, tg, tb, ta := uint32(tint.R()), uint32(tint.G()), uint32(tint.B()), uint32(tint.A())
	w, h := src.Rect.Dx(), src.Rect.Dy()
	for y := range h {
		from := src.Pix[y*src.Stride:][:4*w]
		to := dst.Pix[y*dst.Stride:][:4*w]
		for x := 3; x < len(from); x += 4 {
			a := uint32(from[x])
			to[x-3] = uint8(min(tr*a/255, 255))
			to[x-2] = uint8(min(tg*a/255, 255))
			to[x-1] = uint8(min(tb*a/255, 255))
			to[x] = uint8(min(ta*a/255, 255))
		}
	}
}

// hasJPEGMagic reports whether data starts with the JPEG SOI marker.
func hasJPEGMagic(data []byte) bool {
	return len(data) >= 3 && data[0] == 0xFF && data[1] == 0xD8 && data[2] == 0xFF
}

// IsSymbolic reports whether an icon is symbolic and should follow the
// theme accent: its file stem carries -symbolic, or the SVG paints with
// currentColor.
func IsSymbolic(fileName string, data []byte) bool {
	stem := strings.TrimSuffix(fileName, filepath.Ext(fileName))
	if strings.Contains(stem, symbolicSuffix) {
		return true
	}
	return bytes.Contains(data, []byte(currentColor))
}

// pngMagic is the first bytes of every PNG file.
var pngMagic = []byte{0x89, 'P', 'N', 'G'}

func hasPNGMagic(data []byte) bool {
	return bytes.HasPrefix(data, pngMagic)
}

func gunzip(data []byte) ([]byte, error) {
	zr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	out, err := io.ReadAll(zr)
	if err != nil {
		return nil, err
	}
	if err := zr.Close(); err != nil {
		return nil, err
	}
	return out, nil
}
