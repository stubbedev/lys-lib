// Grapheme cluster segmentation (UAX #29) for the editable text
// widgets: the unit Backspace, Delete, and caret motion move (#57).
// A cluster is one user-perceived character — an emoji ZWJ family
// (👨‍👩‍👧, five runes), a flag (two regional indicators), a base
// rune with its combining marks or skin-tone modifier — and every
// position these primitives return is a cluster boundary, so a caret
// can never sit inside a character and a delete can never cut one in
// half.
//
// Positions stay rune indexes; segmentation is a mapping on top. The
// shaping caret table, the IME byte offsets, the wrap row cache, and
// the undo snapshots are all rune-indexed, and ClusterRun maps between
// the two views in O(log n) over the boundary table. The primitives
// snap defensively in both directions, so even a position placed
// mid-cluster (public SetCursor, a wrap row edge) degrades to the
// enclosing cluster instead of corrupting text.
package text

import (
	"slices"
	"unicode/utf8"

	"github.com/rivo/uniseg"
)

// clusterStarts returns the rune indexes where the grapheme clusters of
// rs begin: element 0 is 0, the last element is len(rs), so c clusters
// come back as c+1 boundaries. Segmenter state chains across the whole
// slice, which regional-indicator pairs and ZWJ joins need.
func clusterStarts(rs []rune) []int {
	starts := make([]int, 1, len(rs)+1)
	s := string(rs)
	state := -1
	for i := 0; len(s) > 0; {
		var cluster string
		cluster, s, _, state = uniseg.FirstGraphemeClusterInString(s, state)
		i += utf8.RuneCountInString(cluster)
		starts = append(starts, i)
	}
	return starts
}

// clusterIndex returns the index into a clusterStarts table of the
// cluster containing at: its start boundary is starts[k] <= at.
func clusterIndex(starts []int, at int) int {
	i, found := slices.BinarySearch(starts, at)
	if found {
		return i
	}
	return i - 1
}

// ClusterRun returns the [start, end) rune span of the grapheme cluster
// containing at, with at clamped into [0, len(rs)]. The empty slice
// yields (0, 0).
func ClusterRun(rs []rune, at int) (start, end int) {
	if len(rs) == 0 {
		return 0, 0
	}
	starts := clusterStarts(rs)
	k := clusterIndex(starts, min(max(at, 0), len(rs)-1))
	return starts[k], starts[k+1]
}

// SnapCluster returns at snapped down to the boundary of the cluster
// containing it — the leftmost caret position at or before at.
func SnapCluster(rs []rune, at int) int {
	if at <= 0 {
		return 0
	}
	if at >= len(rs) {
		return len(rs)
	}
	start, _ := ClusterRun(rs, at)
	return start
}

// SnapClusterForward returns at snapped up to the nearest cluster
// boundary: at itself when it already sits on one, otherwise the end of
// the cluster containing it. This is the rightmost caret position that
// does not skip past at — splice targets use it to park the caret after
// what was just inserted without eating into the following cluster.
func SnapClusterForward(rs []rune, at int) int {
	if at <= 0 {
		return 0
	}
	if at >= len(rs) {
		return len(rs)
	}
	start, end := ClusterRun(rs, at)
	if start == at {
		return at
	}
	return end
}

// PrevCluster returns the caret-left and Backspace target for position
// at: the start of the cluster before it, stepping out of the cluster
// containing at-1 whole. The slice start comes back unchanged, so a
// caret at 0 stays put.
func PrevCluster(rs []rune, at int) int {
	if at <= 0 {
		return 0
	}
	return SnapCluster(rs, min(at, len(rs))-1)
}

// NextCluster returns the caret-right and Delete target for position
// at: the boundary after the cluster containing it, stepping over the
// cluster whole. The slice end comes back unchanged, so a caret at the
// end stays put.
func NextCluster(rs []rune, at int) int {
	if at >= len(rs) {
		return len(rs)
	}
	_, end := ClusterRun(rs, max(at, 0))
	return end
}

// ContinuesCluster reports whether typing next directly after prev
// extends prev's grapheme cluster rather than starting a new one — a
// combining mark completing a base rune, a skin-tone modifier, a ZWJ
// join. It classifies the undo history's typing-run coalescing
// (widget/undo.go), where a mark typed after a word rune must not read
// as a run break. The two-rune test is an approximation at the edges:
// a second regional indicator after a complete flag pair reads as
// continuing, which only coarsens undo grouping, never text.
func ContinuesCluster(prev, next rune) bool {
	return uniseg.GraphemeClusterCount(string([]rune{prev, next})) == 1
}
