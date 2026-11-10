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

// pvq holds the pulse search's scratch for the widest band.
type pvq struct {
	y    [FrameSize]float32
	iy   [FrameSize]int
	sign [FrameSize]bool
	u    [maxPulses + 2]uint32
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
	for i := range n - stride {
		x1, x2 := x[i], x[i+stride]
		x[i+stride] = float32(c*x2) + float32(s*x1)
		x[i] = float32(c*x1) - float32(s*x2)
	}
	for i := n - 2*stride - 1; i >= 0; i-- {
		x1, x2 := x[i], x[i+stride]
		x[i+stride] = float32(c*x2) + float32(s*x1)
		x[i] = float32(c*x1) - float32(s*x2)
	}
}

// quant is alg_quant: it finds the k-pulse vector closest in direction
// to x (blocks interleaved short blocks) and codes its index. x is
// clobbered.
func (p *pvq) quant(rc *rangeEncoder, x []float32, k, spread, blocks int) {
	n := len(x)
	rotate(x, k, spread, blocks)
	y, iy, sign := p.y[:n], p.iy[:n], p.sign[:n]
	for j, v := range x {
		sign[j] = v <= 0
		if sign[j] {
			x[j] = -v
		}
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
			iy[j] = int(math.Floor(float64(rcp * v)))
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
		for j, v := range x {
			rxy := xy + v
			ryy := yy + y[j]
			rxy *= rxy
			if bestDen*rxy > ryy*bestNum {
				bestDen = ryy
				bestNum = rxy
				best = j
			}
		}
		xy += x[best]
		yy += y[best]
		y[best] += 2
		iy[best]++
	}
	for j := range iy {
		if sign[j] {
			iy[j] = -iy[j]
		}
	}
	p.encodePulses(rc, iy, k)
}

// encodePulses codes the pulse vector iy (k pulses) by its index in the
// PVQ codebook (icwrs over the U(N,K) recurrence).
func (p *pvq) encodePulses(rc *rangeEncoder, iy []int, k int) {
	n := len(iy)
	u := p.u[:k+2]
	u[0] = 0
	for j := 1; j < len(u); j++ {
		u[j] = uint32(2*j - 1)
	}
	var idx uint32
	if iy[n-1] < 0 {
		idx = 1
	}
	kk := abs(iy[n-1])
	j := n - 2
	idx += u[kk]
	kk += abs(iy[j])
	if iy[j] < 0 {
		idx += u[kk+1]
	}
	for j > 0 {
		j--
		unext(u, 0)
		idx += u[kk]
		kk += abs(iy[j])
		if iy[j] < 0 {
			idx += u[kk+1]
		}
	}
	rc.uint(idx, u[kk]+u[kk+1])
}

// unext advances u from row n to row n+1 of the U(N,K) table, with u0
// the new row's first entry.
func unext(u []uint32, u0 uint32) {
	j := 1
	for ; j < len(u); j++ {
		u1 := u[j] + u[j-1] + u0
		u[j-1] = u0
		u0 = u1
	}
	u[j-1] = u0
}

// decodePulses is decode_pulses: it reads a k-pulse vector into iy by
// its codebook index (cwrsi over the U(N,K) recurrence) and returns its
// squared norm.
func (p *pvq) decodePulses(rc *rangeDecoder, iy []int, k int) float32 {
	n := len(iy)
	u := p.u[:k+2]
	u[0], u[1] = 0, 1
	for j := 2; j < len(u); j++ {
		u[j] = uint32(2*j - 1)
	}
	for range n - 2 {
		unext(u[1:], 1)
	}
	idx := rc.uint(u[k] + u[k+1])
	var yy float32
	for j := range n {
		pk := u[k+1]
		neg := idx >= pk
		if neg {
			idx -= pk
		}
		yj := k
		pk = u[k]
		for pk > idx {
			k--
			pk = u[k]
		}
		idx -= pk
		yj -= k
		if neg {
			yj = -yj
		}
		iy[j] = yj
		yy += float32(yj * yj)
		uprev(u[:k+2], 0)
	}
	return yy
}

// uprev steps u from row n back to row n-1 of the U(N,K) table, with
// u0 the new row's first entry.
func uprev(u []uint32, u0 uint32) {
	j := 1
	for ; j < len(u); j++ {
		u1 := u[j] - u[j-1] - u0
		u[j-1] = u0
		u0 = u1
	}
	u[j-1] = u0
}

// unquant is alg_unquant: it decodes a k-pulse shape into x scaled to
// norm gain, undoes the spreading and returns which of the blocks
// interleaved short blocks got pulses.
func (p *pvq) unquant(rc *rangeDecoder, x []float32, k, spread, blocks int, gain float32) uint {
	n := len(x)
	iy := p.iy[:n]
	ryy := p.decodePulses(rc, iy, k)
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
