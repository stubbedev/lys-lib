package intconv

import (
	"errors"
	"math"
	"testing"
	"time"
)

func TestExactKeepsValuesTheTargetHolds(t *testing.T) {
	if got, err := Exact[int32](int64(math.MinInt32)); err != nil || got != math.MinInt32 {
		t.Errorf("MinInt32 -> %d %v", got, err)
	}
	if got, err := Exact[uint32](math.MaxUint32); err != nil || got != math.MaxUint32 {
		t.Errorf("MaxUint32 -> %d %v", got, err)
	}
	if got, err := Exact[uint8](255); err != nil || got != 255 {
		t.Errorf("255 -> %d %v", got, err)
	}
	if got, err := Exact[int](uint8(255)); err != nil || got != 255 {
		t.Errorf("uint8 255 -> %d %v", got, err)
	}
	if got, err := Exact[uint64](int64(math.MaxInt64)); err != nil || got != math.MaxInt64 {
		t.Errorf("MaxInt64 -> %d %v", got, err)
	}
	if got, err := Exact[int64](uint32(math.MaxUint32)); err != nil || got != math.MaxUint32 {
		t.Errorf("uint32 MaxUint32 -> %d %v", got, err)
	}
}

func TestExactRefusesValuesTheTargetCannotHold(t *testing.T) {
	for _, c := range []struct {
		name string
		err  error
	}{
		{"int above uint32", second(Exact[uint32](math.MaxUint32 + 1))},
		{"negative to uint32", second(Exact[uint32](-1))},
		{"int64 above int32", second(Exact[int32](int64(math.MaxInt32) + 1))},
		{"int64 below int32", second(Exact[int32](int64(math.MinInt32) - 1))},
		{"uint32 above int32", second(Exact[int32](uint32(math.MaxInt32) + 1))},
		{"uintptr above int32", second(Exact[int32](uintptr(math.MaxUint32)))},
		{"uint64 above int64", second(Exact[int64](uint64(math.MaxInt64) + 1))},
		{"256 to uint8", second(Exact[uint8](256))},
		{"negative int8 to uint8", second(Exact[uint8](int8(-1)))},
		{"int16 into int8", second(Exact[int8](int16(128)))},
	} {
		if _, ok := errors.AsType[*RangeError](c.err); !ok {
			t.Errorf("%s: err = %v, want a *RangeError", c.name, c.err)
		}
		if !errors.Is(c.err, ErrRange) {
			t.Errorf("%s: err = %v, want one that is ErrRange", c.name, c.err)
		}
	}
}

func TestRangeErrorNamesValueAndTarget(t *testing.T) {
	_, err := Exact[uint8](-3)
	re, ok := errors.AsType[*RangeError](err)
	if !ok {
		t.Fatalf("err = %v, want a *RangeError", err)
	}
	if re.Value != "-3" || re.Target.String() != "uint8" {
		t.Errorf("RangeError = {%q, %v}, want {\"-3\", uint8}", re.Value, re.Target)
	}
	if err.Error() != "intconv: -3 does not fit in uint8" {
		t.Errorf("message: %v", err)
	}
}

func TestMustPanicsWithTheRangeError(t *testing.T) {
	if got := Must[uint16](int64(65535)); got != 65535 {
		t.Errorf("Must[uint16](65535) = %d", got)
	}
	defer func() {
		err, ok := recover().(error)
		if !ok || !errors.Is(err, ErrRange) {
			t.Errorf("recovered %v, want an ErrRange error", err)
		}
	}()
	Must[uint8](-1)
}

func TestClampSaturates(t *testing.T) {
	if got := Clamp[int32](int64(math.MaxInt64)); got != math.MaxInt32 {
		t.Errorf("high int32: %d", got)
	}
	if got := Clamp[int32](int64(math.MinInt64)); got != math.MinInt32 {
		t.Errorf("low int32: %d", got)
	}
	if got := Clamp[int32](uint32(math.MaxUint32)); got != math.MaxInt32 {
		t.Errorf("uint32 into int32: %d", got)
	}
	if got := Clamp[int32](uint64(math.MaxUint64)); got != math.MaxInt32 {
		t.Errorf("uint64 into int32: %d", got)
	}
	if got := Clamp[uint32](-5); got != 0 {
		t.Errorf("negative uint32: %d", got)
	}
	if got := Clamp[uint32](int64(math.MaxUint32) + 1); got != math.MaxUint32 {
		t.Errorf("high uint32: %d", got)
	}
	if got := Clamp[uint8](uint64(1) << 40); got != math.MaxUint8 {
		t.Errorf("high uint8: %d", got)
	}
	if got := Clamp[int64](uint64(math.MaxUint64)); got != math.MaxInt64 {
		t.Errorf("high int64: %d", got)
	}
	if got := Clamp[uint64](int64(math.MinInt64)); got != 0 {
		t.Errorf("low uint64: %d", got)
	}
}

func TestClampKeepsValuesInRange(t *testing.T) {
	if got := Clamp[int32](-7); got != -7 {
		t.Errorf("-7: %d", got)
	}
	if got := Clamp[int32](int64(math.MinInt32)); got != math.MinInt32 {
		t.Errorf("MinInt32: %d", got)
	}
	if got := Clamp[uint16](uint64(65535)); got != 65535 {
		t.Errorf("65535: %d", got)
	}
}

func TestDurationScales(t *testing.T) {
	if got := Duration(uint64(1500), time.Millisecond); got != 1500*time.Millisecond {
		t.Errorf("1500ms: %v", got)
	}
	if got := Duration(int64(-2), time.Second); got != -2*time.Second {
		t.Errorf("-2s: %v", got)
	}
}

func TestDurationSaturatesInsteadOfWrapping(t *testing.T) {
	if got := Duration(uint64(math.MaxUint64), time.Millisecond); got != math.MaxInt64 {
		t.Errorf("huge ms: %v", got)
	}
	if got := Duration(uint64(math.MaxInt64/int64(time.Minute))+1, time.Minute); got != math.MaxInt64 {
		t.Errorf("just past the minute limit: %v", got)
	}
	if got := Duration(int64(math.MinInt64), time.Microsecond); got != math.MinInt64 {
		t.Errorf("huge negative: %v", got)
	}
}

func TestDurationRefusesANonPositiveUnit(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("a zero unit did not panic")
		}
	}()
	Duration(1, 0)
}

func TestBitsRoundTrip(t *testing.T) {
	for _, c := range []struct {
		signed int32
		bits   uint32
	}{
		{0, 0},
		{1, 1},
		{math.MaxInt32, math.MaxInt32},
		{math.MinInt32, math.MaxInt32 + 1},
		{-1, math.MaxUint32},
		{-2, 0xfffffffe},
	} {
		if got := Uint32Bits(c.signed); got != c.bits {
			t.Errorf("Uint32Bits(%d) = %#x, want %#x", c.signed, got, c.bits)
		}
		if got := Int32Bits(c.bits); got != c.signed {
			t.Errorf("Int32Bits(%#x) = %d, want %d", c.bits, got, c.signed)
		}
	}
	for _, c := range []struct {
		signed int64
		bits   uint64
	}{
		{0, 0},
		{math.MaxInt64, math.MaxInt64},
		{math.MinInt64, math.MaxInt64 + 1},
		{-1, math.MaxUint64},
		{-1 << 32, 0xffffffff00000000},
	} {
		if got := Uint64Bits(c.signed); got != c.bits {
			t.Errorf("Uint64Bits(%d) = %#x, want %#x", c.signed, got, c.bits)
		}
	}
}

func second[T any](_ T, err error) error { return err }
