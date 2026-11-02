package gpu

import (
	"image"
	"image/color"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stubbedev/lys-lib/fourcc"
	"github.com/stubbedev/lys-lib/third_party/vk"
)

var (
	gamma22 = ColorFunc{Kind: ColorCurve, G: 2.2, A: 1}
	degamma = ColorFunc{Kind: ColorCurve, G: 1 / 2.2, A: 1}
	unit    = [9]float32{1, 0, 0, 0, 1, 0, 0, 0, 1}
	// sdrToPQ shows gamma 2.2 SDR white at 203 cd/m², as an HDR output does.
	sdrToPQ = ColorTransform{Decode: gamma22, Matrix: unit, Scale: 203.0 / 10000, Encode: ColorFunc{Kind: ColorPQ}}
	// pqToSDR is the way back, for SDR white at 203 cd/m² with the rest above
	// 0.8 compressed to fit 1.
	pqToSDR = ColorTransform{Decode: ColorFunc{Kind: ColorPQ}, Matrix: [9]float32{10000.0 / 203, 0, 0, 0, 10000.0 / 203, 0, 0, 0, 10000.0 / 203}, Knee: 0.8, Compress: 4.9, Scale: 1, Encode: degamma}
)

func TestColorApplyEncodesKnownPQSignals(t *testing.T) {
	for _, c := range []struct{ nits, signal float64 }{{0, 0}, {100, 0.5081}, {203, 0.5807}, {1000, 0.7518}, {10000, 1}} {
		tr := ColorTransform{Matrix: unit, Scale: float32(c.nits / 10000), Encode: ColorFunc{Kind: ColorPQ}}
		got := tr.Apply([4]float32{1, 1, 1, 1})
		if math.Abs(float64(got[0])-c.signal) > 0.0015 {
			t.Errorf("%v cd/m² encodes as %v, want %v", c.nits, got[0], c.signal)
		}
		back := ColorTransform{Decode: ColorFunc{Kind: ColorPQ}, Matrix: unit, Scale: 10000}
		if l := back.Apply([4]float32{got[0], got[0], got[0], 1})[0]; math.Abs(float64(l)-c.nits) > 0.02*c.nits+0.01 {
			t.Errorf("%v cd/m² decodes back as %v", c.nits, l)
		}
	}
}

func TestColorApplyDecodesHLGWithItsOOTF(t *testing.T) {
	tr := ColorTransform{Decode: ColorFunc{Kind: ColorHLG}, Luma: [3]float32{0.2627, 0.678, 0.0593}, HLGGamma: 1.2, Matrix: unit, Scale: 1}
	if got := tr.Apply([4]float32{1, 1, 1, 1}); math.Abs(float64(got[0])-1) > 0.001 {
		t.Fatalf("HLG peak decodes to %v, want 1", got[0])
	}
	if got := tr.Apply([4]float32{0.5, 0.5, 0.5, 1}); math.Abs(float64(got[0])-math.Pow(0.25/3, 1.2)) > 0.001 {
		t.Fatalf("HLG 0.5 decodes to %v", got[0])
	}
}

func TestColorApplyCompressesAboveTheKneeOnly(t *testing.T) {
	tr := ColorTransform{Matrix: unit, Knee: 0.8, Compress: 4.98, Scale: 1}
	if got := tr.Apply([4]float32{0.5, 0.25, 0.1, 1}); got != [4]float32{0.5, 0.25, 0.1, 1} {
		t.Fatalf("below the knee %v", got)
	}
	last := float32(0.8)
	for _, v := range []float32{1, 2, 5, 10, 40} {
		got := tr.Apply([4]float32{v, v / 2, 0, 1})
		if got[0] <= last || got[0] >= 1.0 {
			t.Fatalf("%v compresses to %v, after %v", v, got[0], last)
		}
		if math.Abs(float64(got[1]/got[0])-0.5) > 1e-5 {
			t.Fatalf("%v loses its hue: %v", v, got)
		}
		last = got[0]
	}
}

func TestColorApplyDropsWhatIsNotFinite(t *testing.T) {
	nan := float32(math.NaN())
	inf := float32(math.Inf(1))
	tr := ColorTransform{Matrix: unit, Scale: 1}
	if got := tr.Apply([4]float32{1, 1, 1, nan}); got != [4]float32{} {
		t.Fatalf("NaN alpha is %v", got)
	}
	if got := tr.Apply([4]float32{nan, 0.5, inf, 1}); got[0] != 0 || got[1] != 0 || got[2] != 0 || got[3] != 1 {
		t.Fatalf("NaN and infinite channels are %v", got)
	}
}

func TestColorApplyKeepsPremultiplication(t *testing.T) {
	half := sdrToPQ.Apply([4]float32{0.25, 0.25, 0.25, 0.5})
	full := sdrToPQ.Apply([4]float32{0.5, 0.5, 0.5, 1})
	if math.Abs(float64(half[0]-full[0]/2)) > 1e-5 || half[3] != 0.5 {
		t.Fatalf("half coverage %v, full %v", half, full)
	}
	if got := sdrToPQ.Apply([4]float32{}); got != [4]float32{} {
		t.Fatalf("transparent %v", got)
	}
}

func TestColorGLSLIsGLSLES(t *testing.T) {
	if _, err := exec.LookPath("glslangValidator"); err != nil {
		t.Skip("no glslangValidator: run inside the dev shell")
	}
	src := "#version 100\nprecision highp float;\nuniform vec4 cp[9];\n#define CP cp\n" + ColorGLSL + "void main() { gl_FragColor = colorApply(cp[0]); }\n"
	path := filepath.Join(t.TempDir(), "color.frag")
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("glslangValidator", path).CombinedOutput(); err != nil {
		t.Fatalf("ColorGLSL is not GLSL ES 1.00: %v\n%s", err, out)
	}
}

func toByte(v float32) uint8 {
	return uint8(math.Round(float64(min(max(v, 0), 1)) * 255))
}

func wantColor(c [4]float32) color.RGBA {
	return color.RGBA{toByte(c[0]), toByte(c[1]), toByte(c[2]), toByte(c[3])}
}

func premul(c color.RGBA) [4]float32 {
	return [4]float32{float32(c.R) / 255, float32(c.G) / 255, float32(c.B) / 255, float32(c.A) / 255}
}

func TestPassDrawsThroughAColorTransformLikeApply(t *testing.T) {
	d := openDevice(t)
	tg, err := d.NewOffscreen(8, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer tg.Close()
	in := color.RGBA{200, 100, 50, 255}
	tx := newTexture(t, d, fourcc.ARGB8888, 1, 1, [][4]byte{{in.B, in.G, in.R, in.A}})
	stops := newTexture(t, d, fourcc.ARGB8888, 2, 1, [][4]byte{{in.B, in.G, in.R, in.A}, {in.B, in.G, in.R, in.A}})
	see := color.RGBA{0, 0, 0, 255}
	runPass(t, d, tg, []image.Rectangle{{Max: tg.Size()}}, func(p *Pass) {
		p.Clear(see)
		p.SetColor(&sdrToPQ)
		p.DrawTexture(tx, FRect{W: 1, H: 1}, image.Rect(0, 0, 2, 2), TransformNormal, 1, Mask{})
		p.FillRect(image.Rect(2, 0, 4, 2), in, Mask{})
		p.FillGradient(image.Rect(4, 0, 6, 2), Gradient{Stops: stops, From: [2]float32{4, 0}, To: [2]float32{6, 0}, Alpha: 1}, Mask{})
		p.SetColor(nil)
		p.DrawTexture(tx, FRect{W: 1, H: 1}, image.Rect(6, 0, 8, 2), TransformNormal, 1, Mask{})
	})
	img := readTarget(t, tg)
	want := wantColor(sdrToPQ.Apply(premul(in)))
	for _, x := range []int{0, 2, 4} {
		if got := img.RGBAAt(x, 1); !within(got, want, 1) {
			t.Errorf("x=%d %v, want %v", x, got, want)
		}
	}
	if got := img.RGBAAt(6, 1); got != in {
		t.Errorf("after SetColor(nil) %v, want %v", got, in)
	}
}

func TestAFloatTargetKeepsLightAboveOne(t *testing.T) {
	d := openDevice(t)
	work, err := d.NewOffscreenFloat(4, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer work.Close()
	out, err := d.NewOffscreen(4, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	tx, err := work.Texture()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := work.ReadPixels(); err == nil {
		t.Fatal("a float target read back as 8-bit pixels")
	}
	src := newTexture(t, d, fourcc.ARGB8888, 1, 1, [][4]byte{{255, 255, 255, 255}})
	four := ColorTransform{Matrix: [9]float32{4, 0, 0, 0, 4, 0, 0, 0, 4}, Scale: 1}
	quarter := ColorTransform{Matrix: [9]float32{0.25, 0, 0, 0, 0.25, 0, 0, 0, 0.25}, Scale: 1}
	runPass(t, d, work, []image.Rectangle{{Max: work.Size()}}, func(p *Pass) {
		p.Clear(color.RGBA{0, 0, 0, 255})
		p.SetColor(&four)
		p.DrawTexture(src, FRect{W: 1, H: 1}, image.Rect(0, 0, 2, 2), TransformNormal, 1, Mask{})
		p.DrawTexture(src, FRect{W: 1, H: 1}, image.Rect(2, 0, 4, 2), TransformNormal, 0.5, Mask{})
	})
	runPass(t, d, out, []image.Rectangle{{Max: out.Size()}}, func(p *Pass) {
		p.Clear(color.RGBA{0, 0, 0, 255})
		p.SetColor(&quarter)
		p.DrawTexture(tx, FRect{W: 4, H: 2}, image.Rect(0, 0, 4, 2), TransformNormal, 1, Mask{})
	})
	img := readTarget(t, out)
	if got := img.RGBAAt(0, 0); !within(got, color.RGBA{255, 255, 255, 255}, 1) {
		t.Errorf("4.0 through a quarter is %v, want white", got)
	}
	if got := img.RGBAAt(2, 0); !within(got, color.RGBA{128, 128, 128, 255}, 1) {
		t.Errorf("half coverage of 4.0 over black, through a quarter, is %v, want 50%%", got)
	}
}

func TestColorSlotsSurviveManyTransforms(t *testing.T) {
	d := openDevice(t)
	tg, err := d.NewOffscreen(1, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer tg.Close()
	tx := newTexture(t, d, fourcc.ARGB8888, 1, 1, [][4]byte{{255, 255, 255, 255}})
	for i := range 3 * colorSlots {
		k := float32(i%200) / 200
		tr := ColorTransform{Matrix: [9]float32{k, 0, 0, 0, k, 0, 0, 0, k}, Scale: 1}
		tr.Knee = float32(i)
		runPass(t, d, tg, []image.Rectangle{{Max: tg.Size()}}, func(p *Pass) {
			p.Clear(color.RGBA{A: 255})
			p.SetColor(&tr)
			p.DrawTexture(tx, FRect{W: 1, H: 1}, image.Rect(0, 0, 1, 1), TransformNormal, 1, Mask{})
		})
		if i%97 != 0 {
			continue
		}
		want := toByte(k)
		if got := readTarget(t, tg).RGBAAt(0, 0); !within(got, color.RGBA{want, want, want, 255}, 1) {
			t.Fatalf("transform %d drew %v, want %d", i, got, want)
		}
	}
}

func TestPQToSDRTakesSDRWhiteHome(t *testing.T) {
	white := sdrToPQ.Apply([4]float32{1, 1, 1, 1})
	back := pqToSDR.Apply(white)
	if math.Abs(float64(back[0])-math.Pow(float64(0.8+0.2/(1+0.2*4.9)), 1/2.2)) > 0.01 {
		t.Fatalf("SDR white came back as %v", back)
	}
}

func TestColorPipelinesOfEveryYUVKindBuild(t *testing.T) {
	d := openDevice(t)
	if err := d.compInit(); err != nil {
		t.Fatal(err)
	}
	if _, err := d.colorInit(); err != nil {
		t.Fatal(err)
	}
	ct, err := d.compTargetOf(floatTargetFormat)
	if err != nil {
		t.Fatal(err)
	}
	if d.probeYCbCr(d.phys) {
		kind, err := d.ycbcrKindOf(pixelFormats[fourcc.NV12], YCbCr{Matrix: BT2020, Range: FullRange, Chroma: ChromaSiting{X: SitedAt0, Y: SitedAt0}}, vk.FormatFeatureFlags(vk.FormatFeatureCositedChromaSamplesBit))
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := d.colorTexturePipeline(&Texture{ycbcr: kind}, ct, floatTargetFormat); err != nil {
			t.Error(err)
		}
	}
	y444, err := d.yuv444KindOf(pixelFormats[fourcc.AYUV], YCbCr{Matrix: BT2020})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := d.colorTexturePipeline(&Texture{yuv444: y444}, ct, floatTargetFormat); err != nil {
		t.Error(err)
	}
	planes, err := d.planesKindOf(yuvImageLayouts[fourcc.P010], YCbCr{Matrix: BT2020})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := d.colorTexturePipeline(&Texture{planes: planes}, ct, floatTargetFormat); err != nil {
		t.Error(err)
	}
}
