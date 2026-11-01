package gpu

import (
	"encoding/binary"
	"image"
	"image/color"
	"math"
	"os"
	"slices"
	"testing"

	"github.com/stubbedev/lys-lib/dmabuf"
	"github.com/stubbedev/lys-lib/fourcc"
	"github.com/stubbedev/lys-lib/third_party/vk"
)

func expectRGB(y, u, v float64, bits int, m YCbCrMatrix, r YCbCrRange) color.RGBA {
	k := map[YCbCrMatrix][2]float64{BT601: {0.299, 0.114}, BT709: {0.2126, 0.0722}, BT2020: {0.2627, 0.0593}}[m]
	kr, kb := k[0], k[1]
	scale := math.Exp2(float64(bits - 8))
	mid := 128 * scale
	var luma, cb, cr float64
	if r == LimitedRange {
		luma, cb, cr = (y-16*scale)/(219*scale), (u-mid)/(224*scale), (v-mid)/(224*scale)
	} else {
		full := math.Exp2(float64(bits)) - 1
		luma, cb, cr = y/full, (u-mid)/full, (v-mid)/full
	}
	rr := luma + 2*(1-kr)*cr
	bb := luma + 2*(1-kb)*cb
	gg := (luma - kr*rr - kb*bb) / (1 - kr - kb)
	c := func(x float64) uint8 { return uint8(math.Round(255 * min(1, max(0, x)))) }
	return color.RGBA{c(rr), c(gg), c(bb), 255}
}

// yuvLayout is how a format lays its samples out: each plane's samples
// in memory order, how chroma is subsampled and the bits of a sample,
// which sit at the top of its 16 bits above 8.
type yuvLayout struct {
	planes     []string
	hsub, vsub int
	bits       int
}

var yuvLayouts = map[fourcc.Format]yuvLayout{
	fourcc.NV12:   {[]string{"Y", "UV"}, 2, 2, 8},
	fourcc.NV21:   {[]string{"Y", "VU"}, 2, 2, 8},
	fourcc.NV16:   {[]string{"Y", "UV"}, 2, 1, 8},
	fourcc.NV61:   {[]string{"Y", "VU"}, 2, 1, 8},
	fourcc.NV24:   {[]string{"Y", "UV"}, 1, 1, 8},
	fourcc.NV42:   {[]string{"Y", "VU"}, 1, 1, 8},
	fourcc.P010:   {[]string{"Y", "UV"}, 2, 2, 10},
	fourcc.P012:   {[]string{"Y", "UV"}, 2, 2, 12},
	fourcc.P016:   {[]string{"Y", "UV"}, 2, 2, 16},
	fourcc.P210:   {[]string{"Y", "UV"}, 2, 1, 10},
	fourcc.YUV420: {[]string{"Y", "U", "V"}, 2, 2, 8},
	fourcc.YVU420: {[]string{"Y", "V", "U"}, 2, 2, 8},
	fourcc.YUV422: {[]string{"Y", "U", "V"}, 2, 1, 8},
	fourcc.YVU422: {[]string{"Y", "V", "U"}, 2, 1, 8},
	fourcc.YUV444: {[]string{"Y", "U", "V"}, 1, 1, 8},
	fourcc.YVU444: {[]string{"Y", "V", "U"}, 1, 1, 8},
	fourcc.Q410:   {[]string{"Y", "U", "V"}, 1, 1, 10},
	fourcc.Q401:   {[]string{"Y", "V", "U"}, 1, 1, 10},
	fourcc.YUYV:   {[]string{"YUYV"}, 2, 1, 8},
	fourcc.YVYU:   {[]string{"YVYU"}, 2, 1, 8},
	fourcc.UYVY:   {[]string{"UYVY"}, 2, 1, 8},
	fourcc.VYUY:   {[]string{"VYUY"}, 2, 1, 8},
	fourcc.Y210:   {[]string{"YUYV"}, 2, 1, 10},
	fourcc.Y212:   {[]string{"YUYV"}, 2, 1, 12},
	fourcc.Y216:   {[]string{"YUYV"}, 2, 1, 16},
}

type yuvPlanes struct {
	format   fourcc.Format
	w, h     int
	disjoint bool
}

func (p yuvPlanes) buffer(t *testing.T, y, u, v uint16) dmabuf.Buffer {
	t.Helper()
	l := yuvLayouts[p.format]
	size := 1
	if l.bits > 8 {
		size = 2
	}
	put := func(b []byte, sample byte) {
		val := map[byte]uint16{'Y': y, 'U': u, 'V': v}[sample]
		if size == 1 {
			b[0] = byte(val)
			return
		}
		binary.LittleEndian.PutUint16(b, val<<(16-l.bits))
	}
	mem := newUdmabuf(t, 1<<20)
	var planes []dmabuf.Plane
	offset := 0
	for i, samples := range l.planes {
		if i > 0 && p.disjoint {
			mem, offset = newUdmabuf(t, 1<<20), 0
		}
		rows, texels := p.h, p.w
		if samples != "Y" {
			texels /= l.hsub
			if len(samples) < 4 {
				rows /= l.vsub
			}
		}
		for row := range rows {
			for x := range texels {
				for s := range len(samples) {
					put(mem.Mem[offset+row*linearStride+(x*len(samples)+s)*size:], samples[s])
				}
			}
		}
		planes = append(planes, dmabuf.Plane{FD: mem.FD, Offset: uint32(offset), Stride: linearStride})
		offset = (offset + rows*linearStride + 4095) &^ 4095
	}
	return dmabuf.Buffer{Width: p.w, Height: p.h, Format: p.format, Modifier: fourcc.Linear, Planes: planes}
}

func yuvDevice(t *testing.T, f fourcc.Format, disjoint bool) *Device {
	t.Helper()
	if disjoint && os.Getenv("LYS_TEST_VULKAN_LAYERS") != "" {
		t.Skip("the validation layer (1.4.363) crashes recording a disjoint DRM-modifier image's plane binds")
	}
	d := exportingDevice(t)
	if !d.ycbcr {
		t.Skipf("%s converts no YCbCr", d.Name())
	}
	linearOrSkip(t, d, d.TextureModifiers, f)
	if disjoint {
		i := slices.IndexFunc(d.formatModifiers(pixelFormats[f].vk), func(m vk.DrmFormatModifierPropertiesEXT) bool { return m.DrmFormatModifier == 0 })
		if d.formatModifiers(pixelFormats[f].vk)[i].DrmFormatModifierTilingFeatures&vk.FormatFeatureFlags(vk.FormatFeatureDisjointBit) == 0 {
			t.Skipf("%s has no disjoint linear %s", d.Name(), f)
		}
		var props vk.PhysicalDeviceProperties
		d.cmds.GetPhysicalDeviceProperties(d.phys, &props)
		if props.DeviceType == vk.PhysicalDeviceTypeCpu {
			t.Skipf("%s faults sampling a disjoint YCbCr dmabuf", d.Name())
		}
	}
	return d
}

func drawYUV(t *testing.T, d *Device, b dmabuf.Buffer, c *YCbCr) color.RGBA {
	t.Helper()
	var tx *Texture
	var err error
	if c == nil {
		tx, err = d.ImportTexture(b)
	} else {
		tx, err = d.ImportTextureYCbCr(b, *c)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Close()
	if err := tx.Update(image.Rect(0, 0, 1, 1), 4, make([]byte, 4)); err == nil {
		t.Error("a YUV texture took an upload")
	}
	tg, err := d.NewOffscreen(b.Width, b.Height)
	if err != nil {
		t.Fatal(err)
	}
	defer tg.Close()
	runPass(t, d, tg, []image.Rectangle{{Max: tg.Size()}}, func(p *Pass) {
		p.DrawTexture(tx, FRect{W: float64(b.Width), H: float64(b.Height)}, image.Rectangle{Max: tg.Size()}, TransformNormal, 1, Mask{})
	})
	return readTarget(t, tg).RGBAAt(b.Width/2, b.Height/2)
}

func TestNV12ConvertsUnderEveryMatrixAndRange(t *testing.T) {
	d := yuvDevice(t, fourcc.NV12, false)
	const y, u, v = 120, 90, 200
	for _, m := range []YCbCrMatrix{BT601, BT709, BT2020} {
		for _, r := range []YCbCrRange{LimitedRange, FullRange} {
			got := drawYUV(t, d, yuvPlanes{format: fourcc.NV12, w: 8, h: 8}.buffer(t, y, u, v), &YCbCr{Matrix: m, Range: r})
			if want := expectRGB(y, u, v, 8, m, r); !within(got, want, 2) {
				t.Errorf("matrix %d range %d: %v, want %v", m, r, got, want)
			}
		}
	}
	got := drawYUV(t, d, yuvPlanes{format: fourcc.NV12, w: 8, h: 8}.buffer(t, y, u, v), nil)
	if want := expectRGB(y, u, v, 8, BT601, LimitedRange); !within(got, want, 2) {
		t.Errorf("the default encoding drew %v, want BT.601 limited %v", got, want)
	}
}

func TestEveryLayoutConvertsWhereTheDriverOffersIt(t *testing.T) {
	for _, f := range ycbcrFormats {
		t.Run(f.String(), func(t *testing.T) {
			d := yuvDevice(t, f, false)
			if !slices.Contains(d.ImportFormats(), f) {
				t.Skipf("%s is core in a later Vulkan than %s", f, d.Name())
			}
			bits := yuvLayouts[f].bits
			scale := uint16(1) << (bits - 8)
			y, u, v := 120*scale, 90*scale, 200*scale
			got := drawYUV(t, d, yuvPlanes{format: f, w: 8, h: 8}.buffer(t, y, u, v), &YCbCr{Matrix: BT709})
			if want := expectRGB(float64(y), float64(u), float64(v), bits, BT709, LimitedRange); !within(got, want, 2) {
				t.Errorf("%v, want %v", got, want)
			}
		})
	}
}

func TestEveryConvertedFormatHasALayoutAndAYUVName(t *testing.T) {
	for _, f := range ycbcrFormats {
		if _, ok := yuvLayouts[f]; !ok {
			t.Errorf("%s has no test layout", f)
		}
		pf := pixelFormats[f]
		if !f.YUV() || !pf.ycbcr || f.Planes() != pf.planes {
			t.Errorf("%s: YUV %v, ycbcr %v, %d planes against %d", f, f.YUV(), pf.ycbcr, f.Planes(), pf.planes)
		}
	}
}

func TestP010Converts(t *testing.T) {
	d := yuvDevice(t, fourcc.P010, false)
	const y, u, v = 600, 300, 800
	for _, r := range []YCbCrRange{LimitedRange, FullRange} {
		got := drawYUV(t, d, yuvPlanes{format: fourcc.P010, w: 8, h: 8}.buffer(t, y, u, v), &YCbCr{Matrix: BT2020, Range: r})
		if want := expectRGB(y, u, v, 10, BT2020, r); !within(got, want, 2) {
			t.Errorf("range %d: %v, want %v", r, got, want)
		}
	}
}

func TestYUVDisjointPlanes(t *testing.T) {
	for _, f := range ycbcrFormats {
		if pixelFormats[f].planes < 2 {
			continue
		}
		t.Run(f.String(), func(t *testing.T) {
			d := yuvDevice(t, f, true)
			bits := yuvLayouts[f].bits
			scale := uint16(1) << (bits - 8)
			y, u, v := 81*scale, 90*scale, 240*scale
			got := drawYUV(t, d, yuvPlanes{format: f, w: 8, h: 8, disjoint: true}.buffer(t, y, u, v), &YCbCr{Matrix: BT709})
			if want := expectRGB(float64(y), float64(u), float64(v), bits, BT709, LimitedRange); !within(got, want, 2) {
				t.Errorf("disjoint %s: %v, want %v", f, got, want)
			}
		})
	}
}

func TestYCbCrRefusesUnknownHints(t *testing.T) {
	d := yuvDevice(t, fourcc.NV12, false)
	for _, c := range []YCbCr{{Matrix: 3}, {Range: 2}, {Chroma: ChromaSiting{X: 3}}} {
		if tx, err := d.ImportTextureYCbCr(yuvPlanes{format: fourcc.NV12, w: 8, h: 8}.buffer(t, 1, 2, 3), c); err == nil {
			tx.Close()
			t.Errorf("NV12 took %+v", c)
		}
	}
}
