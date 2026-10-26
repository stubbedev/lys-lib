// Package intconv converts between integer types, failing where a plain
// conversion would wrap or truncate.
package intconv

import (
	"errors"
	"fmt"
	"math"
)

// ErrRange is the error To wraps when the target type cannot hold the
// value.
var ErrRange = errors.New("integer out of range")

// Integer is every Go integer type.
type Integer interface {
	~int | ~int8 | ~int16 | ~int32 | ~int64 | ~uint | ~uint8 | ~uint16 | ~uint32 | ~uint64 | ~uintptr
}

// To converts v to T, or fails with ErrRange when T cannot hold v.
func To[T, F Integer](v F) (T, error) {
	t := T(v)
	if F(t) != v || (t < 0) != (v < 0) {
		return 0, fmt.Errorf("%d does not fit %T: %w", v, t, ErrRange)
	}
	return t, nil
}

// Must is v as T, for a value the caller has already bounded: a v T
// cannot hold is a bug in the caller, and panics.
func Must[T, F Integer](v F) T {
	t, err := To[T](v)
	if err != nil {
		panic(err)
	}
	return t
}

// Bits32 is v's two's-complement bits, the word a reader taking it as
// an int32 finds v in.
func Bits32(v int32) uint32 { return uint32(int64(v) & math.MaxUint32) }

// Bits64 is v's two's-complement bits, the word a reader taking it as
// an int64 finds v in.
func Bits64(v int64) uint64 {
	return uint64(v&math.MaxUint32) | uint64(v>>32&math.MaxUint32)<<32
}
