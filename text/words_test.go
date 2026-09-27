package text

import (
	"reflect"
	"testing"
)

// wordsOf extracts the word runs the boundaries bracket: spans that
// start at a boundary on a word rune and run to the next boundary.
func wordsOf(rs []rune, b []bool) []string {
	var out []string
	for i := 0; i < len(rs); i++ {
		if !b[i] || !IsWordRune(rs[i]) {
			continue
		}
		j := i + 1
		for j < len(rs) && !b[j] {
			j++
		}
		out = append(out, string(rs[i:j]))
		i = j - 1
	}
	return out
}

func TestWordBoundaries(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{"empty", "", nil},
		{"single word", "hello", []string{"hello"}},
		{"two words", "hello world", []string{"hello", "world"}},
		{"dotted host splits on the dots", "server.hostname.example", []string{"server", "hostname", "example"}},
		{"a punctuation run is one gap", "foo...bar", []string{"foo", "bar"}},
		{"punctuation only has no words", "... !? ,", nil},
		{"unicode letters are word runes", "æøå blåbær", []string{"æøå", "blåbær"}},
		{"digits and underscore join the word", "x_1 42", []string{"x_1", "42"}},
		{"mixed whitespace is one gap per run", "  a \t\t b", []string{"a", "b"}},
		{"trailing gap", "word   ", []string{"word"}},
		{"leading gap", "   word", []string{"word"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rs := []rune(tt.in)
			b := WordBoundaries(rs)
			if len(b) != len(rs)+1 {
				t.Fatalf("WordBoundaries len = %d, want %d", len(b), len(rs)+1)
			}
			if !b[0] || !b[len(rs)] {
				t.Fatalf("slice edges must be boundaries: first=%v last=%v", b[0], b[len(rs)])
			}
			if got := wordsOf(rs, b); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("words = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestWordStartWordEnd(t *testing.T) {
	// "server.hostname.example": words [0,6), [7,15), [16,23).
	host := "server.hostname.example"
	tests := []struct {
		name  string
		in    string
		at    int
		start int
		end   int
	}{
		{"mid first word", host, 3, 0, 6},
		{"on the first dot", host, 6, 0, 15},
		{"a word start steps on to the previous word", host, 7, 0, 15},
		{"mid last word", host, 20, 16, 23},
		{"at the slice end", host, len(host), 16, len(host)},
		{"clamped past the end", host, len(host) + 5, 16, len(host)},

		{"a word end steps over the gap back", "hello world", 6, 0, 11},
		{"a gap of two spaces skips whole", "ab  cd", 4, 0, 6},
		{"a word end steps to the next word end", "ab  cd", 2, 0, 6},
		{"trailing gap walks back to the word", "word   ", 7, 0, 7},
		{"mid trailing gap walks to the slice end", "word   ", 6, 0, 7},
		{"leading gap walks forward to the word", "   word", 2, 0, 7},
		{"gaps only collapses to the start", " . . ", 5, 0, 5},

		{"unicode words", "æøå øl", 5, 4, 6},
		{"empty", "", 0, 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rs := []rune(tt.in)
			if got := WordStart(rs, tt.at); got != tt.start {
				t.Errorf("WordStart(%q, %d) = %d, want %d", tt.in, tt.at, got, tt.start)
			}
			if got := WordEnd(rs, tt.at); got != tt.end {
				t.Errorf("WordEnd(%q, %d) = %d, want %d", tt.in, tt.at, got, tt.end)
			}
		})
	}
}

// TestWordSteps checks the motion invariants for every caret position:
// stepping strictly progresses and always lands on a word edge, so
// repeated ctrl+arrows walk the words and stop at the slice bounds.
func TestWordSteps(t *testing.T) {
	for _, in := range []string{"server.hostname.example", "æøå, blåbær;  x_1\t...end", "  ", ""} {
		rs := []rune(in)
		for at := range len(rs) + 1 {
			if s := WordStart(rs, at); at > 0 && s >= at {
				t.Errorf("WordStart(%q, %d) = %d, want strict progress", in, at, s)
			}
			if s := WordStart(rs, at); s != 0 && IsWordRune(rs[s-1]) {
				t.Errorf("WordStart(%q, %d) = %d lands mid-word", in, at, s)
			}
			if e := WordEnd(rs, at); at < len(rs) && e <= at {
				t.Errorf("WordEnd(%q, %d) = %d, want strict progress", in, at, e)
			}
			if e := WordEnd(rs, at); e != len(rs) && IsWordRune(rs[e]) {
				t.Errorf("WordEnd(%q, %d) = %d lands mid-word", in, at, e)
			}
		}
	}
}

func TestWordRun(t *testing.T) {
	tests := []struct {
		name  string
		in    string
		at    int
		start int
		end   int
	}{
		{"inside a word", "foo.bar baz", 1, 0, 3},
		{"at a word start", "foo.bar baz", 4, 4, 7},
		{"a punctuation run is its own span", "foo.bar baz", 3, 3, 4},
		{"a whitespace run is its own span", "foo.bar  baz", 8, 7, 9},
		{"at the slice end clamps back", "word", 4, 0, 4},
		{"negative clamps forward", "word", -1, 0, 4},
		{"empty", "", 0, 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			start, end := WordRun([]rune(tt.in), tt.at)
			if start != tt.start || end != tt.end {
				t.Errorf("WordRun(%q, %d) = %d..%d, want %d..%d", tt.in, tt.at, start, end, tt.start, tt.end)
			}
		})
	}
}

// TestWordRunAgreesWithSteps pins the double-click/ctrl+arrows
// contract: a word span from WordRun is exactly the span stepping
// crosses — WordEnd of its start is its end, WordStart of its end is
// its start.
func TestWordRunAgreesWithSteps(t *testing.T) {
	for _, in := range []string{"foo.bar baz", "æøå.blåbær", "a  b"} {
		rs := []rune(in)
		b := WordBoundaries(rs)
		for at := range rs {
			if !b[at] || !IsWordRune(rs[at]) {
				continue // only word starts open a span
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
