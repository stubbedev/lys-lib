package glibclocale

import (
	"bytes"
	"encoding/binary"
	"fmt"
)

const (
	archiveMagic     uint32 = 0xde020109
	archiveHeader           = 56
	nameEntrySize           = 12
	localeRecordSize        = 4 + categorySlots*8
)

type archive struct {
	data      []byte
	order     binary.ByteOrder
	table     uint32
	nameCount uint32
}

func parseArchive(data []byte) (archive, error) {
	if len(data) < archiveHeader {
		return archive{}, fmt.Errorf("%d bytes are shorter than a locale-archive header", len(data))
	}
	var order binary.ByteOrder
	switch archiveMagic {
	case binary.LittleEndian.Uint32(data):
		order = binary.LittleEndian
	case binary.BigEndian.Uint32(data):
		order = binary.BigEndian
	default:
		return archive{}, fmt.Errorf("magic %#08x is not a locale-archive's %#08x", binary.LittleEndian.Uint32(data), archiveMagic)
	}
	a := archive{data: data, order: order, table: order.Uint32(data[8:]), nameCount: order.Uint32(data[16:])}
	if err := a.span(a.table, uint64(a.nameCount)*nameEntrySize, "the name table"); err != nil {
		return archive{}, err
	}
	return a, nil
}

func (a archive) span(at uint32, n uint64, what string) error {
	if uint64(at)+n > uint64(len(a.data)) {
		return fmt.Errorf("%s at offset %d, %d bytes long, runs past the archive's %d bytes", what, at, n, len(a.data))
	}
	return nil
}

type nameEntry struct {
	hash, name, record uint32
}

func (a archive) entry(i uint32) nameEntry {
	e := a.data[uint64(a.table)+uint64(i)*nameEntrySize:]
	return nameEntry{hash: a.order.Uint32(e), name: a.order.Uint32(e[4:]), record: a.order.Uint32(e[8:])}
}

func (a archive) cString(at uint32) ([]byte, error) {
	if uint64(at) >= uint64(len(a.data)) {
		return nil, fmt.Errorf("a locale name at offset %d is past the archive's %d bytes", at, len(a.data))
	}
	n := bytes.IndexByte(a.data[at:], 0)
	if n < 0 {
		return nil, fmt.Errorf("the locale name at offset %d has no terminating NUL", at)
	}
	return a.data[at : int(at)+n], nil
}

func (a archive) localeNames() ([]string, error) {
	out := make([]string, 0, a.nameCount)
	for i := range a.nameCount {
		e := a.entry(i)
		if e.name == 0 {
			continue
		}
		name, err := a.cString(e.name)
		if err != nil {
			return nil, err
		}
		out = append(out, string(name))
	}
	return out, nil
}

func hashName(name string) uint32 {
	h := uint32(len(name)) //nolint:gosec // glibc hashes the length as 32 bits
	for i := range len(name) {
		h = h<<9 | h>>23
		h += uint32(name[i])
	}
	if h == 0 {
		return ^uint32(0)
	}
	return h
}

func (a archive) find(name string) (uint32, bool, error) {
	if a.nameCount < 3 {
		return 0, false, nil
	}
	h := hashName(name)
	at := h % a.nameCount
	step := 1 + h%(a.nameCount-2)
	for range a.nameCount {
		e := a.entry(at)
		if e.name == 0 {
			return 0, false, nil
		}
		if e.hash == h {
			stored, err := a.cString(e.name)
			if err != nil {
				return 0, false, err
			}
			if string(stored) == name {
				return e.record, true, nil
			}
		}
		if at >= a.nameCount-step {
			at -= a.nameCount - step
		} else {
			at += step
		}
	}
	return 0, false, nil
}

func (a archive) category(record uint32, c category) ([]byte, error) {
	if err := a.span(record, localeRecordSize, "a locale record"); err != nil {
		return nil, err
	}
	slot := a.data[uint64(record)+4+8*uint64(c):]
	at, n := a.order.Uint32(slot), a.order.Uint32(slot[4:])
	if n == 0 {
		return nil, errMissingCategory
	}
	if err := a.span(at, uint64(n), "the "+c.String()+" data"); err != nil {
		return nil, err
	}
	return a.data[at : uint64(at)+uint64(n)], nil
}
