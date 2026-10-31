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

type yuvPlanes struct {
	format   fourcc.Format
	w, h     int
	disjoint bool
}

func (p yuvPlanes) buffer(t *testing.T, y, u, v uint16) dmabuf.Buffer {
	t.Helper()
	size := 1
	if p.format == fourcc.P010 {
		size = 2
	}
	put := func(b []byte, val uint16) {
		if size == 1 {
			b[0] = byte(val)
			return
		}
		binary.LittleEndian.PutUint16(b, val<<6)
	}
	lumaBytes := linearStride * p.h
	first := newUdmabuf(t, 1<<20)
	second, offset := first, (lumaBytes+4095)&^4095
	if p.disjoint {
		second, offset = newUdmabuf(t, 1<<20), 0
	}
	for row := range p.h {
		for x := range p.w {
			put(first.Mem[row*linearStride+x*size:], y)
		}
	}
	for row := range p.h / 2 {
		for x := range p.w / 2 {
			at := offset + row*linearStride + x*2*size
			put(second.Mem[at:], u)
			put(second.Mem[at+size:], v)
		}
	}
	return dmabuf.Buffer{
		Width: p.w, Height: p.h, Format: p.format, Modifier: fourcc.Linear,
		Planes: []dmabuf.Plane{{FD: first.FD, Stride: linearStride}, {FD: second.FD, Offset: uint32(offset), Stride: linearStride}},
	}
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
		t.Run(f.String(), func(t *testing.T) {
			d := yuvDevice(t, f, true)
			y, u, v := uint16(81), uint16(90), uint16(240)
			bits := 8
			if f == fourcc.P010 {
				y, u, v, bits = y*4, u*4, v*4, 10
			}
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
