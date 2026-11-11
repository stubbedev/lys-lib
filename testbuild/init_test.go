package testbuild

import (
	"errors"
	"strings"
	"testing"
)

func TestPackageOfATestFunction(t *testing.T) {
	for function, want := range map[string]string{
		"github.com/stubbedev/lys-lib/media/opus.TestImportingCostsNothing":      "github.com/stubbedev/lys-lib/media/opus",
		"github.com/stubbedev/lys-lib/dbusx_test.TestImportingCostsNothing":      "github.com/stubbedev/lys-lib/dbusx",
		"github.com/stubbedev/lys-lib/third_party/wayland/wl.TestX.func1":        "github.com/stubbedev/lys-lib/third_party/wayland/wl",
		"gopkg.in/yaml%2ev3.TestImportingCostsNothing":                           "gopkg.in/yaml.v3",
		"github.com/stubbedev/lys-lib/third_party/gomath.(*T).TestMethod":        "github.com/stubbedev/lys-lib/third_party/gomath",
		"example.com/x/unit_test_test.TestImportingCostsNothing":                 "example.com/x/unit_test",
		"github.com/stubbedev/lys-lib/pulse/native.TestImportingCostsNothing":    "github.com/stubbedev/lys-lib/pulse/native",
		"github.com/stubbedev/lys-lib/xkb_test.TestImportingCostsNothing.func1":  "github.com/stubbedev/lys-lib/xkb",
		"github.com/stubbedev/lys-lib/testbuild.TestThisPackageCostsNothingToo":  "github.com/stubbedev/lys-lib/testbuild",
		"github.com/stubbedev/lys-lib/media/internal/pcm.TestImportingCostsNone": "github.com/stubbedev/lys-lib/media/internal/pcm",
	} {
		if got := packageOf(function); got != want {
			t.Errorf("packageOf(%q) = %q, want %q", function, got, want)
		}
	}
}

func TestInitOfReadsOnePackagesLineOfATrace(t *testing.T) {
	trace := []byte(`init internal/bytealg @0.008 ms, 0 ms clock, 0 bytes, 0 allocs
init runtime @0.059 ms, 0.026 ms clock, 0 bytes, 0 allocs
init example.com/a/render @1.2 ms, 0.076 ms clock, 26264 bytes, 301 allocs
init example.com/a/render/internal @1.3 ms, 0 ms clock, 0 bytes, 0 allocs
PASS
`)
	line, err := initOf(trace, "example.com/a/render")
	if err != nil || line != "init example.com/a/render @1.2 ms, 0.076 ms clock, 26264 bytes, 301 allocs" {
		t.Errorf("initOf = %q, %v; want the package's own line", line, err)
	}
	for _, pkg := range []string{"example.com/a", "example.com/a/rend", "example.com/a/render/internal/x", "render"} {
		if line, err := initOf(trace, pkg); err != nil || line != "" {
			t.Errorf("initOf(%q) = %q, %v; want none: the trace lists no such package", pkg, line, err)
		}
	}
	var none *noInitTraceError
	if _, err := initOf([]byte("PASS\n"), "example.com/a"); !errors.As(err, &none) || !strings.Contains(err.Error(), "PASS") {
		t.Errorf("initOf of output without a trace = %v, want it refused with the output", err)
	}
}

func TestThisPackageCostsNothingToo(t *testing.T) { RequireNoInit(t) }
