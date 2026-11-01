package vector

import (
	"crypto/sha256"
	"fmt"
	"image"
	"testing"
)

func curvePath(z *Rasterizer) {
	z.MoveTo(3.25, 7.5)
	z.CubeTo(61.75, 2.125, 12.5, 93.375, 88.625, 51.25)
	z.QuadTo(140.875, 3.5, 97.75, 41.125)
	z.LineTo(29.5, 88.75)
	z.ClosePath()
}

func coverage(w, h int) string {
	z := NewRasterizer(w, h)
	curvePath(z)
	dst := image.NewAlpha(image.Rect(0, 0, w, h))
	z.Draw(dst, dst.Bounds(), image.Opaque, image.Point{})
	return fmt.Sprintf("%x", sha256.Sum256(dst.Pix))[:16]
}

// Both math paths flatten the same curves to the same coverage on every
// architecture: fixed point up to 512 in both dimensions, floating past it.
func TestCoverageIsTheSameEverywhere(t *testing.T) {
	for _, tc := range []struct {
		w, h int
		want string
	}{
		{100, 100, "fd068a372bb2bc50"},
		{513, 100, "d0c234ecdae4cf1c"},
	} {
		if got := coverage(tc.w, tc.h); got != tc.want {
			t.Errorf("%dx%d: coverage hashes to %s, want %s", tc.w, tc.h, got, tc.want)
		}
	}
}

// The amd64 assembly and the portable Go give the same bytes, which is
// what lets one golden hold on both.
func TestAccumulatorsAgree(t *testing.T) {
	if !haveAccumulateSIMD {
		t.Skip("no assembly accumulators on this architecture")
	}
	z := NewRasterizer(513, 100)
	curvePath(z)
	floats := z.bufF32
	simd, plain := make([]uint8, len(floats)), make([]uint8, len(floats))
	floatingAccumulateOpSrcSIMD(simd, floats)
	floatingAccumulateOpSrc(plain, floats)
	if string(simd) != string(plain) {
		t.Error("floating accumulators differ")
	}
	z = NewRasterizer(100, 100)
	curvePath(z)
	fixed := z.bufU32
	simd, plain = make([]uint8, len(fixed)), make([]uint8, len(fixed))
	fixedAccumulateOpSrcSIMD(simd, fixed)
	fixedAccumulateOpSrc(plain, fixed)
	if string(simd) != string(plain) {
		t.Error("fixed accumulators differ")
	}
}
