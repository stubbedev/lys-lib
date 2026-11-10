package av1

import (
	"errors"
	"testing"
)

// The OBU walk reads each unit's header and payload, and refuses units
// it cannot delimit.
func TestOBUsDelimitUnits(t *testing.T) {
	b := []byte{
		0x12, 0x00,
		0x0a, 0x02, 0xaa, 0xbb,
		0x36, 0x48, 0x81, 0x00, 0xcc,
	}
	walk := NewOBUs(b)
	var got []OBU
	for u, ok := walk.Next(); ok; u, ok = walk.Next() {
		got = append(got, u)
	}
	if err := walk.Err(); err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("%d units, want 3", len(got))
	}
	if got[0].Type != OBUTemporalDelimiter || len(got[0].Payload) != 0 {
		t.Errorf("unit 0 %+v, want an empty temporal delimiter", got[0])
	}
	if got[1].Type != OBUSequenceHeader || got[1].Offset != 2 || got[1].PayloadOffset != 4 || string(got[1].Payload) != "\xaa\xbb" {
		t.Errorf("unit 1 %+v, want a 2-byte sequence header at 2", got[1])
	}
	u := got[2]
	if u.Type != OBUFrame || !u.Extension || u.TemporalID != 2 || u.SpatialID != 1 || u.PayloadOffset != 10 || len(u.Payload) != 1 {
		t.Errorf("unit 2 %+v, want a frame of temporal layer 2, spatial layer 1, with a 2-byte leb128 size", u)
	}
	if u.InOperatingPoint(0x101) || !u.InOperatingPoint(0x204) {
		t.Error("a unit of layers 2 and 1 is in operating point 0x101 or not in 0x204")
	}
	for _, bad := range [][]byte{
		{0x92, 0x00},
		{0x10},
		{0x12, 0x05, 0x00},
		{0x12, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80},
	} {
		walk := NewOBUs(bad)
		if _, ok := walk.Next(); ok || !errors.Is(walk.Err(), ErrSyntax) {
			t.Errorf("% x: walk err %v, want ErrSyntax", bad, walk.Err())
		}
	}
}

// The descriptors read the values the specification's examples code.
func TestDescriptors(t *testing.T) {
	r := newReader([]byte{0b1110_1110})
	if v := r.su(4); v != -2 {
		t.Errorf("su(4) of 1110 = %d, want -2", v)
	}
	if v := r.ns(5); v != 4 {
		t.Errorf("ns(5) of 111 = %d, want 4", v)
	}
	r = newReader([]byte{0x34, 0x12})
	if v := r.le(2); v != 0x1234 {
		t.Errorf("le(2) = %#x, want 0x1234", v)
	}
	r = newReader([]byte{0b0001_0100})
	if v := r.uvlc(); v != 9 {
		t.Errorf("uvlc of 0001010 = %d, want 9", v)
	}
	if _, err := ConfigOBUs([]byte{0x01, 0, 0, 0}); !errors.Is(err, ErrSyntax) {
		t.Errorf("av1C without its marker: %v, want ErrSyntax", err)
	}
	if obus, err := ConfigOBUs([]byte{0x81, 0, 0, 0, 0x12, 0x00}); err != nil || len(obus) != 2 {
		t.Errorf("av1C configOBUs %x, %v", obus, err)
	}
}
