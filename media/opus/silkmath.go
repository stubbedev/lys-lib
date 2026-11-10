package opus

import (
	"math"
	"math/bits"

	"github.com/stubbedev/lys-lib/intconv"
)

// The SILK decoder's fixed-point primitives (silk/macros.h,
// SigProc_FIX.h and Inlines.h). int32 arithmetic wraps in Go as the
// reference's overflow-tolerant macros assume.

// smulwb is (a * int16(b)) >> 16.
func smulwb(a, b int32) int32 { return wrap32((int64(a) * int64(wrap16(b))) >> 16) }

// smlawb is a + smulwb(b, c).
func smlawb(a, b, c int32) int32 { return a + smulwb(b, c) }

// smulbb is int16(a) * int16(b).
func smulbb(a, b int32) int32 { return int32(wrap16(a)) * int32(wrap16(b)) }

// smlabb is a + smulbb(b, c).
func smlabb(a, b, c int32) int32 { return a + smulbb(b, c) }

// smulww is (a * b) >> 16.
func smulww(a, b int32) int32 { return wrap32((int64(a) * int64(b)) >> 16) }

// smlaww is a + smulww(b, c).
func smlaww(a, b, c int32) int32 { return a + smulww(b, c) }

// smultt is (a >> 16) * (b >> 16).
func smultt(a, b int32) int32 { return (a >> 16) * (b >> 16) }

// smmul is (a * b) >> 32.
func smmul(a, b int32) int32 { return wrap32((int64(a) * int64(b)) >> 32) }

// rshiftRound is a >> shift rounded to nearest.
func rshiftRound(a int32, shift uint) int32 {
	if shift == 1 {
		return a>>1 + a&1
	}
	return (a>>(shift-1) + 1) >> 1
}

// rshiftRound64 is rshiftRound on 64 bits.
func rshiftRound64(a int64, shift int) int64 {
	if shift == 1 {
		return a>>1 + a&1
	}
	return (a>>(shift-1) + 1) >> 1
}

// sat16 is a saturated to int16's range.
func sat16(a int32) int16 {
	switch {
	case a > math.MaxInt16:
		return math.MaxInt16
	case a < math.MinInt16:
		return math.MinInt16
	}
	return int16(a)
}

// wrap16 is C's (opus_int16) cast: a's low 16 bits, signed.
func wrap16(a int32) int16 { return int16(a) } //nolint:gosec // the reference truncates here

// wrap32 is C's (opus_int32) cast of a 64-bit value: its low 32 bits,
// signed.
func wrap32(a int64) int32 { return int32(a) } //nolint:gosec // the reference truncates here

// addSat32 is a + b saturated to int32's range.
func addSat32(a, b int32) int32 {
	s := a + b
	switch {
	case a >= 0 && b >= 0 && s < 0:
		return math.MaxInt32
	case a < 0 && b < 0 && s >= 0:
		return math.MinInt32
	}
	return s
}

// subSat32 is a - b saturated to int32's range.
func subSat32(a, b int32) int32 {
	s := a - b
	switch {
	case a >= 0 && b < 0 && s < 0:
		return math.MaxInt32
	case a < 0 && b >= 0 && s >= 0:
		return math.MinInt32
	}
	return s
}

// limit32 is silk_LIMIT: a clamped between the bounds, in either order.
func limit32(a, l1, l2 int32) int32 {
	if l1 > l2 {
		return max(l2, min(l1, a))
	}
	return max(l1, min(l2, a))
}

// lshiftSat32 is a << shift saturated to int32's range.
func lshiftSat32(a int32, shift uint) int32 {
	return limit32(a, math.MinInt32>>shift, math.MaxInt32>>shift) << shift
}

// clz32 counts a's leading zero bits, 32 for zero.
func clz32(a int32) int { return bits.LeadingZeros32(intconv.Uint32Bits(a)) }

// abs32i is |a|, wrapping at the minimum as the reference does.
func abs32i(a int32) int32 {
	if a < 0 {
		return -a
	}
	return a
}

// silkRand is silk_RAND, the linear congruential noise generator.
func silkRand(seed int32) int32 { return 907633515 + seed*196314165 }

// clzFrac is silk_CLZ_FRAC: a's leading zeros and the 7 bits after its
// leading one.
func clzFrac(a int32) (lz int, frac int32) {
	lz = clz32(a)
	return lz, intconv.Int32Bits(bits.RotateLeft32(intconv.Uint32Bits(a), -(24-lz))) & 0x7f
}

// sqrtApprox is silk_SQRT_APPROX, within 10 % above 15.
func sqrtApprox(x int32) int32 {
	if x <= 0 {
		return 0
	}
	lz, frac := clzFrac(x)
	y := int32(46214)
	if lz&1 != 0 {
		y = 32768
	}
	y >>= lz >> 1
	return smlawb(y, y, smulbb(213, frac))
}

// div32VarQ is silk_DIV32_varQ: about (a << q) / b.
func div32VarQ(a, b int32, q int) int32 {
	aHead := clz32(abs32i(a)) - 1
	aNrm := a << aHead
	bHead := clz32(abs32i(b)) - 1
	bNrm := b << bHead
	bInv := (math.MaxInt32 >> 2) / (bNrm >> 16)
	result := smulwb(aNrm, bInv)
	aNrm -= smmul(bNrm, result) << 3
	result = smlawb(result, aNrm, bInv)
	lshift := 29 + aHead - bHead - q
	switch {
	case lshift < 0:
		return lshiftSat32(result, uint(-lshift))
	case lshift < 32:
		return result >> lshift
	}
	return 0
}

// inverse32VarQ is silk_INVERSE32_varQ: about (1 << q) / b.
func inverse32VarQ(b int32, q int) int32 {
	bHead := clz32(abs32i(b)) - 1
	bNrm := b << bHead
	bInv := (math.MaxInt32 >> 2) / (bNrm >> 16)
	result := bInv << 16
	errQ32 := (1<<29 - smulwb(bNrm, bInv)) << 3
	result = smlaww(result, errQ32, bInv)
	lshift := 61 - bHead - q
	switch {
	case lshift <= 0:
		return lshiftSat32(result, uint(-lshift))
	case lshift < 32:
		return result >> lshift
	}
	return 0
}

// log2lin is silk_log2lin: about 2^(x/128).
func log2lin(x int32) int32 {
	if x < 0 {
		return 0
	}
	if x >= 3967 {
		return math.MaxInt32
	}
	out := int32(1) << (x >> 7)
	frac := x & 0x7f
	p := smlawb(frac, smulbb(frac, 128-frac), -174)
	if x < 2048 {
		return out + (out*p)>>7
	}
	return out + (out>>7)*p
}

// sumSqrShift is silk_sum_sqr_shift: the energy of x right-shifted to
// fit an int32 with two bits of headroom, and the shift.
func sumSqrShift(x []int16) (energy int32, shift int) {
	n := intconv.Must[int32](len(x))
	shift = 31 - clz32(n)
	shift = max(0, shift+3-clz32(sqrSum(x, shift, n)))
	return sqrSum(x, shift, 0), shift
}

// sqrSum is init plus the sum of x's squares, pairwise right-shifted
// by shift.
func sqrSum(x []int16, shift int, init int32) int32 {
	nrg := intconv.Uint32Bits(init)
	i := 0
	for ; i < len(x)-1; i += 2 {
		t := intconv.Uint32Bits(smulbb(int32(x[i]), int32(x[i]))) + intconv.Uint32Bits(smulbb(int32(x[i+1]), int32(x[i+1])))
		nrg += t >> shift
	}
	if i < len(x) {
		nrg += intconv.Uint32Bits(smulbb(int32(x[i]), int32(x[i]))) >> shift
	}
	return intconv.Int32Bits(nrg)
}
