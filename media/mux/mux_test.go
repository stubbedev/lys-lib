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

	"github.com/stubbedev/lys-lib/internal/matroska"
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
			config, err := avcC(s, sps, pps)
			if err != nil {
				t.Fatal(err)
			}
			if got := hex.EncodeToString(config); got != c.avcC {
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
	config, err := hvcC(s, vps, sps, pps)
	if err != nil {
		t.Fatal(err)
	}
	if got := hex.EncodeToString(config); got != want {
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

func TestAnnexBSplitsOnBothStartCodes(t *testing.T) {
	var got [][]byte
	err := annexB([]byte{0, 0, 0, 1, 9, 0x10, 0, 0, 1, 0x65, 1, 2, 0, 0, 0, 0, 1, 0x41, 3}, func(unit []byte) error {
		got = append(got, unit)
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
		{"one byte id, minimal uint", func(e *ebml) { e.uint(matroska.TrackNumber, 1) }, "d78101"},
		{"zero uint keeps a byte", func(e *ebml) { e.uint(matroska.FlagLacing, 0) }, "9c8100"},
		{"three byte id, wide uint", func(e *ebml) { e.uint(matroska.TimestampScale, 1000000) }, "2ad7b1830f4240"},
		{"two byte size at 127", func(e *ebml) { e.bytes(matroska.CodecPrivate, make([]byte, 127)) }, "63a2407f" + strings.Repeat("00", 127)},
		{"master with an eight byte size", func(e *ebml) {
			e.begin(matroska.Video)
			e.uint(matroska.PixelWidth, 320)
			e.end()
		}, "e00100000000000004b0820140"},
		{"void fills its total", func(e *ebml) { e.void(4) }, "ec820000"},
		{"float64", func(e *ebml) { e.float(matroska.Duration, 2000) }, "448988409f400000000000"},
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
	id      matroska.ID
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
		out = append(out, element{id: matroska.ID(id), payload: b[start : start+int(size)], at: base + i})
		i = start + int(size)
	}
	return out
}

func find(t *testing.T, els []element, id matroska.ID) element {
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
	if len(top) != 2 || top[0].id != matroska.EBML || top[1].id != matroska.Segment {
		t.Fatalf("top level %+v", top)
	}
	segStart := len(b) - len(top[1].payload)
	seg := children(t, top[1].payload, segStart)
	clusters := 0
	for _, e := range seg {
		if e.id == matroska.Cluster {
			clusters++
		}
	}
	if clusters != 3 {
		t.Errorf("%d clusters for three seconds, want 3", clusters)
	}
	seeks := children(t, find(t, seg, matroska.SeekHead).payload, 0)
	if len(seeks) != 3 {
		t.Fatalf("%d seek entries, want 3", len(seeks))
	}
	for _, s := range seeks {
		entry := children(t, s.payload, 0)
		id := matroska.ID(binary.BigEndian.Uint32(find(t, entry, matroska.SeekID).payload))
		var pos uint64
		for _, c := range find(t, entry, matroska.SeekPosition).payload {
			pos = pos<<8 | uint64(c)
		}
		if target := find(t, seg, id); uint64(target.at-segStart) != pos {
			t.Errorf("seek entry for %x points at %d, the element is at %d", id, pos, target.at-segStart)
		}
	}
	info := children(t, find(t, seg, matroska.Info).payload, 0)
	if d := find(t, info, matroska.Duration).payload; hex.EncodeToString(d) != "40a7700000000000" {
		t.Errorf("duration % x, want 3000 ms", d)
	}
	cues := children(t, find(t, seg, matroska.Cues).payload, 0)
	if len(cues) != 3 {
		t.Errorf("%d cue points, want one per cluster", len(cues))
	}
	if pos, err := f.Seek(0, io.SeekCurrent); err != nil || pos != int64(len(b)) {
		t.Errorf("writer left at %d (%v), the file ends at %d", pos, err, len(b))
	}
}

func TestPayloadClassifiesNALUnits(t *testing.T) {
	sps264 := unhex(t, "67640015acd94141feab0110000003001000000780f162d960")
	pps264 := unhex(t, "68ebe3cb22c0")
	vps265 := unhex(t, "40010c01ffff01600000030090000003000003003f959809")
	sps265 := unhex(t, "42010101600000030090000003000003003fa00a080f1d565959a4932bc05a020000030002000003007810")
	pps265 := unhex(t, "4401c172b46240")
	avc, err := H264{SPS: sps264, PPS: pps264}.describe()
	if err != nil {
		t.Fatal(err)
	}
	hevc, err := H265{VPS: vps265, SPS: sps265, PPS: pps265}.describe()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name  string
		track track
		units [][]byte
		kept  [][]byte
		key   bool
	}{
		{"H.264 IDR with its parameter sets", avc, [][]byte{{0x09, 0x10}, sps264, pps264, {0x65, 1}}, [][]byte{{0x65, 1}}, true},
		{"H.264 non-IDR slice", avc, [][]byte{{0x09, 0x30}, {0x06, 5}, {0x41, 2}}, [][]byte{{0x06, 5}, {0x41, 2}}, false},
		{"H.265 IDR with its parameter sets", hevc, [][]byte{{0x46, 1, 0x10}, vps265, sps265, pps265, {0x26, 1, 3}}, [][]byte{{0x26, 1, 3}}, true},
		{"H.265 CRA", hevc, [][]byte{{0x2a, 1, 4}}, [][]byte{{0x2a, 1, 4}}, true},
		{"H.265 reserved IRAP", hevc, [][]byte{{0x2e, 1, 4}}, [][]byte{{0x2e, 1, 4}}, true},
		{"H.265 trailing picture", hevc, [][]byte{{0x46, 1, 0x30}, {0x4e, 1, 6}, {0x02, 1, 5}}, [][]byte{{0x4e, 1, 6}, {0x02, 1, 5}}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			var au, want []byte
			for _, u := range c.units {
				au = append(append(au, startCode...), u...)
			}
			for _, u := range c.kept {
				want = append(binary.BigEndian.AppendUint32(want, uint32(len(u))), u...)
			}
			got, key, err := c.track.payload(nil, au)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, want) || key != c.key {
				t.Errorf("payload % x key %v, want % x key %v", got, key, want, c.key)
			}
		})
	}
}
