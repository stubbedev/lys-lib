package dbus

import (
	"bytes"
	"encoding/binary"
	"reflect"
	"testing"
)

func TestSigAlignmentMatchesTheTypes(t *testing.T) {
	for _, sig := range []string{"y", "n", "q", "i", "u", "x", "t", "d", "s", "o", "g", "v", "h", "ay", "as", "a{sv}", "a(ii)"} {
		if got, want := sigAlignment(sig), alignment(typeFor(sig)); got != want {
			t.Errorf("%q aligns to %d, want %d", sig, got, want)
		}
	}
	for sig, want := range map[string]int{"b": 4, "(ii)": 8, "(y)": 8, "{sv}": 8} {
		if got := sigAlignment(sig); got != want {
			t.Errorf("%q aligns to %d on the wire, want %d", sig, got, want)
		}
	}
}

// A bool between narrower values travels as a four-aligned uint32, and
// an array of them decodes element by element.
func TestBoolsRoundTripAtTheirWireAlignment(t *testing.T) {
	for _, v := range []any{true, []bool{true, false, true}} {
		msg := new(bytes.Buffer)
		enc := newEncoder(msg, binary.LittleEndian, make([]int, 0))
		if err := enc.Encode(byte(1), v); err != nil {
			t.Fatal(err)
		}
		dec := newDecoder(bytes.NewReader(msg.Bytes()), binary.LittleEndian, nil)
		got, err := dec.Decode(SignatureOf(byte(1), v))
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got[1], v) {
			t.Errorf("decoded %v, want %v", got[1], v)
		}
	}
}
