package fontscan

import (
	"encoding/binary"
	"errors"
	"slices"
	"testing"
	"unicode/utf16"
)

// nameRecord is one record for nameTable.
type nameRecord struct {
	platform, encoding, language, id uint16
	value                            string
}

// nameTable encodes records as a format-0 'name' table: Unicode and
// Windows strings as UTF-16BE, everything else as raw bytes.
func nameTable(records ...nameRecord) []byte {
	var storage []byte
	head := make([]byte, 6+12*len(records))
	binary.BigEndian.PutUint16(head[2:], uint16(len(records)))
	binary.BigEndian.PutUint16(head[4:], uint16(len(head)))
	for i, r := range records {
		var b []byte
		if r.platform == 0 || r.platform == 3 {
			for _, u := range utf16.Encode([]rune(r.value)) {
				b = binary.BigEndian.AppendUint16(b, u)
			}
		} else {
			b = []byte(r.value)
		}
		rec := head[6+12*i:]
		binary.BigEndian.PutUint16(rec, r.platform)
		binary.BigEndian.PutUint16(rec[2:], r.encoding)
		binary.BigEndian.PutUint16(rec[4:], r.language)
		binary.BigEndian.PutUint16(rec[6:], r.id)
		binary.BigEndian.PutUint16(rec[8:], uint16(len(b)))
		binary.BigEndian.PutUint16(rec[10:], uint16(len(storage)))
		storage = append(storage, b...)
	}
	return append(head, storage...)
}

func TestFamilyNamesReadsEveryFamilyRecord(t *testing.T) {
	raw := nameTable(
		nameRecord{1, 0, 0, 1, "JetBrainsMono NF Medium"},
		nameRecord{3, 1, 1033, 16, "JetBrainsMono Nerd Font"},
		nameRecord{3, 1, 2057, 16, "JetBrainsMono NF"},
		nameRecord{3, 1, 1033, 21, "JetBrainsMono WWS"},
		nameRecord{3, 1, 1033, 1, "JetBrainsMono NF Medium"},
		nameRecord{3, 1, 1033, 4, "JetBrainsMono NF Medium Full"},
		nameRecord{1, 1, 0, 1, "Japanese-encoded"},
	)
	got, err := familyNames(raw)
	if err != nil {
		t.Fatalf("familyNames: %v", err)
	}
	want := []string{"jetbrainsmononfmedium", "jetbrainsmononerdfont", "jetbrainsmononf", "jetbrainsmonowws"}
	if !slices.Equal(got, want) {
		t.Errorf("familyNames = %q, want %q (families 1/16/21 on every platform and language, deduped; the full name and unconvertible Mac encodings out)", got, want)
	}
}

func TestFamilyNamesRejectsMalformedTables(t *testing.T) {
	good := nameTable(nameRecord{3, 1, 1033, 1, "Family"})
	for name, raw := range map[string][]byte{
		"short header":     good[:4],
		"truncated record": good[:10],
		"string past end":  good[:len(good)-2],
	} {
		if _, err := familyNames(raw); !errors.Is(err, errNameTable) {
			t.Errorf("%s: err = %v, want errNameTable", name, err)
		}
	}
}

func TestAScannedFaceListsItsOtherFamilyNames(t *testing.T) {
	fp := scanOne(t, renderFont(t, "Quicksand-VF.ttf"))
	if fp.Family != "quicksand" || !slices.Equal(fp.Aliases, []string{"quicksandlight"}) {
		t.Errorf("family %q aliases %q, want quicksand known also as quicksandlight", fp.Family, fp.Aliases)
	}
}
