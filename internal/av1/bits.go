package av1

import (
	"math/bits"

	"github.com/stubbedev/lys-lib/intconv"
	"github.com/stubbedev/lys-lib/internal/nal"
)

// reader reads the descriptors of the syntax (4.10) over nal's bit
// reader, which reads AV1's f(n) as it reads u(n).
type reader struct{ nal.Reader }

func newReader(b []byte) reader { return reader{nal.NewReader(b)} }

// f8 reads f(n) as an n-bit value, n up to 8.
func (r *reader) f8(n int) uint8 { return r.U8(n) }

// su reads su(n): an n-bit two's complement value.
func (r *reader) su(n int) int32 {
	v := intconv.Must[int32](r.U(n))
	if sign := int32(1) << (n - 1); v&sign != 0 {
		v -= 2 * sign
	}
	return v
}

// su8 reads su(n) for n up to 8.
func (r *reader) su8(n int) int8 { return intconv.Must[int8](r.su(n)) }

// ns reads ns(n): a value below n in as few bits as it can take.
func (r *reader) ns(n uint32) uint32 {
	w := bits.Len32(n)
	m := uint32(1)<<w - n
	v := r.U(w - 1)
	if v < m {
		return v
	}
	return v<<1 - m + r.U(1)
}

// le reads le(n): an n-byte little-endian value, byte aligned.
func (r *reader) le(n int) uint32 {
	var v uint32
	for i := range n {
		v |= r.U(8) << (8 * i)
	}
	return v
}

// uvlc reads uvlc(): a variable length code of up to 32 bits.
func (r *reader) uvlc() uint32 {
	zeros := 0
	for !r.Flag() {
		if zeros++; zeros >= 32 || r.Short() {
			return 1<<32 - 1
		}
	}
	return r.U(zeros) + (1<<zeros - 1)
}

// align passes over the zero bits up to the next byte boundary,
// byte_alignment().
func (r *reader) align() { r.Skip(-r.Pos() & 7) }

// bytePos is how many whole bytes have been read.
func (r *reader) bytePos() int { return (r.Pos() + 7) / 8 }
