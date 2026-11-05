package fontscan

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// recursively walk through the given directory, scanning font files and calling dst.consume
// for each valid file found. A symbolic link to a directory is followed once per target, as
// fontconfig does, since a font tree assembled from links (a Nix profile) holds its fonts behind them.
func (dst *footprintScanner) scanDirectory(ctx context.Context, logger Logger, dir string, visited map[string]bool) error {
	var walk func(root string) error
	walkFn := func(path string, d fs.DirEntry, err error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err != nil {
			logger.Printf("error walking font directory %q: %v", path, err)
			return filepath.SkipDir
		}

		if d.IsDir() { // keep going
			dst.dirs = append(dst.dirs, stampedPath{path: path, stamp: settledStamp(path, dst.now)})
			return nil
		}

		if visited[path] {
			return nil // skip the path
		}
		visited[path] = true

		// load the information, following potential symoblic links
		var st unix.Stat_t
		if err := unix.Stat(path, &st); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				logger.Printf("skipping dead link or missing file: %q", path)
				dst.dirs = append(dst.dirs, stampedPath{path: path, stamp: absentStamp})
				return nil
			}
			return fmt.Errorf("failed to stat %q: %w", path, err)
		}

		mode := st.Mode & unix.S_IFMT
		if mode == unix.S_IFDIR {
			target, err := filepath.EvalSymlinks(path)
			if err != nil {
				logger.Printf("skipping unresolvable font directory link %q: %v", path, err)
				return nil
			}
			return walk(target)
		}

		// always ignore files which should never be font files
		if mode != unix.S_IFREG || ignoreFontFile(d.Name()) {
			return nil
		}

		if err := dst.consume(path, stampOf(&st)); err != nil {
			logger.Printf("skipping unreadable font file %q: %v", path, err)
			dst.dirs = append(dst.dirs, stampedPath{path: path, stamp: racyStamp})
		}
		return nil
	}
	walk = func(root string) error {
		if visited[root] {
			return nil
		}
		visited[root] = true
		return filepath.WalkDir(root, walkFn)
	}

	target, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return fmt.Errorf("resolving font directory %q: %w", dir, err)
	}
	return walk(target)
}

type dirEntry = fs.DirEntry

func readDir(name string) ([]dirEntry, error) {
	return os.ReadDir(name)
}
