package edid

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

var vrrEDIDs = []struct {
	file string
	want VRRRange
	err  error
}{
	{"freesync-amd-vsdb.bin", VRRRange{MinMilliHz: 48000, MaxMilliHz: 144000, Source: VRRAMDVendorBlock}, nil},
	{"amd-vsdb-over-range-limits.bin", VRRRange{MinMilliHz: 48000, MaxMilliHz: 240000, Source: VRRAMDVendorBlock}, nil},
	{"range-limits-offset.bin", VRRRange{MinMilliHz: 48000, MaxMilliHz: 280000, Source: VRRRangeLimits}, nil},
	{"displayid2-adaptive-sync.bin", VRRRange{MinMilliHz: 60000, MaxMilliHz: 240000, Source: VRRDisplayIDAdaptiveSync}, nil},
	{"oled-no-range.bin", VRRRange{}, ErrNoVRRRange},
}

func readEDID(tb testing.TB, name string) []byte {
	tb.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		tb.Fatal(err)
	}
	return b
}

func TestParseVRRReadsRealDisplays(t *testing.T) {
	for _, c := range vrrEDIDs {
		got, err := ParseVRR(readEDID(t, c.file))
		if !errors.Is(err, c.err) || got != c.want {
			t.Errorf("%s: %+v, %v, want %+v, %v", c.file, got, err, c.want, c.err)
		}
	}
}

func TestParseVRRAllocatesNothing(t *testing.T) {
	for _, c := range vrrEDIDs {
		b := readEDID(t, c.file)
		if n := testing.AllocsPerRun(100, func() { _, _ = ParseVRR(b) }); n != 0 {
			t.Errorf("%s: %v allocations, want 0", c.file, n)
		}
	}
}

func rangeLimits(t *testing.T, offsets, minHz, maxHz byte) []byte {
	t.Helper()
	b := append([]byte(nil), base(t)[:blockSize]...)
	b[126] = 0
	for i := range descriptorCount {
		d := b[descriptorStart+i*descriptorSize:][:descriptorSize]
		if d[0] == 0 && d[1] == 0 && d[3] == tagRangeLimits {
			d[4], d[5], d[6] = offsets, minHz, maxHz
		}
	}
	seal(b)
	return b
}

func TestParseVRRRangeLimits(t *testing.T) {
	for name, c := range map[string]struct {
		offsets, minHz, maxHz byte
		want                  VRRRange
		err                   error
	}{
		"both offsets":   {0x03, 5, 245, VRRRange{MinMilliHz: 260000, MaxMilliHz: 500000, Source: VRRRangeLimits}, nil},
		"fixed rate":     {0, 60, 60, VRRRange{}, ErrNoVRRRange},
		"minimum zero":   {0, 0, 60, VRRRange{}, ErrInvalid},
		"minimum higher": {0, 120, 60, VRRRange{}, ErrInvalid},
	} {
		got, err := ParseVRR(rangeLimits(t, c.offsets, c.minHz, c.maxHz))
		if !errors.Is(err, c.err) || got != c.want {
			t.Errorf("%s: %+v, %v, want %+v, %v", name, got, err, c.want, c.err)
		}
	}
}

func TestParseVRRRefusesWhatItCannotTrust(t *testing.T) {
	badDisplayID := readEDID(t, "displayid2-adaptive-sync.bin")
	badDisplayID[blockSize+20]++
	badDisplayID[2*blockSize-1]--
	badCTA := readEDID(t, "freesync-amd-vsdb.bin")
	badCTA[blockSize+10]++
	for name, b := range map[string][]byte{
		"a bad DisplayID section checksum": badDisplayID,
		"a bad CTA-861 checksum":           badCTA,
		"no header":                        {1, 2, 3},
	} {
		if _, err := ParseVRR(b); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v, want ErrInvalid", name, err)
		}
	}
}

func TestMaxFrameDuration(t *testing.T) {
	if got := (VRRRange{MinMilliHz: 48000}).MaxFrameDuration(); got != time.Second/48 {
		t.Fatalf("%v, want %v", got, time.Second/48)
	}
}

func FuzzParse(f *testing.F) {
	for _, c := range vrrEDIDs {
		f.Add(readEDID(f, c.file))
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		if r, err := ParseVRR(b); err == nil && (r.MinMilliHz == 0 || r.MinMilliHz >= r.MaxMilliHz || r.Source == 0) {
			t.Fatalf("ParseVRR returned %+v without an error", r)
		}
		_, _ = ParseHDR(b)
		_, _ = Parse(b, nil)
	})
}
