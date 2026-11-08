// Package icontheme resolves freedesktop icon theme names into rasters
// for programs that draw icons without widgets, such as a compositor
// drawing the icon a window names. Lookups follow the icon theme spec
// with the same theme chain, unthemed fallback and bundled icons as
// a Cache of package icons.
package icontheme

import (
	"github.com/stubbedev/lys-lib/icons"
	"github.com/stubbedev/lys-lib/render"
)

// DefaultTheme is the theme a Theme starts on when none is named:
// GTK's default, Adwaita, inheriting hicolor.
const DefaultTheme = icons.DefaultTheme

// ErrNotFound reports that no theme of the chain, no unthemed
// directory and no bundled icon holds the requested name.
var ErrNotFound = icons.ErrNotFound

// Theme resolves and caches icons of one theme. It is safe for
// concurrent use.
type Theme struct {
	cache *icons.Cache
}

// New returns a theme called name, DefaultTheme when name is empty,
// searching the base directories searchPaths in priority order, or the
// XDG data directories when searchPaths is nil.
func New(name string, searchPaths []string) *Theme {
	if name == "" {
		name = DefaultTheme
	}
	c := icons.New(name)
	if searchPaths != nil {
		c.SetSearchPaths(searchPaths)
	}
	return &Theme{cache: c}
}

// Icon returns the icon called name at size logical pixels in its
// natural colors, rasterized into a size times frac120/120 pixel box
// (rounded up). The icon is shared with the cache; treat it as
// read-only. The error wraps ErrNotFound for an unknown name.
func (t *Theme) Icon(name string, size int, frac120 uint32) (*render.Icon, error) {
	return t.cache.Icon(name, size, frac120)
}
