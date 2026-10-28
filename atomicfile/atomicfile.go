// Package atomicfile replaces files whole and durably: the content goes
// to a uniquely named temporary file beside the target, which is synced
// and renamed over it, and then the directory is synced so the rename
// survives a crash. A reader sees the old file or the new one and never
// a partial write, and concurrent writers never share a temporary.
package atomicfile

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// Write replaces path with what write produces, with permissions perm.
// On a failure before the rename path is untouched and the temporary is
// removed; a failure to sync the directory after it is reported with
// path already replaced.
func Write(path string, perm os.FileMode, write func(io.Writer) error) error {
	tmp, err := writeTemp(path, perm, write)
	if err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("atomicfile: replace %s: %w", path, err)
	}
	if err := syncDir(filepath.Dir(path)); err != nil {
		return fmt.Errorf("atomicfile: sync the directory of %s: %w", path, err)
	}
	return nil
}

func writeTemp(path string, perm os.FileMode, write func(io.Writer) error) (string, error) {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp*")
	if err != nil {
		return "", fmt.Errorf("atomicfile: create a temporary beside %s: %w", path, err)
	}
	buf := bufio.NewWriter(f)
	err = write(buf)
	if err == nil {
		err = buf.Flush()
	}
	if err == nil {
		err = f.Chmod(perm)
	}
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(f.Name())
		return "", fmt.Errorf("atomicfile: write %s: %w", path, err)
	}
	return f.Name(), nil
}

// WriteFile replaces path with data, as os.WriteFile but atomically.
func WriteFile(path string, data []byte, perm os.FileMode) error {
	return Write(path, perm, func(w io.Writer) error {
		_, err := w.Write(data)
		return err
	})
}

func syncDir(dir string) error {
	d, err := os.Open(dir) //nolint:gosec // the directory of the caller's own target
	if err != nil {
		return err
	}
	err = d.Sync()
	if closeErr := d.Close(); err == nil {
		err = closeErr
	}
	return err
}
