package xkb

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

var cFunction = regexp.MustCompile(`(?m)^(r?xkb_[a-z0-9_]+)\(|#define (xkb_keycode_is_legal_[a-z0-9]+)\(`)

// TestCoverageTableListsEveryFunction fails when libxkbcommon's headers declare
// a function the published coverage table does not map to Go.
func TestCoverageTableListsEveryFunction(t *testing.T) {
	keysyms := os.Getenv("LYS_XKB_KEYSYMS_H")
	if keysyms == "" {
		t.Fatal("LYS_XKB_KEYSYMS_H is not set: the dev shell points it at libxkbcommon's headers")
	}
	table, err := os.ReadFile(filepath.Join("..", "docs", "xkb.md"))
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(keysyms)
	for _, header := range []string{"xkbcommon.h", "xkbcommon-compose.h", "xkbcommon-x11.h", "xkbregistry.h"} {
		src, err := os.ReadFile(filepath.Join(dir, header))
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range cFunction.FindAllSubmatch(src, -1) {
			name := string(m[1]) + string(m[2])
			if !regexp.MustCompile("`" + name + "`").Match(table) {
				t.Errorf("%s from %s is not in docs/xkb.md", name, header)
			}
		}
	}
}
