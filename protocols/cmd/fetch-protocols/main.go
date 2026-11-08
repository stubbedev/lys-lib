// Command fetch-protocols downloads the upstream protocol XML pinned in
// protocols/sources.json into the protocols directory.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/stubbedev/lys-lib/atomicfile"
)

const maxDownload = 4 << 20

type source struct {
	Name string `json:"name"`
	URL  string `json:"url"`
	Ref  string `json:"ref"`
}

type file struct {
	Source string `json:"source"`
	Path   string `json:"path"`
}

type manifest struct {
	Sources []source `json:"sources"`
	Files   []file   `json:"files"`
}

func main() {
	dir := flag.String("dir", "protocols", "directory holding sources.json and the XML")
	flag.Parse()
	if err := run(*dir); err != nil {
		fmt.Fprintln(os.Stderr, "fetch-protocols:", err)
		os.Exit(1)
	}
}

func run(dir string) error {
	data, err := os.ReadFile(filepath.Join(dir, "sources.json"))
	if err != nil {
		return fmt.Errorf("read the manifest: %w", err)
	}
	var m manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return fmt.Errorf("parse the manifest: %w", err)
	}
	sources := make(map[string]source, len(m.Sources))
	for _, s := range m.Sources {
		sources[s.Name] = s
	}
	client := &http.Client{Timeout: 30 * time.Second}
	for _, f := range m.Files {
		s, ok := sources[f.Source]
		if !ok {
			return fmt.Errorf("%s names unknown source %q", f.Path, f.Source)
		}
		url := strings.NewReplacer("{ref}", s.Ref, "{path}", f.Path).Replace(s.URL)
		body, err := download(client, url)
		if err != nil {
			return err
		}
		if err := atomicfile.WriteFile(filepath.Join(dir, path.Base(f.Path)), body, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", f.Path, err)
		}
	}
	return nil
}

func download(client *http.Client, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build request for %s: %w", url, err)
	}
	resp, err := client.Do(req) //nolint:gosec // the URLs are the pinned sources.json entries
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch %s: %s", url, resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxDownload+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", url, err)
	}
	if len(data) > maxDownload {
		return nil, fmt.Errorf("read %s: larger than %d bytes", url, maxDownload)
	}
	return data, nil
}
