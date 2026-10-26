package opus

import "math"

// transientInvTable is 6*64/x, trained on real data to minimise the
// average error of the harmonic mean below.
var transientInvTable = [128]uint8{
	255, 255, 156, 110, 86, 70, 59, 51, 45, 40, 37, 33, 31, 28, 26, 25,
	23, 22, 21, 20, 19, 18, 17, 16, 16, 15, 15, 14, 13, 13, 12, 12,
	12, 12, 11, 11, 11, 10, 10, 10, 9, 9, 9, 9, 9, 9, 8, 8,
	8, 8, 8, 7, 7, 7, 7, 7, 7, 6, 6, 6, 6, 6, 6, 6,
	6, 6, 6, 6, 6, 6, 6, 6, 6, 5, 5, 5, 5, 5, 5, 5,
	5, 5, 5, 5, 5, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4,
	4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 3, 3,
	3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 2,
}

// transient is the outcome of the time-domain transient analysis.
type transient struct {
	detected bool
	// estimate grows from 0 to 1 with the onset's strength; it biases
	// the TF analysis, the trim and the VBR target.
	estimate float32
	// channel is the channel with the strongest onset, the one the TF
	// analysis looks at.
	channel int
}

// analyseTransient is libopus's transient_analysis: per channel it
// high-passes the frame (its overlap and samples, x[c]), follows the
// energy with forward (6.7 dB/ms) and backward (13.9 dB/ms) masking,
// and compares the frame's energy with the harmonic mean of that
// envelope, a bitrate-normalised temporal noise-to-mask ratio. tmp is
// scratch of len(x[c]). The partial cycle of a strong tone below 200 Hz
// is not a transient.
func analyseTransient(x [][]float32, tn tone, tmp []float32) transient {
	const forwardDecay = float32(0.0625)
	n := len(tmp)
	n2 := n / 2
	var t transient
	maskMetric := 0
	for c, in := range x {
		var mem0, mem1 float32
		for i, v := range in {
			y := mem0 + v
			mem0 = mem1 + y - 2*v
			mem1 = v - 0.5*y
			tmp[i] = y
		}
		clear(tmp[:12])
		var mean float32
		mem0 = 0
		for i := range n2 {
			x2 := tmp[2*i]*tmp[2*i] + tmp[2*i+1]*tmp[2*i+1]
			mean += x2
			mem0 = x2 + (1-forwardDecay)*mem0
			tmp[i] = forwardDecay * mem0
		}
		mem0 = 0
		var maxE float32
		for i := n2 - 1; i >= 0; i-- {
			mem0 = tmp[i] + 0.875*mem0
			tmp[i] = 0.125 * mem0
			maxE = max(maxE, tmp[i])
		}
		mean = sqrt32(mean * maxE * 0.5 * float32(n2))
		norm := float32(n2) / (1e-15 + mean)
		unmask := 0
		for i := 12; i < n2-5; i += 4 {
			id := max(0, min(127, int(math.Floor(float64(64*norm*(tmp[i]+1e-15))))))
			unmask += int(transientInvTable[id])
		}
		unmask = 64 * unmask * 4 / (6 * (n2 - 17))
		if unmask > maskMetric {
			t.channel = c
			maskMetric = unmask
		}
	}
	t.detected = maskMetric > 200
	if tn.strength > 0.98 && tn.freq < 0.026 {
		t.detected = false
		maskMetric = 0
	}
	tfMax := max(0, sqrt32(float32(27*maskMetric))-42)
	t.estimate = sqrt32(max(0, 0.0069*min(163, tfMax)-0.139))
	return t
}

// energyJumped is patch_transient_decision: it reports a mean rise of
// over 6 dB per band from the previous frame's energies old, spread by
// -6 dB per band so that rises in irrelevant bands do not count.
func energyJumped(channels int, newE, old *[2][nbBands]float32) bool {
	var spread [nbBands]float32
	spread[0] = old[0][0]
	if channels == 2 {
		spread[0] = max(old[0][0], old[1][0])
	}
	for i := 1; i < nbBands; i++ {
		o := old[0][i]
		if channels == 2 {
			o = max(o, old[1][i])
		}
		spread[i] = max(spread[i-1]-1, o)
	}
	for i := nbBands - 2; i >= 0; i-- {
		spread[i] = max(spread[i], spread[i+1]-1)
	}
	var meanDiff float32
	for c := range channels {
		for i := 2; i < nbBands-1; i++ {
			meanDiff += max(0, max(0, newE[c][i])-max(0, spread[i]))
		}
	}
	meanDiff /= float32(channels * (nbBands - 3))
	return meanDiff > 1
}

func sqrt32(v float32) float32 { return float32(math.Sqrt(float64(v))) }
