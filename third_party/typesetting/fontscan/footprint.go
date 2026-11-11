package fontscan

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-lib/third_party/typesetting/font"
	ot "github.com/stubbedev/lys-lib/third_party/typesetting/font/opentype"
	"github.com/stubbedev/lys-lib/third_party/typesetting/font/opentype/tables"
)

// Location identifies where a font.Face is stored.
type Location = font.FontID

// Footprint is a condensed summary of the main information
// about a font, serving as a lightweight surrogate
// for the original font file.
type Footprint struct {
	// Location stores the adress of the font resource.
	Location Location

	// Family is the general nature of the font, like
	// "Arial"
	// Note that, for performance reason, we store the
	// normalized version of the family name.
	Family string

	// Aliases are the other normalized family names the font declares, on
	// any platform and in any language. Set for system fonts only.
	Aliases []string

	// Runes is the set of runes supported by the font.
	Runes RuneSet

	// Scripts is the set of scripts deduced from [Runes]
	Scripts ScriptSet

	// Langs is the set of languages deduced from [Runes]
	Langs LangSet

	// Aspect precises the visual characteristics
	// of the font among a family, like "Bold Italic"
	Aspect font.Aspect

	// isUserProvided is set to true for fonts add manually to
	// a FontMap
	// User fonts will always be tried if no other fonts match,
	// and will have priority among font with same family name.
	//
	// This field is not serialized in the index, since it is always false
	// for system fonts.
	isUserProvided bool
}

func newFootprintFromFont(f *font.Font, location Location, md font.Description) (out Footprint) {
	out.Runes, out.Scripts, _ = newCoveragesFromCmap(f.Cmap, nil)
	out.Langs = newLangsetFromCoverage(out.Runes)
	out.Family = font.NormalizeFamily(md.Family)
	out.Aspect = md.Aspect
	out.Location = location
	out.isUserProvided = true
	return out
}

// newFootprintFromLoader summarizes a system font.
func newFootprintFromLoader(ld *ot.Loader, buffer scanBuffer) (out Footprint, _ scanBuffer, err error) {
	out, buffer, err = newCoverageFromLoader(ld, buffer)
	if err != nil {
		return Footprint{}, buffer, err
	}
	desc, raw := font.Describe(ld, buffer.tableBuffer)
	out.Family = font.NormalizeFamily(desc.Family)
	out.Aspect = desc.Aspect
	out.Aliases, buffer.tableBuffer = familyAliases(ld, out.Family, raw)
	return out, buffer, nil
}

// newCoverageFromLoader fills in what a font covers: its runes, scripts
// and languages.
func newCoverageFromLoader(ld *ot.Loader, buffer scanBuffer) (out Footprint, _ scanBuffer, err error) {
	raw := buffer.tableBuffer

	// since raw is shared, special car must be taken in the parsing order

	raw, _ = ld.RawTableTo(ot.Tag('O'<<24|'S'<<16|'/'<<8|'2'), raw)
	fp := tables.FPNone
	if os2, _, err := tables.ParseOs2(raw); err != nil {
		fp = os2.FontPage()
	}

	// we can use the buffer since ProcessCmap do not keep any reference on
	// the input slice
	raw, err = ld.RawTableTo(ot.Tag('c'<<24|'m'<<16|'a'<<8|'p'), raw)
	if err != nil {
		return Footprint{}, buffer, err
	}
	tb, _, err := tables.ParseCmap(raw)
	if err != nil {
		return Footprint{}, buffer, err
	}
	cmap, _, err := font.ProcessCmap(tb, fp)
	if err != nil {
		return Footprint{}, buffer, err
	}

	out.Runes, out.Scripts, buffer.cmapBuffer = newCoveragesFromCmap(cmap, buffer.cmapBuffer) // ... and build the corresponding rune set

	out.Langs = newLangsetFromCoverage(out.Runes)

	buffer.tableBuffer = raw

	return out, buffer, nil
}

// returns true for .ttf and .ttc font files
func (fp *Footprint) isTruetypeHint() bool {
	switch strings.ToLower(filepath.Ext(fp.Location.File)) {
	case ".ttf", ".ttc":
		return true
	default:
		return false
	}
}

// isMonoHint returns true if "mono" is included in the family name
// this is not very precise but much more efficient than using [font.Font.IsMonospace]
func (fp *Footprint) isMonoHint() bool {
	return strings.Contains(fp.Family, "mono")
}

// mapFile maps path privately with the given protection, or reads it on
// a file system that cannot map. An empty file maps to nothing.
func mapFile(path string, prot int) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() == 0 {
		return nil, nil
	}
	data, err := unix.Mmap(int(file.Fd()), 0, int(info.Size()), prot, unix.MAP_PRIVATE)
	if err != nil {
		return os.ReadFile(path)
	}
	return data, nil
}

// unmapFile releases what mapFile returned; EINVAL is the buffer of a
// file that was read rather than mapped, or of an empty one.
func unmapFile(data []byte) error {
	if err := unix.Munmap(data); err != nil && !errors.Is(err, unix.EINVAL) {
		return fmt.Errorf("unmapping font file: %w", err)
	}
	return nil
}

func mapFontFile(path string) ([]byte, error) { return mapFile(path, unix.PROT_READ) }

func (fp *Footprint) parseFont(data []byte) (*font.Font, error) {
	location := fp.Location
	loaders, err := ot.NewLoaders(ot.NewMapped(data))
	if err != nil {
		return nil, err
	}
	if index := int(location.Index); len(loaders) <= index {
		// this should only happen if the font file as changed
		// since the last scan (very unlikely)
		return nil, fmt.Errorf("invalid font index in collection: %d >= %d", index, len(loaders))
	}
	ft, err := font.NewFont(loaders[location.Index])
	if err != nil {
		return nil, fmt.Errorf("reading font at %s: %w", location.File, err)
	}
	return ft, nil
}

// loadFromDisk assume the footprint location refers to the file system.
// The face keeps its mapping, since its tables stay in place.
func (fp *Footprint) loadFromDisk() (*font.Face, error) {
	data, err := mapFontFile(fp.Location.File)
	if err != nil {
		return nil, err
	}
	ft, err := fp.parseFont(data)
	if err != nil {
		return nil, errors.Join(err, unmapFile(data))
	}
	return font.NewFace(ft), nil
}

// checkLoads parses the face and lets it go.
func (fp *Footprint) checkLoads() error {
	data, err := mapFontFile(fp.Location.File)
	if err != nil {
		return err
	}
	_, err = fp.parseFont(data)
	return errors.Join(err, unmapFile(data))
}
