package av1

import "fmt"

// Tile is a tile's coded data within the tile group it was read from:
// its offset and size in bytes.
type Tile struct {
	Offset, Size int
}

// ParseTileGroup reads b, a tile group OBU's payload or a frame OBU's
// after its header, of the frame h heads, appending its tiles to tiles.
// last is set once the group holds the frame's last tile.
func ParseTileGroup(h *FrameHeader, b []byte, tiles []Tile) (out []Tile, last bool, err error) {
	t := &h.Tile
	n := t.Cols * t.Rows
	r := newReader(b)
	start, end := 0, n-1
	if n > 1 && r.Flag() {
		bits := t.ColsLog2 + t.RowsLog2
		start, end = int(r.U(bits)), int(r.U(bits))
	}
	r.align()
	at := r.bytePos()
	if r.Short() || start != len(tiles) || end < start || end >= n {
		return tiles, false, fmt.Errorf("%w: tile group of tiles %d to %d in %d bytes, after %d of %d tiles", ErrSyntax, start, end, len(b), len(tiles), n)
	}
	for tile := start; tile <= end; tile++ {
		size := len(b) - at
		if tile != end {
			if len(b)-at < t.TileSizeBytes {
				return tiles, false, fmt.Errorf("%w: tile %d's size runs past its group's %d bytes", ErrSyntax, tile, len(b))
			}
			r := newReader(b[at : at+t.TileSizeBytes])
			size = int(r.le(t.TileSizeBytes)) + 1
			at += t.TileSizeBytes
			if size > len(b)-at {
				return tiles, false, fmt.Errorf("%w: tile %d of %d bytes runs past its group's %d", ErrSyntax, tile, size, len(b))
			}
		}
		if size <= 0 {
			return tiles, false, fmt.Errorf("%w: tile %d is empty", ErrSyntax, tile)
		}
		tiles = append(tiles, Tile{Offset: at, Size: size})
		at += size
	}
	return tiles, end == n-1, nil
}
