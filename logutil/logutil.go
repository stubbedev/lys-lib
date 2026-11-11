// Package logutil holds the lys libraries' single injectable logger. Library code
// is silent by default: failures come back as returned errors, and the
// events that are not failures — event-loop diagnostics, protocol
// warnings, protocol chatter — emit through the logger installed here,
// which starts as a discarding sink. An embedding application owns its
// logging and installs its *slog.Logger once at startup
// (Set, or lys-ui's app.SetLogger); passing nil
// restores the discarding default.
//
// # Levels
//
// Debug — protocol chatter: optional globals the compositor does not
// advertise, capability changes, sub-object binds. Opt-in by level.
//
// Info — unused. The library logs nothing per-frame or per-keypress,
// and nothing at Info at all.
//
// Warn — degraded but running: an advertised optional protocol failed
// to bind and its feature silently disappears, a theme's contrast sits
// below WCAG AA. The session continues; the app decides whether it
// cares.
//
// Error — terminal conditions only: the compositor raised a fatal
// protocol error against this client.
//
// The lysdebug build-tag trace facility (package debug) is a
// different concern — compile-out traces, not runtime logging — and
// stays independent of this package.
package logutil

import (
	"log/slog"
	"sync"
)

var (
	mu          sync.RWMutex
	logger      *slog.Logger
	discardOnce sync.Once
	discarding  *slog.Logger
)

// discard is the default sink: a logger that drops everything, made
// when a logger is first asked for while none is set.
func discard() *slog.Logger {
	discardOnce.Do(func() { discarding = slog.New(slog.DiscardHandler) })
	return discarding
}

// Set installs l as the library-wide logger. A nil l selects the
// discarding sink — the default, so "no logger configured" and
// "logger unset" mean the same thing. Safe from any goroutine.
func Set(l *slog.Logger) {
	mu.Lock()
	defer mu.Unlock()
	logger = l
}

// L returns the active logger, never nil, so every emit site can log
// without a nil check. With the default configuration every record is
// discarded. Safe from any goroutine.
func L() *slog.Logger {
	mu.RLock()
	defer mu.RUnlock()
	if logger == nil {
		return discard()
	}
	return logger
}
