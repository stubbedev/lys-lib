package motion

import (
	"math"
	"testing"

	"github.com/stubbedev/lys-lib/third_party/gomath"
)

// The closed form is exact: one long step lands where many short ones
// do, in every damping regime.
func TestSpringStepComposes(t *testing.T) {
	for _, zeta := range []float64{0, 0.5, 1, 1.6} {
		x1, v1 := SpringStep(20, zeta, -80, 40, 0.12)
		x, v := -80.0, 40.0
		for range 12 {
			x, v = SpringStep(20, zeta, x, v, 0.01)
		}
		if math.Abs(x-x1) > 1e-9 || math.Abs(v-v1) > 1e-9 {
			t.Errorf("zeta %v: one step (%v, %v), twelve steps (%v, %v)", zeta, x1, v1, x, v)
		}
	}
}

func TestSpringStepWithoutTimeStaysPut(t *testing.T) {
	for _, dt := range []float64{0, -1} {
		if x, v := SpringStep(20, 0.5, 3, 4, dt); x != 3 || v != 4 {
			t.Errorf("dt %v moved to (%v, %v)", dt, x, v)
		}
	}
}

func TestSpringStepReadsNegativeDampingAsNone(t *testing.T) {
	x0, v0 := SpringStep(20, 0, 1, 0, 0.3)
	if x, v := SpringStep(20, -2, 1, 0, 0.3); x != x0 || v != v0 {
		t.Errorf("negative damping gave (%v, %v), undamped (%v, %v)", x, v, x0, v0)
	}
}

func TestOverdampedRootsSolveTheCharacteristicEquation(t *testing.T) {
	const w, z = 10.0, 3.0
	r1, r2 := OverdampedRoots(w, z)
	if !(r2 < r1 && r1 < 0) {
		t.Fatalf("roots %v and %v", r1, r2)
	}
	for _, r := range []float64{r1, r2} {
		if got := float64(r*r) + float64(2*z*w*r) + w*w; math.Abs(got) > 1e-9 {
			t.Errorf("root %v leaves %v", r, got)
		}
	}
}

// These bits are the arm64 and amd64 contract: a difference between
// architectures breaks the goldens downstream.
func TestSolversAreTheSameBitsEverywhere(t *testing.T) {
	for _, c := range []struct {
		zeta, wantX, wantV float64
	}{
		{0.5, 0x1.681cbbb6dc0b2p+01, 0x1.b6a3e42c09a45p+08},
		{1, -0x1.6456e9e3bdeffp+04, 0x1.4fbde9b01a8acp+08},
		{1.6, -0x1.2ad03452c3f73p+05, 0x1.12e688c685555p+08},
	} {
		x, v := SpringStep(21, c.zeta, -80, 40, 0.12)
		if x != c.wantX || v != c.wantV {
			t.Errorf("zeta %v: (%x, %x), want (%x, %x)", c.zeta, x, v, c.wantX, c.wantV)
		}
	}
	b := NewCubicBezier(0.68, -0.6, 0.32, 1.6)
	for _, c := range []struct{ x, want float64 }{
		{0.1, -0x1.2a48a3bea2dd9p-04},
		{0.35, -0x1.18ae0047f998dp-07},
		{0.5, 0x1p-01},
		{0.9, 0x1.12a48a3bea2dep+00},
	} {
		if got := b.Ease(c.x); got != c.want {
			t.Errorf("Ease(%v) = %x, want %x", c.x, got, c.want)
		}
	}
}

func TestBezierEndsExactlyAtItsEnds(t *testing.T) {
	b := NewCubicBezier(0.25, 0.1, 0.25, 1)
	for x, want := range map[float64]float64{-1: 0, 0: 0, 1: 1, 2: 1} {
		if got := b.Ease(x); got != want {
			t.Errorf("Ease(%v) = %v", x, got)
		}
	}
}

func TestBezierMatchesCSSKeywordValues(t *testing.T) {
	for _, c := range []struct {
		b       CubicBezier
		x, want float64
	}{
		{NewCubicBezier(0, 0, 1, 1), 0.3, 0.3},
		{NewCubicBezier(0.25, 0.1, 0.25, 1), 0.5, 0.8024033876},
		{NewCubicBezier(0.42, 0, 1, 1), 0.25, 0.0934646507},
		{NewCubicBezier(0, 0, 0.58, 1), 0.75, 0.9065353493},
		{NewCubicBezier(0.42, 0, 0.58, 1), 0.5, 0.5},
	} {
		if got := c.b.Ease(c.x); math.Abs(got-c.want) > 1e-6 {
			t.Errorf("%+v at %v = %v, want %v", c.b, c.x, got, c.want)
		}
	}
}

func TestBezierClampsTimeControlsLikeCSS(t *testing.T) {
	if a, b := NewCubicBezier(-0.5, 0, 1.5, 1), NewCubicBezier(0, 0, 1, 1); a != b {
		t.Errorf("clamped %+v, want %+v", a, b)
	}
}

func TestBezierSteepCurveInvertsItsTime(t *testing.T) {
	b := NewCubicBezier(1, 0, 0, 1)
	for i := 1; i < 1000; i++ {
		x := float64(i) / 1000
		u := b.param(x)
		if got := b.sampleX(u); math.Abs(got-x) > 2e-9 {
			t.Fatalf("param(%v) = %v, whose x is %v", x, u, got)
		}
	}
}

// SpringPosition and Sincos are shortcuts and must change no bit: the
// position matches SpringStep's, and the underdamped branch matches the
// Sin and Cos it was first written with.
func TestSpringPositionIsSpringStepsPosition(t *testing.T) {
	for _, zeta := range []float64{0, 0.3, 0.86, 1, 1.6, 30} {
		for _, w := range []float64{0.7, 14, 21, 200} {
			for _, dt := range []float64{1e-4, 0.016, 0.12, 0.4, 3, 300} {
				x, _ := SpringStep(w, zeta, -80, 40, dt)
				if got := SpringPosition(w, zeta, -80, 40, dt); got != x {
					t.Errorf("w %v zeta %v dt %v: %x, step %x", w, zeta, dt, got, x)
				}
			}
		}
	}
}

func TestSincosIsSinAndCos(t *testing.T) {
	for i := range 200000 {
		a := float64(i) * 0.000371
		s, c := gomath.Sincos(a)
		if s != gomath.Sin(a) || c != gomath.Cos(a) {
			t.Fatalf("Sincos(%v) = %x, %x; Sin %x, Cos %x", a, s, c, gomath.Sin(a), gomath.Cos(a))
		}
	}
}

func BenchmarkSpringPosition(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		SpringPosition(21, 0.7, -1, 0, 0.016)
	}
}

func BenchmarkSpringStep(b *testing.B) {
	for b.Loop() {
		SpringStep(21, 0.7, -80, 40, 0.016)
	}
}

func BenchmarkBezierEase(b *testing.B) {
	c := NewCubicBezier(0.25, 0.1, 0.25, 1)
	x := 0.0
	for b.Loop() {
		x += 0.01
		if x >= 1 {
			x = 0.01
		}
		c.Ease(x)
	}
}
