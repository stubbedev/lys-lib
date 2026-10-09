package capture

import (
	"errors"
	"image/color"
	"strings"
	"testing"

	"github.com/stubbedev/gelm/third_party/neurlang-wayland/wl"
	"github.com/stubbedev/gelm/wlr"
)

func wlrDamage(x, y, w, h int32) wlr.ImageCopyCaptureFrameV1DamageEvent {
	return wlr.ImageCopyCaptureFrameV1DamageEvent{X: x, Y: y, Width: w, Height: h}
}

// pixel32 lays one 32-bit little-endian pixel into bytes.
func pixel32(v uint32) []byte { return []byte{byte(v), byte(v >> 8), byte(v >> 16), byte(v >> 24)} }

func TestFrameImageFormats(t *testing.T) {
	cases := []struct {
		name   string
		format Format
		px     []byte
		want   color.RGBA
	}{
		{"xrgb ignores the pad byte", FormatXRGB8888, pixel32(0x12_a0_b0_c0), color.RGBA{0xa0, 0xb0, 0xc0, 0xff}},
		{"argb keeps alpha", FormatARGB8888, pixel32(0x80_40_30_20), color.RGBA{0x40, 0x30, 0x20, 0x80}},
		{"xbgr swaps red and blue", FormatXBGR8888, pixel32(0x00_c0_b0_a0), color.RGBA{0xa0, 0xb0, 0xc0, 0xff}},
		{"abgr keeps alpha", FormatABGR8888, pixel32(0x80_20_30_40), color.RGBA{0x40, 0x30, 0x20, 0x80}},
		{"xrgb2101010 narrows to 8 bits", FormatXRGB2101010, pixel32(0x3ff<<20 | 0x200<<10 | 0x004), color.RGBA{0xff, 0x80, 0x01, 0xff}},
		{"argb2101010 widens alpha", FormatARGB2101010, pixel32(2<<30 | 0x3ff), color.RGBA{0, 0, 0xff, 0xaa}},
		{"xbgr2101010 swaps", FormatXBGR2101010, pixel32(0x3ff), color.RGBA{0xff, 0, 0, 0xff}},
		{"rgb888 sits as b g r", FormatRGB888, []byte{0x10, 0x20, 0x30}, color.RGBA{0x30, 0x20, 0x10, 0xff}},
		{"bgr888 sits as r g b", FormatBGR888, []byte{0x10, 0x20, 0x30}, color.RGBA{0x10, 0x20, 0x30, 0xff}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &Frame{Width: 1, Height: 1, Stride: len(tc.px), Format: tc.format, Data: tc.px}
			img, err := f.Image()
			if err != nil {
				t.Fatal(err)
			}
			if got := img.RGBAAt(0, 0); got != tc.want {
				t.Fatalf("pixel = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestFrameImageRowsAndStride(t *testing.T) {
	// Two rows of one pixel each, padded to an 8-byte stride.
	top, bottom := pixel32(0x00ff0000), pixel32(0x000000ff)
	data := append(append(append([]byte{}, top...), 0, 0, 0, 0), bottom...)
	f := &Frame{Width: 1, Height: 2, Stride: 8, Format: FormatXRGB8888, Data: data}
	img, err := f.Image()
	if err != nil {
		t.Fatal(err)
	}
	if img.RGBAAt(0, 0).R != 0xff || img.RGBAAt(0, 1).B != 0xff {
		t.Fatalf("padded stride misread: %v %v", img.RGBAAt(0, 0), img.RGBAAt(0, 1))
	}
	f.YInvert = true
	img, err = f.Image()
	if err != nil {
		t.Fatal(err)
	}
	if img.RGBAAt(0, 0).B != 0xff || img.RGBAAt(0, 1).R != 0xff {
		t.Fatalf("y-inverted rows not flipped back: %v %v", img.RGBAAt(0, 0), img.RGBAAt(0, 1))
	}
}

func TestFrameImageRejects(t *testing.T) {
	t.Run("unknown format", func(t *testing.T) {
		f := &Frame{Width: 1, Height: 1, Stride: 2, Format: Format(wl.ShmFormatRgb565), Data: []byte{0, 0}}
		if _, err := f.Image(); !errors.Is(err, ErrFormat) {
			t.Fatalf("err = %v, want ErrFormat", err)
		}
	})
	t.Run("short data", func(t *testing.T) {
		f := &Frame{Width: 2, Height: 2, Stride: 8, Format: FormatXRGB8888, Data: make([]byte, 12)}
		if _, err := f.Image(); err == nil {
			t.Fatal("a frame shorter than its geometry converted")
		}
	})
	t.Run("stride below the row", func(t *testing.T) {
		f := &Frame{Width: 2, Height: 1, Stride: 4, Format: FormatXRGB8888, Data: make([]byte, 8)}
		if _, err := f.Image(); err == nil {
			t.Fatal("a stride shorter than a row converted")
		}
	})
}

func TestFormatFourccRoundTrip(t *testing.T) {
	if FormatFromFourcc(0x34325258) != FormatXRGB8888 || FormatXRGB8888.Fourcc() != 0x34325258 {
		t.Fatal("XR24 does not map to the legacy xrgb8888 code and back")
	}
	if FormatFromFourcc(0x34325241) != FormatARGB8888 || FormatARGB8888.Fourcc() != 0x34325241 {
		t.Fatal("AR24 does not map to the legacy argb8888 code and back")
	}
	if FormatFromFourcc(uint32(FormatXBGR8888)) != FormatXBGR8888 {
		t.Fatal("a fourcc-coded format changed in the mapping")
	}
	if got := FormatXRGB8888.String(); got != "XR24" {
		t.Fatalf("String = %q, want XR24", got)
	}
	if got := Format(0xdeadbeef).String(); !strings.HasPrefix(got, "format(") {
		t.Fatalf("String of garbage = %q", got)
	}
	if Format(0xdeadbeef).Supported() || !FormatBGR888.Supported() {
		t.Fatal("Supported misclassifies")
	}
}

func TestTransformSwapsAxes(t *testing.T) {
	for _, tr := range []Transform{Transform90, Transform270, TransformFlipped90, TransformFlipped270} {
		if !tr.SwapsAxes() {
			t.Errorf("%d does not swap", tr)
		}
	}
	for _, tr := range []Transform{TransformNormal, Transform180, TransformFlipped, TransformFlipped180} {
		if tr.SwapsAxes() {
			t.Errorf("%d swaps", tr)
		}
	}
}

func TestSessionLayout(t *testing.T) {
	ss := &sessionState{width: 10, height: 5, done: true}
	if ss.negotiated() {
		t.Fatal("negotiated without a format")
	}
	ss.formats = []Format{Format(wl.ShmFormatRgb565), FormatBGR888, FormatXRGB8888}
	if !ss.negotiated() {
		t.Fatal("not negotiated with size, format, and done")
	}
	f, stride, err := ss.layout()
	if err != nil || f != FormatBGR888 || stride != 30 {
		t.Fatalf("layout = %v %d %v, want the first convertible format packed", f, stride, err)
	}
	ss.formats = []Format{Format(wl.ShmFormatRgb565)}
	if _, _, err := ss.layout(); !errors.Is(err, ErrFormat) {
		t.Fatalf("layout of unknown formats = %v, want ErrFormat", err)
	}
	if (&sessionState{formats: []Format{FormatXRGB8888}, width: 1, height: 1}).negotiated() {
		t.Fatal("negotiated before done")
	}
}

func TestFrameStateDamage(t *testing.T) {
	fs := &frameState{}
	fs.HandleImageCopyCaptureFrameV1Damage(wlrDamage(1, 2, 3, 4))
	fs.HandleImageCopyCaptureFrameV1Damage(wlrDamage(-5, -5, 0, 10))
	if len(fs.damage) != 1 || fs.damage[0] != (Rect{1, 2, 3, 4}) {
		t.Fatalf("damage = %v, want the one non-empty rect", fs.damage)
	}
	if fs.finished() {
		t.Fatal("finished before ready or failed")
	}
}

func TestDispatchTolerant(t *testing.T) {
	t.Run("skips events for destroyed objects", func(t *testing.T) {
		calls := 0
		err := dispatchTolerant(func() error {
			calls++
			if calls < 3 {
				return wl.ErrContextRunProxyNil
			}
			return nil
		})
		if err != nil || calls != 3 {
			t.Fatalf("err = %v after %d calls", err, calls)
		}
	})
	t.Run("gives up on an endless stream of them", func(t *testing.T) {
		calls := 0
		err := dispatchTolerant(func() error {
			calls++
			return wl.ErrContextRunProxyNil
		})
		if !errors.Is(err, wl.ErrContextRunProxyNil) || calls != maxProxyNilRetries+1 {
			t.Fatalf("err = %v after %d calls", err, calls)
		}
	})
	t.Run("passes other errors straight through", func(t *testing.T) {
		calls := 0
		err := dispatchTolerant(func() error {
			calls++
			return wl.ErrContextRunConnectionClosed
		})
		if !errors.Is(err, wl.ErrContextRunConnectionClosed) || calls != 1 {
			t.Fatalf("err = %v after %d calls", err, calls)
		}
	})
}

func TestClientCheckPrefersTheCompositorVerdict(t *testing.T) {
	c := &Client{}
	if err := c.check(nil); err != nil {
		t.Fatalf("clean dispatch = %v", err)
	}
	c.protoErr = errors.New("verdict")
	if err := c.check(wl.ErrContextRunConnectionClosed); err.Error() != "verdict" {
		t.Fatalf("check = %v, want the recorded verdict", err)
	}
	if err := c.check(nil); err == nil {
		t.Fatal("a dead client dispatched cleanly")
	}
	c.closed = true
	if !errors.Is(c.usable(), ErrClosed) {
		t.Fatal("a closed client is usable")
	}
}
