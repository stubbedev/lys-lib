// Package mp4audio holds what the readers and writers of an MPEG-4
// AudioSpecificConfig (ISO/IEC 14496-3, 1.6.2.1) share: the sampling
// frequency indices.
package mp4audio

import "slices"

// ExplicitRate is the sampling frequency index that announces a 24-bit
// rate in Hz in the stream instead of a rate of the table.
const ExplicitRate = 15

var sampleRates = [...]int{96000, 88200, 64000, 48000, 44100, 32000, 24000, 22050, 16000, 12000, 11025, 8000, 7350}

// SampleRateIndices is the number of sampling frequency indices that
// name a rate.
const SampleRateIndices = len(sampleRates)

// SampleRate is the rate in Hz of a sampling frequency index; ok is
// false for ExplicitRate and the reserved indices.
func SampleRate(index int) (rate int, ok bool) {
	if index < 0 || index >= len(sampleRates) {
		return 0, false
	}
	return sampleRates[index], true
}

// SampleRateIndex is the sampling frequency index of a rate in Hz; ok
// is false for a rate outside the table, which a config announces with
// ExplicitRate.
func SampleRateIndex(rate int) (index int, ok bool) {
	index = slices.Index(sampleRates[:], rate)
	return index, index >= 0
}
