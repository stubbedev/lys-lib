package fontscan

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"
	"unsafe"

	"github.com/stubbedev/lys-lib/third_party/typesetting/font"
	"github.com/stubbedev/lys-lib/third_party/typesetting/language"
)

func syntheticSnapshot(fonts int) indexSnapshot {
	s := indexSnapshot{
		roots: []string{"/usr/share/fonts", "/home/u/.fonts"},
		dirs:  []stampedPath{{"/usr/share/fonts", 7}, {"/usr/share/fonts/noto", racyStamp}, {"/dead-link", absentStamp}},
		fc: fontconfigResult{
			key:  "env\x00key\x00",
			dirs: []string{"/opt/fonts"},
			deps: []stampedPath{{"/etc/fonts/fonts.conf", 11}, {"/etc/fonts/missing.conf", absentStamp}},
		},
	}
	for i := range fonts {
		var runes RuneSet
		for page := range 3 + i%8 {
			runes = append(runes, runePage{ref: uint16(page * (1 + i%5)), set: pageSet{uint32(i), 2, 3, 4, 5, 6, 7, uint32(page)}})
		}
		path := fmt.Sprintf("/usr/share/fonts/f%d.ttf", i)
		s.fonts = append(s.fonts, fileFootprints{
			path:  path,
			stamp: fileStamp(1000 + i),
			footprints: []Footprint{{
				Location: Location{File: path, Index: 0, Instance: uint16(i % 3)},
				Family:   fmt.Sprintf("family%d", i%40),
				Aliases:  []string{fmt.Sprintf("alias%d", i%40), "shared"},
				Runes:    runes,
				Scripts:  ScriptSet{language.Latin, language.Script(uint32(i))},
				Langs:    LangSet{uint64(i), 2, 3, 4, 5, 6, 7, 8},
				Aspect:   font.Aspect{Style: font.StyleItalic, Weight: font.Weight(100 + i%9*100), Stretch: font.StretchCondensed},
			}, {
				Location: Location{File: path, Index: 1},
				Family:   "other",
			}},
		})
	}
	s.fonts = append(s.fonts, fileFootprints{path: "/usr/share/fonts/not-a-font.ttf", stamp: 5})
	return s
}

func encoded(t testing.TB, s indexSnapshot) []byte {
	t.Helper()
	data, err := s.encode()
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func sameSnapshot(t *testing.T, got, want indexSnapshot) {
	t.Helper()
	if !slices.Equal(got.roots, want.roots) || !slices.Equal(got.dirs, want.dirs) ||
		got.fc.key != want.fc.key || !slices.Equal(got.fc.dirs, want.fc.dirs) || !slices.Equal(got.fc.deps, want.fc.deps) {
		t.Errorf("read back %+v %+v %+v, want %+v %+v %+v", got.roots, got.dirs, got.fc, want.roots, want.dirs, want.fc)
	}
	if len(got.fonts) != len(want.fonts) {
		t.Fatalf("read back %d files, want %d", len(got.fonts), len(want.fonts))
	}
	for i := range want.fonts {
		g, w := got.fonts[i], want.fonts[i]
		if g.path != w.path || g.stamp != w.stamp || len(g.footprints) != len(w.footprints) {
			t.Fatalf("file %d: read back %+v, want %+v", i, g, w)
		}
		for j := range w.footprints {
			gf, wf := g.footprints[j], w.footprints[j]
			if gf.Location != wf.Location || gf.Family != wf.Family || !slices.Equal(gf.Aliases, wf.Aliases) || gf.Langs != wf.Langs || gf.Aspect != wf.Aspect ||
				!slices.Equal(gf.Runes, wf.Runes) || !slices.Equal(gf.Scripts, wf.Scripts) {
				t.Errorf("file %d face %d: read back %+v, want %+v", i, j, gf, wf)
			}
		}
	}
}

func TestTheIndexReadsBackWhatWasWritten(t *testing.T) {
	want := syntheticSnapshot(50)
	path := filepath.Join(t.TempDir(), "index.cache")
	if err := want.writeIndexFile(path); err != nil {
		t.Fatal(err)
	}
	got, err := readIndexFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sameSnapshot(t, got, want)
}

func TestAnEmptySnapshotReadsBack(t *testing.T) {
	got, err := parseIndex(encoded(t, indexSnapshot{}))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, indexSnapshot{}) {
		t.Errorf("read back %+v", got)
	}
}

func TestEqualSnapshotsEncodeToEqualBytes(t *testing.T) {
	if !bytes.Equal(encoded(t, syntheticSnapshot(20)), encoded(t, syntheticSnapshot(20))) {
		t.Error("two encodings of one snapshot differ")
	}
}

func TestIdenticalCoverageIsStoredOnce(t *testing.T) {
	face := func(path string) fileFootprints {
		return fileFootprints{path: path, footprints: []Footprint{{
			Location: Location{File: path},
			Runes:    RuneSet{{ref: 1, set: pageSet{1}}, {ref: 2, set: pageSet{2}}},
			Scripts:  ScriptSet{language.Latin},
		}}}
	}
	one := encoded(t, indexSnapshot{fonts: systemFontsIndex{face("/a"), {path: "/b"}}})
	two := encoded(t, indexSnapshot{fonts: systemFontsIndex{face("/a"), face("/b")}})
	if grew, record := len(two)-len(one), int(unsafe.Sizeof(indexFootprint{})); grew != record {
		t.Errorf("a second face with the same coverage grew the index by %d bytes, want its %d byte record", grew, record)
	}
}

func TestARuneSetFromTheIndexCanBeEdited(t *testing.T) {
	want := syntheticSnapshot(2)
	path := filepath.Join(t.TempDir(), "index.cache")
	if err := want.writeIndexFile(path); err != nil {
		t.Fatal(err)
	}
	got, err := readIndexFile(path)
	if err != nil {
		t.Fatal(err)
	}
	runes := got.fonts[1].footprints[0].Runes
	runes.Delete(rune(runes[0].ref)<<8 | 0)
	runes.Add(0x10FF00)
	onDisk, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(onDisk, encoded(t, want)) {
		t.Error("editing a mapped rune set reached the file")
	}
}

func TestADamagedIndexIsAnErrorNotAPanic(t *testing.T) {
	good := encoded(t, syntheticSnapshot(30))
	for n := 0; n < len(good); n += 1 + len(good)/257 {
		if _, err := parseIndex(slices.Clone(good[:n])); err == nil {
			t.Errorf("an index truncated to %d of %d bytes parsed", n, len(good))
		}
	}
	for i := 0; i < len(good); i += 1 + len(good)/257 {
		bad := slices.Clone(good)
		bad[i] ^= 0x40
		if _, err := parseIndex(bad); err == nil {
			t.Errorf("an index with byte %d flipped parsed", i)
		}
	}
	for name, bad := range map[string][]byte{
		"empty":   nil,
		"garbage": bytes.Repeat([]byte{0xAB}, 4096),
		"gzip":    append([]byte{0x1f, 0x8b, 8, 0}, make([]byte, 400)...),
	} {
		if _, err := parseIndex(bad); err == nil {
			t.Errorf("%s parsed as an index", name)
		}
	}
}

func resealed(data []byte) []byte {
	size := unsafe.Sizeof(indexHeader{})
	binary.NativeEndian.PutUint32(data[unsafe.Offsetof(indexHeader{}.checksum):], crc32.ChecksumIEEE(data[size:]))
	return data
}

func TestAnIndexWithAValidChecksumIsStillBoundsChecked(t *testing.T) {
	good := encoded(t, syntheticSnapshot(30))
	header := (*indexHeader)(unsafe.Pointer(&good[0]))
	fileTable := int(header.files.offset)
	footprintTable := int(header.footprints.offset)
	sections := []uintptr{
		unsafe.Offsetof(header.files), unsafe.Offsetof(header.footprints), unsafe.Offsetof(header.pages),
		unsafe.Offsetof(header.scripts), unsafe.Offsetof(header.strings), unsafe.Offsetof(header.roots),
		unsafe.Offsetof(header.dirs), unsafe.Offsetof(header.fcDirs), unsafe.Offsetof(header.fcDeps), unsafe.Offsetof(header.aliases),
	}
	mutations := map[string]func([]byte){
		"unaligned table": func(b []byte) { binary.NativeEndian.PutUint32(b[sections[0]:], uint32(fileTable+1)) },
		"file path": func(b []byte) {
			binary.NativeEndian.PutUint32(b[fileTable+int(unsafe.Offsetof(indexFile{}.path)):], 1<<31)
		},
		"file footprints": func(b []byte) {
			binary.NativeEndian.PutUint32(b[fileTable+int(unsafe.Offsetof(indexFile{}.footprintsCount)):], 1<<31)
		},
		"family": func(b []byte) {
			binary.NativeEndian.PutUint32(b[footprintTable+int(unsafe.Offsetof(indexFootprint{}.family))+4:], 1<<31)
		},
		"pages": func(b []byte) {
			binary.NativeEndian.PutUint32(b[footprintTable+int(unsafe.Offsetof(indexFootprint{}.pagesCount)):], 1<<31)
		},
		"scripts": func(b []byte) {
			binary.NativeEndian.PutUint32(b[footprintTable+int(unsafe.Offsetof(indexFootprint{}.firstScript)):], 1<<31)
		},
		"aliases": func(b []byte) {
			binary.NativeEndian.PutUint32(b[footprintTable+int(unsafe.Offsetof(indexFootprint{}.firstAlias)):], 1<<31)
		},
		"fontconfig key": func(b []byte) {
			binary.NativeEndian.PutUint32(b[unsafe.Offsetof(header.fcKey)+4:], 1<<31)
		},
	}
	for i, offset := range sections {
		mutations[fmt.Sprintf("section %d count", i)] = func(b []byte) {
			binary.NativeEndian.PutUint32(b[offset+4:], 1<<31)
		}
		mutations[fmt.Sprintf("section %d offset", i)] = func(b []byte) {
			binary.NativeEndian.PutUint32(b[offset:], 1<<31)
		}
	}
	if _, err := parseIndex(resealed(slices.Clone(good))); err != nil {
		t.Fatalf("resealing alone broke the index: %v", err)
	}
	for name, mutate := range mutations {
		bad := slices.Clone(good)
		mutate(bad)
		if _, err := parseIndex(resealed(bad)); err == nil {
			t.Errorf("%s: an out of range index parsed", name)
		}
	}
}

func TestAnIndexOfAnotherByteOrderOrVersionIsRefused(t *testing.T) {
	for name, offset := range map[string]uintptr{
		"byte order": unsafe.Offsetof(indexHeader{}.byteOrder),
		"version":    unsafe.Offsetof(indexHeader{}.version),
	} {
		bad := encoded(t, syntheticSnapshot(3))
		binary.NativeEndian.PutUint32(bad[offset:], 0x04030201)
		if _, err := parseIndex(resealed(bad)); err == nil {
			t.Errorf("an index of another %s parsed", name)
		}
	}
}

func TestLoadingAnIndexAllocatesNoMoreForMoreFonts(t *testing.T) {
	allocs := func(fonts int) float64 {
		data := encoded(t, syntheticSnapshot(fonts))
		return testing.AllocsPerRun(5, func() {
			if _, err := parseIndex(data); err != nil {
				t.Fatal(err)
			}
		})
	}
	small, large := allocs(10), allocs(2000)
	if large != small || large > 8 {
		t.Errorf("parsing an index allocates %v times for 10 fonts and %v for 2000, want the same and at most 8", small, large)
	}
}

type recordingLogger struct {
	lines []string
}

func (l *recordingLogger) Printf(format string, args ...any) {
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
}

type fontTree struct {
	t         *testing.T
	fonts     string
	conf      string
	index     string
	now       time.Time
	cantarell []byte
}

func newFontTree(t *testing.T) *fontTree {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "render", "testdata", "Cantarell-Regular.ttf"))
	if err != nil {
		t.Fatal(err)
	}
	tree := &fontTree{
		t:         t,
		fonts:     filepath.Join(root, "fonts"),
		conf:      filepath.Join(root, "fonts.conf"),
		index:     filepath.Join(root, "cache", "index.cache"),
		now:       time.Now().Add(time.Hour),
		cantarell: data,
	}
	if err := os.Mkdir(tree.fonts, 0o700); err != nil {
		t.Fatal(err)
	}
	tree.writeFont("a.ttf")
	tree.writeConf(tree.fonts)
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_DATA_DIRS", "")
	t.Setenv("FONTCONFIG_FILE", tree.conf)
	systemDirs := fhsFontDirectories
	fhsFontDirectories = nil
	t.Cleanup(func() { fhsFontDirectories = systemDirs })
	return tree
}

func (ft *fontTree) writeFont(name string) {
	ft.t.Helper()
	if err := os.WriteFile(filepath.Join(ft.fonts, name), ft.cantarell, 0o600); err != nil {
		ft.t.Fatal(err)
	}
}

func (ft *fontTree) writeConf(dirs ...string) {
	ft.t.Helper()
	var conf bytes.Buffer
	conf.WriteString("<?xml version=\"1.0\"?>\n<fontconfig>\n")
	for _, dir := range dirs {
		fmt.Fprintf(&conf, "<dir>%s</dir>\n", dir)
	}
	conf.WriteString("</fontconfig>\n")
	if err := os.WriteFile(ft.conf, conf.Bytes(), 0o600); err != nil {
		ft.t.Fatal(err)
	}
}

func (ft *fontTree) refresh() (systemFontsIndex, *recordingLogger) {
	ft.t.Helper()
	logger := &recordingLogger{}
	index, err := refreshSystemFontsIndex(context.Background(), logger, ft.index, ft.now)
	if err != nil {
		ft.t.Fatal(err)
	}
	return index, logger
}

func (ft *fontTree) paths(index systemFontsIndex) []string {
	var out []string
	for _, file := range index {
		out = append(out, filepath.Base(file.path))
	}
	return out
}

func (ft *fontTree) indexBytes() []byte {
	ft.t.Helper()
	data, err := os.ReadFile(ft.index)
	if err != nil {
		ft.t.Fatal(err)
	}
	return data
}

func walked(logger *recordingLogger) bool {
	for _, line := range logger.lines {
		if bytes.Contains([]byte(line), []byte("using system font dirs")) {
			return true
		}
	}
	return false
}

func TestAnUnchangedTreeIsNeitherWalkedNorRewritten(t *testing.T) {
	ft := newFontTree(t)
	first, logger := ft.refresh()
	if !walked(logger) || !slices.Equal(ft.paths(first), []string{"a.ttf"}) {
		t.Fatalf("first refresh: walked %v, files %v", walked(logger), ft.paths(first))
	}
	before, err := os.Stat(ft.index)
	if err != nil {
		t.Fatal(err)
	}
	second, logger := ft.refresh()
	if walked(logger) {
		t.Errorf("an unchanged tree was walked: %v", logger.lines)
	}
	if !slices.Equal(ft.paths(second), []string{"a.ttf"}) || len(second[0].footprints) != 1 {
		t.Errorf("second refresh: %+v", second)
	}
	after, err := os.Stat(ft.index)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Error("an unchanged tree rewrote the index")
	}
}

func TestADamagedIndexFileIsRescannedAndReplaced(t *testing.T) {
	for name, damage := range map[string]func([]byte) []byte{
		"truncated": func(b []byte) []byte { return b[:len(b)/2] },
		"flipped":   func(b []byte) []byte { b[len(b)-1] ^= 1; return b },
		"empty":     func([]byte) []byte { return nil },
		"gzip":      func([]byte) []byte { return []byte{0x1f, 0x8b, 8, 0, 0, 0, 0, 0} },
	} {
		t.Run(name, func(t *testing.T) {
			ft := newFontTree(t)
			ft.refresh()
			good := ft.indexBytes()
			if err := os.WriteFile(ft.index, damage(slices.Clone(good)), 0o600); err != nil {
				t.Fatal(err)
			}
			index, logger := ft.refresh()
			if !walked(logger) || !slices.Equal(ft.paths(index), []string{"a.ttf"}) || len(index[0].footprints) != 1 {
				t.Fatalf("after damage: walked %v, index %+v", walked(logger), index)
			}
			if !bytes.Equal(ft.indexBytes(), good) {
				t.Error("the damaged index was not replaced by a good one")
			}
		})
	}
}

func TestChangesInTheTreeAreFound(t *testing.T) {
	ft := newFontTree(t)
	ft.refresh()

	settle := func() {
		t.Helper()
		ft.refresh()
	}

	ft.writeFont("b.ttf")
	bumpMtime(t, ft.fonts, 1)
	ft.now = ft.now.Add(time.Minute)
	index, _ := ft.refresh()
	if got := ft.paths(index); !slices.Equal(got, []string{"a.ttf", "b.ttf"}) {
		t.Errorf("after adding a font: %v", got)
	}
	settle()

	bumpMtime(t, ft.fonts, 2)
	if err := os.Remove(filepath.Join(ft.fonts, "b.ttf")); err != nil {
		t.Fatal(err)
	}
	index, _ = ft.refresh()
	if got := ft.paths(index); !slices.Equal(got, []string{"a.ttf"}) {
		t.Errorf("after removing a font: %v", got)
	}
	settle()

	sub := filepath.Join(ft.fonts, "sub")
	bumpMtime(t, ft.fonts, 3)
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "c.ttf"), ft.cantarell, 0o600); err != nil {
		t.Fatal(err)
	}
	index, _ = ft.refresh()
	if got := ft.paths(index); !slices.Equal(got, []string{"a.ttf", "c.ttf"}) {
		t.Errorf("after adding a directory: %v", got)
	}
	settle()

	bumpMtime(t, sub, 4)
	if err := os.WriteFile(filepath.Join(sub, "d.ttf"), ft.cantarell, 0o600); err != nil {
		t.Fatal(err)
	}
	index, _ = ft.refresh()
	if got := ft.paths(index); !slices.Equal(got, []string{"a.ttf", "c.ttf", "d.ttf"}) {
		t.Errorf("after adding a font to a subdirectory: %v", got)
	}
}

func TestAFontReplacedWithTheSameMtimeIsRescanned(t *testing.T) {
	ft := newFontTree(t)
	path := filepath.Join(ft.fonts, "a.ttf")
	store := time.Unix(1, 0)
	if err := os.Chtimes(path, store, store); err != nil {
		t.Fatal(err)
	}
	index, _ := ft.refresh()
	if len(index[0].footprints) != 1 {
		t.Fatalf("first refresh: %+v", index)
	}
	replacement := path + ".new"
	if err := os.WriteFile(replacement, []byte("not a font"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(replacement, store, store); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, path); err != nil {
		t.Fatal(err)
	}
	ft.writeFont("keep.ttf")
	index, _ = ft.refresh()
	for _, file := range index {
		if filepath.Base(file.path) == "a.ttf" && len(file.footprints) != 0 {
			t.Errorf("a.ttf kept its old footprints after being replaced at one mtime")
		}
	}
}

func TestAConfigurationChangeIsFound(t *testing.T) {
	ft := newFontTree(t)
	ft.refresh()
	extra := filepath.Join(filepath.Dir(ft.fonts), "extra")
	if err := os.Mkdir(extra, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(extra, "e.ttf"), ft.cantarell, 0o600); err != nil {
		t.Fatal(err)
	}
	ft.writeConf(ft.fonts, extra)
	index, logger := ft.refresh()
	if !walked(logger) {
		t.Error("a changed fontconfig file was not parsed again")
	}
	if got := ft.paths(index); !slices.Contains(got, "e.ttf") {
		t.Errorf("the directory the configuration added was not scanned: %v", got)
	}
	if _, logger = ft.refresh(); walked(logger) {
		t.Errorf("the new configuration was not cached: %v", logger.lines)
	}
}

func TestAnIncludeThatAppearsIsFound(t *testing.T) {
	ft := newFontTree(t)
	extra := filepath.Join(filepath.Dir(ft.fonts), "extra")
	if err := os.Mkdir(extra, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(extra, "e.ttf"), ft.cantarell, 0o600); err != nil {
		t.Fatal(err)
	}
	include := filepath.Join(filepath.Dir(ft.fonts), "later.conf")
	conf := fmt.Sprintf("<fontconfig><dir>%s</dir><include ignore_missing=\"yes\">%s</include></fontconfig>", ft.fonts, include)
	if err := os.WriteFile(ft.conf, []byte(conf), 0o600); err != nil {
		t.Fatal(err)
	}
	ft.refresh()
	if err := os.WriteFile(include, fmt.Appendf(nil, "<fontconfig><dir>%s</dir></fontconfig>", extra), 0o600); err != nil {
		t.Fatal(err)
	}
	index, _ := ft.refresh()
	if got := ft.paths(index); !slices.Contains(got, "e.ttf") {
		t.Errorf("the include that appeared was not read: %v", got)
	}
}

func TestAChangedEnvironmentIsNotServedTheCachedConfiguration(t *testing.T) {
	ft := newFontTree(t)
	ft.refresh()
	other := filepath.Join(filepath.Dir(ft.conf), "other.conf")
	extra := filepath.Join(filepath.Dir(ft.fonts), "extra")
	if err := os.Mkdir(extra, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(extra, "e.ttf"), ft.cantarell, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(other, fmt.Appendf(nil, "<fontconfig><dir>%s</dir></fontconfig>", extra), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FONTCONFIG_FILE", other)
	index, _ := ft.refresh()
	if got := ft.paths(index); !slices.Equal(got, []string{"e.ttf"}) {
		t.Errorf("under another FONTCONFIG_FILE: %v", got)
	}
}

func TestADirectoryChangedAroundTheScanIsWalkedAgain(t *testing.T) {
	ft := newFontTree(t)
	ft.now = time.Now()
	ft.refresh()
	ft.now = time.Now().Add(time.Hour)
	if _, logger := ft.refresh(); !walked(logger) {
		t.Error("a directory stamped within the racy window was trusted")
	}
	if _, logger := ft.refresh(); walked(logger) {
		t.Errorf("a settled directory was walked again: %v", logger.lines)
	}
}

func TestAnUnwritableCacheStillGivesFonts(t *testing.T) {
	ft := newFontTree(t)
	blocker := filepath.Join(filepath.Dir(ft.index), "file")
	if err := os.MkdirAll(filepath.Dir(blocker), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	ft.index = filepath.Join(blocker, "index.cache")
	index, logger := ft.refresh()
	if !slices.Equal(ft.paths(index), []string{"a.ttf"}) {
		t.Errorf("index %v", index)
	}
	if !slices.ContainsFunc(logger.lines, func(line string) bool { return bytes.Contains([]byte(line), []byte("updating font index")) }) {
		t.Errorf("the failed write went unreported: %v", logger.lines)
	}
}

func TestNoCacheDirectoryStillGivesFonts(t *testing.T) {
	ft := newFontTree(t)
	index, err := refreshSystemFontsIndex(context.Background(), discardLogger{}, "", ft.now)
	if err != nil || !slices.Equal(ft.paths(index), []string{"a.ttf"}) {
		t.Errorf("index %v, %v", index, err)
	}
}

func TestWritingTheIndexRemovesTheOldFormatAndLeavesNoTemporaries(t *testing.T) {
	ft := newFontTree(t)
	dir := filepath.Dir(ft.index)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	ft.index = filepath.Join(dir, fmt.Sprintf(cacheFilePattern, cacheFormatVersion))
	old := filepath.Join(dir, fmt.Sprintf(cacheFilePattern, cacheFormatVersion-1))
	if err := os.WriteFile(old, []byte{0x1f, 0x8b}, 0o600); err != nil {
		t.Fatal(err)
	}
	ft.refresh()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != filepath.Base(ft.index) {
		t.Errorf("cache directory holds %v, want only the index", entries)
	}
}

func TestStampsChangeWithSizeAndInode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f")
	if err := os.WriteFile(path, []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	fixed := time.Unix(1, 0)
	if err := os.Chtimes(path, fixed, fixed); err != nil {
		t.Fatal(err)
	}
	first := statStamp(path)
	if first == absentStamp || first == racyStamp {
		t.Fatalf("stamp %d", first)
	}
	if statStamp(path) != first {
		t.Error("the stamp of an unchanged file changed")
	}
	if err := os.WriteFile(path, []byte("longer"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, fixed, fixed); err != nil {
		t.Fatal(err)
	}
	if statStamp(path) == first {
		t.Error("a new size at the same mtime kept the stamp")
	}
	if statStamp(filepath.Join(dir, "missing")) != absentStamp {
		t.Error("a missing file has a stamp")
	}
	if settledStamp(path, fixed) != racyStamp {
		t.Error("a file changed just now is settled")
	}
	if settledStamp(path, fixed.Add(time.Hour)) != statStamp(path) {
		t.Error("a settled file has no plain stamp")
	}
}

func BenchmarkIndexParse1000(b *testing.B) {
	data := encoded(b, syntheticSnapshot(1000))
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	for b.Loop() {
		if _, err := parseIndex(data); err != nil {
			b.Fatal(err)
		}
	}
}

func bumpMtime(t *testing.T, dir string, generation int) {
	t.Helper()
	at := time.Unix(1_000_000+int64(generation), 0)
	if err := os.Chtimes(dir, at, at); err != nil {
		t.Fatal(err)
	}
}

func TestNoCacheDirectoryMeansNoIndexPath(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", "")
	t.Setenv("HOME", "")
	if got := indexPath(discardLogger{}, ""); got != "" {
		t.Errorf("index path %q without a cache directory", got)
	}
	if got := indexPath(discardLogger{}, "/given"); got != filepath.Join("/given", fmt.Sprintf(cacheFilePattern, cacheFormatVersion)) {
		t.Errorf("index path %q under a given directory", got)
	}
}
