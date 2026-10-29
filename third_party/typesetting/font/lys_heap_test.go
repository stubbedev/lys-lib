//go:build !race

package font

import (
	"bytes"
	"runtime"
	"testing"
)

func TestFaceHeapIsAboutItsFileSize(t *testing.T) {
	for _, tc := range []struct {
		name   string
		font   func(testing.TB) []byte
		budget float64
	}{
		{"static DejaVu Sans", lysDejaVuSans, 1.45},
		{"variable Quicksand", lysQuicksand, 2.2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := tc.font(t)
			const faces = 16
			held := make([]*Face, faces)

			var before, after runtime.MemStats
			runtime.GC()
			runtime.ReadMemStats(&before)
			for i := range held {
				face, err := ParseTTF(bytes.NewReader(data))
				if err != nil {
					t.Fatalf("parse %s: %v", tc.name, err)
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
			if ratio > tc.budget {
				t.Errorf("a face holds %.2fx its file in heap, over the %.2fx budget", ratio, tc.budget)
			}
		})
	}
}
