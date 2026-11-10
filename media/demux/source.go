package demux

import (
	"errors"
	"fmt"
	"io"
	"slices"
)

const (
	windowSize = 64 << 10
	// maxHeaderBytes bounds the metadata read into memory whole: an
	// MP4 moov or moof, a Matroska SeekHead, Info, Tracks or Cues.
	maxHeaderBytes = 256 << 20
	// maxPacketBytes bounds one packet.
	maxPacketBytes = 128 << 20
)

// source reads a file through a window, so the small reads of box and
// element headers come out of one larger read.
type source struct {
	r      io.ReaderAt
	size   int64
	win    []byte
	winOff int64
}

func truncated(off int64, n int, size int64) error {
	return fmt.Errorf("%d bytes at offset %d run past the file's end at %d: %w", n, off, size, ErrMalformed)
}

func (s *source) fill(dst []byte, off int64) error {
	n, err := s.r.ReadAt(dst, off)
	if n == len(dst) {
		return nil
	}
	if err == nil || errors.Is(err, io.EOF) {
		return truncated(off, len(dst), s.size)
	}
	return fmt.Errorf("read %d bytes at offset %d: %w", len(dst), off, err)
}

// view is the n bytes at off, n at most windowSize, valid until the
// next call.
func (s *source) view(off int64, n int) ([]byte, error) {
	if off < 0 || n < 0 || int64(n) > s.size-off {
		return nil, truncated(off, n, s.size)
	}
	if off >= s.winOff && off+int64(n) <= s.winOff+int64(len(s.win)) {
		at := int(off - s.winOff)
		return s.win[at : at+n], nil
	}
	if s.win == nil {
		s.win = make([]byte, windowSize)
	}
	s.win = s.win[:min(int64(windowSize), s.size-off)]
	if err := s.fill(s.win, off); err != nil {
		s.win = s.win[:0]
		return nil, err
	}
	s.winOff = off
	return s.win[:n], nil
}

// upTo is the bytes at off up to n of them, fewer at the file's end.
func (s *source) upTo(off int64, n int) ([]byte, error) {
	if off >= s.size {
		return nil, truncated(off, 1, s.size)
	}
	return s.view(off, int(min(int64(n), s.size-off)))
}

// appendAt appends the n bytes at off to dst.
func (s *source) appendAt(dst []byte, off int64, n int64) ([]byte, error) {
	if n < 0 || n > s.size-off || off < 0 {
		return nil, truncated(off, int(min(n, maxPacketBytes)), s.size)
	}
	if n <= windowSize {
		b, err := s.view(off, int(n))
		if err != nil {
			return nil, err
		}
		return append(dst, b...), nil
	}
	at := len(dst)
	dst = slices.Grow(dst, int(n))[:at+int(n)]
	if err := s.fill(dst[at:], off); err != nil {
		return nil, err
	}
	return dst, nil
}

// load reads the n bytes at off into a new slice, refusing more than
// maxHeaderBytes; what names them for the error.
func (s *source) load(off, n int64, what string) ([]byte, error) {
	if n > maxHeaderBytes {
		return nil, fmt.Errorf("%s of %d bytes at offset %d is past the %d-byte bound: %w", what, n, off, maxHeaderBytes, ErrMalformed)
	}
	b, err := s.appendAt(nil, off, n)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", what, err)
	}
	return b, nil
}
