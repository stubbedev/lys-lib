package gpu

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestShadersAreCurrent pins the embedded SPIR-V to the GLSL beside
// it: a shader edited without go generate ./gpu fails here. It needs
// glslangValidator, which the dev shell (and CI's) provides.
func TestShadersAreCurrent(t *testing.T) {
	if _, err := exec.LookPath("glslangValidator"); err != nil {
		t.Skip("no glslangValidator: run inside the dev shell")
	}
	for name, embedded := range map[string][]byte{"prim.vert": primVert, "prim.frag": primFrag} {
		out := filepath.Join(t.TempDir(), name+".spv")
		cmd := exec.Command("glslangValidator", "-V", "--quiet", "-o", out, filepath.Join("shaders", name))
		if msg, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("compiling %s: %v\n%s", name, err, msg)
		}
		got, err := os.ReadFile(out)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, embedded) {
			t.Errorf("shaders/%s.spv is stale: run go generate ./gpu", name)
		}
	}
}
