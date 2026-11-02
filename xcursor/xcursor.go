// Package xcursor reads Xcursor files and resolves cursor themes the way
// libXcursor does: XCURSOR_PATH, theme inheritance and nominal sizes.
package xcursor

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

const (
	magic      = "Xcur"
	imageType  = 0xfffd0002
	headerSize = 16
	imageSize  = 36
	maxSide    = 0x7fff
)

// Image is one cursor frame. Pixels are as the file stores them,
// premultiplied ARGB32 in little-endian bytes, which is what wl_shm's
// argb8888 means, four bytes a pixel with no padding. Delay is how long
// the frame of an animated cursor shows, in milliseconds.
type Image struct {
	Size    image.Point
	Hotspot image.Point
	Nominal int
	Delay   uint32
	Pixels  []byte
}

type tocEntry struct {
	nominal int
	pos     uint32
}

// Nearest reads the first image of the size nearest to want, leaving the
// rest of the file unread.
func Nearest(r io.ReaderAt, size int64, want int) (Image, error) {
	toc, err := readToc(r, size)
	if err != nil {
		return Image{}, err
	}
	return readImage(r, size, toc[nearestIndex(toc, want)].pos)
}

// NearestFrames reads every frame of the size nearest to want, in file
// order: the frames of a cursor that is animated at that size.
func NearestFrames(r io.ReaderAt, size int64, want int) ([]Image, error) {
	toc, err := readToc(r, size)
	if err != nil {
		return nil, err
	}
	nominal := toc[nearestIndex(toc, want)].nominal
	return readImages(r, size, slices.DeleteFunc(toc, func(e tocEntry) bool { return e.nominal != nominal }))
}

// All reads every image of the file, in file order.
func All(r io.ReaderAt, size int64) ([]Image, error) {
	toc, err := readToc(r, size)
	if err != nil {
		return nil, err
	}
	return readImages(r, size, toc)
}

func readImages(r io.ReaderAt, size int64, toc []tocEntry) ([]Image, error) {
	images := make([]Image, len(toc))
	for i, e := range toc {
		img, err := readImage(r, size, e.pos)
		if err != nil {
			return nil, err
		}
		images[i] = img
	}
	return images, nil
}

func nearestIndex(toc []tocEntry, want int) int {
	best := 0
	for i, e := range toc {
		if dist(e.nominal, want) < dist(toc[best].nominal, want) {
			best = i
		}
	}
	return best
}

// readToc lists the file's images; a file with none is an error, so toc
// always has an entry.
func readToc(r io.ReaderAt, size int64) ([]tocEntry, error) {
	le := binary.LittleEndian
	var head [headerSize]byte
	if size < headerSize {
		return nil, errors.New("xcursor: not an Xcursor file")
	}
	if _, err := r.ReadAt(head[:], 0); err != nil || string(head[:4]) != magic {
		return nil, errors.New("xcursor: not an Xcursor file")
	}
	header, ntoc := le.Uint32(head[4:]), le.Uint32(head[12:])
	if int64(header)+int64(ntoc)*12 > size {
		return nil, fmt.Errorf("xcursor: table of contents with %d entries does not fit %d bytes", ntoc, size)
	}
	raw := make([]byte, int(ntoc)*12)
	if _, err := r.ReadAt(raw, int64(header)); err != nil {
		return nil, fmt.Errorf("xcursor: read the table of contents: %w", err)
	}
	toc := make([]tocEntry, 0, ntoc)
	for i := range int(ntoc) {
		entry := raw[i*12:]
		if le.Uint32(entry) == imageType {
			toc = append(toc, tocEntry{nominal: int(le.Uint32(entry[4:])), pos: le.Uint32(entry[8:])})
		}
	}
	if len(toc) == 0 {
		return nil, errors.New("xcursor: no images")
	}
	return toc, nil
}

func readImage(r io.ReaderAt, size int64, pos uint32) (Image, error) {
	le := binary.LittleEndian
	if int64(pos)+imageSize > size {
		return Image{}, fmt.Errorf("xcursor: image header at %d runs past %d bytes", pos, size)
	}
	var h [imageSize]byte
	if _, err := r.ReadAt(h[:], int64(pos)); err != nil {
		return Image{}, fmt.Errorf("xcursor: read the image header at %d: %w", pos, err)
	}
	if le.Uint32(h[:]) != imageSize || le.Uint32(h[4:]) != imageType {
		return Image{}, fmt.Errorf("xcursor: malformed image header at %d", pos)
	}
	nominal, w, ht, xhot, yhot, delay := le.Uint32(h[8:]), le.Uint32(h[16:]), le.Uint32(h[20:]), le.Uint32(h[24:]), le.Uint32(h[28:]), le.Uint32(h[32:])
	if w == 0 || ht == 0 || w > maxSide || ht > maxSide || xhot > w || yhot > ht || nominal > maxSide {
		return Image{}, fmt.Errorf("xcursor: image %dx%d with hotspot %d,%d at %d is invalid", w, ht, xhot, yhot, pos)
	}
	start := int64(pos) + imageSize
	pixels := make([]byte, int(w)*int(ht)*4)
	if start+int64(len(pixels)) > size {
		return Image{}, fmt.Errorf("xcursor: %dx%d pixels at %d run past %d bytes", w, ht, pos, size)
	}
	if _, err := r.ReadAt(pixels, start); err != nil {
		return Image{}, fmt.Errorf("xcursor: read %dx%d pixels at %d: %w", w, ht, pos, err)
	}
	return Image{
		Size:    image.Pt(int(w), int(ht)),
		Hotspot: image.Pt(int(xhot), int(yhot)),
		Nominal: int(nominal),
		Delay:   delay,
		Pixels:  pixels,
	}, nil
}

// SearchPath lists the directories a theme is looked for in: XCURSOR_PATH,
// else the XDG data directories' icons, ~/.icons and /usr/share/pixmaps.
func SearchPath(getenv func(string) string) []string {
	if p := getenv("XCURSOR_PATH"); p != "" {
		return filepath.SplitList(p)
	}
	var dirs []string
	if home := getenv("HOME"); home != "" {
		data := getenv("XDG_DATA_HOME")
		if data == "" {
			data = filepath.Join(home, ".local", "share")
		}
		dirs = append(dirs, filepath.Join(data, "icons"), filepath.Join(home, ".icons"))
	}
	xdg := getenv("XDG_DATA_DIRS")
	if xdg == "" {
		xdg = "/usr/local/share:/usr/share"
	}
	for _, d := range filepath.SplitList(xdg) {
		dirs = append(dirs, filepath.Join(d, "icons"))
	}
	return append(dirs, "/usr/share/pixmaps")
}

// Theme is a cursor theme resolved through its Inherits.
type Theme struct {
	name string
	dirs []string
}

// Open resolves the theme's cursor directories along searchPath, the theme
// first and then what it inherits. A theme with no cursors anywhere is an
// error; a malformed index.theme is too.
func Open(name string, searchPath []string) (*Theme, error) {
	t := &Theme{name: name}
	if err := t.resolve(name, searchPath, map[string]bool{}); err != nil {
		return nil, err
	}
	if len(t.dirs) == 0 {
		return nil, fmt.Errorf("xcursor: no cursors for theme %q in %v", name, searchPath)
	}
	return t, nil
}

func (t *Theme) resolve(name string, searchPath []string, visited map[string]bool) error {
	if visited[name] {
		return nil
	}
	visited[name] = true
	var inherits []string
	for _, base := range searchPath {
		dir := filepath.Join(base, name)
		if info, err := os.Stat(filepath.Join(dir, "cursors")); err == nil && info.IsDir() && !slices.Contains(t.dirs, filepath.Join(dir, "cursors")) {
			t.dirs = append(t.dirs, filepath.Join(dir, "cursors"))
		}
		parents, err := readInherits(filepath.Join(dir, "index.theme"))
		if err != nil {
			return err
		}
		inherits = append(inherits, parents...)
	}
	for _, parent := range inherits {
		if err := t.resolve(parent, searchPath, visited); err != nil {
			return err
		}
	}
	return nil
}

func readInherits(path string) ([]string, error) {
	f, err := os.Open(path) //nolint:gosec // theme files come from the user's XCURSOR_PATH
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("xcursor: open %s: %w", path, err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		key, value, ok := strings.Cut(sc.Text(), "=")
		if ok && strings.TrimSpace(key) == "Inherits" {
			var out []string
			for p := range strings.FieldsFuncSeq(value, func(r rune) bool { return r == ',' || r == ';' || r == ' ' }) {
				out = append(out, p)
			}
			return out, nil
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("xcursor: read %s: %w", path, err)
	}
	return nil, nil
}

// Load reads the first image nearest to size of the first of names the
// theme has.
func (t *Theme) Load(size int, names ...string) (Image, error) {
	for _, name := range names {
		var img Image
		ok, err := t.read(name, func(f *os.File, fileSize int64) (err error) {
			img, err = Nearest(f, fileSize, size)
			return err
		})
		if err != nil {
			return Image{}, err
		}
		if ok {
			return img, nil
		}
	}
	return Image{}, fmt.Errorf("xcursor: theme %q has none of %v", t.name, names)
}

// Frames reads every frame of the named cursor at the size nearest to size.
// A theme without it is an error wrapping fs.ErrNotExist.
func (t *Theme) Frames(size int, name string) ([]Image, error) {
	var frames []Image
	ok, err := t.read(name, func(f *os.File, fileSize int64) (err error) {
		frames, err = NearestFrames(f, fileSize, size)
		return err
	})
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("xcursor: theme %q has no %s: %w", t.name, name, fs.ErrNotExist)
	}
	return frames, nil
}

func (t *Theme) read(name string, parse func(*os.File, int64) error) (bool, error) {
	for _, dir := range t.dirs {
		f, err := os.Open(filepath.Join(dir, name)) //nolint:gosec // theme files come from the user's XCURSOR_PATH
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return false, fmt.Errorf("xcursor: open %s from theme %q: %w", name, t.name, err)
		}
		if err := parseFile(f, parse); err != nil {
			return false, fmt.Errorf("xcursor: %s in theme %q: %w", name, t.name, err)
		}
		return true, nil
	}
	return false, nil
}

func parseFile(f *os.File, parse func(*os.File, int64) error) (err error) {
	defer func() { err = errors.Join(err, f.Close()) }()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	return parse(f, info.Size())
}

func dist(a, b int) int {
	if a > b {
		return a - b
	}
	return b - a
}
