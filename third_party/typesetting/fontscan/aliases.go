package fontscan

import (
	"encoding/binary"
	"errors"
	"unicode/utf16"

	"github.com/stubbedev/lys-lib/third_party/typesetting/font"
	ot "github.com/stubbedev/lys-lib/third_party/typesetting/font/opentype"
	"github.com/stubbedev/lys-lib/third_party/typesetting/font/opentype/tables"
)

// familyNameIDs are the name records a family is matched by, as
// fontconfig indexes them: the legacy family (1), the typographic
// family (16), and the WWS family (21).
var familyNameIDs = [...]uint16{1, 16, 21}

// familyAliases reads the family names a face declares besides family,
// the one it is filed under. A face whose names cannot be read has none:
// it stays reachable under family. raw is reused and returned.
func familyAliases(ld *ot.Loader, family string, raw []byte) ([]string, []byte) {
	table, err := ld.RawTableTo(ot.MustNewTag("name"), raw)
	if err != nil {
		return nil, raw
	}
	names, err := familyNames(table)
	if err != nil {
		return nil, table
	}
	aliases := names[:0]
	for _, name := range names {
		if name != family {
			aliases = append(aliases, name)
		}
	}
	if len(aliases) == 0 {
		return nil, table
	}
	return aliases, table
}

// errNameTable reports a truncated or inconsistent 'name' table.
var errNameTable = errors.New("fontscan: malformed name table")

// familyNames decodes every family record of a raw 'name' table:
// Unicode and Windows records as UTF-16BE, Mac Roman records through
// the Macintosh encoding, normalized and deduped in table order.
// Records in other Mac encodings are skipped, as fontconfig skips
// encodings it cannot convert.
func familyNames(raw []byte) ([]string, error) {
	if len(raw) < 6 {
		return nil, errNameTable
	}
	count := int(binary.BigEndian.Uint16(raw[2:]))
	storage := int(binary.BigEndian.Uint16(raw[4:]))
	if len(raw) < 6+12*count || storage > len(raw) {
		return nil, errNameTable
	}
	var out []string
	seen := map[string]bool{}
	for i := range count {
		rec := raw[6+12*i:]
		platform := tables.PlatformID(binary.BigEndian.Uint16(rec))
		encoding := tables.EncodingID(binary.BigEndian.Uint16(rec[2:]))
		id := binary.BigEndian.Uint16(rec[6:])
		length := int(binary.BigEndian.Uint16(rec[8:]))
		offset := storage + int(binary.BigEndian.Uint16(rec[10:]))
		if !isFamilyNameID(id) || length == 0 {
			continue
		}
		if offset+length > len(raw) {
			return nil, errNameTable
		}
		name, ok := decodeName(platform, encoding, raw[offset:offset+length])
		if !ok {
			continue
		}
		if norm := font.NormalizeFamily(name); norm != "" && !seen[norm] {
			seen[norm] = true
			out = append(out, norm)
		}
	}
	return out, nil
}

func isFamilyNameID(id uint16) bool {
	for _, want := range familyNameIDs {
		if id == want {
			return true
		}
	}
	return false
}

// decodeName decodes one name record's bytes; false for an encoding
// it cannot convert.
func decodeName(platform tables.PlatformID, encoding tables.EncodingID, b []byte) (string, bool) {
	switch {
	case platform == tables.PlatformUnicode || platform == tables.PlatformMicrosoft:
		units := make([]uint16, len(b)/2)
		for i := range units {
			units[i] = binary.BigEndian.Uint16(b[2*i:])
		}
		return string(utf16.Decode(units)), true
	case platform == tables.PlatformMac && encoding == tables.PEMacRoman:
		return tables.DecodeMacintosh(b), true
	}
	return "", false
}
