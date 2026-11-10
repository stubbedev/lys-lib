package aac

import (
	"sync"

	"github.com/stubbedev/lys-lib/third_party/gomath"
)

// tables are what a decoder computes rather than carries: the rising
// window halves by shape (0 sine, 1 KBD), the inverse MDCT plans, the
// codebooks of the scale factor differences and of the 11 spectral
// codebooks, |q|^(4/3) for every quantised magnitude a frame can carry
// and 2^((sf-100)/4) for every scale factor.
type tables struct {
	longWindows     [2][FrameSize]float32
	shortWindows    [2][shortN2]float32
	longPlan        *imdctPlan
	shortPlan       *imdctPlan
	sfHuffman       *huffman
	spectralHuffman [12]*huffman
	pow43           [maxQuant + pulseMaxAmp + 1]float32
	sfGain          [256]float32
}

var shared struct {
	once   sync.Once
	tables *tables
}

// sharedTables builds the tables on the first decoder's behalf, so a
// program that links the package and decodes no AAC pays nothing.
func sharedTables() *tables {
	shared.once.Do(func() { shared.tables = newTables() })
	return shared.tables
}

func newTables() *tables {
	t := &tables{
		longPlan:  newIMDCTPlan(longN),
		shortPlan: newIMDCTPlan(shortN),
		sfHuffman: newHuffman(sfCodes[:], sfBits[:], 9),
		spectralHuffman: [12]*huffman{
			1: newHuffman(spectralCodes1[:], spectralBits1[:], 8), 2: newHuffman(spectralCodes2[:], spectralBits2[:], 8),
			3: newHuffman(spectralCodes3[:], spectralBits3[:], 8), 4: newHuffman(spectralCodes4[:], spectralBits4[:], 8),
			5: newHuffman(spectralCodes5[:], spectralBits5[:], 8), 6: newHuffman(spectralCodes6[:], spectralBits6[:], 8),
			7: newHuffman(spectralCodes7[:], spectralBits7[:], 8), 8: newHuffman(spectralCodes8[:], spectralBits8[:], 8),
			9: newHuffman(spectralCodes9[:], spectralBits9[:], 8), 10: newHuffman(spectralCodes10[:], spectralBits10[:], 8),
			11: newHuffman(spectralCodes11[:], spectralBits11[:], 8),
		},
	}
	sineWindow(t.longWindows[0][:])
	kbdWindow(t.longWindows[1][:], 4)
	sineWindow(t.shortWindows[0][:])
	kbdWindow(t.shortWindows[1][:], 6)
	for i := range t.pow43 {
		f := float64(i)
		t.pow43[i] = float32(f * gomath.Cbrt(f))
	}
	for i := range t.sfGain {
		t.sfGain[i] = float32(gomath.Exp2(0.25 * float64(i-globalOffset)))
	}
	return t
}
