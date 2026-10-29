// SPDX-License-Identifier: Unlicense OR BSD-3-Clause

package tables

// PostScript table
// See https://learn.microsoft.com/en-us/typography/opentype/spec/post
type Post struct {
	version     postVersion
	italicAngle uint32
	// UnderlinePosition is the suggested distance of the top of the
	// underline from the baseline (negative values indicate below baseline).
	UnderlinePosition int16
	// Suggested values for the underline thickness.
	UnderlineThickness int16
	// IsFixedPitch indicates that the font is not proportionally spaced
	// (i.e. monospaced).
	IsFixedPitch uint32
	memoryUsage  [4]uint32
	Names        PostNames `unionField:"version"`
}

type PostNames interface {
	isPostNames()
}

func (PostNames10) isPostNames() {}
func (PostNames20) isPostNames() {}
func (PostNames30) isPostNames() {}

type postVersion uint32

const (
	postVersion10 postVersion = 0x00010000
	postVersion20 postVersion = 0x00020000
	postVersion30 postVersion = 0x00030000
)

type PostNames10 struct{}

type PostNames20 struct {
	GlyphNameIndexes []uint16 `arrayCount:"FirstUint16"` // size numGlyph
	// Strings holds the names as Pascal strings, kept raw:
	// see [PostNames20.StringOffsets] and [PostNames20.String].
	Strings []byte `isOpaque:"" subsliceStart:"AtCurrent"`
}

func (ps *PostNames20) parseStrings(src []byte) error {
	ps.Strings = src
	return nil
}

// StringOffsets returns where each name starts in [PostNames20.Strings].
// A name running past the end of the table is left out, with the ones after it.
// see https://learn.microsoft.com/en-us/typography/opentype/spec/post#version-20
func (ps PostNames20) StringOffsets() []uint32 {
	// "Strings are in Pascal string format, meaning that the first byte of
	// a given string is a length: the number of characters in that string.
	// The length byte is not included; for example, a length byte of 8 indicates
	// that the 8 bytes following the length byte comprise the string character data."
	var out []uint32
	for i := 0; i < len(ps.Strings); {
		end := i + 1 + int(ps.Strings[i])
		if end > len(ps.Strings) {
			break
		}
		out = append(out, uint32(i))
		i = end
	}
	return out
}

// String returns the name starting at [offset], one of [PostNames20.StringOffsets].
func (ps PostNames20) String(offset uint32) string {
	length := uint32(ps.Strings[offset])
	return string(ps.Strings[offset+1 : offset+1+length])
}

type PostNames30 PostNames10
