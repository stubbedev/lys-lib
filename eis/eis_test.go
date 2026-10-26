package eis

import "testing"

// An empty seat name is refused by Pair and Serve alike, before any
// socket is made or any byte is sent; it never falls back to a default.
func TestAnEmptySeatNameIsRefused(t *testing.T) {
	const want = "eis: the seat name is empty: the client lists the seat and its devices by it"
	if f, err := Pair(&recorder{}, ""); f != nil || err == nil || err.Error() != want {
		t.Errorf("Pair with no seat = %v, %v; want %q", f, err, want)
	}
	if err := Serve(nil, &recorder{}, ""); err == nil || err.Error() != want {
		t.Errorf("Serve with no seat = %v; want %q", err, want)
	}
}
