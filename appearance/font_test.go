package appearance

import (
	"math"
	"testing"
)

func TestParseFontSizeReadsPangoSizes(t *testing.T) {
	for _, tc := range []struct {
		desc string
		want FontSize
	}{
		{"Adwaita Sans 11", FontSize{Value: 11}},
		{"Sans Bold Italic 10.5", FontSize{Value: 10.5}},
		{"Monospace 12px", FontSize{Value: 12, Pixels: true}},
		{"Cantarell, Sans 11 @wght=500", FontSize{Value: 11}},
		{"Noto Sans,11", FontSize{Value: 11}},
	} {
		got, ok := ParseFontSize(tc.desc)
		if !ok || got != tc.want {
			t.Errorf("ParseFontSize(%q) = %+v, %v; want %+v", tc.desc, got, ok, tc.want)
		}
	}
}

func TestParseFontSizeRejectsSizelessDescriptions(t *testing.T) {
	for _, desc := range []string{"", "Sans", "Sans Bold", "Sans 0", "Sans -3", "Sans px", "@wght=500"} {
		if got, ok := ParseFontSize(desc); ok {
			t.Errorf("ParseFontSize(%q) = %+v, want no size", desc, got)
		}
	}
}

func TestDefaultFontPxFollowsGTK(t *testing.T) {
	for _, tc := range []struct {
		name string
		s    FontSettings
		want float64
	}{
		{"GTK's Sans 10 at 96 DPI when unset", FontSettings{}, 10 * 96.0 / 72},
		{"a point size at 96 DPI", FontSettings{Font: "Adwaita Sans 11"}, 11 * 96.0 / 72},
		{"a point size at the DPI", FontSettings{Font: "Adwaita Sans 11", DPI: 120}, 11 * 120.0 / 72},
		{"an absolute size ignores the DPI", FontSettings{Font: "Sans 15px", DPI: 120}, 15},
		{"a sizeless font is GTK's default size", FontSettings{Font: "Inter"}, 10 * 96.0 / 72},
	} {
		if got := tc.s.DefaultFontPx(); math.Abs(got-tc.want) > 1e-9 {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestResolveFontSettingsPrefersThePortalPerKey(t *testing.T) {
	for _, tc := range []struct {
		name   string
		pFont  string
		pScale float64
		iFont  string
		iDPI   float64
		want   FontSettings
	}{
		{"nothing set", "", 0, "", 0, FontSettings{}},
		{"settings.ini alone", "", 0, "Cantarell 12", 110, FontSettings{Font: "Cantarell 12", DPI: 110}},
		{"the portal over settings.ini", "Adwaita Sans 11", 1.25, "Cantarell 12", 110, FontSettings{Font: "Adwaita Sans 11", DPI: 120}},
		{"per key: portal font, ini DPI", "Adwaita Sans 11", 0, "Cantarell 12", 110, FontSettings{Font: "Adwaita Sans 11", DPI: 110}},
		{"per key: ini font, portal scale", "", 2, "Cantarell 12", 110, FontSettings{Font: "Cantarell 12", DPI: 192}},
	} {
		if got := ResolveFontSettings(tc.pFont, tc.pScale, tc.iFont, tc.iDPI); got != tc.want {
			t.Errorf("%s: %+v, want %+v", tc.name, got, tc.want)
		}
	}
}
