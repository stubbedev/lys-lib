package opus

import "math"

// bandCoder codes the normalised band shapes of one frame
// (quant_all_bands and quant_band, encoder side). The encoder never
// resynthesises, so the decoder's folding and stereo merging have no
// counterpart here.
type bandCoder struct {
	rc        *rangeEncoder
	pvq       *pvq
	amp       *[2][nbBands]float32
	spread    int
	intensity int
	remaining int
}

// quantAll codes every band of x (and y in stereo) given the
// allocation; totalBits is the frame size in 1/8 bits.
func (q *bandCoder) quantAll(x, y []float32, a *allocation, totalBits int) {
	balance := a.balance
	dual := a.dualStereo
	for i := range nbBands {
		lo, hi := bandStart(i), bandStart(i+1)
		n := hi - lo
		tell := q.rc.tellFrac()
		if i != 0 {
			balance -= tell
		}
		q.remaining = totalBits - tell - 1
		b := 0
		if i <= a.codedBands-1 {
			curr := balance / min(3, a.codedBands-i)
			b = max(0, min(16383, min(q.remaining+1, a.pulses[i]+curr)))
		}
		if dual && i == q.intensity {
			dual = false
		}
		switch {
		case y == nil:
			q.band(i, x[lo:hi], nil, n, b, frameLM)
		case dual:
			q.band(i, x[lo:hi], nil, n, b/2, frameLM)
			q.band(i, y[lo:hi], nil, n, b/2, frameLM)
		default:
			q.band(i, x[lo:hi], y[lo:hi], n, b, frameLM)
		}
		balance += a.pulses[i] + tell
	}
}

// band codes one band (y non-nil for a joint stereo band) with b
// eighth bits, splitting it in halves while that beats one PVQ.
func (q *bandCoder) band(i int, x, y []float32, n, b, lm int) {
	stereo := y != nil
	if n == 1 {
		q.signBit(x)
		if stereo {
			q.signBit(y)
		}
		return
	}
	split := stereo
	if !stereo && lm != -1 && n > 2 {
		cache := cacheRow(i, lm)
		if b > cache[cache[0]]+12 {
			n >>= 1
			y = x[n:]
			x = x[:n]
			split = true
			lm--
		}
	}
	if !split {
		q.pulses(i, x, b, lm)
		return
	}
	pulseCap := logN[i] + lm<<bitRes
	offset := pulseCap >> 1
	if stereo && n == 2 {
		offset -= qthetaTwo
	} else {
		offset -= qthetaOffset
	}
	qn := computeQN(n, b, offset, pulseCap, stereo)
	if stereo && i >= q.intensity {
		qn = 1
	}
	itheta := stereoTheta(x, y, stereo)
	tell := q.rc.tellFrac()
	inv := false
	switch {
	case qn != 1:
		itheta = (itheta*qn + 8192) >> 14
		q.codeTheta(itheta, qn, n, stereo)
		itheta = itheta * 16384 / qn
		if stereo {
			if itheta == 0 {
				q.intensityStereo(i, x, y)
			} else {
				stereoSplit(x, y)
			}
		}
	case stereo:
		inv = itheta > 8192
		if inv {
			for j := range y {
				y[j] = -y[j]
			}
		}
		q.intensityStereo(i, x, y)
		if b > 2<<bitRes && q.remaining > 2<<bitRes {
			q.rc.bitLogp(inv, 2)
		}
		itheta = 0
	}
	qalloc := q.rc.tellFrac() - tell
	b -= qalloc
	var delta int
	switch itheta {
	case 0:
		delta = -16384
	case 16384:
		delta = 16384
	default:
		imid := bitexactCos(itheta)
		iside := bitexactCos(16384 - itheta)
		delta = fracMul16((n-1)<<7, bitexactLog2Tan(iside, imid))
	}
	if n == 2 && stereo {
		sbits := 0
		if itheta != 0 && itheta != 16384 {
			sbits = 1 << bitRes
		}
		mbits := b - sbits
		q.remaining -= qalloc + sbits
		x2, y2 := x, y
		if itheta > 8192 {
			x2, y2 = y, x
		}
		if sbits != 0 {
			q.rc.bits(uint32(b2i(x2[0]*y2[1]-x2[1]*y2[0] < 0)), 1)
		}
		q.band(i, x2, nil, n, mbits, lm)
		return
	}
	mbits := max(0, min(b, (b-delta)/2))
	sbits := b - mbits
	q.remaining -= qalloc
	rebalance := q.remaining
	if mbits >= sbits {
		q.band(i, x, nil, n, mbits, lm)
		rebalance = mbits - (rebalance - q.remaining)
		if rebalance > 3<<bitRes && itheta != 0 {
			sbits += rebalance - 3<<bitRes
		}
		q.band(i, y, nil, n, sbits, lm)
		return
	}
	q.band(i, y, nil, n, sbits, lm)
	rebalance = sbits - (rebalance - q.remaining)
	if rebalance > 3<<bitRes && itheta != 16384 {
		mbits += rebalance - 3<<bitRes
	}
	q.band(i, x, nil, n, mbits, lm)
}

// signBit codes the sign of a one-coefficient band when a bit is left.
func (q *bandCoder) signBit(x []float32) {
	if q.remaining < 1<<bitRes {
		return
	}
	q.rc.bits(uint32(b2i(x[0] < 0)), 1)
	q.remaining -= 1 << bitRes
}

// pulses codes an unsplit band as the most pulses b affords without
// overrunning the frame.
func (q *bandCoder) pulses(i int, x []float32, b, lm int) {
	k := bits2pulses(i, lm, b)
	cost := pulses2bits(i, lm, k)
	q.remaining -= cost
	for q.remaining < 0 && k > 0 {
		q.remaining += cost
		k--
		cost = pulses2bits(i, lm, k)
		q.remaining -= cost
	}
	if k != 0 {
		q.pvq.quant(q.rc, x, getPulses(k), q.spread)
	}
}

// codeTheta codes the quantised split angle: a step PDF for stereo,
// uniform for two-coefficient stereo, triangular for a mono split.
func (q *bandCoder) codeTheta(itheta, qn, n int, stereo bool) {
	switch {
	case stereo && n > 2:
		const p0 = 3
		x0 := qn / 2
		ft := p0*(x0+1) + x0
		if itheta <= x0 {
			q.rc.encode(uint32(p0*itheta), uint32(p0*(itheta+1)), uint32(ft))
		} else {
			fl := (itheta - 1 - x0) + (x0+1)*p0
			q.rc.encode(uint32(fl), uint32(fl+1), uint32(ft))
		}
	case stereo:
		q.rc.uint(uint32(itheta), uint32(qn+1))
	default:
		ft := ((qn >> 1) + 1) * ((qn >> 1) + 1)
		fs := qn + 1 - itheta
		fl := ft - ((qn + 1 - itheta) * (qn + 2 - itheta) >> 1)
		if itheta <= qn>>1 {
			fs = itheta + 1
			fl = itheta * (itheta + 1) >> 1
		}
		q.rc.encode(uint32(fl), uint32(fl+fs), uint32(ft))
	}
}

// intensityStereo folds y into x by the bands' amplitudes; the side is
// not coded.
func (q *bandCoder) intensityStereo(i int, x, y []float32) {
	left, right := q.amp[0][i], q.amp[1][i]
	norm := 1e-15 + float32(math.Sqrt(float64(1e-15+left*left+right*right)))
	a1, a2 := left/norm, right/norm
	for j := range x {
		x[j] = a1*x[j] + a2*y[j]
	}
}

// stereoSplit turns left/right into mid/side.
func stereoSplit(x, y []float32) {
	const r = float32(0.70710678)
	for j := range x {
		l, rr := r*x[j], r*y[j]
		x[j] = l + rr
		y[j] = rr - l
	}
}

// stereoTheta is the angle between the mid and side (stereo) or the
// two halves (a split), in Q14 quarter turns.
func stereoTheta(x, y []float32, stereo bool) int {
	emid, eside := float32(1e-15), float32(1e-15)
	for j := range x {
		m, s := x[j], y[j]
		if stereo {
			m, s = x[j]+y[j], x[j]-y[j]
		}
		emid += m * m
		eside += s * s
	}
	mid := math.Sqrt(float64(emid))
	side := math.Sqrt(float64(eside))
	return int(math.Floor(float64(0.5 + 16384*0.63662*float32(math.Atan2(side, mid)))))
}

// exp2Table8 is 2^(i/8) in Q14.
var exp2Table8 = [8]int{16384, 17866, 19483, 21247, 23170, 25267, 27554, 30048}

// computeQN is the number of steps the split angle is quantised to.
func computeQN(n, b, offset, pulseCap int, stereo bool) int {
	n2 := 2*n - 1
	if stereo && n == 2 {
		n2--
	}
	qb := min(b-pulseCap-(4<<bitRes), (b+n2*offset)/n2)
	qb = min(8<<bitRes, qb)
	if qb < 1<<bitRes>>1 {
		return 1
	}
	qn := exp2Table8[qb&7] >> (14 - qb>>bitRes)
	return (qn + 1) >> 1 << 1
}

// fracMul16 is the reference's Q15 product of two 16-bit values.
func fracMul16(a, b int) int {
	return (16384 + int(int32(int16(a))*int32(int16(b)))) >> 15
}

// bitexactCos is the integer cosine both ends compute bit-exactly, as
// it steers the allocation.
func bitexactCos(x int) int {
	x2 := (4096 + x*x) >> 13
	x2 = (32767 - x2) + fracMul16(x2, -7651+fracMul16(x2, 8277+fracMul16(-626, x2)))
	return 1 + x2
}

// bitexactLog2Tan is log2(isin/icos) in Q11, bit-exactly.
func bitexactLog2Tan(isin, icos int) int {
	lc := ilog(uint32(icos))
	ls := ilog(uint32(isin))
	icos <<= 15 - lc
	isin <<= 15 - ls
	return (ls-lc)*(1<<11) + fracMul16(isin, fracMul16(isin, -2597)+7932) - fracMul16(icos, fracMul16(icos, -2597)+7932)
}
