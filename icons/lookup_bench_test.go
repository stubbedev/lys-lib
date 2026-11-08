package icons

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var benchContexts = []string{
	"actions", "animations", "apps", "categories", "devices", "emblems", "emotes",
	"filesystems", "intl", "legacy", "mimetypes", "places", "status", "stock/chart",
	"stock/code", "stock/data", "stock/form", "stock/image", "stock/io", "stock/media",
	"stock/navigation", "stock/net", "stock/object",
}

var benchSizes = []int{8, 16, 22, 24, 32, 36, 48, 64, 72, 96, 128, 192, 256, 512}

// benchTree lays out a desktop-sized search path: 36 base directories,
// a third of them missing and most of the rest empty, with an
// Adwaita-like theme and a hicolor declaring 23 contexts at 14 sizes
// and two scales, as the real hicolor's 649 directories do. It returns
// the base directories and the 72 names a settings page asks for: 36
// symbolic Adwaita glyphs, 24 hicolor app icons, 12 names no theme has.
func benchTree(b *testing.B) ([]string, []string) {
	b.Helper()
	root := b.TempDir()
	write := func(rel, data string) {
		b.Helper()
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			b.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			b.Fatal(err)
		}
	}
	var dirs []string
	var sections strings.Builder
	for _, scale := range []int{1, 2} {
		for _, size := range benchSizes {
			for _, ctx := range benchContexts {
				suffix := ""
				if scale > 1 {
					suffix = fmt.Sprintf("@%d", scale)
				}
				dir := fmt.Sprintf("%dx%d%s/%s", size, size, suffix, ctx)
				dirs = append(dirs, dir)
				fmt.Fprintf(&sections, "\n[%s]\nSize=%d\nScale=%d\nType=Threshold\n", dir, size, scale)
			}
		}
	}
	for _, ctx := range benchContexts {
		for _, kind := range []string{"scalable", "symbolic"} {
			dir := kind + "/" + ctx
			dirs = append(dirs, dir)
			fmt.Fprintf(&sections, "\n[%s]\nSize=16\nMinSize=8\nMaxSize=512\nType=Scalable\n", dir)
		}
	}
	themes := filepath.Join("share17", "icons")
	write(filepath.Join(themes, "hicolor", "index.theme"),
		"[Icon Theme]\nName=Hicolor\nDirectories="+strings.Join(dirs, ",")+"\n"+sections.String())
	var adw strings.Builder
	var adwDirs []string
	for _, ctx := range benchContexts[:17] {
		for _, kind := range []string{"scalable", "symbolic"} {
			dir := kind + "/" + ctx
			adwDirs = append(adwDirs, dir)
			fmt.Fprintf(&adw, "\n[%s]\nSize=16\nMinSize=8\nMaxSize=512\nType=Scalable\n", dir)
		}
	}
	write(filepath.Join(themes, "Adwaita", "index.theme"),
		"[Icon Theme]\nName=Adwaita\nInherits=hicolor\nDirectories="+strings.Join(adwDirs, ",")+"\n"+adw.String())

	const svg = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 16 16"/>`
	var names []string
	for i := range 36 {
		name := fmt.Sprintf("glyph%02d-symbolic", i)
		write(filepath.Join(themes, "Adwaita", "symbolic", benchContexts[i%17], name+".svg"), svg)
		names = append(names, name)
	}
	for i := range 24 {
		name := fmt.Sprintf("app%02d", i)
		for _, size := range []int{16, 32, 48, 128} {
			write(filepath.Join(themes, "hicolor", fmt.Sprintf("%dx%d", size, size), "apps", name+".png"), "png")
		}
		write(filepath.Join(themes, "hicolor", "scalable", "apps", name+".svg"), svg)
		names = append(names, name)
	}
	for i := range 12 {
		names = append(names, fmt.Sprintf("absent%02d", i))
	}

	paths := make([]string, 0, 36)
	for i := range 36 {
		dir := filepath.Join(root, fmt.Sprintf("share%02d", i), "icons")
		if i%3 != 0 {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				b.Fatal(err)
			}
		}
		paths = append(paths, dir)
	}
	return paths, names
}

func benchLookups(b *testing.B, c *Cache, names []string, frac uint32) {
	b.Helper()
	for _, name := range names {
		_, _ = c.Lookup(name, 24, frac)
	}
}

// BenchmarkLookup72 resolves the 72 names of an icon picker's first
// open: cold on a fresh cache, which indexes the theme chain, and warm
// on one that has resolved them before (#394).
func BenchmarkLookup72(b *testing.B) {
	paths, names := benchTree(b)
	for _, frac := range []uint32{120, 240} {
		b.Run(fmt.Sprintf("cold/scale%d", frac/fracDenom), func(b *testing.B) {
			for b.Loop() {
				c := New(DefaultTheme)
				c.SetSearchPaths(paths)
				benchLookups(b, c, names, frac)
			}
		})
		b.Run(fmt.Sprintf("warm/scale%d", frac/fracDenom), func(b *testing.B) {
			c := New(DefaultTheme)
			c.SetSearchPaths(paths)
			benchLookups(b, c, names, frac)
			for b.Loop() {
				benchLookups(b, c, names, frac)
			}
		})
	}
}
