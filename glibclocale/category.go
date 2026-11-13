package glibclocale

import (
	"bytes"
	"encoding/binary"
	"fmt"
)

type category uint32

const (
	lcCtype          category = 0
	lcNumeric        category = 1
	lcTime           category = 2
	lcMonetary       category = 4
	lcIdentification category = 12
	categorySlots             = 13
)

func (c category) String() string {
	switch c {
	case lcCtype:
		return "LC_CTYPE"
	case lcNumeric:
		return "LC_NUMERIC"
	case lcTime:
		return "LC_TIME"
	case lcMonetary:
		return "LC_MONETARY"
	case lcIdentification:
		return "LC_IDENTIFICATION"
	}
	return fmt.Sprintf("category %d", uint32(c))
}

func (c category) magic() uint32 { return 0x20031115 ^ uint32(c) }

type item uint32

func (i item) category() category { return category(i >> 16) }

func (i item) index() uint32 { return uint32(i & 0xffff) }

const (
	categoryHeader = 8
	maxAltDigits   = 100
)

type categoryData struct {
	category category
	data     []byte
	order    binary.ByteOrder
	count    uint32
}

func parseCategory(c category, data []byte, last item) (categoryData, error) {
	if last.category() != c {
		panic(fmt.Sprintf("glibclocale: item %#x is not in %s", uint32(last), c))
	}
	if len(data) < categoryHeader {
		return categoryData{}, fmt.Errorf("%s data of %d bytes is shorter than its header", c, len(data))
	}
	var order binary.ByteOrder
	switch c.magic() {
	case binary.LittleEndian.Uint32(data):
		order = binary.LittleEndian
	case binary.BigEndian.Uint32(data):
		order = binary.BigEndian
	default:
		return categoryData{}, fmt.Errorf("%s data has magic %#08x, want %#08x", c, binary.LittleEndian.Uint32(data), c.magic())
	}
	count := order.Uint32(data[4:])
	if count <= last.index() {
		return categoryData{}, fmt.Errorf("%s data has %d items, want at least %d", c, count, last.index()+1)
	}
	if uint64(categoryHeader)+4*uint64(count) > uint64(len(data)) {
		return categoryData{}, fmt.Errorf("%s data of %d bytes cannot hold an index of %d items", c, len(data), count)
	}
	return categoryData{category: c, data: data, order: order, count: count}, nil
}

func (d categoryData) offset(i item) (int, error) {
	if i.category() != d.category {
		panic(fmt.Sprintf("glibclocale: item %#x read from %s", uint32(i), d.category))
	}
	if i.index() >= d.count {
		return 0, fmt.Errorf("%s item %d is past its %d items", d.category, i.index(), d.count)
	}
	at := int(d.order.Uint32(d.data[categoryHeader+4*i.index():]))
	if at >= len(d.data) {
		return 0, fmt.Errorf("%s item %d at offset %d is past the data's %d bytes", d.category, i.index(), at, len(d.data))
	}
	return at, nil
}

func (d categoryData) cString(i item, at int) (string, int, error) {
	n := bytes.IndexByte(d.data[at:], 0)
	if n < 0 {
		return "", 0, fmt.Errorf("%s item %d has no terminating NUL", d.category, i.index())
	}
	return string(d.data[at : at+n]), at + n + 1, nil
}

func (d categoryData) str(i item) (string, error) {
	at, err := d.offset(i)
	if err != nil {
		return "", err
	}
	s, _, err := d.cString(i, at)
	return s, err
}

func (d categoryData) char(i item) (byte, error) {
	at, err := d.offset(i)
	if err != nil {
		return 0, err
	}
	return d.data[at], nil
}

func (d categoryData) word(i item) (uint32, error) {
	at, err := d.offset(i)
	if err != nil {
		return 0, err
	}
	if at+4 > len(d.data) {
		return 0, fmt.Errorf("%s item %d at offset %d runs past the data's %d bytes", d.category, i.index(), at, len(d.data))
	}
	return d.order.Uint32(d.data[at:]), nil
}

func (d categoryData) strs(i item, n int) ([]string, error) {
	at, err := d.offset(i)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, min(n, 64))
	for range n {
		if at >= len(d.data) {
			return nil, fmt.Errorf("%s item %d holds %d of its %d strings", d.category, i.index(), len(out), n)
		}
		var s string
		if s, at, err = d.cString(i, at); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

func (d categoryData) strsUpTo(i, next item, limit int) ([]string, error) {
	at, err := d.offset(i)
	if err != nil {
		return nil, err
	}
	end, err := d.offset(next)
	if err != nil {
		return nil, err
	}
	if end < at {
		return nil, fmt.Errorf("%s item %d ends at offset %d, before it starts at %d", d.category, i.index(), end, at)
	}
	if end == at || d.data[at] == 0 {
		return nil, nil
	}
	var out []string
	for at < end && len(out) < limit {
		var s string
		if s, at, err = d.cString(i, at); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

func (d categoryData) strArray(first item, out []string) error {
	for k := range out {
		s, err := d.str(first + item(k))
		if err != nil {
			return err
		}
		out[k] = s
	}
	return nil
}
