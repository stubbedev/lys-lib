package opentype

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// twoTableFont is a font directory with a long table then a short one.
func twoTableFont() []byte {
	const tables = 2
	data := make([]byte, 12+16*tables)
	binary.BigEndian.PutUint32(data, 0x00010000)
	binary.BigEndian.PutUint16(data[4:], tables)
	record := func(i int, tag string, body string) {
		r := data[12+16*i:]
		copy(r, tag)
		binary.BigEndian.PutUint32(r[8:], uint32(len(data)))
		binary.BigEndian.PutUint32(r[12:], uint32(len(body)))
		data = append(data, body...)
	}
	record(0, "long", "a table long enough to hold the next one")
	record(1, "shrt", "short")
	return data
}

func TestReadingATableOverAnotherNeverWritesToTheMappedFont(t *testing.T) {
	data := twoTableFont()
	original := bytes.Clone(data)
	loaders, err := NewLoaders(NewMapped(data))
	if err != nil {
		t.Fatal(err)
	}
	long, err := loaders[0].RawTableTo(MustNewTag("long"), nil)
	if err != nil {
		t.Fatal(err)
	}
	short, err := loaders[0].RawTableTo(MustNewTag("shrt"), long)
	if err != nil {
		t.Fatal(err)
	}
	if string(short) != "short" {
		t.Errorf("read %q", short)
	}
	if !bytes.Equal(data, original) {
		t.Error("reading a table into the previous table's slice wrote to the font")
	}
}
