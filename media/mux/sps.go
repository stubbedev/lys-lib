package mux

import (
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/stubbedev/lys-lib/intconv"
	"github.com/stubbedev/lys-lib/internal/nal"
)

type avcSPS struct {
	profile, compat, level uint8
	chroma                 uint32
	depthLuma, depthChroma uint32
	width, height          int
}

func parseAVCSPS(unit []byte) (avcSPS, error) {
	if len(unit) < 4 || nal.H264Type(unit) != nal.H264NALSPS {
		return avcSPS{}, errors.New("mux: H.264 SPS is not an SPS NAL unit")
	}
	var sps nal.H264SPS
	if err := sps.Parse(nal.Unescape(nil, unit[1:])); err != nil {
		return avcSPS{}, fmt.Errorf("mux: %w", err)
	}
	s := avcSPS{
		profile: sps.ProfileIdc, compat: sps.Constraints, level: sps.LevelIdc,
		chroma: uint32(sps.ChromaFormatIdc), depthLuma: uint32(sps.BitDepthLumaMinus8), depthChroma: uint32(sps.BitDepthChromaMinus8),
	}
	if err := checkFormat(s.chroma, s.depthLuma, s.depthChroma); err != nil {
		return avcSPS{}, fmt.Errorf("mux: H.264 SPS %w", err)
	}
	x0, y0, x1, y1 := sps.CropRect()
	s.width, s.height = x1-x0, y1-y0
	return s, nil
}

func checkFormat(chroma, depthLuma, depthChroma uint32) error {
	if chroma > 3 {
		return fmt.Errorf("chroma_format_idc %d is past 3", chroma)
	}
	if depthLuma > 8 || depthChroma > 8 {
		return fmt.Errorf("bit depths %d and %d (minus 8) are past 8", depthLuma, depthChroma)
	}
	return nil
}

func formatBytes(chroma, depthLuma, depthChroma uint32) (byte, byte, byte) {
	return 0xfc | uint8(chroma&3), 0xf8 | uint8(depthLuma&7), 0xf8 | uint8(depthChroma&7)
}

func avcC(s avcSPS, sps, pps []byte) ([]byte, error) {
	b := []byte{1, s.profile, s.compat, s.level, 0xfc | (nalLenSize - 1), 0xe0 | 1}
	b, err := appendNAL16(b, sps)
	if err != nil {
		return nil, fmt.Errorf("mux: H.264 SPS: %w", err)
	}
	b = append(b, 1)
	if b, err = appendNAL16(b, pps); err != nil {
		return nil, fmt.Errorf("mux: H.264 PPS: %w", err)
	}
	if nal.H264HighProfile(s.profile) {
		chroma, luma, depthChroma := formatBytes(s.chroma, s.depthLuma, s.depthChroma)
		b = append(b, chroma, luma, depthChroma, 0)
	}
	return b, nil
}

func appendNAL16(b, unit []byte) ([]byte, error) {
	size, err := intconv.Exact[uint16](len(unit))
	if err != nil {
		return nil, fmt.Errorf("%d bytes is past the 16-bit parameter set length: %w", len(unit), err)
	}
	b = binary.BigEndian.AppendUint16(b, size)
	return append(b, unit...), nil
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

func parseHEVCSPS(unit []byte) (hevcSPS, error) {
	if len(unit) < 3+hevcPTLGeneral || nal.HEVCType(unit) != nal.HEVCNALSPS {
		return hevcSPS{}, errors.New("mux: H.265 SPS is not an SPS NAL unit")
	}
	rbsp := nal.Unescape(nil, unit[2:])
	if len(rbsp) < 1+hevcPTLGeneral {
		return hevcSPS{}, errors.New("mux: H.265 SPS ends inside its profile, tier and level")
	}
	var s hevcSPS
	subLayers := int(rbsp[0] >> 1 & 7)
	s.temporalLayers, s.temporalNested = uint8(subLayers+1), rbsp[0]&1 == 1
	copy(s.ptl[:], rbsp[1:])
	r := nal.NewReader(rbsp)
	r.Skip(8 * (1 + hevcPTLGeneral))
	profile, level := make([]bool, subLayers), make([]bool, subLayers)
	for i := range subLayers {
		profile[i], level[i] = r.Flag(), r.Flag()
	}
	if subLayers > 0 {
		r.Skip(2 * (8 - subLayers))
	}
	for i := range subLayers {
		if profile[i] {
			r.Skip(88)
		}
		if level[i] {
			r.Skip(8)
		}
	}
	r.UE()
	separate := false
	if s.chroma = r.UE(); s.chroma == 3 {
		separate = r.Flag()
	}
	width, height := int(r.UE()), int(r.UE())
	var crop [4]int
	if r.Flag() {
		for i := range crop {
			crop[i] = int(r.UE())
		}
	}
	s.depthLuma, s.depthChroma = r.UE(), r.UE()
	if r.Short() {
		return hevcSPS{}, errors.New("mux: H.265 SPS ends before the bit depths")
	}
	if err := checkFormat(s.chroma, s.depthLuma, s.depthChroma); err != nil {
		return hevcSPS{}, fmt.Errorf("mux: H.265 SPS %w", err)
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

func hvcC(s hevcSPS, vps, sps, pps []byte) ([]byte, error) {
	b := append([]byte{1}, s.ptl[:]...)
	chroma, luma, depthChroma := formatBytes(s.chroma, s.depthLuma, s.depthChroma)
	b = append(b, 0xf0, 0, 0xfc, chroma, luma, depthChroma, 0, 0)
	nested := uint8(0)
	if s.temporalNested {
		nested = 1
	}
	b = append(b, s.temporalLayers<<3|nested<<2|(nalLenSize-1), 3)
	for _, ps := range [][]byte{vps, sps, pps} {
		b = append(b, 0x80|nal.HEVCType(ps), 0, 1)
		var err error
		if b, err = appendNAL16(b, ps); err != nil {
			return nil, fmt.Errorf("mux: H.265 parameter set: %w", err)
		}
	}
	return b, nil
}
