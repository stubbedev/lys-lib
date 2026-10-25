package text

import "github.com/go-text/typesetting/segmenter"

// LineSegments splits s at its line-break opportunities (UAX #14), each
// segment a word with the spaces after it: "a/b c" gives "a/", "b ",
// "c". Invalid UTF-8 is one segment.
func LineSegments(s string) []string {
	var seg segmenter.Segmenter
	if err := seg.InitWithString(s); err != nil {
		return []string{s}
	}
	var out []string
	for it := seg.LineIterator(); it.Next(); {
		out = append(out, string(it.Line().Text))
	}
	return out
}
