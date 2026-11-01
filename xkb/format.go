package xkb

// KeymapFormat selects a keymap text format.
type KeymapFormat uint8

const (
	// FormatOriginal serializes a keymap in the format it was compiled in.
	// It is valid for AsString only.
	FormatOriginal KeymapFormat = iota
	// FormatTextV1 is the classic XKB text format as xkbcomp generates it,
	// at most 4 layouts. Use it to serialize for X11 and for the keymap
	// wl_keyboard carries, and to parse what a compositor sends.
	FormatTextV1
	// FormatTextV2 is the libxkbcommon superset of FormatTextV1: up to 32
	// layouts, several actions per level, canonical virtual modifiers, and
	// the unlockOnPress, latchOnPress and lockOnRelease action fields. It is
	// incompatible with X11.
	FormatTextV2
)

// SerializeFlags adjusts AsString.
type SerializeFlags uint8

const (
	// SerializePretty spells keysyms by name and aligns the columns.
	SerializePretty SerializeFlags = 1 << iota
	// SerializeKeepUnused keeps key types and interpretations no key uses.
	SerializeKeepUnused
)

func (f KeymapFormat) valid() bool { return f == FormatTextV1 || f == FormatTextV2 }

func (f KeymapFormat) maxGroups() uint32 {
	if f == FormatTextV1 {
		return maxGroupsV1
	}
	return maxGroups
}
