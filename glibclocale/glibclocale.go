// Package glibclocale reads glibc's compiled locale data in pure Go: the
// locale-archive and the per-locale directories localedef writes, laid
// out as glibc's locarchive.h and loadlocale.c read them. It lists the
// locales a system has, as locale -a does, and reads a locale's
// LC_NUMERIC, LC_MONETARY and LC_TIME, the last with a strftime over its
// names and formats.
//
// A locale is found as glibc finds it: with LOCPATH set, only in its
// directories; otherwise in the archive and then in /usr/lib/locale. A
// name's codeset is normalized first, so en_US.UTF-8 finds en_US.utf8.
// Unlike glibc, a name that is not there is an error naming every place
// searched, never the C locale or a shorter name such as en_US or en.
// glibc's locale.alias names are not resolved.
//
// musl ships no compiled locale data: there [Source.List] finds only C and
// POSIX, and loading any other locale is that error.
package glibclocale

import (
	"cmp"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	"github.com/stubbedev/lys-lib/assetfile"
	"github.com/stubbedev/lys-lib/xdg"
)

const (
	defaultArchive = "/usr/lib/locale/locale-archive"
	defaultDir     = "/usr/lib/locale"
	archiveLimit   = math.MaxUint32
	categoryLimit  = 1 << 20
)

var errMissingCategory = errors.New("no such category")

// Source is where locale data is read from: a locale-archive, and
// directories holding a directory per locale.
type Source struct {
	archive string
	dirs    []string
}

// NewSource reads the locale-archive at archive, "" for none, and then
// the locale directories under dirs, in order.
func NewSource(archive string, dirs ...string) Source {
	return Source{archive: archive, dirs: slices.Clone(dirs)}
}

// System is where glibc reads locale data in env: the directories in
// LOCPATH when it is set; otherwise the archive at $LOCALE_ARCHIVE_2_27
// or $LOCALE_ARCHIVE, the variables NixOS's glibc reads, or
// /usr/lib/locale/locale-archive, and then /usr/lib/locale.
func System(env xdg.Env) Source {
	if path := env("LOCPATH"); path != "" {
		return Source{dirs: slices.DeleteFunc(strings.Split(path, ":"), func(dir string) bool { return dir == "" })}
	}
	return Source{archive: cmp.Or(env("LOCALE_ARCHIVE_2_27"), env("LOCALE_ARCHIVE"), defaultArchive), dirs: []string{defaultDir}}
}

// List is the locales the source has, sorted, as locale -a names them:
// C and POSIX, the archive's names and every directory that holds an
// LC_IDENTIFICATION. A missing archive or directory adds nothing; one
// that cannot be read is an error.
func (s Source) List() ([]string, error) {
	names := []string{"C", "POSIX"}
	if s.archive != "" {
		err := withArchive(s.archive, func(a archive) error {
			stored, err := a.localeNames()
			names = append(names, stored...)
			return err
		})
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
	}
	for _, dir := range s.dirs {
		entries, err := os.ReadDir(dir)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("glibclocale: list the locales in %s: %w", dir, err)
		}
		for _, e := range entries {
			info, err := os.Stat(filepath.Join(dir, e.Name(), lcIdentification.String()))
			switch {
			case errors.Is(err, fs.ErrNotExist), errors.Is(err, syscall.ENOTDIR):
			case err != nil:
				return nil, fmt.Errorf("glibclocale: list the locales in %s: %w", dir, err)
			case info.Mode().IsRegular():
				names = append(names, e.Name())
			}
		}
	}
	slices.Sort(names)
	return slices.Compact(names), nil
}

// Numeric reads the LC_NUMERIC of the locale name.
func (s Source) Numeric(name string) (Numeric, error) {
	return load(s, name, lcNumeric, parseNumeric, cNumeric)
}

// Monetary reads the LC_MONETARY of the locale name.
func (s Source) Monetary(name string) (Monetary, error) {
	return load(s, name, lcMonetary, parseMonetary, cMonetary)
}

// Time reads the LC_TIME of the locale name.
func (s Source) Time(name string) (*Time, error) {
	t, err := load(s, name, lcTime, parseTime, cTime)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func load[T any](s Source, name string, c category, parse func([]byte) (T, error), builtin T) (T, error) {
	var zero T
	if name == "C" || name == "POSIX" {
		return builtin, nil
	}
	if err := checkName(name); err != nil {
		return zero, err
	}
	normalized := Normalize(name)
	searched := make([]string, 0, 1+len(s.dirs))
	if s.archive != "" {
		var v T
		found := false
		err := withArchive(s.archive, func(a archive) error {
			record, ok, err := a.find(normalized)
			if err != nil || !ok {
				return err
			}
			data, err := a.category(record, c)
			if errors.Is(err, errMissingCategory) {
				return fmt.Errorf("glibclocale: locale %q in %s has no %s", name, s.archive, c)
			}
			if err != nil {
				return fmt.Errorf("glibclocale: %s of %q in %s: %w", c, name, s.archive, err)
			}
			if v, err = parse(data); err != nil {
				return fmt.Errorf("glibclocale: %s of %q in %s: %w", c, name, s.archive, err)
			}
			found = true
			return nil
		})
		switch {
		case errors.Is(err, fs.ErrNotExist):
			searched = append(searched, s.archive+" (absent)")
		case err != nil:
			return zero, err
		case found:
			return v, nil
		default:
			searched = append(searched, s.archive)
		}
	}
	for _, dir := range s.dirs {
		for _, n := range slices.Compact([]string{normalized, name}) {
			path := filepath.Join(dir, n, c.String())
			data, err := assetfile.Read(path, categoryLimit)
			if errors.Is(err, fs.ErrNotExist) {
				if _, statErr := os.Stat(filepath.Join(dir, n)); statErr == nil {
					return zero, fmt.Errorf("glibclocale: locale %q in %s has no %s", name, filepath.Join(dir, n), c)
				}
				continue
			}
			if err != nil {
				return zero, fmt.Errorf("glibclocale: read %s of %q: %w", c, name, err)
			}
			v, err := parse(data)
			if err != nil {
				return zero, fmt.Errorf("glibclocale: %s: %w", path, err)
			}
			return v, nil
		}
		searched = append(searched, dir)
	}
	if len(searched) == 0 {
		return zero, fmt.Errorf("glibclocale: no locale %q: the source has no archive or directories", name)
	}
	return zero, fmt.Errorf("glibclocale: no locale %q in %s", name, strings.Join(searched, ", "))
}

func withArchive(path string, use func(archive) error) error {
	f, size, err := assetfile.Open(path, archiveLimit)
	if err != nil {
		return fmt.Errorf("glibclocale: open the locale archive: %w", err)
	}
	defer f.Close()
	if size == 0 {
		return fmt.Errorf("glibclocale: the locale archive %s is empty", path)
	}
	data, err := syscall.Mmap(int(f.Fd()), 0, int(size), syscall.PROT_READ, syscall.MAP_PRIVATE)
	if err != nil {
		return fmt.Errorf("glibclocale: map the locale archive %s: %w", path, err)
	}
	defer func() { _ = syscall.Munmap(data) }()
	a, err := parseArchive(data)
	if err != nil {
		return fmt.Errorf("glibclocale: %s: %w", path, err)
	}
	return use(a)
}

func checkName(name string) error {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\x00") {
		return fmt.Errorf("glibclocale: %q is not a locale name", name)
	}
	return nil
}

// Normalize is name with its codeset spelled as glibc looks it up and
// locale -a lists it: lowercase letters and digits only, so en_US.UTF-8
// is en_US.utf8 and de_DE.ISO-8859-15 is de_DE.iso885915.
func Normalize(name string) string {
	dot := strings.IndexByte(name, '.')
	if dot < 0 {
		return name
	}
	rest := name[dot+1:]
	end := strings.IndexByte(rest, '@')
	if end < 0 {
		end = len(rest)
	}
	if end == 0 {
		return name
	}
	codeset := rest[:end]
	digitsOnly := true
	for i := range len(codeset) {
		if isAlpha(codeset[i]) {
			digitsOnly = false
		}
	}
	var b strings.Builder
	b.Grow(len(name) + 3)
	b.WriteString(name[:dot+1])
	if digitsOnly {
		b.WriteString("iso")
	}
	for i := range len(codeset) {
		switch c := codeset[i]; {
		case isAlpha(c):
			b.WriteByte(c | 0x20)
		case '0' <= c && c <= '9':
			b.WriteByte(c)
		}
	}
	b.WriteString(rest[end:])
	return b.String()
}

func isAlpha(c byte) bool { return 'a' <= c|0x20 && c|0x20 <= 'z' }
