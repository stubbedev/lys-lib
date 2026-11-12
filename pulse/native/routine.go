package native

import (
	"sync/atomic"

	"github.com/stubbedev/lys-lib/internal/goroutine"
)

// routine is a goroutine a connection runs, which Close waits for
// unless Close is running on it.
type routine struct {
	id   atomic.Uint64
	done chan struct{}
}

func startRoutine(f func()) *routine {
	r := &routine{done: make(chan struct{})}
	go func() {
		defer close(r.done)
		r.id.Store(goroutine.ID())
		f()
	}()
	return r
}

// wait returns once the routine has ended, or at once when self, the
// calling goroutine, is the routine.
func (r *routine) wait(self uint64) {
	if r.id.Load() == self {
		return
	}
	<-r.done
}
