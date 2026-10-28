package atomicfile

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteFileReplacesContentAndMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(path, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "new" {
		t.Fatalf("read %q, %v", got, err)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %v, want 0600", fi.Mode().Perm())
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Errorf("%d entries, want only the target", len(entries))
	}
}

func TestWriteStreamsTheProducersOutput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f")
	err := Write(path, 0o640, func(w io.Writer) error {
		for _, s := range []string{"a", "b", "c"} {
			if _, err := io.WriteString(w, s); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(path); string(got) != "abc" {
		t.Errorf("read %q, want abc", got)
	}
}

func TestFailedWriteLeavesTheTargetAndNoTemporary(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f")
	if err := WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	boom := errors.New("boom")
	err := Write(path, 0o600, func(w io.Writer) error {
		_, _ = w.Write([]byte("partial"))
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("write returned %v, want the producer's error", err)
	}
	if got, _ := os.ReadFile(path); string(got) != "old" {
		t.Errorf("target became %q", got)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Errorf("%d entries left in the directory, want only the target", len(entries))
	}
}

func TestMissingDirectoryIsReported(t *testing.T) {
	dir := t.TempDir()
	if err := WriteFile(filepath.Join(dir, "absent", "f"), nil, 0o600); err == nil {
		t.Error("writing into a missing directory succeeded")
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("%d entries created, want none", len(entries))
	}
}

func TestDirectoryIsSyncedAfterTheRename(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root opens a directory without read permission")
	}
	dir := filepath.Join(t.TempDir(), "d")
	if err := os.Mkdir(dir, 0o300); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	path := filepath.Join(dir, "f")
	err := WriteFile(path, []byte("new"), 0o600)
	if !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("write into an unreadable directory returned %v, want the directory sync's error", err)
	}
	if got, _ := os.ReadFile(path); string(got) != "new" {
		t.Errorf("target is %q, want the renamed content", got)
	}
}
