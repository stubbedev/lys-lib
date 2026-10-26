package opus

import "math"

// lsbDepth is the input's resolution in bits: int16 PCM.
const lsbDepth = 16

// dynamics is what dynallocAnalysis learns about a frame's spectrum.
type dynamics struct {
	// offsets is each band's boost request in allocation quanta.
	offsets [nbBands]int
	// importance weighs each band in the TF analysis.
	importance [nbBands]int
	// spreadWeight weighs each band in the spreading decision by how
	// far it stands above the masking curve.
	spreadWeight [nbBands]int
	// totalBoost is the boost requested in 1/8 bits, which VBR adds to
	// its target.
	totalBoost int
	// maxDepth is the loudest band's height above the noise floor,
	// which caps the VBR target.
	maxDepth float32
}

// dynallocAnalysis is libopus's dynalloc_analysis: it boosts bands that
// stand above a smoothed spectral envelope (logE2 holds the long-block
// energies, which are steadier on transients), estimates a simple
// masking curve for the spreading weights, and the per-band importance.
// It boosts the bands around a pure tone, which the allocation would
// otherwise starve.
func dynallocAnalysis(channels int, logE, logE2 *[2][nbBands]float32, tn tone, transient, vbr, constrained bool, effectiveBytes int) dynamics {
	var d dynamics
	var noiseFloor [nbBands]float32
	for i := range nbBands {
		noiseFloor[i] = 0.0625*float32(logN[i]) + 0.5 + float32(9-lsbDepth) - eMeans[i] + 0.0062*float32((i+5)*(i+5))
	}
	d.maxDepth = -31.9
	for c := range channels {
		for i := range nbBands {
			d.maxDepth = max(d.maxDepth, logE[c][i]-noiseFloor[i])
		}
	}
	var mask, sig [nbBands]float32
	for i := range nbBands {
		mask[i] = logE[0][i] - noiseFloor[i]
		if channels == 2 {
			mask[i] = max(mask[i], logE[1][i]-noiseFloor[i])
		}
	}
	sig = mask
	for i := 1; i < nbBands; i++ {
		mask[i] = max(mask[i], mask[i-1]-2)
	}
	for i := nbBands - 2; i >= 0; i-- {
		mask[i] = max(mask[i], mask[i+1]-3)
	}
	for i := range nbBands {
		smr := sig[i] - max(max(0, d.maxDepth-12), mask[i])
		shift := min(5, max(0, -int(math.Floor(float64(0.5+smr)))))
		d.spreadWeight[i] = 32 >> shift
	}
	if effectiveBytes < 30+5*frameLM {
		for i := range d.importance {
			d.importance[i] = 13
		}
		return d
	}
	var follower [2][nbBands]float32
	last := 0
	for c := range follower[:channels] {
		e := &logE2[c]
		f := &follower[c]
		f[0] = e[0]
		for i := 1; i < nbBands; i++ {
			if e[i] > e[i-1]+0.5 {
				last = i
			}
			f[i] = min(f[i-1]+1.5, e[i])
		}
		for i := last - 1; i >= 0; i-- {
			f[i] = min(f[i], min(f[i+1]+2, e[i]))
		}
		const offset = 1
		for i := 2; i < nbBands-2; i++ {
			f[i] = max(f[i], medianOf5(e[i-2:i+3])-offset)
		}
		m := medianOf3(e[0:3]) - offset
		f[0] = max(f[0], m)
		f[1] = max(f[1], m)
		m = medianOf3(e[nbBands-3:]) - offset
		f[nbBands-2] = max(f[nbBands-2], m)
		f[nbBands-1] = max(f[nbBands-1], m)
		for i := range nbBands {
			f[i] = max(f[i], noiseFloor[i])
		}
	}
	boost := &follower[0]
	for i := range nbBands {
		if channels == 2 {
			follower[1][i] = max(follower[1][i], follower[0][i]-4)
			follower[0][i] = max(follower[0][i], follower[1][i]-4)
			boost[i] = 0.5 * (max(0, logE[0][i]-follower[0][i]) + max(0, logE[1][i]-follower[1][i]))
		} else {
			boost[i] = max(0, logE[0][i]-follower[0][i])
		}
	}
	for i := range nbBands {
		d.importance[i] = int(math.Floor(float64(0.5 + 13*exp2(min(boost[i], 4)))))
	}
	limited := !vbr || (constrained && !transient)
	for i := range nbBands {
		if (!vbr || constrained) && !transient {
			boost[i] *= 0.5
		}
		if i < 8 {
			boost[i] *= 2
		}
		if i >= 12 {
			boost[i] *= 0.5
		}
	}
	if tn.strength > 0.98 {
		bin := int(math.Floor(float64(0.5 + tn.freq*120/math.Pi)))
		for i := range nbBands {
			near := func(d int) bool { return bin >= eBands[i]-d && bin <= eBands[i+1]+d }
			if near(0) {
				boost[i] += 2
			}
			if near(1) {
				boost[i]++
			}
			if near(2) {
				boost[i]++
			}
			if near(3) {
				boost[i] += 0.5
			}
		}
		if bin >= eBands[nbBands] {
			boost[nbBands-1] += 2
			boost[nbBands-2]++
		}
	}
	for i := range nbBands {
		f := min(boost[i], 4)
		width := channels * bandWidth(i) << frameLM
		var quanta, bits int
		switch {
		case width < 6:
			quanta = int(f)
			bits = quanta * width << bitRes
		case width > 48:
			quanta = int(f * 8)
			bits = (quanta * width << bitRes) / 8
		default:
			quanta = int(f * float32(width) / 6)
			bits = quanta * 6 << bitRes
		}
		if limited && (d.totalBoost+bits)>>bitRes>>3 > 2*effectiveBytes/3 {
			ceiling := (2 * effectiveBytes / 3) << bitRes << 3
			d.offsets[i] = ceiling - d.totalBoost
			d.totalBoost = ceiling
			break
		}
		d.offsets[i] = quanta
		d.totalBoost += bits
	}
	return d
}

func medianOf5(x []float32) float32 {
	t0, t1 := min(x[0], x[1]), max(x[0], x[1])
	t3, t4 := min(x[3], x[4]), max(x[3], x[4])
	t2 := x[2]
	if t0 > t3 {
		t3, t1, t4 = t0, t4, t1
	}
	if t2 > t1 {
		if t1 < t3 {
			return min(t2, t3)
		}
		return min(t4, t1)
	}
	if t2 < t3 {
		return min(t1, t3)
	}
	return min(t2, t4)
}

func medianOf3(x []float32) float32 {
	t0, t1 := min(x[0], x[1]), max(x[0], x[1])
	switch {
	case t1 < x[2]:
		return t1
	case t0 < x[2]:
		return x[2]
	}
	return t0
}

// spreadingDecision is spreading_decision: it picks the rotation
// strength from how peaky the band shapes are, weighted by how audible
// each band is and smoothed over frames. With updateTapset it also
// steers the pre-filter's tapset by the high bands' peakiness.
func (e *Encoder) spreadingDecision(weight *[nbBands]int, updateTapset bool) int {
	sum, total, hfSum := 0, 0, 0
	for ch := range e.channels {
		for i := range nbBands {
			x := e.norm[ch][bandStart(i):bandStart(i+1)]
			n := len(x)
			if n <= 8 {
				continue
			}
			var tcount [3]int
			for _, v := range x {
				x2n := v * v * float32(n)
				if x2n < 0.25 {
					tcount[0]++
				}
				if x2n < 0.0625 {
					tcount[1]++
				}
				if x2n < 0.015625 {
					tcount[2]++
				}
			}
			if i > nbBands-4 {
				hfSum += 32 * (tcount[1] + tcount[0]) / n
			}
			sum += weight[i] * (b2i(2*tcount[2] >= n) + b2i(2*tcount[1] >= n) + b2i(2*tcount[0] >= n))
			total += weight[i]
		}
	}
	if updateTapset {
		if hfSum != 0 {
			hfSum /= e.channels * 4
		}
		e.hfAverage = (e.hfAverage + hfSum) >> 1
		hfSum = e.hfAverage
		switch e.tapsetDecision {
		case 2:
			hfSum += 4
		case 0:
			hfSum -= 4
		}
		switch {
		case hfSum > 22:
			e.tapsetDecision = 2
		case hfSum > 18:
			e.tapsetDecision = 1
		default:
			e.tapsetDecision = 0
		}
	}
	sum = sum << 8 / total
	sum = (sum + e.tonalAverage) >> 1
	e.tonalAverage = sum
	sum = (3*sum + ((3-e.spread)<<7 + 64) + 2) >> 2
	switch {
	case sum < 80:
		return spreadAggressive
	case sum < 256:
		return spreadNormal
	case sum < 384:
		return spreadLight
	}
	return spreadNone
}

// allocTrim is libopus's alloc_trim_analysis: it tilts the allocation
// towards low or high bands by the bitrate, the channels' correlation
// (also updating the mid/side saving VBR counts on), the spectral tilt
// and the transient estimate.
func (e *Encoder) allocTrim(equivRate int, tfEstimate float32) int {
	trim := float32(5)
	if equivRate < 64000 {
		trim = 4
	} else if equivRate < 80000 {
		trim = 4 + float32((equivRate-64000)>>10)/16
	}
	if e.channels == 2 {
		corr := func(i int) float32 {
			return dot(e.norm[0][bandStart(i):bandStart(i+1)], e.norm[1][bandStart(i):])
		}
		var sum float32
		for i := range 8 {
			sum += corr(i)
		}
		sum = min(1, abs32(sum/8))
		minXC := sum
		for i := 8; i < e.intensity; i++ {
			minXC = min(minXC, abs32(corr(i)))
		}
		minXC = min(1, minXC)
		logXC := log2(1.001 - sum*sum)
		logXC2 := max(0.5*logXC, log2(1.001-minXC*minXC))
		trim += max(-4, 0.75*logXC)
		e.stereoSaving = min(e.stereoSaving+0.25, -0.5*logXC2)
	}
	var diff float32
	for ch := range e.channels {
		for i := range nbBands - 1 {
			diff += e.logE[ch][i] * float32(2+2*i-nbBands)
		}
	}
	diff /= float32(e.channels * (nbBands - 1))
	trim -= max(-2, min(2, (diff+1)/6))
	trim -= 2 * tfEstimate
	return max(0, min(10, int(math.Floor(float64(0.5+trim)))))
}

// The intensity stereo start band follows the equivalent rate in kb/s
// with hysteresis.
var (
	intensityThresholds = [nbBands]int{1, 2, 3, 4, 5, 6, 7, 8, 16, 24, 36, 44, 50, 56, 62, 67, 72, 79, 88, 106, 134}
	intensityHysteresis = [nbBands]int{1, 1, 1, 1, 1, 1, 1, 2, 2, 2, 2, 2, 2, 2, 3, 3, 4, 5, 6, 8, 8}
)

// intensityBand is hysteresis_decision over intensityThresholds.
func intensityBand(kbps, prev int) int {
	i := 0
	for i < nbBands && kbps >= intensityThresholds[i] {
		i++
	}
	if i > prev && kbps < intensityThresholds[prev]+intensityHysteresis[prev] {
		i = prev
	}
	if i < prev && kbps > intensityThresholds[prev-1]-intensityHysteresis[prev-1] {
		i = prev
	}
	return i
}

// stereoAnalysis reports whether coding left and right apart (dual
// stereo) is cheaper than mid and side, by their L1 norms.
func (e *Encoder) stereoAnalysis() bool {
	sumLR, sumMS := float32(1e-15), float32(1e-15)
	for j := range bandStart(13) {
		l, r := e.norm[0][j], e.norm[1][j]
		sumLR += abs32(l) + abs32(r)
		sumMS += abs32(l+r) + abs32(l-r)
	}
	sumMS *= 0.707107
	const thetas = 13
	width := float32(eBands[13] << (frameLM + 1))
	return (width+thetas)*sumMS > width*sumLR
}

func abs32(v float32) float32 {
	if v < 0 {
		return -v
	}
	return v
}

func log2(v float32) float32 { return float32(math.Log2(float64(v))) }

func exp2(v float32) float32 { return float32(math.Exp2(float64(v))) }
