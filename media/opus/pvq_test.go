package opus

import (
	"math"
	"slices"
	"testing"
)

type codebook struct{ n, k int }

func reachableCodebooks() []codebook {
	var books []codebook
	for lm := -1; lm <= frameLM; lm++ {
		for i := range nbBands {
			n := bandWidth(i) << (lm + 1) >> 1
			if cacheIndex[(lm+1)*nbBands+i] < 0 || n < 2 {
				continue
			}
			for j := 1; j <= cacheRow(i, lm)[0]; j++ {
				if b := (codebook{n, getPulses(j)}); !slices.Contains(books, b) {
					books = append(books, b)
				}
			}
		}
	}
	return books
}

func TestPVQTableHoldsEveryCodebookThePulseCacheReaches(t *testing.T) {
	const saturated = math.MaxUint32 + 1
	widest := bandWidth(nbBands-1) << frameLM
	if pvqMaxDim != widest {
		t.Fatalf("the table reaches %d dimensions, want the widest band's %d bins", pvqMaxDim, widest)
	}
	exact := make([][]uint64, widest+1)
	for n := range exact {
		exact[n] = make([]uint64, widest+2)
		for k := range exact[n] {
			switch {
			case n == 0 || k == 0:
				if n == k {
					exact[n][k] = 1
				}
			default:
				exact[n][k] = min(saturated, exact[n-1][k]+exact[n][k-1]+exact[n-1][k-1])
			}
		}
	}
	u := &sharedTables().pvqU
	books := reachableCodebooks()
	if len(books) < 100 {
		t.Fatalf("the pulse cache reaches %d codebooks, want the mode's hundreds", len(books))
	}
	for _, b := range books {
		size := exact[b.n][b.k] + exact[b.n][b.k+1]
		if size >= saturated {
			t.Errorf("%d pulses in %d dimensions: %d vectors do not index in 32 bits", b.k, b.n, size)
			continue
		}
		if min(b.n, b.k+1) >= pvqRows || max(b.n, b.k+1) > pvqMaxDim {
			t.Errorf("%d pulses in %d dimensions are outside the table", b.k, b.n)
			continue
		}
		if got := u.size(b.n, b.k); uint64(got) != size {
			t.Errorf("%d pulses in %d dimensions: the table's size is %d, want %d", b.k, b.n, got, size)
		}
	}
}

func TestPVQIndexNumbersEveryVectorOnce(t *testing.T) {
	u := &sharedTables().pvqU
	for n := 2; n <= 7; n++ {
		for k := 1; k <= 6; k++ {
			size := u.size(n, k)
			seen := make(map[string]bool, size)
			iy := make([]int, n)
			for idx := range size {
				yy := u.vector(iy, k, idx)
				pulses, norm := 0, 0
				key := make([]byte, n)
				for d, v := range iy {
					pulses += abs(v)
					norm += v * v
					key[d] = byte(v)
				}
				if pulses != k || yy != float32(norm) {
					t.Fatalf("n %d k %d: index %d is %v with squared norm %v", n, k, idx, iy, yy)
				}
				if seen[string(key)] {
					t.Fatalf("n %d k %d: index %d repeats %v", n, k, idx, iy)
				}
				seen[string(key)] = true
				if got := u.index(iy); got != idx {
					t.Fatalf("n %d k %d: %v has index %d, want %d", n, k, iy, got, idx)
				}
			}
		}
	}
}

func TestPVQVectorInvertsIndexInEveryReachableCodebook(t *testing.T) {
	u := &sharedTables().pvqU
	seed := uint32(11)
	random := func(n int) int {
		seed = 1664525*seed + 1013904223
		return int(seed>>8) % n
	}
	for _, b := range reachableCodebooks() {
		want := make([]int, b.n)
		got := make([]int, b.n)
		for range 8 {
			clear(want)
			for range b.k {
				want[random(b.n)]++
			}
			norm := 0
			for d, v := range want {
				norm += v * v
				if random(2) == 1 {
					want[d] = -v
				}
			}
			idx := u.index(want)
			if size := u.size(b.n, b.k); idx >= size {
				t.Fatalf("n %d k %d: index %d of %v is past the codebook's %d", b.n, b.k, idx, want, size)
			}
			if yy := u.vector(got, b.k, idx); !slices.Equal(got, want) || yy != float32(norm) {
				t.Fatalf("n %d k %d: index %d decodes to %v (squared norm %v), want %v (%d)", b.n, b.k, idx, got, yy, want, norm)
			}
		}
	}
}

func TestRotate1RotatesEachPairForwardThenBackward(t *testing.T) {
	noise := testNoise(13)
	for stride := 1; stride <= 5; stride++ {
		for n := range 48 {
			c, s := noise.next(), noise.next()
			got := make([]float32, n)
			for i := range got {
				got[i] = noise.next()
			}
			want := slices.Clone(got)
			pair := func(i int) {
				x1, x2 := want[i], want[i+stride]
				want[i+stride] = float32(c*x2) + float32(s*x1)
				want[i] = float32(c*x1) - float32(s*x2)
			}
			for i := range n - stride {
				pair(i)
			}
			for i := n - 2*stride - 1; i >= 0; i-- {
				pair(i)
			}
			rotate1(got, stride, c, s)
			if !slices.Equal(got, want) {
				t.Fatalf("%d samples at stride %d: %v, want %v", n, stride, got, want)
			}
		}
	}
}
