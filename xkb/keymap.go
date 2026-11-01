package xkb

import (
	"fmt"
	"slices"
	"strings"
	"sync"
)

const (
	maxMods         = 32
	maxLEDs         = 32
	realModMask     = 0xff
	maxGroupsV1     = 4
	maxGroups       = 32
	allGroupsMask   = 0xffffffff
	maxContiguous   = 0xfff
	keycodeMax      = 0xfffffffe
	layoutInvalid   = ^uint32(0)
	levelInvalid    = ^uint32(0)
	modNone         = ^uint32(0)
	modIndexInvalid = ^uint32(0)
	includeMaxDepth = 15
	rulesIncludeMax = 5
)

type modType uint8

const (
	modReal    modType = 1
	modVirtual modType = 2
	modBoth            = modReal | modVirtual
)

type mod struct {
	name    string
	typ     modType
	mapping uint32
}

type modSet struct {
	mods     [maxMods]mod
	num      uint32
	explicit uint32
}

func newBuiltinMods() modSet {
	var s modSet
	for i, name := range []string{"Shift", "Lock", "Control", "Mod1", "Mod2", "Mod3", "Mod4", "Mod5"} {
		s.mods[i] = mod{name: name, typ: modReal, mapping: 1 << i}
	}
	s.num = 8
	return s
}

func (s *modSet) index(name string, typ modType) (uint32, bool) {
	for i := range s.num {
		if s.mods[i].typ&typ != 0 && s.mods[i].name == name {
			return i, true
		}
	}
	return 0, false
}

func (s *modSet) effective(mask uint32) uint32 {
	out := mask & realModMask
	for i := uint32(8); i < s.num; i++ {
		if mask&(1<<i) != 0 {
			out |= s.mods[i].mapping
		}
	}
	return out
}

func (s *modSet) resetVirtual() {
	for i := range s.num {
		s.mods[i].mapping = 0
	}
	s.explicit = 0
}

type actionType uint8

const (
	actNone actionType = iota
	actVoid
	actModSet
	actModLatch
	actModLock
	actGroupSet
	actGroupLatch
	actGroupLock
	actPtrMove
	actPtrButton
	actPtrLock
	actPtrDefault
	actTerminate
	actSwitchVT
	actCtrlSet
	actCtrlLock
	actLegacy
	actPrivate
)

type actionFlags uint16

const (
	flagLockClear actionFlags = 1 << iota
	flagLatchToLock
	flagLockNoLock
	flagLockNoUnlock
	flagLookupModmap
	flagAbsoluteSwitch
	flagAbsoluteX
	flagAbsoluteY
	flagAccel
	flagSameScreen
)

const controlAll = 0x7ff

type action struct {
	typ      actionType
	flags    actionFlags
	mods     uint32
	mask     uint32
	group    int32
	ctrls    uint32
	x, y     int16
	count    uint8
	button   uint8
	value    int8
	screen   int8
	privData [7]byte
}

type typeEntry struct {
	level        uint32
	mods, mask   uint32
	preserve     uint32
	preserveMask uint32
}

func (e *typeEntry) active() bool { return e.mods == 0 || e.mask != 0 }

type keyType struct {
	name       string
	mods, mask uint32
	required   bool
	numLevels  uint32
	levelNames []string
	entries    []typeEntry
}

type matchOp uint8

const (
	matchNone matchOp = iota
	matchAnyOrNone
	matchAny
	matchAll
	matchExactly
)

type interpret struct {
	sym          Keysym
	match        matchOp
	mods         uint32
	virtualMod   uint32
	levelOneOnly bool
	repeat       bool
	required     bool
	actions      []action
}

type led struct {
	name        string
	whichGroups StateComponent
	groups      uint32
	whichMods   StateComponent
	mods, mask  uint32
	ctrls       uint32
}

type rangeAction uint8

const (
	rangeWrap rangeAction = iota
	rangeSaturate
	rangeRedirect
)

type explicitFields uint8

const (
	explicitSymbols explicitFields = 1 << iota
	explicitInterp
	explicitTypes
	explicitVmodmap
	explicitRepeat
)

type level struct {
	syms    []Keysym
	upper   []Keysym
	actions []action
}

func (l *level) sameSyms(o *level) bool {
	if len(l.syms) != len(o.syms) {
		return false
	}
	for i := range l.syms {
		if l.syms[i] != o.syms[i] {
			return false
		}
	}
	return true
}

func (l *level) sameActions(o *level) bool {
	if len(l.actions) != len(o.actions) {
		return false
	}
	for i := range l.actions {
		if l.actions[i] != o.actions[i] {
			return false
		}
	}
	return true
}

type keyGroup struct {
	explicitActions bool
	explicitType    bool
	typ             *keyType
	levels          []level
}

type key struct {
	keycode   Keycode
	name      string
	explicit  explicitFields
	modmap    uint32
	vmodmap   uint32
	repeats   bool
	rangeAct  rangeAction
	rangeNum  uint32
	groups    []keyGroup
	numGroups uint32
}

type keyMatch struct {
	alias bool
	real  string
	index int
}

// Keymap is a compiled keyboard description: immutable and safe for
// concurrent use once built. Every State over it holds its own mutable
// part.
type Keymap struct {
	minKey, maxKey Keycode
	numLow         int
	keys           []key
	names          map[string]keyMatch
	aliases        []keyAlias
	types          []*keyType
	interprets     []*interpret
	mods           modSet
	canonicalMask  uint32
	numGroups      uint32
	groupNames     []string
	leds           []led
	sections       [fileSymbols + 1]string
	modNames       map[string]uint32
	ledNames       map[string]uint32
	liveLEDs       []liveLED
	flatKeys       []flatKey
	flatGroups     []flatGroup
	flatLevels     []flatLevel
	flatSyms       []Keysym
	flatActions    []action
	flatTypes      []flatType
	flatEntries    []flatEntry
	modLookup      []int32
	wideEntries    []wideEntry
	nameBlob       string
	nameIndex      []flatName
	aliasPairs     []flatAlias
	interpsAny     bool
	textOnce       sync.Once
	text           string
}

type keyAlias struct{ alias, real string }

func newKeymap() *Keymap {
	return &Keymap{mods: newBuiltinMods(), canonicalMask: realModMask}
}

func (m *Keymap) keyByName(name string, aliases bool) *key {
	match, ok := m.names[name]
	if !ok {
		return nil
	}
	if !match.alias {
		return &m.keys[match.index]
	}
	if !aliases {
		return nil
	}
	real, ok := m.names[match.real]
	if !ok {
		return nil
	}
	return &m.keys[real.index]
}

// MinKeycode is the lowest keycode the keymap names.
func (m *Keymap) MinKeycode() Keycode { return m.minKey }

// MaxKeycode is the highest keycode the keymap names.
func (m *Keymap) MaxKeycode() Keycode { return m.maxKey }

func (m *Keymap) level(k *key, layout, lvl uint32) *level {
	if k == nil {
		return nil
	}
	wrapped := wrapGroup(int32(layout), k.numGroups, k.rangeAct, k.rangeNum) //nolint:gosec // a layout index, bounded by the wrap
	if wrapped == layoutInvalid || lvl >= k.groups[wrapped].typ.numLevels {
		return nil
	}
	return &k.groups[wrapped].levels[lvl]
}

// NumMods is how many modifiers the keymap has, real and virtual.
func (m *Keymap) NumMods() uint32 { return m.mods.num }

// ModName is the name of modifier i, "" when out of range.
func (m *Keymap) ModName(i uint32) string {
	if i >= m.mods.num {
		return ""
	}
	return m.mods.mods[i].name
}

// ModIndex is the index of a modifier by its exact name.
func (m *Keymap) ModIndex(name string) (uint32, bool) {
	i, ok := m.modNames[name]
	return i, ok
}

// ModMaskOf is the real modifiers a named modifier maps to, 0 for an
// unknown or unbound one.
func (m *Keymap) ModMaskOf(name string) ModMask {
	i, ok := m.ModIndex(name)
	if !ok {
		return 0
	}
	return ModMask(m.mods.mods[i].mapping)
}

// NumLEDs is how many LED slots the keymap has; some may be unnamed.
func (m *Keymap) NumLEDs() uint32 { return uint32(len(m.leds)) } //nolint:gosec // at most maxLEDs

// LEDName is the name of LED slot i, "" when unnamed or out of range.
func (m *Keymap) LEDName(i uint32) string {
	if int64(i) >= int64(len(m.leds)) {
		return ""
	}
	return m.leds[i].name
}

// LEDIndex is the slot of a LED by its exact name.
func (m *Keymap) LEDIndex(name string) (uint32, bool) {
	i, ok := m.ledNames[name]
	return i, ok
}

// Text is the keymap serialized in the xkb_v1 text format wl_keyboard
// carries, with no includes: libxkbcommon and this package both parse it
// without any xkeyboard-config data.
func (m *Keymap) Text() string {
	m.textOnce.Do(func() { m.text = m.serialize() })
	return m.text
}

func wrapGroup(group int32, numGroups uint32, act rangeAction, num uint32) uint32 {
	if numGroups == 0 {
		return layoutInvalid
	}
	if group >= 0 && uint32(group) < numGroups {
		return uint32(group)
	}
	switch act {
	case rangeRedirect:
		if num >= numGroups {
			return 0
		}
		return num
	case rangeSaturate:
		if group < 0 {
			return 0
		}
		return numGroups - 1
	}
	rem := group % int32(numGroups) //nolint:gosec // numGroups is at most maxGroups
	if rem < 0 {
		rem += int32(numGroups) //nolint:gosec // numGroups is at most maxGroups
	}
	return uint32(rem) //nolint:gosec // checked non-negative
}

func (m *Keymap) findInterps(k *key, group, lvl uint32) ([]*interpret, bool) {
	l := m.level(k, group, lvl)
	if l == nil || len(l.syms) == 0 {
		return nil, false
	}
	out := make([]*interpret, 0, len(l.syms))
	for _, sym := range l.syms {
		out = append(out, m.findInterp(k, sym, lvl, out))
	}
	return out, true
}

var defaultInterpret = interpret{sym: KeyVoidSymbol, repeat: true, match: matchAnyOrNone, virtualMod: modIndexInvalid}

func (m *Keymap) compileResolveAlias(name string) string {
	if match, ok := m.names[name]; ok && match.alias {
		return match.real
	}
	return name
}

func (m *Keymap) findInterp(k *key, sym Keysym, lvl uint32, previous []*interpret) *interpret {
	for i, in := range m.interprets {
		if in.sym != sym && in.sym != KeyNoSymbol {
			continue
		}
		mods := k.modmap
		if in.levelOneOnly && lvl != 0 {
			mods = 0
		}
		if !in.matches(mods) {
			continue
		}
		if i > 0 && in.sym == KeyNoSymbol {
			if slices.Contains(previous, in) {
				return &defaultInterpret
			}
		}
		in.required = true
		return in
	}
	return &defaultInterpret
}

func (in *interpret) matches(mods uint32) bool {
	switch in.match {
	case matchNone:
		return in.mods&mods == 0
	case matchAnyOrNone:
		return mods == 0 || in.mods&mods != 0
	case matchAny:
		return in.mods&mods != 0
	case matchAll:
		return in.mods&mods == in.mods
	case matchExactly:
		return in.mods == mods
	}
	return false
}

func (m *Keymap) applyInterps(k *key) {
	var vmodmap uint32
	for g := range k.numGroups {
		group := &k.groups[g]
		if group.explicitActions {
			continue
		}
		for lvl := range group.typ.numLevels {
			interps, found := m.findInterps(k, g, lvl)
			if !found {
				continue
			}
			var actions []action
			for _, in := range interps {
				if g == 0 && lvl == 0 && k.explicit&explicitRepeat == 0 && in.repeat {
					k.repeats = true
				}
				if ((g == 0 && lvl == 0) || !in.levelOneOnly) && in.virtualMod != modIndexInvalid {
					vmodmap |= 1 << in.virtualMod
				}
				actions = append(actions, in.actions...)
			}
			group.levels[lvl].actions = actions
		}
	}
	if k.explicit&explicitVmodmap == 0 {
		k.vmodmap = vmodmap
	}
}

func isModAction(t actionType) bool { return t == actModSet || t == actModLatch || t == actModLock }

func isGroupAction(t actionType) bool {
	return t == actGroupSet || t == actGroupLatch || t == actGroupLock
}

func dropRepeatedCategories(k *key) {
	for g := range k.numGroups {
		levels := k.groups[g].levels
		for l := range levels {
			actions := levels[l].actions
			for i := range actions {
				mod, grp := isModAction(actions[i].typ), isGroupAction(actions[i].typ)
				if !mod && !grp {
					continue
				}
				for j := i + 1; j < len(actions); j++ {
					if (mod && isModAction(actions[j].typ)) || (grp && isGroupAction(actions[j].typ)) {
						actions[j].typ = actNone
					}
				}
			}
		}
	}
}

func (m *Keymap) hasUnboundVmods(mask uint32) bool {
	for i := uint32(8); i < m.mods.num; i++ {
		if mask&(1<<i) != 0 && m.mods.mods[i].mapping == 0 {
			return true
		}
	}
	return false
}

func (m *Keymap) effectiveMask(mods uint32) uint32 {
	var known uint32 = 0xffffffff
	if m.mods.num < 32 {
		known = (1 << m.mods.num) - 1
	}
	return m.mods.effective(mods) | (mods &^ known)
}

func (m *Keymap) deriveFields() {
	for i := range m.keys {
		m.applyInterps(&m.keys[i])
		dropRepeatedCategories(&m.keys[i])
	}
	for i := range m.keys {
		k := &m.keys[i]
		for idx := uint32(8); idx < m.mods.num; idx++ {
			if k.vmodmap&(1<<idx) != 0 {
				m.mods.mods[idx].mapping |= k.modmap
			}
		}
	}
	var extra uint32
	for idx := uint32(8); idx < m.mods.num; idx++ {
		extra |= m.mods.mods[idx].mapping
	}
	m.canonicalMask |= extra
	for _, t := range m.types {
		t.mask = m.effectiveMask(t.mods)
		for j := range t.entries {
			e := &t.entries[j]
			if m.hasUnboundVmods(e.mods) {
				e.mask = 0
				continue
			}
			e.mask = m.effectiveMask(e.mods)
			e.preserveMask = m.effectiveMask(e.preserve)
		}
	}
	for i := range m.keys {
		k := &m.keys[i]
		for g := range k.numGroups {
			levels := k.groups[g].levels
			for l := range levels {
				for a := range levels[l].actions {
					m.updateActionMods(&levels[l].actions[a], k.modmap)
				}
			}
		}
	}
	for i := range m.leds {
		m.leds[i].mask = m.effectiveMask(m.leds[i].mods)
	}
	for i := range m.keys {
		m.numGroups = max(m.numGroups, m.keys[i].numGroups)
	}
	m.buildFlat()
}

func (m *Keymap) updateActionMods(a *action, modmap uint32) {
	if !isModAction(a.typ) {
		return
	}
	if a.flags&flagLookupModmap != 0 {
		a.mods = modmap
	}
	a.mask = m.effectiveMask(a.mods)
}

func (m *Keymap) finish(f *xkbFile, ctx *session) error {
	var sections [fileSymbols + 1]*xkbFile
	for _, s := range f.sections {
		if s.typ > fileSymbols || sections[s.typ] != nil {
			continue
		}
		sections[s.typ] = s
	}
	if err := m.compileKeycodes(sections[fileKeycodes], ctx); err != nil {
		return fmt.Errorf("compile keycodes: %w", err)
	}
	if err := m.compileTypes(sections[fileTypes], ctx); err != nil {
		return fmt.Errorf("compile types: %w", err)
	}
	if err := m.compileCompat(sections[fileCompat], ctx); err != nil {
		return fmt.Errorf("compile compat: %w", err)
	}
	if err := m.compileSymbols(sections[fileSymbols], ctx); err != nil {
		return fmt.Errorf("compile symbols: %w", err)
	}
	m.deriveFields()
	return nil
}

func escapeMapName(name string) string {
	var b strings.Builder
	b.Grow(len(name))
	for i := range len(name) {
		c := name[i]
		if !legalMapNameByte(c) {
			c = '_'
		}
		b.WriteByte(c)
	}
	return b.String()
}

var legalMapNameBytes = [32]byte{
	0x00, 0x00, 0x00, 0x00, 0x00, 0xa7, 0xff, 0x83,
	0xfe, 0xff, 0xff, 0x87, 0xfe, 0xff, 0xff, 0x07,
	0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
	0xff, 0xff, 0x7f, 0xff, 0xff, 0xff, 0x7f, 0xff,
}

func legalMapNameByte(c byte) bool { return legalMapNameBytes[c/8]&(1<<(c%8)) != 0 }
