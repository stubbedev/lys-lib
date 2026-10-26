package mux

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func unhex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestAVCConfigMatchesFFmpeg(t *testing.T) {
	for _, c := range []struct {
		name, sps, pps, avcC string
		w, h                 int
	}{
		{
			"high 4:2:0 cropped",
			"67640015acd94141feab0110000003001000000780f162d960", "68ebe3cb22c0",
			"01640015ffe1001967640015acd94141feab0110000003001000000780f162d96001000668ebe3cb22c0fdf8f800",
			318, 238,
		},
		{
			"high 4:4:4 with scaling lists",
			"67f4000a919b288f6022000003000200000300641e244b2c", "68ebe3c44c000440",
			"01f4000affe1001867f4000a919b288f6022000003000200000300641e244b2c01000868ebe3c44c000440fff8f800",
			64, 48,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			sps, pps := unhex(t, c.sps), unhex(t, c.pps)
			s, err := parseAVCSPS(sps)
			if err != nil {
				t.Fatal(err)
			}
			if s.width != c.w || s.height != c.h {
				t.Errorf("size %dx%d, want %dx%d", s.width, s.height, c.w, c.h)
			}
			if got := hex.EncodeToString(avcC(s, sps, pps)); got != c.avcC {
				t.Errorf("avcC\n got %s\nwant %s", got, c.avcC)
			}
		})
	}
}

func TestHEVCConfigMatchesFFmpeg(t *testing.T) {
	vps := unhex(t, "40010c01ffff01600000030090000003000003003f959809")
	sps := unhex(t, "42010101600000030090000003000003003fa00a080f1d565959a4932bc05a020000030002000003007810")
	pps := unhex(t, "4401c172b46240")
	s, err := parseHEVCSPS(sps)
	if err != nil {
		t.Fatal(err)
	}
	if s.width != 318 || s.height != 238 {
		t.Errorf("size %dx%d, want 318x238", s.width, s.height)
	}
	want := "0101600000009000000000003ff000fcfdf8f800000f03a00001001840010c01ffff01600000030090000003000003003f959809a10001002b42010101600000030090000003000003003fa00a080f1d565959a4932bc05a020000030002000003007810a2000100074401c172b46240"
	if got := hex.EncodeToString(hvcC(s, vps, sps, pps)); got != want {
		t.Errorf("hvcC\n got %s\nwant %s", got, want)
	}
}

func TestParameterSetsRejectTruncationAndWrongTypes(t *testing.T) {
	sps := unhex(t, "67640015acd94141feab0110000003001000000780f162d960")
	if _, err := parseAVCSPS(sps[:6]); err == nil {
		t.Error("truncated H.264 SPS accepted")
	}
	if _, err := parseAVCSPS(unhex(t, "68ebe3cb22c0")); err == nil {
		t.Error("PPS accepted as an H.264 SPS")
	}
	if _, err := parseHEVCSPS(unhex(t, "40010c01ffff01600000030090000003000003003f959809")); err == nil {
		t.Error("VPS accepted as an H.265 SPS")
	}
	if _, err := (H264{SPS: sps, PPS: sps}).describe(); err == nil {
		t.Error("SPS accepted as a PPS")
	}
}

func TestUnescapeRBSPDropsEmulationPrevention(t *testing.T) {
	got := unescapeRBSP([]byte{1, 0, 0, 3, 0, 0, 0, 3, 3, 0, 3})
	want := []byte{1, 0, 0, 0, 0, 0, 3, 0, 3}
	if !bytes.Equal(got, want) {
		t.Errorf("unescape % x, want % x", got, want)
	}
}

func TestExpGolomb(t *testing.T) {
	r := bitReader{b: []byte{0b1_010_011_0, 0b0100_0010, 0b1_0000000}}
	got := []int64{int64(r.ue()), int64(r.ue()), int64(r.ue()), int64(r.ue()), int64(r.se())}
	want := []int64{0, 1, 2, 3, -2}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("decoded %v, want %v", got, want)
		}
	}
	if r.short {
		t.Error("reader ran short")
	}
	r.ue()
	if !r.short {
		t.Error("reading past the end is not flagged")
	}
}

func TestAnnexBSplitsOnBothStartCodes(t *testing.T) {
	var got [][]byte
	err := annexB([]byte{0, 0, 0, 1, 9, 0x10, 0, 0, 1, 0x65, 1, 2, 0, 0, 0, 0, 1, 0x41, 3}, func(nal []byte) error {
		got = append(got, nal)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := [][]byte{{9, 0x10}, {0x65, 1, 2}, {0x41, 3}}
	if len(got) != len(want) {
		t.Fatalf("split %x, want %x", got, want)
	}
	for i := range want {
		if !bytes.Equal(got[i], want[i]) {
			t.Errorf("NAL %d % x, want % x", i, got[i], want[i])
		}
	}
	if err := annexB([]byte{0x65, 0, 0, 1, 0x65}, func([]byte) error { return nil }); err == nil {
		t.Error("access unit without a leading start code accepted")
	}
}

func TestBoxesNestWithSizes(t *testing.T) {
	var x boxes
	x.begin("moov")
	x.full("mvhd", 1, 2)
	x.u16(0xabcd)
	x.end()
	x.leaf("free", []byte{7})
	x.end()
	want := "0000001f" + hex.EncodeToString([]byte("moov")) +
		"0000000e" + hex.EncodeToString([]byte("mvhd")) + "01000002abcd" +
		"00000009" + hex.EncodeToString([]byte("free")) + "07"
	if got := hex.EncodeToString(x.b); got != want {
		t.Errorf("boxes\n got %s\nwant %s", got, want)
	}
}

func TestEBMLEncoding(t *testing.T) {
	for _, c := range []struct {
		name string
		do   func(*ebml)
		want string
	}{
		{"one byte id, minimal uint", func(e *ebml) { e.uint(idTrackNumber, 1) }, "d78101"},
		{"zero uint keeps a byte", func(e *ebml) { e.uint(idFlagLacing, 0) }, "9c8100"},
		{"three byte id, wide uint", func(e *ebml) { e.uint(idTimestampScale, 1000000) }, "2ad7b1830f4240"},
		{"two byte size at 127", func(e *ebml) { e.bytes(idCodecPrivate, make([]byte, 127)) }, "63a2407f" + strings.Repeat("00", 127)},
		{"master with an eight byte size", func(e *ebml) {
			e.begin(idVideo)
			e.uint(idPixelWidth, 320)
			e.end()
		}, "e00100000000000004b0820140"},
		{"void fills its total", func(e *ebml) { e.void(4) }, "ec820000"},
		{"float64", func(e *ebml) { e.float(idDuration, 2000) }, "448988409f400000000000"},
	} {
		t.Run(c.name, func(t *testing.T) {
			var e ebml
			c.do(&e)
			if got := hex.EncodeToString(e.b); got != c.want {
				t.Errorf("got %s, want %s", got, c.want)
			}
		})
	}
}

const testAU = "\x00\x00\x00\x01\x65\x88\x84"

func TestSamplesAreValidated(t *testing.T) {
	sps := unhex(t, "67640015acd94141feab0110000003001000000780f162d960")
	pps := unhex(t, "68ebe3cb22c0")
	otherSPS := unhex(t, "67f4000a919b288f6022000003000200000300641e244b2c")
	for _, c := range []struct {
		name    string
		samples []Sample
		want    string
	}{
		{"unknown track", []Sample{{Track: 2, Data: []byte(testAU)}}, "track 2"},
		{"negative pts", []Sample{{PTS: -time.Millisecond, Data: []byte(testAU)}}, "before zero"},
		{"repeated pts", []Sample{{Data: []byte(testAU)}, {Data: []byte(testAU)}}, "does not follow"},
		{"empty", []Sample{{Data: nil}}, "empty sample"},
		{"not Annex-B", []Sample{{Data: []byte{0x65, 1}}}, "start code"},
		{"only a delimiter", []Sample{{Data: []byte("\x00\x00\x01\x09\x10")}}, "no picture"},
		{"changed SPS", []Sample{{Data: append([]byte("\x00\x00\x01"), append(otherSPS, testAU...)...)}}, "parameter set differs"},
	} {
		for _, open := range []func(io.Writer) (muxer, error){
			func(w io.Writer) (muxer, error) { return NewMP4(w, H264{SPS: sps, PPS: pps}, Opus{Channels: 2}) },
			func(w io.Writer) (muxer, error) { return NewMKV(w, H264{SPS: sps, PPS: pps}, Opus{Channels: 2}) },
		} {
			m, err := open(io.Discard)
			if err != nil {
				t.Fatal(err)
			}
			var last error
			for _, s := range c.samples {
				if last = m.WriteSample(s); last != nil {
					break
				}
			}
			if last == nil || !strings.Contains(last.Error(), c.want) {
				t.Errorf("%s: error %v, want one naming %q", c.name, last, c.want)
			}
		}
	}
	if _, err := NewMP4(io.Discard, Opus{Channels: 3}); err == nil {
		t.Error("three-channel Opus accepted")
	}
	if _, err := NewMKV(io.Discard); err == nil {
		t.Error("a file without tracks accepted")
	}
}

type failWriter struct{ n int }

func (f *failWriter) Write(p []byte) (int, error) {
	if f.n == 0 {
		return 0, errors.New("disk full")
	}
	f.n--
	return len(p), nil
}

func TestWriteErrorsStick(t *testing.T) {
	m, err := NewMP4(&failWriter{n: 1}, Opus{Channels: 1})
	if err != nil {
		t.Fatal(err)
	}
	var first error
	for i := range 200 {
		if first = m.WriteSample(Sample{PTS: time.Duration(i) * opusPacket, Data: []byte{0xfc}}); first != nil {
			break
		}
	}
	if first == nil || !strings.Contains(first.Error(), "disk full") {
		t.Fatalf("error %v, want the writer's", first)
	}
	if err := m.Close(); !errors.Is(err, first) && err.Error() != first.Error() {
		t.Errorf("Close after a failed write returns %v, want %v", err, first)
	}
}

type element struct {
	id      uint32
	payload []byte
	at      int
}

func readVint(t *testing.T, b []byte, keepMarker bool) (uint64, int) {
	t.Helper()
	if len(b) == 0 || b[0] == 0 {
		t.Fatalf("bad vint at % x", b[:min(len(b), 8)])
	}
	n := 1
	for b[0]&(0x80>>(n-1)) == 0 {
		n++
	}
	v := uint64(b[0])
	if !keepMarker {
		v &^= 0x80 >> (n - 1)
	}
	for _, c := range b[1:n] {
		v = v<<8 | uint64(c)
	}
	return v, n
}

func children(t *testing.T, b []byte, base int) []element {
	t.Helper()
	var out []element
	for i := 0; i < len(b); {
		id, n := readVint(t, b[i:], true)
		size, m := readVint(t, b[i+n:], false)
		start := i + n + m
		if start+int(size) > len(b) {
			t.Fatalf("element %x at %d runs %d bytes past its parent", id, base+i, start+int(size)-len(b))
		}
		out = append(out, element{id: uint32(id), payload: b[start : start+int(size)], at: base + i})
		i = start + int(size)
	}
	return out
}

func find(t *testing.T, els []element, id uint32) element {
	t.Helper()
	for _, e := range els {
		if e.id == id {
			return e
		}
	}
	t.Fatalf("no element %x", id)
	return element{}
}

func TestMKVCloseFinalisesTheHeaderWhenSeekable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.mkv")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	m, err := NewMKV(f, Opus{Channels: 2, PreSkip: 312})
	if err != nil {
		t.Fatal(err)
	}
	for i := range 150 {
		if err := m.WriteSample(Sample{PTS: time.Duration(i) * opusPacket, Data: []byte{0xfc, byte(i)}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	top := children(t, b, 0)
	if len(top) != 2 || top[0].id != idEBML || top[1].id != idSegment {
		t.Fatalf("top level %+v", top)
	}
	segStart := len(b) - len(top[1].payload)
	seg := children(t, top[1].payload, segStart)
	clusters := 0
	for _, e := range seg {
		if e.id == idCluster {
			clusters++
		}
	}
	if clusters != 3 {
		t.Errorf("%d clusters for three seconds, want 3", clusters)
	}
	seeks := children(t, find(t, seg, idSeekHead).payload, 0)
	if len(seeks) != 3 {
		t.Fatalf("%d seek entries, want 3", len(seeks))
	}
	for _, s := range seeks {
		entry := children(t, s.payload, 0)
		id := binary.BigEndian.Uint32(find(t, entry, idSeekID).payload)
		var pos uint64
		for _, c := range find(t, entry, idSeekPosition).payload {
			pos = pos<<8 | uint64(c)
		}
		if target := find(t, seg, id); uint64(target.at-segStart) != pos {
			t.Errorf("seek entry for %x points at %d, the element is at %d", id, pos, target.at-segStart)
		}
	}
	info := children(t, find(t, seg, idInfo).payload, 0)
	if d := find(t, info, idDuration).payload; hex.EncodeToString(d) != "40a7700000000000" {
		t.Errorf("duration % x, want 3000 ms", d)
	}
	cues := children(t, find(t, seg, idCues).payload, 0)
	if len(cues) != 3 {
		t.Errorf("%d cue points, want one per cluster", len(cues))
	}
	if pos, err := f.Seek(0, io.SeekCurrent); err != nil || pos != int64(len(b)) {
		t.Errorf("writer left at %d (%v), the file ends at %d", pos, err, len(b))
	}
}
