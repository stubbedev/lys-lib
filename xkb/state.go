package xkb

import (
	"math/bits"
)

// KeyDirection says whether a key went down or up.
type KeyDirection uint8

const (
	// KeyReleased is a key going up.
	KeyReleased KeyDirection = iota
	// KeyPressed is a key going down.
	KeyPressed
)

// StateComponent names parts of a State: what changed, or what to read.
type StateComponent uint32

const (
	// StateModDepressed is the modifiers of held keys.
	StateModDepressed StateComponent = 1 << iota
	// StateModLatched is the modifiers latched until the next key.
	StateModLatched
	// StateModLocked is the modifiers locked on.
	StateModLocked
	// StateModEffective is the modifiers in effect.
	StateModEffective
	// StateLayoutDepressed is the layout offset of held keys.
	StateLayoutDepressed
	// StateLayoutLatched is the layout offset latched until the next key.
	StateLayoutLatched
	// StateLayoutLocked is the locked layout.
	StateLayoutLocked
	// StateLayoutEffective is the layout in effect.
	StateLayoutEffective
	// StateLEDs is the LEDs.
	StateLEDs
)

type latchState uint8

const (
	noLatch latchState = iota
	latchKeyDown
	latchPending
)

type filter struct {
	act    action
	key    int32
	priv   uint32
	delta  int32
	slot   uint32
	refcnt int32
	latch  latchState
	kind   actionType
}

const initialFilters = 16

type components struct {
	baseGroup, latchedGroup, lockedGroup int32
	group                                uint32
	baseMods, latchedMods, lockedMods    uint32
	mods                                 uint32
	leds                                 uint32
}

// State tracks one keyboard's modifiers, layout and LEDs over a Keymap.
// Not safe for concurrent use.
type State struct {
	keymap    *Keymap
	c         components
	ledsFor   components
	setMods   uint32
	clearMods uint32
	ledsValid bool
	modCount  [maxMods]int16
	filters   []filter
	live      []uint64
	synth     [1]action
}

// NewState opens a state with nothing held, latched or locked.
func (m *Keymap) NewState() *State {
	return &State{keymap: m, filters: make([]filter, 0, initialFilters), live: make([]uint64, 1, 4)}
}

func (s *State) release(f *filter) {
	s.live[f.slot/64] &^= 1 << (f.slot % 64)
}

func (s *State) keyLayout(k *flatKey) uint32 {
	return wrapGroup(int32(s.c.group), uint32(k.numGroups), k.rangeAct, uint32(k.rangeNum)) //nolint:gosec // group is at most maxGroups
}

func (s *State) activeEntry(k *flatKey, layout uint32) *flatEntry {
	m := s.keymap
	t := &m.flatTypes[m.flatGroups[k.firstGroup+layout].typ]
	return m.typeEntry(t, s.c.mods&t.mask)
}

func (s *State) keyLevel(k *flatKey, layout uint32) uint32 {
	if layout >= uint32(k.numGroups) {
		return levelInvalid
	}
	if e := s.activeEntry(k, layout); e != nil {
		return e.level
	}
	return 0
}

func (s *State) newFilter() *filter {
	for w := range s.live {
		free := ^s.live[w]
		if free == 0 {
			continue
		}
		slot := uint32(w)*64 + uint32(bits.TrailingZeros64(free)) //nolint:gosec // bounded by the slots
		if int(slot) > len(s.filters) {
			continue
		}
		if int(slot) == len(s.filters) {
			s.filters = append(s.filters, filter{})
		}
		s.live[w] |= 1 << (slot % 64)
		s.filters[slot] = filter{refcnt: 1, slot: slot}
		return &s.filters[slot]
	}
	s.live = append(s.live, 1)
	slot := uint32(len(s.live)-1) * 64 //nolint:gosec // bounded by the slots
	s.filters = append(s.filters, filter{refcnt: 1, slot: slot})
	return &s.filters[slot]
}

func applyGroupDelta(f *filter, component *int32) {
	if f.act.flags&flagAbsoluteSwitch != 0 {
		*component = f.act.group
	} else {
		*component += f.act.group
	}
}

func (s *State) filterRun(f *filter, ki int32, k *flatKey, dir KeyDirection) bool {
	switch f.kind {
	case actModSet:
		return s.modSetFunc(f, ki, dir)
	case actModLock:
		return s.modLockFunc(f, ki, dir)
	case actModLatch:
		return s.modLatchFunc(f, ki, k, dir)
	case actGroupSet:
		return s.groupSetFunc(f, ki, dir)
	case actGroupLock:
		return s.groupLockFunc(f, ki, dir)
	case actGroupLatch:
		return s.groupLatchFunc(f, ki, k, dir)
	}
	return false
}

func (s *State) groupSetFunc(f *filter, ki int32, dir KeyDirection) bool {
	if ki != f.key {
		f.act.flags &^= flagLockClear
		return false
	}
	if dir == KeyPressed {
		f.refcnt++
		return true
	}
	f.refcnt--
	if f.refcnt > 0 {
		return true
	}
	s.c.baseGroup = int32(f.priv) //nolint:gosec // the bit-for-bit copy filterNew made
	if f.act.flags&flagLockClear != 0 {
		s.c.lockedGroup = 0
	}
	s.release(f)
	return false
}

func hasFilter(t actionType) bool {
	return t >= actModSet && t <= actGroupLock
}

func (s *State) applyFilters(ki int32, k *flatKey, dir KeyDirection) {
	consumed := false
	for w, word := range s.live {
		for ; word != 0; word &= word - 1 {
			f := &s.filters[w*64+bits.TrailingZeros64(word)]
			if s.filterRun(f, ki, k, dir) {
				consumed = true
			}
		}
	}
	if consumed || dir == KeyReleased {
		return
	}
	actions := s.keyActions(k)
	for i := range actions {
		a := &actions[i]
		if !hasFilter(a.typ) {
			continue
		}
		f := s.newFilter()
		f.key = ki
		f.kind = a.typ
		f.act = *a
		s.filterNew(f)
	}
}

func (s *State) updateLEDs() {
	inputs := s.c
	inputs.leds = 0
	if s.ledsValid && inputs == s.ledsFor {
		return
	}
	s.ledsFor, s.ledsValid = inputs, true
	s.c.leds = 0
	live := s.keymap.liveLEDs
	for i := range live {
		if s.ledActive(&s.keymap.leds[live[i].led]) {
			s.c.leds |= 1 << live[i].led
		}
	}
}

func (s *State) ledActive(l *led) bool {
	if s.groupsOrModsLit(l) {
		return true
	}
	return l.ctrls&s.keymap.enabledCtrls != 0
}

func (s *State) groupsOrModsLit(l *led) bool {
	if l.whichMods != 0 && l.mask != 0 {
		var mask uint32
		if l.whichMods&StateModEffective != 0 {
			mask |= s.c.mods
		}
		if l.whichMods&StateModDepressed != 0 {
			mask |= s.c.baseMods
		}
		if l.whichMods&StateModLatched != 0 {
			mask |= s.c.latchedMods
		}
		if l.whichMods&StateModLocked != 0 {
			mask |= s.c.lockedMods
		}
		if l.mask&mask != 0 {
			return true
		}
	}
	if l.whichGroups == 0 {
		return false
	}
	if l.groups == 0 {
		return (l.whichGroups&StateLayoutDepressed != 0 && s.c.baseGroup == 0) ||
			(l.whichGroups&StateLayoutLatched != 0 && s.c.latchedGroup == 0)
	}
	var mask uint32
	if l.whichGroups&StateLayoutEffective != 0 {
		mask |= 1 << s.c.group
	}
	if l.whichGroups&StateLayoutLocked != 0 {
		mask |= 1 << uint32(s.c.lockedGroup) //nolint:gosec // wrapped into range by updateDerived
	}
	if l.whichGroups&StateLayoutDepressed != 0 && s.c.baseGroup != 0 {
		mask |= l.groups
	}
	if l.whichGroups&StateLayoutLatched != 0 && s.c.latchedGroup != 0 {
		mask |= l.groups
	}
	return l.groups&mask != 0
}

func (s *State) updateDerived() {
	s.c.mods = s.c.baseMods | s.c.latchedMods | s.c.lockedMods
	wrapped := wrapGroup(s.c.lockedGroup, s.keymap.numGroups, rangeWrap, 0)
	if wrapped == layoutInvalid {
		wrapped = 0
	}
	s.c.lockedGroup = int32(wrapped)
	wrapped = wrapGroup(s.c.baseGroup+s.c.latchedGroup+s.c.lockedGroup, s.keymap.numGroups, rangeWrap, 0)
	if wrapped == layoutInvalid {
		wrapped = 0
	}
	s.c.group = wrapped
	s.updateLEDs()
}

func componentChanges(a, b *components) StateComponent {
	if *a == *b {
		return 0
	}
	var mask StateComponent
	if a.group != b.group {
		mask |= StateLayoutEffective
	}
	if a.baseGroup != b.baseGroup {
		mask |= StateLayoutDepressed
	}
	if a.latchedGroup != b.latchedGroup {
		mask |= StateLayoutLatched
	}
	if a.lockedGroup != b.lockedGroup {
		mask |= StateLayoutLocked
	}
	if a.mods != b.mods {
		mask |= StateModEffective
	}
	if a.baseMods != b.baseMods {
		mask |= StateModDepressed
	}
	if a.latchedMods != b.latchedMods {
		mask |= StateModLatched
	}
	if a.lockedMods != b.lockedMods {
		mask |= StateModLocked
	}
	if a.leds != b.leds {
		mask |= StateLEDs
	}
	return mask
}

// UpdateKey applies a key press or release through the key's actions, the
// way a compositor runs the keyboard, and reports what changed.
func (s *State) UpdateKey(kc Keycode, dir KeyDirection) StateComponent {
	ki := s.keymap.keyIndex(kc)
	if ki < 0 {
		return 0
	}
	prev := s.c
	s.setMods, s.clearMods = 0, 0
	s.applyFilters(ki, &s.keymap.flatKeys[ki], dir)
	for set := s.setMods; set != 0; set &= set - 1 {
		i := bits.TrailingZeros32(set)
		s.modCount[i]++
		s.c.baseMods |= 1 << i
	}
	for clear := s.clearMods; clear != 0; clear &= clear - 1 {
		i := bits.TrailingZeros32(clear)
		s.modCount[i]--
		if s.modCount[i] <= 0 {
			s.c.baseMods &^= 1 << i
			s.modCount[i] = 0
		}
	}
	s.updateDerived()
	return componentChanges(&prev, &s.c)
}

func (m *Keymap) resolveCanonical(mods uint32) uint32 {
	extra := mods &^ m.canonicalMask
	if extra == 0 {
		return mods
	}
	return (mods & m.canonicalMask) | m.mods.effective(extra)
}

// UpdateMask replaces the state with one a compositor serialized, the way
// a client follows wl_keyboard.modifiers, and reports what changed.
func (s *State) UpdateMask(depressed, latched, locked ModMask, depressedLayout, latchedLayout, lockedLayout uint32) StateComponent {
	prev := s.c
	s.c.baseMods = s.keymap.resolveCanonical(uint32(depressed))
	s.c.latchedMods = s.keymap.resolveCanonical(uint32(latched))
	s.c.lockedMods = s.keymap.resolveCanonical(uint32(locked))
	s.c.baseGroup = int32(depressedLayout)  //nolint:gosec // a two's complement layout offset
	s.c.latchedGroup = int32(latchedLayout) //nolint:gosec // a two's complement layout offset
	s.c.lockedGroup = int32(lockedLayout)   //nolint:gosec // a two's complement layout offset
	s.updateDerived()
	return componentChanges(&prev, &s.c)
}

func (s *State) flatKey(kc Keycode) *flatKey {
	i := s.keymap.keyIndex(kc)
	if i < 0 {
		return nil
	}
	return &s.keymap.flatKeys[i]
}

// KeyGetLayout is the layout the key resolves to now, false for a keycode
// with no key or no layouts.
func (s *State) KeyGetLayout(kc Keycode) (uint32, bool) {
	k := s.flatKey(kc)
	if k == nil {
		return 0, false
	}
	layout := s.keyLayout(k)
	return layout, layout != layoutInvalid
}

// KeyGetLevel is the shift level the key resolves to in a layout, false
// when the key or the layout does not exist.
func (s *State) KeyGetLevel(kc Keycode, layout uint32) (uint32, bool) {
	k := s.flatKey(kc)
	if k == nil {
		return 0, false
	}
	lvl := s.keyLevel(k, layout)
	return lvl, lvl != levelInvalid
}

func (s *State) consumedMods(k *flatKey) uint32 {
	return s.keyConsumed(k, ConsumedModeXKB)
}

func (s *State) modUnconsumed(k *flatKey, idx uint32) bool {
	mapping := s.keymap.mods.mods[idx].mapping
	if mapping == 0 || s.c.mods&mapping != mapping {
		return false
	}
	return mapping&s.consumedMods(k) != mapping
}

const (
	modIndexCaps = 1
	modIndexCtrl = 2
)

func (s *State) currentLevel(k *flatKey) *flatLevel {
	layout := s.keyLayout(k)
	if layout == layoutInvalid {
		return nil
	}
	lvl := s.keyLevel(k, layout)
	if lvl == levelInvalid {
		return nil
	}
	return s.keymap.keyLevel(k, layout, lvl)
}

// KeyGetSyms is the keysyms the key produces now, with Caps Lock's
// capitalization applied. The slice belongs to the keymap.
func (s *State) KeyGetSyms(kc Keycode) []Keysym {
	k := s.flatKey(kc)
	if k == nil {
		return nil
	}
	l := s.currentLevel(k)
	if l == nil || l.numSyms == 0 {
		return nil
	}
	if s.modUnconsumed(k, modIndexCaps) {
		return s.keymap.levelUpper(l)
	}
	return s.keymap.levelSyms(l)
}

// KeyGetOneSym is the key's keysym now, KeyNoSymbol unless it produces
// exactly one.
func (s *State) KeyGetOneSym(kc Keycode) Keysym {
	k := s.flatKey(kc)
	if k == nil {
		return KeyNoSymbol
	}
	l := s.currentLevel(k)
	if l == nil || l.numSyms != 1 {
		return KeyNoSymbol
	}
	if s.modUnconsumed(k, modIndexCaps) {
		return s.keymap.flatSyms[l.upper]
	}
	return s.keymap.flatSyms[l.syms]
}

func (s *State) oneSymForString(k *flatKey) Keysym {
	if k.numGroups == 0 {
		return KeyNoSymbol
	}
	l := s.currentLevel(k)
	if l == nil || l.numSyms != 1 {
		return KeyNoSymbol
	}
	m := s.keymap
	sym := m.flatSyms[l.syms]
	if s.modUnconsumed(k, modIndexCtrl) && sym > 127 {
		for i := range uint32(k.numGroups) {
			other := m.keyLevel(k, i, s.keyLevel(k, i))
			if other != nil && other.numSyms == 1 && m.flatSyms[other.syms] <= 127 {
				sym = m.flatSyms[other.syms]
				break
			}
		}
	}
	if s.modUnconsumed(k, modIndexCaps) {
		sym = KeysymToUpper(sym)
	}
	return sym
}

func toControl(c byte) byte {
	switch {
	case (c >= '@' && c < 0x7f) || c == ' ':
		return c & 0x1f
	case c == '2':
		return 0
	case c >= '3' && c <= '7':
		return c - ('3' - 0x1b)
	case c == '8':
		return 0x7f
	case c == '/':
		return '_' & 0x1f
	}
	return c
}

// KeyGetUTF32 is the character the key types now, 0 when it types none.
func (s *State) KeyGetUTF32(kc Keycode) rune {
	k := s.flatKey(kc)
	if k == nil {
		return 0
	}
	cp := KeysymToUTF32(s.oneSymForString(k))
	if cp >= 0 && cp <= 127 && s.modUnconsumed(k, modIndexCtrl) {
		return rune(toControl(byte(cp)))
	}
	return cp
}

// KeyGetUTF8 is the text the key types now, "" when it types none. It
// allocates nothing: the text belongs to the keymap.
func (s *State) KeyGetUTF8(kc Keycode) string {
	k := s.flatKey(kc)
	if k == nil {
		return ""
	}
	return s.keyText(k)
}

// singleBytes is every ASCII byte, in order.
const singleBytes = "\x00\x01\x02\x03\x04\x05\x06\x07\x08\x09\x0a\x0b\x0c\x0d\x0e\x0f\x10\x11\x12\x13\x14\x15\x16\x17\x18\x19\x1a\x1b\x1c\x1d\x1e\x1f\x20\x21\x22\x23\x24\x25\x26\x27\x28\x29\x2a\x2b\x2c\x2d\x2e\x2f\x30\x31\x32\x33\x34\x35\x36\x37\x38\x39\x3a\x3b\x3c\x3d\x3e\x3f\x40\x41\x42\x43\x44\x45\x46\x47\x48\x49\x4a\x4b\x4c\x4d\x4e\x4f\x50\x51\x52\x53\x54\x55\x56\x57\x58\x59\x5a\x5b\x5c\x5d\x5e\x5f\x60\x61\x62\x63\x64\x65\x66\x67\x68\x69\x6a\x6b\x6c\x6d\x6e\x6f\x70\x71\x72\x73\x74\x75\x76\x77\x78\x79\x7a\x7b\x7c\x7d\x7e\x7f"

func (s *State) keyText(k *flatKey) string {
	m := s.keymap
	l := s.currentLevel(k)
	if l == nil || l.numSyms == 0 {
		return ""
	}
	caps := s.modUnconsumed(k, modIndexCaps)
	var text string
	switch {
	case l.numSyms != 1:
		if caps {
			text = m.textBlob[l.upperText : l.upperText+uint32(l.upperLen)]
		} else {
			text = m.textBlob[l.text : l.text+uint32(l.textLen)]
		}
	default:
		if s.modUnconsumed(k, modIndexCtrl) && m.flatSyms[l.syms] > 127 {
			for i := range uint32(k.numGroups) {
				other := m.keyLevel(k, i, s.keyLevel(k, i))
				if other != nil && other.numSyms == 1 && m.flatSyms[other.syms] <= 127 {
					l = other
					break
				}
			}
		}
		if caps {
			text = m.textBlob[l.upperText : l.upperText+uint32(l.upperLen)]
		} else {
			text = m.textBlob[l.text : l.text+uint32(l.textLen)]
		}
	}
	if len(text) == 1 && text[0] <= 127 && s.modUnconsumed(k, modIndexCtrl) {
		c := toControl(text[0])
		return singleBytes[c : c+1]
	}
	return text
}

// AppendKeyUTF8 appends the text the key types now to dst, allocating
// nothing when dst has room.
func (s *State) AppendKeyUTF8(dst []byte, kc Keycode) []byte {
	return append(dst, s.KeyGetUTF8(kc)...)
}

// SerializeMods is the modifiers of the given components, for
// wl_keyboard.modifiers. StateModEffective wins over the others.
func (s *State) SerializeMods(c StateComponent) ModMask {
	if c&StateModEffective != 0 {
		return ModMask(s.c.mods)
	}
	var out uint32
	if c&StateModDepressed != 0 {
		out |= s.c.baseMods
	}
	if c&StateModLatched != 0 {
		out |= s.c.latchedMods
	}
	if c&StateModLocked != 0 {
		out |= s.c.lockedMods
	}
	return ModMask(out)
}

// SerializeLayout is the layout of the given components, for
// wl_keyboard.modifiers. StateLayoutEffective wins over the others.
func (s *State) SerializeLayout(c StateComponent) uint32 {
	if c&StateLayoutEffective != 0 {
		return s.c.group
	}
	var out int32
	if c&StateLayoutDepressed != 0 {
		out += s.c.baseGroup
	}
	if c&StateLayoutLatched != 0 {
		out += s.c.latchedGroup
	}
	if c&StateLayoutLocked != 0 {
		out += s.c.lockedGroup
	}
	return uint32(out)
}

// ModNameIsActive reports whether every real modifier of the named one is
// active in the components, false for an unknown or unbound modifier.
func (s *State) ModNameIsActive(name string, c StateComponent) bool {
	i, ok := s.keymap.modNames[name]
	if !ok {
		return false
	}
	mapping := s.keymap.mods.mods[i].mapping
	return mapping != 0 && uint32(s.SerializeMods(c))&mapping == mapping
}

// LEDNameIsActive reports whether the named LED is lit, false for an
// unknown one.
func (s *State) LEDNameIsActive(name string) bool {
	i, ok := s.keymap.ledNames[name]
	return ok && s.c.leds&(1<<i) != 0
}

// LEDIndexIsActive reports whether LED slot i is lit, false for an unnamed
// or out of range one. It reads the state only, so a caller holding the
// index from LEDIndex pays no name lookup.
func (s *State) LEDIndexIsActive(i uint32) bool {
	return i < maxLEDs && s.c.leds&(1<<i) != 0 && s.keymap.leds[i].name != ""
}
