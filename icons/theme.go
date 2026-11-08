// Package icons implements the freedesktop icon theme spec in pure Go:
// index.theme parsing with inheritance, the size/threshold directory
// lookup with hicolor fallback, and a per-(name, size, scale) raster
// cache with symbolic icon recoloring. It is the lookup half of
// lys-ui's widget.NewThemeIcon; the SVG/PNG decoding itself lives in render.
//
// Search paths follow the basedir spec: the legacy ~/.icons,
// $XDG_DATA_HOME/icons, every $XDG_DATA_DIRS entry's icons
// subdirectory, and the /usr/share/pixmaps fallback. A Cache carries
// its own paths so tests can point it at a fixture tree; nil restores
// the environment default.
package icons

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// DirType is an icon directory's size-matching rule from index.theme.
type DirType uint8

const (
	// Threshold matches within Size ± Threshold; it is the spec
	// default when a directory declares no Type.
	Threshold DirType = iota
	// Fixed matches exactly Size.
	Fixed
	// Scalable matches everything between MinSize and MaxSize.
	Scalable
)

// Dir is one icon subdirectory declared by a theme's index.theme.
type Dir struct {
	// Path is the directory relative to the theme root, as named in
	// the Directories or ScaledDirectories list: "16x16/apps".
	Path      string
	Size      int
	MinSize   int
	MaxSize   int
	Threshold int
	Scale     int
	Type      DirType
}

// Theme is a parsed index.theme: the display name, the themes it
// inherits, and the icon directories it declares.
type Theme struct {
	// Name is the display name from the [Icon Theme] section.
	Name string
	// DirName is the theme's directory name - the path component
	// icons live under. It is what lookups join on, not Name.
	DirName  string
	Inherits []string
	Dirs     []Dir
}

// loadTheme parses the index.theme of the theme called dirName from the
// first base directory that has one, or nil when none does.
func loadTheme(fsys themeFS, baseDirs []string, dirName string) *Theme {
	for _, base := range baseDirs {
		data, err := fsys.ReadFile(filepath.Join(base, dirName, "index.theme"))
		if err != nil {
			continue
		}
		t := parseIndex(data)
		t.DirName = dirName
		return t
	}
	return nil
}

// parseIndex parses index.theme bytes. Both Directories and
// ScaledDirectories are read into Dirs - a directory's Scale comes from
// its own section - and entries without a usable section are skipped.
func parseIndex(data []byte) *Theme {
	secs := parseINI(data)
	icon := secs["Icon Theme"]
	t := &Theme{Name: icon["Name"]}
	for inh := range strings.SplitSeq(icon["Inherits"], ",") {
		if inh = strings.TrimSpace(inh); inh != "" {
			t.Inherits = append(t.Inherits, inh)
		}
	}
	seen := map[string]bool{}
	for _, list := range []string{icon["Directories"], icon["ScaledDirectories"]} {
		for entry := range strings.SplitSeq(list, ",") {
			entry = strings.TrimSpace(entry)
			if entry == "" || seen[entry] {
				continue
			}
			seen[entry] = true
			if d, ok := dirFromSection(secs[entry], entry); ok {
				t.Dirs = append(t.Dirs, d)
			}
		}
	}
	return t
}

// dirFromSection interprets one [path] section of index.theme; ok is
// false when the section is missing or has no usable Size.
func dirFromSection(sec map[string]string, path string) (Dir, bool) {
	size, err := strconv.Atoi(strings.TrimSpace(sec["Size"]))
	if err != nil || size <= 0 {
		return Dir{}, false
	}
	d := Dir{
		Path:      path,
		Size:      size,
		MinSize:   size,
		MaxSize:   size,
		Threshold: 2,
		Scale:     1,
		Type:      Threshold,
	}
	if v, err := strconv.Atoi(strings.TrimSpace(sec["MinSize"])); err == nil && v > 0 {
		d.MinSize = v
	}
	if v, err := strconv.Atoi(strings.TrimSpace(sec["MaxSize"])); err == nil && v > 0 {
		d.MaxSize = v
	}
	if v, err := strconv.Atoi(strings.TrimSpace(sec["Threshold"])); err == nil && v >= 0 {
		d.Threshold = v
	}
	if v, err := strconv.Atoi(strings.TrimSpace(sec["Scale"])); err == nil && v > 0 {
		d.Scale = v
	}
	switch strings.ToLower(strings.TrimSpace(sec["Type"])) {
	case "fixed":
		d.Type = Fixed
	case "scalable":
		d.Type = Scalable
	}
	return d, true
}

// matches reports whether d accepts an icon of the requested logical
// size at integer directory scale scale, per the spec's
// DirectoryMatchesSize. Only directories at the requested scale match.
func (d Dir) matches(size, scale int) bool {
	if d.Scale != scale {
		return false
	}
	switch d.Type {
	case Fixed:
		return d.Size == size
	case Scalable:
		return d.MinSize <= size && size <= d.MaxSize
	default:
		return d.Size-d.Threshold <= size && size <= d.Size+d.Threshold
	}
}

// distance returns how far d is from the requested size, per the spec's
// DirectorySizeDistance: zero inside the accepted range, the shortfall
// or overshoot outside it.
func (d Dir) distance(size int) int {
	switch d.Type {
	case Fixed:
		return abs(d.Size - size)
	case Scalable:
		return max(d.MinSize-size, size-d.MaxSize, 0)
	default:
		return max(d.Size-d.Threshold-size, size-(d.Size+d.Threshold), 0)
	}
}

// parseINI parses the desktop-entry-style keyfile an index.theme is:
// [Section] headers, Key=Value lines, # comments. Duplicate keys keep
// the first value, matching how the spec's reference readers behave.
func parseINI(data []byte) map[string]map[string]string {
	secs := map[string]map[string]string{}
	cur := ""
	for line := range strings.SplitSeq(string(data), "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			cur = line[1 : len(line)-1]
			if _, ok := secs[cur]; !ok {
				secs[cur] = map[string]string{}
			}
			continue
		}
		if cur == "" {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		if _, dup := secs[cur][key]; !dup {
			secs[cur][key] = strings.TrimSpace(value)
		}
	}
	return secs
}

// themeChain walks the inheritance chain of themeName: the theme
// itself, then its Inherits depth-first (each name resolves against the
// first base directory holding its index.theme), then DefaultTheme and
// hicolor, the fallbacks GTK 4 always looks in. An inherited hicolor
// waits for the end: the spec makes it the last resort, after Adwaita. Missing themes and inheritance cycles are
// tolerated; the depth cap keeps a corrupt index from looping.
func themeChain(fsys themeFS, themeName string, baseDirs []string) []*Theme {
	var chain []*Theme
	seen := map[string]bool{}
	var walk func(name string, depth int)
	walk = func(name string, depth int) {
		if name == "" || depth > maxInheritDepth || seen[name] || (depth > 0 && name == "hicolor") {
			return
		}
		seen[name] = true
		t := loadTheme(fsys, baseDirs, name)
		if t == nil {
			return
		}
		chain = append(chain, t)
		for _, inh := range t.Inherits {
			walk(inh, depth+1)
		}
	}
	walk(themeName, 0)
	walk(DefaultTheme, 0)
	walk("hicolor", 0)
	return chain
}

// maxInheritDepth caps Inherits recursion.
const maxInheritDepth = 16

// defaultSearchPaths returns the base directories of the basedir spec:
// the legacy ~/.icons, $XDG_DATA_HOME/icons (default ~/.local/share/
// icons), each $XDG_DATA_DIRS entry's icons subdirectory (default
// /usr/local/share:/usr/share), and the /usr/share/pixmaps fallback.
// Empty environment entries and duplicates are dropped.
func defaultSearchPaths() []string {
	home, _ := os.UserHomeDir()
	var dirs []string
	add := func(dir string) {
		if dir != "" && !slices.Contains(dirs, dir) {
			dirs = append(dirs, dir)
		}
	}
	add(filepath.Join(home, ".icons"))
	dataHome := os.Getenv("XDG_DATA_HOME")
	if dataHome == "" {
		dataHome = filepath.Join(home, ".local", "share")
	}
	add(filepath.Join(dataHome, "icons"))
	dataDirs := os.Getenv("XDG_DATA_DIRS")
	if dataDirs == "" {
		dataDirs = "/usr/local/share:/usr/share"
	}
	for _, dir := range filepath.SplitList(dataDirs) {
		add(filepath.Join(dir, "icons"))
	}
	add("/usr/share/pixmaps")
	return dirs
}

// fracDenom is the 120-based fractional-scale denominator:
// a scale of 150 is 1.25, 240 is 2.
const fracDenom = 120

// DeviceBox rounds a logical icon size up to device pixels at the
// 120-based scale frac120:
// an icon requested at 16 px on a 1.25x
// surface rasterizes into a 20 px box.
func DeviceBox(size int, frac120 uint32) int {
	if frac120 == 0 {
		frac120 = fracDenom
	}
	return (size*int(frac120) + fracDenom - 1) / fracDenom
}

// dirScale rounds frac120 to the integer directory scale the spec
// matches directories against: a 1.25x surface uses 1x directories and
// rasterizes them at the larger device box.
func dirScale(frac120 uint32) int {
	if frac120 == 0 {
		frac120 = fracDenom
	}
	return max(int((frac120+fracDenom/2)/fracDenom), 1)
}

// abs returns |v|.
func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
