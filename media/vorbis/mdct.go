package vorbis

import (
	"math"
	"math/bits"

	"github.com/stubbedev/lys-lib/intconv"
	"github.com/stubbedev/lys-lib/third_party/gomath"
)

type imdct struct {
	n            int
	preRe, preIm []float32
	postRe       []float32
	postIm       []float32
	twRe, twIm   []float32
	bitrev       []int32
	re, im       []float32
	u            []float32
}

func newIMDCT(n int) *imdct {
	m := n / 2
	l := m / 2
	t := &imdct{
		n:      n,
		preRe:  make([]float32, l),
		preIm:  make([]float32, l),
		postRe: make([]float32, l),
		postIm: make([]float32, l),
		bitrev: make([]int32, l),
		re:     make([]float32, l),
		im:     make([]float32, l),
		u:      make([]float32, m),
	}
	for k := range l {
		s, c := gomath.Sincos(-math.Pi * float64(4*k+1) / float64(4*m))
		t.preRe[k], t.preIm[k] = float32(c), float32(s)
		s, c = gomath.Sincos(-math.Pi * float64(k) / float64(m))
		t.postRe[k], t.postIm[k] = float32(c), float32(s)
	}
	for size := 8; size <= l; size <<= 1 {
		for k := range size / 2 {
			s, c := gomath.Sincos(-2 * math.Pi * float64(k) / float64(size))
			t.twRe = append(t.twRe, float32(c))
			t.twIm = append(t.twIm, float32(s))
		}
	}
	shift := 32 - uint(bits.Len(uint(l))-1)
	for k := range l {
		t.bitrev[k] = intconv.Must[int32](bits.Reverse32(intconv.Must[uint32](k)) >> shift)
	}
	return t
}

func (t *imdct) inverse(x, y []float32) {
	m := t.n / 2
	l := m / 2
	re, im := t.re[:l], t.im[:l]
	x = x[:m]
	pre, pim, rev := t.preRe[:l], t.preIm[:l], t.bitrev[:l]
	for k := range l {
		a, b := x[2*k], x[m-1-2*k]
		c, d := pre[k], pim[k]
		j := rev[k]
		re[j] = float32(a*c) - float32(b*d)
		im[j] = float32(a*d) + float32(b*c)
	}
	for g := 0; g+3 < l; g += 4 {
		r, i := re[g:g+4:g+4], im[g:g+4:g+4]
		b0r, b0i := r[0]+r[1], i[0]+i[1]
		b1r, b1i := r[0]-r[1], i[0]-i[1]
		b2r, b2i := r[2]+r[3], i[2]+i[3]
		b3r, b3i := r[2]-r[3], i[2]-i[3]
		r[0], i[0] = b0r+b2r, b0i+b2i
		r[2], i[2] = b0r-b2r, b0i-b2i
		r[1], i[1] = b1r+b3i, b1i-b3r
		r[3], i[3] = b1r-b3i, b1i+b3r
	}
	tw := 0
	for size := 8; size <= l; size <<= 1 {
		half := size >> 1
		wr, wi := t.twRe[tw:tw+half], t.twIm[tw:tw+half]
		tw += half
		for start := 0; start < l; start += size {
			ar, ai := re[start:start+half], im[start:start+half]
			br, bi := re[start+half:start+size], im[start+half:start+size]
			br, bi = br[:len(ar)], bi[:len(ar)]
			ai = ai[:len(ar)]
			for k := range ar {
				xr, xi := br[k], bi[k]
				cr, ci := wr[k], wi[k]
				tr := float32(xr*cr) - float32(xi*ci)
				ti := float32(xr*ci) + float32(xi*cr)
				pr, pi := ar[k], ai[k]
				ar[k], ai[k] = pr+tr, pi+ti
				br[k], bi[k] = pr-tr, pi-ti
			}
		}
	}
	u := t.u[:m]
	post, poim := t.postRe[:l], t.postIm[:l]
	for j := range l {
		a, b := re[j], im[j]
		c, d := post[j], poim[j]
		u[2*j] = float32(a*c) - float32(b*d)
		u[m-1-2*j] = -(float32(a*d) + float32(b*c))
	}
	n4 := t.n / 4
	y = y[:t.n]
	copy(y[:n4], u[n4:])
	for i := n4; i < 3*n4; i++ {
		y[i] = -u[3*n4-1-i]
	}
	for i := 3 * n4; i < t.n; i++ {
		y[i] = -u[i-3*n4]
	}
}
