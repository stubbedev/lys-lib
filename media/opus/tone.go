package opus

import "math"

// tone is what detectTone learns of a frame dominated by one sinusoid.
type tone struct {
	// freq is the tone's frequency in radians per sample, -1 for none.
	freq float32
	// strength is the squared radius of the fitted resonance; it
	// approaches 1 for a pure tone.
	strength float32
}

// detectTone is libopus's tone_detect: it fits a two-pole resonator to
// the channels' history and frame (x[c], downmixed) by least squares,
// decimating while the fit resonates too close to DC. A pure or nearly
// pure tone would otherwise mislead the transient detector, the pitch
// search and the TF analysis. mix is scratch of len(x[c]).
func detectTone(x [][]float32, mix []float32) tone {
	copy(mix, x[0])
	if len(x) == 2 {
		for i, v := range x[1] {
			mix[i] += v
		}
	}
	delay := 1
	lpc, ok := toneLPC(mix, delay)
	for delay <= SampleRate/3000 && (!ok || (lpc[0] > 1 && lpc[1] < 0)) {
		delay *= 2
		lpc, ok = toneLPC(mix, delay)
	}
	if !ok || lpc[0]*lpc[0]+3.999999*lpc[1] >= 0 {
		return tone{freq: -1}
	}
	return tone{
		freq:     float32(math.Acos(float64(0.5*lpc[0]))) / float32(delay),
		strength: -lpc[1],
	}
}

// toneLPC is tone_lpc: the second-order predictor x[i] ≈ a·x[i-delay] +
// b·x[i-2·delay] fitted forwards and backwards; ok is false when the
// system is too ill-conditioned to solve.
func toneLPC(x []float32, delay int) (lpc [2]float32, ok bool) {
	n := len(x)
	var r00, r01, r02 float32
	for i := range n - 2*delay {
		r00 += x[i] * x[i]
		r01 += x[i] * x[i+delay]
		r02 += x[i] * x[i+2*delay]
	}
	var e11, e22, e12 float32
	for i := range delay {
		e11 += x[n+i-2*delay]*x[n+i-2*delay] - x[i]*x[i]
		e22 += x[n+i-delay]*x[n+i-delay] - x[i+delay]*x[i+delay]
		e12 += x[n+i-2*delay]*x[n+i-delay] - x[i]*x[i+delay]
	}
	r11 := r00 + e11
	r22 := r11 + e22
	r12 := r01 + e12
	r00, r01, r11, r02, r12 = r00+r22, r01+r12, 2*r11, 2*r02, r12+r01
	den := r00*r11 - r01*r01
	if den < 0.001*r00*r11 {
		return lpc, false
	}
	num1 := r02*r11 - r01*r12
	switch {
	case num1 >= den:
		lpc[1] = 1
	case num1 <= -den:
		lpc[1] = -1
	default:
		lpc[1] = num1 / den
	}
	num0 := r00*r12 - r02*r01
	switch {
	case 0.5*num0 >= den:
		lpc[0] = 1.999999
	case 0.5*num0 <= -den:
		lpc[0] = -1.999999
	default:
		lpc[0] = num0 / den
	}
	return lpc, true
}
