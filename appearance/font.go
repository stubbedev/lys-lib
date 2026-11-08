package appearance

import (
	"strconv"
	"strings"
)

// GTK's fallbacks when the desktop sets nothing: the font
// gtksettings.c defaults gtk-font-name to, and the 96 DPI that
// gtk-xft-dpi = -1 means.
const (
	gtkDefaultFont = "Sans 10"
	gtkDefaultDPI  = 96
)

// FontSize is the size a Pango font description names: points, or
// absolute logical pixels when it ends in "px".
type FontSize struct {
	Value  float64
	Pixels bool
}

// ParseFontSize reads the size of a Pango font description ("Adwaita
// Sans 11", "Sans Bold 10.5", "Monospace 12px", "Cantarell 11
// @wght=500"); false when the description names no positive size.
func ParseFontSize(desc string) (FontSize, bool) {
	words := strings.FieldsFunc(desc, func(r rune) bool { return r == ' ' || r == ',' })
	for len(words) > 0 && strings.HasPrefix(words[len(words)-1], "@") {
		words = words[:len(words)-1]
	}
	if len(words) == 0 {
		return FontSize{}, false
	}
	last := words[len(words)-1]
	size := FontSize{}
	if digits, ok := strings.CutSuffix(last, "px"); ok {
		last, size.Pixels = digits, true
	}
	v, err := strconv.ParseFloat(last, 64)
	if err != nil || v <= 0 {
		return FontSize{}, false
	}
	size.Value = v
	return size, true
}

// FontSettings is where a GTK app takes its default font size from:
// the desktop font description (gtk-font-name, "" when unset) and the
// text DPI (gtk-xft-dpi in dots per inch, 0 when unset).
type FontSettings struct {
	Font string
	DPI  float64
}

// DefaultFontPx is the default font size, in logical pixels, a GTK app
// with these settings uses - what its 1rem is
// (gtk_css_font_size_get_default_px): an absolute size as given, a
// point size at the DPI. An unset or sizeless font is GTK's "Sans 10",
// an unset DPI 96.
func (s FontSettings) DefaultFontPx() float64 {
	size, ok := ParseFontSize(s.Font)
	if !ok {
		size, _ = ParseFontSize(gtkDefaultFont)
	}
	if size.Pixels {
		return size.Value
	}
	dpi := s.DPI
	if dpi <= 0 {
		dpi = gtkDefaultDPI
	}
	return size.Value * dpi / 72
}

// ResolveFontSettings applies GTK's per-key precedence on Wayland: the
// portal's org.gnome.desktop.interface value when it published one
// (font-name; text-scaling-factor, as gtk-xft-dpi = 96 x the factor),
// else settings.ini's (gtk-font-name, gtk-xft-dpi). Unset inputs are
// "" and 0.
func ResolveFontSettings(portalFont string, portalTextScale float64, iniFont string, iniDPI float64) FontSettings {
	s := FontSettings{Font: iniFont, DPI: iniDPI}
	if portalFont != "" {
		s.Font = portalFont
	}
	if portalTextScale > 0 {
		s.DPI = gtkDefaultDPI * portalTextScale
	}
	return s
}
