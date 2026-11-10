// Package av1 reads the AV1 bitstream (AV1 Bitstream & Decoding Process
// Specification, version 1.0.0 with Errata 1) as far as a decoder that
// keeps its own reference state needs: the open bitstream units, the
// sequence header, the frame headers with the reference frame state
// their syntax predicts from, and the tile groups' layout. Decoding the
// tiles is the hardware's (Vulkan Video's).
package av1

import (
	"errors"
	"fmt"

	"github.com/stubbedev/lys-lib/intconv"
)

// ErrSyntax is an AV1 stream a decoder cannot read: malformed, or using
// syntax outside what it reads. Errors wrap it with what was read.
var ErrSyntax = errors.New("av1: stream")

// OBUType is an open bitstream unit's obu_type (6.2.2).
type OBUType uint8

// The OBU types.
const (
	OBUSequenceHeader       OBUType = 1
	OBUTemporalDelimiter    OBUType = 2
	OBUFrameHeader          OBUType = 3
	OBUTileGroup            OBUType = 4
	OBUMetadata             OBUType = 5
	OBUFrame                OBUType = 6
	OBURedundantFrameHeader OBUType = 7
	OBUTileList             OBUType = 8
	OBUPadding              OBUType = 15
)

// OBU is an open bitstream unit (5.3): its type, its extension's layer
// IDs when it has one, and its payload, a slice of the bytes it was read
// from; Offset and PayloadOffset are where in them the unit and its
// payload start.
type OBU struct {
	Type          OBUType
	Extension     bool
	TemporalID    uint8
	SpatialID     uint8
	Offset        int
	PayloadOffset int
	Payload       []byte
}

// OBUs walks the units of a temporal unit in the low overhead bitstream
// format (5.2), which every unit's size field delimits.
type OBUs struct {
	b   []byte
	at  int
	err error
}

// NewOBUs walks b.
func NewOBUs(b []byte) OBUs { return OBUs{b: b} }

// Next is the next unit, false at the end or at a unit that does not
// parse, which Err then says.
func (o *OBUs) Next() (OBU, bool) {
	if o.err != nil || o.at >= len(o.b) {
		return OBU{}, false
	}
	u, n, err := parseOBU(o.b[o.at:])
	if err != nil {
		o.err = fmt.Errorf("OBU at byte %d of %d: %w", o.at, len(o.b), err)
		return OBU{}, false
	}
	u.Offset, u.PayloadOffset = o.at, o.at+n-len(u.Payload)
	o.at += n
	return u, true
}

// Err is why the walk stopped early, nil at the end of the units.
func (o *OBUs) Err() error { return o.err }

// parseOBU reads the unit b starts with and how many bytes it takes.
func parseOBU(b []byte) (OBU, int, error) {
	if len(b) == 0 {
		return OBU{}, 0, fmt.Errorf("%w: empty OBU", ErrSyntax)
	}
	h := b[0]
	if h&0x80 != 0 {
		return OBU{}, 0, fmt.Errorf("%w: OBU header %#02x has its forbidden bit set", ErrSyntax, h)
	}
	u := OBU{Type: OBUType(h >> 3 & 0xf), Extension: h&4 != 0}
	n := 1
	if u.Extension {
		if len(b) < 2 {
			return OBU{}, 0, fmt.Errorf("%w: OBU ends inside its extension header", ErrSyntax)
		}
		u.TemporalID, u.SpatialID = b[1]>>5, b[1]>>3&3
		n++
	}
	if h&2 == 0 {
		return OBU{}, 0, fmt.Errorf("%w: OBU type %d has no size field, which the low overhead format needs", ErrSyntax, u.Type)
	}
	size, m, err := leb128(b[n:])
	if err != nil {
		return OBU{}, 0, err
	}
	n += m
	if size > intconv.Must[uint64](len(b)-n) {
		return OBU{}, 0, fmt.Errorf("%w: OBU type %d of %d bytes in %d", ErrSyntax, u.Type, size, len(b)-n)
	}
	end := n + intconv.Must[int](size)
	u.Payload = b[n:end]
	return u, end, nil
}

// leb128 reads leb128() (4.10.5): the value and its bytes.
func leb128(b []byte) (uint64, int, error) {
	var v uint64
	for i := range 8 {
		if i >= len(b) {
			return 0, 0, fmt.Errorf("%w: leb128 ends after %d bytes", ErrSyntax, i)
		}
		v |= uint64(b[i]&0x7f) << (7 * i)
		if b[i]&0x80 == 0 {
			if v > 1<<32-1 {
				return 0, 0, fmt.Errorf("%w: leb128 value %d past 2^32-1", ErrSyntax, v)
			}
			return v, i + 1, nil
		}
	}
	return 0, 0, fmt.Errorf("%w: leb128 longer than 8 bytes", ErrSyntax)
}

// InOperatingPoint reports whether u belongs to the operating point
// whose operating_point_idc is idc, the units a decoder of it drops
// otherwise (7.1, the operating point selection's drop rule).
func (u *OBU) InOperatingPoint(idc uint16) bool {
	if u.Type == OBUSequenceHeader || u.Type == OBUTemporalDelimiter || idc == 0 || !u.Extension {
		return true
	}
	return idc>>u.TemporalID&1 != 0 && idc>>(u.SpatialID+8)&1 != 0
}

// ConfigOBUs is the configOBUs of av1C, an AV1CodecConfigurationRecord
// (AV1 Codec ISO Media File Format Binding 2.3.3, which Matroska's
// CodecPrivate carries too): the sequence header and metadata units a
// decoder starts with.
func ConfigOBUs(av1C []byte) ([]byte, error) {
	if len(av1C) < 4 {
		return nil, fmt.Errorf("%w: av1C of %d bytes", ErrSyntax, len(av1C))
	}
	if av1C[0] != 0x81 {
		return nil, fmt.Errorf("%w: av1C marker and version %#02x, want 0x81", ErrSyntax, av1C[0])
	}
	return av1C[4:], nil
}
