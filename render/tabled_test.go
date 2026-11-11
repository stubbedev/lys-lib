package render

import (
	"math"
	"testing"

	"github.com/stubbedev/lys-lib/third_party/gomath"
)

func TestTheTabledConstantsAreWhatTheirFormulasCompute(t *testing.T) {
	for name, c := range map[string]struct{ got, want float64 }{
		"invTurn32":      {float64(invTurn32), float64(Reciprocal(turn32))},
		"Inv255":         {float64(Inv255), float64(Reciprocal(float32(255)))},
		"inv360":         {float64(inv360), float64(Reciprocal(float32(360)))},
		"syntheticSlant": {syntheticSlant, gomath.Tan(14 * math.Pi / 180)},
	} {
		if c.got != c.want {
			t.Errorf("%s is %x, want %x", name, c.got, c.want)
		}
	}
}

func TestSoftLightTableIsTheCurve(t *testing.T) {
	for b, got := range SoftLightTable {
		x := float64(b) / 255
		d := math.Sqrt(x)
		if x <= 0.25 {
			d = (float64((float64(16*x)-12)*x) + 4) * x
		}
		if want := int32(math.Round(d * blendK)); got != want {
			t.Errorf("SoftLightTable[%d] = %d, want %d", b, got, want)
		}
	}
}
