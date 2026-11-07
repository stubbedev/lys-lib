package gpu

import (
	"errors"
	"time"
)

// LossLimit is how many device losses within LossWindow a consumer
// recovers from by opening a new device; one more within the window is
// a GPU that cannot keep a device, and the consumer stops recovering.
const LossLimit = 3

// LossWindow is the span over which Losses counts losses against
// LossLimit.
const LossWindow = time.Minute

// Losses is the record of a consumer's recent device losses, deciding
// whether the next one is recovered from. The zero value has none.
type Losses struct {
	at   []time.Time
	errs []error
}

// Record adds a loss at now and reports whether it is within LossLimit
// for the LossWindow ending at now. Losses older than the window are
// forgotten.
func (l *Losses) Record(now time.Time, err error) bool {
	keep := 0
	for keep < len(l.at) && now.Sub(l.at[keep]) >= LossWindow {
		keep++
	}
	l.at = append(l.at[:0], l.at[keep:]...)
	l.errs = append(l.errs[:0], l.errs[keep:]...)
	l.at = append(l.at, now)
	l.errs = append(l.errs, err)
	return len(l.at) <= LossLimit
}

// Count is the number of losses within the window of the last Record.
func (l *Losses) Count() int { return len(l.at) }

// Err joins every loss within the window of the last Record, oldest
// first; nil when there is none.
func (l *Losses) Err() error { return errors.Join(l.errs...) }
