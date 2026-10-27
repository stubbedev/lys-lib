package render

import (
	"image"
	"runtime"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// A free waits for every List open when it was retired, runs once the
// last of them ends, End or ResetRecorder, and never waits for a List
// opened after it.
func TestRetireWaitsForTheListsOpenBeforeIt(t *testing.T) {
	var f fence
	a, b := f.newRecorder(4, 4, 1, 1), f.newRecorder(4, 4, 1, 1)
	var ran []string
	f.retire(func() { ran = append(ran, "first") })
	later := f.newRecorder(4, 4, 1, 1)
	f.retire(func() { ran = append(ran, "second") })
	if len(ran) != 0 {
		t.Fatalf("ran %v with the Lists open", ran)
	}
	a.Recording().End()
	if len(ran) != 0 {
		t.Fatalf("ran %v with a List open before them", ran)
	}
	b.ResetRecorder(4, 4, 1, 1)
	if want := []string{"first"}; !slices.Equal(ran, want) {
		t.Fatalf("ran %v once the first's Lists ended, want %v", ran, want)
	}
	later.Recording().End()
	b.Recording().End()
	if want := []string{"first", "second"}; !slices.Equal(ran, want) {
		t.Errorf("ran %v once every List ended, want %v", ran, want)
	}
	f.retire(func() { ran = append(ran, "now") })
	if ran[len(ran)-1] != "now" {
		t.Error("a free retired with no List open did not run at once")
	}
}

// Ending an ended List does nothing, and recording into or playing an
// ended List panics: the rasters it held may be freed.
func TestEndedListRefusesUse(t *testing.T) {
	var f fence
	rec := f.newRecorder(4, 4, 1, 1)
	img := NewStillImage(image.NewRGBA(image.Rect(0, 0, 1, 1)))
	rec.DrawImageDevice(img, 0, 0)
	l := rec.Recording()
	l.End()
	l.End()
	for name, use := range map[string]func(){
		"play":   func() { l.Paint(New(make([]byte, Stride(4)*4), Stride(4), 4, 4)) },
		"record": func() { rec.DrawImageDevice(img, 0, 0) },
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%s of an ended List did not panic", name)
				}
			}()
			use()
		}()
	}
	rec.ResetRecorder(4, 4, 1, 1)
	rec.DrawImageDevice(img, 0, 0)
	if rec.Recording().Len() != 1 {
		t.Error("a reset List did not record again")
	}
}

// A List dropped without End stops holding frees back once collected.
func TestRetireRunsAfterADroppedList(t *testing.T) {
	var f fence
	f.newRecorder(4, 4, 1, 1)
	var freed atomic.Bool
	f.retire(func() { freed.Store(true) })
	deadline := time.Now().Add(10 * time.Second)
	for !freed.Load() {
		if time.Now().After(deadline) {
			t.Fatal("a dropped List held its free back")
		}
		runtime.GC()
		time.Sleep(time.Millisecond)
	}
}

// Lists recorded and ended on several goroutines while others retire:
// every free runs once, never while a List open before it is.
func TestRetireAcrossGoroutines(t *testing.T) {
	var f fence
	var wg sync.WaitGroup
	var early, runs atomic.Int64
	for range 8 {
		wg.Go(func() {
			rec := f.newRecorder(4, 4, 1, 1)
			for range 200 {
				var ended atomic.Bool
				f.retire(func() {
					if !ended.Load() {
						early.Add(1)
					}
					runs.Add(1)
				})
				ended.Store(true)
				rec.ResetRecorder(4, 4, 1, 1)
			}
			rec.Recording().End()
		})
	}
	wg.Wait()
	if early.Load() != 0 || runs.Load() != 8*200 {
		t.Errorf("%d of %d frees ran, %d before their List ended", runs.Load(), 8*200, early.Load())
	}
}

// A raster has an identity of its own: a mask's is fixed at first use,
// a still image's sub-images share it, and no two rasters share one.
func TestRastersHaveDistinctIdentities(t *testing.T) {
	var a, b GlyphMask
	first := a.ID()
	if first == 0 || a.ID() != first || b.ID() == first {
		t.Errorf("mask identities %d then %d, other %d", first, a.ID(), b.ID())
	}
	pix := image.NewRGBA(image.Rect(0, 0, 4, 4))
	s, again := NewStillImage(pix), NewStillImage(pix)
	sub, ok := s.SubImage(image.Rect(1, 1, 3, 3)).(StillImage)
	if !ok || sub.ID != s.ID || s.ID == again.ID || s.ID == 0 {
		t.Errorf("still identities %d, sub %d, rewrapped %d", s.ID, sub.ID, again.ID)
	}
	if Still(pix).ID == 0 {
		t.Error("Still made a copy without an identity")
	}
}
