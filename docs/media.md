# Media

## Video encoding

`gpu.Encoder` encodes H.264 and H.265 on the device's hardware encoder: its
Vulkan video queue when that encodes the codec, else its VA-API driver.
`Backend` reports which, `VulkanVideo` or `VAAPI`. VA-API is there for GPUs whose
Vulkan driver encodes no video, and [needs `libva`](/overview#pure-go).

```go
package main

import (
	"log"
	"slices"
	"time"

	"github.com/stubbedev/lys-lib/fourcc"
	"github.com/stubbedev/lys-lib/gpu"
)

func main() {
	dev, err := gpu.Open()
	if err != nil {
		log.Fatal(err)
	}
	defer dev.Close()
	if !slices.Contains(dev.Codecs(), gpu.H264) {
		log.Fatal("this device does not encode H.264")
	}

	enc, err := dev.NewEncoder(gpu.EncoderConfig{
		Codec:       gpu.H264,
		Width:       1920,
		Height:      1080,
		FrameRate:   60,
		KeyInterval: 120,
		QP:          20,
	})
	if err != nil {
		log.Fatal(err)
	}
	defer enc.Close()

	modifiers, err := dev.ExportModifiers(fourcc.ARGB8888)
	if err != nil {
		log.Fatal(err)
	}
	frame, err := dev.NewBuffer(1920, 1080, fourcc.ARGB8888, modifiers)
	if err != nil {
		log.Fatal(err)
	}
	defer frame.Close()

	packets, err := enc.Encode(frame, time.Second/60)
	if err != nil {
		log.Fatal(err)
	}
	rest, err := enc.Flush()
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("%s: %d packets", enc.Backend(), len(packets)+len(rest))
}
```

The source is a `Buffer` a compositor copied a screen into: hand `frame.Dmabuf()`
to [`capture`](/wayland#capture) to fill it. A compute pass crops, turns, overlays
(`SetOverlay`) and converts it to NV12, and the backend encodes that while the
next frame converts, so `src` is free to take the next frame when `Encode`
returns. Like the `Device`, an `Encoder` belongs to one goroutine.

`NewEncoder` fails with `ErrNoVideoEncoder`, carrying both backends' reasons,
when neither encodes the codec. A device without constant-QP coding runs its own
rate control at a bitrate matching QP 20 on desktop content.

Each `Packet` is one Annex-B access unit without the parameter sets. `ParameterSets`
gives those as NAL units without start codes: the SPS and PPS for H.264, the VPS,
SPS and PPS for H.265. That is the shape `media/mux` takes.

## media/mux

`mux` writes timestamped H.264, H.265 and Opus samples into fragmented MP4
(`NewMP4`) and Matroska (`NewMKV`) files. Both write whole fragments or clusters
at a time, so a file cut short by a crash still plays up to the last one written.

Video samples are Annex-B access units without B-frames, and audio samples are
single Opus packets at 48 kHz. Each sample's duration runs to the next one's PTS
on the same track, so PTS must increase strictly within a track, on a clock
shared by all tracks.

## media/opus

`opus` is a pure-Go Opus encoder (RFC 6716) for lys-shell's recordings:
CELT-only fullband 20 ms frames at a constant or constrained variable bitrate,
from 48 kHz interleaved signed 16-bit samples, one or two channels. It ports the
RFC's reference encoder with libopus's later analysis, and leaves out libopus's
tonality analysis.

This writes one second of silence into an MP4 with one Opus track:

```go
package main

import (
	"log"
	"os"
	"time"

	"github.com/stubbedev/lys-lib/media/mux"
	"github.com/stubbedev/lys-lib/media/opus"
)

func main() {
	const channels = 2
	enc, err := opus.NewEncoder(channels, 128000, opus.CBR)
	if err != nil {
		log.Fatal(err)
	}

	out, err := os.Create("silence.mp4")
	if err != nil {
		log.Fatal(err)
	}
	defer out.Close()
	m, err := mux.NewMP4(out, mux.Opus{Channels: channels, PreSkip: opus.PreSkip})
	if err != nil {
		log.Fatal(err)
	}

	pcm := make([]int16, opus.FrameSize*channels)
	frame := time.Second * opus.FrameSize / opus.SampleRate
	for i := range 50 {
		packet, err := enc.Encode(nil, pcm)
		if err != nil {
			log.Fatal(err)
		}
		sample := mux.Sample{Track: 0, PTS: time.Duration(i) * frame, Data: packet}
		if err := m.WriteSample(sample); err != nil {
			log.Fatal(err)
		}
	}
	if err := m.Close(); err != nil {
		log.Fatal(err)
	}
}
```

## media/vorbis

`vorbis` is a pure-Go Ogg Vorbis decoder (Vorbis I) for the sounds freedesktop
sound themes ship. `Decode` reads the first logical Vorbis stream of an Ogg file
in one call and returns its sample rate, channel count and interleaved signed
16-bit samples, the layout a PulseAudio sample cache upload or a playback stream
takes. It covers the whole specification (floor types 0 and 1, residue types 0,
1 and 2, coupling, any channel count), checks every page's CRC, and matches
libvorbis to within one least significant bit. Invalid input fails with
`ErrNotVorbis`, `ErrCorrupt` or `ErrTruncated`; the setup header's codebooks are
bounded, so a hostile file cannot make it allocate without limit.

```go
package main

import (
	"fmt"
	"log"
	"os"

	"github.com/stubbedev/lys-lib/media/vorbis"
)

func main() {
	data, err := os.ReadFile("/run/current-system/sw/share/sounds/freedesktop/stereo/message-new-instant.oga")
	if err != nil {
		log.Fatal(err)
	}
	sound, err := vorbis.Decode(data)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(sound.SampleRate, sound.Channels, sound.Frames())
}
```

## audiomix

`audiomix.Mixer` sums sources of interleaved 16-bit PCM into frames, sample by
sample with clipping. `Push` queues what a source delivered and `Mix` takes the
oldest samples of every source, with missing ones as silence. A source running
fast is trimmed from its oldest samples past the backlog given to `New`, rather
than growing without end.

## v4l2

`v4l2` streams a V4L2 capture node in pure Go, with mmap buffers over ioctls.
`Open` takes the device, a frame size and a frame rate cap, and asks for YUYV,
else MJPEG, at about the frame's width and at least `MinWidth`.

`Frames` delivers `*image.RGBA` pictures letterboxed to the frame's aspect, and
`Recycle` hands one back for reuse. `Err` reports why the stream ended, and
`CaptureCard` names a node that captures video.
