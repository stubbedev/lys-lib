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
  to NV12. It is a submission of the device like a pass, and the Vulkan video
  queue encodes after it on the GPU, by a timeline semaphore.
- The buffer is its writer's again when `Encode` returns. The conversion that
  reads it leaves its fence on the buffer's dmabuf, which whoever writes the
  next frame into it under linux-dmabuf's implicit synchronization (a
  compositor's screencopy) waits for, so `Encode` waits for nothing. Where no
  fence tells the writer (NVIDIA's driver, a device without `sync_file`
  semaphores) `Encode` waits for the conversion instead, as it does for the
  VA-API driver, which takes no semaphore.
- A packet collected before a conversion or an encode that then fails comes
  out of the next `Encode` or `Flush`.
- Every wait is bounded by the device's wait limit, the VA-API driver's for a
  picture too: a hung GPU is an error instead of a frozen caller.
- An `Encoder` belongs to one goroutine.
- `NewEncoder` fails with `ErrNoVideoEncoder`, carrying both backends' reasons,
  when neither encodes the codec.
- Each `Packet` is one Annex-B access unit without parameter sets.
  `ParameterSets` returns them as NAL units without start codes (SPS and PPS for
  H.264; VPS, SPS and PPS for H.265), the form `media/mux` takes.

## Video decoding

`gpu.VideoDecoder` decodes H.264, H.265, AV1 and VP9 on the device's Vulkan
video decode queue, the device the renderer draws with. It takes [`demux`](#media-demux)
packets in decode order and hands out frames in presentation order, each
drawn by a `Pass` (`DrawFrame`) through its YCbCr conversion: the picture never
leaves the GPU.

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
		for frame := dec.Next(); frame != nil; frame = dec.Next() {
			// pass.DrawFrame(frame, ...) at frame.PTS().
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
  or nil until more packets complete one. `Flush` at the end of the track hands
  out the frames held back for reordering.
- `Decode` submits a packet's pictures and makes room for more in flight (up
  to 16) instead of waiting for the GPU. `Busy` says, without waiting, whether
  the GPU is four pictures or more behind: a player that holds its next packet
  back until its loop's next turn while `Busy` never has `Decode` wait.
- Nothing waits on the CPU for a decode. `Next` hands a frame out while its
  picture may still be decoding, and the pass that draws it waits for the
  decode on the GPU: the decode queue signals a timeline semaphore the pass's
  submission waits for. What waits for that pass (`Finish`, a CPU read,
  `Publish` on a device whose dmabufs carry no fence) then waits for the decode
  too; `VideoFrame.Decoded` says, without waiting, whether the decode has
  finished, for a caller that must not stall there. A device decodes only where
  it has timeline semaphores.
- A frame carries its packet's `PTS` and `Duration`, its picture's `Size`
  with the stream's cropping applied, and its `FieldOrder`. `Pass.DrawFrame`
  draws a region of the
  picture, given in the picture's own pixels, as `DrawTexture` draws a
  texture's; the codec's padding around the picture is out of its reach. The
  YCbCr matrix, range and chroma siting come from the stream's VUI, AV1 colour
  config or VP9 colour space (an unnamed matrix is BT.709 from 720 lines up,
  BT.601 below; a VP9 stream's lines are its render size's, which a stream
  that resizes its frames keeps). An AV1 frame's `Size` is its render size
  where that crops the frame, else the frame's; a VP9 frame's is the frame's,
  which changes where the stream resizes. Pictures are luma and
  a plane of interleaved chroma in the stream's depth and chroma format: NV12
  for 8-bit 4:2:0, P010 (`G10X6_B10X6R10X6_2PLANE_420`) for 10-bit, and the
  12-bit and 4:4:4 formats of the same family, all drawn through the sampler's
  YCbCr conversion.
- A frame's texture belongs to the decoder and is never handed out, so nothing
  can update or close it: `Release` hands the frame back, and the decoder
  decodes into it again once the passes that drew it, the open one too, have
  finished. Up to 8 frames may be held beside the ones held for reordering and
  the AV1 and VP9 frames decoded hidden to be shown later; `Close` releases them
  all, and a pass refuses a frame released or of a closed decoder.
- To seek, `Reset` the decoder, `Seek` the file and decode from the keyframe,
  dropping frames before the target. Decoding starts at an IDR or I picture
  (H.264), an IRAP picture (H.265) or a key frame (AV1 and VP9, from the first
  packet marked a keyframe); pictures before it, and leading pictures that predict
  from before it (open-GOP B-frames, RASL pictures), are dropped.
- Streams decode bit-exactly as ffmpeg decodes them: H.264 Baseline, Main,
  High and Extended where it keeps to Main (B-frames, reference marking, every
  picture order count type, scaling matrices, slices, gaps in `frame_num`,
  interlaced frames and fields), H.265 Main and Main 10 (reference
  picture sets in the SPS or slice headers, long-term pictures, CRA starts,
  slices, wavefronts, scaling lists), AV1 Main at 8 and 10 bits (hidden frames
  shown by later frame headers, all seven references, compound and skip modes,
  tiles, 64x64 and 128x128 superblocks, segmentation, delta q and loop filter,
  CDEF, loop restoration, global motion, error resilient frames, screen content
  tools; libaom and SVT-AV1 streams), and VP9 profiles 0 and 2 at 8 and 10
  bits (hidden alternate reference frames in superframes, all three
  references, segmentation with temporal prediction and cyclic refresh, tile
  columns, error resilient and lossless frames, odd sizes, frames predicted
  from references of another size, intra-only frames; libvpx streams in
  WebM and MP4), and, where the device decodes them (NVIDIA does), 12-bit
  H.265 and VP9 profile 2 and 4:4:4 H.265 at 8, 10 and 12 bits (the format
  range extensions). AV1 frames coded at a superres-scaled width are errors
  that say so, as are AV1 inter frames whose size differs from their
  references' (reference scaling) and frames smaller than the device decodes
  (NVIDIA decodes AV1 from 128x128, H.265 from 144x144).
- Interlaced H.264 comes out as it is coded, a frame's two fields woven into
  one picture, the top field its even lines: nothing deinterlaces.
  `VideoFrame.FieldOrder` says whether a frame is two fields and which is the
  earlier (`Progressive`, `TopFieldFirst`, `BottomFieldFirst`), for a caller
  that deinterlaces or tells its output: a frame coded as two field pictures
  or as field and frame macroblock pairs (MBAFF) is two fields, the earlier
  the one of the lower picture order count, the top field where they are
  equal, as ffmpeg says of it; the picture timing SEI is not read. A field
  picture is a decode of its own into its frame's DPB slot, which the device
  holds with the fields' lines interleaved; the frame goes to `Next` when its
  second field has decoded, whether a packet holds both fields (MP4) or one (a
  transport stream), with the first field's `PTS`; each field decodes with the
  parameter sets before it in the packet, which a stream may change between
  the two. A field whose frame's other
  field never comes is decoded for the pictures that predict from it and never
  shown, as ffmpeg shows none. Reference marking is by field (8.2.5): the
  sliding window, and memory management control operations that name single
  fields.
- Of interlaced H.264 the decoder refuses, saying what: a sequence of fields
  on a device with output pictures apart from its reference pictures (the
  fields meet in the reference picture before the frame is copied out, the one
  way verified); a frame whose fields are a reference and none (no decoder
  pairs them, ffmpeg neither); a frame with a short-term and a long-term
  reference field, and a frame decoded as one picture of which one field alone
  is left a reference (a Vulkan Video reference picture is long-term or not,
  and a frame or the fields it was decoded as, as a whole); a field that
  predicts from more reference fields than the device decodes a picture from
  (NVIDIA: 16, each field of a frame decoded as two counting); and a gap in
  `frame_num`, which is inferred in sequences of frames only.
- A VP9 frame is the size its header says, whatever its references' (libvpx
  resizes a stream it cannot fit in its bitrate): the device scales the
  references as it predicts, each picture decoded at its own size into the
  pictures of the session its key frame made, so the frames' `Size` changes
  and the caller scales them to where it draws them. A frame larger than the
  session's pictures starts another session where it leaves nothing of the
  frames before it (a key frame, or an intra-only frame that refreshes every
  reference slot and resets every frame context); one that keeps reference
  pictures or frame contexts is refused, as is one naming a reference VP9
  scales to no frame of its size (less than half or more than 16 times it).
- A frame header that shows a picture shown before (AV1 and VP9
  `show_existing_frame`) gets the picture copied out of its reference picture
  into a frame of its own again, with its film grain; a device with output
  pictures apart from its reference pictures copies nothing out of those, and
  refuses the stream there.
- A VP9 intra frame decodes from frame context 0 and saves to it whatever
  `frame_context_idx` it codes, which the device is told as 0; the context
  coded is the one `reset_frame_context` 2 resets. A Vulkan Video picture
  names one context, so an intra-only frame that resets another one is
  decoded where nothing was saved to that context (resetting it changes
  nothing) and refused where something was.
- AV1 film grain comes out as dav1d synthesizes it, sample for sample. A
  device with output pictures apart from its reference pictures applies it; on
  one that decodes into its reference pictures alone (NVIDIA) the decoder
  synthesizes it: the host makes the grain templates, scaling functions and
  block offsets (`internal/av1`), and a compute pass of the graphics queue
  adds the noise to the frame's picture after its decode, ahead of any pass
  that draws it. Nothing waits for it, and `Decoded` covers it.
- Every other depth and chroma format is put to the device: a stream it
  refuses fails with `ErrNoVideoDecoder` naming the profile, the samples and
  the answer of `vkGetPhysicalDeviceVideoCapabilitiesKHR`
  (`VK_ERROR_VIDEO_PROFILE_FORMAT_NOT_SUPPORTED_KHR` from NVIDIA for H.264
  High 10, High 4:2:2 and High 4:4:4, 4:2:2 H.265, AV1 High and Professional
  and VP9 profiles 1 and 3). Those formats are verified on no device, so one
  that accepts the query is refused them too, saying so, rather than trusted
  with pictures nothing has checked. Monochrome streams have no picture format
  and are refused when parsed.
- A stream in a format that is not decoded fails with `ErrNoVideoDecoder`,
  from `NewVideoDecoder` or the `Decode` that meets it, for every codec: the
  error says what the stream is and wraps what the parser read. A malformed
  stream fails with another error, so `errors.Is(err, gpu.ErrNoVideoDecoder)`
  tells the two apart.
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
  they predict from, and which frame contexts frames saved to; the device
  reads the compressed header and keeps the probability contexts). Their
  tests check them against ffmpeg's `trace_headers` element for element.
  libvpx codes no intra-only frame, no frame shown twice and, through ffmpeg,
  no resized frame: the tests make the first two of the frames it does code
  (`internal/vp9/vp9test`), and `gpu/testdata/vp9-resize.ivf` is a stream
  vpxenc resized (`gpu/testdata/README.md` says how). x264 codes interlaced
  video as frames of field and frame macroblock pairs, never as field
  pictures: the tests make those of the pictures of progressive CAVLC streams
  at half the height, their slice headers rewritten (`gpu/h264cut_test.go`),
  which decode to pictures no encoder meant but the standard defines, and
  ffmpeg decodes as the reference. Field pictures coded with CABAC have no
  such source and no test in the tree; they, and what else real field
  encoders code (temporal direct prediction, weighted prediction, picture
  order count type 1, memory management in multi-reference streams), were
  checked once on the NVIDIA driver against 86 streams of the H.264
  conformance suite (ITU-T H.264.1), 59 of them coding fields or
  macroblock-adaptive frames, 3984 field pictures in all: every one decodes
  as ffmpeg decodes it.
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

Importing the package runs nothing: the MDCT, window and pulse codebook tables
are built by the first `NewEncoder` or `NewDecoder` and shared by every one
after it, so coding a frame does no lookup for them.

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
- Importing the package runs nothing: the transform, window and codebook
  tables are built by the first `NewDecoder` and shared by every decoder after
  it.
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
