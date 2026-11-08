package icons

import (
	"strings"
	"testing"

	"github.com/stubbedev/lys-lib/render"
)

// Every mapped name resolves to a vendored file, and every vendored
// file decodes to visible, recolorable pixels.
func TestBuiltinSetResolves(t *testing.T) {
	c := New("hicolor")
	c.SetSearchPaths([]string{t.TempDir()}) // no theme anywhere
	tint := render.RGB(0xff, 0, 0)
	for _, name := range BuiltinNames() {
		for _, n := range []string{name, name + "-symbolic"} {
			p, err := c.Lookup(n, 16, 120)
			if err != nil || !strings.HasPrefix(p, builtinPrefix) {
				t.Fatalf("Lookup(%q) = %q, %v", n, p, err)
			}
		}
		ic, err := c.SymbolicIcon(name+"-symbolic", 24, 120, tint)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !inkOf(ic, tint) {
			t.Errorf("%s: no ink in the tint", name)
		}
	}
	if _, err := c.Lookup("settings", 16, 120); err != nil {
		t.Error("a Lucide name did not resolve as itself")
	}
	if _, err := c.Lookup("no-such-icon", 16, 120); err == nil {
		t.Error("an unknown name resolved")
	}
}

// inkOf reports whether any pixel is opaque-ish in exactly the tint.
func inkOf(ic *render.Icon, tint render.Color) bool {
	w, h := ic.Size()
	for y := range h {
		for x := range w {
			r, g, bl, a := ic.At(x, y).RGBA()
			if a > 0x8000 && r > 0x8000 && g < 0x1000 && bl < 0x1000 {
				return true
			}
		}
	}
	return false
}
