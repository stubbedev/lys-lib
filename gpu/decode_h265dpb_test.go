package gpu

import (
	"errors"
	"slices"
	"testing"

	"github.com/stubbedev/lys-lib/internal/nal"
)

// h265Run decodes pictures through a h265State as the decoder does.
type h265Run struct {
	t     *testing.T
	sps   nal.HEVCSPS
	state h265State
}

func newH265Run(t *testing.T) *h265Run {
	r := &h265Run{t: t, sps: nal.HEVCSPS{Log2MaxPocLsb: 4}}
	r.state.slots = 5
	return r
}

// rps is a slice-coded short-term set of the delta POCs given, all
// used.
func rps(neg, pos []int32) nal.HEVCShortTermRPS {
	var s nal.HEVCShortTermRPS
	s.NumNegative, s.NumPositive = uint8(len(neg)), uint8(len(pos))
	copy(s.DeltaPocS0[:], neg)
	copy(s.DeltaPocS1[:], pos)
	s.UsedS0, s.UsedS1 = 1<<len(neg)-1, 1<<len(pos)-1
	return s
}

func (r *h265Run) pic(nalType uint8, lsb uint32, set nal.HEVCShortTermRPS) (h265Pic, error) {
	h := nal.HEVCSliceHeader{PicOrderCntLsb: lsb, RPS: set, PicOutput: true}
	p := h265Pic{hdr: &h, sps: &r.sps, nalType: nalType}
	if err := r.state.begin(&p); err != nil {
		return p, err
	}
	r.state.finish(&p)
	return p, nil
}

func (r *h265Run) must(nalType uint8, lsb uint32, set nal.HEVCShortTermRPS) h265Pic {
	r.t.Helper()
	p, err := r.pic(nalType, lsb, set)
	if err != nil {
		r.t.Fatal(err)
	}
	return p
}

func (r *h265Run) refs() []int32 {
	var out []int32
	for _, ref := range r.state.refs {
		if ref.used {
			out = append(out, ref.poc)
		}
	}
	slices.Sort(out)
	return out
}

// Picture order counts carry on past pic_order_cnt_lsb's wrap, and the
// reference picture set keeps the pictures it names, marking the rest
// unused, its current lists naming their slots.
func TestH265PictureOrderAndReferenceSets(t *testing.T) {
	r := newH265Run(t)
	r.must(nal.HEVCNALIDRWRAD, 0, nal.HEVCShortTermRPS{})
	var pocs []int32
	for _, lsb := range []uint32{4, 8, 12, 0} {
		pocs = append(pocs, r.must(1, lsb, rps([]int32{-4}, nil)).poc)
	}
	pocs = append(pocs, r.must(1, 2, rps([]int32{-2}, nil)).poc)
	p := r.must(1, 6, rps([]int32{-4, -6}, nil))
	pocs = append(pocs, p.poc)
	if p.nBefore != 2 || r.state.refs[p.before[0]].poc != 18 || r.state.refs[p.before[1]].poc != 16 {
		t.Errorf("current list %v, want the slots of 18 and 16", p.before[:p.nBefore])
	}
	if want := []int32{4, 8, 12, 16, 18, 22}; !slices.Equal(pocs, want) {
		t.Errorf("POC %v, want %v", pocs, want)
	}
	if got := r.refs(); !slices.Equal(got, []int32{16, 18, 22}) {
		t.Errorf("references %v, want 16, 18 and 22", got)
	}
	if _, err := r.pic(1, 10, rps([]int32{-3}, nil)); err == nil || errors.Is(err, errSkipPicture) {
		t.Errorf("a picture referencing one not decoded: %v, want an error", err)
	}
}

// Decoding starts at an IRAP picture: pictures before it are dropped,
// and so are the RASL pictures of a CRA picture it starts at, but not
// those of a later CRA picture.
func TestH265StartsAtAnIRAP(t *testing.T) {
	r := newH265Run(t)
	if _, err := r.pic(1, 3, nal.HEVCShortTermRPS{}); !errors.Is(err, errSkipPicture) {
		t.Fatalf("a picture before any IRAP: %v, want errSkipPicture", err)
	}
	cra := r.must(nal.HEVCNALCRA, 8, nal.HEVCShortTermRPS{})
	if !cra.noRaslOutput || cra.poc != 8 {
		t.Fatalf("starting CRA %+v, want NoRaslOutputFlag and POC 8", cra)
	}
	if _, err := r.pic(nal.HEVCNALRASLN, 6, rps([]int32{-4}, []int32{2})); !errors.Is(err, errSkipPicture) {
		t.Fatalf("a RASL picture of the starting CRA: %v, want errSkipPicture", err)
	}
	r.must(1, 12, rps([]int32{-4}, nil))
	later := r.must(nal.HEVCNALCRA, 0, rps([]int32{-4}, nil))
	if later.noRaslOutput || later.poc != 16 {
		t.Fatalf("later CRA %+v, want no NoRaslOutputFlag and POC 16", later)
	}
	if _, err := r.pic(nal.HEVCNALRASLR, 14, rps([]int32{-2}, []int32{2})); err != nil {
		t.Errorf("a RASL picture of a later CRA: %v", err)
	}
}

// A long-term picture named by its POC LSBs, or with its MSB cycle by
// its whole POC, is kept and marked long-term.
func TestH265LongTermReference(t *testing.T) {
	r := newH265Run(t)
	r.must(nal.HEVCNALIDRWRAD, 0, nal.HEVCShortTermRPS{})
	h := nal.HEVCSliceHeader{PicOrderCntLsb: 4, NumLongTermPics: 1, PicOutput: true}
	h.LongTerm[0] = nal.HEVCLongTerm{PocLsb: 0, UsedByCurrPic: true}
	p := h265Pic{hdr: &h, sps: &r.sps, nalType: 1}
	if err := r.state.begin(&p); err != nil {
		t.Fatal(err)
	}
	r.state.finish(&p)
	if p.nLt != 1 || !r.state.refs[p.lt[0]].long || r.state.refs[p.lt[0]].poc != 0 {
		t.Fatalf("long-term list %v, want picture 0 marked long-term", p.lt[:p.nLt])
	}
	for _, lsb := range []uint32{8, 12, 0} {
		h = nal.HEVCSliceHeader{PicOrderCntLsb: lsb, NumLongTermPics: 1, PicOutput: true, RPS: rps([]int32{-4}, nil)}
		h.LongTerm[0] = nal.HEVCLongTerm{PocLsb: 0, UsedByCurrPic: true, MsbPresent: true, DeltaPocMsbCycle: 1}
		if lsb != 0 {
			h.LongTerm[0].DeltaPocMsbCycle = 0
		}
		p = h265Pic{hdr: &h, sps: &r.sps, nalType: 1}
		if err := r.state.begin(&p); err != nil {
			t.Fatalf("picture at LSB %d: %v", lsb, err)
		}
		r.state.finish(&p)
	}
	if p.poc != 16 || p.nLt != 1 || r.state.refs[p.lt[0]].poc != 0 {
		t.Errorf("picture %d's long-term list %v, want picture 0 by its whole POC", p.poc, p.lt[:p.nLt])
	}
}
