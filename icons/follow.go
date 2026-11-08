// Icon-theme following (#64): the cache applies live icon-theme
// switches delivered by the portal monitor (portalsettings), which
// the application wires in. This is a lookup refresh, never a palette
// swap — the "toolkit never flips its own theme" rule is untouched
// (docs/architecture.md "Non-goals"); what changes is which theme's
// files icon names resolve against.
package icons

import (
	"log/slog"

	"github.com/stubbedev/lys-lib/logutil"
)

// ApplyIconTheme switches the theme lookups resolve against and drops
// every cached raster (bumping Generation, which live themed widgets
// watch): the follower's half of a live icon-theme switch. An empty
// name — a broken or missing setting — keeps the previous theme and
// logs Warn, the degraded-but-running convention; a name equal to the
// current theme is a no-op. OnIconThemeChanged listeners hear about
// every applied switch.
func (c *Cache) ApplyIconTheme(name string) {
	c.mu.Lock()
	switch name {
	case "":
		previous := c.theme
		c.mu.Unlock()
		logutil.L().Warn("icons: portal reported an empty icon theme; keeping the previous one",
			slog.String("theme", previous))
		return
	case c.theme:
		c.mu.Unlock()
		return
	}
	c.theme = name
	resets := c.resetLocked()
	listeners := cloneThemeListeners(c.themeListeners)
	c.mu.Unlock()
	runAll(resets)
	for _, fn := range listeners {
		fn(name)
	}
}

// OnIconThemeChanged registers fn to run with the applied theme name
// after each applied icon-theme switch (ApplyIconTheme), once the cache
// has switched and bumped Generation. Callbacks run on the caller's
// goroutine — with a portal monitor feeding the cache that is the
// monitor's; bridge into the loop with Application.Invoke, where the
// usual reaction is requesting a repaint. Registering twice runs both
// per change.
func (c *Cache) OnIconThemeChanged(fn func(name string)) {
	if fn == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.themeListeners = append(c.themeListeners, fn)
}

// cloneThemeListeners copies the listener slice out from under the lock.
func cloneThemeListeners(fns []func(string)) []func(string) {
	if len(fns) == 0 {
		return nil
	}
	out := make([]func(string), len(fns))
	copy(out, fns)
	return out
}
