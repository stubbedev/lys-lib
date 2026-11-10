package vorbis

import (
	"encoding/binary"
	"fmt"

	"github.com/stubbedev/lys-lib/intconv"
)

const (
	pageHeaderSize = 27
	flagContinued  = 0x01
	flagBOS        = 0x02
	flagEOS        = 0x04
	noGranule      = -1
)

func pageCRC(page []byte) uint32 {
	var crc uint32
	for i, b := range page {
		if i >= 22 && i < 26 {
			b = 0
		}
		crc = crc<<8 ^ crcTable[byte(crc>>24)^b]
	}
	return crc
}

type packet struct {
	data    []byte
	granule int64
	eos     bool
}

type oggReader struct {
	data    []byte
	off     int
	serial  uint32
	seq     uint32
	started bool
	done    bool

	pageOff int
	lacing  []byte
	body    []byte
	seg     int
	bodyAt  int
	lastEnd int
	granule int64
	flags   byte

	pending []byte
}

func newOggReader(data []byte) *oggReader {
	return &oggReader{data: data}
}

func (o *oggReader) nextPage() (bool, error) {
	for {
		if o.done {
			return false, nil
		}
		if o.off == len(o.data) {
			if !o.started {
				return false, fmt.Errorf("vorbis: no Ogg page: %w", ErrNotVorbis)
			}
			if len(o.pending) > 0 {
				return false, fmt.Errorf("vorbis: stream ends inside a packet at offset %d: %w", o.off, ErrTruncated)
			}
			return false, nil
		}
		at := o.off
		rest := o.data[at:]
		if capture := rest[:min(4, len(rest))]; string(capture) != "OggS"[:len(capture)] {
			if !o.started {
				return false, fmt.Errorf("vorbis: no Ogg capture pattern at offset %d: %w", at, ErrNotVorbis)
			}
			return false, fmt.Errorf("vorbis: no Ogg capture pattern at offset %d: %w", at, ErrCorrupt)
		}
		if len(rest) < pageHeaderSize {
			return false, fmt.Errorf("vorbis: page header at offset %d: %w", at, ErrTruncated)
		}
		if rest[4] != 0 {
			return false, fmt.Errorf("vorbis: page at offset %d has Ogg version %d: %w", at, rest[4], ErrCorrupt)
		}
		nseg := int(rest[26])
		headerLen := pageHeaderSize + nseg
		if len(rest) < headerLen {
			return false, fmt.Errorf("vorbis: segment table at offset %d: %w", at, ErrTruncated)
		}
		lacing := rest[pageHeaderSize:headerLen]
		bodyLen := 0
		for _, l := range lacing {
			bodyLen += int(l)
		}
		if len(rest) < headerLen+bodyLen {
			return false, fmt.Errorf("vorbis: page body at offset %d: %w", at, ErrTruncated)
		}
		page := rest[:headerLen+bodyLen]
		if got, want := pageCRC(page), binary.LittleEndian.Uint32(page[22:]); got != want {
			return false, fmt.Errorf("vorbis: page at offset %d: checksum %08x, header says %08x: %w", at, got, want, ErrCorrupt)
		}
		o.off += len(page)
		flags := page[5]
		serial := binary.LittleEndian.Uint32(page[14:])
		seq := binary.LittleEndian.Uint32(page[18:])
		if !o.started {
			if flags&flagBOS == 0 {
				return false, fmt.Errorf("vorbis: first page at offset %d does not begin a stream: %w", at, ErrNotVorbis)
			}
			o.started = true
			o.serial = serial
		} else {
			if serial != o.serial {
				continue
			}
			if seq != o.seq+1 {
				return false, fmt.Errorf("vorbis: page at offset %d has sequence %d after %d: %w", at, seq, o.seq, ErrCorrupt)
			}
		}
		continued := flags&flagContinued != 0
		if continued != (len(o.pending) > 0) {
			return false, fmt.Errorf("vorbis: page at offset %d: continuation flag %t with %d bytes pending: %w", at, continued, len(o.pending), ErrCorrupt)
		}
		o.seq = seq
		o.pageOff = at
		o.flags = flags
		o.granule = intconv.Int64Bits(binary.LittleEndian.Uint64(page[6:]))
		o.lacing = lacing
		o.body = page[headerLen:]
		o.seg = 0
		o.bodyAt = 0
		o.lastEnd = -1
		for i, l := range lacing {
			if l < 255 {
				o.lastEnd = i
			}
		}
		if flags&flagEOS != 0 {
			o.done = true
		}
		return true, nil
	}
}

func (o *oggReader) next() (p packet, ok bool, err error) {
	for {
		for o.seg < len(o.lacing) {
			start := o.bodyAt
			end := -1
			for o.seg < len(o.lacing) {
				l := o.lacing[o.seg]
				o.bodyAt += int(l)
				o.seg++
				if l < 255 {
					end = o.seg - 1
					break
				}
			}
			chunk := o.body[start:o.bodyAt]
			if end < 0 {
				o.pending = append(o.pending, chunk...)
				break
			}
			p = packet{data: chunk, granule: noGranule}
			if len(o.pending) > 0 {
				o.pending = append(o.pending, chunk...)
				p.data = o.pending
				o.pending = o.pending[:0]
			}
			if end == o.lastEnd {
				p.granule = o.granule
				p.eos = o.flags&flagEOS != 0
			}
			return p, true, nil
		}
		more, err := o.nextPage()
		if err != nil || !more {
			return packet{}, false, err
		}
	}
}

func lastGranule(data []byte, serial uint32) int64 {
	for at := len(data) - pageHeaderSize; at >= 0; at-- {
		if data[at] != 'O' || string(data[at:at+4]) != "OggS" {
			continue
		}
		if binary.LittleEndian.Uint32(data[at+14:]) != serial {
			continue
		}
		if g := intconv.Int64Bits(binary.LittleEndian.Uint64(data[at+6:])); g >= 0 {
			return g
		}
	}
	return 0
}
