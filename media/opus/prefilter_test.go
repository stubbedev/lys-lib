package opus

import (
	"slices"
	"testing"
)

func TestCombFilterMatchesItsDefinitionInPlaceAndApart(t *testing.T) {
	window := sharedTables().window[:]
	noise := testNoise(17)
	const at = 64
	for _, c := range []struct {
		name   string
		f0, f1 pitchFilter
		window []float32
		n      int
	}{
		{"no filter", pitchFilter{period: 40}, pitchFilter{period: 33, tapset: 1}, window, 400},
		{"one filter", pitchFilter{period: 40, gain: 0.5, tapset: 1}, pitchFilter{period: 40, gain: 0.5, tapset: 1}, window, 400},
		{"shortest period", pitchFilter{period: 3, gain: 0.3}, pitchFilter{period: combMinPeriod, gain: 0.7, tapset: 2}, window, 400},
		{"cross-fade", pitchFilter{period: 17, gain: 0.25, tapset: 2}, pitchFilter{period: 23, gain: 0.75}, window, 400},
		{"fade in", pitchFilter{period: 20}, pitchFilter{period: 28, gain: 0.6, tapset: 1}, window, 400},
		{"fade out", pitchFilter{period: 20, gain: 0.6, tapset: 1}, pitchFilter{period: 28}, window, 400},
		{"no window", pitchFilter{period: 19, gain: 0.4}, pitchFilter{period: 21, gain: 0.6, tapset: 2}, nil, 400},
		{"only the window", pitchFilter{period: 16, gain: 0.2}, pitchFilter{period: 30, gain: 0.4}, window, overlap},
		{"one sample past the window", pitchFilter{period: 16, gain: 0.2}, pitchFilter{period: 30, gain: 0.4}, window, overlap + 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			x := make([]float32, at+c.n)
			for i := range x {
				x[i] = noise.next()
			}
			t0, t1 := max(c.f0.period, combMinPeriod), max(c.f1.period, combMinPeriod)
			fade := len(c.window)
			if c.f0.gain == c.f1.gain && t0 == t1 && c.f0.tapset == c.f1.tapset {
				fade = 0
			}
			addTaps := func(v float32, x []float32, j int, f pitchFilter, period int, share float32) float32 {
				g := combGains[f.tapset]
				v += float32(share * (f.gain * g[0]) * x[j-period])
				v += float32(share * (f.gain * g[1]) * (x[j-period+1] + x[j-period-1]))
				v += float32(share * (f.gain * g[2]) * (x[j-period+2] + x[j-period-2]))
				return v
			}
			define := func(y, x []float32) {
				for i := range y {
					j := at + i
					switch {
					case c.f0.gain == 0 && c.f1.gain == 0:
						y[i] = x[j]
					case i < fade:
						f := float32(c.window[i] * c.window[i])
						y[i] = addTaps(addTaps(x[j], x, j, c.f0, t0, 1-f), x, j, c.f1, t1, f)
					default:
						y[i] = addTaps(x[j], x, j, c.f1, t1, 1)
					}
				}
			}
			apart, want := make([]float32, c.n), make([]float32, c.n)
			define(want, x)
			combFilter(apart, x, at, c.f0, c.f1, c.window)
			if i := firstDifference(apart, want); i >= 0 {
				t.Errorf("into another buffer: sample %d is %v, want %v", i, apart[i], want[i])
			}
			inPlace, wantInPlace := slices.Clone(x), slices.Clone(x)
			define(wantInPlace[at:], wantInPlace)
			combFilter(inPlace[at:], inPlace, at, c.f0, c.f1, c.window)
			if i := firstDifference(inPlace, wantInPlace); i >= 0 {
				t.Errorf("in place: sample %d is %v, want %v", i-at, inPlace[i], wantInPlace[i])
			}
		})
	}
}

func firstDifference(got, want []float32) int {
	for i := range want {
		if got[i] != want[i] {
			return i
		}
	}
	return -1
}
