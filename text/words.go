// Package text segments text into words for the word-wise editing
// shared by Entry and TextArea: ctrl+arrow motion, ctrl+backspace and
// ctrl+delete deletion, double-click selection, and the typing-run
// coalescing in the undo history.
//
// Word segmentation is a rune-class rule, not UAX #29. A word is a
// maximal run of word runes — letters, digits, and underscore
// (IsWordRune) — and every other run (whitespace, punctuation) is a gap
// the word keys step over. The existing segmenters were checked first
// and rejected on that pinned rule: rivo/uniseg and
// go-text/typesetting's segmenter.WordIterator both implement UAX #29
// word boundaries, whose rules glue mid-word punctuation to the word
// (can't, 3.14) and cut punctuation runs into segments of their own,
// so a word-left across "server.hostname.example" would park on the
// dots instead of skipping them. The class rule keeps the caret on
// real words, keeps double-click selection and word motion on one
// definition of a word, and matches the undo history's typing-run
// breaks, which already used it.
//
// Since #57 the unit under the class rule is the grapheme cluster, not
// the rune: uniseg's UAX #29 cluster segmentation (grapheme.go) splits
// rs into clusters, each cluster classified by its first rune, and
// words are maximal runs of same-class clusters. A combining mark, a
// skin-tone modifier, or a ZWJ continuation therefore extends the word
// it follows instead of reading as a boundary — "cafe\u0301" steps,
// deletes, and double-clicks as one unit. The exported functions still
// take and return rune indexes, so call sites are unchanged; clusters
// refine the unit under the same signatures.
package text

import "unicode"

// IsWordRune reports whether r counts as a word character: a letter, a
// digit, or an underscore. It is the single word-class rule behind
// word motion, word deletion, double-click selection, and typing-run
// coalescing. The class applies to a whole grapheme cluster through
// its first rune (wordCluster below); the continuation runes of a
// cluster never carry a class of their own.
func IsWordRune(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)
}

// wordCluster classifies the cluster starting at rs[start] by its first
// rune: the cluster is a word cluster when that rune is, so marks and
// modifiers inherit their base's class.
func wordCluster(rs []rune, start int) bool { return IsWordRune(rs[start]) }

// WordBoundaries reports, for every gap between the runes of rs,
// whether a word boundary sits there: element i covers the gap before
// rune i, element len(rs) the gap after the last rune. Boundaries sit
// between grapheme clusters only — never inside one — and where the
// word class changes: the edges of the word-cluster runs.
func WordBoundaries(rs []rune) []bool {
	b := make([]bool, len(rs)+1)
	b[0] = true
	starts := clusterStarts(rs)
	for k := 1; k+1 < len(starts); k++ {
		if wordCluster(rs, starts[k-1]) != wordCluster(rs, starts[k]) {
			b[starts[k]] = true
		}
	}
	b[len(rs)] = true
	return b
}

// WordStart returns the column word-left motion lands on: the start of
// the word at or before at, stepping back over any gap (whitespace,
// punctuation) in between. Clusters are atomic: at snaps to its
// cluster first, and the walk steps cluster-wise. at is clamped into
// [0, len(rs)]; the slice start is returned unchanged, so a caret at 0
// stays put.
func WordStart(rs []rune, at int) int {
	starts := clusterStarts(rs)
	k := clusterIndex(starts, min(max(at, 0), len(rs)))
	for k > 0 && !wordCluster(rs, starts[k-1]) {
		k--
	}
	for k > 0 && wordCluster(rs, starts[k-1]) {
		k--
	}
	return starts[k]
}

// WordEnd returns the column word-right motion lands on: the end of
// the word at or after at, stepping over any gap in between. Clusters
// are atomic: at snaps to its cluster first, and the walk steps
// cluster-wise. at is clamped into [0, len(rs)]; the slice end is
// returned unchanged, so a caret at the end stays put.
func WordEnd(rs []rune, at int) int {
	starts := clusterStarts(rs)
	k := clusterIndex(starts, min(max(at, 0), len(rs)))
	for k < len(starts)-1 && !wordCluster(rs, starts[k]) {
		k++
	}
	for k < len(starts)-1 && wordCluster(rs, starts[k]) {
		k++
	}
	return starts[k]
}

// WordRun returns the span of the same-class run containing at: a
// word's [start, end) when at sits on a word cluster, the punctuation
// or whitespace run otherwise. Runs are cluster runs — a mark or
// modifier never splits one — and at is clamped into the slice; the
// empty slice yields (0, 0). Double-click selection uses this, so what
// it grabs is exactly what the ctrl+arrows steps land on.
func WordRun(rs []rune, at int) (start, end int) {
	if len(rs) == 0 {
		return 0, 0
	}
	starts := clusterStarts(rs)
	last := len(starts) - 2
	k := clusterIndex(starts, min(max(at, 0), len(rs)-1))
	class := wordCluster(rs, starts[k])
	start = k
	for start > 0 && wordCluster(rs, starts[start-1]) == class {
		start--
	}
	end = k
	for end < last && wordCluster(rs, starts[end+1]) == class {
		end++
	}
	return starts[start], starts[end+1]
}
