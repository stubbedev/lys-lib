package demux

import "fmt"

// XiphHeaders splits a Vorbis track's Config, Xiph-laced as Matroska
// stores CodecPrivate, into its identification, comment and setup
// headers.
func XiphHeaders(config []byte) (id, comment, setup []byte, err error) {
	if len(config) == 0 || config[0] != 2 {
		return nil, nil, nil, fmt.Errorf("demux: Xiph-laced headers: count byte of %d bytes, want 2 (three headers): %w", len(config), ErrMalformed)
	}
	p := 1
	var sizes [2]int
	for i := range sizes {
		for {
			if p >= len(config) {
				return nil, nil, nil, fmt.Errorf("demux: Xiph-laced headers: sizes run past %d bytes: %w", len(config), ErrMalformed)
			}
			c := config[p]
			p++
			sizes[i] += int(c)
			if c != 0xff {
				break
			}
		}
	}
	rest := config[p:]
	if sizes[0]+sizes[1] > len(rest) {
		return nil, nil, nil, fmt.Errorf("demux: Xiph-laced headers of %d and %d bytes in %d: %w", sizes[0], sizes[1], len(rest), ErrMalformed)
	}
	return rest[:sizes[0]], rest[sizes[0] : sizes[0]+sizes[1]], rest[sizes[0]+sizes[1]:], nil
}
