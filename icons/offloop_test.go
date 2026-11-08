package icons

import (
	"image"
	"io/fs"
	"sync"
	"testing"

	"github.com/stubbedev/lys-lib/render"
)

type heldFS struct {
	countingFS
	once             sync.Once
	entered, release chan struct{}
}

func (f *heldFS) ReadDir(name string) ([]fs.DirEntry, error) {
	f.once.Do(func() {
		close(f.entered)
		<-f.release
	})
	return f.countingFS.ReadDir(name)
}

func TestGenerationNeverWaitsForTheLock(t *testing.T) {
	c := New("hicolor")
	before := c.Generation()
	c.mu.Lock()
	got := c.Generation()
	c.mu.Unlock()
	if got != before {
		t.Errorf("generation %d, want %d", got, before)
	}
	c.InvalidateTheme()
	if c.Generation() != before+1 {
		t.Errorf("a reset moved the generation to %d, want %d", c.Generation(), before+1)
	}
}

func TestIndexBuildsOutsideTheLock(t *testing.T) {
	paths := equivTree(t)
	c := New("testtheme")
	c.SetSearchPaths(paths)
	fsys := &heldFS{entered: make(chan struct{}), release: make(chan struct{})}
	c.fsys = fsys
	done := make(chan struct{})
	go func() {
		defer close(done)
		c.Preload("flag", 24, 120)
	}()
	<-fsys.entered
	if !c.mu.TryLock() {
		t.Fatal("the lock is held across the index build: a paint would wait for the disk")
	}
	c.mu.Unlock()
	if _, ok := c.Peek("flag", 24, 120); ok {
		t.Error("decoded before its index was built")
	}
	close(fsys.release)
	<-done
	if r, ok := c.Peek("flag", 24, 120); !ok || r.Err != nil || r.Icon == nil {
		t.Errorf("after the preload: raster %+v, found %v", r, ok)
	}
}

func TestPaintPathReadsNothing(t *testing.T) {
	paths := equivTree(t)
	c := New("testtheme")
	c.SetSearchPaths(paths)
	fsys := &countingFS{}
	c.fsys = fsys
	for range 3 {
		if _, ok := c.Peek("flag", 24, 120); ok {
			t.Fatal("nothing has decoded it")
		}
		if _, ok := c.PeekFallback("flag-extra-long"); ok {
			t.Fatal("nothing has resolved it")
		}
	}
	if n := fsys.n(); n != 0 {
		t.Fatalf("a miss made %d reads", n)
	}
	name := c.FallbackName("flag-extra-long")
	c.Preload(name, 24, 120)
	reads := fsys.n()
	if reads == 0 {
		t.Fatal("the off-loop load read nothing, so this pins nothing")
	}
	known, ok := c.PeekFallback("flag-extra-long")
	if !ok || known != "flag" {
		t.Fatalf("fallback = %q, %v", known, ok)
	}
	r, ok := c.Peek(known, 24, 120)
	if !ok || r.Err != nil || r.Icon == nil {
		t.Fatalf("peek after the load = %+v, %v", r, ok)
	}
	if c.Tint(r, render.RGB(1, 2, 3)) == nil {
		t.Fatal("no shared tint")
	}
	if got := fsys.n(); got != reads {
		t.Errorf("a hit made %d reads", got-reads)
	}
}

func TestFallbackMemoDoesNotOutliveAReset(t *testing.T) {
	paths := equivTree(t)
	c := New("testtheme")
	c.SetSearchPaths(paths)
	c.FallbackName("flag-extra")
	c.InvalidateTheme()
	if _, ok := c.PeekFallback("flag-extra"); ok {
		t.Error("the answer survived the reset that may have changed it")
	}
}

func TestTintIntoMatchesIconTint(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 3, 2))
	for i := range src.Pix {
		src.Pix[i] = uint8(i * 19)
	}
	ic, err := render.IconFromImage(src, 3, 2)
	if err != nil {
		t.Fatal(err)
	}
	const tint = render.Color(0x80c04020)
	want := ic.Tint(tint).Image()
	dst := image.NewRGBA(want.Rect)
	TintInto(dst, ic.Image(), tint)
	if string(dst.Pix) != string(want.Pix) {
		t.Errorf("TintInto %v, Icon.Tint %v", dst.Pix, want.Pix)
	}
}
