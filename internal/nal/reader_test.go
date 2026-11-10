package nal

import (
	"bytes"
	"testing"
)

func TestUnescapeDropsEmulationPrevention(t *testing.T) {
	got := Unescape(nil, []byte{1, 0, 0, 3, 0, 0, 0, 3, 3, 0, 3})
	want := []byte{1, 0, 0, 0, 0, 0, 3, 0, 3}
	if !bytes.Equal(got, want) {
		t.Errorf("unescape % x, want % x", got, want)
	}
}

func TestReaderExpGolomb(t *testing.T) {
	r := NewReader([]byte{0b1_010_011_0, 0b0100_0010, 0b1_0000000})
	got := []int64{int64(r.UE()), int64(r.UE()), int64(r.UE()), int64(r.UE()), int64(r.SE())}
	want := []int64{0, 1, 2, 3, -2}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("decoded %v, want %v", got, want)
		}
	}
	if r.Short() {
		t.Error("reader ran short")
	}
	r.UE()
	if !r.Short() {
		t.Error("reading past the end is not flagged")
	}
}

// What Writer writes, Reader reads back.
func TestReaderReadsWhatWriterWrites(t *testing.T) {
	var w Writer
	w.UE(0)
	w.UE(254)
	w.SE(-77)
	w.SE(31)
	w.U(13, 0x1abc)
	w.Flag(true)
	w.Trailing()
	r := NewReader(w.buf)
	if a, b, c, d, e, f := r.UE(), r.UE(), r.SE(), r.SE(), r.U(13), r.Flag(); a != 0 || b != 254 || c != -77 || d != 31 || e != 0x1abc || !f {
		t.Errorf("read %d %d %d %d %#x %v", a, b, c, d, e, f)
	}
	if r.MoreRBSPData() {
		t.Error("more RBSP data before the trailing bits")
	}
}

// more_rbsp_data is true up to the last one bit, the stop bit.
func TestMoreRBSPData(t *testing.T) {
	r := NewReader([]byte{0b1010_0000, 0})
	for i, want := range []bool{true, true, false} {
		if got := r.MoreRBSPData(); got != want {
			t.Errorf("after %d bits more = %v, want %v", i, got, want)
		}
		r.Skip(1)
	}
}
