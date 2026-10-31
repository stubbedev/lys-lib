package capture

import (
	"image"
	"testing"
)

func BenchmarkFrameImageInto(b *testing.B) {
	const w, h = 1920, 1080
	for _, f := range []Format{FormatXRGB8888, FormatABGR8888, FormatXRGB2101010, FormatBGR888} {
		bpp := f.BytesPerPixel()
		fr := Frame{Width: w, Height: h, Stride: w * bpp, Format: f, Data: make([]byte, w*h*bpp)}
		for i := range fr.Data {
			fr.Data[i] = byte(i * 7)
		}
		dst := image.NewRGBA(image.Rect(0, 0, w, h))
		b.Run(f.String(), func(b *testing.B) {
			b.SetBytes(int64(len(fr.Data)))
			for b.Loop() {
				if err := fr.ImageInto(dst); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
