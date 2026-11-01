package gpu

import (
	"testing"
	"time"
)

func TestClockSampleMapsTimestamps(t *testing.T) {
	base := clockSample{device: 1000, at: 5 * time.Second}
	for _, tc := range []struct {
		name      string
		sample    clockSample
		timestamp uint64
		validBits uint32
		period    float64
		want      time.Duration
		near      bool
	}{
		{"after the sample", base, 1250, 64, 4, 5*time.Second + time.Microsecond, true},
		{"before the sample", base, 750, 64, 4, 5*time.Second - time.Microsecond, true},
		{"the same tick", base, 1000, 36, 52.08, 5 * time.Second, true},
		{"a narrow counter wrapping forward", clockSample{device: 1<<36 - 10, at: time.Second}, 15, 36, 2, time.Second + 50*time.Nanosecond, true},
		{"a narrow counter wrapping back", clockSample{device: 5, at: time.Second}, 1<<36 - 5, 36, 2, time.Second - 20*time.Nanosecond, true},
		{"a restarted counter", clockSample{device: 1 << 40, at: time.Hour}, 100, 64, 1, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, near := tc.sample.monotonic(tc.timestamp, tc.validBits, tc.period)
			if near != tc.near {
				t.Fatalf("near = %v, want %v (mapped to %v)", near, tc.near, got)
			}
			if tc.near && got != tc.want {
				t.Errorf("mapped to %v, want %v", got, tc.want)
			}
		})
	}
}
