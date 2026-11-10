package opus

import (
	"math"

	"github.com/stubbedev/lys-lib/third_party/gomath"
)

// The MDCT runs at two sizes: one long block over the whole frame (1920
// inputs to 960 coefficients) or, on transients, shortBlocks short ones
// (240 inputs to 120 coefficients each), all sharing the 120-sample
// overlap window and a mixed-radix complex FFT of a quarter the size.
const (
	shortBlocks = FrameSize / shortMdct
	maxRadix    = 5
	longN4      = FrameSize / 2
)

// mdct is one transform size's constants, computed once.
type mdct struct {
	n       int
	factors []int
	trig    []float32
	twiddle []complex64
	sine    float32
}

var (
	mdctWindow = newWindow()
	longMDCT   = newMDCT(2*FrameSize, 4, 4, 2, 3, 5)
	shortMDCT  = newMDCT(2*shortMdct, 4, 3, 5)
)

func newWindow() *[overlap]float32 {
	var w [overlap]float32
	for i := range w {
		s := gomath.Sin(0.5 * math.Pi * (float64(i) + 0.5) / overlap)
		w[i] = float32(gomath.Sin(0.5 * math.Pi * s * s))
	}
	return &w
}

// newMDCT builds an n-input transform whose quarter-size FFT factors
// into the given radices.
func newMDCT(n int, factors ...int) *mdct {
	n4 := n / 4
	t := &mdct{
		n:       n,
		factors: factors,
		trig:    make([]float32, n4+1),
		twiddle: make([]complex64, n4),
		sine:    float32(2 * math.Pi * 0.125 / float64(n)),
	}
	for i := range t.trig {
		t.trig[i] = float32(gomath.Cos(2 * math.Pi * float64(i) / float64(n)))
	}
	for i := range t.twiddle {
		phase := -2 * math.Pi * float64(i) / float64(n4)
		t.twiddle[i] = complex(float32(gomath.Cos(phase)), float32(gomath.Sin(phase)))
	}
	return t
}

// fftStage is the forward DFT of src into dst (unscaled), a recursive
// mixed-radix decimation in time.
func (t *mdct) fftStage(dst, src []complex64, stride, depth int) {
	n4 := len(t.twiddle)
	p := t.factors[depth]
	m := len(dst) / p
	for q := range p {
		if m == 1 {
			dst[q] = src[q*stride]
			continue
		}
		t.fftStage(dst[q*m:(q+1)*m], src[q*stride:], stride*p, depth+1)
	}
	var sums [maxRadix]complex64
	for k := range m {
		for u := range p {
			step := (k + u*m) * stride % n4
			w := 0
			var sum complex64
			for q := range p {
				sum += complexMul(dst[q*m+k], t.twiddle[w])
				if w += step; w >= n4 {
					w -= n4
				}
			}
			sums[u] = sum
		}
		for u := range p {
			dst[k+u*m] = sums[u]
		}
	}
}

// forward is clt_mdct_forward: in holds the overlap and the block
// (n/2+overlap samples); coefficient k lands in out[k*stride], so short
// blocks interleave. f and g are scratch of at least n/4.
func (t *mdct) forward(in, out []float32, stride int, f, g []complex64) {
	n2, n4 := t.n/2, t.n/4
	f, g = f[:n4], g[:n4]
	w := mdctWindow
	xp1 := overlap / 2
	xp2 := n2 - 1 + overlap/2
	wp1 := overlap / 2
	wp2 := overlap/2 - 1
	i := 0
	for ; i < overlap/4; i++ {
		f[i] = complex(float32(w[wp2]*in[xp1+n2])+float32(w[wp1]*in[xp2]), float32(w[wp1]*in[xp1])-float32(w[wp2]*in[xp2-n2]))
		xp1 += 2
		xp2 -= 2
		wp1 += 2
		wp2 -= 2
	}
	wp1 = 0
	wp2 = overlap - 1
	for ; i < n4-overlap/4; i++ {
		f[i] = complex(in[xp2], in[xp1])
		xp1 += 2
		xp2 -= 2
	}
	for ; i < n4; i++ {
		f[i] = complex(float32(-w[wp1]*in[xp1-n2])+float32(w[wp2]*in[xp2]), float32(w[wp2]*in[xp1])+float32(w[wp1]*in[xp2+n2]))
		xp1 += 2
		xp2 -= 2
		wp1 += 2
		wp2 -= 2
	}
	for i := range n4 {
		re, im := real(f[i]), imag(f[i])
		yr := float32(-re*t.trig[i]) - float32(im*t.trig[n4-i])
		yi := float32(-im*t.trig[i]) + float32(re*t.trig[n4-i])
		f[i] = complex(yr+float32(yi*t.sine), yi-float32(yr*t.sine))
	}
	t.fftStage(g, f, 1, 0)
	scale := 1 / float32(n4)
	for i := range n4 {
		re, im := real(g[i])*scale, imag(g[i])*scale
		yr := float32(im*t.trig[n4-i]) + float32(re*t.trig[i])
		yi := float32(re*t.trig[n4-i]) - float32(im*t.trig[i])
		out[2*i*stride] = yr - float32(yi*t.sine)
		out[(n2-1-2*i)*stride] = yi + float32(yr*t.sine)
	}
}

// complexMul is a*b as the compiler lowers it, in float64 and then
// narrowed, with each product rounded: the operator's own products may
// fuse on arm64, where they would round differently.
func complexMul(a, b complex64) complex64 {
	ar, ai := float64(real(a)), float64(imag(a))
	br, bi := float64(real(b)), float64(imag(b))
	return complex(float32(float64(ar*br)-float64(ai*bi)), float32(float64(ar*bi)+float64(ai*br)))
}

// imdct is clt_mdct_backward for one transform size: the FFT of a
// quarter the size and the backward transform's rotation, cos(2π(i +
// 1/8)/n) for i up to n/2.
type imdct struct {
	fft  *mdct
	trig []float32
}

// imdcts are the backward transforms per LM: 2.5 ms << LM long blocks;
// LM 0 is also every short block's.
var imdcts = [4]*imdct{
	newIMDCT(shortMDCT),
	newIMDCT(newMDCT(4*shortMdct, 4, 2, 3, 5)),
	newIMDCT(newMDCT(8*shortMdct, 4, 4, 3, 5)),
	newIMDCT(longMDCT),
}

func newIMDCT(m *mdct) *imdct {
	t := &imdct{fft: m, trig: make([]float32, m.n/2)}
	for i := range t.trig {
		t.trig[i] = float32(gomath.Cos(2 * math.Pi * (float64(i) + 0.125) / float64(m.n)))
	}
	return t
}

// backward inverts the n/2 coefficients in[k*stride] into out: n/2
// samples from out[overlap/2], then the windowed TDAC mirror over
// out[:overlap] with the n/2 samples the previous block left there.
// f and g are scratch of at least n/4.
func (t *imdct) backward(in []float32, stride int, out []float32, f, g []complex64) {
	n2, n4 := t.fft.n/2, t.fft.n/4
	f, g = f[:n4], g[:n4]
	trig := t.trig
	for i := range n4 {
		x1, x2 := in[2*i*stride], in[(n2-1-2*i)*stride]
		yr := float32(x2*trig[i]) + float32(x1*trig[n4+i])
		yi := float32(x1*trig[i]) - float32(x2*trig[n4+i])
		f[i] = complex(yi, yr)
	}
	t.fft.fftStage(g, f, 1, 0)
	y := out[overlap/2:]
	for i := range (n4 + 1) >> 1 {
		j := n4 - 1 - i
		re, im := imag(g[i]), real(g[i])
		yr := float32(re*trig[i]) + float32(im*trig[n4+i])
		yi := float32(re*trig[n4+i]) - float32(im*trig[i])
		re2, im2 := imag(g[j]), real(g[j])
		y[2*i] = yr
		y[2*j+1] = yi
		yr = float32(re2*trig[n4-i-1]) + float32(im2*trig[n2-i-1])
		yi = float32(re2*trig[n2-i-1]) - float32(im2*trig[n4-i-1])
		y[2*j] = yr
		y[2*i+1] = yi
	}
	w := mdctWindow
	for i := range overlap / 2 {
		x1, x2 := out[overlap-1-i], out[i]
		out[i] = float32(w[overlap-1-i]*x2) - float32(w[i]*x1)
		out[overlap-1-i] = float32(w[i]*x2) + float32(w[overlap-1-i]*x1)
	}
}
