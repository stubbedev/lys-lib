package fourcc

import "testing"

func TestFormatsPrintAsTheirCode(t *testing.T) {
	for _, tc := range []struct {
		f    Format
		want string
	}{{XRGB8888, "XR24"}, {ARGB8888, "AR24"}, {NV12, "NV12"}, {YUV420, "YU12"}, {Format(0x01020304), "0x01020304"}} {
		if got := tc.f.String(); got != tc.want {
			t.Errorf("%#x prints as %q, want %q", uint32(tc.f), got, tc.want)
		}
	}
}

func TestModifiersPrintByName(t *testing.T) {
	for _, tc := range []struct {
		m    Modifier
		want string
	}{{Linear, "linear"}, {Invalid, "invalid"}, {Modifier(0x0100000000000001), "0x0100000000000001"}} {
		if got := tc.m.String(); got != tc.want {
			t.Errorf("%#x prints as %q, want %q", uint64(tc.m), got, tc.want)
		}
	}
}

func TestAlphaAndItsOpaqueTwin(t *testing.T) {
	for f, want := range map[Format]Format{
		ARGB8888: XRGB8888, ABGR8888: XBGR8888, ARGB2101010: XRGB2101010, ABGR2101010: XBGR2101010,
		XRGB8888: XRGB8888, XRGB2101010: XRGB2101010, NV12: NV12,
	} {
		if got := f.Opaque(); got != want {
			t.Errorf("%v.Opaque() = %v, want %v", f, got, want)
		}
		if got := want.WithAlpha(); f.HasAlpha() && got != f {
			t.Errorf("%v.WithAlpha() = %v, want %v", want, got, f)
		}
		if got := f.WithAlpha(); got.Opaque() != want || (f.HasAlpha() && got != f) {
			t.Errorf("%v.WithAlpha() = %v", f, got)
		}
		if f.HasAlpha() != (f != want) {
			t.Errorf("%v.HasAlpha() = %v", f, f.HasAlpha())
		}
	}
}
