// Package assetfile opens the files assets load from: icons, images,
// stylesheets and fonts. It opens without blocking and checks the file it
// opened, so a named pipe, a device or a socket at an asset's path is an
// error rather than a read that never returns, and it bounds what it reads.
package assetfile

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"

	"golang.org/x/sys/unix"
)

// ErrNotRegular is a path that names something other than a regular file.
var ErrNotRegular = errors.New("not a regular file")

// ErrTooLarge is a file over the limit its asset kind allows.
var ErrTooLarge = errors.New("over the size limit")

// Open opens path for reading without blocking on it, and returns the
// file with its size once fstat on that same descriptor shows a regular
// file of at most limit bytes. The caller closes the file.
func Open(path string, limit int64) (*os.File, int64, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, 0, &fs.PathError{Op: "open", Path: path, Err: err}
	}
	f := os.NewFile(uintptr(fd), path)
	info, err := f.Stat()
	if err != nil {
		return nil, 0, errors.Join(err, f.Close())
	}
	if !info.Mode().IsRegular() {
		return nil, 0, errors.Join(fmt.Errorf("assetfile: %s is %s: %w", path, kindOf(info.Mode()), ErrNotRegular), f.Close())
	}
	if info.Size() > limit {
		return nil, 0, errors.Join(fmt.Errorf("assetfile: %s is %d bytes, %w of %d", path, info.Size(), ErrTooLarge, limit), f.Close())
	}
	return f, info.Size(), nil
}

// Read is the contents of the regular file at path, at most limit bytes,
// opened as Open opens it.
func Read(path string, limit int64) ([]byte, error) {
	f, size, err := Open(path, limit)
	if err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return nil, fmt.Errorf("assetfile: read %s: %w", path, err)
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("assetfile: %s grew past %d bytes while read (from %d): %w", path, limit, size, ErrTooLarge)
	}
	return data, nil
}

func kindOf(m fs.FileMode) string {
	switch {
	case m.IsDir():
		return "a directory"
	case m&fs.ModeNamedPipe != 0:
		return "a named pipe"
	case m&fs.ModeSocket != 0:
		return "a socket"
	case m&fs.ModeCharDevice != 0:
		return "a character device"
	case m&fs.ModeDevice != 0:
		return "a block device"
	}
	return "an irregular file (" + m.Type().String() + ")"
}
