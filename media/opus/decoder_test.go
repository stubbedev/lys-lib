package opus

import (
	"bytes"
	"errors"
	"testing"

	"github.com/stubbedev/lys-lib/testbuild"
)

// rangeOp is one symbol of the range coder round trip: its kind and
// the parameters both ends need.
type rangeOp struct {
	kind         int
	fl, fh, ft   uint32
	bits         uint
	set          bool
	value, decay int
}

func randomRangeOps(n int) []rangeOp {
	seed := uint32(7)
	rnd := func() uint32 {
		seed = 1664525*seed + 1013904223
		return seed >> 8
	}
	ops := make([]rangeOp, n)
	for i := range ops {
		op := rangeOp{kind: int(rnd() % 7)}
		switch op.kind {
		case 0:
			op.ft = 2 + rnd()%5000
			op.fl = rnd() % op.ft
			op.fh = op.fl + 1
		case 1:
			op.bits = uint(1 + rnd()%15)
			op.fl = rnd() % (1 << op.bits)
			op.fh = op.fl + 1
		case 2:
			op.set, op.bits = rnd()&1 == 1, uint(1+rnd()%15)
		case 3:
			op.value = int(rnd() % 11)
		case 4:
			op.ft = 2 + rnd()%(1<<23)*64
			op.fl = rnd() % op.ft
		case 5:
			op.bits = uint(1 + rnd()%16)
			op.fl = rnd() & (1<<op.bits - 1)
		case 6:
			op.value = int(rnd()%41) - 20
			op.fl = (1 + rnd()%189) << 7
			op.decay = int((1 + rnd()%178) << 6)
		}
		ops[i] = op
	}
	return ops
}

func TestRangeDecoderReadsWhatTheEncoderWrote(t *testing.T) {
	ops := randomRangeOps(2000)
	buf := make([]byte, 4096)
	var enc rangeEncoder
	enc.reset(buf)
	tells := make([][2]int, len(ops))
	for i := range ops {
		op := &ops[i]
		switch op.kind {
		case 0:
			enc.encode(op.fl, op.fh, op.ft)
		case 1:
			enc.encodeBin(op.fl, op.fh, op.bits)
		case 2:
			enc.bitLogp(op.set, op.bits)
		case 3:
			enc.icdf(op.value, trimICDF, 7)
		case 4:
			enc.uint(op.fl, op.ft)
		case 5:
			enc.bits(op.fl, op.bits)
		case 6:
			laplaceEncode(&enc, &op.value, op.fl, uint32(op.decay))
		}
		tells[i] = [2]int{enc.tell(), enc.tellFrac()}
	}
	enc.done()
	if enc.failed {
		t.Fatal("encoder overran")
	}
	var dec rangeDecoder
	dec.reset(buf)
	for i, op := range ops {
		var ok bool
		switch op.kind {
		case 0:
			s := dec.decode(op.ft)
			dec.update(op.fl, op.fh, op.ft)
			ok = s >= op.fl && s < op.fh
		case 1:
			s := dec.decodeBin(op.bits)
			dec.update(op.fl, op.fh, 1<<op.bits)
			ok = s >= op.fl && s < op.fh
		case 2:
			ok = dec.bitLogp(op.bits) == op.set
		case 3:
			ok = dec.icdf(trimICDF, 7) == op.value
		case 4:
			ok = dec.uint(op.ft) == op.fl
		case 5:
			ok = dec.bits(op.bits) == op.fl
		case 6:
			ok = laplaceDecode(&dec, op.fl, uint32(op.decay)) == op.value
		}
		if !ok {
			t.Fatalf("op %d (kind %d) decoded wrong", i, op.kind)
		}
		if got := [2]int{dec.tell(), dec.tellFrac()}; got != tells[i] {
			t.Fatalf("after op %d: tell, tellFrac = %v, the encoder's %v", i, got, tells[i])
		}
	}
	if dec.failed {
		t.Fatal("decoder flagged an error")
	}
}

func encodeStream(t testing.TB, channels, bitrate int, pcm []int16) (*Encoder, [][]byte) {
	t.Helper()
	enc, err := NewEncoder(channels, bitrate, ConstrainedVBR)
	if err != nil {
		t.Fatal(err)
	}
	var packets [][]byte
	for off := 0; off+FrameSize*channels <= len(pcm); off += FrameSize * channels {
		p, err := enc.Encode(nil, pcm[off:off+FrameSize*channels])
		if err != nil {
			t.Fatal(err)
		}
		packets = append(packets, p)
	}
	return enc, packets
}

func TestDecodeReconstructsTheEncoderInput(t *testing.T) {
	for _, s := range testSignals() {
		if s.minSNR == 0 {
			continue
		}
		t.Run(s.name, func(t *testing.T) {
			enc, packets := encodeStream(t, s.channels, 128000, s.pcm)
			d, err := NewDecoder(enc.AppendHead(nil))
			if err != nil {
				t.Fatal(err)
			}
			var got []int16
			for _, p := range packets {
				if got, err = d.Decode(got, p); err != nil {
					t.Fatal(err)
				}
			}
			got = got[d.PreSkip()*s.channels:]
			want := highPassed(s.pcm, s.channels)
			if snr := snr(want[:len(got)], got, s.channels, 0); snr < s.minSNR {
				t.Errorf("SNR %.2f dB, want at least %.1f dB", snr, s.minSNR)
			}
		})
	}
}

func TestDecodeDownmixesAndUpmixes(t *testing.T) {
	s := testSignals()[0]
	stereo, packets := encodeStream(t, s.channels, 128000, s.pcm)
	mono := append([]byte(nil), stereo.AppendHead(nil)...)
	mono[9] = 3 - byte(s.channels)
	d, err := NewDecoder(mono)
	if err != nil {
		t.Fatal(err)
	}
	var got []int16
	for _, p := range packets {
		if got, err = d.Decode(got, p); err != nil {
			t.Fatal(err)
		}
	}
	if want := len(packets) * FrameSize * (3 - s.channels); len(got) != want {
		t.Errorf("%d samples, want %d", len(got), want)
	}
}

func TestDecodeRejects(t *testing.T) {
	head := (&Encoder{channels: 2}).AppendHead(nil)
	d, err := NewDecoder(head)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name   string
		packet []byte
		want   error
	}{
		{"empty", nil, ErrInvalidPacket},
		{"SILK", []byte{0x08, 1, 2, 3}, ErrUnsupportedMode},
		{"hybrid", []byte{12 << 3, 1, 2, 3}, ErrUnsupportedMode},
		{"odd code 1", []byte{31<<3 | 1, 1, 2, 3}, ErrInvalidPacket},
		{"code 2 overrun", []byte{31<<3 | 2, 200, 1}, ErrInvalidPacket},
		{"code 3 no count", []byte{31<<3 | 3}, ErrInvalidPacket},
		{"code 3 over 120 ms", []byte{31<<3 | 3, 7, 0}, ErrInvalidPacket},
		{"code 3 padding past end", []byte{31<<3 | 3, 0x41, 200, 1}, ErrInvalidPacket},
		{"frame past 1275 bytes", append([]byte{31 << 3}, make([]byte, 1276)...), ErrInvalidPacket},
	} {
		if _, err := d.Decode(nil, c.packet); !errors.Is(err, c.want) {
			t.Errorf("%s: error %v, want %v", c.name, err, c.want)
		}
	}
	for _, h := range [][]byte{head[:10], append([]byte("OpusTags"), head[8:]...)} {
		if _, err := NewDecoder(h); !errors.Is(err, ErrInvalidPacket) {
			t.Errorf("header % x: error %v", h, err)
		}
	}
	multi := bytes.Clone(head)
	multi[18] = 1
	if _, err := NewDecoder(multi); !errors.Is(err, ErrUnsupportedMode) {
		t.Errorf("mapping family 1: error %v", err)
	}
}

func TestDecodeSplitsCodedFrames(t *testing.T) {
	s := testSignals()[0]
	_, packets := encodeStream(t, s.channels, 128000, s.pcm[:4*FrameSize*s.channels])
	frame := func(p []byte) []byte { return p[1:] }
	toc := packets[0][0] &^ 3
	two := appendFrameLength([]byte{toc | 2}, len(frame(packets[0])))
	two = append(two, frame(packets[0])...)
	two = append(two, frame(packets[1])...)
	three := []byte{toc | 3, 0x80 | 0x40 | 3, 2}
	for _, p := range packets[:2] {
		three = appendFrameLength(three, len(frame(p)))
	}
	for _, p := range packets[:3] {
		three = append(three, frame(p)...)
	}
	three = append(three, 0, 0)
	for _, c := range []struct {
		name   string
		packet []byte
		frames int
	}{{"code 2", two, 2}, {"code 3 VBR padded", three, 3}} {
		d, err := NewDecoder((&Encoder{channels: s.channels}).AppendHead(nil))
		if err != nil {
			t.Fatal(err)
		}
		ref, err := NewDecoder((&Encoder{channels: s.channels}).AppendHead(nil))
		if err != nil {
			t.Fatal(err)
		}
		got, err := d.Decode(nil, c.packet)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		var want []int16
		for _, p := range packets[:c.frames] {
			if want, err = ref.Decode(want, p); err != nil {
				t.Fatal(err)
			}
		}
		if !slicesEqual(got, want) {
			t.Errorf("%s: %d samples differ from the frames decoded one by one (%d)", c.name, len(got), len(want))
		}
	}
}

// appendFrameLength codes a frame length in one or two bytes (RFC 6716
// section 3.2.1).
func appendFrameLength(b []byte, n int) []byte {
	if n < 252 {
		return append(b, byte(n))
	}
	b0 := 252 + n&3
	return append(b, byte(b0), byte((n-b0)/4))
}

func slicesEqual(a, b []int16) bool { return bytes.Equal(int16Bytes(a), int16Bytes(b)) }

func int16Bytes(s []int16) []byte {
	b := make([]byte, 0, 2*len(s))
	for _, v := range s {
		b = append(b, byte(v), byte(v>>8))
	}
	return b
}

func TestDecodeAllocatesNothing(t *testing.T) {
	if testbuild.Race {
		t.Skip("-race instrumentation allocates")
	}
	s := testSignals()[0]
	enc, packets := encodeStream(t, s.channels, 128000, s.pcm)
	d, err := NewDecoder(enc.AppendHead(nil))
	if err != nil {
		t.Fatal(err)
	}
	dst := make([]int16, 0, FrameSize*s.channels)
	if _, err := d.Decode(dst, packets[0]); err != nil {
		t.Fatal(err)
	}
	i := 1
	allocs := testing.AllocsPerRun(50, func() {
		if _, err := d.Decode(dst[:0], packets[i%len(packets)]); err != nil {
			t.Fatal(err)
		}
		i++
	})
	if allocs != 0 {
		t.Errorf("Decode allocates %.1f times per packet, want 0", allocs)
	}
}

func FuzzDecode(f *testing.F) {
	s := testSignals()[0]
	_, packets := encodeStream(f, s.channels, 16000, s.pcm[:8*FrameSize*s.channels])
	for _, p := range packets {
		f.Add(p)
	}
	f.Add([]byte{31<<3 | 3, 0xc5, 255, 3})
	f.Fuzz(func(t *testing.T, packet []byte) {
		for _, channels := range []int{1, 2} {
			d, err := NewDecoder((&Encoder{channels: channels}).AppendHead(nil))
			if err != nil {
				t.Fatal(err)
			}
			pcm, err := d.Decode(nil, packet)
			if err != nil {
				continue
			}
			if len(pcm)%channels != 0 || len(pcm) > maxPacketDur*channels {
				t.Fatalf("%d samples from a %d-byte packet", len(pcm), len(packet))
			}
		}
	})
}

func BenchmarkDecode(b *testing.B) {
	s := testSignals()[0]
	enc, packets := encodeStream(b, s.channels, 128000, s.pcm)
	d, err := NewDecoder(enc.AppendHead(nil))
	if err != nil {
		b.Fatal(err)
	}
	dst := make([]int16, 0, FrameSize*s.channels)
	b.ReportAllocs()
	for i := 0; b.Loop(); i++ {
		if _, err := d.Decode(dst[:0], packets[i%len(packets)]); err != nil {
			b.Fatal(err)
		}
	}
}
