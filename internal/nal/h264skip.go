package nal

// H264SkipPPS is the picture parameter set H264SkipSlice's slices name:
// id, of SPS spsID, with CAVLC, one active reference, no weighted
// prediction and the deblocking filter control present.
func H264SkipPPS(id, spsID uint8) H264PPS {
	return H264PPS{ID: id, SPSID: spsID, DeblockingFilterControlPresent: true}
}

// H264SkipSlice is the NAL unit of one P slice covering a frame of s
// (frame_mbs_only) in which every macroblock is skipped, with the
// deblocking filter off: decoded, it copies the first picture of its
// reference list. A decoder makes one for each frame a gap in frame_num
// leaves non-existing (8.2.5.2), so the frame has a picture as a
// decoder that conceals the gap with the frame before has. pps is an
// H264SkipPPS of s; frameNum and pocLsb are the slice's frame_num and,
// for pic_order_cnt_type 0, its pic_order_cnt_lsb.
func H264SkipSlice(s *H264SPS, pps *H264PPS, frameNum, pocLsb uint32) []byte {
	var w Writer
	w.UE(0)
	w.UE(5)
	w.UE(uint32(pps.ID))
	if s.SeparateColourPlane {
		w.U(2, 0)
	}
	w.U(uint(s.Log2MaxFrameNum), frameNum)
	switch {
	case s.PicOrderCntType == 0:
		w.U(uint(s.Log2MaxPicOrderCntLsb), pocLsb)
	case s.PicOrderCntType == 1 && !s.DeltaPicOrderAlwaysZero:
		w.SE(0)
	}
	w.Flag(false)
	w.Flag(false)
	w.Flag(false)
	w.SE(0)
	w.UE(1)
	w.UEInt(s.WidthMbs * s.FrameHeightMbs())
	w.Trailing()
	return w.Unit(0x21)
}
