package opus

import "math"

// softClip is opus_pcm_soft_clip on samples it first scales: it brings
// x (one packet's interleaved samples) to [-1, 1] scale by scale and
// bends every excursion past full scale back under it with the
// quadratic x + a*x², from the zero crossing before the peak to the
// one after, so loud decoded audio saturates smoothly instead of
// clipping. mem carries each channel's curve into the next packet.
func softClip(x []float32, scale float32, channels int, mem *[2]float32) {
	var peak uint32
	for i, v := range x {
		v *= scale
		x[i] = v
		peak = max(peak, absBits(v))
	}
	if peak <= math.Float32bits(1) && mem[0] == 0 && mem[1] == 0 {
		return
	}
	for i, v := range x {
		x[i] = max(-2, min(2, v))
	}
	n := len(x) / channels
	for c := range channels {
		at := func(i int) *float32 { return &x[i*channels+c] }
		bend := func(i int, a float32) {
			v := *at(i)
			*at(i) = v + float32(float32(a*v)*v)
		}
		a := mem[c]
		for i := range n {
			if float32(*at(i)*a) >= 0 {
				break
			}
			bend(i, a)
		}
		curr := 0
		x0 := *at(0)
		for {
			i := curr
			for i < n && *at(i) <= 1 && *at(i) >= -1 {
				i++
			}
			if i == n {
				a = 0
				break
			}
			peak := i
			start, end := i, i
			maxval := abs32(*at(i))
			for start > 0 && float32(*at(i)**at(start - 1)) >= 0 {
				start--
			}
			for end < n && float32(*at(i)**at(end)) >= 0 {
				if v := abs32(*at(end)); v > maxval {
					maxval, peak = v, end
				}
				end++
			}
			special := start == 0 && float32(*at(i)**at(0)) >= 0
			a = (maxval - 1) / float32(maxval*maxval)
			a += float32(a * 2.4e-7)
			if *at(i) > 0 {
				a = -a
			}
			for j := start; j < end; j++ {
				bend(j, a)
			}
			if special && peak >= 2 {
				offset := x0 - *at(0)
				delta := offset / float32(peak)
				for j := curr; j < peak; j++ {
					offset -= delta
					*at(j) = max(-1, min(1, *at(j)+offset))
				}
			}
			curr = end
			if curr == n {
				break
			}
		}
		mem[c] = a
	}
}
