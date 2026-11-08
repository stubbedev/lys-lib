//go:build linux

package os

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

func TestAnAnonymousFileNeedsNoRuntimeDir(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", "")
	file, err := CreateAnonymousFile(4096)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != 4096 {
		t.Fatalf("the file is %d bytes, want 4096", info.Size())
	}
	link, err := os.Readlink(fmt.Sprintf("/proc/self/fd/%d", file.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(link, "/memfd:") {
		t.Fatalf("the file is %q, want a memfd", link)
	}
}
