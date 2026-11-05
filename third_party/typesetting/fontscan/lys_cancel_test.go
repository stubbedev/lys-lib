package fontscan

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

type cancelAfter struct {
	context.Context
	entries atomic.Int32
	limit   int32
}

func (c *cancelAfter) Err() error {
	if c.entries.Add(1) > c.limit {
		return context.Canceled
	}
	return nil
}

func writeNonFonts(t *testing.T, dir string, n int) {
	t.Helper()
	for i := range n {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("f%02d.ttf", i)), []byte("not a font"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAScanStopsBetweenEntriesOnceItsContextIsDone(t *testing.T) {
	dir := t.TempDir()
	writeNonFonts(t, dir, 20)
	ctx := &cancelAfter{Context: context.Background(), limit: 5}
	scan, err := scanFontFootprints(ctx, discardLogger{}, nil, time.Now(), dir)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("a scan cancelled mid-walk returned %v, want context.Canceled", err)
	}
	if len(scan.index) != 0 {
		t.Errorf("a cancelled scan returned %d files", len(scan.index))
	}
	if got := ctx.entries.Load(); got != ctx.limit+1 {
		t.Errorf("the walk asked the context %d times, want it to stop at the first refusal (%d)", got, ctx.limit+1)
	}
}

func TestAScanWithALiveContextReadsEveryEntry(t *testing.T) {
	dir := t.TempDir()
	writeNonFonts(t, dir, 20)
	scan, err := scanFontFootprints(context.Background(), discardLogger{}, nil, time.Now(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(scan.index) != 20 {
		t.Errorf("a full scan found %d files, want 20", len(scan.index))
	}
}

func resetSystemFonts(t *testing.T) {
	t.Helper()
	reset := func() {
		systemFontsMu.Lock()
		systemFonts, systemFontsErr = nil, nil
		systemFontsMu.Unlock()
	}
	reset()
	t.Cleanup(reset)
}

func TestACancelledSystemScanLeavesTheNextCallerToScan(t *testing.T) {
	ft := newFontTree(t)
	resetSystemFonts(t)
	cache := filepath.Dir(ft.index)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := SystemFonts(cancelled, discardLogger{}, cache); !errors.Is(err, context.Canceled) {
		t.Fatalf("a cancelled scan returned %v, want context.Canceled", err)
	}
	fonts, err := SystemFonts(context.Background(), discardLogger{}, cache)
	if err != nil {
		t.Fatalf("the scan after a cancelled one failed: %v", err)
	}
	if len(fonts) != 1 || filepath.Base(fonts[0].Location.File) != "a.ttf" {
		t.Errorf("the scan after a cancelled one found %v, want a.ttf", fonts)
	}
}

func TestAFailedSystemScanReportsItsErrorToEveryCaller(t *testing.T) {
	newFontTree(t)
	resetSystemFonts(t)
	t.Setenv("FONTCONFIG_FILE", filepath.Join(t.TempDir(), "absent.conf"))
	_, first := SystemFonts(context.Background(), discardLogger{}, t.TempDir())
	if first == nil {
		t.Fatal("a scan with no font directory succeeded")
	}
	if _, second := SystemFonts(context.Background(), discardLogger{}, t.TempDir()); !errors.Is(second, first) {
		t.Errorf("the second caller got %v, want the first scan's error %v", second, first)
	}
}
