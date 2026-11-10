package aac

import (
	"math"

	"github.com/stubbedev/lys-lib/third_party/gomath"
)

// The filterbank (4.6.11): inverse MDCTs of 2048 and 256 points,
// windowed with the sine or Kaiser-Bessel-derived window of the frame's
// and the previous frame's shape, overlapped and added.
const (
	longN   = 2 * FrameSize
	shortN  = 256
	shortN2 = shortN / 2
	// shortStart is where the first short window starts in a frame.
	shortStart = (FrameSize - shortN2) / 2
)

// channel is one output channel: the spectrum and windows staged for
// the frame being decoded and the overlap and window shape the last
// frame left.
type channel struct {
	coef      [FrameSize]float32
	seq       windowSequence
	shape     uint8
	prevShape uint8
	overlap   [FrameSize]float32
}

// Window halves, rising, by shape (0 sine, 1 KBD).
var (
	longWindows = func() (w [2][FrameSize]float32) {
		sineWindow(w[0][:])
		kbdWindow(w[1][:], 4)
		return w
	}()
	shortWindows = func() (w [2][shortN2]float32) {
		sineWindow(w[0][:])
		kbdWindow(w[1][:], 6)
		return w
	}()
)

// sineWindow is the rising half of the sine window of 2n points.
func sineWindow(w []float32) {
	n := len(w)
	for i := range n {
		w[i] = float32(gomath.Sin(math.Pi / float64(2*n) * (float64(i) + 0.5)))
	}
}

// kbdWindow is the rising half of the Kaiser-Bessel-derived window of
// 2n points with parameter alpha (4.6.11.3.2).
func kbdWindow(w []float32, alpha float64) {
	n := len(w)
	kernel := make([]float64, n+1)
	for i := range kernel {
		x := float64(2*float64(i)/float64(n)) - 1
		kernel[i] = besselI0(math.Pi * alpha * math.Sqrt(1-float64(x*x)))
	}
	var total float64
	for _, k := range kernel {
		total += k
	}
	var sum float64
	for i := range n {
		sum += kernel[i]
		w[i] = float32(math.Sqrt(sum / total))
	}
}

// besselI0 is the zeroth-order modified Bessel function of the first
// kind, by its power series.
func besselI0(x float64) float64 {
	sum, term := 1.0, 1.0
	for k := 1; k < 50; k++ {
		f := x / (2 * float64(k))
		term = float64(term * float64(f*f))
		sum += term
	}
	return sum
}

// synthesize turns the staged spectrum into the frame's output samples
// and the overlap for the next frame.
func (c *channel) synthesize(t *imdct, out *[FrameSize]float32) {
	var buf [longN]float32
	cur, prev := c.shape, c.prevShape
	long, prevLong := &longWindows[cur], &longWindows[prev]
	switch c.seq {
	case eightShort:
		var acc [longN]float32
		var b [shortN]float32
		for w := range maxWindows {
			t.short(c.coef[w*shortN2:(w+1)*shortN2], b[:])
			left := &shortWindows[cur]
			if w == 0 {
				left = &shortWindows[prev]
			}
			right := &shortWindows[cur]
			dst := acc[shortStart+w*shortN2:]
			for i := range shortN2 {
				dst[i] += float32(b[i] * left[i])
				dst[shortN2+i] += float32(b[shortN2+i] * right[shortN2-1-i])
			}
		}
		for i := range FrameSize {
			out[i] = c.overlap[i] + acc[i]
		}
		copy(c.overlap[:], acc[FrameSize:])
	default:
		t.long(c.coef[:], buf[:])
		switch c.seq {
		case longStop:
			short := &shortWindows[prev]
			for i := range shortStart {
				out[i] = c.overlap[i]
			}
			for i := range shortN2 {
				out[shortStart+i] = c.overlap[shortStart+i] + float32(buf[shortStart+i]*short[i])
			}
			for i := shortStart + shortN2; i < FrameSize; i++ {
				out[i] = c.overlap[i] + buf[i]
			}
		default:
			for i := range FrameSize {
				out[i] = c.overlap[i] + float32(buf[i]*prevLong[i])
			}
		}
		if c.seq == longStart {
			short := &shortWindows[cur]
			copy(c.overlap[:shortStart], buf[FrameSize:FrameSize+shortStart])
			for i := range shortN2 {
				c.overlap[shortStart+i] = buf[FrameSize+shortStart+i] * short[shortN2-1-i]
			}
			clear(c.overlap[shortStart+shortN2:])
		} else {
			for i := range FrameSize {
				c.overlap[i] = buf[FrameSize+i] * long[FrameSize-1-i]
			}
		}
	}
	c.prevShape = cur
}

// imdct computes the standard's inverse MDCT (4.6.11.3.1),
// x[n] = 2/N sum X[k] cos(2pi/N (n + n0)(k + 1/2)), by an N/4-point
// complex FFT between pre- and post-twiddles.
type imdct struct {
	z [FrameSize / 2]complex64
}

// imdctPlan is the twiddles, the FFT's roots per stage from the third
// on, and the bit reversal of one size.
type imdctPlan struct {
	n       int
	twiddle []complex64
	stages  [][]complex64
	rev     []uint16
}

var (
	longPlan  = newIMDCTPlan(longN)
	shortPlan = newIMDCTPlan(shortN)
)

func newIMDCTPlan(n int) *imdctPlan {
	n4 := n / 4
	p := &imdctPlan{n: n, twiddle: make([]complex64, n4), rev: make([]uint16, n4)}
	scale := math.Sqrt(2 / float64(n))
	for k := range n4 {
		a := 2 * math.Pi * (float64(k) + 0.125) / float64(n)
		p.twiddle[k] = complex(float32(scale*gomath.Cos(a)), float32(scale*gomath.Sin(a)))
	}
	for size := 8; size <= n4; size <<= 1 {
		roots := make([]complex64, size/2)
		for k := range roots {
			a := 2 * math.Pi * float64(k) / float64(size)
			roots[k] = complex(float32(gomath.Cos(a)), float32(gomath.Sin(a)))
		}
		p.stages = append(p.stages, roots)
	}
	bits := 0
	for 1<<bits < n4 {
		bits++
	}
	for i := range n4 {
		r := 0
		for b := range bits {
			r |= (i >> b & 1) << (bits - 1 - b)
		}
		p.rev[i] = uint16(r)
	}
	return p
}

func (t *imdct) long(in, out []float32)  { t.run(longPlan, in, out) }
func (t *imdct) short(in, out []float32) { t.run(shortPlan, in, out) }

// run is the inverse MDCT of the n/2 coefficients in into the n
// samples of out.
func (t *imdct) run(p *imdctPlan, in, out []float32) {
	n := p.n
	n2, n4, n8 := n/2, n/4, n/8
	z := t.z[:n4]
	for k := range n4 {
		x1, x2 := in[2*k], in[n2-1-2*k]
		c, s := real(p.twiddle[k]), imag(p.twiddle[k])
		z[p.rev[k]] = complex(float32(x2*c)-float32(x1*s), float32(x1*c)+float32(x2*s))
	}
	for i := 0; i < n4; i += 4 {
		a, b, c, e := z[i], z[i+1], z[i+2], z[i+3]
		s0, d0 := a+b, a-b
		s1, d1 := c+e, c-e
		rot := complex(-imag(d1), real(d1))
		z[i], z[i+2] = s0+s1, s0-s1
		z[i+1], z[i+3] = d0+rot, d0-rot
	}
	for _, roots := range p.stages {
		half := len(roots)
		for start := 0; start < n4; start += 2 * half {
			lo, hi := z[start:start+half], z[start+half:start+2*half]
			for j, w := range roots {
				a, b := lo[j], hi[j]
				br, bi := real(b), imag(b)
				wr, wi := real(w), imag(w)
				m := complex(float32(br*wr)-float32(bi*wi), float32(br*wi)+float32(bi*wr))
				lo[j] = a + m
				hi[j] = a - m
			}
		}
	}
	for k := range n4 {
		re, im := real(z[k]), imag(z[k])
		c, s := real(p.twiddle[k]), imag(p.twiddle[k])
		z[k] = complex(float32(re*c)-float32(im*s), float32(im*c)+float32(re*s))
	}
	for k := 0; k < n8; k += 2 {
		out[2*k] = imag(z[n8+k])
		out[2+2*k] = imag(z[n8+1+k])
		out[1+2*k] = -real(z[n8-1-k])
		out[3+2*k] = -real(z[n8-2-k])
		out[n4+2*k] = real(z[k])
		out[n4+2+2*k] = real(z[1+k])
		out[n4+1+2*k] = -imag(z[n4-1-k])
		out[n4+3+2*k] = -imag(z[n4-2-k])
		out[n2+2*k] = real(z[n8+k])
		out[n2+2+2*k] = real(z[n8+1+k])
		out[n2+1+2*k] = -imag(z[n8-1-k])
		out[n2+3+2*k] = -imag(z[n8-2-k])
		out[n2+n4+2*k] = -imag(z[k])
		out[n2+n4+2+2*k] = -imag(z[1+k])
		out[n2+n4+1+2*k] = real(z[n4-1-k])
		out[n2+n4+3+2*k] = real(z[n4-2-k])
	}
}
