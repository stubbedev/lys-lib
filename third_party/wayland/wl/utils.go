package wl

import (
	"math"
)

// FixedToFloat converts a fixed precision Wayland decimal encoded as int32 to a float64
func FixedToFloat(fixed int32) float64 {
	dat := ((int64(1023 + 44)) << 52) + (1 << 51) + int64(fixed)
	return math.Float64frombits(uint64(dat)) - float64(3<<43)
}

// FloatToFixed converts a float64 to a fixed precision Wayland decimal encoded as int32
func FloatToFixed(v float64) int32 {
	dat := v + float64(int64(3)<<(51-8))
	return int32(math.Float64bits(dat))
}
