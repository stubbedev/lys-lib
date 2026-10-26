package intconv

import (
	"errors"
	"math"
	"testing"
)

func TestToKeepsValuesTheTargetHolds(t *testing.T) {
	if v, err := To[uint32](math.MaxUint32); err != nil || v != math.MaxUint32 {
		t.Fatalf("To[uint32](MaxUint32) = %d, %v", v, err)
	}
	if v, err := To[int32](math.MinInt32); err != nil || v != math.MinInt32 {
		t.Fatalf("To[int32](MinInt32) = %d, %v", v, err)
	}
	if v, err := To[int](uint8(255)); err != nil || v != 255 {
		t.Fatalf("To[int](uint8 255) = %d, %v", v, err)
	}
	if v, err := To[uint64](int64(math.MaxInt64)); err != nil || v != math.MaxInt64 {
		t.Fatalf("To[uint64](MaxInt64) = %d, %v", v, err)
	}
}

func TestToRefusesValuesTheTargetCannotHold(t *testing.T) {
	for _, c := range []struct {
		name string
		err  error
	}{
		{"int above uint32", second(To[uint32](math.MaxUint32 + 1))},
		{"negative to uint32", second(To[uint32](-1))},
		{"int above int32", second(To[int32](math.MaxInt32 + 1))},
		{"int below int32", second(To[int32](math.MinInt32 - 1))},
		{"uint32 above int32", second(To[int32](uint32(math.MaxUint32)))},
		{"uint64 above int64", second(To[int64](uint64(math.MaxUint64)))},
		{"negative to uint8", second(To[uint8](int8(-1)))},
		{"int16 into int8", second(To[int8](int16(128)))},
	} {
		if !errors.Is(c.err, ErrRange) {
			t.Errorf("%s: err = %v, want ErrRange", c.name, c.err)
		}
	}
}

func second[T any](_ T, err error) error { return err }
