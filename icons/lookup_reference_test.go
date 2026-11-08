package icons

import (
	"os"
	"path/filepath"
	"strings"
)

// The lookup as it stood before the theme index (#394): every name
// walks every base directory times every directory of every theme of
// the chain. It stays as the reference the indexed lookup is proven
// equal to.

func refResolve(ix refDirIndex, chain []*Theme, paths []string, name string, size int, frac120 uint32) string {
	scale := dirScale(frac120)
	for _, t := range chain {
		if path := refLookupTheme(ix, t, paths, name, size, scale); path != "" {
			return path
		}
	}
	if path := refLookupUnthemed(ix, paths, name); path != "" {
		return path
	}
	if path := lookupBuiltin(name); path != "" || strings.HasSuffix(name, symbolicSuffix) || name == "" {
		return path
	}
	return refResolve(ix, chain, paths, name+symbolicSuffix, size, frac120)
}

func refLookupTheme(ix refDirIndex, t *Theme, baseDirs []string, name string, size, scale int) string {
	if path := refLookupThemeScale(ix, t, baseDirs, name, size, scale); path != "" {
		return path
	}
	if scale != 1 {
		return refLookupThemeScale(ix, t, baseDirs, name, size, 1)
	}
	return ""
}

func refLookupThemeScale(ix refDirIndex, t *Theme, baseDirs []string, name string, size, scale int) string {
	for _, base := range baseDirs {
		for _, d := range t.Dirs {
			if !d.matches(size, scale) {
				continue
			}
			if path := refFindIconFile(ix, base, t.DirName, d.Path, name); path != "" {
				return path
			}
		}
	}
	best := ""
	bestDist := 0
	for _, base := range baseDirs {
		for _, d := range t.Dirs {
			if d.Scale != scale {
				continue
			}
			dist := d.distance(size)
			if best != "" && dist >= bestDist {
				continue
			}
			if path := refFindIconFile(ix, base, t.DirName, d.Path, name); path != "" {
				best, bestDist = path, dist
			}
		}
	}
	return best
}

func refLookupUnthemed(ix refDirIndex, baseDirs []string, name string) string {
	for _, base := range baseDirs {
		if path := refFindIconFile(ix, base, "", "", name); path != "" {
			return path
		}
	}
	return ""
}

func refFindIconFile(ix refDirIndex, base, theme, dir, name string) string {
	d := filepath.Join(base, theme, dir)
	stems := []string{name}
	if b, ok := strings.CutSuffix(name, symbolicSuffix); ok {
		stems = append(stems, b)
	}
	for _, nm := range stems {
		for _, ext := range extensions {
			if ix.isFile(d, nm+ext) {
				return filepath.Join(d, nm+ext)
			}
		}
	}
	return ""
}

type refDirIndex map[string]map[string]fileKind

func (ix refDirIndex) isFile(dir, name string) bool {
	entries, ok := ix[dir]
	if !ok {
		entries = refReadEntries(dir)
		ix[dir] = entries
	}
	switch entries[name] {
	case fileRegular:
		return true
	case fileLink:
		kind := fileOther
		if info, err := os.Stat(filepath.Join(dir, name)); err == nil && info.Mode().IsRegular() {
			kind = fileRegular
		}
		entries[name] = kind
		return kind == fileRegular
	}
	return false
}

func refReadEntries(dir string) map[string]fileKind {
	des, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	entries := make(map[string]fileKind, len(des))
	for _, de := range des {
		switch t := de.Type(); {
		case t.IsRegular():
			entries[de.Name()] = fileRegular
		case t&os.ModeSymlink != 0:
			entries[de.Name()] = fileLink
		}
	}
	return entries
}
