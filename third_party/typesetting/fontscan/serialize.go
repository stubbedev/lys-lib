package fontscan

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"math"
	"os"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-lib/third_party/typesetting/font"
	"github.com/stubbedev/lys-lib/third_party/typesetting/language"
)

// The index is a flat file read in place through a private mapping: the
// tables below are native-endian arrays of fixed-size records, so loading
// it checks it and points slices and strings into it, decoding nothing.
// A mapping that outlives every Footprint is the price: an index stays
// mapped for the life of the process, and is only ever replaced by rename,
// never rewritten.

const (
	cacheFormatVersion = 7
	indexMagic         = "lysfscan"
	indexByteOrder     = 0x01020304
	indexTableAlign    = 8
)

type indexSection struct {
	offset uint32
	count  uint32
}

type indexHeader struct {
	magic      [8]byte
	byteOrder  uint32
	version    uint32
	size       uint64
	checksum   uint32
	reserved   uint32
	files      indexSection
	footprints indexSection
	pages      indexSection
	scripts    indexSection
	strings    indexSection
	roots      indexSection
	dirs       indexSection
	fcDirs     indexSection
	fcDeps     indexSection
	fcKey      indexStringRef
}

type indexStringRef struct {
	offset uint32
	length uint32
}

type indexFile struct {
	path            indexStringRef
	stamp           fileStamp
	firstFootprint  uint32
	footprintsCount uint32
}

type indexFootprint struct {
	langs        LangSet
	family       indexStringRef
	firstPage    uint32
	pagesCount   uint32
	firstScript  uint32
	scriptsCount uint32
	index        uint16
	instance     uint16
	style        uint32
	weight       float32
	stretch      float32
	reserved     [2]uint32
}

type indexStamped struct {
	path  indexStringRef
	stamp fileStamp
}

var (
	_ [36 - unsafe.Sizeof(runePage{})]byte
	_ [unsafe.Sizeof(runePage{}) - 36]byte
	_ [112 - unsafe.Sizeof(indexFootprint{})]byte
	_ [unsafe.Sizeof(indexFootprint{}) - 112]byte
	_ [24 - unsafe.Sizeof(indexFile{})]byte
	_ [unsafe.Sizeof(indexFile{}) - 24]byte
	_ [16 - unsafe.Sizeof(indexStamped{})]byte
	_ [unsafe.Sizeof(indexStamped{}) - 16]byte
	_ [4 - unsafe.Sizeof(language.Script(0))]byte
	_ [unsafe.Sizeof(language.Script(0)) - 4]byte
)

// indexSnapshot is what the index file holds: the footprints, and what
// decides whether they are still current without walking the font tree.
type indexSnapshot struct {
	fonts systemFontsIndex
	// roots are the directories the scan started from.
	roots []string
	// dirs are the directories it walked: adding, removing or renaming
	// a font changes one of their stamps.
	dirs []stampedPath
	fc   fontconfigResult
}

func sectionTable[T any](data []byte, s indexSection) ([]T, error) {
	var zero T
	end := uint64(s.offset) + uint64(s.count)*uint64(unsafe.Sizeof(zero))
	if s.offset%indexTableAlign != 0 || end > uint64(len(data)) {
		return nil, fmt.Errorf("table at %d of %d records runs past the %d byte file", s.offset, s.count, len(data))
	}
	if s.count == 0 {
		return nil, nil
	}
	return unsafe.Slice((*T)(unsafe.Pointer(&data[s.offset])), s.count), nil
}

func refString(strs []byte, ref indexStringRef) (string, error) {
	if uint64(ref.offset)+uint64(ref.length) > uint64(len(strs)) {
		return "", fmt.Errorf("string at %d of %d bytes runs past the %d byte pool", ref.offset, ref.length, len(strs))
	}
	if ref.length == 0 {
		return "", nil
	}
	return unsafe.String(&strs[ref.offset], ref.length), nil
}

func sectionStrings(data, strs []byte, s indexSection) ([]string, error) {
	refs, err := sectionTable[indexStringRef](data, s)
	if err != nil {
		return nil, err
	}
	if len(refs) == 0 {
		return nil, nil
	}
	out := make([]string, len(refs))
	for i, ref := range refs {
		if out[i], err = refString(strs, ref); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func sectionStamped(data, strs []byte, s indexSection) ([]stampedPath, error) {
	records, err := sectionTable[indexStamped](data, s)
	if err != nil {
		return nil, err
	}
	if len(records) == 0 {
		return nil, nil
	}
	out := make([]stampedPath, len(records))
	for i, record := range records {
		path, err := refString(strs, record.path)
		if err != nil {
			return nil, err
		}
		out[i] = stampedPath{path: path, stamp: record.stamp}
	}
	return out, nil
}

// parseIndex checks data and builds the snapshot over it, which keeps
// pointing into data. Every offset is checked, so a damaged file is an
// error here, never a bad read later.
func parseIndex(data []byte) (indexSnapshot, error) {
	headerSize := unsafe.Sizeof(indexHeader{})
	if uintptr(len(data)) < headerSize {
		return indexSnapshot{}, fmt.Errorf("%d bytes is too short for an index", len(data))
	}
	header := (*indexHeader)(unsafe.Pointer(&data[0]))
	if string(header.magic[:]) != indexMagic {
		return indexSnapshot{}, errors.New("not a font index")
	}
	if header.byteOrder != indexByteOrder {
		return indexSnapshot{}, errors.New("index written on another byte order")
	}
	if header.version != cacheFormatVersion {
		return indexSnapshot{}, fmt.Errorf("index format %d, want %d", header.version, cacheFormatVersion)
	}
	if header.size != uint64(len(data)) {
		return indexSnapshot{}, fmt.Errorf("index of %d bytes declares %d", len(data), header.size)
	}
	if sum := crc32.ChecksumIEEE(data[headerSize:]); sum != header.checksum {
		return indexSnapshot{}, fmt.Errorf("index checksum %08x, want %08x", sum, header.checksum)
	}

	files, err := sectionTable[indexFile](data, header.files)
	if err != nil {
		return indexSnapshot{}, err
	}
	records, err := sectionTable[indexFootprint](data, header.footprints)
	if err != nil {
		return indexSnapshot{}, err
	}
	pages, err := sectionTable[runePage](data, header.pages)
	if err != nil {
		return indexSnapshot{}, err
	}
	scripts, err := sectionTable[language.Script](data, header.scripts)
	if err != nil {
		return indexSnapshot{}, err
	}
	strs, err := sectionTable[byte](data, header.strings)
	if err != nil {
		return indexSnapshot{}, err
	}

	var out indexSnapshot
	if out.roots, err = sectionStrings(data, strs, header.roots); err != nil {
		return indexSnapshot{}, err
	}
	if out.dirs, err = sectionStamped(data, strs, header.dirs); err != nil {
		return indexSnapshot{}, err
	}
	if out.fc.dirs, err = sectionStrings(data, strs, header.fcDirs); err != nil {
		return indexSnapshot{}, err
	}
	if out.fc.deps, err = sectionStamped(data, strs, header.fcDeps); err != nil {
		return indexSnapshot{}, err
	}
	if out.fc.key, err = refString(strs, header.fcKey); err != nil {
		return indexSnapshot{}, err
	}

	if len(files) > 0 {
		out.fonts = make(systemFontsIndex, len(files))
	}
	var footprints []Footprint
	if len(records) > 0 {
		footprints = make([]Footprint, len(records))
	}
	for i := range files {
		file := &files[i]
		path, err := refString(strs, file.path)
		if err != nil {
			return indexSnapshot{}, err
		}
		end := uint64(file.firstFootprint) + uint64(file.footprintsCount)
		if end > uint64(len(records)) {
			return indexSnapshot{}, fmt.Errorf("file %d lists footprints up to %d of %d", i, end, len(records))
		}
		out.fonts[i] = fileFootprints{path: path, stamp: file.stamp}
		if file.footprintsCount == 0 {
			continue
		}
		for j := file.firstFootprint; j < uint32(end); j++ {
			record := &records[j]
			family, err := refString(strs, record.family)
			if err != nil {
				return indexSnapshot{}, err
			}
			pageEnd := uint64(record.firstPage) + uint64(record.pagesCount)
			scriptEnd := uint64(record.firstScript) + uint64(record.scriptsCount)
			if pageEnd > uint64(len(pages)) || scriptEnd > uint64(len(scripts)) {
				return indexSnapshot{}, fmt.Errorf("footprint %d names pages up to %d of %d and scripts up to %d of %d", j, pageEnd, len(pages), scriptEnd, len(scripts))
			}
			footprints[j] = Footprint{
				Location: Location{File: path, Index: record.index, Instance: record.instance},
				Family:   family,
				Runes:    RuneSet(pages[record.firstPage:pageEnd:pageEnd]),
				Scripts:  ScriptSet(scripts[record.firstScript:scriptEnd:scriptEnd]),
				Langs:    record.langs,
				Aspect:   font.Aspect{Style: font.Style(record.style), Weight: font.Weight(record.weight), Stretch: font.Stretch(record.stretch)},
			}
		}
		out.fonts[i].footprints = footprints[file.firstFootprint:end:end]
	}
	return out, nil
}

// readIndexFile maps the index at cachePath. A mapped index that parses
// stays mapped for the life of the process, since its footprints point
// into it. The mapping is private and writable so that those footprints
// behave as a heap copy would for a caller editing a RuneSet in place.
func readIndexFile(cachePath string) (indexSnapshot, error) {
	data, err := mapFile(cachePath, unix.PROT_READ|unix.PROT_WRITE)
	if err != nil {
		return indexSnapshot{}, err
	}
	snapshot, err := parseIndex(data)
	if err != nil {
		return indexSnapshot{}, errors.Join(err, unmapFile(data))
	}
	return snapshot, nil
}

type indexBuilder struct {
	strs      []byte
	stringAt  map[string]indexStringRef
	pages     []byte
	pagesAt   map[string]uint32
	scripts   []byte
	scriptsAt map[string]uint32
}

func (b *indexBuilder) addString(s string) indexStringRef {
	if ref, ok := b.stringAt[s]; ok {
		return ref
	}
	ref := indexStringRef{offset: uint32(len(b.strs)), length: uint32(len(s))}
	b.strs = append(b.strs, s...)
	b.stringAt[s] = ref
	return ref
}

func (b *indexBuilder) addRunes(rs RuneSet) (first, count uint32) {
	if len(rs) == 0 {
		return 0, 0
	}
	encoded := make([]byte, 0, len(rs)*int(unsafe.Sizeof(runePage{})))
	for _, page := range rs {
		encoded = binary.NativeEndian.AppendUint16(encoded, page.ref)
		encoded = append(encoded, 0, 0)
		for _, word := range page.set {
			encoded = binary.NativeEndian.AppendUint32(encoded, word)
		}
	}
	first, ok := b.pagesAt[string(encoded)]
	if !ok {
		first = uint32(len(b.pages) / int(unsafe.Sizeof(runePage{})))
		b.pages = append(b.pages, encoded...)
		b.pagesAt[string(encoded)] = first
	}
	return first, uint32(len(rs))
}

func (b *indexBuilder) addScripts(ss ScriptSet) (first, count uint32) {
	if len(ss) == 0 {
		return 0, 0
	}
	encoded := make([]byte, 0, len(ss)*4)
	for _, script := range ss {
		encoded = binary.NativeEndian.AppendUint32(encoded, uint32(script))
	}
	first, ok := b.scriptsAt[string(encoded)]
	if !ok {
		first = uint32(len(b.scripts) / 4)
		b.scripts = append(b.scripts, encoded...)
		b.scriptsAt[string(encoded)] = first
	}
	return first, uint32(len(ss))
}

func appendRecords[T any](dst []byte, section *indexSection, records []T) []byte {
	for len(dst)%indexTableAlign != 0 {
		dst = append(dst, 0)
	}
	section.offset = uint32(len(dst))
	section.count = uint32(len(records))
	if len(records) == 0 {
		return dst
	}
	var zero T
	return append(dst, unsafe.Slice((*byte)(unsafe.Pointer(&records[0])), len(records)*int(unsafe.Sizeof(zero)))...)
}

// encode lays the snapshot out as the index file. Every record is built
// field by field over a zeroed value, so padding is zero and equal
// snapshots encode to equal bytes. Identical rune and script sets, as
// the faces of one family have, are stored once.
func (s indexSnapshot) encode() ([]byte, error) {
	b := indexBuilder{
		stringAt:  map[string]indexStringRef{},
		pagesAt:   map[string]uint32{},
		scriptsAt: map[string]uint32{},
	}
	files := make([]indexFile, len(s.fonts))
	var footprints []indexFootprint
	for i, file := range s.fonts {
		files[i] = indexFile{
			path:            b.addString(file.path),
			stamp:           file.stamp,
			firstFootprint:  uint32(len(footprints)),
			footprintsCount: uint32(len(file.footprints)),
		}
		for _, fp := range file.footprints {
			record := indexFootprint{
				langs:    fp.Langs,
				family:   b.addString(fp.Family),
				index:    fp.Location.Index,
				instance: fp.Location.Instance,
				style:    uint32(fp.Aspect.Style),
				weight:   float32(fp.Aspect.Weight),
				stretch:  float32(fp.Aspect.Stretch),
			}
			record.firstPage, record.pagesCount = b.addRunes(fp.Runes)
			record.firstScript, record.scriptsCount = b.addScripts(fp.Scripts)
			footprints = append(footprints, record)
		}
	}
	stringRefs := func(ss []string) []indexStringRef {
		refs := make([]indexStringRef, len(ss))
		for i, value := range ss {
			refs[i] = b.addString(value)
		}
		return refs
	}
	stamped := func(paths []stampedPath) []indexStamped {
		records := make([]indexStamped, len(paths))
		for i, path := range paths {
			records[i] = indexStamped{path: b.addString(path.path), stamp: path.stamp}
		}
		return records
	}
	roots, dirs := stringRefs(s.roots), stamped(s.dirs)
	fcDirs, fcDeps := stringRefs(s.fc.dirs), stamped(s.fc.deps)

	var header indexHeader
	copy(header.magic[:], indexMagic)
	header.byteOrder = indexByteOrder
	header.version = cacheFormatVersion
	header.fcKey = b.addString(s.fc.key)

	data := make([]byte, unsafe.Sizeof(header))
	data = appendRecords(data, &header.files, files)
	data = appendRecords(data, &header.footprints, footprints)
	data = appendRecords(data, &header.roots, roots)
	data = appendRecords(data, &header.dirs, dirs)
	data = appendRecords(data, &header.fcDirs, fcDirs)
	data = appendRecords(data, &header.fcDeps, fcDeps)
	data = appendRecords(data, &header.pages, b.pages)
	header.pages.count /= uint32(unsafe.Sizeof(runePage{}))
	data = appendRecords(data, &header.scripts, b.scripts)
	header.scripts.count /= 4
	data = appendRecords(data, &header.strings, b.strs)
	if len(data) > math.MaxUint32 {
		return nil, fmt.Errorf("font index of %d bytes exceeds the format's limit", len(data))
	}

	header.size = uint64(len(data))
	header.checksum = crc32.ChecksumIEEE(data[unsafe.Sizeof(header):])
	copy(data, unsafe.Slice((*byte)(unsafe.Pointer(&header)), unsafe.Sizeof(header)))
	return data, nil
}

// writeIndexFile replaces the index at cachePath whole, through a rename,
// so a concurrent reader sees the old file or the new one. A mapping of
// the old one stays valid.
func (s indexSnapshot) writeIndexFile(cachePath string) error {
	data, err := s.encode()
	if err != nil {
		return err
	}
	dir := filepath.Dir(cachePath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("unable to create system font cache dir %q: %w", dir, err)
	}
	f, err := os.CreateTemp(dir, filepath.Base(cachePath)+".*")
	if err != nil {
		return fmt.Errorf("creating font index in %q: %w", dir, err)
	}
	defer os.Remove(f.Name())

	if _, err := f.Write(data); err != nil {
		f.Close()
		return fmt.Errorf("writing font index %q: %w", f.Name(), err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("writing font index %q: %w", f.Name(), err)
	}
	if err := os.Rename(f.Name(), cachePath); err != nil {
		return fmt.Errorf("replacing font index %q: %w", cachePath, err)
	}
	return nil
}
