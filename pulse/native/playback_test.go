package native_test

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stubbedev/lys-lib/pulse/native"
	"github.com/stubbedev/lys-lib/pulse/pulsetest"
)

func stereo48k() native.PlaybackParams {
	return native.PlaybackParams{
		SampleSpec: native.SampleSpec{Format: native.SampleS16LE, Channels: 2, Rate: 48000},
		ChannelMap: native.ChannelMap{native.ChannelFrontLeft, native.ChannelFrontRight},
	}
}

func playbackServer(t *testing.T) (*pulsetest.Server, *native.Conn) {
	t.Helper()
	srv := pulsetest.New(t)
	srv.PutSink(speakers())
	srv.SetDefaults("alsa_output.speakers", "")
	return srv, dial(t, srv, native.Options{})
}

func pattern(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i * 7)
	}
	return b
}

func waitWrite(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("Write did not return")
		return nil
	}
}

func TestPlaybackStreamWritesAsTheServerRequests(t *testing.T) {
	srv, c := playbackServer(t)
	params := stereo48k()
	params.Latency = 10 * time.Millisecond
	params.Props = native.PropList{"media.name": "clip"}
	stream, err := c.CreatePlaybackStream(context.Background(), params)
	if err != nil {
		t.Fatal(err)
	}
	p := srv.WaitPlaybacks(t, 1)[0]
	if p.TargetLength != 480*4 || !p.AdjustLatency || p.Sink != "" || p.SampleSpec != params.SampleSpec ||
		p.Props["media.name"] != "clip" || p.Corked() {
		t.Errorf("server saw %+v", p)
	}
	info := stream.Info()
	if info.SinkInput != p.SinkInput || info.SinkName != "alsa_output.speakers" || info.TargetLength != 480*4 {
		t.Errorf("granted %+v", info)
	}
	if st, ok := srv.SinkInput(info.SinkInput); !ok || st.Name != "clip" || st.Device != 1 {
		t.Errorf("sink input %+v %v", st, ok)
	}

	audio := pattern(480*4 + 800)
	if err := stream.Write(context.Background(), audio[:480*4]); err != nil {
		t.Fatalf("the first request's worth: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- stream.Write(context.Background(), audio[480*4:]) }()
	p.Request(400)
	p.WaitData(t, 480*4+400)
	select {
	case err := <-done:
		t.Fatalf("Write returned %v with 400 of 800 bytes requested", err)
	default:
	}
	p.Request(400)
	if err := waitWrite(t, done); err != nil {
		t.Fatal(err)
	}
	if got := p.WaitData(t, len(audio)); !bytes.Equal(got, audio) {
		t.Errorf("server received %d bytes, not the written audio", len(got))
	}
}

func TestPlaybackStreamCorksAndTakesSinkInputControls(t *testing.T) {
	srv, c := playbackServer(t)
	stream, err := c.CreatePlaybackStream(context.Background(), stereo48k())
	if err != nil {
		t.Fatal(err)
	}
	p := srv.WaitPlaybacks(t, 1)[0]
	if err := stream.Cork(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if st, _ := srv.SinkInput(p.SinkInput); !p.Corked() || !st.Corked {
		t.Error("Cork(true) left the stream running")
	}
	if err := stream.Cork(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if p.Corked() {
		t.Error("Cork(false) left the stream corked")
	}
	index := stream.Info().SinkInput
	if err := c.SetSinkInputVolume(context.Background(), index, native.CVolume{0x4000, 0x4000}); err != nil {
		t.Fatal(err)
	}
	if err := c.SetSinkInputMute(context.Background(), index, true); err != nil {
		t.Fatal(err)
	}
	if st, _ := srv.SinkInput(index); st.Volume[1] != 0x4000 || !st.Mute {
		t.Errorf("sink input after the controls: %+v", st)
	}
}

func TestPlaybackStreamEnds(t *testing.T) {
	t.Run("killed", func(t *testing.T) {
		srv, c := playbackServer(t)
		stream, err := c.CreatePlaybackStream(context.Background(), stereo48k())
		if err != nil {
			t.Fatal(err)
		}
		p := srv.WaitPlaybacks(t, 1)[0]
		if err := stream.Write(context.Background(), make([]byte, p.TargetLength)); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- stream.Write(context.Background(), make([]byte, 4)) }()
		p.Kill()
		if err := waitWrite(t, done); !errors.Is(err, native.ErrStreamKilled) {
			t.Errorf("blocked Write = %v, want ErrStreamKilled", err)
		}
		if !errors.Is(stream.Err(), native.ErrStreamKilled) {
			t.Errorf("Err = %v", stream.Err())
		}
		if err := stream.Close(context.Background()); err != nil {
			t.Errorf("Close after kill: %v", err)
		}
	})
	t.Run("closed", func(t *testing.T) {
		srv, c := playbackServer(t)
		stream, err := c.CreatePlaybackStream(context.Background(), stereo48k())
		if err != nil {
			t.Fatal(err)
		}
		p := srv.WaitPlaybacks(t, 1)[0]
		if err := stream.Write(context.Background(), make([]byte, p.TargetLength)); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- stream.Write(context.Background(), make([]byte, 4)) }()
		if err := stream.Close(context.Background()); err != nil {
			t.Fatal(err)
		}
		if err := waitWrite(t, done); !errors.Is(err, native.ErrClosed) {
			t.Errorf("blocked Write = %v, want ErrClosed", err)
		}
		srv.WaitPlaybacks(t, 0)
		if _, ok := srv.SinkInput(p.SinkInput); ok {
			t.Error("the sink input outlived its stream")
		}
	})
	t.Run("disconnected", func(t *testing.T) {
		srv, c := playbackServer(t)
		stream, err := c.CreatePlaybackStream(context.Background(), stereo48k())
		if err != nil {
			t.Fatal(err)
		}
		srv.Disconnect()
		select {
		case <-stream.Ended():
		case <-time.After(5 * time.Second):
			t.Fatal("stream outlived its connection")
		}
		if stream.Err() == nil || errors.Is(stream.Err(), native.ErrStreamKilled) {
			t.Errorf("Err = %v, want the connection error", stream.Err())
		}
	})
	t.Run("context", func(t *testing.T) {
		srv, c := playbackServer(t)
		stream, err := c.CreatePlaybackStream(context.Background(), stereo48k())
		if err != nil {
			t.Fatal(err)
		}
		p := srv.WaitPlaybacks(t, 1)[0]
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- stream.Write(ctx, make([]byte, p.TargetLength+4)) }()
		p.WaitData(t, int(p.TargetLength))
		cancel()
		if err := waitWrite(t, done); !errors.Is(err, context.Canceled) {
			t.Errorf("Write = %v, want Canceled", err)
		}
	})
}

func TestPlaybackStreamsGetTheirOwnSyncGroup(t *testing.T) {
	srv, c := playbackServer(t)
	for range 2 {
		if _, err := c.CreatePlaybackStream(context.Background(), stereo48k()); err != nil {
			t.Fatal(err)
		}
	}
	ps := srv.WaitPlaybacks(t, 2)
	if ps[0].SyncID == ps[1].SyncID {
		t.Errorf("both streams joined sync group %d", ps[0].SyncID)
	}
}

func TestPlaybackStreamErrors(t *testing.T) {
	srv, c := playbackServer(t)
	missing := stereo48k()
	missing.Sink = "nope"
	if _, err := c.CreatePlaybackStream(context.Background(), missing); !errors.Is(err, native.ErrNoEntity) {
		t.Errorf("missing sink = %v, want ErrNoEntity", err)
	}
	for name, mutate := range map[string]func(*native.PlaybackParams){
		"channel map":      func(p *native.PlaybackParams) { p.ChannelMap = p.ChannelMap[:1] },
		"zero rate":        func(p *native.PlaybackParams) { p.SampleSpec.Rate = 0 },
		"negative latency": func(p *native.PlaybackParams) { p.Latency = -time.Millisecond },
		"sub-frame":        func(p *native.PlaybackParams) { p.Latency = time.Microsecond },
		"huge latency":     func(p *native.PlaybackParams) { p.Latency = 1 << 62 },
	} {
		p := stereo48k()
		mutate(&p)
		if _, err := c.CreatePlaybackStream(context.Background(), p); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if calls := srv.Calls(); calls[len(calls)-1] != native.CmdCreatePlaybackStream {
		t.Errorf("an invalid stream reached the server: %v", calls)
	}
	stream, err := c.CreatePlaybackStream(context.Background(), stereo48k())
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Write(context.Background(), make([]byte, 3)); err == nil {
		t.Error("a partial frame was written")
	}
}
