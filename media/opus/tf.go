package opus

// tfSelectTable is the TF change a band's coded bit and the frame's
// tf_select give 20 ms frames, indexed 4*transient + 2*tfSelect + bit:
// positive values trade short blocks for frequency resolution, negative
// ones split a long block for time resolution.
var tfSelectTable = [8]int{0, -2, 0, -3, 3, 0, 1, -1}

// tfIndex is tfSelectTable's index for a band's coded bit.
func tfIndex(transient bool, tfSelect, bit int) int {
	return 4*b2i(transient) + 2*tfSelect + bit
}

// tfAnalysis is libopus's tf_analysis: per band of the transient
// channel's shape x it finds the Haar depth with the smallest L1 norm,
// biased towards frequency resolution by the transient estimate, then
// picks per band bits (into res) and tf_select by a Viterbi search
// weighing each band by its importance against lambda per change.
func tfAnalysis(x []float32, transient bool, tfEstimate float32, lambda int, importance *[nbBands]int, res *[nbBands]int, tmp, tmp1 []float32) int {
	bias := 0.04 * max(-0.25, 0.5-tfEstimate)
	var metric [nbBands]int
	lmT := 0
	if transient {
		lmT = frameLM
	}
	for i := range nbBands {
		n := bandWidth(i) << frameLM
		narrow := bandWidth(i) == 1
		band := tmp[:n]
		copy(band, x[bandStart(i):bandStart(i+1)])
		bestL1 := l1Metric(band, lmT, bias)
		bestLevel := 0
		if transient && !narrow {
			split := tmp1[:n]
			copy(split, band)
			haar1(split, n>>frameLM, 1<<frameLM)
			if l1 := l1Metric(split, frameLM+1, bias); l1 < bestL1 {
				bestL1 = l1
				bestLevel = -1
			}
		}
		levels := frameLM
		if !transient && !narrow {
			levels++
		}
		for k := range levels {
			b := k + 1
			if transient {
				b = frameLM - k - 1
			}
			haar1(band, n>>k, 1<<k)
			if l1 := l1Metric(band, b, bias); l1 < bestL1 {
				bestL1 = l1
				bestLevel = k + 1
			}
		}
		metric[i] = -2 * bestLevel
		if transient {
			metric[i] = 2 * bestLevel
		}
		if narrow && (metric[i] == 0 || metric[i] == -2*frameLM) {
			metric[i]--
		}
	}
	cost := func(i, sel, bit int) int {
		return importance[i] * abs(metric[i]-2*tfSelectTable[tfIndex(transient, sel, bit)])
	}
	start1 := 0
	if !transient {
		start1 = lambda
	}
	var selCost [2]int
	for sel := range 2 {
		cost0 := cost(0, sel, 0)
		cost1 := cost(0, sel, 1) + start1
		for i := 1; i < nbBands; i++ {
			curr0 := min(cost0, cost1+lambda)
			curr1 := min(cost0+lambda, cost1)
			cost0 = curr0 + cost(i, sel, 0)
			cost1 = curr1 + cost(i, sel, 1)
		}
		selCost[sel] = min(cost0, cost1)
	}
	tfSelect := 0
	if selCost[1] < selCost[0] && transient {
		tfSelect = 1
	}
	var path0, path1 [nbBands]int
	cost0 := cost(0, tfSelect, 0)
	cost1 := cost(0, tfSelect, 1) + start1
	for i := 1; i < nbBands; i++ {
		from0, from1 := cost0, cost1+lambda
		curr0 := from1
		path0[i] = 1
		if from0 < from1 {
			curr0 = from0
			path0[i] = 0
		}
		from0, from1 = cost0+lambda, cost1
		curr1 := from1
		path1[i] = 1
		if from0 < from1 {
			curr1 = from0
			path1[i] = 0
		}
		cost0 = curr0 + cost(i, tfSelect, 0)
		cost1 = curr1 + cost(i, tfSelect, 1)
	}
	res[nbBands-1] = b2i(cost0 >= cost1)
	for i := nbBands - 2; i >= 0; i-- {
		if res[i+1] == 1 {
			res[i] = path1[i+1]
		} else {
			res[i] = path0[i+1]
		}
	}
	return tfSelect
}

// l1Metric is a band's L1 norm, inflated by bias per level of time
// resolution so that ties favour frequency resolution.
func l1Metric(x []float32, lm int, bias float32) float32 {
	var l1 float32
	for _, v := range x {
		l1 += abs32(v)
	}
	return l1 + float32(lm)*bias*l1
}

// encodeTF is tf_encode: it codes the per-band bits in res as changes
// from band to band and tf_select when it matters, then turns res into
// each band's TF change.
func encodeTF(rc *rangeEncoder, transient bool, tfSelect int, res *[nbBands]int) {
	budget := len(rc.buf) * 8
	tell := rc.tell()
	logp := 4
	if transient {
		logp = 2
	}
	selectRsv := tell+logp+1 <= budget
	if selectRsv {
		budget--
	}
	curr, changed := 0, 0
	for i := range nbBands {
		if tell+logp <= budget {
			rc.bitLogp(res[i] != curr, uint(logp))
			tell = rc.tell()
			curr = res[i]
			changed |= curr
		} else {
			res[i] = curr
		}
		logp = 5
		if transient {
			logp = 4
		}
	}
	if selectRsv && tfSelectTable[tfIndex(transient, 0, changed)] != tfSelectTable[tfIndex(transient, 1, changed)] {
		rc.bitLogp(tfSelect == 1, 1)
	} else {
		tfSelect = 0
	}
	for i := range res {
		res[i] = tfSelectTable[tfIndex(transient, tfSelect, res[i])]
	}
}
