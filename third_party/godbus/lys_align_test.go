package dbus

import "testing"

func TestSigAlignmentMatchesTheTypes(t *testing.T) {
	for _, sig := range []string{"y", "b", "n", "q", "i", "u", "x", "t", "d", "s", "o", "g", "v", "h", "ay", "as", "a{sv}", "a(ii)"} {
		if got, want := sigAlignment(sig), alignment(typeFor(sig)); got != want {
			t.Errorf("%q aligns to %d, want %d", sig, got, want)
		}
	}
	for _, sig := range []string{"(ii)", "(y)", "{sv}"} {
		if got := sigAlignment(sig); got != 8 {
			t.Errorf("%q aligns to %d, want a struct's 8", sig, got)
		}
	}
}
