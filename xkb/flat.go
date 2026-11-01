package xkb

import (
	"slices"
	"sort"
	"strings"
)

type flatKey struct {
	keycode    Keycode
	firstGroup uint32
	modmap     uint32
	vmodmap    uint32
	nameOff    uint32
	nameLen    uint16
	numGroups  uint8
	rangeNum   uint8
	rangeAct   rangeAction
	explicit   explicitFields
	repeats    bool
}

type flatGroup struct {
	firstLevel uint32
	typ        uint16
	flags      flatGroupFlags
}

type flatGroupFlags uint8

const flatGroupExplicitType flatGroupFlags = 1

type flatLevel struct {
	syms       uint32
	upper      uint32
	actions    uint32
	numSyms    uint16
	numActions uint16
}

type flatType struct {
	mask      uint32
	lookup    uint32
	wide      uint32
	numWide   uint16
	numLevels uint16
}

type flatEntry struct {
	level    uint32
	preserve uint32
}

type wideEntry struct {
	mask  uint32
	entry uint32
}

type flatName struct {
	off  uint32
	key  uint32
	size uint16
}

type liveLED struct {
	led uint32
}

const (
	modLookupSize = 256
	noEntry       = int32(-1)
)

func (m *Keymap) detachStrings() {
	for i := range m.mods.num {
		m.mods.mods[i].name = strings.Clone(m.mods.mods[i].name)
	}
	for _, t := range m.types {
		t.name = strings.Clone(t.name)
		for i := range t.levelNames {
			t.levelNames[i] = strings.Clone(t.levelNames[i])
		}
	}
	for i := range m.leds {
		m.leds[i].name = strings.Clone(m.leds[i].name)
	}
	for i := range m.groupNames {
		m.groupNames[i] = strings.Clone(m.groupNames[i])
	}
	for i := range m.sections {
		m.sections[i] = strings.Clone(m.sections[i])
	}
}

func (m *Keymap) buildFlat() {
	var numGroups, numLevels, numSyms, numActions int
	for i := range m.keys {
		k := &m.keys[i]
		numGroups += len(k.groups)
		for g := range k.groups {
			levels := k.groups[g].levels
			numLevels += len(levels)
			for l := range levels {
				numSyms += len(levels[l].syms)
				numActions += len(levels[l].actions)
				if !sameSlice(levels[l].upper, levels[l].syms) {
					numSyms += len(levels[l].upper)
				}
			}
		}
	}
	m.flatKeys = make([]flatKey, 0, len(m.keys))
	m.flatGroups = make([]flatGroup, 0, numGroups)
	m.flatLevels = make([]flatLevel, 0, numLevels)
	m.flatSyms = make([]Keysym, 0, numSyms)
	m.flatActions = make([]action, 0, numActions)
	typeIndex := m.buildTypes()
	var names strings.Builder
	for i := range m.keys {
		k := &m.keys[i]
		fk := flatKey{
			keycode:    k.keycode,
			firstGroup: uint32(len(m.flatGroups)), //nolint:gosec // bounded by the keymap's size
			modmap:     k.modmap,
			vmodmap:    k.vmodmap,
			nameOff:    uint32(names.Len()),  //nolint:gosec // bounded by the keymap's size
			nameLen:    uint16(len(k.name)),  //nolint:gosec // a key name
			numGroups:  uint8(len(k.groups)), //nolint:gosec // at most maxGroupsV1
			rangeNum:   uint8(k.rangeNum),    //nolint:gosec // at most maxGroupsV1
			rangeAct:   k.rangeAct,
			explicit:   k.explicit,
			repeats:    k.repeats,
		}
		names.WriteString(k.name)
		m.flatKeys = append(m.flatKeys, fk)
		for g := range k.groups {
			m.appendGroup(&k.groups[g], typeIndex)
		}
	}
	m.buildNameIndex(&names)
	m.detachStrings()
	m.buildLEDs()
	m.keys, m.names = nil, nil
	kept := m.interprets[:0]
	for _, in := range m.interprets {
		if in.required {
			kept = append(kept, in)
		}
	}
	m.interpsAny = len(m.interprets) > 0
	m.interprets = kept
}

func sameSlice(a, b []Keysym) bool {
	return len(a) == len(b) && (len(a) == 0 || &a[0] == &b[0])
}

func (m *Keymap) appendGroup(g *keyGroup, typeIndex map[*keyType]uint16) {
	var flags flatGroupFlags
	if g.explicitType {
		flags |= flatGroupExplicitType
	}
	m.flatGroups = append(m.flatGroups, flatGroup{
		firstLevel: uint32(len(m.flatLevels)), //nolint:gosec // bounded by the keymap's size
		typ:        typeIndex[g.typ],
		flags:      flags,
	})
	for l := range g.levels {
		lv := &g.levels[l]
		syms := uint32(len(m.flatSyms)) //nolint:gosec // bounded by the keymap's size
		m.flatSyms = append(m.flatSyms, lv.syms...)
		upper := syms
		if !sameSlice(lv.upper, lv.syms) {
			upper = uint32(len(m.flatSyms)) //nolint:gosec // bounded by the keymap's size
			m.flatSyms = append(m.flatSyms, lv.upper...)
		}
		actions := uint32(len(m.flatActions)) //nolint:gosec // bounded by the keymap's size
		m.flatActions = append(m.flatActions, lv.actions...)
		m.flatLevels = append(m.flatLevels, flatLevel{
			syms:       syms,
			upper:      upper,
			actions:    actions,
			numSyms:    uint16(len(lv.syms)),    //nolint:gosec // a level's keysyms
			numActions: uint16(len(lv.actions)), //nolint:gosec // a level's actions
		})
	}
}

func (m *Keymap) buildTypes() map[*keyType]uint16 {
	kept := make([]*keyType, 0, len(m.types))
	for _, t := range m.types {
		if t.required {
			kept = append(kept, t)
		}
	}
	m.types = kept
	index := make(map[*keyType]uint16, len(kept))
	m.flatTypes = make([]flatType, len(kept))
	m.modLookup = make([]int32, len(kept)*modLookupSize)
	for i := range m.modLookup {
		m.modLookup[i] = noEntry
	}
	for i, t := range kept {
		index[t] = uint16(i)
		m.flattenType(i, t)
	}
	return index
}

func (m *Keymap) flattenType(i int, t *keyType) {
	lookup := m.modLookup[i*modLookupSize : (i+1)*modLookupSize]
	wideStart := len(m.wideEntries)
	base := uint32(len(m.flatEntries)) //nolint:gosec // bounded by the file's size
	for j := range t.entries {
		e := &t.entries[j]
		m.flatEntries = append(m.flatEntries, flatEntry{level: e.level, preserve: e.preserveMask})
		if !e.active() {
			continue
		}
		idx := base + uint32(j)
		if e.mask < modLookupSize {
			if lookup[e.mask] == noEntry {
				lookup[e.mask] = int32(idx) //nolint:gosec // bounded by the file's size
			}
			continue
		}
		wide := m.wideEntries[wideStart:]
		pos := sort.Search(len(wide), func(p int) bool { return wide[p].mask >= e.mask })
		if pos < len(wide) && wide[pos].mask == e.mask {
			continue
		}
		m.wideEntries = slices.Insert(m.wideEntries, wideStart+pos, wideEntry{mask: e.mask, entry: idx})
	}
	m.flatTypes[i] = flatType{
		mask:      t.mask,
		lookup:    uint32(i * modLookupSize),              //nolint:gosec // bounded by the file's size
		wide:      uint32(wideStart),                      //nolint:gosec // bounded by the file's size
		numWide:   uint16(len(m.wideEntries) - wideStart), //nolint:gosec // bounded by the file's size
		numLevels: uint16(t.numLevels),                    //nolint:gosec // at most levelMaxImpl
	}
}

func (m *Keymap) buildNameIndex(names *strings.Builder) {
	entries := make([]flatName, 0, len(m.flatKeys)+len(m.aliases))
	for i := range m.flatKeys {
		k := &m.flatKeys[i]
		if k.nameLen > 0 {
			entries = append(entries, flatName{off: k.nameOff, size: k.nameLen, key: uint32(i)})
		}
	}
	m.aliasPairs = make([]flatAlias, 0, len(m.aliases))
	for _, a := range m.aliases {
		off := uint32(names.Len()) //nolint:gosec // bounded by the keymap's size
		names.WriteString(a.alias)
		realOff := uint32(names.Len()) //nolint:gosec // bounded by the keymap's size
		names.WriteString(a.real)
		entries = append(entries, flatName{off: off, size: uint16(len(a.alias)), key: uint32(m.names[a.real].index)}) //nolint:gosec // bounded by the keymap's size
		m.aliasPairs = append(m.aliasPairs, flatAlias{off, realOff, uint16(len(a.alias)), uint16(len(a.real))})       //nolint:gosec // key names
	}
	m.nameBlob = names.String()
	slices.SortFunc(entries, func(a, b flatName) int {
		return strings.Compare(m.nameBlob[a.off:a.off+uint32(a.size)], m.nameBlob[b.off:b.off+uint32(b.size)])
	})
	m.nameIndex = entries
	m.aliases = nil
}

type flatAlias struct {
	alias, real         uint32
	aliasSize, realSize uint16
}

func (m *Keymap) buildLEDs() {
	m.ledNames = make(map[string]uint32, len(m.leds))
	for i := range slices.Backward(m.leds) {
		if m.leds[i].name != "" {
			m.ledNames[m.leds[i].name] = uint32(i) //nolint:gosec // at most maxLEDs
		}
	}
	for i := range m.leds {
		l := &m.leds[i]
		if (l.whichMods != 0 && l.mask != 0) || l.whichGroups != 0 {
			m.liveLEDs = append(m.liveLEDs, liveLED{led: uint32(i)})
		}
	}
	m.modNames = make(map[string]uint32, m.mods.num)
	for i := m.mods.num; i > 0; i-- {
		m.modNames[m.mods.mods[i-1].name] = i - 1
	}
}

func (m *Keymap) keyIndex(kc Keycode) int32 {
	if kc < m.minKey || kc > m.maxKey {
		return -1
	}
	if idx := int(kc - m.minKey); idx < m.numLow {
		return int32(idx) //nolint:gosec // bounded by the keymap's size
	}
	lo, hi := m.numLow, len(m.flatKeys)
	for lo < hi {
		mid := int(uint(lo+hi) >> 1)
		switch code := m.flatKeys[mid].keycode; {
		case code < kc:
			lo = mid + 1
		case code > kc:
			hi = mid
		default:
			return int32(mid) //nolint:gosec // bounded by the keymap's size
		}
	}
	return -1
}

func (m *Keymap) keyName(k *flatKey) string {
	return m.nameBlob[k.nameOff : k.nameOff+uint32(k.nameLen)]
}

func (m *Keymap) keyLevel(k *flatKey, layout, lvl uint32) *flatLevel {
	wrapped := wrapGroup(int32(layout), uint32(k.numGroups), k.rangeAct, uint32(k.rangeNum)) //nolint:gosec // a layout index, bounded by the wrap
	if wrapped == layoutInvalid {
		return nil
	}
	g := &m.flatGroups[k.firstGroup+wrapped]
	if lvl >= uint32(m.flatTypes[g.typ].numLevels) {
		return nil
	}
	return &m.flatLevels[g.firstLevel+lvl]
}

func (m *Keymap) levelSyms(l *flatLevel) []Keysym {
	return m.flatSyms[l.syms : l.syms+uint32(l.numSyms)]
}

func (m *Keymap) levelUpper(l *flatLevel) []Keysym {
	return m.flatSyms[l.upper : l.upper+uint32(l.numSyms)]
}

func (m *Keymap) levelActions(l *flatLevel) []action {
	return m.flatActions[l.actions : l.actions+uint32(l.numActions)]
}

func (m *Keymap) typeEntry(t *flatType, active uint32) *flatEntry {
	if active < modLookupSize {
		if i := m.modLookup[t.lookup+active]; i != noEntry {
			return &m.flatEntries[i]
		}
		return nil
	}
	wide := m.wideEntries[t.wide : t.wide+uint32(t.numWide)]
	lo, hi := 0, len(wide)
	for lo < hi {
		mid := int(uint(lo+hi) >> 1)
		switch {
		case wide[mid].mask < active:
			lo = mid + 1
		case wide[mid].mask > active:
			hi = mid
		default:
			return &m.flatEntries[wide[mid].entry]
		}
	}
	return nil
}

// NumLayouts is how many layouts (groups) the keymap has.
func (m *Keymap) NumLayouts() uint32 { return m.numGroups }

// LayoutName is the name of layout i, "" when it has none.
func (m *Keymap) LayoutName(i uint32) string {
	if int64(i) >= int64(len(m.groupNames)) {
		return ""
	}
	return m.groupNames[i]
}

// NumLevels is how many shift levels the key has in layout, wrapped into
// the key's own layouts; 0 for a key that does not exist.
func (m *Keymap) NumLevels(kc Keycode, layout uint32) uint32 {
	i := m.keyIndex(kc)
	if i < 0 {
		return 0
	}
	k := &m.flatKeys[i]
	wrapped := wrapGroup(int32(layout), uint32(k.numGroups), k.rangeAct, uint32(k.rangeNum)) //nolint:gosec // a layout index, bounded by the wrap
	if wrapped == layoutInvalid {
		return 0
	}
	return uint32(m.flatTypes[m.flatGroups[k.firstGroup+wrapped].typ].numLevels)
}

// NumLayoutsForKey is how many layouts the key itself has.
func (m *Keymap) NumLayoutsForKey(kc Keycode) uint32 {
	i := m.keyIndex(kc)
	if i < 0 {
		return 0
	}
	return uint32(m.flatKeys[i].numGroups)
}

// KeySymsByLevel is the keysyms of one level of one layout of a key, nil
// when it has none. The slice belongs to the keymap.
func (m *Keymap) KeySymsByLevel(kc Keycode, layout, lvl uint32) []Keysym {
	i := m.keyIndex(kc)
	if i < 0 {
		return nil
	}
	l := m.keyLevel(&m.flatKeys[i], layout, lvl)
	if l == nil || l.numSyms == 0 {
		return nil
	}
	return m.levelSyms(l)
}

// KeyGetName is the key's name without the angle brackets, "" when the
// keycode names no key.
func (m *Keymap) KeyGetName(kc Keycode) string {
	i := m.keyIndex(kc)
	if i < 0 {
		return ""
	}
	return m.keyName(&m.flatKeys[i])
}

// KeyByName is the keycode of a key or key alias name.
func (m *Keymap) KeyByName(name string) (Keycode, bool) {
	lo, hi := 0, len(m.nameIndex)
	for lo < hi {
		mid := int(uint(lo+hi) >> 1)
		e := &m.nameIndex[mid]
		switch c := strings.Compare(m.nameBlob[e.off:e.off+uint32(e.size)], name); {
		case c < 0:
			lo = mid + 1
		case c > 0:
			hi = mid
		default:
			return m.flatKeys[e.key].keycode, true
		}
	}
	return 0, false
}

// KeyRepeats reports whether holding the key repeats it.
func (m *Keymap) KeyRepeats(kc Keycode) bool {
	i := m.keyIndex(kc)
	return i >= 0 && m.flatKeys[i].repeats
}

// KeyModMap is the real modifiers the key's modifier_map entries give it.
func (m *Keymap) KeyModMap(kc Keycode) ModMask {
	i := m.keyIndex(kc)
	if i < 0 {
		return 0
	}
	return ModMask(m.flatKeys[i].modmap)
}
