package demux

import (
	"encoding/binary"
	"math"
	"math/bits"
	"time"

	"github.com/stubbedev/lys-lib/intconv"
)

// fields reads big-endian fields out of a box or element payload; a
// read past its end sets short and yields zero.
type fields struct {
	b     []byte
	short bool
}

func (f *fields) take(n int) []byte {
	if n < 0 || n > len(f.b) {
		f.short = true
		f.b = nil
		return nil
	}
	v := f.b[:n]
	f.b = f.b[n:]
	return v
}

func (f *fields) u8() uint8 {
	if b := f.take(1); b != nil {
		return b[0]
	}
	return 0
}

func (f *fields) u16() uint16 {
	if b := f.take(2); b != nil {
		return binary.BigEndian.Uint16(b)
	}
	return 0
}

func (f *fields) u32() uint32 {
	if b := f.take(4); b != nil {
		return binary.BigEndian.Uint32(b)
	}
	return 0
}

func (f *fields) u64() uint64 {
	if b := f.take(8); b != nil {
		return binary.BigEndian.Uint64(b)
	}
	return 0
}

func (f *fields) skip(n int) { f.take(n) }

// ticksToTime is ticks of a clock at scale Hz as a duration, saturated
// to its range.
func ticksToTime(ticks int64, scale uint64) time.Duration {
	neg := ticks < 0
	mag := intconv.Uint64Bits(ticks)
	if neg {
		mag = -mag
	}
	hi, lo := bits.Mul64(mag, uint64(time.Second))
	if hi >= scale {
		if neg {
			return math.MinInt64
		}
		return math.MaxInt64
	}
	q, _ := bits.Div64(hi, lo, scale)
	d := time.Duration(intconv.Clamp[int64](q))
	if neg {
		return -d
	}
	return d
}
