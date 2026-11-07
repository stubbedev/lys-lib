package vorbis

import (
	"math"
	"math/bits"

	"github.com/stubbedev/lys-lib/intconv"
)

type bitReader struct {
	data []byte
	pos  int
	acc  uint64
	n    uint
	eop  bool
}

func (r *bitReader) reset(data []byte) {
	*r = bitReader{data: data}
}

func (r *bitReader) fill() {
	for r.n <= 56 && r.pos < len(r.data) {
		r.acc |= uint64(r.data[r.pos]) << r.n
		r.pos++
		r.n += 8
	}
}

func (r *bitReader) read(n uint) uint32 {
	if n == 0 {
		return 0
	}
	if r.n < n {
		r.fill()
		if r.n < n {
			r.eop = true
			r.acc, r.n = 0, 0
			return 0
		}
	}
	v := uint32(r.acc & (1<<n - 1) & math.MaxUint32)
	r.acc >>= n
	r.n -= n
	return v
}

func (r *bitReader) flag() bool { return r.read(1) == 1 }

func ilog(x int) uint { return intconv.Must[uint](bits.Len(intconv.Must[uint](x))) }

func float32Unpack(x uint32) float64 {
	mantissa := float64(x & 0x1fffff)
	exponent := int((x & 0x7fe00000) >> 21)
	if x&0x80000000 != 0 {
		mantissa = -mantissa
	}
	return math.Ldexp(mantissa, exponent-788)
}

func (r *bitReader) remaining() int { return (len(r.data)-r.pos)*8 + int(r.n) }
