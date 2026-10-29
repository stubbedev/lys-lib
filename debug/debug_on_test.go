//go:build lysdebug

package debug

import "testing"

func TestCategoriesFollowLYSDEBUG(t *testing.T) {
	for _, c := range []struct {
		spec   string
		admits map[string]bool
	}{
		{"", map[string]bool{"wire": true, "demo": true}},
		{"*", map[string]bool{"wire": true, "demo": true}},
		{"demo", map[string]bool{"wire": false, "demo": true}},
		{"demo, wire", map[string]bool{"wire": true, "demo": true, "frame": false}},
		{"-wire", map[string]bool{"wire": false, "demo": true}},
		{"*,-wire", map[string]bool{"wire": false, "demo": true}},
		{"demo,-demo", map[string]bool{"demo": false, "wire": false}},
	} {
		f := parseCategories(c.spec)
		for category, want := range c.admits {
			if got := f.admits(category); got != want {
				t.Errorf("LYS_DEBUG=%q admits %s = %v, want %v", c.spec, category, got, want)
			}
		}
	}
}
