package pcm

import (
	"math"
	"testing"

	"github.com/stubbedev/lys-lib/testbuild"
)

func TestImportingCostsNothing(t *testing.T) { testbuild.RequireNoInit(t) }

func TestInt16RoundsHalvesToEvenAndSaturates(t *testing.T) {
	want := func(v float32) int16 {
		return int16(math.RoundToEven(float64(max(-32768, min(32767, v)))))
	}
	values := []float32{
		0, float32(math.Copysign(0, -1)), 0.5, -0.5, 1.5, -1.5, 2.5, -2.5, 0.49999997, -0.49999997, 0.50000006,
		32766.5, 32766.498, 32767, 32767.002, 32768, 40000, -32767.5, -32767.502, -32768, -32768.004, -40000,
		float32(math.Inf(1)), float32(math.Inf(-1)), math.MaxFloat32, -math.MaxFloat32, math.SmallestNonzeroFloat32,
	}
	for i := -65536; i <= 65536; i++ {
		values = append(values, float32(i)/2, float32(i)/2+0.25, math.Nextafter32(float32(i)/2, 0))
	}
	seed := uint32(21)
	for range 1 << 16 {
		seed = seed*1664525 + 1013904223
		values = append(values, 40000*(float32(seed>>8)/(1<<23)-1))
	}
	for _, v := range values {
		if got := Int16(v); got != want(v) {
			t.Fatalf("Int16(%v) = %d, want %d", v, got, want(v))
		}
	}
	if got := Int16(float32(math.NaN())); got != 0 {
		t.Errorf("Int16(NaN) = %d, want 0", got)
	}
}

func TestInt16OfEveryFloat32WithinRangeIsTheNearestEven(t *testing.T) {
	for bits := uint32(0); bits < math.Float32bits(32768); bits += 1021 {
		for _, v := range [2]float32{math.Float32frombits(bits), -math.Float32frombits(bits)} {
			if got, want := Int16(v), int16(math.RoundToEven(float64(max(-32768, min(32767, v))))); got != want {
				t.Fatalf("Int16(%v) = %d, want %d", v, got, want)
			}
		}
	}
}
