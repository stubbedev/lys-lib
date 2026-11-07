package edid

import (
	"errors"
	"fmt"
	"time"
)

const (
	tagRangeLimits         = 0xfd
	edidRevisionOffset     = 19
	displayIDVersion2      = 0x20
	displayIDAdaptiveSync  = 0x2b
	adaptiveSyncDescriptor = 6
	amdOUI                 = 0x00001a
	amdMinRefreshOffset    = 5
	amdMaxRefreshOffset    = 6
)

// VRRSource is the part of an EDID a VRRRange was read from. A larger
// value is a more specific source.
type VRRSource uint8

const (
	// VRRRangeLimits is the base block's display range limits descriptor.
	VRRRangeLimits VRRSource = iota + 1
	// VRRAMDVendorBlock is the CTA-861 AMD FreeSync vendor-specific data block.
	VRRAMDVendorBlock
	// VRRDisplayIDAdaptiveSync is DisplayID 2.x's Adaptive Sync data block.
	VRRDisplayIDAdaptiveSync
)

func (s VRRSource) String() string {
	switch s {
	case VRRRangeLimits:
		return "range limits descriptor"
	case VRRAMDVendorBlock:
		return "AMD vendor-specific data block"
	case VRRDisplayIDAdaptiveSync:
		return "DisplayID adaptive sync data block"
	}
	return fmt.Sprintf("VRRSource(%d)", uint8(s))
}

// VRRRange is the vertical refresh range a display can vary its rate
// within, in millihertz, and where the EDID says so.
type VRRRange struct {
	MinMilliHz, MaxMilliHz uint32
	Source                 VRRSource
}

// MaxFrameDuration is the longest the display holds one frame: the period
// of its minimum refresh rate.
func (r VRRRange) MaxFrameDuration() time.Duration {
	return time.Second * 1000 / time.Duration(r.MinMilliHz)
}

// ErrNoVRRRange is an EDID that states no variable refresh range.
var ErrNoVRRRange = errors.New("edid: no variable refresh range")

// ParseVRR reads b's variable refresh range from the most specific source
// it lists: DisplayID 2.x's Adaptive Sync data block, then the CTA-861 AMD
// vendor-specific data block, then the base block's range limits
// descriptor with EDID 1.4's 255 Hz offsets. A range whose minimum equals
// its maximum is a fixed rate and is no range. It fails with ErrNoVRRRange
// when no source states a range, and with ErrInvalid on a bad base block,
// an extension with a bad checksum, a data block that overruns or a range
// whose minimum is zero or above its maximum.
func ParseVRR(b []byte) (VRRRange, error) {
	if err := checkBase(b); err != nil {
		return VRRRange{}, err
	}
	var p vrrParse
	eachDisplayDescriptor(b, func(tag byte, d []byte) {
		if tag == tagRangeLimits {
			p.readRangeLimits(d, b[edidRevisionOffset])
		}
	})
	if err := eachExtension(b, tagCTA, "CTA-861", func(block []byte) error { return eachCTADataBlock(block, p.readCTADataBlock) }); err != nil {
		return VRRRange{}, err
	}
	if err := eachExtension(b, tagDisplayID, "DisplayID", func(block []byte) error {
		return eachDisplayIDDataBlock(block, p.readDisplayIDDataBlock)
	}); err != nil {
		return VRRRange{}, err
	}
	if p.err != nil {
		return VRRRange{}, p.err
	}
	if p.best.Source == 0 {
		return VRRRange{}, ErrNoVRRRange
	}
	return p.best, nil
}

type vrrParse struct {
	best VRRRange
	err  error
}

func (p *vrrParse) offer(source VRRSource, minHz, maxHz uint32) {
	if p.err != nil || source <= p.best.Source {
		return
	}
	if minHz == 0 || minHz > maxHz {
		p.err = fmt.Errorf("%w: the %s states a refresh range of %d-%d Hz", ErrInvalid, source, minHz, maxHz)
		return
	}
	if minHz == maxHz {
		return
	}
	p.best = VRRRange{MinMilliHz: minHz * 1000, MaxMilliHz: maxHz * 1000, Source: source}
}

func (p *vrrParse) readRangeLimits(d []byte, revision byte) {
	minHz, maxHz := uint32(d[5]), uint32(d[6])
	if revision >= 4 && d[4]&0x02 != 0 {
		maxHz += 255
		if d[4]&0x01 != 0 {
			minHz += 255
		}
	}
	p.offer(VRRRangeLimits, minHz, maxHz)
}

func (p *vrrParse) readCTADataBlock(tag byte, data []byte) {
	if tag != ctaVendorTag || len(data) <= amdMaxRefreshOffset {
		return
	}
	if uint32(data[0])|uint32(data[1])<<8|uint32(data[2])<<16 != amdOUI {
		return
	}
	p.offer(VRRAMDVendorBlock, uint32(data[amdMinRefreshOffset]), uint32(data[amdMaxRefreshOffset]))
}

func (p *vrrParse) readDisplayIDDataBlock(version, tag, revision byte, payload []byte) {
	if version < displayIDVersion2 || tag != displayIDAdaptiveSync {
		return
	}
	size := adaptiveSyncDescriptor + int(revision>>4&0x7)
	var chosen []byte
	for at := 0; at+size <= len(payload); at += size {
		d := payload[at : at+size]
		if chosen == nil || d[0]&1 != 0 && chosen[0]&1 == 0 {
			chosen = d
		}
	}
	if chosen == nil {
		return
	}
	p.offer(VRRDisplayIDAdaptiveSync, uint32(chosen[2]), 1+(uint32(chosen[3])|uint32(chosen[4]&3)<<8))
}
