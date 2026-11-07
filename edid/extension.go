package edid

import "fmt"

const (
	tagCTA       = 0x02
	tagDisplayID = 0x70
	ctaUseExtTag = 7
	ctaVendorTag = 3
)

func eachExtension(b []byte, tag byte, name string, visit func(block []byte) error) error {
	for at := blockSize; at+blockSize <= len(b); at += blockSize {
		block := b[at : at+blockSize]
		if block[0] != tag {
			continue
		}
		if sum := checksum(block); sum != 0 {
			return fmt.Errorf("%w: the %s extension at %d has a checksum off by %d", ErrInvalid, name, at, sum)
		}
		if err := visit(block); err != nil {
			return err
		}
	}
	return nil
}

func checksum(b []byte) byte {
	var sum byte
	for _, c := range b {
		sum += c
	}
	return sum
}

func eachCTADataBlock(block []byte, visit func(tag byte, data []byte)) error {
	end := int(block[2])
	if end == 0 {
		end = blockSize - 1
	}
	if end < 4 || end > blockSize-1 {
		return fmt.Errorf("%w: a CTA-861 extension whose data blocks end at %d", ErrInvalid, end)
	}
	for i := 4; i < end; {
		tag, n := block[i]>>5, int(block[i]&0x1f)
		if i+1+n > end {
			return fmt.Errorf("%w: a CTA-861 data block of %d bytes at %d overruns %d", ErrInvalid, n, i, end)
		}
		visit(tag, block[i+1:i+1+n])
		i += 1 + n
	}
	return nil
}

func eachDisplayIDDataBlock(block []byte, visit func(version, tag, revision byte, payload []byte)) error {
	version, n := block[1], int(block[2])
	end := 5 + n
	if end >= blockSize-1 {
		return fmt.Errorf("%w: a DisplayID section of %d bytes overruns its extension", ErrInvalid, n)
	}
	if sum := checksum(block[1 : end+1]); sum != 0 {
		return fmt.Errorf("%w: a DisplayID section with a checksum off by %d", ErrInvalid, sum)
	}
	for i := 5; i+3 <= end; {
		tag, revision, size := block[i], block[i+1], int(block[i+2])
		if i+3+size > end {
			return fmt.Errorf("%w: a DisplayID data block of %d bytes at %d overruns %d", ErrInvalid, size, i, end)
		}
		visit(version, tag, revision, block[i+3:i+3+size])
		i += 3 + size
	}
	return nil
}
