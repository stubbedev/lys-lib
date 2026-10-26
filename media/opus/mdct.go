package opus

import "math"

// The 20 ms MDCT: 1920 inputs (960 new samples behind the 120-sample
// overlap, the window zero elsewhere) to 960 coefficients, through a
// 480-point complex FFT.
const (
	mdctN    = 2 * FrameSize
	mdctN2   = mdctN / 2
	mdctN4   = mdctN / 4
	maxRadix = 5
)

// fftFactors factors mdctN4 into the radices the FFT runs.
var fftFactors = [...]int{4, 4, 2, 3, 5}

// mdctTables are the transform's constants, computed once.
type mdctTables struct {
	window  [overlap]float32
	trig    [mdctN4 + 1]float32
	twiddle [mdctN4]complex64
	sine    float32
}

var mdctConsts = newMDCTTables()

func newMDCTTables() *mdctTables {
	t := &mdctTables{sine: float32(2 * math.Pi * 0.125 / mdctN)}
	for i := range t.window {
		s := math.Sin(0.5 * math.Pi * (float64(i) + 0.5) / overlap)
		t.window[i] = float32(math.Sin(0.5 * math.Pi * s * s))
	}
	for i := range t.trig {
		t.trig[i] = float32(math.Cos(2 * math.Pi * float64(i) / mdctN))
	}
	for i := range t.twiddle {
		phase := -2 * math.Pi * float64(i) / mdctN4
		t.twiddle[i] = complex(float32(math.Cos(phase)), float32(math.Sin(phase)))
	}
	return t
}

// fft is the forward DFT of src into dst (both mdctN4 long, unscaled),
// a recursive mixed-radix decimation in time.
func (t *mdctTables) fft(dst, src []complex64) {
	t.fftStage(dst, src, 1, 0)
}

func (t *mdctTables) fftStage(dst, src []complex64, stride, depth int) {
	p := fftFactors[depth]
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
			var sum complex64
			for q := range p {
				sum += dst[q*m+k] * t.twiddle[q*(k+u*m)*stride%mdctN4]
			}
			sums[u] = sum
		}
		for u := range p {
			dst[k+u*m] = sums[u]
		}
	}
}

// mdct is clt_mdct_forward for one long block: in holds the overlap
// and the frame (FrameSize+overlap samples), out the coefficients.
func (t *mdctTables) mdct(in []float32, out []float32, f, g *[mdctN4]complex64) {
	w := &t.window
	xp1 := overlap / 2
	xp2 := mdctN2 - 1 + overlap/2
	wp1 := overlap / 2
	wp2 := overlap/2 - 1
	i := 0
	for ; i < overlap/4; i++ {
		f[i] = complex(w[wp2]*in[xp1+mdctN2]+w[wp1]*in[xp2], w[wp1]*in[xp1]-w[wp2]*in[xp2-mdctN2])
		xp1 += 2
		xp2 -= 2
		wp1 += 2
		wp2 -= 2
	}
	wp1 = 0
	wp2 = overlap - 1
	for ; i < mdctN4-overlap/4; i++ {
		f[i] = complex(in[xp2], in[xp1])
		xp1 += 2
		xp2 -= 2
	}
	for ; i < mdctN4; i++ {
		f[i] = complex(-w[wp1]*in[xp1-mdctN2]+w[wp2]*in[xp2], w[wp2]*in[xp1]+w[wp1]*in[xp2+mdctN2])
		xp1 += 2
		xp2 -= 2
		wp1 += 2
		wp2 -= 2
	}
	for i := range mdctN4 {
		re, im := real(f[i]), imag(f[i])
		yr := -re*t.trig[i] - im*t.trig[mdctN4-i]
		yi := -im*t.trig[i] + re*t.trig[mdctN4-i]
		f[i] = complex(yr+yi*t.sine, yi-yr*t.sine)
	}
	t.fft(g[:], f[:])
	const scale = float32(1) / mdctN4
	for i := range mdctN4 {
		re, im := real(g[i])*scale, imag(g[i])*scale
		yr := im*t.trig[mdctN4-i] + re*t.trig[i]
		yi := re*t.trig[mdctN4-i] - im*t.trig[i]
		out[2*i] = yr - yi*t.sine
		out[mdctN2-1-2*i] = yi + yr*t.sine
	}
}
