package fontscan

import (
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	ot "github.com/stubbedev/lys-lib/third_party/typesetting/font/opentype"
)

// ErrNoFontDirectories is DefaultFontDirectories finding no font directory
// on the system, as in a minimal container.
var ErrNoFontDirectories = errors.New("no font directories found")

// DefaultFontDirectories return the OS-dependent usual directories for
// fonts, or an error if no one exists.
// These are the directories used by `FontMap.UseSystemFonts` to locate fonts.
func DefaultFontDirectories(logger Logger) ([]string, error) {
	return fontDirectories(logger, func() []string {
		result, err := fcVarsFromEnv().parseFcConfig(logger, time.Now())
		if err != nil {
			logger.Printf("unable to process fontconfig config file: %s", err)
		}
		return result.dirs
	})
}

func fontDirectories(logger Logger, fontconfigDirs func() []string) ([]string, error) {
	var dirs []string
	switch runtime.GOOS {
	case "windows":
		sysRoot := os.Getenv("SYSTEMROOT")
		if sysRoot == "" {
			sysRoot = os.Getenv("SYSTEMDRIVE")
		}
		if sysRoot == "" { // try with the common C:
			sysRoot = "C:"
		}
		dir := filepath.Join(filepath.VolumeName(sysRoot), `\Windows`, "Fonts")
		dirs = []string{
			dir,
			filepath.Join(os.Getenv("windir"), "Fonts"),
			filepath.Join(os.Getenv("localappdata"), "Microsoft", "Windows", "Fonts"),
		}
	case "darwin":
		dirs = []string{
			"/System/Library/Fonts",
			"/Library/Fonts",
			"~/Library/Fonts",
			"/Network/Library/Fonts",
			"/System/Library/Assets/com_apple_MobileAsset_Font3",
			"/System/Library/Assets/com_apple_MobileAsset_Font4",
			"/System/Library/Assets/com_apple_MobileAsset_Font5",
		}
	case "linux", "openbsd", "freebsd":
		dirs = []string{
			"/usr/X11R6/lib/X11/fonts",
			"/usr/local/share/fonts",
			"/usr/share/fonts",
			"/usr/share/texmf/fonts/opentype/public",
			"~/.fonts/",
			filepath.Join(getEnvWithDefault("XDG_DATA_HOME", "~/.local/share"), "fonts"),
		}

		if dataPaths := os.Getenv("XDG_DATA_DIRS"); dataPaths != "" {
			for _, dataPath := range filepath.SplitList(dataPaths) {
				dirs = append(dirs, filepath.Join(dataPath, "fonts"))
			}
		}
		dirs = append(dirs, fontconfigDirs()...)
	case "android":
		dirs = []string{
			"/system/fonts",
			"/system/font",
			"/data/fonts",
		}
	case "ios":
		dirs = []string{
			"/System/Library/Fonts",
			"/System/Library/Fonts/Cache",
		}
	default:
		return nil, fmt.Errorf("unsupported plaform %s", runtime.GOOS)
	}

	var (
		validDirs []string
		seen      = map[string]bool{}
	)
	for _, dir := range dirs {
		dir = expandUser(dir)
		dir, err := filepath.Abs(dir)
		if err != nil {
			continue
		}

		if seen[dir] {
			continue
		}
		seen[dir] = true

		info, err := os.Stat(dir)
		if err != nil { // ignore the non existent directory
			continue
		}

		if !info.IsDir() {
			logger.Printf("font dir is not a directory: %q", dir)
			continue
		}

		validDirs = append(validDirs, dir)
	}
	sort.Strings(validDirs)

	if len(validDirs) == 0 {
		return nil, ErrNoFontDirectories
	}

	return validDirs, nil
}

func expandUser(path string) (expandedPath string) {
	if strings.HasPrefix(path, "~") {
		if u, err := user.Current(); err == nil {
			return strings.Replace(path, "~", u.HomeDir, -1)
		}
	}
	return path
}

// rejects several extensions which are for sure not supported font files
// return `true` is the file should be ignored
func ignoreFontFile(name string) bool {
	// ignore hidden file
	if name == "" || name[0] == '.' {
		return true
	} else if strings.HasSuffix(name, ".enc.gz") || // encodings
		strings.HasSuffix(name, ".afm") || // metrics (ascii)
		strings.HasSuffix(name, ".pfm") || // metrics (binary)
		strings.HasSuffix(name, ".dir") || // summary
		strings.HasSuffix(name, ".scale") ||
		strings.HasSuffix(name, ".alias") ||
		strings.HasSuffix(name, ".pcf") || strings.HasSuffix(name, ".pcf.gz") || // Bitmap
		strings.HasSuffix(name, ".pfb") /* Type1 */ {
		return true
	}

	return false
}

// --------------------- footprint mode -----------------------

// fileStamp identifies a file's state without reading it. The inode and
// size count with the modification time because a Nix store fixes every
// mtime, so a font replaced by an upgrade at the same path changes only those.
type fileStamp int64

const (
	// absentStamp is a path that could not be stat'ed.
	absentStamp fileStamp = 0
	// racyStamp never matches: the file was seen too close to its last
	// change for its mtime to reveal the next one, as a coarse file
	// system clock may give both the same time.
	racyStamp fileStamp = -1

	racyWindow = 2 * time.Second
)

func stampOf(st *unix.Stat_t) fileStamp {
	const prime = 0x100000001b3
	h := uint64(st.Mtim.Nano())
	h = (h ^ uint64(st.Size)) * prime
	h = (h ^ uint64(st.Ino)) * prime
	if stamp := fileStamp(h); stamp != absentStamp && stamp != racyStamp {
		return stamp
	}
	return 1
}

func statStamp(path string) fileStamp {
	var st unix.Stat_t
	if err := unix.Stat(path, &st); err != nil {
		return absentStamp
	}
	return stampOf(&st)
}

// settledStamp is statStamp, or racyStamp for a file changed around now.
func settledStamp(path string, now time.Time) fileStamp {
	var st unix.Stat_t
	if err := unix.Stat(path, &st); err != nil {
		return absentStamp
	}
	age := now.Sub(time.Unix(0, st.Mtim.Nano()))
	if -racyWindow < age && age < racyWindow {
		return racyStamp
	}
	return stampOf(&st)
}

// stampedPath is a path and the stamp it had when it was last read.
type stampedPath struct {
	path  string
	stamp fileStamp
}

// systemFontsIndex stores the footprint comming from the file system
type systemFontsIndex []fileFootprints

func (sfi systemFontsIndex) flatten() fontSet {
	n := 0
	for _, file := range sfi {
		n += len(file.footprints)
	}
	out := make(fontSet, 0, n)
	for _, file := range sfi {
		out = append(out, file.footprints...)
	}
	return out
}

// assertValid makes sur at least one face is valid
func (sfi systemFontsIndex) assertValid() error {
	for _, file := range sfi {
		for _, fp := range file.footprints {
			if fp.checkLoads() == nil {
				return nil
			}
		}
	}

	return errors.New("no valid font")
}

// groups the footprints by origin file
type fileFootprints struct {
	path string // file path

	footprints []Footprint // font content for the path

	stamp fileStamp
}

type footprintScanner struct {
	previousIndex map[string]fileFootprints // reference index, to be updated

	now    time.Time
	dst    systemFontsIndex // accumulated footprints
	dirs   []stampedPath    // what a later start checks instead of walking
	reused int              // files of dst taken from previousIndex

	// used to reduce allocations
	scanBuffer
}

type scanBuffer struct {
	tableBuffer []byte
	cmapBuffer  [][2]rune
}

func newFootprintAccumulator(currentIndex systemFontsIndex, now time.Time) footprintScanner {
	// map font files to their footprints
	out := footprintScanner{now: now, previousIndex: make(map[string]fileFootprints, len(currentIndex))}
	for _, fp := range currentIndex {
		out.previousIndex[fp.path] = fp
	}
	return out
}

func (fa *footprintScanner) consume(path string, stamp fileStamp) error {
	// try to avoid scanning the file
	if indexedFile, has := fa.previousIndex[path]; has && indexedFile.stamp == stamp {
		// we already have an up to date scan of the file:
		// skip the scan and add the current footprints
		fa.dst = append(fa.dst, indexedFile)
		fa.reused++
		return nil
	}

	// do the actual scan

	data, err := mapFontFile(path)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Munmap(data) }()

	ff := fileFootprints{
		path:  path,
		stamp: stamp,
	}

	// fetch the loaders for the given font file, or nil if is not
	// an Opentype font.
	loaders, _ := ot.NewLoaders(ot.NewMapped(data))

	for i, ld := range loaders {
		var fp Footprint
		fp, fa.scanBuffer, err = newFootprintFromLoader(ld, fa.scanBuffer)
		// the font won't be usable, just ignore it
		if err != nil {
			continue
		}

		fp.Location.File = path
		fp.Location.Index = uint16(i)
		// TODO: for now, we do not handle variable fonts

		ff.footprints = append(ff.footprints, fp)
	}
	// it may be a table of the mapping about to go away
	fa.scanBuffer.tableBuffer = nil

	// if the file is not a valid Opentype file,
	// we store an empty list of footprints but still adds the entry to the index
	// so that subsequent calls won't try to open it again
	fa.dst = append(fa.dst, ff)

	return nil
}

// scanFontFootprints walk through the given directories
// and scan each font file to extract its footprint.
// An error is returned if the directory traversal fails, not for invalid font files,
// which are simply ignored.
// `currentIndex` may be passed to avoid scanning font files that are
// already present in `currentIndex` and up to date, and directly duplicating
// the footprint in `currentIndex`
func scanFontFootprints(logger Logger, currentIndex systemFontsIndex, now time.Time, dirs ...string) (scan scanResult, err error) {
	// keep track of visited dirs to avoid double inclusions,
	// for instance with symbolic links
	visited := make(map[string]bool)

	accu := newFootprintAccumulator(currentIndex, now)
	for _, dir := range dirs {
		err := accu.scanDirectory(logger, dir, visited)
		if err != nil {
			return scanResult{}, err
		}
	}
	return scanResult{
		index:     accu.dst,
		dirs:      accu.dirs,
		unchanged: accu.reused == len(currentIndex) && len(accu.dst) == accu.reused,
	}, nil
}

type scanResult struct {
	index     systemFontsIndex
	dirs      []stampedPath
	unchanged bool
}

// isCurrent reports whether a scan of roots would find exactly this index,
// by checking every directory walked and every font file, without opening
// any of them. A file added, removed or renamed changes its directory.
func (s indexSnapshot) isCurrent(roots []string) bool {
	if len(s.fonts) == 0 || !slices.Equal(s.roots, roots) {
		return false
	}
	for _, dir := range s.dirs {
		if statStamp(dir.path) != dir.stamp {
			return false
		}
	}
	for i := range s.fonts {
		if statStamp(s.fonts[i].path) != s.fonts[i].stamp {
			return false
		}
	}
	return true
}
