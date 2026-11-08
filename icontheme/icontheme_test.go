package icontheme

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

const square = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 16 16"><rect width="16" height="16" fill="#%s"/></svg>`

func writeTheme(t *testing.T, base, name, inherits string, icons map[string]string) {
	t.Helper()
	dir := filepath.Join(base, "icons", name)
	index := "[Icon Theme]\nName=" + name + "\nDirectories=16x16/apps\n"
	if inherits != "" {
		index += "Inherits=" + inherits + "\n"
	}
	index += "\n[16x16/apps]\nSize=16\nType=Fixed\n"
	if err := os.MkdirAll(filepath.Join(dir, "16x16", "apps"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "index.theme"), []byte(index), 0o644); err != nil {
		t.Fatal(err)
	}
	for icon, color := range icons {
		svg := fmt.Appendf(nil, square, color)
		if err := os.WriteFile(filepath.Join(dir, "16x16", "apps", icon+".svg"), svg, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestIconsResolveThroughTheThemeChain(t *testing.T) {
	base := t.TempDir()
	writeTheme(t, base, "mine", "hicolor", map[string]string{"own": "ff0000"})
	writeTheme(t, base, "hicolor", "", map[string]string{"inherited": "00ff00"})
	theme := New("mine", []string{filepath.Join(base, "icons")})
	for name, want := range map[string][4]uint32{"own": {0xffff, 0, 0, 0xffff}, "inherited": {0, 0xffff, 0, 0xffff}} {
		ic, err := theme.Icon(name, 16, 240)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if w, h := ic.Size(); w != 32 || h != 32 {
			t.Fatalf("%s at 16 logical pixels and scale 2 is %dx%d, want 32x32", name, w, h)
		}
		r, g, b, a := ic.At(16, 16).RGBA()
		if [4]uint32{r, g, b, a} != want {
			t.Fatalf("%s is %v in the middle, want %v", name, ic.At(16, 16), want)
		}
	}
	if _, err := theme.Icon("lys-no-such-icon", 16, 120); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an unknown icon: %v, want ErrNotFound", err)
	}
}
