package gomath

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math"
	"testing"
)

// inputs are the same bits on every architecture: integers scaled by
// an exact power of two, reaching far past each function's reduction.
func inputs(lo, hi int64) []float64 {
	const n = 20000
	out := make([]float64, 0, n)
	state := uint64(0x9e3779b97f4a7c15)
	span := uint64(hi - lo)
	for range n {
		state = state*6364136223846793005 + 1442695040888963407
		out = append(out, float64(lo+int64(state>>11%span))/(1<<20))
	}
	return out
}

func unary(f func(float64) float64, lo, hi int64) func() []float64 {
	return func() []float64 {
		in := inputs(lo, hi)
		for i, x := range in {
			in[i] = f(x)
		}
		return in
	}
}

func binaryOf(f func(a, b float64) float64, lo, hi int64) func() []float64 {
	return func() []float64 {
		in := inputs(lo, hi)
		for i := 0; i+1 < len(in); i++ {
			in[i] = f(in[i], in[i+1])
		}
		return in
	}
}

const scale = 1 << 20

// One hash per function, with no per-architecture variant: this file's
// whole point is that one table holds on all of them.
type result struct {
	name string
	got  func() []float64
	want string
}

func results() []result {
	return []result{
		{"Sin", unary(Sin, -1000*scale, 1000*scale), "d88a6c430e19545c"},
		{"Cos", unary(Cos, -1000*scale, 1000*scale), "0d89b2212f289b34"},
		{"Tan", unary(Tan, -1000*scale, 1000*scale), "aefd93fd9e323335"},
		{"Sincos", unary(func(x float64) float64 { s, c := Sincos(x); return s + c }, -1000*scale, 1000*scale), "789b41bc07accce7"},
		{"Atan", unary(Atan, -1000*scale, 1000*scale), "6a6a51ed09b1acab"},
		{"Atan2", binaryOf(Atan2, -1000*scale, 1000*scale), "90328429196e9ca4"},
		{"Asin", unary(Asin, -scale, scale), "7bc3e58939662c0e"},
		{"Acos", unary(Acos, -scale, scale), "a9490ecb1bdf983b"},
		{"Exp", unary(Exp, -700*scale, 700*scale), "354892341ff4a51c"},
		{"Exp2", unary(Exp2, -1000*scale, 1000*scale), "0da5551d1aac141e"},
		{"Log", unary(Log, 1, 1000000*scale), "71c5e764cb0dd8c7"},
		{"Log2", unary(Log2, 1, 1000000*scale), "2d697ada0c42f775"},
		{"Log10", unary(Log10, 1, 1000000*scale), "1b3d5c1eebbeaee8"},
		{"Pow", binaryOf(Pow, 1, 64*scale), "40fed3dad2504d26"},
		{"Cbrt", unary(Cbrt, -1000000*scale, 1000000*scale), "1a3c96b1f5fd78e0"},
		{"Hypot", binaryOf(Hypot, -1000000*scale, 1000000*scale), "96c90e6ba0914703"},
		{"Erf", unary(Erf, -8*scale, 8*scale), "e17c5ae3e5b7299f"},
		{"Erfc", unary(Erfc, -8*scale, 8*scale), "2fd33bd76387219e"},
	}
}

func TestResultsAreTheSameBitsEverywhere(t *testing.T) {
	for _, r := range results() {
		h := sha256.New()
		var b [8]byte
		for _, v := range r.got() {
			binary.LittleEndian.PutUint64(b[:], math.Float64bits(v))
			h.Write(b[:])
		}
		if got := fmt.Sprintf("%x", h.Sum(nil)[:8]); got != r.want {
			t.Errorf("%s: results hash to %s, want %s", r.name, got, r.want)
		}
	}
}

// Rounding each product stays within a few units in the last place of
// the standard library, whichever implementation it runs here.
func TestResultsStayCloseToMath(t *testing.T) {
	const ulps = 8
	for _, tc := range []struct {
		name     string
		got, std func(float64) float64
		lo, hi   int64
	}{
		{"Sin", Sin, math.Sin, -1000 * scale, 1000 * scale},
		{"Cos", Cos, math.Cos, -1000 * scale, 1000 * scale},
		{"Atan", Atan, math.Atan, -1000 * scale, 1000 * scale},
		{"Exp", Exp, math.Exp, -700 * scale, 700 * scale},
		{"Log", Log, math.Log, 1, 1000000 * scale},
		{"Cbrt", Cbrt, math.Cbrt, -1000000 * scale, 1000000 * scale},
	} {
		for _, x := range inputs(tc.lo, tc.hi) {
			g, s := tc.got(x), tc.std(x)
			if d := math.Abs(g - s); d > ulps*(math.Nextafter(math.Abs(s), math.Inf(1))-math.Abs(s)) && d > 1e-12 {
				t.Errorf("%s(%v) = %v, math has %v", tc.name, x, g, s)
				break
			}
		}
	}
}

func TestSpecialCases(t *testing.T) {
	if !math.IsNaN(Sin(math.NaN())) || !math.IsNaN(Log(-1)) || Exp(math.Inf(-1)) != 0 || !math.IsInf(Exp(1000), 1) {
		t.Error("special cases differ from math's")
	}
	if Log2(8) != 3 || Pow(2, 10) != 1024 || Hypot(3, 4) != 5 || Cbrt(27) != 3 {
		t.Error("exact results lost")
	}
}
