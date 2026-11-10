package pulsetest_test

import (
	"slices"
	"testing"
	"time"

	"github.com/stubbedev/lys-lib/pulse/native"
	"github.com/stubbedev/lys-lib/pulse/pulsetest"
)

const eventWait = 5 * time.Second

func device(index uint32, name string) native.DeviceInfo {
	return native.DeviceInfo{
		Index: index, Name: name,
		SampleSpec: native.SampleSpec{Format: native.SampleS16LE, Channels: 2, Rate: 48000},
		ChannelMap: native.ChannelMap{native.ChannelFrontLeft, native.ChannelFrontRight},
		Volume:     native.CVolume{native.VolumeNorm, native.VolumeNorm},
		Card:       native.InvalidIndex, OwnerModule: native.InvalidIndex,
		Props: native.PropList{},
	}
}

func scripted(index uint32) native.StreamInfo {
	return native.StreamInfo{
		Index: index, Name: "scripted", OwnerModule: native.InvalidIndex, Client: 9, Device: 1,
		SampleSpec: native.SampleSpec{Format: native.SampleS16LE, Channels: 2, Rate: 48000},
		ChannelMap: native.ChannelMap{native.ChannelFrontLeft, native.ChannelFrontRight},
		Volume:     native.CVolume{native.VolumeNorm, native.VolumeNorm},
		Props:      native.PropList{}, HasVolume: true, VolumeWritable: true,
		Format: native.FormatInfo{Encoding: native.EncodingPCM, Props: native.PropList{}},
	}
}

func dial(t *testing.T, srv *pulsetest.Server, opts native.Options) *native.Conn {
	t.Helper()
	opts.Server = srv.Addr()
	c, err := native.Dial(t.Context(), opts)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func indexes(streams []native.StreamInfo) []uint32 {
	out := make([]uint32, len(streams))
	for i, st := range streams {
		out[i] = st.Index
	}
	return out
}

func TestADisconnectedClientsStreamsLeaveTheListings(t *testing.T) {
	srv := pulsetest.New(t)
	srv.PutSink(device(1, "speakers"))
	srv.PutSource(device(2, "microphone"))
	srv.SetDefaults("speakers", "microphone")
	srv.PutSinkInput(scripted(50))
	srv.PutSourceOutput(scripted(51))

	events := make(chan native.SubscribeEvent, 16)
	watcher := dial(t, srv, native.Options{OnSubscribe: func(ev native.SubscribeEvent) { events <- ev }})
	if err := watcher.Subscribe(t.Context(), native.MaskSinkInput|native.MaskSourceOutput); err != nil {
		t.Fatal(err)
	}

	spec := native.SampleSpec{Format: native.SampleS16LE, Channels: 2, Rate: 48000}
	channels := native.ChannelMap{native.ChannelFrontLeft, native.ChannelFrontRight}
	client := dial(t, srv, native.Options{})
	playback, err := client.CreatePlaybackStream(t.Context(), native.PlaybackParams{SampleSpec: spec, ChannelMap: channels})
	if err != nil {
		t.Fatal(err)
	}
	record, err := client.CreateRecordStream(t.Context(), native.RecordParams{SampleSpec: spec, ChannelMap: channels}, func([]byte) {})
	if err != nil {
		t.Fatal(err)
	}
	closed, err := client.CreatePlaybackStream(t.Context(), native.PlaybackParams{SampleSpec: spec, ChannelMap: channels})
	if err != nil {
		t.Fatal(err)
	}
	if err := closed.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	sinkInput, sourceOutput := playback.Info().SinkInput, record.Info().SourceOutput
	next := func(what string) native.SubscribeEvent {
		t.Helper()
		select {
		case ev := <-events:
			return ev
		case <-time.After(eventWait):
			t.Fatalf("no event for %s within %s", what, eventWait)
			return native.SubscribeEvent{}
		}
	}
	for _, want := range []native.SubscribeEvent{
		{Facility: native.FacilitySinkInput, Operation: native.OpNew, Index: sinkInput},
		{Facility: native.FacilitySourceOutput, Operation: native.OpNew, Index: sourceOutput},
		{Facility: native.FacilitySinkInput, Operation: native.OpNew, Index: closed.Info().SinkInput},
		{Facility: native.FacilitySinkInput, Operation: native.OpRemove, Index: closed.Info().SinkInput},
	} {
		if got := next("the client's streams"); got != want {
			t.Fatalf("event %+v, want %+v", got, want)
		}
	}

	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	removed := []native.SubscribeEvent{next("the disconnect"), next("the disconnect")}
	want := []native.SubscribeEvent{
		{Facility: native.FacilitySourceOutput, Operation: native.OpRemove, Index: sourceOutput},
		{Facility: native.FacilitySinkInput, Operation: native.OpRemove, Index: sinkInput},
	}
	if !slices.Equal(removed, want) {
		t.Errorf("the disconnect announced %+v, want %+v", removed, want)
	}

	inputs, err := watcher.SinkInputs(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	outputs, err := watcher.SourceOutputs(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if got := indexes(inputs); !slices.Equal(got, []uint32{50}) {
		t.Errorf("sink inputs after the disconnect %v, want the scripted 50 alone", got)
	}
	if got := indexes(outputs); !slices.Equal(got, []uint32{51}) {
		t.Errorf("source outputs after the disconnect %v, want the scripted 51 alone", got)
	}
	if _, err := watcher.SinkInput(t.Context(), sinkInput); err == nil {
		t.Errorf("sink input %d of the disconnected client is still served", sinkInput)
	}
	if _, ok := srv.SourceOutput(sourceOutput); ok {
		t.Errorf("source output %d of the disconnected client is still held", sourceOutput)
	}
	if n := len(srv.Playbacks()) + len(srv.Records()); n != 0 {
		t.Errorf("%d streams of the disconnected client still live", n)
	}
	select {
	case ev := <-events:
		t.Errorf("the disconnect announced %+v too: the stream the client closed itself was removed twice", ev)
	default:
	}
}
