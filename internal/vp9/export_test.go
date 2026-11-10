package vp9

import "testing"

// Packets is the packets of the stream ffmpeg's libvpx codes of src in
// passes, each with its arguments.
func Packets(t *testing.T, src string, passes ...[]string) [][]byte {
	t.Helper()
	pkts, _ := traceIVF(t, src, passes...)
	return pkts
}
