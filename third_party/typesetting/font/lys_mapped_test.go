package font

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"unsafe"

	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-lib/third_party/typesetting/font/opentype"
	"github.com/stubbedev/lys-lib/third_party/typesetting/font/opentype/tables"
)

func readOnlyMapping(t *testing.T, content []byte) []byte {
	t.Helper()
	path := filepath.Join(t.TempDir(), "font.ttf")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	data, err := unix.Mmap(int(f.Fd()), 0, len(content), unix.PROT_READ, unix.MAP_PRIVATE)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = unix.Munmap(data) })
	return data
}

func within(outer, inner []byte) bool {
	start := uintptr(unsafe.Pointer(unsafe.SliceData(outer)))
	at := uintptr(unsafe.Pointer(unsafe.SliceData(inner)))
	return at >= start && at+uintptr(len(inner)) <= start+uintptr(len(outer))
}

func TestMappedTablesAreTheFilesBytesNotCopies(t *testing.T) {
	content := lysDejaVuSans(t)
	data := readOnlyMapping(t, content)
	mapped, err := opentype.NewLoader(opentype.NewMapped(data))
	if err != nil {
		t.Fatal(err)
	}
	copied, err := opentype.NewLoader(bytes.NewReader(content))
	if err != nil {
		t.Fatal(err)
	}
	for _, tag := range copied.Tables() {
		want, err := copied.RawTable(tag)
		if err != nil {
			t.Fatal(err)
		}
		got, err := mapped.RawTable(tag)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("table %s differs from the copying loader's", tag)
		}
		if !within(data, got) {
			t.Errorf("table %s is a copy, not the mapping", tag)
		}
		if cap(got) != len(got) {
			t.Errorf("table %s has capacity %d past its %d bytes: an append would write to the file", tag, cap(got), len(got))
		}
	}
	dst := make([]byte, 0, 1<<20)
	tag := copied.Tables()[0]
	got, err := mapped.RawTableTo(tag, dst)
	if err != nil {
		t.Fatal(err)
	}
	if within(data, got) {
		t.Errorf("table %s ignored the buffer the caller asked for", tag)
	}
}

func TestAFaceOnAReadOnlyMappingNeverWritesToIt(t *testing.T) {
	face, err := ParseTTF(opentype.NewMapped(readOnlyMapping(t, lysDejaVuSans(t))))
	if err != nil {
		t.Fatal(err)
	}
	for gid := range tables.GlyphID(face.glyf.Len()) {
		if _, err := face.glyf.Glyph(gid); err != nil {
			t.Fatalf("glyph %d: %v", gid, err)
		}
	}
	if face.Upem() == 0 {
		t.Error("a face without metrics")
	}
}

func TestMappedRejectsATablePastTheFile(t *testing.T) {
	content := lysDejaVuSans(t)
	if _, err := ParseTTF(opentype.NewMapped(content[:len(content)/2])); err == nil {
		t.Error("a truncated font parsed, reading tables past its end")
	}
}
