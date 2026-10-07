package render

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/draw"
	"image/gif"
	_ "image/jpeg" // still-image decoders for DecodeImage
	_ "image/png"
	"time"

	"github.com/kettek/apng"
	_ "golang.org/x/image/webp" // still WebP (lossy and lossless)
)

// Animation is a decoded animated image (GIF, APNG): every frame fully
// composited over the canvas, how long each shows, and how many times
// the sequence plays (0: forever). It is an image.Image as its first
// frame, so every still-image path - sizing, a static paint, a cache -
// takes it unchanged; an animating painter steps through Frames.
type Animation struct {
	Frames []*image.RGBA
	Delays []time.Duration
	Loops  int
}

// ColorModel implements image.Image (the first frame's).
func (a *Animation) ColorModel() color.Model { return color.RGBAModel }

// Bounds implements image.Image: the canvas.
func (a *Animation) Bounds() image.Rectangle { return a.Frames[0].Bounds() }

// At implements image.Image: the first frame.
func (a *Animation) At(x, y int) color.Color { return a.Frames[0].At(x, y) }

// PixelBytes is the frames' combined pixel cost, what a byte-budget
// cache charges for the animation.
func (a *Animation) PixelBytes() int {
	n := 0
	for _, f := range a.Frames {
		n += len(f.Pix)
	}
	return n
}

// minFrameDelay floors a frame's display time: a zero or near-zero
// delay means "as fast as you like" in the formats, which browsers
// render at 100 ms for GIF; APNG keeps its delays above 10 ms.
const (
	gifMinDelay  = 20 * time.Millisecond
	gifZeroDelay = 100 * time.Millisecond
	apngMinDelay = 10 * time.Millisecond
)

// DecodeImage decodes image bytes: an animated GIF or APNG to an
// *Animation, anything else (PNG, JPEG, GIF, still WebP) to a still
// image. Animated WebP is not decoded: golang.org/x/image/webp, the
// pure-Go decoder, reads still images only.
func DecodeImage(data []byte) (image.Image, error) {
	switch {
	case bytes.HasPrefix(data, []byte("GIF8")):
		if a, err := decodeGIF(data); err == nil && len(a.Frames) > 1 {
			return a, nil
		}
	case bytes.HasPrefix(data, []byte("\x89PNG")) && bytes.Contains(data[:min(len(data), 4096)], []byte("acTL")):
		if a, err := decodeAPNG(data); err == nil && len(a.Frames) > 1 {
			return a, nil
		}
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	return img, err
}

// decodeGIF composites a GIF's frames with their disposal methods.
func decodeGIF(data []byte) (*Animation, error) {
	g, err := gif.DecodeAll(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	canvas := image.NewRGBA(image.Rect(0, 0, g.Config.Width, g.Config.Height))
	a := &Animation{Loops: gifLoops(g.LoopCount)}
	for i, fr := range g.Image {
		var restore *image.RGBA
		disposal := byte(0)
		if i < len(g.Disposal) {
			disposal = g.Disposal[i]
		}
		if disposal == gif.DisposalPrevious {
			restore = cloneRGBA(canvas)
		}
		draw.Draw(canvas, fr.Bounds(), fr, fr.Bounds().Min, draw.Over)
		a.Frames = append(a.Frames, cloneRGBA(canvas))
		d := gifZeroDelay
		if i < len(g.Delay) && g.Delay[i] > 0 {
			d = max(time.Duration(g.Delay[i])*10*time.Millisecond, gifMinDelay)
		}
		a.Delays = append(a.Delays, d)
		switch disposal {
		case gif.DisposalBackground:
			draw.Draw(canvas, fr.Bounds(), image.Transparent, image.Point{}, draw.Src)
		case gif.DisposalPrevious:
			canvas = restore
		}
	}
	return a, nil
}

// gifLoops maps the GIF loop count (0 forever, -1 once, n: n more
// times) onto Animation.Loops (0 forever, else total plays).
func gifLoops(n int) int {
	switch {
	case n == 0:
		return 0
	case n < 0:
		return 1
	}
	return n + 1
}

// decodeAPNG composites an APNG's frames with their blend and dispose
// operations; a default image that is not part of the animation is
// skipped.
func decodeAPNG(data []byte) (*Animation, error) {
	p, err := apng.DecodeAll(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	if len(p.Frames) == 0 {
		return nil, errors.New("render: apng without frames")
	}
	size := p.Frames[0].Image.Bounds()
	canvas := image.NewRGBA(image.Rect(0, 0, size.Dx(), size.Dy()))
	a := &Animation{Loops: int(p.LoopCount)}
	for _, fr := range p.Frames {
		if fr.IsDefault {
			continue
		}
		r := fr.Image.Bounds().Sub(fr.Image.Bounds().Min).Add(image.Pt(fr.XOffset, fr.YOffset))
		var restore *image.RGBA
		if fr.DisposeOp == apng.DISPOSE_OP_PREVIOUS {
			restore = cloneRGBA(canvas)
		}
		op := draw.Over
		if fr.BlendOp == apng.BLEND_OP_SOURCE {
			op = draw.Src
		}
		draw.Draw(canvas, r, fr.Image, fr.Image.Bounds().Min, op)
		a.Frames = append(a.Frames, cloneRGBA(canvas))
		a.Delays = append(a.Delays, max(time.Duration(fr.GetDelay()*float64(time.Second)), apngMinDelay))
		switch fr.DisposeOp {
		case apng.DISPOSE_OP_BACKGROUND:
			draw.Draw(canvas, r, image.Transparent, image.Point{}, draw.Src)
		case apng.DISPOSE_OP_PREVIOUS:
			canvas = restore
		}
	}
	if len(a.Frames) == 0 {
		return nil, errors.New("render: apng without animation frames")
	}
	return a, nil
}

// cloneRGBA copies an RGBA image.
func cloneRGBA(m *image.RGBA) *image.RGBA {
	c := image.NewRGBA(m.Rect)
	copy(c.Pix, m.Pix)
	return c
}
