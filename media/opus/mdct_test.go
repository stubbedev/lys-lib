package opus

import (
	"math"
	"math/cmplx"
	"testing"
)

type testNoise uint32

func (n *testNoise) next() float32 {
	*n = 1664525**n + 1013904223
	return float32(int32(*n)) / (1 << 31)
}

func TestFFTMatchesTheDefinition(t *testing.T) {
	noise := testNoise(3)
	for lm, m := range mdcts {
		n := m.n / 4
		in := make([]complex128, n)
		got := make([]complex64, n)
		for i := range in {
			v := complex(noise.next(), noise.next())
			in[i] = complex128(v)
			got[m.fft.bitrev[i]] = v
		}
		m.fft.transform(got)
		worst := 0.0
		for k := range n {
			var want complex128
			for i, v := range in {
				want += v * cmplx.Rect(1, -2*math.Pi*float64(i*k%n)/float64(n))
			}
			worst = max(worst, cmplx.Abs(complex128(got[k])-want))
		}
		if limit := 2e-6 * float64(n); worst > limit {
			t.Errorf("LM %d: the %d-point FFT is %g off the DFT of inputs up to 1, want at most %g", lm, n, worst, limit)
		}
	}
}

func TestNewFFTRejectsLayoutsWithoutAButterfly(t *testing.T) {
	for _, c := range []struct {
		name    string
		n       int
		radices []int
	}{
		{"radix 2 first", 8, []int{2, 4}},
		{"radix 2 late", 32, []int{4, 4, 2}},
		{"radix 3 first", 12, []int{3, 4}},
		{"radix 7", 28, []int{4, 7}},
		{"wrong size", 60, []int{4, 3}},
	} {
		t.Run(c.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Errorf("newFFT(%d, %v) built a transform", c.n, c.radices)
				}
			}()
			newFFT(c.n, c.radices...)
		})
	}
}

func windowedBlock(m *mdct, in []float32) []float64 {
	n2 := m.n / 2
	x := make([]float64, m.n)
	at := (n2 - overlap) / 2
	for j, v := range in {
		w := 1.0
		switch {
		case j < overlap:
			w = float64(mdctWindow[j])
		case j >= n2:
			w = float64(mdctWindow[n2+overlap-1-j])
		}
		x[at+j] = w * float64(v)
	}
	return x
}

func TestMDCTForwardMatchesTheDefinition(t *testing.T) {
	noise := testNoise(5)
	var scratch [longN4]complex64
	for lm, m := range mdcts {
		n, n2 := m.n, m.n/2
		in := make([]float32, n2+overlap)
		for i := range in {
			in[i] = noise.next()
		}
		const stride = 3
		got := make([]float32, n2*stride)
		m.forward(in, got, stride, &scratch)
		x := windowedBlock(m, in)
		worst := 0.0
		for k := range n2 {
			want := 0.0
			for i, v := range x {
				want += v * math.Cos(2*math.Pi*(float64(i)+0.5+float64(n)/4)*(float64(k)+0.5)/float64(n))
			}
			want /= float64(n) / 4
			worst = max(worst, math.Abs(float64(got[k*stride])-want))
		}
		if worst > 2e-6 {
			t.Errorf("LM %d: forward is %g off the MDCT of inputs up to 1, want at most 2e-6", lm, worst)
		}
	}
}

func TestMDCTBackwardInvertsForward(t *testing.T) {
	for _, c := range []struct {
		name   string
		m      *mdct
		blocks int
	}{
		{"2.5 ms", mdcts[0], 1},
		{"5 ms", mdcts[1], 1},
		{"10 ms", mdcts[2], 1},
		{"20 ms", longMDCT, 1},
		{"short blocks", shortMDCT, shortBlocks},
	} {
		t.Run(c.name, func(t *testing.T) {
			const frames = 4
			noise := testNoise(9)
			var scratch [longN4]complex64
			nb := c.m.n / 2
			frame := nb * c.blocks
			signal := make([]float32, overlap+frames*frame)
			for i := range signal[overlap:] {
				signal[overlap+i] = noise.next()
			}
			freq := make([]float32, frame)
			out := make([]float32, frames*frame+overlap)
			for f := range frames {
				for b := range c.blocks {
					at := f*frame + b*nb
					c.m.forward(signal[at:at+nb+overlap], freq[b:], c.blocks, &scratch)
					c.m.backward(freq[b:], c.blocks, out[at:], &scratch)
				}
			}
			worst := 0.0
			for i := overlap; i < frames*frame; i++ {
				worst = max(worst, math.Abs(float64(out[i]-signal[i])))
			}
			if worst > 4e-6 {
				t.Errorf("the round trip is %g off inputs up to 1, want at most 4e-6", worst)
			}
		})
	}
}

func BenchmarkMDCT(b *testing.B) {
	noise := testNoise(1)
	var scratch [longN4]complex64
	in := make([]float32, FrameSize+overlap)
	for i := range in {
		in[i] = noise.next()
	}
	freq := make([]float32, FrameSize)
	out := make([]float32, FrameSize+overlap)
	b.Run("forward", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			longMDCT.forward(in, freq, 1, &scratch)
		}
	})
	b.Run("backward", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			longMDCT.backward(freq, 1, out, &scratch)
		}
	})
}
