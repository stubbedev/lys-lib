package protocols

import (
	"encoding/json"
	"os"
	"path"
	"testing"
)

func TestEverySourcedFileIsBundled(t *testing.T) {
	data, err := os.ReadFile("sources.json")
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Sources []struct{ Name string } `json:"sources"`
		Files   []struct {
			Source string `json:"source"`
			Path   string `json:"path"`
		} `json:"files"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	known := map[string]bool{}
	for _, s := range manifest.Sources {
		known[s.Name] = true
	}
	if len(manifest.Files) == 0 {
		t.Fatal("sources.json lists no files")
	}
	for _, f := range manifest.Files {
		if !known[f.Source] {
			t.Errorf("%s names unknown source %q", f.Path, f.Source)
		}
		if _, err := FS.ReadFile(path.Base(f.Path)); err != nil {
			t.Errorf("%s is not bundled: %v", f.Path, err)
		}
	}
}
