// Package isobmff names the ISO base media file format (MP4) flag
// bits shared by the muxer that writes fragments and the demuxer that
// reads them (ISO/IEC 14496-12).
package isobmff

// The tfhd flags.
const (
	TfhdBaseDataOffset        = 0x000001
	TfhdSampleDescription     = 0x000002
	TfhdDefaultSampleDuration = 0x000008
	TfhdDefaultSampleSize     = 0x000010
	TfhdDefaultSampleFlags    = 0x000020
	TfhdDefaultBaseIsMoof     = 0x020000
)

// The trun flags.
const (
	TrunDataOffset       = 0x000001
	TrunFirstSampleFlags = 0x000004
	TrunSampleDuration   = 0x000100
	TrunSampleSize       = 0x000200
	TrunSampleFlags      = 0x000400
	TrunSampleCTSOffset  = 0x000800
)

// The sample flags of a trun, trex or tfhd.
const (
	SampleDependsOnOthers = 0x01000000
	SampleDependsOnNone   = 0x02000000
	SampleNonSync         = 0x00010000
)
