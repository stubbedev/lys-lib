package gpu

import "time"

const (
	clockSampleEvery = time.Second
	clockSampleNear  = 4 * clockSampleEvery
)

// clockSample is one reading of the device and monotonic clocks taken
// together.
type clockSample struct {
	device uint64
	at     time.Duration
}

// monotonic maps a device timestamp onto CLOCK_MONOTONIC through the
// sample, with the counter validBits wide and ticking every period
// nanoseconds. near is false when the timestamp is too far from the
// sample to trust it: the counter restarted, as it may across a suspend.
func (c clockSample) monotonic(timestamp uint64, validBits uint32, period float64) (at time.Duration, near bool) {
	mask := ^uint64(0)
	if validBits < 64 {
		mask = 1<<validBits - 1
	}
	ahead := (timestamp - c.device) & mask
	since := time.Duration(float64(ahead) * period)
	if ahead > mask/2 {
		since = -time.Duration(float64((c.device-timestamp)&mask) * period)
	}
	return c.at + since, since >= -clockSampleNear && since <= clockSampleNear
}
