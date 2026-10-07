package render

import "testing"

// The implicit and compact path forms come out explicit.
func TestNormalizePathData(t *testing.T) {
	for in, want := range map[string]string{
		"M1 2 3 4 5 6":                 "M 1 2 L 3 4 L 5 6",
		"m1 2 3 4":                     "m 1 2 l 3 4",
		"a2 2 0 0 1 4 0 2 2 0 0 0 3 2": "a 2 2 0 0 1 4 0 a 2 2 0 0 0 3 2",
		"a1 1 0 011 1":                 "a 1 1 0 0 1 1 1",
		"M.5.5L1-1z":                   "M .5 .5 L 1 -1 z",
		"M1e2,3E-1 h4":                 "M 1e2 3E-1 h 4",
		"m21 21-4.34-4.34":             "m 21 21 l -4.34 -4.34",
	} {
		if got := normalizePathData(in); got != want {
			t.Errorf("%q\n got %q\nwant %q", in, got, want)
		}
	}
}
