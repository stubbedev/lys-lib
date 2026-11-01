package xkb

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

type groupField uint8

const (
	groupSyms groupField = 1 << iota
	groupActs
	groupType
)

type keyField uint8

const (
	keyRepeat keyField = 1 << iota
	keyDefaultType
	keyGroupInfo
	keyVmodmap
)

type keyRepeatSetting uint8

const (
	repeatUndefined keyRepeatSetting = iota
	repeatYes
	repeatNo
)

var repeatEntries = []lookupEntry{
	{"true", uint32(repeatYes)},
	{"yes", uint32(repeatYes)},
	{"on", uint32(repeatYes)},
	{"false", uint32(repeatNo)},
	{"no", uint32(repeatNo)},
	{"off", uint32(repeatNo)},
	{"default", uint32(repeatUndefined)},
}

type groupInfo struct {
	defined groupField
	levels  []level
	typ     string
}

func (g *groupInfo) clone() groupInfo {
	out := *g
	out.levels = make([]level, len(g.levels))
	for i := range g.levels {
		out.levels[i] = level{syms: slices.Clone(g.levels[i].syms), actions: slices.Clone(g.levels[i].actions)}
	}
	return out
}

type keyInfo struct {
	defined     keyField
	merge       mergeMode
	name        string
	groups      []groupInfo
	repeat      keyRepeatSetting
	vmodmap     uint32
	defaultType string
	rangeAct    rangeAction
	rangeNum    uint32
}

func (k *keyInfo) clone() *keyInfo {
	out := *k
	out.groups = make([]groupInfo, len(k.groups))
	for i := range k.groups {
		out.groups[i] = k.groups[i].clone()
	}
	return &out
}

type modMapEntry struct {
	merge      mergeMode
	haveSymbol bool
	modifier   uint32
	keyName    string
	sym        Keysym
}

type symbolsInfo struct {
	name          string
	depth         int
	explicitGroup uint32
	keys          []*keyInfo
	keysByName    map[string]*keyInfo
	defaultKey    *keyInfo
	defaults      *actionDefaults
	groupNames    []string
	modmaps       []modMapEntry
	mods          modSet
	keymap        *Keymap
	ctx           *session
}

func newSymbolsInfo(ctx *session, keymap *Keymap, depth int, mods *modSet) *symbolsInfo {
	info := &symbolsInfo{
		depth:         depth,
		explicitGroup: layoutInvalid,
		defaultKey:    &keyInfo{name: "*"},
		defaults:      newActionDefaults(),
		mods:          *mods,
		keymap:        keymap,
		ctx:           ctx,
	}
	if depth > 0 {
		info.mods.resetVirtual()
	}
	return info
}

func mergeSyms(into, from *level, clobber bool) int {
	switch {
	case len(from.syms) == 0:
	case clobber:
		into.syms = from.syms
		return 1
	case len(into.syms) == 0:
		into.syms = from.syms
		return 1
	}
	return 0
}

func mergeActions(into, from *level, clobber bool) int {
	switch {
	case len(from.actions) == 0:
	case clobber:
		into.actions = from.actions
		return 1
	case len(into.actions) == 0:
		into.actions = from.actions
		return 1
	}
	return 0
}

func mergeGroups(into, from *groupInfo, clobber bool) {
	if into.typ != from.typ {
		if into.typ == "" || (from.typ != "" && clobber) {
			into.typ = from.typ
		}
	}
	into.defined |= from.defined & groupType
	if len(from.levels) == 0 {
		return
	}
	if len(into.levels) == 0 {
		typ := into.typ
		*into = *from
		into.typ = typ
		return
	}
	both := min(len(into.levels), len(from.levels))
	fromSyms, fromActs := 0, 0
	for i := range both {
		il, fl := &into.levels[i], &from.levels[i]
		if len(fl.syms) == 0 && len(fl.actions) == 0 {
			continue
		}
		if len(il.syms) == 0 && len(il.actions) == 0 {
			*il = *fl
			fromSyms++
			fromActs++
			continue
		}
		if !fl.sameSyms(il) {
			fromSyms += mergeSyms(il, fl, clobber)
		}
		if !il.sameActions(fl) {
			fromActs += mergeActions(il, fl, clobber)
		}
	}
	for i := both; i < len(from.levels); i++ {
		into.levels = append(into.levels, from.levels[i])
		fromSyms++
		fromActs++
	}
	if fromSyms > 0 {
		if fromSyms == len(into.levels) {
			into.defined &^= groupSyms
		}
		into.defined |= from.defined & groupSyms
	}
	if fromActs > 0 {
		if fromActs == len(into.levels) {
			into.defined &^= groupActs
		}
		into.defined |= from.defined & groupActs
	}
}

func mergeKeys(into, from *keyInfo) {
	clobber := from.merge != mergeAugment
	if from.merge == mergeReplace {
		*into = *from
		return
	}
	both := min(len(into.groups), len(from.groups))
	for i := range both {
		mergeGroups(&into.groups[i], &from.groups[i], clobber)
	}
	into.groups = append(into.groups, from.groups[both:]...)
	if useNewField(keyVmodmap, into.defined, from.defined, clobber) {
		into.vmodmap = from.vmodmap
		into.defined |= keyVmodmap
	}
	if useNewField(keyRepeat, into.defined, from.defined, clobber) {
		into.repeat = from.repeat
		into.defined |= keyRepeat
	}
	if useNewField(keyDefaultType, into.defined, from.defined, clobber) {
		into.defaultType = from.defaultType
		into.defined |= keyDefaultType
	}
	if useNewField(keyGroupInfo, into.defined, from.defined, clobber) {
		into.rangeAct = from.rangeAct
		into.rangeNum = from.rangeNum
		into.defined |= keyGroupInfo
	}
}

func (info *symbolsInfo) addKey(k *keyInfo) {
	k.name = info.keymap.compileResolveAlias(k.name)
	if info.keysByName == nil {
		info.keysByName = make(map[string]*keyInfo, len(info.keys)+1)
		for _, old := range info.keys {
			info.keysByName[old.name] = old
		}
	}
	if old, ok := info.keysByName[k.name]; ok {
		mergeKeys(old, k)
		return
	}
	info.keysByName[k.name] = k
	info.keys = append(info.keys, k)
}

func (info *symbolsInfo) addModMap(entry modMapEntry) {
	clobber := entry.merge != mergeAugment
	for i := range info.modmaps {
		old := &info.modmaps[i]
		if entry.haveSymbol != old.haveSymbol || (entry.haveSymbol && entry.sym != old.sym) || (!entry.haveSymbol && entry.keyName != old.keyName) {
			continue
		}
		if clobber {
			old.modifier = entry.modifier
		}
		return
	}
	info.modmaps = append(info.modmaps, entry)
}

func (info *symbolsInfo) merge(from *symbolsInfo, merge mergeMode) {
	mergeModSets(&info.mods, &from.mods, merge)
	if info.name == "" {
		info.name = from.name
	}
	both := min(len(info.groupNames), len(from.groupNames))
	for i := range both {
		if from.groupNames[i] == "" || (merge == mergeAugment && info.groupNames[i] != "") {
			continue
		}
		info.groupNames[i] = from.groupNames[i]
	}
	info.groupNames = append(info.groupNames, from.groupNames[both:]...)
	if len(info.keys) == 0 {
		info.keys, info.keysByName = from.keys, from.keysByName
	} else {
		for _, k := range from.keys {
			k.merge = merge
			info.addKey(k)
		}
	}
	if len(info.modmaps) == 0 {
		info.modmaps = from.modmaps
	} else {
		for _, mm := range from.modmaps {
			mm.merge = merge
			info.addModMap(mm)
		}
	}
}

func explicitGroupFromModifier(s string) uint32 {
	end := 0
	for end < len(s) && isDigit(s[end]) {
		end++
	}
	v, err := strconv.ParseInt(s[:end], 10, 32)
	if err != nil {
		v = 0
	}
	return uint32(v) - 1 //nolint:gosec // atoi-style wraparound is the out of range value
}

func (info *symbolsInfo) handleInclude(inc includeDecl) error {
	if info.depth >= includeMaxDepth {
		return fmt.Errorf("exceeded the include depth of %d at %q", includeMaxDepth, inc.text)
	}
	included := newSymbolsInfo(info.ctx, info.keymap, info.depth+1, &info.mods)
	included.name = inc.text
	for _, stmt := range inc.stmts {
		f, err := info.ctx.processInclude(stmt, fileSymbols)
		if err != nil {
			return err
		}
		next := newSymbolsInfo(info.ctx, info.keymap, info.depth+1, &included.mods)
		switch {
		case stmt.modifier != "":
			next.explicitGroup = explicitGroupFromModifier(stmt.modifier)
			if next.explicitGroup >= maxGroupsV1 {
				next.explicitGroup = info.explicitGroup
			}
		case info.keymap.numGroups != 0 && next.depth == 1:
			next.explicitGroup = 0
		default:
			next.explicitGroup = info.explicitGroup
		}
		if err := next.handleFile(f); err != nil {
			return err
		}
		included.merge(next, stmt.merge)
	}
	info.merge(included, inc.stmts[0].merge)
	return nil
}

func (info *symbolsInfo) handleFile(f *xkbFile) error {
	info.name = f.name
	for _, d := range f.decls {
		var err error
		switch v := d.(type) {
		case includeDecl:
			err = info.handleInclude(v)
		case symbolsDecl:
			err = info.handleSymbols(v)
		case varDecl:
			err = info.handleGlobalVar(v)
		case vmodDecl:
			err = handleVMod(&info.mods, v)
		case modMapDecl:
			err = info.handleModMap(v)
		default:
			err = fmt.Errorf("symbols files may not include %T", d)
		}
		if err != nil {
			return fmt.Errorf("%s: %w", mapDisplayName(f), err)
		}
	}
	return nil
}

func (info *symbolsInfo) groupIndex(k *keyInfo, index expr, field groupField) (uint32, error) {
	if index == nil {
		for i := range k.groups {
			if k.groups[i].defined&field == 0 {
				return uint32(i), nil
			}
		}
		if len(k.groups) >= maxGroupsV1 {
			return 0, fmt.Errorf("too many groups for key <%s>", k.name)
		}
		k.groups = append(k.groups, groupInfo{})
		return uint32(len(k.groups) - 1), nil //nolint:gosec // at most maxGroupsV1
	}
	g, err := resolveGroup(index)
	if err != nil {
		return 0, fmt.Errorf("group index of key <%s>: %w", k.name, err)
	}
	g--
	for int(g) >= len(k.groups) {
		k.groups = append(k.groups, groupInfo{})
	}
	return g, nil
}

func (info *symbolsInfo) addSymbols(k *keyInfo, index, value expr) error {
	ndx, err := info.groupIndex(k, index, groupSyms)
	if err != nil {
		return err
	}
	g := &k.groups[ndx]
	if _, empty := value.(exprEmptyList); empty {
		g.defined |= groupSyms
		return nil
	}
	list, ok := value.(exprSymsLevels)
	if !ok {
		return fmt.Errorf("expected a list of symbols for key <%s>, found %T", k.name, value)
	}
	if g.defined&groupSyms != 0 {
		return fmt.Errorf("symbols for key <%s> group %d already defined", k.name, ndx+1)
	}
	levels := list.levels
	for len(levels) > 0 && len(levels[len(levels)-1].syms) == 0 {
		levels = levels[:len(levels)-1]
	}
	for len(g.levels) < len(levels) {
		g.levels = append(g.levels, level{})
	}
	g.defined |= groupSyms
	for i, l := range levels {
		g.levels[i].syms = l.syms
	}
	return nil
}

func (info *symbolsInfo) addActions(k *keyInfo, index, value expr) error {
	ndx, err := info.groupIndex(k, index, groupActs)
	if err != nil {
		return err
	}
	g := &k.groups[ndx]
	if _, empty := value.(exprEmptyList); empty {
		g.defined |= groupActs
		return nil
	}
	var lists []exprActionList
	switch v := value.(type) {
	case exprActsLevels:
		lists = v.levels
	case exprActionList:
		lists = []exprActionList{v}
	default:
		return fmt.Errorf("bad expression %T for the actions of key <%s>", value, k.name)
	}
	if g.defined&groupActs != 0 {
		return fmt.Errorf("actions for key <%s> group %d already defined", k.name, ndx+1)
	}
	for len(g.levels) < len(lists) {
		g.levels = append(g.levels, level{})
	}
	g.defined |= groupActs
	nonEmpty := 0
	for i, list := range lists {
		l := &g.levels[i]
		var actions []action
		for _, def := range list.actions {
			a, err := info.defaults.parse(&info.mods, def)
			if err != nil {
				a = action{typ: actNone}
			}
			if a.typ != actNone {
				actions = append(actions, a)
			}
		}
		l.actions = actions
		if len(l.actions) > 0 || len(l.syms) > 0 {
			nonEmpty = i + 1
		}
	}
	if nonEmpty < len(lists) {
		g.levels = g.levels[:nonEmpty]
	}
	return nil
}

func (info *symbolsInfo) setField(k *keyInfo, field string, index, value expr) error {
	name := strings.ToLower(field)
	switch name {
	case "type":
		return info.setType(k, index, value)
	case "symbols":
		return info.addSymbols(k, index, value)
	case "actions":
		return info.addActions(k, index, value)
	case "vmods", "virtualmods", "virtualmodifiers":
		mask, err := resolveModMask(value, modVirtual, &info.mods)
		if err != nil {
			return fmt.Errorf("virtual modifiers of key <%s>: %w", k.name, err)
		}
		k.vmodmap = mask
		k.defined |= keyVmodmap
		return nil
	case "locking", "lock", "locks", "radiogroup", "permanentradiogroup", "allownone":
		return nil
	case "repeating", "repeats", "repeat":
		v, err := resolveEnum(value, repeatEntries)
		if err != nil {
			return fmt.Errorf("repeat of key <%s>: %w", k.name, err)
		}
		k.repeat = keyRepeatSetting(v) //nolint:gosec // the table's own values
		k.defined |= keyRepeat
		return nil
	case "groupswrap", "wrapgroups":
		set, err := resolveBoolean(value)
		if err != nil {
			return fmt.Errorf("groupsWrap of key <%s>: %w", k.name, err)
		}
		k.rangeAct = rangeSaturate
		if set {
			k.rangeAct = rangeWrap
		}
		k.defined |= keyGroupInfo
		return nil
	case "groupsclamp", "clampgroups":
		set, err := resolveBoolean(value)
		if err != nil {
			return fmt.Errorf("groupsClamp of key <%s>: %w", k.name, err)
		}
		k.rangeAct = rangeWrap
		if set {
			k.rangeAct = rangeSaturate
		}
		k.defined |= keyGroupInfo
		return nil
	case "groupsredirect", "redirectgroups":
		g, err := resolveGroup(value)
		if err != nil {
			return fmt.Errorf("groupsRedirect of key <%s>: %w", k.name, err)
		}
		k.rangeAct = rangeRedirect
		k.rangeNum = g - 1
		k.defined |= keyGroupInfo
		return nil
	}
	if strings.HasPrefix(name, "overlay") || strings.HasPrefix(name, "permanentoverlay") {
		return nil
	}
	return fmt.Errorf("unknown field %q for key <%s>", field, k.name)
}

func (info *symbolsInfo) setType(k *keyInfo, index, value expr) error {
	val, err := resolveString(value)
	if err != nil {
		return fmt.Errorf("the type of key <%s>: %w", k.name, err)
	}
	if index == nil {
		k.defaultType = val
		k.defined |= keyDefaultType
		return nil
	}
	g, err := resolveGroup(index)
	if err != nil {
		return fmt.Errorf("the type of key <%s>: %w", k.name, err)
	}
	g--
	for int(g) >= len(k.groups) {
		k.groups = append(k.groups, groupInfo{})
	}
	k.groups[g].typ = val
	k.groups[g].defined |= groupType
	return nil
}

func (info *symbolsInfo) setGroupName(index, value expr, merge mergeMode) error {
	if index == nil {
		return errors.New("a group name needs an index")
	}
	group, err := resolveGroup(index)
	if err != nil {
		return fmt.Errorf("group name: %w", err)
	}
	name, err := resolveString(value)
	if err != nil {
		return fmt.Errorf("group %d name: %w", group, err)
	}
	target := group - 1
	if info.explicitGroup != layoutInvalid {
		if group != 1 {
			return fmt.Errorf("an explicit group was given, but map %q names group %d", info.name, group)
		}
		target = info.explicitGroup
	}
	if int(target) >= len(info.groupNames) {
		for int(target) >= len(info.groupNames) {
			info.groupNames = append(info.groupNames, "")
		}
	} else if old := info.groupNames[target]; old != "" && old != name && merge == mergeAugment {
		name = old
	}
	info.groupNames[target] = name
	return nil
}

func (info *symbolsInfo) handleGlobalVar(v varDecl) error {
	l, err := resolveLhs(v.name)
	if err != nil {
		return err
	}
	if strings.EqualFold(l.elem, "key") {
		temp := &keyInfo{name: "*", merge: v.merge}
		if err := info.setField(temp, l.field, l.index, v.value); err != nil {
			return err
		}
		mergeKeys(info.defaultKey, temp)
		return nil
	}
	if l.elem != "" {
		return info.defaults.setField(&info.mods, l.elem, l.field, l.index, v.value, v.merge)
	}
	switch strings.ToLower(l.field) {
	case "name", "groupname":
		return info.setGroupName(l.index, v.value, v.merge)
	case "groupswrap", "wrapgroups", "groupsclamp", "clampgroups", "groupsredirect", "redirectgroups", "allownone":
		return nil
	}
	return fmt.Errorf("default defined for unknown field %q", l.field)
}

func (info *symbolsInfo) handleSymbols(d symbolsDecl) error {
	k := info.defaultKey.clone()
	k.merge = d.merge
	k.name = d.name
	for _, v := range d.body {
		field := "symbols"
		var index expr
		if v.name == nil {
			if _, isActions := v.value.(exprActsLevels); isActions {
				field = "actions"
			}
		} else {
			l, err := resolveLhs(v.name)
			if err != nil {
				return err
			}
			if l.elem != "" {
				return fmt.Errorf("cannot set defaults for %q in a key statement", l.elem)
			}
			field, index = l.field, l.index
		}
		if err := info.setField(k, field, index, v.value); err != nil {
			return err
		}
	}
	info.setExplicitGroup(k)
	info.addKey(k)
	return nil
}

func (info *symbolsInfo) setExplicitGroup(k *keyInfo) {
	if info.explicitGroup == layoutInvalid {
		return
	}
	for i := 1; i < len(k.groups); i++ {
		if k.groups[i].defined != 0 {
			k.groups[i] = groupInfo{}
		}
	}
	groups := make([]groupInfo, info.explicitGroup+1)
	copy(groups, k.groups)
	k.groups = groups
	if info.explicitGroup > 0 {
		k.groups[info.explicitGroup] = k.groups[0]
		k.groups[0] = groupInfo{}
	}
}

func (info *symbolsInfo) handleModMap(d modMapDecl) error {
	var modifier uint32
	if strings.EqualFold(d.modifier, "none") {
		modifier = modNone
	} else {
		i, ok := info.mods.index(d.modifier, modReal)
		if !ok {
			return fmt.Errorf("modifier map for non-modifier %q", d.modifier)
		}
		modifier = i
	}
	for _, e := range d.keys {
		entry := modMapEntry{merge: d.merge, modifier: modifier}
		switch v := e.(type) {
		case exprKeyName:
			entry.keyName = v.name
		case exprKeysym:
			if v.sym == KeyNoSymbol {
				continue
			}
			entry.haveSymbol = true
			entry.sym = v.sym
		default:
			continue
		}
		info.addModMap(entry)
	}
	return nil
}

func (m *Keymap) findKeyForSymbol(sym Keysym) *key {
	for group := uint32(0); ; group++ {
		gotGroup := false
		for lvl := uint32(0); ; lvl++ {
			gotLevel := false
			for i := range m.keys {
				k := &m.keys[i]
				if group >= k.numGroups || lvl >= k.groups[group].typ.numLevels {
					continue
				}
				gotGroup, gotLevel = true, true
				if slices.Contains(k.groups[group].levels[lvl].syms, sym) {
					return k
				}
			}
			if !gotLevel {
				break
			}
		}
		if !gotGroup {
			return nil
		}
	}
}

func firstSym(g *groupInfo, lvl int) Keysym {
	if syms := g.levels[lvl].syms; len(syms) > 0 {
		return syms[0]
	}
	return KeyNoSymbol
}

func automaticType(g *groupInfo) string {
	width := len(g.levels)
	if width <= 1 {
		return "ONE_LEVEL"
	}
	sym0, sym1 := firstSym(g, 0), firstSym(g, 1)
	if width == 2 {
		if keysymIsLower(sym0) && keysymIsUpperOrTitle(sym1) {
			return "ALPHABETIC"
		}
		if KeysymIsKeypad(sym0) || KeysymIsKeypad(sym1) {
			return "KEYPAD"
		}
		return "TWO_LEVEL"
	}
	if width > 4 {
		return ""
	}
	if keysymIsLower(sym0) && keysymIsUpperOrTitle(sym1) {
		sym2, sym3 := firstSym(g, 2), KeyNoSymbol
		if width == 4 {
			sym3 = firstSym(g, 3)
		}
		if keysymIsLower(sym2) && keysymIsUpperOrTitle(sym3) {
			return "FOUR_LEVEL_ALPHABETIC"
		}
		return "FOUR_LEVEL_SEMIALPHABETIC"
	}
	if KeysymIsKeypad(sym0) || KeysymIsKeypad(sym1) {
		return "FOUR_LEVEL_KEYPAD"
	}
	return "FOUR_LEVEL"
}

func (m *Keymap) typeForGroup(k *keyInfo, g *groupInfo) (*keyType, bool) {
	name := g.typ
	explicit := true
	if name == "" {
		if k.defaultType != "" {
			name = k.defaultType
		} else if name = automaticType(g); name != "" {
			explicit = false
		}
	}
	if name != "" {
		for _, t := range m.types {
			if t.name == name {
				t.required = true
				return t, explicit
			}
		}
	}
	m.types[0].required = true
	return m.types[0], explicit
}

func upperSyms(syms []Keysym) []Keysym {
	for i, s := range syms {
		if KeysymToUpper(s) == s {
			continue
		}
		upper := slices.Clone(syms)
		for j := i; j < len(upper); j++ {
			upper[j] = KeysymToUpper(upper[j])
		}
		return upper
	}
	return syms
}

func (m *Keymap) copyKey(ki *keyInfo) {
	k := m.keyByName(ki.name, false)
	if k == nil {
		return
	}
	k.numGroups = 0
	for i := range ki.groups {
		g := &ki.groups[i]
		hasExplicit := ki.defined&keyDefaultType != 0 || g.defined&groupType != 0
		if len(g.levels) > 0 || hasExplicit {
			k.numGroups = uint32(i + 1)
		}
		if hasExplicit {
			k.explicit |= explicitTypes
		}
	}
	if k.numGroups == 0 {
		if ki.defined == 0 {
			return
		}
		m.copyKeyFields(k, ki)
		return
	}
	groups := ki.groups[:k.numGroups]
	for i := 1; i < len(groups); i++ {
		if groups[i].defined == 0 {
			groups[i] = groups[0].clone()
		}
	}
	k.groups = make([]keyGroup, k.numGroups)
	for i := range groups {
		g := &groups[i]
		t, explicit := m.typeForGroup(ki, g)
		levels := make([]level, t.numLevels)
		copy(levels, g.levels)
		k.groups[i] = keyGroup{explicitType: explicit, typ: t, levels: levels}
	}
	for i := range groups {
		g := &groups[i]
		levels := k.groups[i].levels
		for l := range levels {
			levels[l].upper = upperSyms(levels[l].syms)
		}
		if k.groups[i].typ.numLevels > 1 || len(levels[0].syms) > 0 {
			k.explicit |= explicitSymbols
		}
		if g.defined&groupActs != 0 {
			k.groups[i].explicitActions = true
			k.explicit |= explicitInterp
		}
		if k.groups[i].explicitType {
			k.explicit |= explicitTypes
		}
	}
	k.rangeNum = ki.rangeNum
	k.rangeAct = ki.rangeAct
	m.copyKeyFields(k, ki)
}

func (m *Keymap) copyKeyFields(k *key, ki *keyInfo) {
	if ki.defined&keyVmodmap != 0 {
		k.vmodmap = ki.vmodmap
		k.explicit |= explicitVmodmap
	}
	if ki.repeat != repeatUndefined {
		k.repeats = ki.repeat == repeatYes
		k.explicit |= explicitRepeat
	}
}

func (m *Keymap) copyModMap(entry modMapEntry) {
	var k *key
	if entry.haveSymbol {
		k = m.findKeyForSymbol(entry.sym)
	} else {
		k = m.keyByName(entry.keyName, true)
	}
	if k == nil || entry.modifier == modNone {
		return
	}
	k.modmap |= 1 << entry.modifier
}

func (m *Keymap) compileSymbols(f *xkbFile, ctx *session) error {
	info := newSymbolsInfo(ctx, m, 0, &m.mods)
	if f != nil {
		if err := info.handleFile(f); err != nil {
			return err
		}
	}
	m.sections[fileSymbols] = escapeMapName(info.name)
	m.mods = info.mods
	m.groupNames = info.groupNames
	for _, ki := range info.keys {
		m.copyKey(ki)
	}
	for _, mm := range info.modmaps {
		m.copyModMap(mm)
	}
	return nil
}
