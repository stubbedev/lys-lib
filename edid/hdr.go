package edid

import (
	"fmt"

	"github.com/stubbedev/lys-lib/third_party/gomath"
)

const (
	tagCTA        = 0x02
	ctaUseExtTag  = 7
	extColorimety = 0x05
	extHDRStatic  = 0x06
	chromaOffset  = 25
)

// Chromaticity is a CIE 1931 xy coordinate.
type Chromaticity struct {
	X, Y float64
}

// HDR is what an EDID says about a display's colour: the primaries of its
// base block, and the transfer functions, gamut and luminance of its CTA-861
// extension's Colorimetry and HDR Static Metadata data blocks. Luminances are
// in cd/m², and zero where the display leaves them out.
type HDR struct {
	Red, Green, Blue, White Chromaticity
	ST2084, HLG             bool
	BT2020                  bool
	StaticMetadata          bool
	MaxLuminance            float64
	MaxAverageLuminance     float64
	MinLuminance            float64
}

// ParseHDR reads b's colour characteristics. A display with no CTA-861
// extension, or none with the data blocks, has an HDR with only its primaries.
// It fails with ErrInvalid on a bad base block or a data block that overruns.
// An extension with a bad checksum fails too, not to promise a signal the
// display may not take.
func ParseHDR(b []byte) (HDR, error) {
	if err := checkBase(b); err != nil {
		return HDR{}, err
	}
	var h HDR
	h.Red, h.Green, h.Blue, h.White = chromaticities(b)
	for at := blockSize; at+blockSize <= len(b); at += blockSize {
		block := b[at : at+blockSize]
		if block[0] != tagCTA {
			continue
		}
		var sum byte
		for _, c := range block {
			sum += c
		}
		if sum != 0 {
			return HDR{}, fmt.Errorf("%w: the CTA-861 extension at %d has a checksum off by %d", ErrInvalid, at, sum)
		}
		if err := h.readCTA(block); err != nil {
			return HDR{}, err
		}
	}
	return h, nil
}

func chromaticities(b []byte) (red, green, blue, white Chromaticity) {
	low0, low1, hi := b[chromaOffset], b[chromaOffset+1], b[chromaOffset+2:chromaOffset+10]
	coord := func(high byte, low byte) float64 { return float64(int(high)<<2|int(low&3)) / 1024 }
	at := func(i int, xLow, yLow byte) Chromaticity {
		return Chromaticity{X: coord(hi[i], xLow), Y: coord(hi[i+1], yLow)}
	}
	return at(0, low0>>6, low0>>4), at(2, low0>>2, low0), at(4, low1>>6, low1>>4), at(6, low1>>2, low1)
}

func (h *HDR) readCTA(block []byte) error {
	end := int(block[2])
	if end == 0 {
		end = blockSize - 1
	}
	if end < 4 || end > blockSize-1 {
		return fmt.Errorf("%w: a CTA-861 extension whose data blocks end at %d", ErrInvalid, end)
	}
	for i := 4; i < end; {
		tag, n := block[i]>>5, int(block[i]&0x1f)
		if i+1+n > end {
			return fmt.Errorf("%w: a CTA-861 data block of %d bytes at %d overruns %d", ErrInvalid, n, i, end)
		}
		data := block[i+1 : i+1+n]
		i += 1 + n
		if tag != ctaUseExtTag || n < 1 {
			continue
		}
		switch data[0] {
		case extColorimety:
			if n >= 2 {
				h.BT2020 = data[1]&0x80 != 0
			}
		case extHDRStatic:
			h.readStatic(data[1:])
		}
	}
	return nil
}

func (h *HDR) readStatic(d []byte) {
	if len(d) < 2 {
		return
	}
	h.ST2084 = d[0]&(1<<2) != 0
	h.HLG = d[0]&(1<<3) != 0
	h.StaticMetadata = d[1]&1 != 0
	if len(d) > 2 && d[2] != 0 {
		h.MaxLuminance = luminance(d[2])
	}
	if len(d) > 3 && d[3] != 0 {
		h.MaxAverageLuminance = luminance(d[3])
	}
	if len(d) > 4 && h.MaxLuminance > 0 {
		k := float64(d[4]) / 255
		h.MinLuminance = h.MaxLuminance * k * k / 100
	}
}

func luminance(code byte) float64 {
	return 50 * gomath.Pow(2, float64(code)/32)
}
