package gpu

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"slices"
	"unsafe"

	"github.com/stubbedev/lys-lib/intconv"
	"github.com/stubbedev/lys-lib/internal/nal"
	"github.com/stubbedev/lys-lib/media/demux"
	"github.com/stubbedev/lys-lib/third_party/vk"
)

// h265Decoder is H.265's part of a decoder, as h264Decoder is H.264's.
type h265Decoder struct {
	lengthSize    int
	vps           [16]*nal.HEVCVPS
	sps           [16]*nal.HEVCSPS
	pps           [64]*nal.HEVCPPS
	raw           [3][64][]byte
	levels        [16]vk.StdVideoH265LevelIdc
	paramsChanged bool
	active        *nal.HEVCSPS
	state         h265State
	hdr           nal.HEVCSliceHeader
	rbsp          []byte
	slices        [][]byte
	offsets       []uint32

	candidates  []vk.StdVideoH265ProfileIdc
	profileInfo vk.VideoDecodeH265ProfileInfoKHR
	capsInfo    vk.VideoDecodeH265CapabilitiesKHR
	std         h265DecodeParams

	pic       vk.StdVideoDecodeH265PictureInfo
	picInfo   vk.VideoDecodeH265PictureInfoKHR
	setupStd  vk.StdVideoDecodeH265ReferenceInfo
	setupInfo vk.VideoDecodeH265DpbSlotInfoKHR
	refStd    [h265MaxSlots]vk.StdVideoDecodeH265ReferenceInfo
	refInfo   [h265MaxSlots]vk.VideoDecodeH265DpbSlotInfoKHR
	refSlots  [h265MaxSlots]int
	refPtrs   [h265MaxSlots]unsafe.Pointer
	req       decodePicture
}

// h265DecodeParams are the StdVideo parameter sets the session parameters
// are made of, and what they point at.
type h265DecodeParams struct {
	vps      []vk.StdVideoH265VideoParameterSet
	sps      []vk.StdVideoH265SequenceParameterSet
	pps      []vk.StdVideoH265PictureParameterSet
	ptl      []vk.StdVideoH265ProfileTierLevel
	dpb      []vk.StdVideoH265DecPicBufMgr
	vui      []vk.StdVideoH265SequenceParameterSetVui
	lists    []vk.StdVideoH265ScalingLists
	rps      [][64]vk.StdVideoH265ShortTermRefPicSet
	longTerm []vk.StdVideoH265LongTermRefPicsSps
	ppsLists []vk.StdVideoH265ScalingLists
	add      vk.VideoDecodeH265SessionParametersAddInfoKHR
	create   vk.VideoDecodeH265SessionParametersCreateInfoKHR
}

// The parameter set kinds, as raw indexes them.
const (
	hevcVPS = iota
	hevcSPS
	hevcPPS
)

// newH265Decoder reads config, the track's hvcC
// (HEVCDecoderConfigurationRecord, ISO/IEC 14496-15 8.3.3.1): the NAL
// unit length size and the parameter sets.
func newH265Decoder(config []byte) (decodeCodec, error) {
	c := &h265Decoder{candidates: make([]vk.StdVideoH265ProfileIdc, 0, 2)}
	if len(config) < 23 || config[0] != 1 {
		return nil, fmt.Errorf("hvcC of %d bytes, version %v: %w", len(config), config[:min(len(config), 1)], demux.ErrMalformed)
	}
	c.lengthSize = int(config[21]&3) + 1
	if c.lengthSize == 3 {
		return nil, fmt.Errorf("hvcC NAL unit length size 3: %w", demux.ErrMalformed)
	}
	b := config[23:]
	for range config[22] {
		if len(b) < 3 {
			return nil, fmt.Errorf("hvcC ends inside its arrays: %w", demux.ErrMalformed)
		}
		n := int(b[1])<<8 | int(b[2])
		b = b[3:]
		for range n {
			if len(b) < 2 || len(b) < 2+(int(b[0])<<8|int(b[1])) {
				return nil, fmt.Errorf("hvcC ends inside a NAL unit: %w", demux.ErrMalformed)
			}
			size := int(b[0])<<8 | int(b[1])
			unit := b[2 : 2+size]
			b = b[2+size:]
			if len(unit) < 2 {
				continue
			}
			switch nal.HEVCType(unit) {
			case nal.HEVCNALVPS, nal.HEVCNALSPS, nal.HEVCNALPPS:
				if err := c.parameterSet(unit); err != nil {
					return nil, fmt.Errorf("hvcC: %w", err)
				}
			}
		}
	}
	if !slices.ContainsFunc(c.sps[:], func(s *nal.HEVCSPS) bool { return s != nil }) {
		return nil, fmt.Errorf("hvcC holds no SPS: %w", demux.ErrMalformed)
	}
	return c, nil
}

// parameterSet keeps unit, a VPS, SPS or PPS, when it differs from the
// one of its ID there is.
func (c *h265Decoder) parameterSet(unit []byte) error {
	if len(unit) < 3 {
		return fmt.Errorf("%w parameter set of %d bytes", nal.ErrHEVC, len(unit))
	}
	c.rbsp = nal.Unescape(c.rbsp[:0], unit[2:])
	kind, id := 0, 0
	switch nal.HEVCType(unit) {
	case nal.HEVCNALVPS:
		v := &nal.HEVCVPS{}
		if err := v.Parse(c.rbsp); err != nil {
			return err
		}
		kind, id = hevcVPS, int(v.ID)
		if bytes.Equal(c.raw[kind][id], unit) {
			return nil
		}
		c.vps[id] = v
	case nal.HEVCNALSPS:
		s := &nal.HEVCSPS{}
		if err := s.Parse(c.rbsp); err != nil {
			return err
		}
		kind, id = hevcSPS, int(s.ID)
		if bytes.Equal(c.raw[kind][id], unit) {
			return nil
		}
		level, err := h265LevelOf(s.PTL.LevelIdc)
		if err != nil {
			return fmt.Errorf("SPS %d: %w", s.ID, err)
		}
		c.sps[id], c.levels[id] = s, level
	default:
		p := &nal.HEVCPPS{}
		if err := p.Parse(c.rbsp, &c.sps); err != nil {
			return err
		}
		kind, id = hevcPPS, int(p.ID)
		if bytes.Equal(c.raw[kind][id], unit) {
			return nil
		}
		c.pps[id] = p
	}
	c.raw[kind][id] = bytes.Clone(unit)
	c.paramsChanged = true
	return nil
}

// h265LevelOf is general_level_idc idc as a StdVideo level enumerant,
// an index of h265Levels.
func h265LevelOf(idc uint8) (vk.StdVideoH265LevelIdc, error) {
	for i, l := range h265Levels {
		if l.idc == int(idc) {
			return vk.StdVideoH265LevelIdc(i), nil
		}
	}
	return 0, fmt.Errorf("%w: general_level_idc %d names no level", nal.ErrHEVC, idc)
}

// decode reads the packet's NAL units, length-prefixed, and decodes the
// picture its slice segments make.
func (c *h265Decoder) decode(v *VideoDecoder, p demux.Packet) error {
	c.slices = c.slices[:0]
	for data := p.Data; len(data) > 0; {
		if len(data) < c.lengthSize {
			return fmt.Errorf("%d bytes left for a %d-byte NAL unit length: %w", len(data), c.lengthSize, demux.ErrMalformed)
		}
		n := 0
		for _, b := range data[:c.lengthSize] {
			n = n<<8 | int(b)
		}
		data = data[c.lengthSize:]
		if n > len(data) {
			return fmt.Errorf("a %d-byte NAL unit in %d bytes: %w", n, len(data), demux.ErrMalformed)
		}
		unit := data[:n]
		data = data[n:]
		if n < 3 {
			continue
		}
		switch t := nal.HEVCType(unit); {
		case t == nal.HEVCNALVPS || t == nal.HEVCNALSPS || t == nal.HEVCNALPPS:
			if err := c.parameterSet(unit); err != nil {
				return err
			}
		case t <= nal.HEVCNALRASLR || t >= nal.HEVCNALBLAWLP && t <= nal.HEVCNALCRA:
			c.slices = append(c.slices, unit)
		}
	}
	if len(c.slices) == 0 {
		return nil
	}
	return c.picture(v, p)
}

// picture decodes the packet's slice segments, one picture.
func (c *h265Decoder) picture(v *VideoDecoder, p demux.Packet) error {
	first := c.slices[0]
	for _, unit := range c.slices[1:] {
		if unit[2]&0x80 != 0 {
			return fmt.Errorf("%w: the packet holds more than one picture", nal.ErrHEVC)
		}
	}
	end := min(len(first), 2+sliceHeaderBytes)
	c.rbsp = nal.Unescape(c.rbsp[:0], first[2:end])
	err := c.hdr.Parse(c.rbsp, nal.HEVCType(first), &c.pps, &c.sps)
	if err != nil && end < len(first) {
		c.rbsp = nal.Unescape(c.rbsp[:0], first[2:])
		err = c.hdr.Parse(c.rbsp, nal.HEVCType(first), &c.pps, &c.sps)
	}
	if err != nil {
		return err
	}
	pps := c.pps[c.hdr.PPSID]
	sps := c.sps[pps.SPSID]
	if c.vps[sps.VPSID] == nil {
		return fmt.Errorf("%w: SPS %d names VPS %d, which the stream has not sent", nal.ErrHEVC, sps.ID, sps.VPSID)
	}
	shape, err := c.shape(sps)
	if err != nil {
		return err
	}
	c.state.slots = shape.slots
	pic := h265Pic{hdr: &c.hdr, sps: sps, nalType: nal.HEVCType(first), tid: nal.HEVCTemporalID(first)}
	if err := c.state.begin(&pic); err != nil {
		if errors.Is(err, errSkipPicture) {
			return nil
		}
		return err
	}
	c.active = sps
	if err := v.shapeFor(shape); err != nil {
		return err
	}
	if c.paramsChanged {
		if err := v.createParameters(); err != nil {
			return err
		}
		c.paramsChanged = false
	}
	frame, err := v.frame()
	if err != nil {
		return err
	}
	total := 0
	for _, unit := range c.slices {
		total += len(startCode) + len(unit)
	}
	buf, err := v.stream(total)
	if err != nil {
		return err
	}
	c.offsets = c.offsets[:0]
	at := 0
	for _, unit := range c.slices {
		c.offsets = append(c.offsets, intconv.Must[uint32](at))
		at += copy(buf[at:], startCode)
		at += copy(buf[at:], unit)
	}
	c.fill(&pic, pps)
	c.req = decodePicture{frame: frame, bytes: total, info: unsafe.Pointer(&c.picInfo), setup: pic.slot, setupInfo: unsafe.Pointer(&c.setupInfo)}
	n := 0
	for _, list := range [3][]int{pic.before[:pic.nBefore], pic.after[:pic.nAfter], pic.lt[:pic.nLt]} {
		for _, slot := range list {
			if slices.Contains(c.refSlots[:n], slot) {
				continue
			}
			r := &c.state.refs[slot]
			c.refStd[n] = vk.StdVideoDecodeH265ReferenceInfo{PicOrderCntVal: r.poc}
			if r.long {
				c.refStd[n].Flags = vk.StdVideoDecodeH265ReferenceUsedForLongTerm
			}
			c.refInfo[n] = vk.VideoDecodeH265DpbSlotInfoKHR{SType: vk.StructureTypeVideoDecodeH265DpbSlotInfoKhr, PStdReferenceInfo: &c.refStd[n]}
			c.refSlots[n], c.refPtrs[n] = slot, unsafe.Pointer(&c.refInfo[n])
			n++
		}
	}
	c.req.refs, c.req.refInfo = c.refSlots[:n], c.refPtrs[:n]
	if err := v.submit(&c.req); err != nil {
		return err
	}
	frame.pts, frame.duration = p.PTS, p.Duration
	x0, y0, x1, y1 := sps.CropRect()
	frame.rect = image.Rect(x0, y0, x1, y1)
	c.state.finish(&pic)
	boundary := pic.irap() && pic.noRaslOutput
	if !pic.output() {
		if boundary {
			v.pending.drain(v.present)
		}
		frame.free(0)
		return nil
	}
	v.queue(frame, pic.poc, boundary, boundary && c.hdr.NoOutputOfPriorPics)
	return nil
}

// fill lays out pic's picture info and the reference info of the DPB
// slot it sets up.
func (c *h265Decoder) fill(pic *h265Pic, pps *nal.HEVCPPS) {
	h, sps := pic.hdr, pic.sps
	var flags uint32
	if pic.irap() {
		flags |= vk.StdVideoDecodeH265PictureIrap
	}
	if pic.idr() {
		flags |= vk.StdVideoDecodeH265PictureIdr
	}
	if t := pic.nalType; t > 14 || t%2 == 1 {
		flags |= vk.StdVideoDecodeH265PictureIsReference
	}
	if h.ShortTermRefPicSetSPS {
		flags |= vk.StdVideoDecodeH265PictureShortTermRefPicSetSps
	}
	var refDeltas uint8
	if !h.ShortTermRefPicSetSPS && h.RPS.InterRPSPred {
		ref := &sps.ShortTermRPS[int(sps.NumShortTermRPS)-int(h.RPS.DeltaIdxMinus1)-1]
		refDeltas = intconv.Must[uint8](ref.NumDeltaPocs())
	}
	c.pic = vk.StdVideoDecodeH265PictureInfo{
		Flags: flags, SpsVideoParameterSetID: sps.VPSID, PpsSeqParameterSetID: sps.ID, PpsPicParameterSetID: pps.ID,
		NumDeltaPocsOfRefRpsIdx: refDeltas, PicOrderCntVal: pic.poc, NumBitsForSTRefPicSetInSlice: intconv.Must[uint16](h.STRPSBits),
	}
	for i := range vk.StdVideoDecodeH265RefPicSetListSize {
		c.pic.RefPicSetStCurrBefore[i], c.pic.RefPicSetStCurrAfter[i], c.pic.RefPicSetLtCurr[i] = 0xff, 0xff, 0xff
	}
	for i, slot := range pic.before[:pic.nBefore] {
		c.pic.RefPicSetStCurrBefore[i] = intconv.Must[uint8](slot)
	}
	for i, slot := range pic.after[:pic.nAfter] {
		c.pic.RefPicSetStCurrAfter[i] = intconv.Must[uint8](slot)
	}
	for i, slot := range pic.lt[:pic.nLt] {
		c.pic.RefPicSetLtCurr[i] = intconv.Must[uint8](slot)
	}
	c.picInfo = vk.VideoDecodeH265PictureInfoKHR{
		SType: vk.StructureTypeVideoDecodeH265PictureInfoKhr, PStdPictureInfo: &c.pic,
		SliceSegmentCount: intconv.Must[uint32](len(c.offsets)), PSliceSegmentOffsets: &c.offsets[0],
	}
	c.setupStd = vk.StdVideoDecodeH265ReferenceInfo{PicOrderCntVal: pic.poc}
	c.setupInfo = vk.VideoDecodeH265DpbSlotInfoKHR{SType: vk.StructureTypeVideoDecodeH265DpbSlotInfoKhr, PStdReferenceInfo: &c.setupStd}
}

// shape is the session s decodes in, and the profiles the device may
// decode it as.
func (c *h265Decoder) shape(s *nal.HEVCSPS) (decodeShape, error) {
	depth, ok := depthOf(8 + int(s.BitDepthLumaMinus8))
	if s.ChromaFormatIdc != 1 || s.BitDepthChromaMinus8 != s.BitDepthLumaMinus8 || !ok {
		return decodeShape{}, fmt.Errorf("%w: SPS %d is chroma_format_idc %d at %d and %d bits; only 4:2:0 at 8 or 10 bits is decoded", nal.ErrHEVC, s.ID, s.ChromaFormatIdc, 8+s.BitDepthLumaMinus8, 8+s.BitDepthChromaMinus8)
	}
	c.candidates = c.candidates[:0]
	main := s.PTL.Compatibility&(1<<30) != 0
	main10 := s.PTL.Compatibility&(1<<29) != 0
	switch {
	case depth == depth8 && (s.PTL.ProfileIdc == 1 || main):
		c.candidates = append(c.candidates, vk.StdVideoH265ProfileIdcMain)
	case depth == depth8 && s.PTL.ProfileIdc == 3:
		c.candidates = append(c.candidates, vk.StdVideoH265ProfileIdcMainStillPicture, vk.StdVideoH265ProfileIdcMain)
	case s.PTL.ProfileIdc == 2 || main10:
		c.candidates = append(c.candidates, vk.StdVideoH265ProfileIdcMain10)
	default:
		return decodeShape{}, fmt.Errorf("%w: SPS %d is general_profile_idc %d at %d bits; Main and Main 10 are decoded", nal.ErrHEVC, s.ID, s.PTL.ProfileIdc, 8+s.BitDepthLumaMinus8)
	}
	_, y0, _, y1 := s.CropRect()
	v := &s.VUI
	ycbcr, err := videoSignal{
		typed: s.VUIPresent && v.VideoSignalTypePresent, fullRange: v.FullRange,
		described: v.ColourDescriptionPresent, matrix: v.MatrixCoeffs,
		sited: s.VUIPresent && v.ChromaLocInfoPresent, siting: v.ChromaSampleLocTypeTop, lines: y1 - y0,
	}.ycbcr()
	if err != nil {
		return decodeShape{}, fmt.Errorf("%w: SPS %d: %w", nal.ErrHEVC, s.ID, err)
	}
	top := s.MaxSubLayersMinus1
	refs := max(int(s.Ordering.MaxDecPicBufferingMinus1[top]), 1)
	return decodeShape{
		profile: int(s.PTL.ProfileIdc), coded: [2]int{s.Width, s.Height},
		slots: refs + 1, refs: refs, reorder: int(s.Ordering.MaxNumReorderPics[top]), depth: depth, ycbcr: ycbcr,
	}, nil
}

func (c *h265Decoder) profiles() int { return len(c.candidates) }

func (c *h265Decoder) profile(i int, next unsafe.Pointer) unsafe.Pointer {
	c.profileInfo = vk.VideoDecodeH265ProfileInfoKHR{SType: vk.StructureTypeVideoDecodeH265ProfileInfoKhr, PNext: (*uintptr)(next), StdProfileIdc: c.candidates[i]}
	return unsafe.Pointer(&c.profileInfo)
}

func (c *h265Decoder) caps() unsafe.Pointer {
	c.capsInfo = vk.VideoDecodeH265CapabilitiesKHR{SType: vk.StructureTypeVideoDecodeH265CapabilitiesKhr}
	return unsafe.Pointer(&c.capsInfo)
}

func (c *h265Decoder) checkCaps() error {
	if level := c.levels[c.active.ID]; level > c.capsInfo.MaxLevelIdc {
		return fmt.Errorf("H.265 level %d.%d is past the device's (StdVideo level %d)", c.active.PTL.LevelIdc/30, c.active.PTL.LevelIdc%30/3, c.capsInfo.MaxLevelIdc)
	}
	return nil
}

func (c *h265Decoder) reset() { c.state.reset() }

// parameters lays out every parameter set the stream has sent as the
// session parameters' create chain.
func (c *h265Decoder) parameters() unsafe.Pointer {
	p := &c.std
	count := func(sets int, present func(int) bool) int {
		n := 0
		for i := range sets {
			if present(i) {
				n++
			}
		}
		return n
	}
	nv := count(len(c.vps), func(i int) bool { return c.vps[i] != nil })
	ns := count(len(c.sps), func(i int) bool { return c.sps[i] != nil })
	np := count(len(c.pps), func(i int) bool { return c.pps[i] != nil })
	p.vps, p.sps, p.pps = resize(p.vps, nv), resize(p.sps, ns), resize(p.pps, np)
	p.ptl, p.dpb = resize(p.ptl, nv+ns), resize(p.dpb, nv+ns)
	p.vui, p.lists, p.rps, p.longTerm = resize(p.vui, ns), resize(p.lists, ns), resize(p.rps, ns), resize(p.longTerm, ns)
	p.ppsLists = resize(p.ppsLists, np)
	i := 0
	for _, v := range c.vps {
		if v == nil {
			continue
		}
		level, _ := h265LevelOf(v.PTL.LevelIdc)
		p.ptl[i], p.dpb[i] = h265StdPTL(&v.PTL, level), h265StdDPB(&v.Ordering)
		p.vps[i] = h265StdVPS(v, &p.ptl[i], &p.dpb[i])
		i++
	}
	j := 0
	for _, s := range c.sps {
		if s == nil {
			continue
		}
		p.ptl[nv+j], p.dpb[nv+j] = h265StdPTL(&s.PTL, c.levels[s.ID]), h265StdDPB(&s.Ordering)
		p.vui[j], p.lists[j], p.longTerm[j] = h265StdVUI(&s.VUI), h265StdLists(&s.Scaling), h265StdLongTerm(s)
		for k := range int(s.NumShortTermRPS) {
			p.rps[j][k] = h265StdRPS(&s.ShortTermRPS[k])
		}
		p.sps[j] = h265StdSPS(s, &p.ptl[nv+j], &p.dpb[nv+j], &p.vui[j], &p.lists[j], &p.rps[j][0], &p.longTerm[j])
		j++
	}
	k := 0
	for _, pp := range c.pps {
		if pp == nil {
			continue
		}
		p.ppsLists[k] = h265StdLists(&pp.Scaling)
		var vps uint8
		if s := c.sps[pp.SPSID]; s != nil {
			vps = s.VPSID
		}
		p.pps[k] = h265StdPPS(pp, vps, &p.ppsLists[k])
		k++
	}
	p.add = vk.VideoDecodeH265SessionParametersAddInfoKHR{
		SType:       vk.StructureTypeVideoDecodeH265SessionParametersAddInfoKhr,
		StdVPSCount: intconv.Must[uint32](nv), StdSPSCount: intconv.Must[uint32](ns), StdPPSCount: intconv.Must[uint32](np),
	}
	if nv > 0 {
		p.add.PStdVPSs = &p.vps[0]
	}
	if ns > 0 {
		p.add.PStdSPSs = &p.sps[0]
	}
	if np > 0 {
		p.add.PStdPPSs = &p.pps[0]
	}
	p.create = vk.VideoDecodeH265SessionParametersCreateInfoKHR{
		SType:          vk.StructureTypeVideoDecodeH265SessionParametersCreateInfoKhr,
		MaxStdVPSCount: max(p.add.StdVPSCount, 1), MaxStdSPSCount: max(p.add.StdSPSCount, 1), MaxStdPPSCount: max(p.add.StdPPSCount, 1),
		PParametersAddInfo: &p.add,
	}
	return unsafe.Pointer(&p.create)
}

// resize is s with n elements, its storage reused when it holds them.
func resize[T any](s []T, n int) []T {
	if cap(s) < n {
		return make([]T, n)
	}
	return s[:n]
}

func h265StdPTL(p *nal.HEVCProfileTierLevel, level vk.StdVideoH265LevelIdc) vk.StdVideoH265ProfileTierLevel {
	return vk.StdVideoH265ProfileTierLevel{
		Flags: bitfield(
			flagBit{p.Tier, vk.StdVideoH265PtlTier},
			flagBit{p.Progressive, vk.StdVideoH265PtlProgressiveSource},
			flagBit{p.Interlaced, vk.StdVideoH265PtlInterlacedSource},
			flagBit{p.NonPacked, vk.StdVideoH265PtlNonPackedConstraint},
			flagBit{p.FrameOnly, vk.StdVideoH265PtlFrameOnlyConstraint},
		),
		GeneralProfileIdc: vk.StdVideoH265ProfileIdc(p.ProfileIdc), GeneralLevelIdc: level,
	}
}

func h265StdDPB(o *nal.HEVCSubLayerOrdering) vk.StdVideoH265DecPicBufMgr {
	return vk.StdVideoH265DecPicBufMgr{
		MaxLatencyIncreasePlus1: o.MaxLatencyIncreasePlus1, MaxDecPicBufferingMinus1: o.MaxDecPicBufferingMinus1, MaxNumReorderPics: o.MaxNumReorderPics,
	}
}

func h265StdVPS(v *nal.HEVCVPS, ptl *vk.StdVideoH265ProfileTierLevel, dpb *vk.StdVideoH265DecPicBufMgr) vk.StdVideoH265VideoParameterSet {
	return vk.StdVideoH265VideoParameterSet{
		Flags: bitfield(
			flagBit{v.TemporalIDNesting, vk.StdVideoH265VpsTemporalIDNesting},
			flagBit{v.SubLayerOrderingInfoPresent, vk.StdVideoH265VpsSubLayerOrderingInfoPresent},
			flagBit{v.TimingInfoPresent, vk.StdVideoH265VpsTimingInfoPresent},
			flagBit{v.PocProportionalToTiming, vk.StdVideoH265VpsPocProportionalToTiming},
		),
		VpsVideoParameterSetID: v.ID, VpsMaxSubLayersMinus1: v.MaxSubLayersMinus1,
		VpsNumUnitsInTick: v.NumUnitsInTick, VpsTimeScale: v.TimeScale, VpsNumTicksPocDiffOneMinus1: v.NumTicksPocDiffOneMinus1,
		PDecPicBufMgr: dpb, PProfileTierLevel: ptl,
	}
}

func h265StdLists(l *nal.HEVCScalingLists) vk.StdVideoH265ScalingLists {
	return vk.StdVideoH265ScalingLists{
		ScalingList4x4: l.List4x4, ScalingList8x8: l.List8x8, ScalingList16x16: l.List16x16, ScalingList32x32: l.List32x32,
		ScalingListDCCoef16x16: l.DC16x16, ScalingListDCCoef32x32: l.DC32x32,
	}
}

// h265StdRPS lays r out with its syntax and the delta POCs it works out
// to, each as its distance from the one before less one.
func h265StdRPS(r *nal.HEVCShortTermRPS) vk.StdVideoH265ShortTermRefPicSet {
	out := vk.StdVideoH265ShortTermRefPicSet{
		Flags: bitfield(
			flagBit{r.InterRPSPred, vk.StdVideoH265StrpsInterRefPicSetPrediction},
			flagBit{r.DeltaRPSSign, vk.StdVideoH265StrpsDeltaRpsSign},
		),
		DeltaIdxMinus1: r.DeltaIdxMinus1, UseDeltaFlag: uint16(r.UseDeltaFlag & 0xffff), AbsDeltaRpsMinus1: uint16(r.AbsDeltaRPSMinus1 & 0xffff),
		UsedByCurrPicFlag: uint16(r.UsedByCurrPicFlag & 0xffff), UsedByCurrPicS0Flag: r.UsedS0, UsedByCurrPicS1Flag: r.UsedS1,
		NumNegativePics: r.NumNegative, NumPositivePics: r.NumPositive,
	}
	var prev int32
	for i := range int(r.NumNegative) {
		out.DeltaPocS0Minus1[i] = intconv.Must[uint16](prev - r.DeltaPocS0[i] - 1)
		prev = r.DeltaPocS0[i]
	}
	prev = 0
	for i := range int(r.NumPositive) {
		out.DeltaPocS1Minus1[i] = intconv.Must[uint16](r.DeltaPocS1[i] - prev - 1)
		prev = r.DeltaPocS1[i]
	}
	return out
}

func h265StdLongTerm(s *nal.HEVCSPS) vk.StdVideoH265LongTermRefPicsSps {
	return vk.StdVideoH265LongTermRefPicsSps{UsedByCurrPicLtSpsFlag: s.UsedByCurrPicLtSPS, LtRefPicPocLsbSps: s.LtRefPicPocLsbSPS}
}

// h265StdVUI lays v out, its HRD parameters left out: decoding reads
// none of them.
func h265StdVUI(v *nal.HEVCVUI) vk.StdVideoH265SequenceParameterSetVui {
	return vk.StdVideoH265SequenceParameterSetVui{
		Flags: bitfield(
			flagBit{v.AspectRatioInfoPresent, vk.StdVideoH265VuiAspectRatioInfoPresent},
			flagBit{v.OverscanInfoPresent, vk.StdVideoH265VuiOverscanInfoPresent},
			flagBit{v.OverscanAppropriate, vk.StdVideoH265VuiOverscanAppropriate},
			flagBit{v.VideoSignalTypePresent, vk.StdVideoH265VuiVideoSignalTypePresent},
			flagBit{v.FullRange, vk.StdVideoH265VuiVideoFullRange},
			flagBit{v.ColourDescriptionPresent, vk.StdVideoH265VuiColourDescriptionPresent},
			flagBit{v.ChromaLocInfoPresent, vk.StdVideoH265VuiChromaLocInfoPresent},
			flagBit{v.NeutralChromaIndication, vk.StdVideoH265VuiNeutralChromaIndication},
			flagBit{v.FieldSeq, vk.StdVideoH265VuiFieldSeq},
			flagBit{v.FrameFieldInfoPresent, vk.StdVideoH265VuiFrameFieldInfoPresent},
			flagBit{v.DefaultDisplayWindow, vk.StdVideoH265VuiDefaultDisplayWindow},
			flagBit{v.TimingInfoPresent, vk.StdVideoH265VuiTimingInfoPresent},
			flagBit{v.PocProportionalToTiming, vk.StdVideoH265VuiPocProportionalToTiming},
			flagBit{v.BitstreamRestriction, vk.StdVideoH265VuiBitstreamRestriction},
			flagBit{v.TilesFixedStructure, vk.StdVideoH265VuiTilesFixedStructure},
			flagBit{v.MotionVectorsOverPicBoundaries, vk.StdVideoH265VuiMotionVectorsOverPicBoundaries},
			flagBit{v.RestrictedRefPicLists, vk.StdVideoH265VuiRestrictedRefPicLists},
		),
		AspectRatioIdc: vk.StdVideoH265AspectRatioIdc(v.AspectRatioIdc), SarWidth: v.SarWidth, SarHeight: v.SarHeight,
		VideoFormat: v.VideoFormat, ColourPrimaries: v.ColourPrimaries, TransferCharacteristics: v.TransferCharacteristics, MatrixCoeffs: v.MatrixCoeffs,
		ChromaSampleLocTypeTopField: v.ChromaSampleLocTypeTop, ChromaSampleLocTypeBottomField: v.ChromaSampleLocTypeBot,
		DefDispWinLeftOffset: v.DefDispWin[0], DefDispWinRightOffset: v.DefDispWin[1], DefDispWinTopOffset: v.DefDispWin[2], DefDispWinBottomOffset: v.DefDispWin[3],
		VuiNumUnitsInTick: v.NumUnitsInTick, VuiTimeScale: v.TimeScale, VuiNumTicksPocDiffOneMinus1: v.NumTicksPocDiffOneMinus1,
		MinSpatialSegmentationIdc: v.MinSpatialSegmentationIdc, MaxBytesPerPicDenom: v.MaxBytesPerPicDenom, MaxBitsPerMinCuDenom: v.MaxBitsPerMinCuDenom,
		Log2MaxMvLengthHorizontal: v.Log2MaxMvLengthHorizontal, Log2MaxMvLengthVertical: v.Log2MaxMvLengthVertical,
	}
}

func h265StdSPS(s *nal.HEVCSPS, ptl *vk.StdVideoH265ProfileTierLevel, dpb *vk.StdVideoH265DecPicBufMgr, vui *vk.StdVideoH265SequenceParameterSetVui,
	lists *vk.StdVideoH265ScalingLists, rps *vk.StdVideoH265ShortTermRefPicSet, longTerm *vk.StdVideoH265LongTermRefPicsSps,
) vk.StdVideoH265SequenceParameterSet {
	out := vk.StdVideoH265SequenceParameterSet{
		Flags: bitfield(
			flagBit{s.TemporalIDNesting, vk.StdVideoH265SpsTemporalIDNesting},
			flagBit{s.SeparateColourPlane, vk.StdVideoH265SpsSeparateColourPlane},
			flagBit{s.ConformanceWindow, vk.StdVideoH265SpsConformanceWindow},
			flagBit{s.SubLayerOrderingInfoPresent, vk.StdVideoH265SpsSubLayerOrderingInfoPresent},
			flagBit{s.ScalingListEnabled, vk.StdVideoH265SpsScalingListEnabled},
			flagBit{s.ScalingListDataPresent, vk.StdVideoH265SpsScalingListDataPresent},
			flagBit{s.AMP, vk.StdVideoH265SpsAmpEnabled},
			flagBit{s.SAO, vk.StdVideoH265SpsSampleAdaptiveOffset},
			flagBit{s.PCM, vk.StdVideoH265SpsPcmEnabled},
			flagBit{s.PCMLoopFilterDisabled, vk.StdVideoH265SpsPcmLoopFilterDisabled},
			flagBit{s.LongTermRefPicsPresent, vk.StdVideoH265SpsLongTermRefPicsPresent},
			flagBit{s.TemporalMVP, vk.StdVideoH265SpsTemporalMvpEnabled},
			flagBit{s.StrongIntraSmoothing, vk.StdVideoH265SpsStrongIntraSmoothing},
			flagBit{s.VUIPresent, vk.StdVideoH265SpsVuiParametersPresent},
			flagBit{s.ExtensionPresent, vk.StdVideoH265SpsExtensionPresent},
			flagBit{s.RangeExtension, vk.StdVideoH265SpsRangeExtension},
			flagBit{s.TransformSkipRotation, vk.StdVideoH265SpsTransformSkipRotation},
			flagBit{s.TransformSkipContext, vk.StdVideoH265SpsTransformSkipContext},
			flagBit{s.ImplicitRDPCM, vk.StdVideoH265SpsImplicitRdpcm},
			flagBit{s.ExplicitRDPCM, vk.StdVideoH265SpsExplicitRdpcm},
			flagBit{s.ExtendedPrecision, vk.StdVideoH265SpsExtendedPrecisionProcessing},
			flagBit{s.IntraSmoothingDisabled, vk.StdVideoH265SpsIntraSmoothingDisabled},
			flagBit{s.HighPrecisionOffsets, vk.StdVideoH265SpsHighPrecisionOffsets},
			flagBit{s.PersistentRiceAdaptation, vk.StdVideoH265SpsPersistentRiceAdaptation},
			flagBit{s.CabacBypassAlignment, vk.StdVideoH265SpsCabacBypassAlignment},
		),
		ChromaFormatIdc:       vk.StdVideoH265ChromaFormatIdc(s.ChromaFormatIdc),
		PicWidthInLumaSamples: intconv.Must[uint32](s.Width), PicHeightInLumaSamples: intconv.Must[uint32](s.Height),
		SpsVideoParameterSetID: s.VPSID, SpsMaxSubLayersMinus1: s.MaxSubLayersMinus1, SpsSeqParameterSetID: s.ID,
		BitDepthLumaMinus8: s.BitDepthLumaMinus8, BitDepthChromaMinus8: s.BitDepthChromaMinus8, Log2MaxPicOrderCntLsbMinus4: s.Log2MaxPocLsb - 4,
		Log2MinLumaCodingBlockSizeMinus3: s.Log2MinCbMinus3, Log2DiffMaxMinLumaCodingBlockSize: s.Log2DiffMaxMinCb,
		Log2MinLumaTransformBlockSizeMinus2: s.Log2MinTbMinus2, Log2DiffMaxMinLumaTransformBlockSize: s.Log2DiffMaxMinTb,
		MaxTransformHierarchyDepthInter: s.MaxTransformDepthInter, MaxTransformHierarchyDepthIntra: s.MaxTransformDepthIntra,
		NumShortTermRefPicSets: s.NumShortTermRPS, NumLongTermRefPicsSps: s.NumLongTermRefPicsSPS,
		PcmSampleBitDepthLumaMinus1: s.PCMBitDepthLumaMinus1, PcmSampleBitDepthChromaMinus1: s.PCMBitDepthChromaMinus1,
		Log2MinPcmLumaCodingBlockSizeMinus3: s.Log2MinPCMCbMinus3, Log2DiffMaxMinPcmLumaCodingBlockSize: s.Log2DiffMaxMinPCMCb,
		ConfWinLeftOffset: s.Conf[0], ConfWinRightOffset: s.Conf[1], ConfWinTopOffset: s.Conf[2], ConfWinBottomOffset: s.Conf[3],
		PProfileTierLevel: ptl, PDecPicBufMgr: dpb, PLongTermRefPicsSps: longTerm,
	}
	if s.ScalingListEnabled {
		out.PScalingLists = lists
	}
	if s.NumShortTermRPS > 0 {
		out.PShortTermRefPicSet = rps
	}
	if s.VUIPresent {
		out.PSequenceParameterSetVui = vui
	}
	return out
}

func h265StdPPS(p *nal.HEVCPPS, vps uint8, lists *vk.StdVideoH265ScalingLists) vk.StdVideoH265PictureParameterSet {
	out := vk.StdVideoH265PictureParameterSet{
		Flags: bitfield(
			flagBit{p.DependentSliceSegments, vk.StdVideoH265PpsDependentSliceSegments},
			flagBit{p.OutputFlagPresent, vk.StdVideoH265PpsOutputFlagPresent},
			flagBit{p.SignDataHiding, vk.StdVideoH265PpsSignDataHiding},
			flagBit{p.CabacInitPresent, vk.StdVideoH265PpsCabacInitPresent},
			flagBit{p.ConstrainedIntraPred, vk.StdVideoH265PpsConstrainedIntraPred},
			flagBit{p.TransformSkipEnabled, vk.StdVideoH265PpsTransformSkipEnabled},
			flagBit{p.CuQpDeltaEnabled, vk.StdVideoH265PpsCuQpDeltaEnabled},
			flagBit{p.SliceChromaQpOffsetsPresent, vk.StdVideoH265PpsSliceChromaQpOffsetsPresent},
			flagBit{p.WeightedPred, vk.StdVideoH265PpsWeightedPred},
			flagBit{p.WeightedBipred, vk.StdVideoH265PpsWeightedBipred},
			flagBit{p.TransquantBypassEnabled, vk.StdVideoH265PpsTransquantBypassEnabled},
			flagBit{p.TilesEnabled, vk.StdVideoH265PpsTilesEnabled},
			flagBit{p.EntropyCodingSyncEnabled, vk.StdVideoH265PpsEntropyCodingSyncEnabled},
			flagBit{p.UniformSpacing, vk.StdVideoH265PpsUniformSpacing},
			flagBit{p.LoopFilterAcrossTiles, vk.StdVideoH265PpsLoopFilterAcrossTiles},
			flagBit{p.LoopFilterAcrossSlices, vk.StdVideoH265PpsLoopFilterAcrossSlices},
			flagBit{p.DeblockingFilterControlPresent, vk.StdVideoH265PpsDeblockingFilterControlPresent},
			flagBit{p.DeblockingFilterOverrideEnabled, vk.StdVideoH265PpsDeblockingFilterOverrideEnabled},
			flagBit{p.DeblockingFilterDisabled, vk.StdVideoH265PpsDeblockingFilterDisabled},
			flagBit{p.ScalingListDataPresent, vk.StdVideoH265PpsScalingListDataPresent},
			flagBit{p.ListsModificationPresent, vk.StdVideoH265PpsListsModificationPresent},
			flagBit{p.SliceSegmentHeaderExtension, vk.StdVideoH265PpsSliceSegmentHeaderExtensionPresent},
			flagBit{p.ExtensionPresent, vk.StdVideoH265PpsExtensionPresent},
			flagBit{p.CrossComponentPrediction, vk.StdVideoH265PpsCrossComponentPrediction},
			flagBit{p.ChromaQpOffsetListEnabled, vk.StdVideoH265PpsChromaQpOffsetListEnabled},
			flagBit{p.RangeExtension, vk.StdVideoH265PpsRangeExtension},
		),
		PpsPicParameterSetID: p.ID, PpsSeqParameterSetID: p.SPSID, SpsVideoParameterSetID: vps,
		NumExtraSliceHeaderBits: p.NumExtraSliceHeaderBits, NumRefIdxL0DefaultActiveMinus1: p.NumRefIdxL0DefaultActiveMinus1, NumRefIdxL1DefaultActiveMinus1: p.NumRefIdxL1DefaultActiveMinus1,
		InitQpMinus26: p.InitQpMinus26, DiffCuQpDeltaDepth: p.DiffCuQpDeltaDepth, PpsCbQpOffset: p.CbQpOffset, PpsCrQpOffset: p.CrQpOffset,
		PpsBetaOffsetDiv2: p.BetaOffsetDiv2, PpsTcOffsetDiv2: p.TcOffsetDiv2, Log2ParallelMergeLevelMinus2: p.Log2ParallelMergeLevelMinus2,
		Log2MaxTransformSkipBlockSizeMinus2: p.Log2MaxTransformSkipBlockMinus2, DiffCuChromaQpOffsetDepth: p.DiffCuChromaQpOffsetDepth,
		ChromaQpOffsetListLenMinus1: p.ChromaQpOffsetListLenMinus1, CbQpOffsetList: p.CbQpOffsetList, CrQpOffsetList: p.CrQpOffsetList,
		Log2SaoOffsetScaleLuma: p.Log2SaoOffsetScaleLuma, Log2SaoOffsetScaleChroma: p.Log2SaoOffsetScaleChroma,
		NumTileColumnsMinus1: p.NumTileColumnsMinus1, NumTileRowsMinus1: p.NumTileRowsMinus1,
		ColumnWidthMinus1: p.ColumnWidthMinus1, RowHeightMinus1: p.RowHeightMinus1,
	}
	if p.ScalingListDataPresent {
		out.PScalingLists = lists
	}
	return out
}
