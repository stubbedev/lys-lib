package font

import (
	"os"
	"testing"
)

const lysQuicksandVF = "../../../render/testdata/Quicksand-VF.ttf"

func lysDejaVuSans(t testing.TB) []byte {
	t.Helper()
	path := os.Getenv("LYS_TEST_DEJAVU_SANS")
	if path == "" {
		t.Skip("LYS_TEST_DEJAVU_SANS unset: the dev shell points it at DejaVu Sans")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read DejaVu Sans: %v", err)
	}
	return data
}

func lysQuicksand(t testing.TB) []byte {
	t.Helper()
	data, err := os.ReadFile(lysQuicksandVF)
	if err != nil {
		t.Fatalf("read Quicksand: %v", err)
	}
	return data
}
