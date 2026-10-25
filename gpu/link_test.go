package gpu

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestOneFakecgo pins what lets an application link lys-ui beside its
// own native loading: cgo-free, a program links one runtime shim
// (fakecgo), and two (goffi's beside purego's) fail at link time with
// a duplicated _cgo_init. Built without cgo, every lys-ui package's
// dependencies carry purego's fakecgo and no other.
func TestOneFakecgo(t *testing.T) {
	cmd := exec.Command("go", "list", "-deps", "github.com/stubbedev/lys-ui/...")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list -deps: %v", err)
	}
	var shims []string
	for pkg := range strings.FieldsSeq(string(out)) {
		if strings.HasSuffix(pkg, "/fakecgo") {
			shims = append(shims, pkg)
		}
	}
	if len(shims) != 1 || shims[0] != "github.com/ebitengine/purego/internal/fakecgo" {
		t.Errorf("runtime shims %v, want only purego's", shims)
	}
}
