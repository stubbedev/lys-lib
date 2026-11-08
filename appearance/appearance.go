// Package appearance holds the desktop's presentation preferences as
// xdg-desktop-portal publishes them: the color scheme, the accent
// color and the contrast level.
package appearance

import (
	"math"

	"github.com/stubbedev/lys-lib/render"
)

// ColorScheme is the desktop's dark/light preference.
type ColorScheme uint8

const (
	// Unknown means no portal, no preference, or a value outside the
	// spec. It is a reason to keep the current theme.
	Unknown ColorScheme = iota
	// Dark means the desktop prefers a dark style.
	Dark
	// Light means the desktop prefers a light style.
	Light
)

// String returns "dark", "light" or "unknown".
func (s ColorScheme) String() string {
	switch s {
	case Dark:
		return "dark"
	case Light:
		return "light"
	default:
		return "unknown"
	}
}

// Accent is the desktop's accent-color preference: RGB components in
// [0, 1], with Known set when the desktop published one.
type Accent struct {
	R, G, B float64
	Known   bool
}

// Color returns the accent as an opaque render.Color, and false when
// the desktop published none.
func (a Accent) Color() (render.Color, bool) {
	if !a.Known {
		return 0, false
	}
	return render.RGB(channel(a.R), channel(a.G), channel(a.B)), true
}

func channel(f float64) uint8 {
	return uint8(math.Round(math.Max(0, math.Min(1, f)) * 255))
}

// Contrast is the desktop's contrast preference.
type Contrast uint8

const (
	// ContrastUnknown is no preference, no portal, or an unreadable
	// value.
	ContrastUnknown Contrast = iota
	// ContrastHigh asks for the high-contrast presentation.
	ContrastHigh
)

// Motion is the desktop's reduced-motion preference.
type Motion uint8

const (
	// MotionUnknown is no preference, no portal, or an unreadable value.
	MotionUnknown Motion = iota
	// MotionReduced asks for things not to move: surfaces fade, and
	// nothing slides, bounces or scrolls smoothly.
	MotionReduced
)
