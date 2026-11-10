package native

import (
	"context"
	"net"
	"testing"
)

func TestPlaybackWriteDoesNotAllocate(t *testing.T) {
	client, server := net.Pipe()
	t.Cleanup(func() { _ = server.Close() })
	go func() {
		buf := make([]byte, 64<<10)
		for {
			if _, err := server.Read(buf); err != nil {
				return
			}
		}
	}()
	c := newConn(client, Options{})
	t.Cleanup(func() { _ = c.Close() })
	s := &PlaybackStream{ended: make(chan struct{}), c: c, channel: 3, frameSize: 4, wake: make(chan struct{}, 1)}
	registerStream(c, &c.playbacks, s.channel, s)
	audio := make([]byte, 1920)
	allocs := testing.AllocsPerRun(200, func() {
		s.grant(uint32(len(audio)))
		if err := s.Write(context.Background(), audio); err != nil {
			t.Fatal(err)
		}
	})
	if allocs != 0 {
		t.Errorf("Write allocates %.1f times per call", allocs)
	}
}
