package opus

import (
	"math"

	"github.com/stubbedev/lys-lib/third_party/gomath"
)

// The spreading levels a frame signals (RFC 6716 section 4.3.4.3).
const (
	spreadNone = iota
	spreadLight
	spreadNormal
	spreadAggressive
)

// spreadFactor is each spreading level's rotation strength.
var spreadFactor = [4]int{0, 15, 10, 5}

// pvq codes pulse vectors: the codebook sizes and the pulse search's
// scratch for the widest band.
type pvq struct {
	u    *pvqTable
	y    [FrameSize]float32
	iy   [FrameSize]int
	sign [FrameSize]bool
}

// rotate is exp_rotation in the forward direction: it spreads the
// energy of each of the blocks interleaved in x before the pulse search,
// so sparse pulse vectors reconstruct as less tonal shapes, which the
// decoder undoes.
func rotate(x []float32, k, spread, blocks int) {
	c, s, stride2, ok := rotation(len(x), k, spread, blocks)
	if !ok {
		return
	}
	n := len(x) / blocks
	for b := range blocks {
		block := x[b*n : (b+1)*n]
		rotate1(block, 1, c, -s)
		if stride2 != 0 {
			rotate1(block, stride2, s, -c)
		}
	}
}

// unrotate is exp_rotation in the inverse direction, the decoder's
// undoing of rotate.
func unrotate(x []float32, k, spread, blocks int) {
	c, s, stride2, ok := rotation(len(x), k, spread, blocks)
	if !ok {
		return
	}
	n := len(x) / blocks
	for b := range blocks {
		block := x[b*n : (b+1)*n]
		if stride2 != 0 {
			rotate1(block, stride2, s, c)
		}
		rotate1(block, 1, c, s)
	}
}

// rotation is the spreading rotation of n coefficients in blocks
// holding k pulses: its cosine and sine, the second pass's stride, and
// false when the vector is not rotated.
func rotation(n, k, spread, blocks int) (c, s float32, stride2 int, ok bool) {
	if 2*k >= n || spread == spreadNone {
		return 0, 0, 0, false
	}
	gain := float32(n) / float32(n+spreadFactor[spread]*k)
	theta := float32(0.5 * gain * gain)
	c = float32(gomath.Cos(0.5 * math.Pi * float64(theta)))
	s = float32(gomath.Cos(0.5 * math.Pi * float64(1-theta)))
	if n >= 8*blocks {
		stride2 = 1
		for (stride2*stride2+stride2)*blocks+blocks>>2 < n {
			stride2++
		}
	}
	return c, s, stride2, true
}

// rotate1 applies the Givens rotation (c, s) between samples stride
// apart, forward and then backward over x.
func rotate1(x []float32, stride int, c, s float32) {
	n := len(x)
	if stride == 1 {
		rotateAdjacent(x, c, s)
		return
	}
	if n <= stride {
		return
	}
	lo := x[:n-stride]
	hi := x[stride:][:len(lo)]
	for i, x2 := range hi {
		x1 := lo[i]
		hi[i] = float32(c*x2) + float32(s*x1)
		lo[i] = float32(c*x1) - float32(s*x2)
	}
	if n <= 2*stride {
		return
	}
	lo = x[:n-2*stride]
	hi = x[stride:][:len(lo)]
	for i := len(lo) - 1; i >= 0; i-- {
		x1, x2 := lo[i], hi[i]
		hi[i] = float32(c*x2) + float32(s*x1)
		lo[i] = float32(c*x1) - float32(s*x2)
	}
}

// rotateAdjacent is rotate1 at stride 1, where each rotation takes the
// sample the one before it wrote: that sample stays in a register
// instead of going through memory.
func rotateAdjacent(x []float32, c, s float32) {
	n := len(x)
	if n < 2 {
		return
	}
	x1 := x[0]
	for i, x2 := range x[1:] {
		x[i] = float32(c*x1) - float32(s*x2)
		x1 = float32(c*x2) + float32(s*x1)
	}
	x[n-1] = x1
	x2 := x[n-2]
	for i := n - 3; i >= 0; i-- {
		x1 := x[i]
		x[i+1] = float32(c*x2) + float32(s*x1)
		x2 = float32(c*x1) - float32(s*x2)
	}
	x[0] = x2
}

// quant is alg_quant: it finds the k-pulse vector closest in direction
// to x (blocks interleaved short blocks) and codes its index. x is
// clobbered.
func (p *pvq) quant(rc *rangeEncoder, x []float32, k, spread, blocks int) {
	n := len(x)
	rotate(x, k, spread, blocks)
	y, iy, sign := p.y[:n], p.iy[:n], p.sign[:n]
	for j, v := range x {
		negative := v <= 0
		sign[j] = negative
		x[j] = math.Float32frombits(math.Float32bits(v) ^ b2u(negative)<<31)
		iy[j] = 0
		y[j] = 0
	}
	var xy, yy float32
	left := k
	if k > n>>1 {
		var sum float32
		for _, v := range x {
			sum += v
		}
		if !(sum > 1e-15 && sum < 64) {
			x[0] = 1
			clear(x[1:])
			sum = 1
		}
		rcp := float32(k-1) * (1 / sum)
		for j, v := range x {
			iy[j] = int(float32(rcp * v))
			y[j] = float32(iy[j])
			yy += float32(y[j] * y[j])
			xy += float32(v * y[j])
			y[j] *= 2
			left -= iy[j]
		}
	}
	if left > n+3 {
		t := float32(left)
		yy += float32(t*t) + float32(t*y[0])
		iy[0] += left
		left = 0
	}
	for range left {
		best := 0
		bestNum := float32(-1e15)
		bestDen := float32(0)
		yy++
		for j := 0; ; j++ {
			for ; j < len(x); j++ {
				if num, den := pulseGain(xy, yy, x[j], y[j]); bestDen*num > den*bestNum {
					break
				}
			}
			if j == len(x) {
				break
			}
			bestNum, bestDen = pulseGain(xy, yy, x[j], y[j])
			best = j
		}
		xy += x[best]
		yy += y[best]
		y[best] += 2
		iy[best]++
	}
	for j, v := range iy {
		iy[j] = signed(v, sign[j])
	}
	rc.uint(p.u.index(iy), p.u.size(n, k))
}

// pulseGain is what one more pulse does at a position of magnitude v
// that holds y/2 of them, given the vector's correlation xy with the
// band and its energy yy with the pulse counted: the squared
// correlation and the energy, whose ratio the search maximises. The
// search computes it again for a position that wins instead of keeping
// every position's, which leaves its scan nothing to save.
func pulseGain(xy, yy, v, y float32) (num, den float32) {
	rxy := xy + v
	return rxy * rxy, yy + y
}

const (
	pvqRows   = 15
	pvqMaxDim = 22 << frameLM
)

// pvqTable is U(N,K), the number of pulse vectors of N dimensions and
// K pulses whose first pulse is positive, for the smaller of the two
// below pvqRows.
type pvqTable [pvqRows][pvqMaxDim + 1]uint32

func (u *pvqTable) fill() {
	u[0][0] = 1
	for n := 1; n < pvqRows; n++ {
		for k := 1; k <= pvqMaxDim; k++ {
			u[n][k] = u[n-1][k] + u[n][k-1] + u[n-1][k-1]
		}
	}
}

func (u *pvqTable) count(n, k int) uint32 { return u[min(n, k)][max(n, k)] }

func (u *pvqTable) size(n, k int) uint32 { return u.count(n, k) + u.count(n, k+1) }

// index is icwrs: the index of the pulse vector iy, of at least two
// dimensions, in its codebook.
func (u *pvqTable) index(iy []int) uint32 {
	n := len(iy)
	j := n - 1
	var idx uint32
	if iy[j] < 0 {
		idx = 1
	}
	k := abs(iy[j])
	for j > 0 {
		j--
		idx += u.count(n-j, k)
		k += abs(iy[j])
		if iy[j] < 0 {
			idx += u.count(n-j, k+1)
		}
	}
	return idx
}

func signed(v int, negative bool) int {
	flip := -b2i(negative)
	return (v ^ flip) - flip
}

// vector is cwrsi: it writes the k-pulse vector of index idx into iy,
// of at least two dimensions, and returns its squared norm.
func (u *pvqTable) vector(iy []int, k int, idx uint32) float32 {
	yy := 0
	n := len(iy)
	for ; n > 2; n-- {
		var v int
		switch {
		case k >= n:
			row := &u[n]
			p := row[k+1]
			negative := idx >= p
			idx -= p & -b2u(negative)
			k0 := k
			if row[n] > idx {
				for k, p = n-1, u[n-1][n]; p > idx; p = u[k][n] {
					k--
				}
			} else {
				for p = row[k]; p > idx; p = row[k] {
					k--
				}
			}
			idx -= p
			v = signed(k0-k, negative)
		default:
			p, q := u[k][n], u[k+1][n]
			if p <= idx && idx < q {
				idx -= p
				break
			}
			negative := idx >= q
			idx -= q & -b2u(negative)
			k0 := k
			for k, p = k-1, u[k-1][n]; p > idx; p = u[k][n] {
				k--
			}
			idx -= p
			v = signed(k0-k, negative)
		}
		iy[len(iy)-n] = v
		yy += v * v
	}
	p := u.count(2, k+1)
	negative := idx >= p
	idx -= p & -b2u(negative)
	k0 := k
	k = int((idx + 1) >> 1)
	idx -= u.count(2, k)
	v := signed(k0-k, negative)
	iy[len(iy)-2] = v
	yy += v * v
	v = signed(k, idx != 0)
	iy[len(iy)-1] = v
	return float32(yy + v*v)
}

// unquant is alg_unquant: it decodes a k-pulse shape into x scaled to
// norm gain, undoes the spreading and returns which of the blocks
// interleaved short blocks got pulses.
func (p *pvq) unquant(rc *rangeDecoder, x []float32, k, spread, blocks int, gain float32) uint {
	n := len(x)
	iy := p.iy[:n]
	ryy := p.u.vector(iy, k, rc.uint(p.u.size(n, k)))
	g := gain / float32(math.Sqrt(float64(ryy)))
	for i, v := range iy {
		x[i] = g * float32(v)
	}
	unrotate(x, k, spread, blocks)
	if blocks <= 1 {
		return 1
	}
	n0 := n / blocks
	var mask uint
	for b := range blocks {
		for _, v := range iy[b*n0 : (b+1)*n0] {
			if v != 0 {
				mask |= 1 << b
				break
			}
		}
	}
	return mask
}

// renormalise scales x to norm gain.
func renormalise(x []float32, gain float32) {
	e := float32(1e-15)
	for _, v := range x {
		e += float32(v * v)
	}
	g := gain / float32(math.Sqrt(float64(e)))
	for i := range x {
		x[i] *= g
	}
}
