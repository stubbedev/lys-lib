package icons

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

const (
	direntReclenAt = 16
	direntTypeAt   = 18
	direntNameAt   = 19
)

type listedEntry struct {
	name string
	typ  fs.FileMode
	dir  string
}

func (e listedEntry) Name() string               { return e.name }
func (e listedEntry) IsDir() bool                { return e.typ.IsDir() }
func (e listedEntry) Type() fs.FileMode          { return e.typ }
func (e listedEntry) Info() (fs.FileInfo, error) { return os.Lstat(filepath.Join(e.dir, e.name)) }

func listDir(dir string) ([]fs.DirEntry, error) {
	fd, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: dir, Err: err}
	}
	defer func() { _ = unix.Close(fd) }()
	buf := make([]byte, 32<<10)
	var out []fs.DirEntry
	for {
		n, err := unix.ReadDirent(fd, buf)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return out, &fs.PathError{Op: "getdents", Path: dir, Err: err}
		}
		if n == 0 {
			return out, nil
		}
		if out, err = appendDirents(out, dir, buf[:n]); err != nil {
			return out, err
		}
	}
}

func appendDirents(out []fs.DirEntry, dir string, buf []byte) ([]fs.DirEntry, error) {
	for len(buf) > 0 {
		if len(buf) < direntNameAt {
			return out, fmt.Errorf("icons: list %s: a %d byte directory record", dir, len(buf))
		}
		reclen := int(binary.NativeEndian.Uint16(buf[direntReclenAt:]))
		if reclen < direntNameAt || reclen > len(buf) {
			return out, fmt.Errorf("icons: list %s: a directory record of %d bytes in %d", dir, reclen, len(buf))
		}
		raw := buf[direntNameAt:reclen]
		kind := buf[direntTypeAt]
		buf = buf[reclen:]
		name := unix.ByteSliceToString(raw)
		if name == "." || name == ".." {
			continue
		}
		typ, ok := direntMode(kind)
		if !ok {
			info, err := os.Lstat(filepath.Join(dir, name))
			if err != nil {
				continue
			}
			typ = info.Mode().Type()
		}
		out = append(out, listedEntry{name: name, typ: typ, dir: dir})
	}
	return out, nil
}

func direntMode(kind byte) (fs.FileMode, bool) {
	switch kind {
	case unix.DT_REG:
		return 0, true
	case unix.DT_DIR:
		return fs.ModeDir, true
	case unix.DT_LNK:
		return fs.ModeSymlink, true
	case unix.DT_FIFO:
		return fs.ModeNamedPipe, true
	case unix.DT_SOCK:
		return fs.ModeSocket, true
	case unix.DT_CHR:
		return fs.ModeDevice | fs.ModeCharDevice, true
	case unix.DT_BLK:
		return fs.ModeDevice, true
	}
	return 0, false
}
