package keymapfd

import (
	"os"
	"testing"
)

// Two readers of one shared fd both get the whole keymap: mapping
// leaves the shared offset alone (a read() would have moved it to EOF
// for the second).
func TestSharedFDReadsWholeTwice(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "keymap")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	text := "xkb_keymap { };"
	if _, err := f.WriteString(text + "\x00"); err != nil {
		t.Fatal(err)
	}
	for i := range 2 {
		got, err := Read(f.Fd(), uint32(len(text)+1))
		if err != nil || string(got) != text {
			t.Fatalf("reader %d: %q, %v", i, got, err)
		}
	}
	if _, err := Read(f.Fd(), 0); err == nil {
		t.Error("an empty keymap read")
	}
}
