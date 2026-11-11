// Package pcm converts the samples the audio decoders compute to the
// 16-bit PCM they return.
package pcm

// Int16 rounds v to the nearest int16, halves to even, saturating;
// NaN is 0. Adding 1.5·2²³ to a value within int16 leaves a float32
// with no fraction bits, which rounds it as the conversion must; v is
// converted first so that a product passed in cannot fuse into the sum.
func Int16(v float32) int16 {
	const integral = 3 << 22
	switch {
	case v > -32768 && v < 32767:
		return int16(float32(float32(v)+integral) - integral)
	case v >= 32767:
		return 32767
	case v <= -32768:
		return -32768
	}
	return 0
}

// FullScale is Int16 of a sample whose full scale is 1.
func FullScale(v float32) int16 { return Int16(v * 32768) }
