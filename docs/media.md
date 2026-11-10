# Media

## Video encoding

`gpu.Encoder` encodes H.264 and H.265 on the device's hardware: its Vulkan video
queue if that encodes the codec, else its VA-API driver
([needs libva](/overview#native-libraries)). `Backend` reports `VulkanVideo` or
`VAAPI`.

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

`EncoderConfig`:

| Field | Effect |
| --- | --- |
| `Codec` | `H264` or `H265` |
| `Width`, `Height` | Frame size (after `Transform`) |
| `FrameRate` | Nominal rate, for the stream's level; frames may arrive at any time |
| `KeyInterval` | Frames between keyframes |
| `Source` | Region of the buffer to read; zero reads all of it |
| `Transform` | The captured output's `wl_output` transform, undone by the encoder |
| `QP` | Constant quantiser, 0 (best) to 51. Devices without constant-QP coding use a bitrate matching QP 20 on desktop content |

- The source is a `Buffer`; pass `frame.Dmabuf()` to [`capture`](/wayland#capture)
  to fill it.
- A compute pass crops, rotates, draws the overlay (`SetOverlay`) and converts
  to NV12. Encoding overlaps the next conversion, so the buffer is free again
  when `Encode` returns.
- An `Encoder` belongs to one goroutine.
- `NewEncoder` fails with `ErrNoVideoEncoder`, carrying both backends' reasons,
  when neither encodes the codec.
- Each `Packet` is one Annex-B access unit without parameter sets.
  `ParameterSets` returns them as NAL units without start codes (SPS and PPS for
  H.264; VPS, SPS and PPS for H.265), the form `media/mux` takes.

## Video decoding

`gpu.VideoDecoder` decodes H.264, H.265, AV1 and VP9 on the device's Vulkan
video decode queue, the device the renderer draws with. It takes [`demux`](#media-demux)
packets in decode order and hands out frames in presentation order, each a
`Texture` a `Pass` draws through its YCbCr conversion: the picture never leaves
the GPU.

```go
package main

import (
	"errors"
	"io"
	"log"

	"github.com/stubbedev/lys-lib/gpu"
	"github.com/stubbedev/lys-lib/media/demux"
)

func main() {
	dev, err := gpu.Open()
	if err != nil {
		log.Fatal(err)
	}
	defer dev.Close()
	file, err := demux.OpenFile("clip.mp4")
	if err != nil {
		log.Fatal(err)
	}
	defer file.Close()
	track := file.Tracks()[0]
	dec, err := dev.NewVideoDecoder(track)
	if errors.Is(err, gpu.ErrNoVideoDecoder) {
		log.Fatal(file.Unsupported(track.Index))
	}
	if err != nil {
		log.Fatal(err)
	}
	defer dec.Close()
	if err := file.Select(track.Index); err != nil {
		log.Fatal(err)
	}
	show := func() {
		for {
			frame, err := dec.Next()
			if err != nil {
				log.Fatal(err)
			}
			if frame == nil {
				return
			}
			// Draw frame.Texture()'s frame.Rect() in a Pass at frame.PTS().
			frame.Release()
		}
	}
	for {
		p, err := file.ReadPacket()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			log.Fatal(err)
		}
		if err := dec.Decode(p); err != nil {
			log.Fatal(err)
		}
		show()
	}
	dec.Flush()
	show()
}
```

- `DecodeCodecs` lists what the device decodes. `NewVideoDecoder` fails with
  `ErrNoVideoDecoder`, naming the codec and why, for any other; lavapipe
  decodes nothing.
- `Decode` takes one packet (an access unit; an AV1 temporal unit or a VP9
  superframe, whose hidden frames it decodes too). `Next` returns the next frame in presentation order,
  or nil until more packets complete one; it waits for that frame's decode.
  `Flush` at the end of the track hands out the frames held back for
  reordering.
- A frame carries its packet's `PTS` and `Duration`. `Texture` is larger than
  the picture by the codec's padding: draw `Rect` of it. Its YCbCr matrix, range
  and chroma siting come from the stream's VUI, AV1 colour config or VP9
  colour space (an unnamed matrix is BT.709 from 720 lines up, BT.601 below).
  An AV1 frame's `Rect` is its render size where that crops the frame, else the
  frame; a VP9 frame's is the frame. An 8-bit stream decodes into NV12
  textures, a 10-bit one into P010 (`G10X6_B10X6R10X6_2PLANE_420`), both drawn
  through the sampler's YCbCr conversion.
- The texture belongs to the decoder: `Release` hands the frame back, and the
  decoder decodes into it again once the passes submitted so far, which may
  sample it, have finished. Up to 8 frames may be held beside the ones held for
  reordering and the AV1 and VP9 frames decoded hidden to be shown later; `Close`
  releases them all.
- To seek, `Reset` the decoder, `Seek` the file and decode from the keyframe,
  dropping frames before the target. Decoding starts at an IDR or I picture
  (H.264), an IRAP picture (H.265) or a key frame (AV1 and VP9, from the first
  packet marked a keyframe); pictures before it, and leading pictures that predict
  from before it (open-GOP B-frames, RASL pictures), are dropped.
- Streams decode bit-exactly as ffmpeg decodes them: H.264 Baseline, Main and
  High (B-frames, reference marking, every picture order count type, scaling
  matrices, slices, gaps in `frame_num`), H.265 Main and Main 10 (reference
  picture sets in the SPS or slice headers, long-term pictures, CRA starts,
  slices, wavefronts, scaling lists), AV1 Main at 8 and 10 bits (hidden frames
  shown by later frame headers, all seven references, compound and skip modes,
  tiles, 64x64 and 128x128 superblocks, segmentation, delta q and loop filter,
  CDEF, loop restoration, global motion, error resilient frames, screen content
  tools; libaom and SVT-AV1 streams), and VP9 profiles 0 and 2 at 8 and 10
  bits (hidden alternate reference frames in superframes, all three
  references, segmentation with temporal prediction and cyclic refresh, tile
  columns, error resilient and lossless frames, odd sizes; libvpx streams in
  WebM and MP4), 4:2:0 progressive only. Interlaced H.264, 10-bit H.264,
  12-bit and 4:2:2/4:4:4 streams are errors that say so, as are AV1 frames
  coded at a superres-scaled width, AV1 and VP9 inter frames whose size
  differs from their references' (reference scaling), a frame showing a frame
  it already showed, and frames smaller than the device decodes (NVIDIA
  decodes AV1 from 128x128, H.265 from 144x144). AV1 film grain is applied
  where the device applies it into distinct output pictures; a frame applying
  grain on a device that decodes only into its reference pictures (NVIDIA) is
  an error that says so.
- A gap in H.264 `frame_num` that the SPS allows
  (`gaps_in_frame_num_value_allowed_flag`) infers the frames it leaves
  non-existing (8.2.5.2): each decodes as a skip slice copying the reference
  frame before it, so it holds a DPB slot and its place in the reference lists,
  and a stream predicting from it (frames lost) reads what ffmpeg conceals it
  with; ffmpeg deblocks such a stream a little differently, taking the copy
  for the frame it copies. A gap the SPS does not allow is an error naming the frames missing.
- `internal/av1` reads the OBUs, sequence headers, frame headers (with the
  reference frame state they predict from) and tile groups; `internal/vp9`
  reads superframe indexes and uncompressed headers (with the reference frame
  sizes, loop filter deltas, segmentation features and motion vector reuse
  they predict from; the device reads the compressed header and keeps the
  probability contexts). Their tests check them against ffmpeg's
  `trace_headers` element for element.
- A device whose decoder writes its output into its reference pictures (NVIDIA)
  has each picture copied into its frame on the decode queue; one with distinct
  output pictures decodes into the frame directly.
- Decoding a frame allocates nothing once the decoder has made its frames.
- A `VideoDecoder` belongs to its device's goroutine.

## media/mux

`mux` writes timestamped H.264, H.265 and Opus samples into fragmented MP4
(`NewMP4`) and Matroska (`NewMKV`).

- Video samples are Annex-B access units without B-frames. Audio samples are
  single Opus packets at 48 kHz.
- A sample lasts until the next PTS on its track; the last repeats the previous
  duration. PTS must strictly increase within a track, on a clock shared by all
  tracks.
- Whole fragments or clusters are written at once, so a truncated file plays up
  to the last one written.

## media/demux

`demux` reads MP4/ISOBMFF (moov first or last, and fragmented) and
Matroska/WebM in process. `OpenFile` gives the tracks (codec, frame size,
sample rate, channels, codec config, delay and pre-roll) and the duration;
`ReadPacket` returns packets in file order with PTS and key flag, and `Select`
skips the data of tracks nobody reads. `Seek` lands on a keyframe from `stss` or
fragment flags in MP4 and from Cues in Matroska, scanning the clusters once when
a file has no Cues. A track whose codec lys cannot decode reports it through
`Unsupported`, an error naming the codec and the file. Every size is checked
before it is allocated; malformed input wraps `ErrMalformed`.

`Track.CodecDelay` is what the container says to trim from the start: an MP4
edit list's media time (AAC's encoder priming) or Matroska's CodecDelay. Old
Matroska AAC tracks that name their profile in the codec ID
(`A_AAC/MPEG4/LC` and friends) and carry no CodecPrivate get an
AudioSpecificConfig built from the profile, sample rate and channels.

## media/opus

`opus` is a pure-Go Opus codec (RFC 6716). The encoder is CELT-only, fullband, 20 ms frames,
from 48 kHz interleaved signed 16-bit PCM, one or two channels. Rate control is
`CBR` or `ConstrainedVBR` (no packet exceeds twice the average). Tonality
analysis is not implemented. `AppendHead` writes the stream's ID header.

The `Decoder` takes an OpusHead and decodes SILK, hybrid and CELT packets of
every frame size (2.5 to 60 ms, up to 120 ms per packet) and bandwidth, with
libopus's mode transitions and redundant CELT frames, within one LSB of
libopus, applying the head's gain and libopus's soft clipping. Channel mapping
families 0 (mono, stereo), 1 (Vorbis order, up to 8 channels: surround) and
255 decode; other families return `ErrUnsupportedMapping`. A frame of at most
one byte (discontinuous transmission) is concealed as libopus conceals a lost
frame: SILK's pitch and noise extrapolation with comfort noise bit-exactly,
CELT's pitch or noise concealment within about 80 dB of libopus. Inband FEC
(LBRR) is skipped; there is no call to conceal a whole lost packet.

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

`vorbis` is a pure-Go Ogg Vorbis I decoder. `Decode` reads the first logical
Vorbis stream of an Ogg file and returns a `Sound`: sample rate, channel count
and interleaved signed 16-bit samples. For packets from a container,
`NewDecoder` takes the three headers and `Decoder.Decode` one packet at a time.

- Full specification: floor types 0 and 1, residue types 0–2, coupling, any
  channel count. Every page CRC is checked.
- Output is within one LSB of libvorbis.
- Errors: `ErrNotVorbis`, `ErrCorrupt`, `ErrTruncated`. Codebook sizes are
  bounded, so hostile input cannot force unbounded allocation.

```go
sound, err := vorbis.Decode(data)
if err != nil {
	return err
}
fmt.Println(sound.SampleRate, sound.Channels, sound.Frames())
```

## media/aac

`aac` is a pure-Go MPEG-4 AAC-LC decoder (ISO/IEC 14496-3, audio object type
2). `NewDecoder` takes the AudioSpecificConfig that `demux` reports as the
track's `Config`; `Decode` turns one raw data block into 1024 interleaved
signed 16-bit samples per channel.

- Channel configurations 1 to 7 (mono to 7.1), output in the configuration's
  order: centre first, then the front pair, the surround pairs and LFE.
- Every LC tool: all window sequences and both window shapes, mid/side and
  intensity stereo, TNS, pulses and perceptual noise substitution, with the
  standard's correlated noise in mid/side bands. Within an LSB of ffmpeg's
  decoder (except the PNS noise itself); no allocation per frame.
- `ErrUnsupported` names what a stream needs beyond LC: another object type
  (HE-AAC's SBR and PS, Main, LTP, ER and USAC), 960-sample frames, a program
  config element instead of a channel configuration, or a coupling channel
  element. Implicitly signalled SBR decodes as its LC core at the core rate.
  Dynamic range control data is ignored. Malformed input wraps `ErrInvalid`.
- An MP4 edit list's encoder priming is in the first packet's negative
  timestamp: drop that many samples from the start.

```go
dec, err := aac.NewDecoder(track.Config)
if err != nil {
	return err
}
pcm, err := dec.Decode(nil, packet.Data)
```

## audiomix

`audiomix.Mixer` sums sources of interleaved 16-bit PCM with clipping.
`New(sources, channels, backlog)` creates it. `Push` queues a source's
little-endian samples; `Mix` fills a frame from every source's oldest samples,
padding missing ones with silence. A source more than `backlog` frames ahead is
trimmed from its oldest samples.

## v4l2

`v4l2` streams a V4L2 capture node in pure Go with mmap buffers.

- `Open(device, frame, maxFPS)` requests YUYV, else MJPEG, near the frame's width
  and at least `MinWidth` (640).
- `Frames` delivers `*image.RGBA` pictures letterboxed to the frame's aspect;
  `Recycle` returns one for reuse; `Err` says why the stream ended; `Format`
  names the negotiated format.
- `CaptureCard` returns a node's card name if it captures video.
