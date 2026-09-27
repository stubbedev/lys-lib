// Package text segments text into words for the word-wise editing
// shared by Entry and TextArea: ctrl+arrow motion, ctrl+backspace and
// ctrl+delete deletion, double-click selection, and the typing-run
// coalescing in the undo history.
//
// Segmentation is a rune-class rule, not UAX #29. A word is a maximal
// run of word runes — letters, digits, and underscore (IsWordRune) —
// and every other run (whitespace, punctuation) is a gap the word keys
// step over. The existing segmenters were checked first and rejected
// on that pinned rule: rivo/uniseg and go-text/typesetting's
// segmenter.WordIterator both implement UAX #29 word boundaries, whose
// rules glue mid-word punctuation to the word (can't, 3.14) and cut
// punctuation runs into segments of their own, so a word-left across
// "server.hostname.example" would park on the dots instead of skipping
// them. The class rule keeps the caret on real words, keeps double-
// click selection and word motion on one definition of a word, and
// matches the undo history's typing-run breaks, which already used it.
//
// Every function takes a []rune and weighs each rune as one unit.
// Grapheme clusters (issue #57) will layer on by refining the unit —
// feeding cluster-aware input or extending these functions to walk
// cluster boundaries — without changing call sites. Until then a
// combining mark after a word rune reads as a boundary; that is
// cluster work, not segmentation work.
package text

import "unicode"

// IsWordRune reports whether r counts as a word character: a letter, a
// digit, or an underscore. It is the single word-class rule behind
// word motion, word deletion, double-click selection, and typing-run
// coalescing.
func IsWordRune(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)
}

// WordBoundaries reports, for every gap between the runes of rs,
// whether a word boundary sits there: element i covers the gap before
// rune i, element len(rs) the gap after the last rune. A boundary is
// any change of word class — the edges of the word-rune runs — so
// words start where a boundary meets a word rune and end where one
// meets a non-word rune.
func WordBoundaries(rs []rune) []bool {
	b := make([]bool, len(rs)+1)
	b[0] = true
	for i := 1; i < len(rs); i++ {
		b[i] = IsWordRune(rs[i-1]) != IsWordRune(rs[i])
	}
	b[len(rs)] = true
	return b
}

// WordStart returns the column word-left motion lands on: the start of
// the word at or before at, stepping back over any gap (whitespace,
// punctuation) in between. at is clamped into [0, len(rs)]; the slice
// start is returned unchanged, so a caret at 0 stays put.
func WordStart(rs []rune, at int) int {
	i := min(max(at, 0), len(rs))
	for i > 0 && !IsWordRune(rs[i-1]) {
		i--
	}
	for i > 0 && IsWordRune(rs[i-1]) {
		i--
	}
	return i
}

// WordEnd returns the column word-right motion lands on: the end of
// the word at or after at, stepping over any gap in between. at is
// clamped into [0, len(rs)]; the slice end is returned unchanged, so a
// caret at the end stays put.
func WordEnd(rs []rune, at int) int {
	i := min(max(at, 0), len(rs))
	for i < len(rs) && !IsWordRune(rs[i]) {
		i++
	}
	for i < len(rs) && IsWordRune(rs[i]) {
		i++
	}
	return i
}

// WordRun returns the span of the same-class run containing at: a
// word's [start, end) when at sits on a word rune, the punctuation or
// whitespace run otherwise. at is clamped into the slice; the empty
// slice yields (0, 0). Double-click selection uses this, so what it
// grabs is exactly what the ctrl+arrows steps land on.
func WordRun(rs []rune, at int) (start, end int) {
	if len(rs) == 0 {
		return 0, 0
	}
	at = min(max(at, 0), len(rs)-1)
	start = at
	for start > 0 && IsWordRune(rs[start-1]) == IsWordRune(rs[at]) {
		start--
	}
	end = at + 1
	for end < len(rs) && IsWordRune(rs[end]) == IsWordRune(rs[at]) {
		end++
	}
	return start, end
}
