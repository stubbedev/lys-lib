package icons

import (
	"embed"
	"io/fs"
	"path"
	"slices"
	"strings"
)

// lucideFS is the bundled icon set: a lean selection of Lucide icons
// (https://lucide.dev, ISC - lucide/LICENSE), vendored verbatim from
// lucide-static 1.52.0, covering UI essentials (open, save, close, add,
// remove, search, settings, navigation, dialogs, media, emblems). They
// stroke in currentColor, so they recolor like any symbolic icon, and
// rasterize on demand through the cache. The set is the final tier of
// every lookup - after the theme chain, hicolor, and the unthemed
// directories - so stock chrome still draws on a host with a broken or
// absent icon theme (containers, minimal VMs, the headless gate), and
// it never shadows an installed theme.
//
//go:embed lucide/*.svg
var lucideFS embed.FS

// builtinPrefix marks a resolved bundled icon's pseudo-path (Lookup
// returns it where a theme would return a file).
const builtinPrefix = "builtin:"

// freedesktopLucide maps the freedesktop (Adwaita) names applications
// ask for onto the bundled Lucide icons. A Lucide name resolves as
// itself too.
var freedesktopLucide = map[string]string{
	"list-add":             "plus",
	"list-remove":          "minus",
	"window-close":         "x",
	"process-stop":         "x",
	"application-exit":     "x",
	"window-minimize":      "minus",
	"window-maximize":      "square",
	"window-restore":       "copy",
	"system-search":        "search",
	"edit-find":            "search",
	"folder":               "folder",
	"document-open":        "folder-open",
	"document-save":        "save",
	"text-x-generic":       "file",
	"document-new":         "file-plus",
	"edit-copy":            "copy",
	"edit-paste":           "clipboard",
	"edit-cut":             "scissors",
	"user-trash":           "trash-2",
	"edit-delete":          "trash-2",
	"edit-undo":            "undo-2",
	"edit-redo":            "redo-2",
	"edit-clear":           "circle-x",
	"view-refresh":         "refresh-cw",
	"go-previous":          "arrow-left",
	"go-next":              "arrow-right",
	"go-up":                "arrow-up",
	"go-down":              "arrow-down",
	"pan-down":             "chevron-down",
	"pan-up":               "chevron-up",
	"pan-start":            "chevron-left",
	"pan-end":              "chevron-right",
	"object-select":        "check",
	"open-menu":            "menu",
	"view-more":            "ellipsis-vertical",
	"view-more-horizontal": "ellipsis",
	"emblem-system":        "settings",
	"preferences-system":   "settings",
	"dialog-information":   "info",
	"help-about":           "info",
	"dialog-warning":       "triangle-alert",
	"dialog-error":         "circle-alert",
	"emblem-important":     "circle-alert",
	"emblem-ok":            "circle-check",
	"emblem-default":       "circle-check",
	"starred":              "star",
	"non-starred":          "star",
	"emblem-favorite":      "star",
	"user-home":            "house",
	"go-home":              "house",
	"avatar-default":       "user",
	"media-playback-start": "play",
	"media-playback-pause": "pause",
	"media-playback-stop":  "square",
	"audio-volume-high":    "volume-2",
	"audio-volume-muted":   "volume-x",
	"view-reveal":          "eye",
	"view-conceal":         "eye-off",
	"view-list":            "list",
	"view-grid":            "layout-grid",
	"image-missing":        "image-off",
}

// lookupBuiltin resolves name - a freedesktop name with or without
// -symbolic, or a Lucide name - against the bundled set; "" when it has
// none.
func lookupBuiltin(name string) string {
	stem := strings.TrimSuffix(name, symbolicSuffix)
	if l, ok := freedesktopLucide[stem]; ok {
		stem = l
	}
	file := path.Join("lucide", stem+".svg")
	if _, err := fs.Stat(lucideFS, file); err != nil {
		return ""
	}
	return builtinPrefix + file
}

// readIcon reads a resolved icon: a theme file, or a bundled one.
func readIcon(fsys themeFS, p string) ([]byte, error) {
	if file, ok := strings.CutPrefix(p, builtinPrefix); ok {
		return lucideFS.ReadFile(file)
	}
	return fsys.ReadFile(p)
}

// BuiltinNames lists the freedesktop names the bundled set serves,
// sorted; the Lucide names themselves resolve too.
func BuiltinNames() []string {
	out := make([]string, 0, len(freedesktopLucide))
	for name := range freedesktopLucide {
		out = append(out, name)
	}
	slices.Sort(out)
	return out
}
