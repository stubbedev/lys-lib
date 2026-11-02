// Package motion holds the two solvers animation is built on, a damped
// harmonic spring and a CSS cubic-bezier timing function, as pure maths
// over floats. lys-ui's anim and lys-wm's anim keep their own
// configuration on top: a response and damping fraction there, stiffness,
// damping and mass here. Results are the same bits on every architecture:
// products never fuse into an FMA and the transcendental functions come
// from gomath.
package motion

import (
	"math"

	"github.com/stubbedev/lys-lib/third_party/gomath"
)

// SpringStep is the damped harmonic oscillator's exact solution at
// angular frequency omega and damping ratio zeta: the displacement from
// the target and the velocity dt seconds after displacement x0 at
// velocity v0. A zeta of exactly 1 is the critically damped case, a
// negative one reads as 0, and a dt that is not positive returns the
// start unchanged.
func SpringStep(omega, zeta, x0, v0, dt float64) (x, v float64) {
	return springSolve(omega, zeta, x0, v0, dt, true)
}

// SpringPosition is SpringStep's displacement alone, the same bits, for a
// caller that has no use for the velocity.
func SpringPosition(omega, zeta, x0, v0, dt float64) float64 {
	x, _ := springSolve(omega, zeta, x0, v0, dt, false)
	return x
}

func springSolve(omega, zeta, x0, v0, dt float64, wantVelocity bool) (x, v float64) {
	if dt <= 0 {
		return x0, v0
	}
	w, z := omega, math.Max(zeta, 0)
	switch {
	case z < 1:
		wd := w * math.Sqrt(1-float64(z*z))
		e := gomath.Exp(-z * w * dt)
		s, c := gomath.Sincos(wd * dt)
		b := (v0 + float64(z*w*x0)) / wd
		x = e * (float64(x0*c) + float64(b*s))
		if wantVelocity {
			v = e * (float64((float64(b*wd)-float64(z*w*x0))*c) - float64((float64(x0*wd)+float64(z*w*b))*s))
		}
	case z == 1:
		e := gomath.Exp(-w * dt)
		b := v0 + float64(w*x0)
		x = (x0 + float64(b*dt)) * e
		if wantVelocity {
			v = (b - float64(w*(x0+float64(b*dt)))) * e
		}
	default:
		r1, r2 := OverdampedRoots(w, z)
		c1 := (v0 - float64(r2*x0)) / (r1 - r2)
		c2 := x0 - c1
		e1, e2 := gomath.Exp(r1*dt), gomath.Exp(r2*dt)
		x = float64(c1*e1) + float64(c2*e2)
		if wantVelocity {
			v = float64(r1*c1*e1) + float64(r2*c2*e2)
		}
	}
	return x, v
}

// OverdampedRoots are the two decay rates of an overdamped spring
// (zeta above 1), both negative, r1 the slower one that governs how long
// it takes to settle.
func OverdampedRoots(omega, zeta float64) (r1, r2 float64) {
	r := float64(omega * math.Sqrt(float64(zeta*zeta)-1))
	return float64(-zeta*omega) + r, float64(-zeta*omega) - r
}

const (
	bezierSolveEpsilon = 1e-9
	bezierFlatSlope    = 1e-6
	bezierNewtonSteps  = 8
	bezierBisectSteps  = 64
)

// CubicBezier is a CSS cubic-bezier timing function, its control points
// reduced to polynomial coefficients. The zero value is not a curve; use
// NewCubicBezier. It is comparable.
type CubicBezier struct {
	ax, bx, cx float64
	ay, by, cy float64
	// The slope's coefficients, kept so a Newton step need not form them.
	sa, sb float64
}

// NewCubicBezier returns cubic-bezier(x1, y1, x2, y2). x1 and x2 clamp
// to [0, 1], the range CSS admits, so the curve stays a function of
// time; the y controls are free, and overshoot when they leave [0, 1].
func NewCubicBezier(x1, y1, x2, y2 float64) CubicBezier {
	x1, x2 = clampUnit(x1), clampUnit(x2)
	cx, cy := float64(3*x1), float64(3*y1)
	bx, by := float64(3*(x2-x1))-cx, float64(3*(y2-y1))-cy
	ax := 1 - cx - bx
	return CubicBezier{ax: ax, bx: bx, cx: cx, ay: 1 - cy - by, by: by, cy: cy, sa: float64(3 * ax), sb: float64(2 * bx)}
}

func clampUnit(v float64) float64 {
	switch {
	case v < 0:
		return 0
	case v > 1:
		return 1
	}
	return v
}

// Ease is the progress at time x: exactly 0 at or before 0, exactly 1 at
// or after 1, and in between the curve's y where its x is x, the curve's
// parameter solved by Newton where it converges and bisection where it
// does not.
func (b CubicBezier) Ease(x float64) float64 {
	switch {
	case x <= 0:
		return 0
	case x >= 1:
		return 1
	}
	u := b.param(x)
	return float64((float64((float64(b.ay*u)+b.by)*u) + b.cy) * u)
}

func (b CubicBezier) sampleX(u float64) float64 {
	return float64((float64((float64(b.ax*u)+b.bx)*u) + b.cx) * u)
}

func (b CubicBezier) param(x float64) float64 {
	u := x
	for range bezierNewtonSteps {
		d := b.sampleX(u) - x
		if math.Abs(d) < bezierSolveEpsilon {
			return u
		}
		slope := float64((float64(b.sa*u)+b.sb)*u) + b.cx
		if math.Abs(slope) < bezierFlatSlope {
			break
		}
		u -= d / slope
		if u < 0 || u > 1 {
			break
		}
	}
	lo, hi := 0.0, 1.0
	u = x
	for range bezierBisectSteps {
		v := b.sampleX(u)
		if math.Abs(v-x) < bezierSolveEpsilon {
			return u
		}
		if v < x {
			lo = u
		} else {
			hi = u
		}
		u = (lo + hi) / 2
	}
	return u
}
