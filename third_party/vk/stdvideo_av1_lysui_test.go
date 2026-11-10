package vk

import (
	"testing"
	"unsafe"
)

// The AV1 structures match the C headers' layouts (vulkan-headers
// 1.4.363 on x86-64; aarch64 lays them out the same).
func TestStdVideoAV1Layouts(t *testing.T) {
	var (
		color StdVideoAV1ColorConfig
		seq   StdVideoAV1SequenceHeader
		lf    StdVideoAV1LoopFilter
		q     StdVideoAV1Quantization
		seg   StdVideoAV1Segmentation
		tile  StdVideoAV1TileInfo
		cdef  StdVideoAV1CDEF
		lr    StdVideoAV1LoopRestoration
		gm    StdVideoAV1GlobalMotion
		fg    StdVideoAV1FilmGrain
		pic   StdVideoDecodeAV1PictureInfo
		ref   StdVideoDecodeAV1ReferenceInfo
		info  VideoDecodeAV1PictureInfoKHR
	)
	for _, c := range []struct {
		name      string
		got, want uintptr
	}{
		{"color config size", unsafe.Sizeof(color), 24},
		{"color config.ColorPrimaries", unsafe.Offsetof(color.ColorPrimaries), 8},
		{"timing info size", unsafe.Sizeof(StdVideoAV1TimingInfo{}), 16},
		{"sequence header size", unsafe.Sizeof(seq), 40},
		{"sequence header.MaxFrameWidthMinus1", unsafe.Offsetof(seq.MaxFrameWidthMinus1), 10},
		{"sequence header.OrderHintBitsMinus1", unsafe.Offsetof(seq.OrderHintBitsMinus1), 16},
		{"sequence header.PColorConfig", unsafe.Offsetof(seq.PColorConfig), 24},
		{"loop filter size", unsafe.Sizeof(lf), 24},
		{"loop filter.LoopFilterRefDeltas", unsafe.Offsetof(lf.LoopFilterRefDeltas), 10},
		{"loop filter.LoopFilterModeDeltas", unsafe.Offsetof(lf.LoopFilterModeDeltas), 19},
		{"quantization size", unsafe.Sizeof(q), 16},
		{"quantization.QmV", unsafe.Offsetof(q.QmV), 12},
		{"segmentation size", unsafe.Sizeof(seg), 136},
		{"tile info size", unsafe.Sizeof(tile), 48},
		{"tile info.TileSizeBytesMinus1", unsafe.Offsetof(tile.TileSizeBytesMinus1), 8},
		{"tile info.PMiColStarts", unsafe.Offsetof(tile.PMiColStarts), 16},
		{"CDEF size", unsafe.Sizeof(cdef), 34},
		{"loop restoration size", unsafe.Sizeof(lr), 20},
		{"loop restoration.LoopRestorationSize", unsafe.Offsetof(lr.LoopRestorationSize), 12},
		{"global motion size", unsafe.Sizeof(gm), 200},
		{"global motion.GmParams", unsafe.Offsetof(gm.GmParams), 8},
		{"film grain size", unsafe.Sizeof(fg), 164},
		{"film grain.PointCbValue", unsafe.Offsetof(fg.PointCbValue), 41},
		{"film grain.ArCoeffsYPlus128", unsafe.Offsetof(fg.ArCoeffsYPlus128), 82},
		{"film grain.CbOffset", unsafe.Offsetof(fg.CbOffset), 158},
		{"film grain.CrOffset", unsafe.Offsetof(fg.CrOffset), 162},
		{"picture size", unsafe.Sizeof(pic), 136},
		{"picture.InterpolationFilter", unsafe.Offsetof(pic.InterpolationFilter), 16},
		{"picture.SkipModeFrame", unsafe.Offsetof(pic.SkipModeFrame), 26},
		{"picture.OrderHints", unsafe.Offsetof(pic.OrderHints), 32},
		{"picture.ExpectedFrameID", unsafe.Offsetof(pic.ExpectedFrameID), 40},
		{"picture.PTileInfo", unsafe.Offsetof(pic.PTileInfo), 72},
		{"picture.PFilmGrain", unsafe.Offsetof(pic.PFilmGrain), 128},
		{"reference size", unsafe.Sizeof(ref), 16},
		{"reference.SavedOrderHints", unsafe.Offsetof(ref.SavedOrderHints), 7},
		{"VideoDecodeAV1PictureInfoKHR size", unsafe.Sizeof(info), 80},
		{"VideoDecodeAV1PictureInfoKHR.FrameHeaderOffset", unsafe.Offsetof(info.FrameHeaderOffset), 52},
		{"VideoDecodeAV1PictureInfoKHR.PTileOffsets", unsafe.Offsetof(info.PTileOffsets), 64},
		{"VideoDecodeAV1ProfileInfoKHR size", unsafe.Sizeof(VideoDecodeAV1ProfileInfoKHR{}), 24},
		{"VideoDecodeAV1CapabilitiesKHR size", unsafe.Sizeof(VideoDecodeAV1CapabilitiesKHR{}), 24},
	} {
		if c.got != c.want {
			t.Errorf("%s = %d, want %d", c.name, c.got, c.want)
		}
	}
}
