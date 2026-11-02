package edid

import (
	"errors"
	"math"
	"testing"
)

func cta(blocks ...[]byte) []byte {
	b := make([]byte, blockSize)
	b[0], b[1] = tagCTA, 3
	at := 4
	for _, d := range blocks {
		at += copy(b[at:], d)
	}
	b[2] = byte(at)
	seal(b)
	return b
}

func seal(b []byte) {
	var sum byte
	for _, c := range b[:blockSize-1] {
		sum += c
	}
	b[blockSize-1] = -sum
}

func extended(tag byte, data ...byte) []byte {
	return append([]byte{ctaUseExtTag<<5 | byte(len(data)+1), tag}, data...)
}

func withExtension(t *testing.T, ext []byte) []byte {
	t.Helper()
	b := append([]byte(nil), base(t)...)
	b = b[:blockSize]
	b[126] = 1
	seal(b)
	return append(b, ext...)
}

func close2(a, b float64) bool { return math.Abs(a-b) < 0.01 }

func TestParseHDRReadsTheDataBlocks(t *testing.T) {
	audio := []byte{1<<5 | 3, 9, 9, 9}
	b := withExtension(t, cta(audio, extended(extColorimety, 0x80, 0), extended(extHDRStatic, 0b1101, 1, 124, 96, 40)))
	h, err := ParseHDR(b)
	if err != nil {
		t.Fatal(err)
	}
	if !h.ST2084 || !h.HLG || !h.BT2020 || !h.StaticMetadata {
		t.Fatalf("%+v, want PQ, HLG, BT.2020 and static metadata", h)
	}
	if want := 50 * math.Pow(2, 124.0/32); !close2(h.MaxLuminance, want) || want < 700 {
		t.Fatalf("max %v, want %v", h.MaxLuminance, want)
	}
	if want := 50 * math.Pow(2, 96.0/32); !close2(h.MaxAverageLuminance, want) {
		t.Fatalf("average %v, want %v", h.MaxAverageLuminance, want)
	}
	if want := h.MaxLuminance * (40.0 / 255) * (40.0 / 255) / 100; !close2(h.MinLuminance, want) {
		t.Fatalf("min %v, want %v", h.MinLuminance, want)
	}
}

func TestParseHDRReadsTheBaseBlockPrimaries(t *testing.T) {
	h, err := ParseHDR(base(t))
	if err != nil {
		t.Fatal(err)
	}
	if h.ST2084 || h.HLG || h.BT2020 {
		t.Fatalf("a monitor without the blocks reads %+v", h)
	}
	for name, c := range map[string]struct{ got, want Chromaticity }{
		"red":   {h.Red, Chromaticity{0.682, 0.320}},
		"green": {h.Green, Chromaticity{0.266, 0.690}},
		"blue":  {h.Blue, Chromaticity{0.149, 0.060}},
		"white": {h.White, Chromaticity{0.313, 0.329}},
	} {
		if !close2(c.got.X, c.want.X) || !close2(c.got.Y, c.want.Y) {
			t.Errorf("%s %+v, want about %+v", name, c.got, c.want)
		}
	}
}

func TestParseHDRWithoutOptionalLuminance(t *testing.T) {
	h, err := ParseHDR(withExtension(t, cta(extended(extHDRStatic, 0b101, 1))))
	if err != nil {
		t.Fatal(err)
	}
	if !h.ST2084 || h.HLG || h.MaxLuminance != 0 || h.MinLuminance != 0 {
		t.Fatalf("%+v", h)
	}
}

func TestParseHDRRefusesWhatItCannotTrust(t *testing.T) {
	bad := cta(extended(extHDRStatic, 0b101, 1))
	bad[10]++
	overrun := make([]byte, blockSize)
	overrun[0], overrun[1], overrun[2] = tagCTA, 3, 6
	overrun[4] = ctaUseExtTag<<5 | 30
	seal(overrun)
	short := []byte{1, 2, 3}
	for name, b := range map[string][]byte{
		"a bad extension checksum":   withExtension(t, bad),
		"a data block that overruns": withExtension(t, overrun),
		"no header":                  short,
	} {
		if _, err := ParseHDR(b); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v, want ErrInvalid", name, err)
		}
	}
}
