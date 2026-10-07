package render

import (
	"bytes"
	"image"
	"image/color"
	"image/gif"
	"os"
	"testing"
	"time"

	"github.com/kettek/apng"
)

var (
	red   = color.RGBA{255, 0, 0, 255}
	green = color.RGBA{0, 255, 0, 255}
	blue  = color.RGBA{0, 0, 255, 255}
)

// paletted is a w x h frame at (x, y) filled with c.
func paletted(x, y, w, h int, c color.Color) *image.Paletted {
	p := image.NewPaletted(image.Rect(x, y, x+w, y+h), color.Palette{color.Transparent, red, green, blue})
	for py := y; py < y+h; py++ {
		for px := x; px < x+w; px++ {
			p.Set(px, py, c)
		}
	}
	return p
}

// at is the RGBA of frame f at (x, y).
func at(f *image.RGBA, x, y int) color.RGBA { return f.RGBAAt(x, y) }

// A GIF's frames composite over the canvas through each disposal
// method, with delays floored and the loop count mapped.
func TestDecodeAnimatedGIF(t *testing.T) {
	var buf bytes.Buffer
	err := gif.EncodeAll(&buf, &gif.GIF{
		Image: []*image.Paletted{
			paletted(0, 0, 4, 4, red),   // full red
			paletted(0, 0, 2, 2, green), // green corner, then disposed to background
			paletted(2, 2, 2, 2, blue),  // blue corner, then restored to previous
			paletted(2, 0, 2, 2, green), // green top-right
		},
		Delay:     []int{10, 0, 1, 50},
		Disposal:  []byte{gif.DisposalNone, gif.DisposalBackground, gif.DisposalPrevious, gif.DisposalNone},
		LoopCount: 2,
		Config:    image.Config{Width: 4, Height: 4},
	})
	if err != nil {
		t.Fatal(err)
	}
	img, err := DecodeImage(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	a, ok := img.(*Animation)
	if !ok || len(a.Frames) != 4 {
		t.Fatalf("decoded %T with %v frames", img, a)
	}
	f := a.Frames
	if at(f[0], 3, 3) != red || at(f[1], 0, 0) != green || at(f[1], 3, 3) != red {
		t.Error("frames 0-1 composite wrong")
	}
	if at(f[2], 0, 0).A != 0 || at(f[2], 3, 3) != blue {
		t.Error("background disposal did not clear frame 1's rect")
	}
	// Frame 2 disposes to previous: its blue corner is undone back to
	// the red underneath, the cleared corner stays clear.
	if at(f[3], 3, 3) != red || at(f[3], 0, 0).A != 0 || at(f[3], 3, 0) != green {
		t.Errorf("previous disposal did not restore: %v %v %v", at(f[3], 3, 3), at(f[3], 0, 0), at(f[3], 3, 0))
	}
	want := []time.Duration{100 * time.Millisecond, gifZeroDelay, gifMinDelay, 500 * time.Millisecond}
	for i, d := range want {
		if a.Delays[i] != d {
			t.Errorf("delay %d = %v, want %v", i, a.Delays[i], d)
		}
	}
	if a.Loops != 3 || a.Bounds() != image.Rect(0, 0, 4, 4) || a.PixelBytes() != 4*4*4*4 {
		t.Errorf("loops %d bounds %v bytes %d", a.Loops, a.Bounds(), a.PixelBytes())
	}
}

// An APNG's frames composite with their offsets, blend and dispose
// operations.
func TestDecodeAPNG(t *testing.T) {
	full := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for i := range full.Pix {
		full.Pix[i] = []byte{255, 0, 0, 255}[i%4]
	}
	corner := image.NewRGBA(image.Rect(0, 0, 2, 2))
	for i := range corner.Pix {
		corner.Pix[i] = []byte{0, 0, 255, 255}[i%4]
	}
	var buf bytes.Buffer
	err := apng.Encode(&buf, apng.APNG{Frames: []apng.Frame{
		{Image: full, DelayNumerator: 1, DelayDenominator: 10},
		{Image: corner, XOffset: 2, YOffset: 2, DelayNumerator: 1, DelayDenominator: 4, BlendOp: apng.BLEND_OP_OVER},
	}, LoopCount: 0})
	if err != nil {
		t.Fatal(err)
	}
	img, err := DecodeImage(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	a, ok := img.(*Animation)
	if !ok || len(a.Frames) != 2 {
		t.Fatalf("decoded %T", img)
	}
	if at(a.Frames[1], 3, 3) != blue || at(a.Frames[1], 0, 0) != red || a.Delays[1] != 250*time.Millisecond || a.Loops != 0 {
		t.Errorf("apng frame 1: %v %v delay %v loops %d", at(a.Frames[1], 3, 3), at(a.Frames[1], 0, 0), a.Delays[1], a.Loops)
	}
}

// Still WebP decodes (lossy and lossless), and a still GIF or PNG is
// no animation.
func TestDecodeStillImages(t *testing.T) {
	for _, name := range []string{"blue-purple-pink.lossy.webp", "blue-purple-pink.lossless.webp"} {
		data, err := os.ReadFile("testdata/images/" + name)
		if err != nil {
			t.Fatal(err)
		}
		img, err := DecodeImage(data)
		if err != nil || img.Bounds().Dx() == 0 {
			t.Errorf("%s: %v", name, err)
		}
		if _, anim := img.(*Animation); anim {
			t.Errorf("%s decoded as an animation", name)
		}
	}
	var buf bytes.Buffer
	_ = gif.Encode(&buf, paletted(0, 0, 3, 3, red), nil)
	if img, err := DecodeImage(buf.Bytes()); err != nil {
		t.Error(err)
	} else if _, anim := img.(*Animation); anim {
		t.Error("a still GIF decoded as an animation")
	}
}
