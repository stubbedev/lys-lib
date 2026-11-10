package mp4audio

import "testing"

func TestSampleRatesRoundTrip(t *testing.T) {
	want := map[int]int{0: 96000, 3: 48000, 4: 44100, 11: 8000, 12: 7350}
	for index := range SampleRateIndices {
		rate, ok := SampleRate(index)
		if !ok {
			t.Fatalf("index %d names no rate", index)
		}
		if w, pinned := want[index]; pinned && rate != w {
			t.Errorf("index %d is %d Hz, want %d", index, rate, w)
		}
		if got, ok := SampleRateIndex(rate); !ok || got != index {
			t.Errorf("%d Hz is index %d (%v), want %d", rate, got, ok, index)
		}
	}
	for _, index := range []int{-1, SampleRateIndices, 14, ExplicitRate} {
		if rate, ok := SampleRate(index); ok {
			t.Errorf("index %d names %d Hz, want none", index, rate)
		}
	}
	for _, rate := range []int{0, 8, 44099, 192000} {
		if index, ok := SampleRateIndex(rate); ok {
			t.Errorf("%d Hz has index %d, want none", rate, index)
		}
	}
}
