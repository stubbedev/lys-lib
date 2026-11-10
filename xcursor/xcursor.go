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

	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-lib/xdg"
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

// tocProbe is how much of a file the first read takes: the header and a
// table of four sizes, a still cursor's, in one read. An animated cursor's
// longer table takes a second.
const tocProbe = headerSize + 4*12

// toc is a table of contents as the file stores it, twelve bytes an entry.
type toc []byte

func (t toc) entries() int { return len(t) / 12 }

func (t toc) image(i int) (nominal int, pos uint32, ok bool) {
	e := t[i*12:]
	le := binary.LittleEndian
	return int(le.Uint32(e[4:])), le.Uint32(e[8:]), le.Uint32(e) == imageType
}

// nearest is the nominal size nearest to want among the images, the first
// such image's position, and how many images have that size. A file with
// no images is an error.
func (t toc) nearest(want int) (nominal int, pos uint32, count int, err error) {
	found := false
	for i := range t.entries() {
		n, p, ok := t.image(i)
		switch {
		case !ok:
		case !found || dist(n, want) < dist(nominal, want):
			nominal, pos, count, found = n, p, 1, true
		case n == nominal:
			count++
		}
	}
	if !found {
		return 0, 0, 0, errors.New("xcursor: no images")
	}
	return nominal, pos, count, nil
}

// withToc reads the table of contents and hands it to use, valid for the
// call only.
func withToc[T any](r io.ReaderAt, size int64, use func(toc) (T, error)) (T, error) {
	var zero T
	if size < headerSize {
		return zero, errors.New("xcursor: not an Xcursor file")
	}
	buf := make([]byte, min(size, tocProbe))
	if _, err := r.ReadAt(buf, 0); err != nil || string(buf[:4]) != magic {
		return zero, errors.New("xcursor: not an Xcursor file")
	}
	le := binary.LittleEndian
	header, ntoc := int64(le.Uint32(buf[4:])), int64(le.Uint32(buf[12:]))
	end := header + ntoc*12
	if end > size {
		return zero, fmt.Errorf("xcursor: table of contents with %d entries does not fit %d bytes", ntoc, size)
	}
	if end <= int64(len(buf)) {
		return use(toc(buf[header:end]))
	}
	raw := make([]byte, ntoc*12)
	if _, err := r.ReadAt(raw, header); err != nil {
		return zero, fmt.Errorf("xcursor: read the table of contents: %w", err)
	}
	return use(toc(raw))
}

// Nearest reads the first image of the size nearest to want, leaving the
// rest of the file unread.
func Nearest(r io.ReaderAt, size int64, want int) (Image, error) {
	return withToc(r, size, func(t toc) (Image, error) {
		nominal, pos, _, err := t.nearest(want)
		if err != nil {
			return Image{}, err
		}
		return readImage(r, size, pos, nominal)
	})
}

// NearestFrames reads every frame of the size nearest to want, in file
// order: the frames of a cursor that is animated at that size. Frames
// stored one after the other, as xcursorgen writes them, cost one read and
// share one allocation; their Pixels must not be appended to.
func NearestFrames(r io.ReaderAt, size int64, want int) ([]Image, error) {
	return withToc(r, size, func(t toc) ([]Image, error) {
		nominal, first, count, err := t.nearest(want)
		if err != nil {
			return nil, err
		}
		positions := make([]uint32, 0, count)
		for i := range t.entries() {
			if n, p, ok := t.image(i); ok && n == nominal {
				positions = append(positions, p)
			}
		}
		if frames, ok, err := readRun(r, size, first, positions, nominal); ok || err != nil {
			return frames, err
		}
		frames := make([]Image, len(positions))
		for i, pos := range positions {
			if frames[i], err = readImage(r, size, pos, nominal); err != nil {
				return nil, err
			}
		}
		return frames, nil
	})
}

// Widest reads the widest image of the file, the first of those as wide:
// only the image headers and that image's pixels.
func Widest(r io.ReaderAt, size int64) (Image, error) {
	return withToc(r, size, func(t toc) (Image, error) {
		var best uint32
		var widest uint32
		found := false
		for i := range t.entries() {
			_, pos, ok := t.image(i)
			if !ok {
				continue
			}
			var h [imageSize]byte
			if err := readHeader(r, size, pos, h[:]); err != nil {
				return Image{}, err
			}
			if w := binary.LittleEndian.Uint32(h[16:]); !found || w > widest {
				best, widest, found = pos, w, true
			}
		}
		if !found {
			return Image{}, errors.New("xcursor: no images")
		}
		return readImage(r, size, best, int(widest))
	})
}

func readHeader(r io.ReaderAt, size int64, pos uint32, h []byte) error {
	if int64(pos)+imageSize > size {
		return fmt.Errorf("xcursor: image header at %d runs past %d bytes", pos, size)
	}
	if _, err := r.ReadAt(h, int64(pos)); err != nil {
		return fmt.Errorf("xcursor: read the image header at %d: %w", pos, err)
	}
	return nil
}

// parseImage validates the image header at the start of h and fills in all
// but Pixels; pixelBytes is how many bytes of pixels follow the header.
func parseImage(h []byte, pos uint32) (img Image, pixelBytes int, err error) {
	le := binary.LittleEndian
	if le.Uint32(h) != imageSize || le.Uint32(h[4:]) != imageType {
		return Image{}, 0, fmt.Errorf("xcursor: malformed image header at %d", pos)
	}
	nominal, w, ht, xhot, yhot, delay := le.Uint32(h[8:]), le.Uint32(h[16:]), le.Uint32(h[20:]), le.Uint32(h[24:]), le.Uint32(h[28:]), le.Uint32(h[32:])
	if w == 0 || ht == 0 || w > maxSide || ht > maxSide || xhot > w || yhot > ht || nominal > maxSide {
		return Image{}, 0, fmt.Errorf("xcursor: image %dx%d with hotspot %d,%d at %d is invalid", w, ht, xhot, yhot, pos)
	}
	return Image{
		Size:    image.Pt(int(w), int(ht)),
		Hotspot: image.Pt(int(xhot), int(yhot)),
		Nominal: int(nominal),
		Delay:   delay,
	}, int(w) * int(ht) * 4, nil
}

// readImage reads the image at pos. A cursor image is nearly always
// side by side pixels square, so guessing that from side reads its header
// and pixels at once, into the allocation the pixels keep; a wrong guess
// costs one more read.
func readImage(r io.ReaderAt, size int64, pos uint32, side int) (Image, error) {
	if int64(pos)+imageSize > size {
		return Image{}, fmt.Errorf("xcursor: image header at %d runs past %d bytes", pos, size)
	}
	guess := min(int64(imageSize)+squareBytes(side), size-int64(pos))
	buf := make([]byte, guess)
	if _, err := r.ReadAt(buf, int64(pos)); err != nil {
		return Image{}, fmt.Errorf("xcursor: read the image at %d: %w", pos, err)
	}
	img, pixelBytes, err := parseImage(buf, pos)
	if err != nil {
		return Image{}, err
	}
	start := int64(pos) + imageSize
	if start+int64(pixelBytes) > size {
		return Image{}, fmt.Errorf("xcursor: %dx%d pixels at %d run past %d bytes", img.Size.X, img.Size.Y, pos, size)
	}
	if imageSize+pixelBytes <= len(buf) {
		img.Pixels = buf[imageSize : imageSize+pixelBytes : imageSize+pixelBytes]
		return img, nil
	}
	img.Pixels = make([]byte, pixelBytes)
	if _, err := r.ReadAt(img.Pixels, start); err != nil {
		return Image{}, fmt.Errorf("xcursor: read %dx%d pixels at %d: %w", img.Size.X, img.Size.Y, pos, err)
	}
	return img, nil
}

func squareBytes(side int) int64 {
	if side <= 0 || side > maxSide {
		return 0
	}
	return int64(side) * int64(side) * 4
}

// readRun reads frames that lie one after the other, each side pixels
// square, in one read. ok is false when they do not, or when a header
// disagrees with the guess, for the caller to read them one by one.
func readRun(r io.ReaderAt, size int64, first uint32, positions []uint32, side int) (frames []Image, ok bool, err error) {
	stride := imageSize + squareBytes(side)
	for i, pos := range positions {
		if int64(pos) != int64(first)+int64(i)*stride {
			return nil, false, nil
		}
	}
	total := stride * int64(len(positions))
	if squareBytes(side) == 0 || int64(first)+total > size {
		return nil, false, nil
	}
	buf := make([]byte, total)
	if _, err := r.ReadAt(buf, int64(first)); err != nil {
		return nil, false, fmt.Errorf("xcursor: read %d frames at %d: %w", len(positions), first, err)
	}
	frames = make([]Image, len(positions))
	for i, pos := range positions {
		at := int64(i) * stride
		img, pixelBytes, err := parseImage(buf[at:at+imageSize], pos)
		if err != nil {
			return nil, false, err
		}
		if int64(pixelBytes) != squareBytes(side) {
			return nil, false, nil
		}
		end := at + stride
		img.Pixels = buf[at+imageSize : end : end]
		frames[i] = img
	}
	return frames, true, nil
}

// SearchPath lists the directories a theme is looked for in: XCURSOR_PATH,
// else the XDG data home's icons, ~/.icons, each XDG system data
// directory's icons and /usr/share/pixmaps.
func SearchPath(env xdg.Env) []string {
	if p := env("XCURSOR_PATH"); p != "" {
		return filepath.SplitList(p)
	}
	system := env.DataDirs()
	dirs := make([]string, 0, len(system)+3)
	if data, ok := env.DataHome(); ok {
		dirs = append(dirs, filepath.Join(data, "icons"))
	}
	if home, ok := env.Home(); ok {
		dirs = append(dirs, filepath.Join(home, ".icons"))
	}
	for _, d := range system {
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
		if info, err := os.Stat(filepath.Join(dir, "cursors")); err == nil && info.IsDir() && !slices.Contains(t.dirs, filepath.Join(dir, "cursors")) { //nolint:gosec // theme files come from the user's XCURSOR_PATH
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
		img, ok, err := readThemed(t, name, func(f fileAt, fileSize int64) (Image, error) {
			return Nearest(f, fileSize, size)
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
	frames, ok, err := readThemed(t, name, func(f fileAt, fileSize int64) ([]Image, error) {
		return NearestFrames(f, fileSize, size)
	})
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("xcursor: theme %q has no %s: %w", t.name, name, fs.ErrNotExist)
	}
	return frames, nil
}

func readThemed[T any](t *Theme, name string, parse func(fileAt, int64) (T, error)) (T, bool, error) {
	var zero T
	for _, dir := range t.dirs {
		v, err := readFile(filepath.Join(dir, name), parse)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return zero, false, fmt.Errorf("xcursor: %s in theme %q: %w", name, t.name, err)
		}
		return v, true, nil
	}
	return zero, false, nil
}

// ReadWidest reads the widest image of the Xcursor file at path.
func ReadWidest(path string) (Image, error) {
	return readFile(path, func(f fileAt, size int64) (Image, error) { return Widest(f, size) })
}

// fileAt reads a file by descriptor. A regular file has nothing for the
// runtime's poller to do, and os.File would ask it anyway, on top of an
// allocation and a finalizer for each file.
type fileAt int

func (f fileAt) ReadAt(p []byte, off int64) (int, error) {
	done := 0
	for done < len(p) {
		n, err := unix.Pread(int(f), p[done:], off+int64(done))
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return done, err
		}
		if n == 0 {
			return done, io.EOF
		}
		done += n
	}
	return done, nil
}

func readFile[T any](path string, parse func(fileAt, int64) (T, error)) (v T, err error) {
	var zero T
	fd, err := openReadOnly(path)
	if err != nil {
		return zero, err
	}
	defer func() { err = errors.Join(err, unix.Close(fd)) }()
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return zero, fmt.Errorf("stat %s: %w", path, err)
	}
	return parse(fileAt(fd), st.Size)
}

func openReadOnly(path string) (int, error) {
	for {
		fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC, 0)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return -1, fmt.Errorf("open %s: %w", path, err)
		}
		return fd, nil
	}
}

func dist(a, b int) int {
	if a > b {
		return a - b
	}
	return b - a
}
