package xkb

// Format is the text format the keymap was compiled in.
func (m *Keymap) Format() KeymapFormat { return m.format }

// Context is the context the keymap was compiled with.
func (m *Keymap) Context() *Context { return m.ctx }

// ModMaskOfIndex is the real modifiers modifier idx maps to, 0 for an
// unknown or unbound one.
func (m *Keymap) ModMaskOfIndex(idx uint32) ModMask {
	if idx >= m.mods.num {
		return 0
	}
	return ModMask(m.mods.mods[idx].mapping)
}

// LayoutIndex is the index of a named layout.
func (m *Keymap) LayoutIndex(name string) (uint32, bool) {
	for i, n := range m.groupNames {
		if n == name && name != "" {
			return uint32(i), true
		}
	}
	return 0, false
}

// KeyForEach calls fn with every keycode in the keymap, in order.
func (m *Keymap) KeyForEach(fn func(Keycode)) {
	for i := range m.flatKeys {
		fn(m.flatKeys[i].keycode)
	}
}

// KeyGetModsForLevel is the modifier masks that produce a level of a layout
// of a key. The empty mask comes first for level 0 when no entry maps it.
func (m *Keymap) KeyGetModsForLevel(kc Keycode, layout, lvl uint32) []ModMask {
	i := m.keyIndex(kc)
	if i < 0 {
		return nil
	}
	k := &m.flatKeys[i]
	wrapped := wrapGroup(int32(layout), uint32(k.numGroups), k.rangeAct, uint32(k.rangeNum)) //nolint:gosec // a two's complement layout
	if wrapped == layoutInvalid {
		return nil
	}
	t := &m.flatTypes[m.flatGroups[k.firstGroup+wrapped].typ]
	if lvl >= uint32(t.numLevels) {
		return nil
	}
	entries := m.flatEntries[t.entries : t.entries+t.numEntries]
	var masks []ModMask
	if lvl == 0 {
		emptyMapped := false
		for j := range entries {
			if entries[j].active && entries[j].mask == 0 {
				emptyMapped = true
				break
			}
		}
		if !emptyMapped {
			masks = append(masks, 0)
		}
	}
	for j := range entries {
		if entries[j].active && entries[j].level == lvl {
			masks = append(masks, ModMask(entries[j].mask))
		}
	}
	return masks
}

// KeycodeIsLegalExt reports whether a keycode is legal in the XKB extended
// range libxkbcommon uses.
func KeycodeIsLegalExt(kc Keycode) bool { return kc <= keycodeMax }

// KeycodeIsLegalX11 reports whether a keycode fits the X11 protocol.
func KeycodeIsLegalX11(kc Keycode) bool { return kc >= 8 && kc <= 255 }

// ParseLogLevel reads a log level the way XKB_LOG_LEVEL is read: a number or
// a name such as "warning", LogError for anything else.
func ParseLogLevel(s string) LogLevel { return parseLogLevel(s) }

// Logf logs a message through the context at a level, for code built on it.
func (c *Context) Logf(level LogLevel, format string, args ...any) {
	c.log(level, 0, 0, format, args...)
}
