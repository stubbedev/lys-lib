package vorbis

import (
	"encoding/binary"
	"errors"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
)

type fixture struct {
	name       string
	rate       int
	channels   int
	frames     int
	floor      string
	residues   []int
	refStart   int
	refFrames  int
	submaps    int
	coupled    bool
	blocksizes [2]int
}

var fixtures = []fixture{
	{name: "floor0-residue0", rate: 44100, channels: 2, frames: 7936, floor: "*vorbis.floor0", residues: []int{0, 0}, refFrames: 7936, submaps: 1, blocksizes: [2]int{256, 2048}},
	{name: "stereo-coupled", rate: 44100, channels: 2, frames: 11025, floor: "*vorbis.floor1", residues: []int{2, 2}, refFrames: 11025, submaps: 1, coupled: true, blocksizes: [2]int{256, 2048}},
	{name: "stereo-q10", rate: 48000, channels: 2, frames: 9600, floor: "*vorbis.floor1", residues: []int{2, 2}, refFrames: 9600, submaps: 1, coupled: true, blocksizes: [2]int{256, 2048}},
	{name: "mono-one-blocksize", rate: 22050, channels: 1, frames: 11025, floor: "*vorbis.floor1", residues: []int{1}, refFrames: 11025, submaps: 1, blocksizes: [2]int{1024, 1024}},
	{name: "mono-8khz", rate: 8000, channels: 1, frames: 4000, floor: "*vorbis.floor1", residues: []int{1}, refFrames: 4000, submaps: 1, blocksizes: [2]int{512, 512}},
	{name: "surround-silent-channels", rate: 44100, channels: 6, frames: 17640, floor: "*vorbis.floor1", residues: []int{2, 2, 1}, refStart: 4400, refFrames: 1400, submaps: 2, coupled: true, blocksizes: [2]int{512, 4096}},
}

func readFixture(t testing.TB, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func readReference(t testing.TB, name string) []int16 {
	raw := readFixture(t, name+".pcm")
	ref := make([]int16, len(raw)/2)
	for i := range ref {
		ref[i] = int16(binary.LittleEndian.Uint16(raw[2*i:]))
	}
	return ref
}

func TestFixturesCoverTheSpecification(t *testing.T) {
	for _, f := range fixtures {
		t.Run(f.name, func(t *testing.T) {
			ogg := newOggReader(readFixture(t, f.name+".ogg"))
			d, err := readHeaders(ogg)
			if err != nil {
				t.Fatal(err)
			}
			if d.blocksizes != f.blocksizes {
				t.Errorf("block sizes %v, want %v", d.blocksizes, f.blocksizes)
			}
			for i, fl := range d.floors {
				if got := typeName(fl); got != f.floor {
					t.Errorf("floor %d is %s, want %s", i, got, f.floor)
				}
			}
			var kinds []int
			for _, r := range d.residues {
				kinds = append(kinds, r.kind)
			}
			if !slices.Equal(kinds, f.residues) {
				t.Errorf("residue types %v, want %v", kinds, f.residues)
			}
			submaps, coupled := 0, false
			for _, m := range d.mappings {
				submaps = max(submaps, len(m.floors))
				coupled = coupled || len(m.coupling) > 0
			}
			if submaps != f.submaps || coupled != f.coupled {
				t.Errorf("%d submaps, coupled %t; want %d, %t", submaps, coupled, f.submaps, f.coupled)
			}
		})
	}
}

func typeName(f floor) string {
	switch f.(type) {
	case *floor0:
		return "*vorbis.floor0"
	case *floor1:
		return "*vorbis.floor1"
	}
	return "unknown"
}

func TestDecodeMatchesLibvorbis(t *testing.T) {
	for _, f := range fixtures {
		t.Run(f.name, func(t *testing.T) {
			s, err := Decode(readFixture(t, f.name+".ogg"))
			if err != nil {
				t.Fatal(err)
			}
			if s.SampleRate != f.rate || s.Channels != f.channels || s.Frames() != f.frames {
				t.Fatalf("decoded %d Hz, %d channels, %d frames; want %d, %d, %d", s.SampleRate, s.Channels, s.Frames(), f.rate, f.channels, f.frames)
			}
			ref := readReference(t, f.name)
			if len(ref) != f.refFrames*f.channels {
				t.Fatalf("reference holds %d samples, want %d", len(ref), f.refFrames*f.channels)
			}
			got := s.Samples[f.refStart*f.channels:][:len(ref)]
			off := 0
			for i, want := range ref {
				d := int(got[i]) - int(want)
				if d < -1 || d > 1 {
					t.Fatalf("frame %d channel %d: %d, libvorbis %d", f.refStart+i/f.channels, i%f.channels, got[i], want)
				}
				if d != 0 {
					off++
				}
			}
			if off*100 > len(ref) {
				t.Errorf("%d of %d samples differ from libvorbis by one", off, len(ref))
			}
		})
	}
}

func TestDecodeIsDeterministic(t *testing.T) {
	data := readFixture(t, "stereo-coupled.ogg")
	a, err := Decode(data)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Decode(data)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(a.Samples, b.Samples) {
		t.Fatal("two decodes of one stream differ")
	}
}

func TestDecodeRejects(t *testing.T) {
	good := readFixture(t, "stereo-coupled.ogg")
	pages := splitPages(t, good)
	corruptCRC := slices.Clone(good)
	corruptCRC[pageOffset(pages, 2)+40] ^= 0x10
	gap := slices.Concat(pages[0], pages[2])
	opus := page(t, 7, 0, flagBOS, []byte("OpusHead\x01\x02\x38\x01\x80\xbb\x00\x00\x00\x00\x00"))
	for _, c := range []struct {
		name string
		data []byte
		want error
	}{
		{"empty", nil, ErrNotVorbis},
		{"wav", []byte("RIFF\x24\x00\x00\x00WAVEfmt "), ErrNotVorbis},
		{"opus", opus, ErrNotVorbis},
		{"truncated page", good[:len(good)-100], ErrTruncated},
		{"truncated header", good[:20], ErrTruncated},
		{"headers only", slices.Concat(pages[0]), ErrTruncated},
		{"bad checksum", corruptCRC, ErrCorrupt},
		{"page gap", gap, ErrCorrupt},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, err := Decode(c.data)
			if !errors.Is(err, c.want) {
				t.Fatalf("Decode = %d samples, %v; want %v", len(s.Samples), err, c.want)
			}
		})
	}
}

func TestDecodeEndsAtALastPageWithoutEOS(t *testing.T) {
	good := readFixture(t, "floor0-residue0.ogg")
	pages := splitPages(t, good)
	last := slices.Clone(pages[len(pages)-1])
	last[5] &^= flagEOS
	reseal(last)
	data := slices.Concat(slices.Concat(pages[:len(pages)-1]...), last)
	s, err := Decode(data)
	if err != nil {
		t.Fatal(err)
	}
	want, err := Decode(good)
	if err != nil {
		t.Fatal(err)
	}
	if s.Frames() < want.Frames() || !slices.Equal(s.Samples[:len(want.Samples)], want.Samples) {
		t.Fatalf("without EOS: %d frames, with: %d, or their samples differ", s.Frames(), want.Frames())
	}
}

func TestDecodeSkipsOtherLogicalStreams(t *testing.T) {
	good := readFixture(t, "stereo-coupled.ogg")
	pages := splitPages(t, good)
	foreign := page(t, 99, 0, flagBOS, []byte("\x80theora"))
	var mixed []byte
	for i, p := range pages {
		mixed = append(mixed, p...)
		if i == 1 {
			mixed = append(mixed, foreign...)
		}
	}
	got, err := Decode(mixed)
	if err != nil {
		t.Fatal(err)
	}
	want, err := Decode(good)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got.Samples, want.Samples) {
		t.Fatal("a multiplexed foreign stream changed the decode")
	}
}

func TestDecodePacketAllocatesNothing(t *testing.T) {
	for _, f := range fixtures {
		t.Run(f.name, func(t *testing.T) {
			ogg := newOggReader(readFixture(t, f.name+".ogg"))
			d, err := readHeaders(ogg)
			if err != nil {
				t.Fatal(err)
			}
			var packets [][]byte
			for {
				p, ok, err := ogg.next()
				if err != nil {
					t.Fatal(err)
				}
				if !ok {
					break
				}
				packets = append(packets, slices.Clone(p.data))
			}
			out := make([]int16, 0, (len(packets)+1)*f.blocksizes[1]*f.channels)
			allocs := testing.AllocsPerRun(5, func() {
				d.started = false
				out = out[:0]
				for _, p := range packets {
					if out, err = d.decodePacket(p, out); err != nil {
						t.Fatal(err)
					}
				}
			})
			if allocs != 0 {
				t.Fatalf("decoding %d packets allocates %.0f times", len(packets), allocs)
			}
		})
	}
}

func TestDecodeAllocatesOnlyItsSetup(t *testing.T) {
	data := readFixture(t, "stereo-coupled.ogg")
	allocs := testing.AllocsPerRun(5, func() {
		if _, err := Decode(data); err != nil {
			t.Fatal(err)
		}
	})
	ogg := newOggReader(data)
	if _, err := readHeaders(ogg); err != nil {
		t.Fatal(err)
	}
	setup := testing.AllocsPerRun(5, func() {
		if _, err := readHeaders(newOggReader(data)); err != nil {
			t.Fatal(err)
		}
	})
	if extra := allocs - setup; extra > 3 {
		t.Fatalf("Decode allocates %.0f times past its %.0f setup allocations", extra, setup)
	}
}

func TestIMDCTMatchesTheDefinition(t *testing.T) {
	for _, n := range []int{64, 256, 2048, 8192} {
		x := make([]float32, n/2)
		for i := range x {
			x[i] = float32((i*7919)%201-100) / 100
		}
		got := make([]float32, n)
		newIMDCT(n).inverse(x, got)
		for _, i := range []int{0, 1, n/4 - 1, n / 4, n/2 + 3, 3 * n / 4, n - 1} {
			want := naiveIMDCT(x, i)
			if d := float64(got[i]) - want; d > 1e-3*float64(n) || d < -1e-3*float64(n) {
				t.Fatalf("n=%d y[%d] = %g, definition %g", n, i, got[i], want)
			}
		}
	}
}

func naiveIMDCT(x []float32, i int) float64 {
	n := 2 * len(x)
	var sum float64
	for k, v := range x {
		sum += float64(v) * math.Cos(2*3.141592653589793/float64(n)*(float64(i)+0.5+float64(n)/4)*(float64(k)+0.5))
	}
	return sum
}

func BenchmarkDecode(b *testing.B) {
	for _, name := range []string{"stereo-coupled", "stereo-q10", "floor0-residue0", "surround-silent-channels"} {
		b.Run(name, func(b *testing.B) {
			data := readFixture(b, name+".ogg")
			s, err := Decode(data)
			if err != nil {
				b.Fatal(err)
			}
			b.SetBytes(int64(len(s.Samples) * 2))
			b.ReportAllocs()
			for b.Loop() {
				if _, err := Decode(data); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkIMDCT(b *testing.B) {
	for _, n := range []int{256, 2048} {
		b.Run(strconv.Itoa(n), func(b *testing.B) {
			t := newIMDCT(n)
			x := make([]float32, n/2)
			y := make([]float32, n)
			for i := range x {
				x[i] = float32(i % 17)
			}
			b.ReportAllocs()
			for b.Loop() {
				t.inverse(x, y)
			}
		})
	}
}

func splitPages(t *testing.T, data []byte) [][]byte {
	t.Helper()
	var pages [][]byte
	for off := 0; off < len(data); {
		n := int(data[off+26])
		size := pageHeaderSize + n
		for _, l := range data[off+pageHeaderSize : off+pageHeaderSize+n] {
			size += int(l)
		}
		pages = append(pages, data[off:off+size])
		off += size
	}
	return pages
}

func pageOffset(pages [][]byte, i int) int {
	off := 0
	for _, p := range pages[:i] {
		off += len(p)
	}
	return off
}

func page(t *testing.T, serial, seq uint32, flags byte, body []byte) []byte {
	t.Helper()
	if len(body) >= 255 {
		t.Fatalf("test page body of %d bytes needs more than one segment", len(body))
	}
	p := make([]byte, pageHeaderSize+1, pageHeaderSize+1+len(body))
	copy(p, "OggS")
	p[5] = flags
	binary.LittleEndian.PutUint32(p[14:], serial)
	binary.LittleEndian.PutUint32(p[18:], seq)
	p[26] = 1
	p[27] = byte(len(body))
	p = append(p, body...)
	reseal(p)
	return p
}

func reseal(p []byte) {
	binary.LittleEndian.PutUint32(p[22:], 0)
	binary.LittleEndian.PutUint32(p[22:], pageCRC(p))
}

func FuzzDecode(f *testing.F) {
	for _, fx := range fixtures {
		f.Add(readFixture(f, fx.name+".ogg"))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		data = slices.Clone(data)
		resealAll(data)
		s, err := Decode(data)
		if err != nil {
			if !errors.Is(err, ErrNotVorbis) && !errors.Is(err, ErrCorrupt) && !errors.Is(err, ErrTruncated) {
				t.Fatalf("Decode failed with an undefined error: %v", err)
			}
			return
		}
		if len(s.Samples)%s.Channels != 0 {
			t.Fatalf("%d samples do not fill %d channels", len(s.Samples), s.Channels)
		}
	})
}

func resealAll(data []byte) {
	for off := 0; off+pageHeaderSize <= len(data); {
		n := int(data[off+26])
		if off+pageHeaderSize+n > len(data) {
			return
		}
		size := pageHeaderSize + n
		for _, l := range data[off+pageHeaderSize : off+pageHeaderSize+n] {
			size += int(l)
		}
		if off+size > len(data) {
			return
		}
		reseal(data[off : off+size])
		off += size
	}
}
