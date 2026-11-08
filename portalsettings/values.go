package portalsettings

import (
	"github.com/stubbedev/lys-lib/appearance"
	dbus "github.com/stubbedev/lys-lib/third_party/godbus"
)

// contrastHigh is the portal's one defined contrast value.
const contrastHigh uint32 = 1

// motionReduced is the portal's one defined reduced-motion value.
const motionReduced uint32 = 1

// accentValue maps a portal value - a variant around a (ddd) RGB
// triple - to an Accent. Anything else (wrong arity, out-of-range
// components, wrong types) maps to the zero Accent rather than a
// guessed color.
func accentValue(v any) appearance.Accent {
	if variant, ok := v.(dbus.Variant); ok {
		v = variant.Value()
	}
	var rgb [3]float64
	switch triple := v.(type) {
	case []float64:
		if len(triple) != 3 {
			return appearance.Accent{}
		}
		rgb = [3]float64{triple[0], triple[1], triple[2]}
	case []any:
		if len(triple) != 3 {
			return appearance.Accent{}
		}
		for i, part := range triple {
			f, ok := part.(float64)
			if !ok {
				return appearance.Accent{}
			}
			rgb[i] = f
		}
	default:
		return appearance.Accent{}
	}
	for _, f := range rgb {
		if f < 0 || f > 1 {
			return appearance.Accent{}
		}
	}
	return appearance.Accent{R: rgb[0], G: rgb[1], B: rgb[2], Known: true}
}

// contrastValue maps a portal value - a variant around uint32 - to a
// Contrast: 1 is high contrast, everything else (0 "no preference",
// out of range, wrong type) is Unknown.
func contrastValue(v any) appearance.Contrast {
	if variant, ok := v.(dbus.Variant); ok {
		v = variant.Value()
	}
	if u, ok := v.(uint32); ok && u == contrastHigh {
		return appearance.ContrastHigh
	}
	return appearance.ContrastUnknown
}

// Motion: 1 is reduced, everything else (0 "no preference", out of
// range, wrong type) is Unknown.
func motionValue(v any) appearance.Motion {
	if variant, ok := v.(dbus.Variant); ok {
		v = variant.Value()
	}
	if u, ok := v.(uint32); ok && u == motionReduced {
		return appearance.MotionReduced
	}
	return appearance.MotionUnknown
}

// scaleValue maps a portal value - a variant around a double - to a
// text scaling factor; anything else, and a non-positive factor, maps
// to 0 (unset) rather than a guessed scale.
func scaleValue(v any) float64 {
	if variant, ok := v.(dbus.Variant); ok {
		v = variant.Value()
	}
	if f, ok := v.(float64); ok && f > 0 {
		return f
	}
	return 0
}
