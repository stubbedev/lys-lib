package va

import (
	"errors"
	"path/filepath"
	"testing"
)

// A display opens on each render node a VA driver serves and lists its
// encode entrypoints; without libva, or without a driver for a node,
// opening reports it. Where the host has a VA driver this exercises
// every call up to the encode itself.
func TestOpenListsEncoders(t *testing.T) {
	nodes, _ := filepath.Glob("/dev/dri/renderD*")
	if len(nodes) == 0 {
		t.Skip("no DRM render node")
	}
	for _, n := range nodes {
		d, err := Open(n)
		if errors.Is(err, ErrNoLibrary) {
			t.Skipf("no libva: %v", err)
		}
		if err != nil {
			t.Logf("%s: %v", n, err)
			continue
		}
		for _, p := range []struct {
			name    string
			profile int32
		}{{"H.264 High", ProfileH264High}, {"H.264 Main", ProfileH264Main}, {"HEVC Main", ProfileHEVCMain}} {
			t.Logf("%s (%s): %s entrypoints %v", n, d.Vendor(), p.name, d.Entrypoints(p.profile))
		}
		d.Close()
	}
}
