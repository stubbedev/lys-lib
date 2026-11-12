package gpu

import (
	"fmt"

	"github.com/stubbedev/lys-lib/intconv"
	"github.com/stubbedev/lys-lib/internal/nal"
)

// h265MaxSlots are the DPB slots an H.265 stream can need: its 16
// pictures, the one being decoded among them, and one spare.
const h265MaxSlots = 17

// h265Ref is a DPB slot's picture as reference picture marking (8.3.2)
// sees it.
type h265Ref struct {
	poc        int32
	used, long bool
}

// h265Pic is the picture being decoded: its first slice segment's
// header and what decoding it works out.
type h265Pic struct {
	hdr     *nal.HEVCSliceHeader
	sps     *nal.HEVCSPS
	nalType uint8
	tid     uint8
	poc     int32
	slot    int
	// noRaslOutput is NoRaslOutputFlag of an IRAP picture.
	noRaslOutput bool
	// before, after and lt are the DPB slots of RefPicSetStCurrBefore,
	// RefPicSetStCurrAfter and RefPicSetLtCurr.
	before, after, lt    [8]int
	nBefore, nAfter, nLt int
}

func (p *h265Pic) irap() bool {
	return p.nalType >= nal.HEVCNALBLAWLP && p.nalType <= nal.HEVCNALIRAPMax
}

func (p *h265Pic) idr() bool {
	return p.nalType == nal.HEVCNALIDRWRAD || p.nalType == nal.HEVCNALIDRNLP
}

func (p *h265Pic) rasl() bool {
	return p.nalType == nal.HEVCNALRASLN || p.nalType == nal.HEVCNALRASLR
}

// skipsTid0 reports a sub-layer non-reference picture, RADL or RASL:
// none of which counts as prevTid0Pic (8.3.1).
func (p *h265Pic) skipsTid0() bool {
	t := p.nalType
	return t <= 14 && t%2 == 0 || t >= nal.HEVCNALRADLN && t <= nal.HEVCNALRASLR
}

// output is whether the picture is presented: PicOutputFlag.
func (p *h265Pic) output() bool { return p.hdr.PicOutput }

// h265State is the reference pictures of a stream being decoded, by
// DPB slot, and the picture order count state.
type h265State struct {
	refs [h265MaxSlots]h265Ref
	// prevTid0 is prevTid0Pic's PicOrderCntVal.
	prevTid0 int32
	// started is set once an IRAP picture began decoding; skipRASL
	// while the last IRAP picture's RASL pictures predict from
	// pictures not decoded.
	started  bool
	skipRASL bool
}

// reset forgets every reference picture: what a seek does.
func (s *h265State) reset() { *s = h265State{} }

// decoded is the state once p has decoded: it works out p's picture
// order count (8.3.1), its reference picture set (8.3.2), marking the
// pictures it leaves out unused, and its DPB slot among the first
// slots, which p then holds as a short-term reference picture. s stays
// as it was, and is what an error returns, so a picture that fails to
// decode leaves no trace and works out the same when given again. It
// returns errSkipPicture for a picture before the first IRAP picture or
// a RASL picture of an IRAP picture decoding started at.
func (s h265State) decoded(p *h265Pic, slots int) (h265State, error) {
	next := s
	if !next.started && !p.irap() {
		return s, errSkipPicture
	}
	if p.irap() {
		p.noRaslOutput = !next.started || p.idr() || p.nalType <= nal.HEVCNALBLAWLP+2
		next.skipRASL = p.noRaslOutput
	}
	if p.rasl() && next.skipRASL {
		return s, errSkipPicture
	}
	maxLsb := int32(1) << p.sps.Log2MaxPocLsb
	lsb := intconv.Must[int32](p.hdr.PicOrderCntLsb)
	var msb int32
	if !p.irap() || !p.noRaslOutput {
		prevLsb := next.prevTid0 & (maxLsb - 1)
		msb = next.prevTid0 - prevLsb
		switch {
		case lsb < prevLsb && prevLsb-lsb >= maxLsb/2:
			msb += maxLsb
		case lsb > prevLsb && lsb-prevLsb > maxLsb/2:
			msb -= maxLsb
		}
	}
	p.poc = msb + lsb
	if p.tid == 0 && !p.skipsTid0() {
		next.prevTid0 = p.poc
	}
	if err := next.referencePictures(p, maxLsb); err != nil {
		return s, err
	}
	for i := range slots {
		if !next.refs[i].used {
			p.slot = i
			next.started = true
			next.refs[i] = h265Ref{poc: p.poc, used: true}
			return next, nil
		}
	}
	return s, fmt.Errorf("gpu: H.265 picture %d finds all %d DPB slots holding reference pictures", p.poc, slots)
}

// referencePictures derives p's reference picture set: the slots of its
// current lists, the long-term ones marked so, and every reference
// picture in none of its five lists marked unused. A picture a current
// list names that the DPB lacks is an error.
func (s *h265State) referencePictures(p *h265Pic, maxLsb int32) error {
	p.nBefore, p.nAfter, p.nLt = 0, 0, 0
	if p.idr() || p.irap() && p.noRaslOutput {
		for i := range s.refs {
			s.refs[i] = h265Ref{}
		}
		return nil
	}
	var keep [h265MaxSlots]bool
	h := p.hdr
	var cycle uint32
	for i := range int(h.NumLongTermSPS + h.NumLongTermPics) {
		lt := &h.LongTerm[i]
		if i != 0 && i != int(h.NumLongTermSPS) {
			cycle += lt.DeltaPocMsbCycle
		} else {
			cycle = lt.DeltaPocMsbCycle
		}
		poc, mask := intconv.Must[int32](lt.PocLsb), maxLsb-1
		if lt.MsbPresent {
			poc += p.poc - intconv.Must[int32](cycle)*maxLsb - p.poc&(maxLsb-1)
			mask = -1
		}
		slot := s.find(func(r *h265Ref) bool { return r.poc&mask == poc })
		if slot < 0 {
			if lt.UsedByCurrPic {
				return fmt.Errorf("gpu: H.265 picture %d references long-term picture %d, which is not decoded", p.poc, poc)
			}
			continue
		}
		keep[slot], s.refs[slot].long = true, true
		if lt.UsedByCurrPic {
			if p.nLt == len(p.lt) {
				return fmt.Errorf("gpu: H.265 picture %d references more than %d long-term pictures", p.poc, len(p.lt))
			}
			p.lt[p.nLt] = slot
			p.nLt++
		}
	}
	rps := h.ShortTermRPS(p.sps)
	for i := range int(rps.NumNegative) {
		if err := s.shortTerm(p, p.poc+rps.DeltaPocS0[i], rps.UsedS0&(1<<i) != 0, &keep, p.before[:], &p.nBefore); err != nil {
			return err
		}
	}
	for i := range int(rps.NumPositive) {
		if err := s.shortTerm(p, p.poc+rps.DeltaPocS1[i], rps.UsedS1&(1<<i) != 0, &keep, p.after[:], &p.nAfter); err != nil {
			return err
		}
	}
	for i := range s.refs {
		if !keep[i] {
			s.refs[i].used = false
		}
	}
	return nil
}

// shortTerm finds the short-term picture at poc, kept, and when used
// adds its slot to list.
func (s *h265State) shortTerm(p *h265Pic, poc int32, used bool, keep *[h265MaxSlots]bool, list []int, n *int) error {
	slot := s.find(func(r *h265Ref) bool { return !r.long && r.poc == poc })
	if slot < 0 {
		if used {
			return fmt.Errorf("gpu: H.265 picture %d references picture %d, which is not decoded", p.poc, poc)
		}
		return nil
	}
	keep[slot] = true
	if !used {
		return nil
	}
	if *n == len(list) {
		return fmt.Errorf("gpu: H.265 picture %d references more than %d short-term pictures on one side", p.poc, len(list))
	}
	list[*n] = slot
	*n++
	return nil
}

// find is the slot of the reference picture match takes, -1 for none.
func (s *h265State) find(match func(*h265Ref) bool) int {
	for i := range s.refs {
		if s.refs[i].used && match(&s.refs[i]) {
			return i
		}
	}
	return -1
}
