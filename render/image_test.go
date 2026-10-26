package render

import (
	"bytes"
	"image"
	"image/color"
	"math"
	"testing"
)

// placeCentered is PlaceImage at the default position, read as the
// source rect and drawn size the policy tests compare.
func placeCentered(srcW, srcH, boxW, boxH int, s ImageScale) (image.Rectangle, int, int) {
	src, dst := PlaceImage(srcW, srcH, boxW, boxH, s, ImageCentered)
	return src, dst.W, dst.H
}

// TestPlaceImagePositions pins object-position: the object's origin at
// a fraction of the free space plus an offset, the box showing only
// what lands inside it.
func TestPlaceImagePositions(t *testing.T) {
	tests := []struct {
		name       string
		srcW, srcH int
		boxW, boxH int
		s          ImageScale
		pos        ImagePos
		wantSrc    image.Rectangle
		wantDst    Rect
	}{
		{"none at left bottom", 4, 4, 10, 10, ImageNone, ImagePos{X: 0, Y: 1}, image.Rect(0, 0, 4, 4), Rect{X: 0, Y: 6, W: 4, H: 4}},
		{"none overflowing at left top", 16, 16, 10, 10, ImageNone, ImagePos{}, image.Rect(0, 0, 10, 10), Rect{W: 10, H: 10}},
		{"none overflowing at right bottom", 16, 16, 10, 10, ImageNone, ImagePos{X: 1, Y: 1}, image.Rect(6, 6, 16, 16), Rect{W: 10, H: 10}},
		{"none offset past the top", 4, 4, 10, 10, ImageNone, ImagePos{OffX: 3, OffY: -2}, image.Rect(0, 2, 4, 4), Rect{X: 3, W: 4, H: 2}},
		{"fit at the bottom", 100, 50, 20, 40, ImageFit, ImagePos{Y: 1}, image.Rect(0, 0, 100, 50), Rect{Y: 30, W: 20, H: 10}},
		{"cover at the left", 4, 2, 10, 10, ImageCover, ImagePos{}, image.Rect(0, 0, 2, 2), Rect{W: 10, H: 10}},
		{"cover at the right", 4, 2, 10, 10, ImageCover, ImagePos{X: 1}, image.Rect(2, 0, 4, 2), Rect{W: 10, H: 10}},
		{"cover shifted right", 4, 2, 10, 10, ImageCover, ImagePos{OffX: 5}, image.Rect(0, 0, 1, 2), Rect{X: 5, W: 5, H: 10}},
		{"placed outside the box", 4, 4, 10, 10, ImageNone, ImagePos{OffX: 20}, image.Rectangle{}, Rect{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src, dst := PlaceImage(tt.srcW, tt.srcH, tt.boxW, tt.boxH, tt.s, tt.pos)
			if src != tt.wantSrc || dst != tt.wantDst {
				t.Errorf("PlaceImage = %v into %v, want %v into %v", src, dst, tt.wantSrc, tt.wantDst)
			}
		})
	}
}

func TestPlaceImageFit(t *testing.T) {
	tests := []struct {
		name         string
		srcW, srcH   int
		boxW, boxH   int
		wantSrc      image.Rectangle
		wantW, wantH int
	}{
		{"downscale, width binds", 100, 50, 20, 40, image.Rect(0, 0, 100, 50), 20, 10},
		{"aspect equal fills the box", 60, 30, 20, 10, image.Rect(0, 0, 60, 30), 20, 10},
		{"downscale, rounding on the free axis", 3, 2, 10, 10, image.Rect(0, 0, 3, 2), 10, 7},
		{"upscale, width binds", 7, 5, 10, 3, image.Rect(0, 0, 7, 5), 4, 3},
		{"upscale, square into square", 3, 3, 7, 7, image.Rect(0, 0, 3, 3), 7, 7},
		{"portrait source, landscape box", 2, 8, 16, 8, image.Rect(0, 0, 2, 8), 2, 8},
		{"zero source", 0, 5, 10, 10, image.Rectangle{}, 0, 0},
		{"zero box", 5, 5, 0, 10, image.Rectangle{}, 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src, w, h := placeCentered(tt.srcW, tt.srcH, tt.boxW, tt.boxH, ImageFit)
			if src != tt.wantSrc || w != tt.wantW || h != tt.wantH {
				t.Errorf("PlaceImage(%d,%d in %d,%d, fit) = %v, %dx%d, want %v, %dx%d",
					tt.srcW, tt.srcH, tt.boxW, tt.boxH, src, w, h, tt.wantSrc, tt.wantW, tt.wantH)
			}
		})
	}
}

func TestPlaceImageFitMatchesContainScale(t *testing.T) {
	// Sweep a grid of source and box sizes: the fit result must stay
	// inside the box and be exactly the contain scale, rounded, with a
	// floor at one pixel for extreme aspect ratios.
	for srcW := 1; srcW <= 33; srcW += 4 {
		for srcH := 1; srcH <= 33; srcH += 4 {
			for boxW := 1; boxW <= 20; boxW += 3 {
				for boxH := 1; boxH <= 20; boxH += 3 {
					_, w, h := placeCentered(srcW, srcH, boxW, boxH, ImageFit)
					s := math.Min(float64(boxW)/float64(srcW), float64(boxH)/float64(srcH))
					wantW := clampInt(int(math.Round(float64(srcW)*s)), 1, boxW)
					wantH := clampInt(int(math.Round(float64(srcH)*s)), 1, boxH)
					if w != wantW || h != wantH {
						t.Fatalf("fit %dx%d into %dx%d gave %dx%d, want %dx%d",
							srcW, srcH, boxW, boxH, w, h, wantW, wantH)
					}
				}
			}
		}
	}
}

func clampInt(v, lo, hi int) int { return min(hi, max(lo, v)) }

func TestPlaceImageCover(t *testing.T) {
	tests := []struct {
		name         string
		srcW, srcH   int
		boxW, boxH   int
		wantSrc      image.Rectangle
		wantW, wantH int
	}{
		{"wide source, square box: crop the sides", 4, 2, 10, 10, image.Rect(1, 0, 3, 2), 10, 10},
		{"tall source, square box: crop top and bottom", 2, 4, 10, 10, image.Rect(0, 1, 2, 3), 10, 10},
		{"matching aspect fills the box", 60, 30, 20, 10, image.Rect(0, 0, 60, 30), 20, 10},
		{"non-integer crop", 5, 3, 4, 4, image.Rect(1, 0, 4, 3), 4, 4},
		{"wide box, portrait source", 5, 5, 4, 2, image.Rect(0, 1, 5, 4), 4, 2},
		{"zero source", 0, 5, 10, 10, image.Rectangle{}, 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src, w, h := placeCentered(tt.srcW, tt.srcH, tt.boxW, tt.boxH, ImageCover)
			if src != tt.wantSrc || w != tt.wantW || h != tt.wantH {
				t.Errorf("PlaceImage(%d,%d in %d,%d, cover) = %v, %dx%d, want %v, %dx%d",
					tt.srcW, tt.srcH, tt.boxW, tt.boxH, src, w, h, tt.wantSrc, tt.wantW, tt.wantH)
			}
		})
	}
}

func TestPlaceImageCoverAlwaysFillsAndCropsCenter(t *testing.T) {
	for srcW := 1; srcW <= 30; srcW += 3 {
		for srcH := 1; srcH <= 30; srcH += 3 {
			for boxW := 1; boxW <= 24; boxW += 5 {
				for boxH := 1; boxH <= 24; boxH += 5 {
					src, w, h := placeCentered(srcW, srcH, boxW, boxH, ImageCover)
					if w != boxW || h != boxH {
						t.Fatalf("cover %dx%d into %dx%d gave %dx%d, must fill the box",
							srcW, srcH, boxW, boxH, w, h)
					}
					cropW, cropH := src.Dx(), src.Dy()
					if cropW <= 0 || cropH <= 0 || cropW > srcW || cropH > srcH {
						t.Fatalf("cover %dx%d into %dx%d sampled %v: outside the source",
							srcW, srcH, boxW, boxH, src)
					}
					// Centered crop: equal margins on both sides.
					if src.Min.X != (srcW-cropW)/2 || src.Min.Y != (srcH-cropH)/2 {
						t.Fatalf("cover %dx%d into %dx%d sampled %v: not centered",
							srcW, srcH, boxW, boxH, src)
					}
					// The crop carries the box's aspect ratio (within a
					// pixel of rounding).
					if math.Abs(float64(cropW)*float64(boxH)-float64(cropH)*float64(boxW)) > float64(boxH+boxW) {
						t.Fatalf("cover %dx%d into %dx%d sampled %v: crop lost the box ratio",
							srcW, srcH, boxW, boxH, src)
					}
				}
			}
		}
	}
}

func TestPlaceImageNone(t *testing.T) {
	tests := []struct {
		name         string
		srcW, srcH   int
		boxW, boxH   int
		wantSrc      image.Rectangle
		wantW, wantH int
	}{
		{"source smaller than box: all of it", 4, 4, 10, 10, image.Rect(0, 0, 4, 4), 4, 4},
		{"source bigger than box: centered clip", 16, 16, 10, 10, image.Rect(3, 3, 13, 13), 10, 10},
		{"odd sizes round down", 7, 7, 4, 4, image.Rect(1, 1, 5, 5), 4, 4},
		{"mixed: centered clip on the wide axis", 8, 3, 4, 10, image.Rect(2, 0, 6, 3), 4, 3},
		{"zero box", 4, 4, 0, 4, image.Rectangle{}, 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src, w, h := placeCentered(tt.srcW, tt.srcH, tt.boxW, tt.boxH, ImageNone)
			if src != tt.wantSrc || w != tt.wantW || h != tt.wantH {
				t.Errorf("PlaceImage(%d,%d in %d,%d, none) = %v, %dx%d, want %v, %dx%d",
					tt.srcW, tt.srcH, tt.boxW, tt.boxH, src, w, h, tt.wantSrc, tt.wantW, tt.wantH)
			}
		})
	}
}

// TestResampleBlendsAcrossEdges pins the resampler choice: a hard
// two-color edge upscaled 4x must blend across the boundary. A
// nearest-neighbor pass would produce only pure black and pure white
// blocks; the CatmullRom kernel mixes - and slightly overshoots - at
// every edge (stubbedev/lys-ui#14).
func TestResampleBlendsAcrossEdges(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 2, 1))
	src.SetRGBA(0, 0, color.RGBA{A: 0xff})
	src.SetRGBA(1, 0, color.RGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff})

	dst := Resample(src, src.Bounds(), 8, 4)
	if got := dst.Bounds(); got.Dx() != 8 || got.Dy() != 4 {
		t.Fatalf("resample size = %v, want 8x4", got)
	}
	// Nearest maps this pixel back to source column 0: pure black.
	// Only a kernel with support past the sample point puts ink here.
	p := dst.RGBAAt(2, 2)
	if p.A != 0xff {
		t.Fatalf("interior alpha = %d, want 255", p.A)
	}
	if p.R == 0 || p.R == 0xff {
		t.Fatalf("edge pixel R = %d, want a blend strictly between black and white", p.R)
	}
}

// TestResampleBeatsNearestOnRamps is the pixel-diff sanity check: on a
// smooth ramp, cubic resampling tracks the ideal box average far closer
// than nearest-neighbor sampling (which alias-steps across the ramp).
func TestResampleBeatsNearestOnRamps(t *testing.T) {
	const srcSize, dstSize, step = 64, 8, 8
	src := image.NewRGBA(image.Rect(0, 0, srcSize, srcSize))
	for y := range srcSize {
		for x := range srcSize {
			v := uint8(x * 4 % 256)
			src.SetRGBA(x, y, color.RGBA{R: v, G: v, B: v, A: 0xff})
		}
	}
	dst := Resample(src, src.Bounds(), dstSize, dstSize)

	mae := func(sample func(int, int) color.RGBA) float64 {
		sum := 0.0
		for y := range dstSize {
			for x := range dstSize {
				// The ideal downsample of a linear ramp is its center value.
				want := uint8((x*step + step/2) * 4 % 256)
				got := sample(x, y).R
				sum += math.Abs(float64(got) - float64(want))
			}
		}
		return sum / float64(dstSize*dstSize)
	}
	cubic := mae(func(x, y int) color.RGBA { return dst.RGBAAt(x, y) })
	nearest := mae(func(x, y int) color.RGBA {
		return src.RGBAAt(x*step, y*step)
	})
	if cubic >= nearest {
		t.Errorf("cubic MAE = %.2f, nearest MAE = %.2f: resampling did not beat nearest", cubic, nearest)
	}
}

func TestResampleEmptyInputs(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 4, 4))
	got := Resample(src, image.Rectangle{}, 4, 4)
	if got.Bounds().Dx() != 4 {
		t.Errorf("empty src rect gave %v, want a transparent 4x4", got.Bounds())
	}
	for y := range 4 {
		for x := range 4 {
			if got.RGBAAt(x, y) != (color.RGBA{}) {
				t.Fatalf("empty src rect painted %v at %d,%d", got.RGBAAt(x, y), x, y)
			}
		}
	}
	if got := Resample(src, src.Bounds(), 0, 4); got.Bounds().Dx() != 0 {
		t.Errorf("zero width gave %v", got.Bounds())
	}
}

func TestDrawImageDeviceIsOneToOne(t *testing.T) {
	// A 2x1 raster lands on exactly 2 device pixels even on a 2x
	// canvas, where DrawImage's logical mapping would have scaled it
	// onto 4.
	img := image.NewRGBA(image.Rect(0, 0, 2, 1))
	img.SetRGBA(0, 0, color.RGBA{R: 0xff, A: 0xff})
	img.SetRGBA(1, 0, color.RGBA{R: 0x40, G: 0x40, B: 0x40, A: 0xff})

	data := make([]byte, Stride(4)*4)
	cv := NewScaled(data, Stride(4), 4, 4, 2, 1)
	cv.Clear(cv.Rect(), RGB(0, 0, 0))
	cv.DrawImageDevice(img, 1, 0)

	get := func(x, y int) Color { return ColorFromBytes(data[y*Stride(4)+x*4:]) }
	if get(0, 0) != RGB(0, 0, 0) || get(3, 0) != RGB(0, 0, 0) {
		t.Errorf("raster spread past its two device pixels: %v / %v", get(0, 0), get(3, 0))
	}
	if got := get(1, 0); got != RGB(0xff, 0, 0) {
		t.Errorf("pixel 1 = %v, want red", got)
	}
	if got := get(2, 0); got != RGB(0x40, 0x40, 0x40) {
		t.Errorf("pixel 2 = %v, want gray", got)
	}
}

// opaqueImage hides an image's concrete type, forcing DrawImageDevice
// onto its generic At path.
type opaqueImage struct{ image.Image }

// The *image.RGBA fast path must paint exactly what the generic path
// paints: translucent and opaque pixels, a sub-image whose bounds do
// not start at the origin, a clip, and a modulating opacity.
func TestDrawImageDeviceRGBAFastPathMatchesGeneric(t *testing.T) {
	full := image.NewRGBA(image.Rect(0, 0, 6, 5))
	for y := range 5 {
		for x := range 6 {
			a := uint8(0xff)
			if (x+y)%3 == 0 {
				a = 0x80
			}
			full.SetRGBA(x, y, color.RGBA{R: uint8(x * 40 * int(a) / 255), G: uint8(y * 50 * int(a) / 255), B: a / 2, A: a})
		}
	}
	sub := full.SubImage(image.Rect(1, 1, 5, 4)).(*image.RGBA)
	paint := func(img image.Image, alpha float64) []byte {
		data := make([]byte, Stride(8)*6)
		cv := New(data, Stride(8), 8, 6)
		cv.Clear(cv.Rect(), RGB(0x10, 0x20, 0x30))
		prevA := cv.PushAlpha(alpha)
		prev := cv.PushClip(Rect{X: 1, Y: 0, W: 6, H: 5})
		cv.DrawImageDevice(img, 2, 1)
		cv.PopClip(prev)
		cv.PopAlpha(prevA)
		return data
	}
	for _, alpha := range []float64{1, 0.5} {
		for _, src := range []*image.RGBA{full, sub} {
			fast, slow := paint(src, alpha), paint(opaqueImage{src}, alpha)
			if !bytes.Equal(fast, slow) {
				t.Errorf("alpha %v bounds %v: the fast path differs from the generic one", alpha, src.Bounds())
			}
		}
	}
}

func TestDrawImageDeviceClips(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for y := range 4 {
		for x := range 4 {
			img.SetRGBA(x, y, color.RGBA{R: 0xff, A: 0xff})
		}
	}
	data := make([]byte, Stride(4)*4)
	cv := New(data, Stride(4), 4, 4)
	cv.Clear(cv.Rect(), RGB(0, 0, 0))
	cv.ResetTouched()
	prev := cv.PushClip(Rect{X: 1, Y: 1, W: 2, H: 2})
	cv.DrawImageDevice(img, 0, 0)
	cv.PopClip(prev)

	// Only the clipped window blends; its pixels count as touched.
	if got := cv.ResetTouched(); got != 4 {
		t.Errorf("touched = %d, want the 4 clipped pixels", got)
	}
	get := func(x, y int) Color { return ColorFromBytes(data[y*Stride(4)+x*4:]) }
	if get(0, 0) != RGB(0, 0, 0) || get(3, 3) != RGB(0, 0, 0) {
		t.Error("drew outside the clip")
	}
	if get(1, 1) != RGB(0xff, 0, 0) {
		t.Error("did not draw inside the clip")
	}
}

// Still copies any image into the pixels the CPU draws it from (each
// At, premultiplied to 8 bits) and keeps a still image as it is.
func TestStillHoldsThePixelsDrawnFrom(t *testing.T) {
	r := image.Rect(3, 5, 19, 14)
	nrgba, ycc, gray := image.NewNRGBA(r), image.NewYCbCr(r, image.YCbCrSubsampleRatio420), image.NewGray(r)
	for i := range nrgba.Pix {
		nrgba.Pix[i] = uint8(37*i + 11)
	}
	for i := range ycc.Y {
		ycc.Y[i] = uint8(53*i + 7)
	}
	for i := range ycc.Cb {
		ycc.Cb[i], ycc.Cr[i] = uint8(71*i+3), uint8(29*i+200)
	}
	for i := range gray.Pix {
		gray.Pix[i] = uint8(19 * i)
	}
	pal := image.NewPaletted(r, color.Palette{color.NRGBA{R: 200, G: 10, B: 90, A: 128}, color.Gray{Y: 77}})
	for i := range pal.Pix {
		pal.Pix[i] = uint8(i % 2)
	}
	rgba := image.NewRGBA(r)
	for i := range rgba.Pix {
		rgba.Pix[i] = uint8(13 * i)
	}
	for _, img := range []image.Image{nrgba, ycc, gray, pal, rgba} {
		s := Still(img)
		if s.Rect != r {
			t.Fatalf("%T: bounds %v, want %v", img, s.Rect, r)
		}
		for y := r.Min.Y; y < r.Max.Y; y++ {
			for x := r.Min.X; x < r.Max.X; x++ {
				cr, cg, cb, ca := img.At(x, y).RGBA()
				want := color.RGBA{R: uint8(cr >> 8), G: uint8(cg >> 8), B: uint8(cb >> 8), A: uint8(ca >> 8)}
				if got := s.RGBAAt(x, y); got != want {
					t.Fatalf("%T at (%d, %d): %v, want %v", img, x, y, got, want)
				}
			}
		}
	}
	if s := Still(rgba); &s.Pix[0] == &rgba.Pix[0] {
		t.Error("Still shares an *image.RGBA's pixels")
	}
	if s := (StillImage{rgba}); Still(s) != s {
		t.Error("Still changed a still image")
	}
}
