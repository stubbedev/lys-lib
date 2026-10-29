package font

import (
	"bytes"
	"os"
	"runtime"
	"testing"
)

const lysFaceHeapBudget = 1.6

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

func TestFaceHeapIsAboutItsFileSize(t *testing.T) {
	data := lysDejaVuSans(t)
	const faces = 4
	held := make([]*Face, faces)

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	for i := range held {
		face, err := ParseTTF(bytes.NewReader(data))
		if err != nil {
			t.Fatalf("parse DejaVu Sans: %v", err)
		}
		held[i] = face
	}
	runtime.GC()
	runtime.ReadMemStats(&after)
	runtime.KeepAlive(data)
	runtime.KeepAlive(held)

	perFace := float64(int64(after.HeapAlloc)-int64(before.HeapAlloc)) / faces
	ratio := perFace / float64(len(data))
	t.Logf("%.0f B of heap per face for a %d B file (%.2fx)", perFace, len(data), ratio)
	if ratio > lysFaceHeapBudget {
		t.Errorf("a face holds %.2fx its file in heap, over the %.2fx budget", ratio, lysFaceHeapBudget)
	}
}
