package fontscan

import (
	"errors"
	"fmt"
	"log"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/stubbedev/lys-lib/third_party/typesetting/font"
	ot "github.com/stubbedev/lys-lib/third_party/typesetting/font/opentype"
	"github.com/stubbedev/lys-lib/third_party/typesetting/language"
)

type cacheEntry struct {
	Location

	Family string
	font.Aspect
}

// Logger is a type that can log warnings.
type Logger interface {
	Printf(format string, args ...interface{})
}

// The family substitution algorithm is copied from fontconfig
// and the match algorithm is inspired from Rust font-kit library

// SystemFonts loads the system fonts, using an index stored in [cacheDir].
// See [FontMap.UseSystemFonts] for more details. The returned slice is shared
// and must not be modified.
//
// If [logger] is nil, log.Default() is used.
func SystemFonts(logger Logger, cacheDir string) ([]Footprint, error) {
	if logger == nil {
		logger = log.New(log.Writer(), "fontscan", log.Flags())
	}

	// safe for concurrent use; subsequent calls are no-ops
	err := initSystemFonts(logger, cacheDir)
	if err != nil {
		return nil, err
	}

	// systemFonts is read-only, so may be used concurrently
	return systemFonts, nil
}

// FontMap provides a mechanism to select a [font.Face] from a font description.
// It supports system and user-provided fonts, and implements the CSS font substitutions
// rules.
//
// Note that [FontMap] is NOT safe for concurrent use, but several font maps may coexist
// in an application.
//
// [FontMap] is mainly designed to work with an index built by scanning the system fonts :
// see [UseSystemFonts] for more details.
type FontMap struct {
	logger Logger
	// caches of already loaded faceCache : the two maps are updated conjointly
	firstFace *font.Face
	faceCache map[Location]*font.Face
	metaCache map[*font.Font]cacheEntry

	// pending holds the fonts [AddFont] registered and nothing has needed
	// yet, by their index in database: only the family and aspect of those
	// entries are filled in. See [FontMap.realize].
	pending map[int]*ot.Loader
	// firstPending is the entry that becomes firstFace when it is realized,
	// the first font registered while firstFace was unset, or noPendingFace.
	firstPending int

	// the database to query, either loaded from an index
	// or populated with the [UseSystemFonts], [AddFont], and/or [AddFace] method.
	database  fontSet
	scriptMap map[language.Script][]int
	lru       runeLRU

	// built holds whether the candidates are populated.
	built bool
	// the candidates for the current query, which influences ResolveFace output
	candidates candidates

	// internal buffers used in [buildCandidates]

	footprintsBuffer scoredFootprints
	cribleBuffer     familyCrible

	query  Query           // current query
	script language.Script // current script
}

const noPendingFace = -1

// NewFontMap return a new font map, which should be filled with the `UseSystemFonts`
// or `AddFont` methods. The provided logger will be used to record non-fatal errors
// encountered during font loading. If logger is nil, log.Default() is used.
func NewFontMap(logger Logger) *FontMap {
	if logger == nil {
		logger = log.New(log.Writer(), "fontscan", log.Flags())
	}
	fm := &FontMap{
		logger:       logger,
		faceCache:    make(map[Location]*font.Face),
		metaCache:    make(map[*font.Font]cacheEntry),
		cribleBuffer: make(familyCrible, 150),
		scriptMap:    make(map[language.Script][]int),
		pending:      make(map[int]*ot.Loader),
		firstPending: noPendingFace,
	}
	fm.lru.maxSize = 4096
	return fm
}

// SetRuneCacheSize configures the size of the cache powering [FontMap.ResolveFace].
// Applications displaying large quantities of text should tune this value to be greater
// than the number of unique glyphs they expect to display at one time in order to achieve
// optimal performance when segmenting text by face rune coverage.
func (fm *FontMap) SetRuneCacheSize(size int) {
	fm.lru.maxSize = size
}

// UseSystemFonts loads the system fonts and adds them to the font map.
//
// The first call of this method trigger a rather long scan.
// A per-application on-disk cache is used to speed up subsequent initialisations.
// Callers can provide an appropriate directory path within which this cache may be
// stored. If the empty string is provided, the FontMap will attempt to infer a correct,
// platform-dependent cache path.
//
// NOTE: On Android, callers *must* provide a writable path manually, as it cannot
// be inferred without access to the Java runtime environment of the application.
//
// Multiple font maps may call this method concurrently, without duplicating
// the work of finding the system fonts.
func (fm *FontMap) UseSystemFonts(cacheDir string) error {
	// safe for concurrent use; subsequent calls are no-ops
	err := initSystemFonts(fm.logger, cacheDir)
	if err != nil {
		return err
	}

	// systemFonts is read-only, so may be used concurrently
	fm.appendFootprints(systemFonts...)

	fm.built = false

	fm.lru.Clear()
	return nil
}

// appendFootprints adds the provided footprints to the database and maps their script
// coverage.
func (fm *FontMap) appendFootprints(footprints ...Footprint) {
	startIdx := len(fm.database)
	if startIdx == 0 {
		fm.database = footprints[:len(footprints):len(footprints)]
	} else {
		fm.database = append(fm.database, footprints...)
	}
	// Insert entries into scriptMap for each footprint's covered scripts.
	for i, fp := range footprints {
		dbIdx := startIdx + i
		for _, script := range fp.Scripts {
			fm.scriptMap[script] = append(fm.scriptMap[script], dbIdx)
		}
	}
}

// systemFonts is a global index of the system fonts.
// initSystemFontsOnce protects the initial assignment,
// and `systemFonts` use is then read-only
var (
	systemFonts         fontSet
	initSystemFontsOnce sync.Once
)

func cacheDir(userProvided string) (string, error) {
	if userProvided != "" {
		return userProvided, nil
	}
	// load an existing index
	return platformCacheDir()
}

// initSystemFonts scan the system fonts and update `SystemFonts`.
// If the returned error is nil, `SystemFonts` is guaranteed to contain
// at least one valid font.Face.
// It is protected by sync.Once, and is then safe to use by multiple goroutines.
func initSystemFonts(logger Logger, userCacheDir string) error {
	var err error

	initSystemFontsOnce.Do(func() {
		var index systemFontsIndex
		index, err = refreshSystemFontsIndex(logger, indexPath(logger, userCacheDir), time.Now())
		if err == nil {
			systemFonts = index.flatten()
		}
	})

	return err
}

const cacheFilePattern = "font_index_v%d.cache"

// indexPath is where the index lives, "" without a cache directory, as for
// a system user with no home: the fonts are then scanned on every start.
func indexPath(logger Logger, userCacheDir string) string {
	dir, err := cacheDir(userCacheDir)
	if err != nil {
		logger.Printf("no font index: %s", err)
		return ""
	}
	return filepath.Join(dir, fmt.Sprintf(cacheFilePattern, cacheFormatVersion))
}

func removePreviousIndex(logger Logger, cachePath string) {
	previous := filepath.Join(filepath.Dir(cachePath), fmt.Sprintf(cacheFilePattern, cacheFormatVersion-1))
	if err := os.Remove(previous); err != nil && !errors.Is(err, os.ErrNotExist) {
		logger.Printf("removing old font index: %s", err)
	}
}

func refreshSystemFontsIndex(logger Logger, cachePath string, now time.Time) (systemFontsIndex, error) {
	var previous indexSnapshot
	if cachePath != "" {
		var err error
		previous, err = readIndexFile(cachePath)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			logger.Printf("rescanning system fonts: unusable index %q: %s", cachePath, err)
		}
	}

	fc := previous.fc
	fcCurrent := true
	roots, err := fontDirectories(logger, func() []string {
		vars := fcVarsFromEnv()
		if fc.currentFor(vars) {
			return fc.dirs
		}
		var err error
		fc, err = vars.parseFcConfig(logger, now)
		if err != nil {
			logger.Printf("unable to process fontconfig config file: %s", err)
			return nil
		}
		fcCurrent = false
		return fc.dirs
	})
	if err != nil {
		return nil, fmt.Errorf("searching font directories: %w", err)
	}

	if fcCurrent && previous.isCurrent(roots) {
		return previous.fonts, nil
	}
	logger.Printf("using system font dirs %q", roots)

	scan, err := scanFontFootprints(logger, previous.fonts, now, roots...)
	if err != nil {
		return nil, fmt.Errorf("scanning system fonts: %s", err)
	}

	// since ResolveFace must always return a valid face, we make sure
	// at least one font exists and is valid.
	// Otherwise, the font map is useless; this is an extreme case anyway.
	// An index that was read unchanged passed this before it was written.
	if !scan.unchanged || len(scan.index) == 0 {
		if err := scan.index.assertValid(); err != nil {
			return nil, fmt.Errorf("loading system fonts: %s", err)
		}
	}

	if cachePath == "" || (scan.unchanged && fcCurrent && slices.Equal(previous.roots, roots) && slices.Equal(previous.dirs, scan.dirs)) {
		return scan.index, nil
	}

	// The index only saves work on the next start: failing to write it is
	// no reason to go without fonts.
	updated := indexSnapshot{fonts: scan.index, roots: roots, dirs: scan.dirs, fc: fc}
	if err := updated.writeIndexFile(cachePath); err != nil {
		logger.Printf("updating font index: %s", err)
		return scan.index, nil
	}
	removePreviousIndex(logger, cachePath)
	return scan.index, nil
}

// [AddFont] loads the faces contained in [fontFile] and add them to
// the font map.
// [fileID] is used as the [Location.File] entry returned by [FontLocation].
//
// If `familyName` is not empty, it is used as the family name for `fontFile`
// instead of the one found in the font file.
//
// An error is returned if the font resource is not supported. Its faces
// are parsed when a query first needs what they cover, so fontFile must
// stay readable as long as the FontMap is used, and a face whose tables
// turn out to be damaged is then logged and covers nothing.
//
// The order of calls to [AddFont] and [AddFace] determines relative priority
// of manually loaded fonts. See [ResolveFace] for details about when this matters.
func (fm *FontMap) AddFont(fontFile font.Resource, fileID, familyName string) error {
	loaders, err := ot.NewLoaders(fontFile)
	if err != nil {
		return fmt.Errorf("unsupported font resource: %s", err)
	}

	added := make([]Footprint, len(loaders))
	var raw []byte
	for i, ld := range loaders {
		var desc font.Description
		desc, raw = font.Describe(ld, raw)
		family := familyName
		if family == "" {
			family = desc.Family
		}
		added[i] = Footprint{
			Location:       Location{File: fileID, Index: uint16(i)},
			Family:         font.NormalizeFamily(family),
			Aspect:         desc.Aspect,
			isUserProvided: true,
		}
		fm.pending[len(fm.database)+i] = ld
	}
	if len(added) == 0 {
		return fmt.Errorf("empty font resource %s", fileID)
	}
	if fm.firstFace == nil && fm.firstPending == noPendingFace {
		fm.firstPending = len(fm.database)
	}

	fm.appendFootprints(added...)

	fm.built = false

	fm.lru.Clear()
	return nil
}

// realize parses the font registered at index in the database, if it is
// still pending, and fills in what it covers. A font that does not parse
// is logged and covers nothing.
func (fm *FontMap) realize(index int) {
	ld, isPending := fm.pending[index]
	if !isPending {
		return
	}
	delete(fm.pending, index)
	if index == fm.firstPending {
		fm.firstPending = noPendingFace
	}
	fp := &fm.database[index]
	coverage, _, err := newCoverageFromLoader(ld, scanBuffer{})
	if err != nil {
		fm.logger.Printf("reading font %s: %v", fp.Location.File, err)
		return
	}
	ft, err := font.NewFont(ld)
	if err != nil {
		fm.logger.Printf("reading font %s: %v", fp.Location.File, err)
		return
	}
	fp.Runes, fp.Scripts, fp.Langs = coverage.Runes, coverage.Scripts, coverage.Langs
	for _, script := range fp.Scripts {
		entries := fm.scriptMap[script]
		at, _ := slices.BinarySearch(entries, index)
		fm.scriptMap[script] = slices.Insert(entries, at, index)
	}
	fm.cache(*fp, font.NewFace(ft))
}

// realizeAll parses every pending font, in the order they were registered.
func (fm *FontMap) realizeAll() {
	for _, index := range slices.Sorted(maps.Keys(fm.pending)) {
		fm.realize(index)
	}
}

// [AddFace] inserts an already-loaded font.Face into the FontMap. The caller
// is responsible for ensuring that [md] is accurate for the face.
//
// The order of calls to [AddFont] and [AddFace] determines relative priority
// of manually loaded fonts. See [ResolveFace] for details about when this matters.
func (fm *FontMap) AddFace(face *font.Face, location Location, md font.Description) {
	fp := newFootprintFromFont(face.Font, location, md)
	fm.cache(fp, face)

	fm.appendFootprints(fp)

	fm.built = false
	fm.lru.Clear()
}

func (fm *FontMap) cache(fp Footprint, face *font.Face) {
	if fm.firstFace == nil && fm.firstPending == noPendingFace {
		fm.firstFace = face
	}
	fm.faceCache[fp.Location] = face
	fm.metaCache[face.Font] = cacheEntry{fp.Location, fp.Family, fp.Aspect}
}

// FontLocation returns the origin of the provided font. If the font was not
// previously returned from this FontMap by a call to ResolveFace, the zero
// value will be returned instead.
func (fm *FontMap) FontLocation(ft *font.Font) Location {
	return fm.metaCache[ft].Location
}

// FontMetadata returns a description of the provided font. If the font was not
// previously returned from this FontMap by a call to ResolveFace, the zero
// value will be returned instead.
//
// Note that, for fonts added with [AddFace], it is the user provided description
// that is returned, not the one returned by [Font.Describe]
func (fm *FontMap) FontMetadata(ft *font.Font) (family string, aspect font.Aspect) {
	item := fm.metaCache[ft]
	return item.Family, item.Aspect
}

// FindSystemFont looks for a system font with the given [family],
// returning the first match, or false is no one is found.
//
// User added fonts are ignored, and the [FontMap] must have been
// initialized with [UseSystemFonts] or this method will always return false.
//
// Family names are compared through [font.Normalize].
func (fm *FontMap) FindSystemFont(family string) (Location, bool) {
	family = font.NormalizeFamily(family)
	for _, footprint := range fm.database {
		if footprint.isUserProvided {
			continue
		}
		if footprint.Family == family {
			return footprint.Location, true
		}
	}
	return Location{}, false
}

// FindSystemFonts is the same as FindSystemFont, but returns all matched fonts.
func (fm *FontMap) FindSystemFonts(family string) []Location {
	var locations []Location
	family = font.NormalizeFamily(family)
	for _, footprint := range fm.database {
		if footprint.isUserProvided {
			continue
		}
		if footprint.Family == family {
			locations = append(locations, footprint.Location)
		}
	}

	return locations
}

// SetQuery set the families and aspect required, influencing subsequent
// [ResolveFace] calls. See also [SetScript].
func (fm *FontMap) SetQuery(query Query) {
	if len(query.Families) == 0 {
		query.Families = []string{""}
	}
	fm.query = query
	fm.built = false
}

// SetScript set the script to which the (next) runes passed to [ResolveFace]
// belongs, influencing the choice of fallback fonts.
func (fm *FontMap) SetScript(s language.Script) {
	fm.script = s
	fm.built = false
}

// candidates is a cache storing the indices into FontMap.database of footprints matching a Query
// families
type candidates struct {
	// footprints with exact match :
	// for each queried family, at most one footprint is selected
	withoutFallback []int

	// footprints matching the expanded query (where subsitutions have been applied)
	withFallback []int

	manual []int // manually inserted faces to be tried if the other candidates fail.
}

// reset slices, setting the capacity of withoutFallback to nbFamilies
func (cd *candidates) resetWithSize(nbFamilies int) {
	if cap(cd.withoutFallback) < nbFamilies { // reallocate
		cd.withoutFallback = make([]int, nbFamilies)
	}

	cd.withoutFallback = cd.withoutFallback[:0]
	cd.withFallback = cd.withFallback[:0]
	cd.manual = cd.manual[:0]
}

func (fm *FontMap) buildCandidates() {
	if fm.built {
		return
	}
	// a script selects fonts by what they cover
	if fm.script != 0 {
		fm.realizeAll()
	}

	fm.candidates.resetWithSize(len(fm.query.Families))

	// first pass for an exact match
	{
		for _, family := range fm.query.Families {
			candidates := fm.database.selectByFamilyExact(family, fm.cribleBuffer, &fm.footprintsBuffer)
			if len(candidates) == 0 {
				continue
			}

			// select the correct aspects
			candidates = fm.database.retainsBestMatches(candidates, fm.query.Aspect)

			// with no system fallback, the CSS spec says
			// that only one font among the candidates must be tried
			fm.candidates.withoutFallback = append(fm.candidates.withoutFallback, candidates[0])
		}
	}

	// second pass with substitutions
	{
		candidates := fm.database.selectByFamilyWithSubs(fm.query.Families, fm.script, fm.cribleBuffer, &fm.footprintsBuffer)

		// select the correct aspects
		candidates = fm.database.retainsBestMatches(candidates, fm.query.Aspect)

		// candidates is owned by fm.footprintsBuffer: copy its content
		S := fm.candidates.withFallback
		if L := len(candidates); cap(S) < L {
			S = make([]int, L)
		} else {
			S = S[:L]
		}
		copy(S, candidates)
		fm.candidates.withFallback = S
	}

	// third pass with user provided fonts
	{
		fm.candidates.manual = fm.database.filterUserProvided(fm.candidates.manual)
		fm.candidates.manual = fm.database.retainsBestMatches(fm.candidates.manual, fm.query.Aspect)
	}

	fm.built = true
}

// returns nil if not candidates supports the rune `r`
func (fm *FontMap) resolveForRune(candidates []int, r rune) *font.Face {
	for _, footprintIndex := range candidates {
		fm.realize(footprintIndex)
		// check the coverage
		if fp := fm.database[footprintIndex]; fp.Runes.Contains(r) {
			// try to use the font
			face, err := fm.loadFont(fp)
			if err != nil { // very unlikely; try another family
				fm.logger.Printf("failed loading face: %v", err)
				continue
			}

			return face
		}
	}

	return nil
}

// returns nil if no candidates support the language `lang`
func (fm *FontMap) resolveForLang(candidates []int, lang LangID) *font.Face {
	for _, footprintIndex := range candidates {
		fm.realize(footprintIndex)
		// check the coverage
		if fp := fm.database[footprintIndex]; fp.Langs.Contains(lang) {
			// try to use the font
			face, err := fm.loadFont(fp)
			if err != nil { // very unlikely; try another family
				fm.logger.Printf("failed loading face: %v", err)
				continue
			}

			return face
		}
	}

	return nil
}

// ResolveFace select a font based on the current query (set by [FontMap.SetQuery] and [FontMap.SetScript]),
// and supporting the given rune, applying CSS font selection rules.
//
// Fonts are tried with the following steps :
//
//	1 - Only fonts matching exacly one of the [Query.Families] are considered; the list
//		is prunned to keep the best match with [Query.Aspect]
//	2 - Fallback fonts are considered, that is fonts with similar families and fonts
//		supporting the current script; the list is also prunned according to [Query.Aspect]
//	3 - Fonts added manually by [AddFont] and [AddFace] (prunned according to [Query.Aspect]),
//		will be searched, in the order in which they were added.
//	4 - All fonts matching the current script (set by [FontMap.SetScript]) are tried,
//		ignoring [Query.Aspect]
//
// If no fonts match after these steps, an arbitrary face will be returned.
// This face will be nil only if the underlying font database is empty,
// or if the file system is broken; otherwise the returned [font.Face] is always valid.
func (fm *FontMap) ResolveFace(r rune) (face *font.Face) {
	key := fm.lru.KeyFor(fm.query, fm.script, r)
	face, ok := fm.lru.Get(key, fm.query)
	if ok {
		return face
	}
	defer func() {
		fm.lru.Put(key, fm.query, face)
	}()

	// Build the candidates if we missed the cache. If they're already built this is a
	// no-op.
	fm.buildCandidates()

	// we first look up for an exact family match, without substitutions
	if face := fm.resolveForRune(fm.candidates.withoutFallback, r); face != nil {
		return face
	}

	// if no family has matched so far, try again with system fallback,
	// including fonts with matching script and user provided ones
	if face := fm.resolveForRune(fm.candidates.withFallback, r); face != nil {
		return face
	}

	// try manually loaded faces even if the typeface doesn't match, looking for matching aspects
	// and rune coverage.
	// Note that, when [SetScript] has been called, this step is actually not needed,
	// since the fonts supporting the given script are already added in [withFallback] fonts
	if face := fm.resolveForRune(fm.candidates.manual, r); face != nil {
		return face
	}

	fm.logger.Printf("No font matched for aspect %v, script %s, and rune %U (%c) -> searching by script coverage only", fm.query.Aspect, fm.script, r, r)
	scriptCandidates := fm.scriptMap[fm.script]
	if face := fm.resolveForRune(scriptCandidates, r); face != nil {
		return face
	}

	fm.logger.Printf("No font matched for script %s and rune %U (%c) -> returning arbitrary face", fm.script, r, r)
	// return an arbitrary face
	fm.realizeAll()
	if fm.firstFace == nil && len(fm.database) > 0 {
		for _, fp := range fm.database {
			face, err := fm.loadFont(fp)
			if err != nil {
				// very unlikely; warn and keep going
				fm.logger.Printf("failed loading face: %v", err)
				continue
			}
			return face
		}
	}

	return fm.firstFace
	// refreshSystemFontsIndex makes sure at least one face is valid
	// and AddFont also check for valid font files, meaning that
	// a valid FontMap should always contain a valid face,
	// and we should never return a nil face.
}

// ResolveForLang returns the first face supporting the given language
// (for the actual query), or nil if no one is found.
//
// The matching logic is similar to the one used by [ResolveFace].
func (fm *FontMap) ResolveFaceForLang(lang LangID) *font.Face {
	// no-op if already built
	fm.buildCandidates()

	// we first look up for an exact family match, without substitutions
	if face := fm.resolveForLang(fm.candidates.withoutFallback, lang); face != nil {
		return face
	}

	// if no family has matched so far, try again with system fallback
	if face := fm.resolveForLang(fm.candidates.withFallback, lang); face != nil {
		return face
	}

	// try manually loaded faces even if the typeface doesn't match, looking for matching aspects
	// and rune coverage.
	if face := fm.resolveForLang(fm.candidates.manual, lang); face != nil {
		return face
	}

	return nil
}

func (fm *FontMap) loadFont(fp Footprint) (*font.Face, error) {
	if face, hasCached := fm.faceCache[fp.Location]; hasCached {
		return face, nil
	}

	// since user provided fonts are added to `faceCache`
	// we may now assume the font is stored on the file system
	face, err := fp.loadFromDisk()
	if err != nil {
		return nil, err
	}

	// add the face to the cache
	fm.cache(fp, face)

	return face, nil
}
