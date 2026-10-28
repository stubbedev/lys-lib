// Package intconv converts between integer types without silent
// wraparound: Exact refuses a value the target cannot hold, Must
// panics on one, Clamp saturates it, and Duration scales a count into
// a time.Duration. Uint32Bits, Uint64Bits and Int32Bits reinterpret
// two's-complement bits, where a wire format means exactly that.
package intconv

import (
	"errors"
	"fmt"
	"math"
	"reflect"
	"time"
)

// Integer is every Go integer type.
type Integer interface {
	~int | ~int8 | ~int16 | ~int32 | ~int64 |
		~uint | ~uint8 | ~uint16 | ~uint32 | ~uint64 | ~uintptr
}

// ErrRange is what every *RangeError unwraps to, for errors.Is.
var ErrRange = errors.New("integer out of range")

// RangeError is a value its target type cannot hold.
type RangeError struct {
	Value  string
	Target reflect.Type
}

func (e *RangeError) Error() string {
	return fmt.Sprintf("intconv: %s does not fit in %s", e.Value, e.Target)
}

// Unwrap is ErrRange.
func (e *RangeError) Unwrap() error { return ErrRange }

func fits[T, F Integer](v F) (T, bool) {
	t := T(v)
	return t, F(t) == v && (t < 0) == (v < 0)
}

// Exact returns v as a T, or a *RangeError when T cannot hold it.
func Exact[T, F Integer](v F) (T, error) {
	t, ok := fits[T](v)
	if !ok {
		return 0, &RangeError{Value: fmt.Sprint(v), Target: reflect.TypeFor[T]()}
	}
	return t, nil
}

// Must is v as T, for a value the caller has already bounded: a v T
// cannot hold is a bug in the caller, and panics with its *RangeError.
func Must[T, F Integer](v F) T {
	t, err := Exact[T](v)
	if err != nil {
		panic(err)
	}
	return t
}

// Clamp returns v as a T, saturated to T's range.
func Clamp[T, F Integer](v F) T {
	if t, ok := fits[T](v); ok {
		return t
	}
	if v < 0 {
		return minOf[T]()
	}
	return maxOf[T]()
}

func signed[T Integer]() bool {
	var zero T
	return zero-1 < 0
}

func minOf[T Integer]() T {
	if !signed[T]() {
		return 0
	}
	return T(1) << (reflect.TypeFor[T]().Bits() - 1)
}

func maxOf[T Integer]() T {
	if !signed[T]() {
		return ^T(0)
	}
	return T(1)<<(reflect.TypeFor[T]().Bits()-1) - 1
}

// Duration is n units, saturated to time.Duration's range. It panics
// on a unit that is not positive.
func Duration[F Integer](n F, unit time.Duration) time.Duration {
	if unit <= 0 {
		panic(fmt.Sprintf("intconv: Duration unit %v is not positive", unit))
	}
	c := Clamp[int64](n)
	switch {
	case c > math.MaxInt64/int64(unit):
		return math.MaxInt64
	case c < math.MinInt64/int64(unit):
		return math.MinInt64
	}
	return time.Duration(c) * unit
}

// Uint32Bits is v's two's-complement bits, the word a reader taking it
// as an int32 finds v in.
func Uint32Bits(v int32) uint32 { return uint32(int64(v) & math.MaxUint32) }

// Uint64Bits is v's two's-complement bits, the word a reader taking it
// as an int64 finds v in.
func Uint64Bits(v int64) uint64 {
	return uint64(v&math.MaxUint32) | uint64(v>>32&math.MaxUint32)<<32
}

// Int32Bits is the int32 with v's two's-complement bits, the value a
// wire format's signed 32-bit field carries in v: Uint32Bits's inverse.
func Int32Bits(v uint32) int32 {
	x := int32(v & math.MaxInt32)
	if v > math.MaxInt32 {
		x += math.MinInt32
	}
	return x
}
