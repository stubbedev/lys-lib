package opus

import "math"

// The pitch pre-filter: a comb filter that attenuates the harmonics of
// a periodic signal before the MDCT, which the decoder's post-filter
// undoes, so the quantisation noise between harmonics is shaped away.
const (
	combMinPeriod = 15
	combMaxPeriod = 1024
	// pitchBufLen is the 2x-decimated history and frame the pitch
	// search runs over.
	pitchBufLen = (combMaxPeriod + FrameSize) >> 1
)

// combGains are the three-tap filters (centre, ±1, ±2) per tapset.
var combGains = [3][3]float32{
	{0.3066406250, 0.2170410156, 0.1296386719},
	{0.4638671875, 0.2680664062, 0},
	{0.7998046875, 0.1000976562, 0},
}

// tapsetICDF is the inverse CDF of the coded tapset.
var tapsetICDF = []uint8{2, 1, 0}

// pitchFilter is one frame's comb filter: period in samples, gain and
// tapset.
type pitchFilter struct {
	period int
	gain   float32
	tapset int
}

// combFilter is comb_filter: y[i] = x[at+i] plus the taps of x before
// it, cross-fading over the overlap (with the squared window) from
// filter f0 to f1, then f1 alone; window nil means no cross-fade.
func combFilter(y, x []float32, at int, f0, f1 pitchFilter, window []float32) {
	n := len(y)
	if f0.gain == 0 && f1.gain == 0 {
		copy(y, x[at:at+n])
		return
	}
	t0 := max(f0.period, combMinPeriod)
	t1 := max(f1.period, combMinPeriod)
	g0 := [3]float32{f0.gain * combGains[f0.tapset][0], f0.gain * combGains[f0.tapset][1], f0.gain * combGains[f0.tapset][2]}
	g1 := [3]float32{f1.gain * combGains[f1.tapset][0], f1.gain * combGains[f1.tapset][1], f1.gain * combGains[f1.tapset][2]}
	fade := len(window)
	if f0.gain == f1.gain && t0 == t1 && f0.tapset == f1.tapset {
		fade = 0
	}
	for i := range fade {
		j := at + i
		f := window[i] * window[i]
		y[i] = x[j] +
			(1-f)*g0[0]*x[j-t0] +
			(1-f)*g0[1]*(x[j-t0+1]+x[j-t0-1]) +
			(1-f)*g0[2]*(x[j-t0+2]+x[j-t0-2]) +
			f*g1[0]*x[j-t1] +
			f*g1[1]*(x[j-t1+1]+x[j-t1-1]) +
			f*g1[2]*(x[j-t1+2]+x[j-t1-2])
	}
	for i := fade; i < n; i++ {
		j := at + i
		y[i] = x[j] +
			g1[0]*x[j-t1] +
			g1[1]*(x[j-t1+1]+x[j-t1-1]) +
			g1[2]*(x[j-t1+2]+x[j-t1-2])
	}
}

// pitchDownsample is pitch_downsample: it halves the rate of the
// channels' history and frame into lp (summing stereo) and whitens it
// with a fourth-order LPC fit plus a fixed zero.
func pitchDownsample(x [][]float32, lp []float32) {
	clear(lp)
	for _, in := range x {
		lp[0] += 0.25*in[1] + 0.5*in[0]
		for i := 1; i < len(lp); i++ {
			lp[i] += 0.25*in[2*i-1] + 0.25*in[2*i+1] + 0.5*in[2*i]
		}
	}
	var ac [5]float32
	for k := range ac {
		var sum float32
		for i := k; i < len(lp); i++ {
			sum += lp[i] * lp[i-k]
		}
		ac[k] = sum
	}
	ac[0] *= 1.0001
	for i := 1; i < len(ac); i++ {
		w := 0.008 * float32(i)
		ac[i] -= ac[i] * w * w
	}
	var lpc [4]float32
	lpcFromAutocorr(ac[:], lpc[:])
	g := float32(1)
	for i := range lpc {
		g *= 0.9
		lpc[i] *= g
	}
	const c1 = float32(0.8)
	num := [5]float32{lpc[0] + c1, lpc[1] + c1*lpc[0], lpc[2] + c1*lpc[1], lpc[3] + c1*lpc[2], c1 * lpc[3]}
	var mem [5]float32
	for i, v := range lp {
		sum := v
		for k := range num {
			sum += num[k] * mem[k]
		}
		copy(mem[1:], mem[:4])
		mem[0] = v
		lp[i] = sum
	}
}

// lpcFromAutocorr is _celt_lpc: the Levinson-Durbin recursion for the
// predictor lpc (len(ac)-1 coefficients, zero on entry) of
// autocorrelation ac, stopping early at 30 dB of prediction gain.
func lpcFromAutocorr(ac, lpc []float32) {
	if ac[0] <= 1e-10 {
		return
	}
	errE := ac[0]
	for i, next := range ac[1:] {
		var rr float32
		for j, a := range lpc[:i] {
			rr += a * ac[i-j]
		}
		rr += next
		r := -rr / errE
		lpc[i] = r
		for j := range (i + 1) >> 1 {
			a, b := lpc[j], lpc[i-1-j]
			lpc[j] = a + r*b
			lpc[i-1-j] = b + r*a
		}
		errE -= r * r * errE
		if errE <= 0.001*ac[0] {
			return
		}
	}
}

// pitchSearch is pitch_search: the lag (in full-rate samples, up to
// maxPitch) at which y best matches x, both 2x decimated, from a 4x
// decimated coarse search refined around its two best candidates.
func pitchSearch(x, y []float32, n, maxPitch int, xcorr []float32) int {
	var x4 [FrameSize >> 2]float32
	var y4 [(FrameSize + combMaxPeriod) >> 2]float32
	for j := range n >> 2 {
		x4[j] = x[2*j]
	}
	for j := range (n + maxPitch) >> 2 {
		y4[j] = y[2*j]
	}
	coarse := xcorr[:maxPitch>>2]
	for i := range coarse {
		coarse[i] = dot(x4[:n>>2], y4[i:])
	}
	best := findBestPitch(coarse, y4[:], n>>2)
	fine := xcorr[:maxPitch>>1]
	for i := range fine {
		fine[i] = 0
		if abs(i-2*best[0]) > 2 && abs(i-2*best[1]) > 2 {
			continue
		}
		fine[i] = max(-1, dot(x[:n>>1], y[i:]))
	}
	best = findBestPitch(fine, y, n>>1)
	offset := 0
	if b := best[0]; b > 0 && b < len(fine)-1 {
		a, m, c := fine[b-1], fine[b], fine[b+1]
		switch {
		case c-a > 0.7*(m-a):
			offset = 1
		case a-c > 0.7*(m-c):
			offset = -1
		}
	}
	return 2*best[0] - offset
}

// findBestPitch is find_best_pitch: the two lags with the highest
// normalised correlation xcorr[i]^2 / |y[i:i+n]|^2.
func findBestPitch(xcorr, y []float32, n int) [2]int {
	syy := float32(1)
	bestNum := [2]float32{-1, -1}
	var bestDen [2]float32
	best := [2]int{0, 1}
	for _, v := range y[:n] {
		syy += v * v
	}
	for i, xc := range xcorr {
		if xc > 0 {
			xc16 := xc * 1e-12
			num := xc16 * xc16
			if num*bestDen[1] > bestNum[1]*syy {
				if num*bestDen[0] > bestNum[0]*syy {
					bestNum[1], bestDen[1], best[1] = bestNum[0], bestDen[0], best[0]
					bestNum[0], bestDen[0], best[0] = num, syy, i
				} else {
					bestNum[1], bestDen[1], best[1] = num, syy, i
				}
			}
		}
		syy = max(1, syy+y[i+n]*y[i+n]-y[i]*y[i])
	}
	return best
}

// secondCheck is the second sub-multiple remove_doubling checks for
// each divisor k.
var secondCheck = [16]int{0, 0, 3, 2, 3, 2, 5, 2, 3, 2, 3, 2, 5, 2, 3, 2}

// removeDoubling is remove_doubling: it checks the sub-multiples t/k of
// the found period t (full rate) for an equally good, shorter period,
// favouring continuity with the previous filter, and returns the period
// and its gain. x is the 2x decimated history and frame.
func removeDoubling(x []float32, t int, prev pitchFilter, yyLookup []float32) (int, float32) {
	const maxPeriod = combMaxPeriod / 2
	const minPeriod = combMinPeriod / 2
	const n = FrameSize / 2
	t0 := min(t/2, maxPeriod-1)
	prevPeriod := prev.period / 2
	at := maxPeriod
	frame := x[at : at+n]
	lagged := func(lag int) []float32 { return x[at-lag : at-lag+n] }
	xx := dot(frame, frame)
	xy := dot(frame, lagged(t0))
	yy := xx
	yyLookup[0] = xx
	for i := 1; i <= maxPeriod; i++ {
		yy += x[at-i]*x[at-i] - x[at+n-i]*x[at+n-i]
		yyLookup[i] = max(0, yy)
	}
	yy = yyLookup[t0]
	bestXY, bestYY := xy, yy
	g0 := pitchGain(xy, xx, yy)
	g := g0
	tBest := t0
	for k := 2; k <= 15; k++ {
		t1 := (2*t0 + k) / (2 * k)
		if t1 < minPeriod {
			break
		}
		t1b := (2*secondCheck[k]*t0 + k) / (2 * k)
		if k == 2 {
			t1b = t0 + t1
			if t1+t0 > maxPeriod {
				t1b = t0
			}
		}
		xy := 0.5 * (dot(frame, lagged(t1)) + dot(frame, lagged(t1b)))
		yy := 0.5 * (yyLookup[t1] + yyLookup[t1b])
		g1 := pitchGain(xy, xx, yy)
		var cont float32
		switch d := abs(t1 - prevPeriod); {
		case d <= 1:
			cont = prev.gain
		case d <= 2 && 5*k*k < t0:
			cont = 0.5 * prev.gain
		}
		thresh := max(0.3, 0.7*g0-cont)
		if t1 < 3*minPeriod {
			thresh = max(0.4, 0.85*g0-cont)
		}
		if g1 > thresh {
			bestXY, bestYY = xy, yy
			tBest = t1
			g = g1
		}
	}
	bestXY = max(0, bestXY)
	pg := float32(1)
	if bestYY > bestXY {
		pg = bestXY / (bestYY + 1)
	}
	var xc [3]float32
	for k := range xc {
		xc[k] = dot(frame, lagged(tBest+k-1))
	}
	offset := 0
	switch {
	case xc[2]-xc[0] > 0.7*(xc[1]-xc[0]):
		offset = 1
	case xc[0]-xc[2] > 0.7*(xc[1]-xc[2]):
		offset = -1
	}
	return max(combMinPeriod, 2*tBest+offset), min(pg, g)
}

// pitchGain is the normalised correlation of a lag.
func pitchGain(xy, xx, yy float32) float32 {
	return xy / sqrt32(1+xx*yy)
}

func dot(x, y []float32) float32 {
	var sum float32
	for i, v := range x {
		sum += v * y[i]
	}
	return sum
}

// prefilter is run_prefilter: it searches the pitch of the channels'
// history and frame (pre[c], combMaxPeriod+FrameSize samples), or takes
// it from a pure tone, decides whether a comb filter pays off, and
// filters the frame into in[c] behind the previous frame's filtered
// overlap. It returns the filter, its quantised gain and whether it is
// on.
func (e *Encoder) prefilter(enabled bool, tn tone, tfEstimate float32, availableBytes int) (pitchFilter, int, bool) {
	pre := e.pre[:e.channels]
	prev := e.pitch
	f := pitchFilter{period: combMinPeriod, tapset: e.tapsetDecision}
	switch {
	case enabled && tn.strength > 0.99:
		f.period = tonePeriod(tn.freq)
		f.gain = 0.75
	case enabled:
		var xs [2][]float32
		for c := range pre {
			xs[c] = pre[c][:]
		}
		lp := e.pitchBuf[:]
		pitchDownsample(xs[:e.channels], lp)
		lag := pitchSearch(lp[combMaxPeriod>>1:], lp, FrameSize, combMaxPeriod-3*combMinPeriod, e.xcorr[:])
		f.period, f.gain = removeDoubling(lp, combMaxPeriod-lag, prev, e.xcorr[:])
		f.period = min(f.period, combMaxPeriod-2)
		f.gain *= 0.7
	}
	threshold := float32(0.2)
	if abs(f.period-prev.period)*10 > f.period {
		threshold += 0.2
		if tfEstimate > 0.98 {
			f.gain = 0
		}
	}
	if availableBytes < 25 {
		threshold += 0.1
	}
	if availableBytes < 35 {
		threshold += 0.1
	}
	if prev.gain > 0.4 {
		threshold -= 0.1
	}
	if prev.gain > 0.55 {
		threshold -= 0.1
	}
	threshold = max(threshold, 0.2)
	qg := 0
	on := f.gain >= threshold
	if !on {
		f.gain = 0
	} else {
		if abs32(f.gain-prev.gain) < 0.1 {
			f.gain = prev.gain
		}
		qg = max(0, min(7, int(math.Floor(float64(0.5+f.gain*32/3)))-1))
		f.gain = 0.09375 * float32(qg+1)
	}
	prev.period = max(prev.period, combMinPeriod)
	neg := func(p pitchFilter) pitchFilter { p.gain = -p.gain; return p }
	var before, after [2]float32
	for c := range pre {
		out := e.in[c][overlap:]
		before[c] = sumAbs(pre[c][combMaxPeriod:])
		combFilter(out, pre[c][:], combMaxPeriod, neg(prev), neg(f), mdctWindow[:])
		after[c] = sumAbs(out)
	}
	cancel := after[0] > before[0]
	if e.channels == 2 {
		thresh := [2]float32{
			0.25*f.gain*before[0] + 0.01*before[1],
			0.25*f.gain*before[1] + 0.01*before[0],
		}
		cancel = after[0]-before[0] > thresh[0] || after[1]-before[1] > thresh[1] ||
			(before[0]-after[0] < thresh[0] && before[1]-after[1] < thresh[1])
	}
	if cancel && on {
		off := f
		off.gain = 0
		for c := range pre {
			out := e.in[c][overlap:]
			copy(out, pre[c][combMaxPeriod:])
			combFilter(out[:overlap], pre[c][:], combMaxPeriod, neg(prev), off, mdctWindow[:])
		}
		f.gain = 0
		qg = 0
		on = false
	}
	for c := range pre {
		copy(e.in[c][:overlap], e.inMem[c][:])
		copy(e.inMem[c][:], e.in[c][FrameSize:])
		copy(pre[c][:combMaxPeriod], pre[c][FrameSize:])
	}
	return f, qg, on
}

// tonePeriod is the comb filter period for a tone of freq radians per
// sample: its period, or a multiple of it when that is too short for
// the filter, or the shortest period for a tone too low to follow,
// whose filter's DC response then sits near the tone.
func tonePeriod(freq float32) int {
	if freq >= 3.1416 {
		freq = 3.141593 - freq
	}
	multiple := 1
	for freq >= float32(multiple)*0.39 {
		multiple++
	}
	if freq <= 0.006148 {
		return combMinPeriod
	}
	return min(int(math.Floor(0.5+2*math.Pi*float64(multiple)/float64(freq))), combMaxPeriod-2)
}

func sumAbs(x []float32) float32 {
	var sum float32
	for _, v := range x {
		sum += abs32(v)
	}
	return sum
}
