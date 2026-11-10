package native

import (
	"errors"
	"fmt"
	"sync"
)

// ErrStreamKilled ends a stream the server killed (its device went
// away, or it was killed from outside).
var ErrStreamKilled = errors.New("pulse: stream killed by the server")

const maxChannels = 32

type lifetime struct {
	ended chan struct{}
	once  sync.Once
	err   error
}

// Ended closes when the stream is gone: deleted, killed by the server,
// or lost with its connection. Err then says which.
func (l *lifetime) Ended() <-chan struct{} { return l.ended }

// Err reports why the stream ended; nil while it runs.
func (l *lifetime) Err() error {
	select {
	case <-l.ended:
		return l.err
	default:
		return nil
	}
}

func (l *lifetime) end(err error) {
	l.once.Do(func() {
		l.err = err
		close(l.ended)
	})
}

func validateFormat(spec SampleSpec, m ChannelMap) error {
	switch {
	case spec.Format.BytesPerSample() == 0:
		return fmt.Errorf("unsupported sample format %d", spec.Format)
	case spec.Channels == 0 || spec.Channels > maxChannels:
		return fmt.Errorf("%d channels", spec.Channels)
	case spec.Rate == 0:
		return errors.New("zero sample rate")
	case len(m) != int(spec.Channels):
		return fmt.Errorf("channel map has %d positions for %d channels", len(m), spec.Channels)
	}
	return nil
}

func unityVolume(channels uint8) CVolume {
	v := make(CVolume, channels)
	for i := range v {
		v[i] = VolumeNorm
	}
	return v
}

func registerStream[S any](c *Conn, streams *map[uint32]S, channel uint32, s S) {
	c.mu.Lock()
	(*streams)[channel] = s
	c.mu.Unlock()
}
