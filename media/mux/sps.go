package mux

import (
	"encoding/binary"
	"errors"
	"fmt"
)

type bitReader struct {
	b     []byte
	i     int
	short bool
}

func (r *bitReader) u(n int) uint32 {
	var v uint32
	for range n {
		if r.i >= 8*len(r.b) {
			r.short = true
			return 0
		}
		v = v<<1 | uint32(r.b[r.i>>3]>>(7-r.i&7)&1)
		r.i++
	}
	return v
}

func (r *bitReader) skip(n int) {
	r.i += n
	if r.i > 8*len(r.b) {
		r.short = true
	}
}

func (r *bitReader) ue() uint32 {
	zeros := 0
	for r.u(1) == 0 {
		if zeros++; zeros > 31 || r.short {
			r.short = true
			return 0
		}
	}
	return 1<<zeros - 1 + r.u(zeros)
}

func (r *bitReader) se() int32 {
	k := r.ue()
	if k&1 == 1 {
		return int32(k/2 + 1)
	}
	return -int32(k / 2)
}

func unescapeRBSP(nal []byte) []byte {
	out := make([]byte, 0, len(nal))
	zeros := 0
	for _, c := range nal {
		if zeros >= 2 && c == 3 {
			zeros = 0
			continue
		}
		if c == 0 {
			zeros++
		} else {
			zeros = 0
		}
		out = append(out, c)
	}
	return out
}

type avcSPS struct {
	profile, compat, level uint8
	chroma                 uint32
	depthLuma, depthChroma uint32
	width, height          int
}

var avcHighProfiles = map[uint8]bool{100: true, 110: true, 122: true, 244: true, 44: true, 83: true, 86: true, 118: true, 128: true, 138: true, 139: true, 134: true, 135: true}

func parseAVCSPS(nal []byte) (avcSPS, error) {
	if len(nal) < 4 || nalTypeH264(nal) != nalH264SPS {
		return avcSPS{}, errors.New("mux: H.264 SPS is not an SPS NAL unit")
	}
	r := bitReader{b: unescapeRBSP(nal[1:])}
	s := avcSPS{profile: uint8(r.u(8)), compat: uint8(r.u(8)), level: uint8(r.u(8)), chroma: 1}
	r.ue()
	separate := false
	if avcHighProfiles[s.profile] {
		if s.chroma = r.ue(); s.chroma == 3 {
			separate = r.u(1) == 1
		}
		s.depthLuma, s.depthChroma = r.ue(), r.ue()
		r.skip(1)
		if r.u(1) == 1 {
			lists := 8
			if s.chroma == 3 {
				lists = 12
			}
			for i := range lists {
				size := 64
				if i < 6 {
					size = 16
				}
				if r.u(1) == 1 {
					skipScalingList(&r, size)
				}
			}
		}
	}
	r.ue()
	switch r.ue() {
	case 0:
		r.ue()
	case 1:
		r.skip(1)
		r.se()
		r.se()
		for range r.ue() {
			r.se()
		}
	}
	r.ue()
	r.skip(1)
	mbsWide, mapUnitsHigh := int(r.ue())+1, int(r.ue())+1
	frameMbsOnly := int(r.u(1))
	if frameMbsOnly == 0 {
		r.skip(1)
	}
	r.skip(1)
	var crop [4]int
	if r.u(1) == 1 {
		for i := range crop {
			crop[i] = int(r.ue())
		}
	}
	if r.short {
		return avcSPS{}, errors.New("mux: H.264 SPS ends before the frame size")
	}
	cropX, cropY := 1, 2-frameMbsOnly
	if !separate && s.chroma != 0 {
		subW, subH := 2, 2
		switch s.chroma {
		case 2:
			subH = 1
		case 3:
			subW, subH = 1, 1
		}
		cropX, cropY = subW, subH*(2-frameMbsOnly)
	}
	s.width = mbsWide*16 - cropX*(crop[0]+crop[1])
	s.height = (2-frameMbsOnly)*mapUnitsHigh*16 - cropY*(crop[2]+crop[3])
	if s.width <= 0 || s.height <= 0 {
		return avcSPS{}, fmt.Errorf("mux: H.264 SPS frame size %dx%d is not positive", s.width, s.height)
	}
	return s, nil
}

func skipScalingList(r *bitReader, size int) {
	last, next := int32(8), int32(8)
	for range size {
		if next != 0 {
			next = (last + r.se() + 256) % 256
		}
		if next != 0 {
			last = next
		}
	}
}

func avcC(s avcSPS, sps, pps []byte) []byte {
	b := []byte{1, s.profile, s.compat, s.level, 0xfc | (nalLenSize - 1), 0xe0 | 1}
	b = appendNAL16(b, sps)
	b = append(b, 1)
	b = appendNAL16(b, pps)
	if avcHighProfiles[s.profile] {
		b = append(b, 0xfc|uint8(s.chroma), 0xf8|uint8(s.depthLuma), 0xf8|uint8(s.depthChroma), 0)
	}
	return b
}

func appendNAL16(b, nal []byte) []byte {
	b = binary.BigEndian.AppendUint16(b, uint16(len(nal)))
	return append(b, nal...)
}

const hevcPTLGeneral = 12

type hevcSPS struct {
	ptl                    [hevcPTLGeneral]byte
	temporalLayers         uint8
	temporalNested         bool
	chroma                 uint32
	depthLuma, depthChroma uint32
	width, height          int
}

func parseHEVCSPS(nal []byte) (hevcSPS, error) {
	if len(nal) < 3+hevcPTLGeneral || nalTypeH265(nal) != nalH265SPS {
		return hevcSPS{}, errors.New("mux: H.265 SPS is not an SPS NAL unit")
	}
	rbsp := unescapeRBSP(nal[2:])
	if len(rbsp) < 1+hevcPTLGeneral {
		return hevcSPS{}, errors.New("mux: H.265 SPS ends inside its profile, tier and level")
	}
	var s hevcSPS
	subLayers := int(rbsp[0] >> 1 & 7)
	s.temporalLayers, s.temporalNested = uint8(subLayers+1), rbsp[0]&1 == 1
	copy(s.ptl[:], rbsp[1:])
	r := bitReader{b: rbsp, i: 8 * (1 + hevcPTLGeneral)}
	profile, level := make([]bool, subLayers), make([]bool, subLayers)
	for i := range subLayers {
		profile[i], level[i] = r.u(1) == 1, r.u(1) == 1
	}
	if subLayers > 0 {
		r.skip(2 * (8 - subLayers))
	}
	for i := range subLayers {
		if profile[i] {
			r.skip(88)
		}
		if level[i] {
			r.skip(8)
		}
	}
	r.ue()
	separate := false
	if s.chroma = r.ue(); s.chroma == 3 {
		separate = r.u(1) == 1
	}
	width, height := int(r.ue()), int(r.ue())
	var crop [4]int
	if r.u(1) == 1 {
		for i := range crop {
			crop[i] = int(r.ue())
		}
	}
	s.depthLuma, s.depthChroma = r.ue(), r.ue()
	if r.short {
		return hevcSPS{}, errors.New("mux: H.265 SPS ends before the bit depths")
	}
	subW, subH := 1, 1
	if !separate {
		switch s.chroma {
		case 1:
			subW, subH = 2, 2
		case 2:
			subW = 2
		}
	}
	s.width = width - subW*(crop[0]+crop[1])
	s.height = height - subH*(crop[2]+crop[3])
	if s.width <= 0 || s.height <= 0 {
		return hevcSPS{}, fmt.Errorf("mux: H.265 SPS frame size %dx%d is not positive", s.width, s.height)
	}
	return s, nil
}

func hvcC(s hevcSPS, vps, sps, pps []byte) []byte {
	b := append([]byte{1}, s.ptl[:]...)
	b = append(b, 0xf0, 0, 0xfc, 0xfc|uint8(s.chroma), 0xf8|uint8(s.depthLuma), 0xf8|uint8(s.depthChroma), 0, 0)
	nested := uint8(0)
	if s.temporalNested {
		nested = 1
	}
	b = append(b, s.temporalLayers<<3|nested<<2|(nalLenSize-1), 3)
	for _, ps := range [][]byte{vps, sps, pps} {
		b = append(b, 0x80|nalTypeH265(ps), 0, 1)
		b = appendNAL16(b, ps)
	}
	return b
}
