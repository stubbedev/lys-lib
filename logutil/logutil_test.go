package logutil

import (
	"bytes"
	"log/slog"
	"strings"
	"sync"
	"testing"
)

func TestDefaultIsDiscard(t *testing.T) {
	if L() == nil {
		t.Fatal("L() = nil, want a non-nil discarding logger")
	}
	// The default sink must be enabled nowhere: emitting through it
	// writes nothing anywhere, which is what keeps the library silent
	// with no configuration.
	if L().Handler().Enabled(t.Context(), slog.LevelError) {
		t.Error("default logger enabled at Error level; want discard")
	}
}

func TestSetNilRestoresDiscard(t *testing.T) {
	t.Cleanup(func() { Set(nil) })
	Set(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	Set(nil)
	if L().Handler().Enabled(t.Context(), slog.LevelDebug) {
		t.Error("Set(nil) must restore the discarding default")
	}
}

func TestSetInstallsLogger(t *testing.T) {
	t.Cleanup(func() { Set(nil) })
	var buf lockedBuffer
	Set(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	L().Warn("degraded", slog.String("global", "xdg_activation_v1"))
	if !strings.Contains(buf.String(), `level=WARN`) || !strings.Contains(buf.String(), `global=xdg_activation_v1`) {
		t.Errorf("injected logger lost the record: %q", buf.String())
	}
}

// lockedBuffer is safe against the handler's internal locking being
// insufficient when Set races with a log from another goroutine.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestConcurrentSetAndLog(t *testing.T) {
	t.Cleanup(func() { Set(nil) })
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for range 200 {
				L().Debug("chatter", slog.Int("i", i))
			}
		}()
		go func() {
			defer wg.Done()
			for range 200 {
				Set(nil)
			}
		}()
	}
	wg.Wait()
}
