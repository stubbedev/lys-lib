package render

import (
	"image"
	"image/color"
	"math"
	"runtime"
	"testing"

	xdraw "golang.org/x/image/draw"
)

// wallpaper is a 3840x2880 4:2:0 YCbCr gradient, the shape a decoded
// photo JPEG has.
func wallpaper() *image.YCbCr {
	img := image.NewYCbCr(image.Rect(0, 0, 3840, 2880), image.YCbCrSubsampleRatio420)
	for y := range 2880 {
		for x := range 3840 {
			img.Y[img.YOffset(x, y)] = uint8((x + y) / 27)
		}
	}
	for i := range img.Cb {
		img.Cb[i], img.Cr[i] = 100, 160
	}
	return img
}

// Scaling a 4K photo to a 1440p output stays within a few rows of
// working memory: x/image's kernel scaler held a srcW x dstH float64
// temporary, 177 MB a call, which every output and re-raster repeated.
func TestResampleOfAPhotoIsMemoryBounded(t *testing.T) {
	src := wallpaper()
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	dst := Resample(src, src.Bounds(), 2560, 1440)
	runtime.ReadMemStats(&after)
	out := uint64(len(dst.Pix))
	if got := after.TotalAlloc - before.TotalAlloc; got > out+4<<20 {
		t.Errorf("resampling allocated %d MB beyond its %d MB output, want under 4 MB", (got-out)>>20, out>>20)
	}
	if allocs := testing.AllocsPerRun(1, func() { Resample(src, src.Bounds(), 2560, 1440) }); allocs > 3000 {
		t.Errorf("%v allocations a call, want them per output pixel row at most", allocs)
	}
}

// The resampler agrees with x/image's CatmullRom, which it replaced, on
// a smooth photo-like field: no visible change in quality.
func TestResampleMatchesTheCatmullRomItReplaced(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 300, 200))
	for y := range 200 {
		for x := range 300 {
			src.SetRGBA(x, y, color.RGBA{R: uint8(x * 255 / 299), G: uint8(y * 255 / 199), B: uint8((x + y) % 256 / 2), A: 0xff})
		}
	}
	for _, size := range [][2]int{{120, 80}, {450, 300}, {97, 61}} {
		got := Resample(src, src.Bounds(), size[0], size[1])
		want := image.NewRGBA(image.Rect(0, 0, size[0], size[1]))
		xdraw.CatmullRom.Scale(want, want.Bounds(), src, src.Bounds(), xdraw.Src, nil)
		var sum float64
		for i := range got.Pix {
			sum += math.Abs(float64(got.Pix[i]) - float64(want.Pix[i]))
		}
		if mean := sum / float64(len(got.Pix)); mean > 1.5 {
			t.Errorf("%dx%d: mean channel difference %.2f from x/image's CatmullRom, want under 1.5", size[0], size[1], mean)
		}
	}
}

// A translucent NRGBA source comes out premultiplied, never brighter
// than its alpha.
func TestResamplePremultipliesNRGBA(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 8, 8))
	for i := 0; i < len(src.Pix); i += 4 {
		src.Pix[i], src.Pix[i+1], src.Pix[i+2], src.Pix[i+3] = 0xff, 0x80, 0, 0x80
	}
	got := Resample(src, src.Bounds(), 4, 4).RGBAAt(1, 1)
	if got.A != 0x80 || got.R != 0x80 || got.G != 0x40 {
		t.Errorf("resampled %v, want the premultiplied {128 64 0 128}", got)
	}
}

func BenchmarkResamplePhotoToOutput(b *testing.B) {
	src := wallpaper()
	b.ReportAllocs()
	for b.Loop() {
		Resample(src, src.Bounds(), 2560, 1440)
	}
}
