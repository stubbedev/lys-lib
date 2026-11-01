// Copyright 2009 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package gomath

import "math"

// Atan2 returns the arc tangent of y/x, using
// the signs of the two to determine the quadrant
// of the return value.
//
// Special cases are (in order):
//
//	Atan2(y, NaN) = NaN
//	Atan2(NaN, x) = NaN
//	Atan2(+0, x>=0) = +0
//	Atan2(-0, x>=0) = -0
//	Atan2(+0, x<=-0) = +math.Pi
//	Atan2(-0, x<=-0) = -math.Pi
//	Atan2(y>0, 0) = +math.Pi/2
//	Atan2(y<0, 0) = -math.Pi/2
//	Atan2(+Inf, +Inf) = +math.Pi/4
//	Atan2(-Inf, +Inf) = -math.Pi/4
//	Atan2(+Inf, -Inf) = 3Pi/4
//	Atan2(-Inf, -Inf) = -3Pi/4
//	Atan2(y, +Inf) = 0
//	Atan2(y>0, -Inf) = +math.Pi
//	Atan2(y<0, -Inf) = -math.Pi
//	Atan2(+Inf, x) = +math.Pi/2
//	Atan2(-Inf, x) = -math.Pi/2
func Atan2(y, x float64) float64 {
	return atan2(y, x)
}

func atan2(y, x float64) float64 {
	// special cases
	switch {
	case math.IsNaN(y) || math.IsNaN(x):
		return math.NaN()
	case y == 0:
		if x >= 0 && !math.Signbit(x) {
			return math.Copysign(0, y)
		}
		return math.Copysign(math.Pi, y)
	case x == 0:
		return math.Copysign(math.Pi/2, y)
	case math.IsInf(x, 0):
		if math.IsInf(x, 1) {
			switch {
			case math.IsInf(y, 0):
				return math.Copysign(math.Pi/4, y)
			default:
				return math.Copysign(0, y)
			}
		}
		switch {
		case math.IsInf(y, 0):
			return math.Copysign(3*math.Pi/4, y)
		default:
			return math.Copysign(math.Pi, y)
		}
	case math.IsInf(y, 0):
		return math.Copysign(math.Pi/2, y)
	}

	// Call atan and determine the quadrant.
	q := Atan(y / x)
	if x < 0 {
		if q <= 0 {
			return q + math.Pi
		}
		return q - math.Pi
	}
	return q
}
