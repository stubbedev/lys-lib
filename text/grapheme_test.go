package text

import (
	"reflect"
	"slices"
	"testing"

	"github.com/rivo/uniseg"
)

const (
	familyEmoji = "👨‍👩‍👧"   // 5 runes, 1 cluster
	flagEmoji   = "🇩🇪"      // 2 runes, 1 cluster
	tonedEmoji  = "👍🏽"      // 2 runes, 1 cluster
	accentedE   = "e\u0301" // 2 runes, 1 cluster
)

func TestClusterStarts(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []int
	}{
		{"empty", "", []int{0}},
		{"ascii is one cluster per rune", "abc", []int{0, 1, 2, 3}},
		{"zwj family is one cluster", familyEmoji, []int{0, 5}},
		{"family between letters", "a" + familyEmoji + "b", []int{0, 1, 6, 7}},
		{"flag is one cluster", "x" + flagEmoji, []int{0, 1, 3}},
		{"skin tone joins its base", tonedEmoji + "!", []int{0, 2, 3}},
		{"combining mark joins its base", "caf" + accentedE + "x", []int{0, 1, 2, 3, 5, 6}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := clusterStarts([]rune(tt.in))
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("clusterStarts(%q) = %v, want %v", tt.in, got, tt.want)
			}
			if n := uniseg.GraphemeClusterCount(tt.in); n != len(tt.want)-1 {
				t.Errorf("sanity: uniseg sees %d clusters in %q, want %d", n, tt.in, len(tt.want)-1)
			}
		})
	}
}

func TestClusterStepping(t *testing.T) {
	rs := []rune("a" + familyEmoji + "b") // boundaries 0, 1, 6, 7
	tests := []struct {
		at            int
		prev, next    int
		snap, snapFwd int
	}{
		{at: 0, prev: 0, next: 1, snap: 0, snapFwd: 0},
		{at: 1, prev: 0, next: 6, snap: 1, snapFwd: 1},
		{at: 3, prev: 1, next: 6, snap: 1, snapFwd: 6}, // mid-family is not a caret position
		{at: 6, prev: 1, next: 7, snap: 6, snapFwd: 6},
		{at: 7, prev: 6, next: 7, snap: 7, snapFwd: 7},
		{at: 99, prev: 6, next: 7, snap: 7, snapFwd: 7},
		{at: -3, prev: 0, next: 1, snap: 0, snapFwd: 0},
	}
	for _, tt := range tests {
		if got := PrevCluster(rs, tt.at); got != tt.prev {
			t.Errorf("PrevCluster(at=%d) = %d, want %d", tt.at, got, tt.prev)
		}
		if got := NextCluster(rs, tt.at); got != tt.next {
			t.Errorf("NextCluster(at=%d) = %d, want %d", tt.at, got, tt.next)
		}
		if got := SnapCluster(rs, tt.at); got != tt.snap {
			t.Errorf("SnapCluster(at=%d) = %d, want %d", tt.at, got, tt.snap)
		}
		if got := SnapClusterForward(rs, tt.at); got != tt.snapFwd {
			t.Errorf("SnapClusterForward(at=%d) = %d, want %d", tt.at, got, tt.snapFwd)
		}
	}

	if start, end := ClusterRun(rs, 3); start != 1 || end != 6 {
		t.Errorf("ClusterRun(mid-family) = %d..%d, want 1..6", start, end)
	}
	if start, end := ClusterRun(nil, 2); start != 0 || end != 0 {
		t.Errorf("ClusterRun(empty) = %d..%d, want 0..0", start, end)
	}

	// Every primitive output is a valid caret position for every input:
	// stepping from anywhere lands on cluster boundaries and progresses.
	for at := range len(rs) + 1 {
		if p := PrevCluster(rs, at); p > 0 && !isClusterStart(rs, p) {
			t.Errorf("PrevCluster(at=%d) = %d, not a cluster start", at, p)
		}
		if n := NextCluster(rs, at); n < len(rs) && !isClusterStart(rs, n) {
			t.Errorf("NextCluster(at=%d) = %d, not a cluster start", at, n)
		}
	}
}

func isClusterStart(rs []rune, at int) bool {
	return slices.Contains(clusterStarts(rs), at)
}

func TestContinuesCluster(t *testing.T) {
	tests := []struct {
		name      string
		prev, r   rune
		continues bool
	}{
		{"combining mark completes its base", 'e', '\u0301', true},
		{"skin tone completes its emoji", '\U0001F44D', '\U0001F3FD', true},
		{"zwj joins", '\U0001F468', '\u200d', true},
		{"plain letters start fresh clusters", 'a', 'b', false},
		{"word rune then emoji starts fresh", 'e', '\U0001F468', false},
	}
	for _, tt := range tests {
		if got := ContinuesCluster(tt.prev, tt.r); got != tt.continues {
			t.Errorf("ContinuesCluster(%q, %q) = %v, want %v", tt.prev, tt.r, got, tt.continues)
		}
	}
}

// The #56 composition: with clusters as the unit under the word class
// rule, a combining mark after a word rune no longer reads as a
// boundary — the accented cluster is one word unit for stepping,
// deletion, and double-click alike.
func TestWordBoundariesOverClusters(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{"an accented cluster is one word unit", "caf" + accentedE + " blues", []string{"caf" + accentedE, "blues"}},
		{"a family emoji is one gap cluster", "hi " + familyEmoji + "!", []string{"hi"}},
		{"a flag is one gap cluster", flagEmoji + " x", []string{"x"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rs := []rune(tt.in)
			b := WordBoundaries(rs)
			if got := wordsOf(rs, b); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("words = %q, want %q", got, tt.want)
			}
		})
	}

	// Stepping lands past the accented cluster, not before its mark.
	cafe := []rune("caf" + accentedE + " blues")
	if got := WordEnd(cafe, 0); got != 5 {
		t.Errorf("WordEnd = %d, want 5 (past the accented cluster)", got)
	}
	if got := WordStart(cafe, 7); got != 6 {
		t.Errorf("WordStart = %d, want 6 (start of blues)", got)
	}
	if got := WordEnd([]rune("x "+accentedE), 1); got != 4 {
		t.Errorf("WordEnd = %d, want 4 (past the accented cluster)", got)
	}

	// Double-click grabs the accented cluster's whole word.
	if start, end := WordRun(cafe, 2); start != 0 || end != 5 {
		t.Errorf("WordRun = %d..%d, want 0..5", start, end)
	}

	// The ctrl+arrows / double-click agreement holds over clusters.
	for _, in := range []string{"caf" + accentedE + ".blues", familyEmoji + "  x", "👍🏽_ok"} {
		rs := []rune(in)
		b := WordBoundaries(rs)
		for at := range rs {
			if !b[at] || !IsWordRune(rs[at]) {
				continue
			}
			start, end := WordRun(rs, at)
			if start != at {
				t.Fatalf("WordRun(%q, %d) start = %d, want the word start", in, at, start)
			}
			if got := WordEnd(rs, start); got != end {
				t.Errorf("%q: WordEnd(%d) = %d, want the WordRun end %d", in, start, got, end)
			}
			if got := WordStart(rs, end); got != start {
				t.Errorf("%q: WordStart(%d) = %d, want the WordRun start %d", in, end, got, start)
			}
		}
	}
}
