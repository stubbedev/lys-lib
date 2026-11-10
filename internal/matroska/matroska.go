// Package matroska names the EBML elements and codec identifiers of
// Matroska and WebM, shared by the muxer that writes them and the
// demuxer that reads them.
package matroska

// ID is an EBML element ID, its length marker included.
type ID uint32

// The elements lys-lib writes or reads.
const (
	EBML                 ID = 0x1a45dfa3
	EBMLVersion          ID = 0x4286
	EBMLReadVersion      ID = 0x42f7
	EBMLMaxIDLength      ID = 0x42f2
	EBMLMaxSizeLength    ID = 0x42f3
	DocType              ID = 0x4282
	DocTypeVersion       ID = 0x4287
	DocTypeReadVersion   ID = 0x4285
	Segment              ID = 0x18538067
	SeekHead             ID = 0x114d9b74
	Seek                 ID = 0x4dbb
	SeekID               ID = 0x53ab
	SeekPosition         ID = 0x53ac
	Info                 ID = 0x1549a966
	TimestampScale       ID = 0x2ad7b1
	Duration             ID = 0x4489
	MuxingApp            ID = 0x4d80
	WritingApp           ID = 0x5741
	Tracks               ID = 0x1654ae6b
	TrackEntry           ID = 0xae
	TrackNumber          ID = 0xd7
	TrackUID             ID = 0x73c5
	TrackType            ID = 0x83
	FlagLacing           ID = 0x9c
	DefaultDuration      ID = 0x23e383
	CodecID              ID = 0x86
	CodecPrivate         ID = 0x63a2
	CodecDelay           ID = 0x56aa
	SeekPreRoll          ID = 0x56bb
	Video                ID = 0xe0
	PixelWidth           ID = 0xb0
	PixelHeight          ID = 0xba
	Audio                ID = 0xe1
	SamplingFrequency    ID = 0xb5
	Channels             ID = 0x9f
	ContentEncodings     ID = 0x6d80
	ContentEncoding      ID = 0x6240
	ContentEncodingScope ID = 0x5032
	ContentEncodingType  ID = 0x5033
	ContentCompression   ID = 0x5034
	ContentCompAlgo      ID = 0x4254
	ContentCompSettings  ID = 0x4255
	ContentEncryption    ID = 0x5035
	Cluster              ID = 0x1f43b675
	Timestamp            ID = 0xe7
	SimpleBlock          ID = 0xa3
	BlockGroup           ID = 0xa0
	Block                ID = 0xa1
	BlockDuration        ID = 0x9b
	ReferenceBlock       ID = 0xfb
	Cues                 ID = 0x1c53bb6b
	CuePoint             ID = 0xbb
	CueTime              ID = 0xb3
	CueTrackPositions    ID = 0xb7
	CueTrack             ID = 0xf7
	CueClusterPosition   ID = 0xf1
	Chapters             ID = 0x1043a770
	Tags                 ID = 0x1254c367
	Attachments          ID = 0x1941a469
	Void                 ID = 0xec
)

// The TrackType values.
const (
	TrackVideo    = 1
	TrackAudio    = 2
	TrackSubtitle = 17
)

// The block flags.
const (
	FlagKeyframe   = 0x80
	FlagLacingMask = 0x06
)

// The codec identifiers lys-lib writes or reads.
const (
	CodecH264   = "V_MPEG4/ISO/AVC"
	CodecH265   = "V_MPEGH/ISO/HEVC"
	CodecAV1    = "V_AV1"
	CodecVP8    = "V_VP8"
	CodecVP9    = "V_VP9"
	CodecOpus   = "A_OPUS"
	CodecVorbis = "A_VORBIS"
	CodecFLAC   = "A_FLAC"
	CodecMP3    = "A_MPEG/L3"
	CodecAAC    = "A_AAC"
)
