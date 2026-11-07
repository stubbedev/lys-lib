package capture

import "sync/atomic"

// captureLoop is the life of a goroutine capturing on a Client of its
// own: stopping asks it to end, done closes once it has, and err is why
// it ended on its own.
type captureLoop struct {
	client   *Client
	stopping atomic.Bool
	done     chan struct{}
	err      error
}

func newCaptureLoop(client *Client) captureLoop {
	return captureLoop{client: client, done: make(chan struct{})}
}

// Done is closed when the capture loop ended, on Close or on its own;
// Err then says why.
func (l *captureLoop) Done() <-chan struct{} { return l.done }

// Err is why the capture loop ended on its own; nil while it runs and
// after Close.
func (l *captureLoop) Err() error {
	select {
	case <-l.done:
		return l.err
	default:
		return nil
	}
}

// fail ends the loop with err, unless it is ending because it was asked
// to.
func (l *captureLoop) fail(err error) {
	if !l.stopping.Load() {
		l.err = err
	}
}

// stop asks the loop to end and waits until it has, reporting whether
// this call was the one that stopped it.
func (l *captureLoop) stop() bool {
	if l.stopping.Swap(true) {
		<-l.done
		return false
	}
	// Wake a capture parked on a static source: the sync's reply is an
	// event, so the loop's dispatch returns and sees the stop flag.
	_, _ = l.client.display.Sync()
	<-l.done
	return true
}

// wait is how the loop waits for a frame: holding the client, until the
// frame's verdict or the loop's stop.
func (l *captureLoop) wait() frameWait {
	return frameWait{client: l.client, quit: &l.stopping}
}
