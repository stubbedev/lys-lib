package capture

import (
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/stubbedev/lys-lib/dmabuf"
	"github.com/stubbedev/lys-lib/fourcc"
	"github.com/stubbedev/lys-lib/intconv"
	"github.com/stubbedev/lys-lib/third_party/wayland/wl"
	"github.com/stubbedev/lys-lib/wlr"
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

// ImageInto converts into an image the caller owns, at its own stride
// and origin (a sub-image of a larger raster), as Image would, and
// refuses one of another size.
func TestFrameImageInto(t *testing.T) {
	top, bottom := pixel32(0x00ff0000), pixel32(0x000000ff)
	f := &Frame{Width: 1, Height: 2, Stride: 4, Format: FormatXRGB8888, Data: append(append([]byte{}, top...), bottom...), YInvert: true}
	big := image.NewRGBA(image.Rect(0, 0, 3, 4))
	dst, ok := big.SubImage(image.Rect(1, 1, 2, 3)).(*image.RGBA)
	if !ok {
		t.Fatal("an RGBA sub-image is not an RGBA")
	}
	if err := f.ImageInto(dst); err != nil {
		t.Fatal(err)
	}
	if big.RGBAAt(1, 1).B != 0xff || big.RGBAAt(1, 2).R != 0xff {
		t.Errorf("converted rows %v %v, want the y-inverted frame", big.RGBAAt(1, 1), big.RGBAAt(1, 2))
	}
	if big.RGBAAt(0, 1).A != 0 || big.RGBAAt(2, 2).A != 0 {
		t.Error("the conversion wrote outside the destination")
	}
	if err := f.ImageInto(image.NewRGBA(image.Rect(0, 0, 2, 2))); err == nil {
		t.Error("a frame converted into an image of another size")
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
	if FormatFromFourcc(fourcc.XRGB8888) != FormatXRGB8888 || FormatXRGB8888.Fourcc() != fourcc.XRGB8888 {
		t.Fatal("XR24 does not map to the legacy xrgb8888 code and back")
	}
	if FormatFromFourcc(fourcc.ARGB8888) != FormatARGB8888 || FormatARGB8888.Fourcc() != fourcc.ARGB8888 {
		t.Fatal("AR24 does not map to the legacy argb8888 code and back")
	}
	if FormatFromFourcc(fourcc.XBGR8888) != FormatXBGR8888 {
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

// words lays 64-bit values out as the 32-bit words a wl_array decodes
// to.
func words(vs ...uint64) []int32 {
	out := make([]int32, 0, 2*len(vs))
	for _, v := range vs {
		var b [8]byte
		binary.NativeEndian.PutUint64(b[:], v)
		out = append(out, intconv.Int32Bits(binary.NativeEndian.Uint32(b[:4])), intconv.Int32Bits(binary.NativeEndian.Uint32(b[4:])))
	}
	return out
}

// negotiate feeds one batch of constraint events into ss, ended by
// done.
func negotiate(ss *sessionState, w, h uint32, shm []Format, device []int32, offers map[fourcc.Format][]uint64) {
	ss.HandleImageCopyCaptureSessionV1BufferSize(wlr.ImageCopyCaptureSessionV1BufferSizeEvent{Width: w, Height: h})
	for _, f := range shm {
		ss.HandleImageCopyCaptureSessionV1ShmFormat(wlr.ImageCopyCaptureSessionV1ShmFormatEvent{Format: uint32(f)})
	}
	if device != nil {
		ss.HandleImageCopyCaptureSessionV1DmabufDevice(wlr.ImageCopyCaptureSessionV1DmabufDeviceEvent{Device: device})
	}
	for _, f := range []fourcc.Format{fourcc.XRGB8888, fourcc.ARGB8888} {
		if mods, ok := offers[f]; ok {
			ss.HandleImageCopyCaptureSessionV1DmabufFormat(wlr.ImageCopyCaptureSessionV1DmabufFormatEvent{Format: uint32(f), Modifiers: words(mods...)})
		}
	}
	ss.HandleImageCopyCaptureSessionV1Done(wlr.ImageCopyCaptureSessionV1DoneEvent{})
}

func TestSessionLayout(t *testing.T) {
	k := constraints{width: 10, height: 5, shm: []Format{Format(wl.ShmFormatRgb565), FormatBGR888, FormatXRGB8888}}
	f, stride, err := k.layout()
	if err != nil || f != FormatBGR888 || stride != 30 {
		t.Fatalf("layout = %v %d %v, want the first convertible format packed", f, stride, err)
	}
	k.shm = []Format{Format(wl.ShmFormatRgb565)}
	if _, _, err := k.layout(); !errors.Is(err, ErrFormat) {
		t.Fatalf("layout of unknown formats = %v, want ErrFormat", err)
	}
	k.shm = nil
	if _, _, err := k.layout(); !errors.Is(err, ErrFormat) {
		t.Fatalf("layout of a dmabuf-only session = %v, want ErrFormat", err)
	}
}

// Each batch of constraints replaces the last at its done: a resize
// neither keeps the old formats nor shows half a batch.
func TestSessionConstraintsBatch(t *testing.T) {
	ss := &sessionState{}
	negotiate(ss, 320, 200, []Format{FormatXRGB8888}, words(0x1234), map[fourcc.Format][]uint64{fourcc.XRGB8888: {uint64(fourcc.Linear)}})
	if ss.generation != 1 || ss.current.width != 320 || len(ss.current.shm) != 1 {
		t.Fatalf("first batch: generation %d, constraints %+v", ss.generation, ss.current)
	}
	ss.HandleImageCopyCaptureSessionV1BufferSize(wlr.ImageCopyCaptureSessionV1BufferSizeEvent{Width: 640, Height: 400})
	if ss.current.width != 320 {
		t.Fatalf("a batch before its done changed the constraints to %+v", ss.current)
	}
	negotiate(ss, 640, 400, []Format{FormatARGB8888}, nil, nil)
	if ss.generation != 2 || ss.current.width != 640 || !slices.Equal(ss.current.shm, []Format{FormatARGB8888}) || ss.current.hasDevice || len(ss.current.dmabuf) != 0 {
		t.Fatalf("second batch: generation %d, constraints %+v", ss.generation, ss.current)
	}
	if ss.err != nil {
		t.Fatal(ss.err)
	}
}

func TestSessionDmabufFormats(t *testing.T) {
	const tiled = fourcc.Modifier(0x0100000000000001)
	t.Run("explicit modifiers in the compositor's order", func(t *testing.T) {
		ss := &sessionState{}
		negotiate(ss, 320, 200, nil, words(0xe280), map[fourcc.Format][]uint64{
			fourcc.XRGB8888: {uint64(tiled), uint64(fourcc.Invalid), uint64(fourcc.Linear), uint64(tiled)},
			fourcc.ARGB8888: {uint64(fourcc.Invalid)},
		})
		got, err := ss.current.dmabufFormats()
		if err != nil {
			t.Fatal(err)
		}
		want := []DmabufFormat{{Fourcc: fourcc.XRGB8888, Width: 320, Height: 200, Modifiers: []fourcc.Modifier{tiled, fourcc.Linear}, Device: 0xe280}}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("formats = %+v, want %+v", got, want)
		}
	})
	for _, tc := range []struct {
		name   string
		device []int32
		offers map[fourcc.Format][]uint64
	}{
		{"no dmabuf format", words(1), nil},
		{"no device", nil, map[fourcc.Format][]uint64{fourcc.XRGB8888: {uint64(fourcc.Linear)}}},
		{"only implicit modifiers", words(1), map[fourcc.Format][]uint64{fourcc.XRGB8888: {uint64(fourcc.Invalid)}}},
	} {
		t.Run(tc.name+" is unsupported", func(t *testing.T) {
			ss := &sessionState{}
			negotiate(ss, 320, 200, []Format{FormatXRGB8888}, tc.device, tc.offers)
			if _, err := ss.current.dmabufFormats(); !errors.Is(err, ErrUnsupported) {
				t.Fatalf("dmabufFormats = %v, want ErrUnsupported", err)
			}
		})
	}
	t.Run("a malformed array is the session's error", func(t *testing.T) {
		ss := &sessionState{}
		negotiate(ss, 320, 200, nil, []int32{1, 2, 3}, map[fourcc.Format][]uint64{fourcc.XRGB8888: {uint64(fourcc.Linear)}})
		ss.HandleImageCopyCaptureSessionV1DmabufFormat(wlr.ImageCopyCaptureSessionV1DmabufFormatEvent{Format: uint32(fourcc.XRGB8888), Modifiers: []int32{1}})
		if ss.err == nil || !strings.Contains(ss.err.Error(), "dmabuf_device") {
			t.Fatalf("err = %v, want the malformed dmabuf_device named", ss.err)
		}
		cs := &copySession{state: ss}
		if err := cs.usable(); err == nil {
			t.Fatal("a session with a malformed constraint is usable")
		}
	})
}

func TestConstraintsFit(t *testing.T) {
	k := constraints{width: 320, height: 200, dmabuf: []dmabufOffer{{format: fourcc.XRGB8888, modifiers: []fourcc.Modifier{fourcc.Linear}}}}
	ok := dmabuf.Buffer{Width: 320, Height: 200, Format: fourcc.XRGB8888, Modifier: fourcc.Linear}
	if !k.fits(ok) {
		t.Fatalf("%+v does not fit %+v", ok, k)
	}
	for name, b := range map[string]dmabuf.Buffer{
		"another size":     {Width: 640, Height: 200, Format: fourcc.XRGB8888, Modifier: fourcc.Linear},
		"another format":   {Width: 320, Height: 200, Format: fourcc.ARGB8888, Modifier: fourcc.Linear},
		"another modifier": {Width: 320, Height: 200, Format: fourcc.XRGB8888, Modifier: fourcc.Modifier(0x0100000000000001)},
	} {
		if k.fits(b) {
			t.Errorf("a buffer of %s fits: %+v", name, b)
		}
	}
}

func TestFrameVerdict(t *testing.T) {
	for _, tc := range []struct {
		name    string
		fs      frameState
		stopped bool
		want    error
	}{
		{"ready", frameState{ready: true}, false, nil},
		{"stopped session", frameState{}, true, ErrSessionStopped},
		{"stopped reason", frameState{failed: true, reason: failureStopped}, false, ErrSessionStopped},
		{"buffer constraints", frameState{failed: true, reason: failureBufferConstraints}, false, ErrFailed},
		{"unknown", frameState{failed: true}, false, ErrFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cs := &copySession{state: &sessionState{stopped: tc.stopped}}
			err := cs.verdict(&tc.fs)
			if tc.want == nil && err != nil || tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("verdict = %v, want %v", err, tc.want)
			}
		})
	}
	if !errors.Is(ErrSessionStopped, ErrFailed) {
		t.Fatal("ErrSessionStopped is not an ErrFailed")
	}
	fs := &frameState{}
	fs.HandleImageCopyCaptureFrameV1Failed(wlr.ImageCopyCaptureFrameV1FailedEvent{Reason: wlr.ImageCopyCaptureFrameV1FailureReasonBufferConstraints})
	if !fs.finished() || fs.reason != failureBufferConstraints {
		t.Fatalf("failed event left %+v", fs)
	}
}

func TestFrameStateDamage(t *testing.T) {
	fs := &frameState{}
	fs.HandleImageCopyCaptureFrameV1Damage(wlrDamage(1, 2, 3, 4))
	fs.HandleImageCopyCaptureFrameV1Damage(wlrDamage(-5, -5, 0, 10))
	if len(fs.info.Damage) != 1 || fs.info.Damage[0] != (Rect{1, 2, 3, 4}) {
		t.Fatalf("damage = %v, want the one non-empty rect", fs.info.Damage)
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

func TestFrameImageOddWidthRow(t *testing.T) {
	for _, f := range []Format{FormatXRGB8888, FormatARGB8888, FormatXBGR8888, FormatABGR8888} {
		var data []byte
		for _, px := range []uint32{0x80_10_20_30, 0x40_50_60_70, 0xc0_90_a0_b0} {
			data = append(data, pixel32(px)...)
		}
		fr := Frame{Width: 3, Height: 1, Stride: len(data), Format: f, Data: data}
		img, err := fr.Image()
		if err != nil {
			t.Fatal(err)
		}
		for x := range 3 {
			one := Frame{Width: 1, Height: 1, Stride: 4, Format: f, Data: data[x*4 : x*4+4]}
			want, err := one.Image()
			if err != nil {
				t.Fatal(err)
			}
			if got := img.RGBAAt(x, 0); got != want.RGBAAt(0, 0) {
				t.Errorf("%s pixel %d = %v, want %v", f, x, got, want.RGBAAt(0, 0))
			}
		}
	}
}
