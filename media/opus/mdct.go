package opus

import (
	"fmt"
	"math"

	"github.com/stubbedev/lys-lib/third_party/gomath"
)

// The MDCT runs at two sizes: one long block over the whole frame (1920
// inputs to 960 coefficients) or, on transients, shortBlocks short ones
// (240 inputs to 120 coefficients each), all sharing the 120-sample
// overlap window and a mixed-radix complex FFT of a quarter the size.
const (
	shortBlocks = FrameSize / shortMdct
	longN4      = FrameSize / 2
)

var mdctWindow = newWindow()

func newWindow() *[overlap]float32 {
	var w [overlap]float32
	for i := range w {
		s := gomath.Sin(0.5 * math.Pi * (float64(i) + 0.5) / overlap)
		w[i] = float32(gomath.Sin(0.5 * math.Pi * s * s))
	}
	return &w
}

type fft struct {
	bitrev []uint16
	stages []fftStage
}

type fftStage struct {
	butterfly func(s *fftStage, x []complex64)
	m         int
	twiddle   []complex64
	root1     complex64
	root2     complex64
}

func newFFT(n int, radices ...int) *fft {
	f := &fft{bitrev: make([]uint16, n), stages: make([]fftStage, len(radices))}
	root := func(i int) complex64 {
		phase := -2 * math.Pi * float64(i) / float64(n)
		return complex(float32(gomath.Cos(phase)), float32(gomath.Sin(phase)))
	}
	m := 1
	for i, radix := range radices {
		s := &f.stages[i]
		s.m = m
		switch {
		case radix == 4 && i == 0:
			s.butterfly = butterfly4Points
		case radix == 2 && i == 1:
			s.butterfly = butterfly2
		case radix == 3 && i > 0:
			s.butterfly = butterfly3
		case radix == 4 && i > 0:
			s.butterfly = butterfly4
		case radix == 5 && i > 0:
			s.butterfly = butterfly5
		default:
			panic(fmt.Sprintf("opus: no radix-%d butterfly for stage %d of a %d-point FFT", radix, i, n))
		}
		if radix != 2 && m > 1 {
			stride := n / (radix * m)
			s.twiddle = make([]complex64, 0, (radix-1)*m)
			for q := 1; q < radix; q++ {
				for j := range m {
					s.twiddle = append(s.twiddle, root(q*j*stride))
				}
			}
			s.root1, s.root2 = root(n/radix), root(2*n/radix)
		}
		m *= radix
	}
	if m != n {
		panic(fmt.Sprintf("opus: radices %v make a %d-point FFT, not %d", radices, m, n))
	}
	at, digits := 0, make([]int, len(radices))
	for i := range f.bitrev {
		f.bitrev[i] = uint16(at)
		for s := len(radices) - 1; s >= 0; s-- {
			at += f.stages[s].m
			if digits[s]++; digits[s] < radices[s] {
				break
			}
			at -= radices[s] * f.stages[s].m
			digits[s] = 0
		}
	}
	return f
}

func (f *fft) transform(x []complex64) {
	for i := range f.stages {
		s := &f.stages[i]
		s.butterfly(s, x)
	}
}

func complexMul(a, b complex64) complex64 {
	ar, ai, br, bi := real(a), imag(a), real(b), imag(b)
	return complex(float32(ar*br)-float32(ai*bi), float32(ar*bi)+float32(ai*br))
}

func butterfly4Points(_ *fftStage, x []complex64) {
	for ; len(x) >= 4; x = x[4:] {
		x0, x1, x2, x3 := x[0], x[1], x[2], x[3]
		d02 := x0 - x2
		x0 += x2
		s13 := x1 + x3
		d13 := x1 - x3
		x[0] = x0 + s13
		x[1] = complex(real(d02)+imag(d13), imag(d02)-real(d13))
		x[2] = x0 - s13
		x[3] = complex(real(d02)-imag(d13), imag(d02)+real(d13))
	}
}

func butterfly2(_ *fftStage, x []complex64) {
	const tw float32 = 0.7071067812
	for ; len(x) >= 8; x = x[8:] {
		a, b := x[0], x[4]
		x[0], x[4] = a+b, a-b
		a, b = x[1], x[5]
		t := complex(float32((real(b)+imag(b))*tw), float32((imag(b)-real(b))*tw))
		x[1], x[5] = a+t, a-t
		a, b = x[2], x[6]
		t = complex(imag(b), -real(b))
		x[2], x[6] = a+t, a-t
		a, b = x[3], x[7]
		t = complex(float32((imag(b)-real(b))*tw), float32(-(imag(b)+real(b))*tw))
		x[3], x[7] = a+t, a-t
	}
}

func butterfly3(s *fftStage, x []complex64) {
	m := s.m
	tw1, tw2 := s.twiddle[:m], s.twiddle[m:2*m]
	epi3 := imag(s.root1)
	for ; len(x) >= 3*m; x = x[3*m:] {
		x0 := x[:m]
		x1, x2 := x[m : 2*m][:len(x0)], x[2*m : 3*m][:len(x0)]
		tw1, tw2 := tw1[:len(x0)], tw2[:len(x0)]
		for j, a := range x0 {
			b := complexMul(x1[j], tw1[j])
			c := complexMul(x2[j], tw2[j])
			sum, diff := b+c, b-c
			hr := real(a) - float32(0.5*real(sum))
			hi := imag(a) - float32(0.5*imag(sum))
			dr, di := float32(real(diff)*epi3), float32(imag(diff)*epi3)
			x0[j] = a + sum
			x1[j] = complex(hr-di, hi+dr)
			x2[j] = complex(hr+di, hi-dr)
		}
	}
}

func butterfly4(s *fftStage, x []complex64) {
	m := s.m
	tw1, tw2, tw3 := s.twiddle[:m], s.twiddle[m:2*m], s.twiddle[2*m:3*m]
	for ; len(x) >= 4*m; x = x[4*m:] {
		x0 := x[:m]
		x1, x2, x3 := x[m : 2*m][:len(x0)], x[2*m : 3*m][:len(x0)], x[3*m : 4*m][:len(x0)]
		tw1, tw2, tw3 := tw1[:len(x0)], tw2[:len(x0)], tw3[:len(x0)]
		for j, a := range x0 {
			b := complexMul(x1[j], tw1[j])
			c := complexMul(x2[j], tw2[j])
			d := complexMul(x3[j], tw3[j])
			dac := a - c
			a += c
			sbd, dbd := b+d, b-d
			x0[j] = a + sbd
			x1[j] = complex(real(dac)+imag(dbd), imag(dac)-real(dbd))
			x2[j] = a - sbd
			x3[j] = complex(real(dac)-imag(dbd), imag(dac)+real(dbd))
		}
	}
}

func butterfly5(s *fftStage, x []complex64) {
	m := s.m
	tw1, tw2, tw3, tw4 := s.twiddle[:m], s.twiddle[m:2*m], s.twiddle[2*m:3*m], s.twiddle[3*m:4*m]
	yar, yai, ybr, ybi := real(s.root1), imag(s.root1), real(s.root2), imag(s.root2)
	for ; len(x) >= 5*m; x = x[5*m:] {
		x0 := x[:m]
		x1, x2, x3, x4 := x[m : 2*m][:len(x0)], x[2*m : 3*m][:len(x0)], x[3*m : 4*m][:len(x0)], x[4*m : 5*m][:len(x0)]
		tw1, tw2, tw3, tw4 := tw1[:len(x0)], tw2[:len(x0)], tw3[:len(x0)], tw4[:len(x0)]
		for j, a := range x0 {
			b := complexMul(x1[j], tw1[j])
			c := complexMul(x2[j], tw2[j])
			d := complexMul(x3[j], tw3[j])
			e := complexMul(x4[j], tw4[j])
			sbe, dbe := b+e, b-e
			scd, dcd := c+d, c-d
			x0[j] = complex(real(a)+(real(sbe)+real(scd)), imag(a)+(imag(sbe)+imag(scd)))
			p := complex(
				real(a)+(float32(real(sbe)*yar)+float32(real(scd)*ybr)),
				imag(a)+(float32(imag(sbe)*yar)+float32(imag(scd)*ybr)),
			)
			q := complex(
				float32(imag(dbe)*yai)+float32(imag(dcd)*ybi),
				-(float32(real(dbe)*yai) + float32(real(dcd)*ybi)),
			)
			x1[j], x4[j] = p-q, p+q
			p = complex(
				real(a)+(float32(real(sbe)*ybr)+float32(real(scd)*yar)),
				imag(a)+(float32(imag(sbe)*ybr)+float32(imag(scd)*yar)),
			)
			q = complex(
				float32(imag(dcd)*yai)-float32(imag(dbe)*ybi),
				float32(real(dbe)*ybi)-float32(real(dcd)*yai),
			)
			x2[j], x3[j] = p+q, p-q
		}
	}
}

// mdct is the lapped transform of one size, libopus's clt_mdct_forward
// and clt_mdct_backward: n inputs to n/2 coefficients and back through
// the FFT of a quarter the size, rotated before and after by
// cos(2π(i+1/8)/n) for i up to n/2.
type mdct struct {
	n     int
	fft   *fft
	trig  []float32
	scale float32
}

// mdcts are the transforms per LM: 2.5 ms << LM long blocks; LM 0 is
// also every short block's.
var (
	mdcts = [4]*mdct{
		newMDCT(2*shortMdct, 4, 3, 5),
		newMDCT(4*shortMdct, 4, 2, 3, 5),
		newMDCT(8*shortMdct, 4, 4, 3, 5),
		newMDCT(2*FrameSize, 4, 2, 4, 3, 5),
	}
	shortMDCT = mdcts[0]
	longMDCT  = mdcts[len(mdcts)-1]
)

func newMDCT(n int, radices ...int) *mdct {
	t := &mdct{n: n, fft: newFFT(n/4, radices...), trig: make([]float32, n/2), scale: 1 / float32(n/4)}
	for i := range t.trig {
		t.trig[i] = float32(gomath.Cos(2 * math.Pi * (float64(i) + 0.125) / float64(n)))
	}
	return t
}

func (t *mdct) rotateIn(f []complex64, i int, re, im float32) {
	n4 := t.n / 4
	t0, t1 := t.trig[i], t.trig[n4+i]
	yr := float32(re*t0) - float32(im*t1)
	yi := float32(im*t0) + float32(re*t1)
	f[t.fft.bitrev[i]] = complex(yr*t.scale, yi*t.scale)
}

// forward transforms in, the overlap and the block (n/2+overlap
// samples); coefficient k lands in out[k*stride], so short blocks
// interleave.
func (t *mdct) forward(in, out []float32, stride int, scratch *[longN4]complex64) {
	n2, n4 := t.n/2, t.n/4
	f := scratch[:n4]
	w := mdctWindow
	xp1 := overlap / 2
	xp2 := n2 - 1 + overlap/2
	wp1 := overlap / 2
	wp2 := overlap/2 - 1
	i := 0
	for ; i < overlap/4; i++ {
		t.rotateIn(f, i,
			float32(w[wp2]*in[xp1+n2])+float32(w[wp1]*in[xp2]),
			float32(w[wp1]*in[xp1])-float32(w[wp2]*in[xp2-n2]))
		xp1 += 2
		xp2 -= 2
		wp1 += 2
		wp2 -= 2
	}
	wp1 = 0
	wp2 = overlap - 1
	for ; i < n4-overlap/4; i++ {
		t.rotateIn(f, i, in[xp2], in[xp1])
		xp1 += 2
		xp2 -= 2
	}
	for ; i < n4; i++ {
		t.rotateIn(f, i,
			float32(-w[wp1]*in[xp1-n2])+float32(w[wp2]*in[xp2]),
			float32(w[wp2]*in[xp1])+float32(w[wp1]*in[xp2+n2]))
		xp1 += 2
		xp2 -= 2
		wp1 += 2
		wp2 -= 2
	}
	t.fft.transform(f)
	t0, t1 := t.trig[:n4], t.trig[n4:n2]
	for i, c := range f {
		re, im := real(c), imag(c)
		out[2*i*stride] = float32(im*t1[i]) - float32(re*t0[i])
		out[(n2-1-2*i)*stride] = float32(re*t1[i]) + float32(im*t0[i])
	}
}

// backward inverts the n/2 coefficients in[k*stride] into out: n/2
// samples from out[overlap/2], then the windowed TDAC mirror over
// out[:overlap] with the n/2 samples the previous block left there.
func (t *mdct) backward(in []float32, stride int, out []float32, scratch *[longN4]complex64) {
	n2, n4 := t.n/2, t.n/4
	f := scratch[:n4]
	trig := t.trig
	t0, t1 := trig[:n4], trig[n4:n2]
	for i, rev := range t.fft.bitrev {
		x1, x2 := in[2*i*stride], in[(n2-1-2*i)*stride]
		yr := float32(x2*t0[i]) + float32(x1*t1[i])
		yi := float32(x1*t0[i]) - float32(x2*t1[i])
		f[rev] = complex(yi, yr)
	}
	t.fft.transform(f)
	y := out[overlap/2:]
	for i := range (n4 + 1) >> 1 {
		j := n4 - 1 - i
		re, im := imag(f[i]), real(f[i])
		yr := float32(re*trig[i]) + float32(im*trig[n4+i])
		yi := float32(re*trig[n4+i]) - float32(im*trig[i])
		re2, im2 := imag(f[j]), real(f[j])
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
