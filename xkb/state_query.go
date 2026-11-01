package xkb

import "math/bits"

// ConsumedMode selects how consumed modifiers are computed.
type ConsumedMode uint8

const (
	// ConsumedModeXKB is the XKB protocol's rule: every modifier the key's
	// type looks at is consumed, except those it preserves.
	ConsumedModeXKB ConsumedMode = iota
	// ConsumedModeGTK is the rule of GTK and Firefox: a modifier is consumed
	// only when it changes the level's keysyms on its own or is part of the
	// matching combination.
	ConsumedModeGTK
)

// StateMatch selects how several modifiers match in ModIndicesAreActive and
// ModNamesAreActive.
type StateMatch uint32

const (
	// MatchAny is true when any of the modifiers is active.
	MatchAny StateMatch = 1 << 0
	// MatchAll is true when all of the modifiers are active.
	MatchAll StateMatch = 1 << 1
	// MatchNonExclusive allows other modifiers to be active as well.
	MatchNonExclusive StateMatch = 1 << 16
)

// Keymap is the keymap the state runs on.
func (s *State) Keymap() *Keymap { return s.keymap }

func (s *State) keyConsumed(k *flatKey, mode ConsumedMode) uint32 {
	layout := s.keyLayout(k)
	if layout == layoutInvalid {
		return 0
	}
	m := s.keymap
	group := &m.flatGroups[k.firstGroup+layout]
	t := &m.flatTypes[group.typ]
	var preserve uint32
	matching := m.typeEntry(t, s.c.mods&t.mask)
	if matching != nil {
		preserve = matching.preserve
	}
	if mode == ConsumedModeXKB {
		return t.mask &^ preserve
	}
	noModsLevel := uint32(0)
	if e := m.typeEntry(t, 0); e != nil {
		noModsLevel = e.level
	}
	base := m.levelSyms(&m.flatLevels[group.firstLevel+noModsLevel])
	var consumed uint32
	entries := m.flatEntries[t.entries : t.entries+t.numEntries]
	for i := range entries {
		e := &entries[i]
		if !e.active || symsEqual(m.levelSyms(&m.flatLevels[group.firstLevel+e.level]), base) {
			continue
		}
		if e == matching || bits.OnesCount32(e.mask) == 1 {
			consumed |= e.mask &^ e.preserve
		}
	}
	return consumed &^ preserve
}

func symsEqual(a, b []Keysym) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// KeyGetConsumedMods is the modifiers the key's translation consumed, 0 for
// a keycode with no key.
func (s *State) KeyGetConsumedMods(kc Keycode, mode ConsumedMode) ModMask {
	k := s.flatKey(kc)
	if k == nil || mode > ConsumedModeGTK {
		return 0
	}
	return ModMask(s.keyConsumed(k, mode))
}

// ModIndexIsConsumed reports whether the key's translation consumed
// modifier idx. ok is false for a keycode with no key or an unknown modifier.
func (s *State) ModIndexIsConsumed(kc Keycode, idx uint32, mode ConsumedMode) (consumed, ok bool) {
	k := s.flatKey(kc)
	if k == nil || idx >= s.keymap.mods.num {
		return false, false
	}
	mapping := s.keymap.mods.mods[idx].mapping
	if mapping == 0 {
		return false, true
	}
	return mapping&s.keyConsumed(k, mode) == mapping, true
}

// ModMaskRemoveConsumed removes from mask what the key's translation
// consumed in ConsumedModeXKB, 0 for a keycode with no key.
func (s *State) ModMaskRemoveConsumed(kc Keycode, mask ModMask) ModMask {
	k := s.flatKey(kc)
	if k == nil {
		return 0
	}
	return ModMask(s.keymap.resolveCanonical(uint32(mask)) &^ s.keyConsumed(k, ConsumedModeXKB))
}

// ModIndexIsActive reports whether modifier idx is active in the components.
// ok is false for an unknown modifier.
func (s *State) ModIndexIsActive(idx uint32, c StateComponent) (active, ok bool) {
	if idx >= s.keymap.mods.num {
		return false, false
	}
	mapping := s.keymap.mods.mods[idx].mapping
	return mapping != 0 && uint32(s.SerializeMods(c))&mapping == mapping, true
}

func (s *State) matchMods(c StateComponent, match StateMatch, wanted uint32) bool {
	if wanted == 0 {
		return false
	}
	active := uint32(s.SerializeMods(c))
	if match&MatchNonExclusive == 0 && active&^wanted != 0 {
		return false
	}
	if match&MatchAny != 0 {
		return active&wanted != 0
	}
	return active&wanted == wanted
}

// ModIndicesAreActive matches several modifiers against the components. ok
// is false when any index is unknown.
func (s *State) ModIndicesAreActive(c StateComponent, match StateMatch, indices ...uint32) (active, ok bool) {
	var wanted uint32
	for _, idx := range indices {
		if idx >= s.keymap.mods.num {
			return false, false
		}
		wanted |= s.keymap.mods.mods[idx].mapping
	}
	return s.matchMods(c, match, wanted), true
}

// ModNamesAreActive matches several named modifiers against the components.
// ok is false when any name is unknown.
func (s *State) ModNamesAreActive(c StateComponent, match StateMatch, names ...string) (active, ok bool) {
	var wanted uint32
	for _, name := range names {
		idx, known := s.keymap.modNames[name]
		if !known {
			return false, false
		}
		wanted |= s.keymap.mods.mods[idx].mapping
	}
	return s.matchMods(c, match, wanted), true
}

// LayoutIndexIsActive reports whether layout idx is active in the components.
// ok is false for an unknown layout.
func (s *State) LayoutIndexIsActive(idx uint32, c StateComponent) (active, ok bool) {
	if idx >= s.keymap.numGroups {
		return false, false
	}
	return (c&StateLayoutEffective != 0 && s.c.group == idx) ||
		(c&StateLayoutDepressed != 0 && s.c.baseGroup == int32(idx)) || //nolint:gosec // below maxGroups
		(c&StateLayoutLatched != 0 && s.c.latchedGroup == int32(idx)) || //nolint:gosec // below maxGroups
		(c&StateLayoutLocked != 0 && s.c.lockedGroup == int32(idx)), true //nolint:gosec // below maxGroups
}

// LayoutNameIsActive reports whether the named layout is active in the
// components. ok is false for an unknown layout.
func (s *State) LayoutNameIsActive(name string, c StateComponent) (active, ok bool) {
	idx, known := s.keymap.LayoutIndex(name)
	if !known {
		return false, false
	}
	return s.LayoutIndexIsActive(idx, c)
}
