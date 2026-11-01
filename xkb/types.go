package xkb

import (
	"errors"
	"fmt"
	"strings"
)

type typeInfo struct {
	merge      mergeMode
	name       string
	mods       uint32
	modsSet    bool
	numLevels  uint32
	entries    []typeEntry
	levelNames []string
}

type typesInfo struct {
	name  string
	depth int
	types []*typeInfo
	mods  modSet
	ctx   *session
}

func newTypesInfo(ctx *session, depth int, mods *modSet) *typesInfo {
	info := &typesInfo{depth: depth, mods: *mods, ctx: ctx}
	if depth > 0 {
		info.mods.resetVirtual()
	}
	return info
}

func (info *typesInfo) add(t *typeInfo, sameFile bool) {
	for i, old := range info.types {
		if old.name != t.name {
			continue
		}
		if t.merge != mergeAugment {
			if info.ctx.report(sameFile) {
				info.ctx.warn(warnConflictingTypeDefs, "Multiple definitions of the %s key type; Earlier definition ignored\n", t.name)
			}
			info.types[i] = t
		} else if sameFile {
			info.ctx.verbose(verbosityDetailed, warnConflictingTypeDefs, "Multiple definitions of the %s key type; Later definition ignored\n", t.name)
		}
		return
	}
	info.types = append(info.types, t)
}

func (info *typesInfo) merge(from *typesInfo, merge mergeMode) {
	mergeModSets(&info.mods, &from.mods, merge)
	if info.name == "" {
		info.name = from.name
	}
	if len(info.types) == 0 {
		info.types = from.types
		return
	}
	for _, t := range from.types {
		t.merge = merge
		info.add(t, false)
	}
}

func mergeModSets(into, from *modSet, merge mergeMode) {
	clobber := merge != mergeAugment
	for i := range from.num {
		m := &from.mods[i]
		if m.typ != modVirtual {
			continue
		}
		mask := uint32(1) << i
		if into.mods[i].typ == 0 {
			into.mods[i] = *m
			if from.explicit&mask != 0 {
				into.explicit |= mask
			}
			continue
		}
		switch {
		case from.explicit&mask == 0:
		case into.explicit&mask == 0:
			into.mods[i].mapping = m.mapping
			into.explicit |= mask
		case clobber:
			into.mods[i].mapping = m.mapping
		}
	}
	into.num = from.num
}

func handleVMod(mods *modSet, d vmodDecl) error {
	var mapping uint32
	if d.value != nil {
		var err error
		if mapping, err = resolveModMask(d.value, modReal, mods); err != nil {
			return fmt.Errorf("virtual modifier %s: %w", d.name, err)
		}
	}
	for i := range mods.num {
		m := &mods.mods[i]
		if m.name != d.name {
			continue
		}
		if m.typ != modVirtual {
			return fmt.Errorf("cannot add a virtual modifier named %q: a real modifier has that name", d.name)
		}
		mask := uint32(1) << i
		if d.value == nil {
			return nil
		}
		if mods.explicit&mask == 0 || d.merge != mergeAugment {
			m.mapping = mapping
		}
		mods.explicit |= mask
		return nil
	}
	if mods.num >= maxMods {
		return fmt.Errorf("cannot define virtual modifier %s: more than %d modifiers", d.name, maxMods)
	}
	mods.mods[mods.num] = mod{name: d.name, typ: modVirtual, mapping: mapping}
	if d.value != nil {
		mods.explicit |= 1 << mods.num
	}
	mods.num++
	return nil
}

func (info *typesInfo) handleInclude(inc includeDecl) error {
	if info.depth >= includeMaxDepth {
		return fmt.Errorf("exceeded the include depth of %d at %q", includeMaxDepth, inc.text)
	}
	included := newTypesInfo(info.ctx, info.depth+1, &info.mods)
	included.name = inc.text
	for _, stmt := range inc.stmts {
		f, err := info.ctx.processInclude(stmt, fileTypes)
		if err != nil {
			return err
		}
		next := newTypesInfo(info.ctx, info.depth+1, &included.mods)
		if err := next.handleFile(f); err != nil {
			return err
		}
		included.merge(next, stmt.merge)
	}
	info.merge(included, inc.stmts[0].merge)
	return nil
}

func (info *typesInfo) handleFile(f *xkbFile) error {
	info.name = f.name
	var errs sectionErrors
	for _, d := range f.decls {
		var err error
		switch v := d.(type) {
		case includeDecl:
			err = info.handleInclude(v)
		case typeDecl:
			err = info.handleType(v)
		case varDecl:
			err = errors.New("key type files have no defaults")
		case vmodDecl:
			err = handleVMod(&info.mods, v)
		default:
			err = fmt.Errorf("key type files may not include %T", d)
		}
		if err != nil && errs.add(info.ctx.Context, fmt.Errorf("%s: %w", mapDisplayName(f), err), "keytypes file", mapDisplayName(f)) {
			break
		}
	}
	return errs.err()
}

func (info *typesInfo) handleType(d typeDecl) error {
	t := &typeInfo{merge: d.merge, name: d.name, numLevels: 1}
	var errs []error
	for _, v := range d.body {
		l, err := resolveLhs(v.name)
		if err != nil {
			errs = append(errs, fmt.Errorf("type %s: %w", d.name, err))
			continue
		}
		if l.elem != "" {
			if !strings.EqualFold(l.elem, "type") {
				errs = append(errs, codedf(codeDefaultsScope, "type %s: cannot set defaults for %q", d.name, l.elem))
			}
			continue
		}
		if err := info.setField(t, l, v.value); err != nil {
			errs = append(errs, fmt.Errorf("type %s: %w", d.name, err))
		}
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	info.add(t, true)
	return nil
}

func (info *typesInfo) setField(t *typeInfo, l lhs, value expr) error {
	switch strings.ToLower(l.field) {
	case "modifiers":
		return info.setModifiers(t, l, value)
	case "map":
		return info.setMapEntry(t, l, value)
	case "preserve":
		return info.setPreserve(t, l, value)
	case "levelname", "level_name":
		return info.setLevelName(t, l, value)
	}
	return codedf(codeUnknownField, "unknown field %q", l.field)
}

func (info *typesInfo) setModifiers(t *typeInfo, l lhs, value expr) error {
	if l.index != nil {
		return errors.New("the modifiers field is not an array")
	}
	mods, err := resolveModMask(value, modBoth, &info.mods)
	if err != nil {
		return coded(codeUnsupportedModMask, fmt.Errorf("modifiers: %w", err))
	}
	if t.modsSet {
		info.ctx.warn(0, "Multiple modifier mask definitions for key type %s; Using %#x, ignoring %#x\n", t.name, t.mods, mods)
		return errors.New("multiple modifier mask definitions")
	}
	t.mods, t.modsSet = mods, true
	return nil
}

func (info *typesInfo) setMapEntry(t *typeInfo, l lhs, value expr) error {
	if l.index == nil {
		return errors.New("the map field must be an array")
	}
	mods, err := resolveModMask(l.index, modBoth, &info.mods)
	if err != nil {
		return coded(codeUnsupportedModMask, fmt.Errorf("map entry: %w", err))
	}
	if mods&^t.mods != 0 {
		info.ctx.verbose(verbosityBrief, warnUndeclaredTypeModifiers, "Map entry for modifiers not used by type %s; Using %#x instead of %#x\n", t.name, mods&t.mods, mods)
	}
	mods &= t.mods
	lvl, err := resolveLevel(value)
	if err != nil {
		return coded(codeShiftLevel, fmt.Errorf("map entry: %w", err))
	}
	if lvl >= t.numLevels {
		t.numLevels = lvl + 1
	}
	for i := range t.entries {
		if t.entries[i].mods == mods {
			if t.entries[i].level != lvl {
				info.ctx.warn(warnConflictingMapEntry, "Multiple map entries for %#x in %s; Using %d, ignoring %d\n", mods, t.name, lvl+1, t.entries[i].level+1)
			} else {
				info.ctx.verbose(verbosityVerbose, warnConflictingMapEntry, "Multiple occurrences of map[%#x]= %d in %s; Ignored\n", mods, lvl+1, t.name)
			}
			t.entries[i].level = lvl
			return nil
		}
	}
	t.entries = append(t.entries, typeEntry{level: lvl, mods: mods})
	return nil
}

func (info *typesInfo) setPreserve(t *typeInfo, l lhs, value expr) error {
	if l.index == nil {
		return errors.New("the preserve field must be an array")
	}
	mods, err := resolveModMask(l.index, modBoth, &info.mods)
	if err != nil {
		return coded(codeUnsupportedModMask, fmt.Errorf("preserve entry: %w", err))
	}
	if mods&^t.mods != 0 {
		info.ctx.verbose(verbosityBrief, warnUndeclaredTypeModifiers, "Preserve entry for modifiers not used by the %s type; Index %#x converted to %#x\n", t.name, mods, mods&t.mods)
	}
	mods &= t.mods
	preserve, err := resolveModMask(value, modBoth, &info.mods)
	if err != nil {
		return coded(codeUnsupportedModMask, fmt.Errorf("preserve value: %w", err))
	}
	if preserve&^mods != 0 {
		info.ctx.verbose(verbosityBrief, warnIllegalPreserveResult, "Illegal value for preserve[%#x] in type %s; Converted %#x to %#x\n", mods, t.name, preserve, preserve&mods)
	}
	preserve &= mods
	for i := range t.entries {
		if t.entries[i].mods == mods {
			switch old := t.entries[i].preserve; {
			case old == preserve && old != 0:
				info.ctx.verbose(verbosityVerbose, warnDuplicateEntry, "Identical definitions for preserve[%#x] in %s; Ignored\n", mods, t.name)
			case old != 0:
				info.ctx.verbose(verbosityBrief, warnConflictingPreserve, "Multiple definitions for preserve[%#x] in %s; Using %#x, ignoring %#x\n", mods, t.name, preserve, old)
			}
			t.entries[i].preserve = preserve
			return nil
		}
	}
	t.entries = append(t.entries, typeEntry{mods: mods, preserve: preserve})
	return nil
}

func (info *typesInfo) setLevelName(t *typeInfo, l lhs, value expr) error {
	if l.index == nil {
		return errors.New("the level name field must be an array")
	}
	lvl, err := resolveLevel(l.index)
	if err != nil {
		return coded(codeShiftLevel, fmt.Errorf("level name: %w", err))
	}
	name, err := resolveString(value)
	if err != nil {
		return fmt.Errorf("level %d name: %w", lvl+1, err)
	}
	for int(lvl) >= len(t.levelNames) {
		t.levelNames = append(t.levelNames, "")
	}
	if old := t.levelNames[lvl]; old == name {
		info.ctx.verbose(verbosityVerbose, warnDuplicateEntry, "Duplicate names for level %d of key type %s; Ignored\n", lvl+1, t.name)
		return nil
	} else if old != "" {
		info.ctx.verbose(verbosityBrief, warnConflictingLevelNames, "Multiple names for level %d of key type %s; Using %s, ignoring %s\n", lvl+1, t.name, name, old)
	}
	t.levelNames[lvl] = name
	return nil
}

var requiredTypes = map[string]bool{"ONE_LEVEL": true, "TWO_LEVEL": true, "ALPHABETIC": true, "KEYPAD": true}

func (m *Keymap) compileTypes(f *xkbFile, ctx *session) error {
	info := newTypesInfo(ctx, 0, &m.mods)
	if f != nil {
		if err := info.handleFile(f); err != nil {
			return err
		}
	}
	if len(info.types) == 0 {
		m.types = []*keyType{{name: "ONE_LEVEL", numLevels: 1, required: true}}
	} else {
		m.types = make([]*keyType, len(info.types))
		for i, t := range info.types {
			m.types[i] = &keyType{
				name:       t.name,
				mods:       t.mods,
				numLevels:  t.numLevels,
				levelNames: t.levelNames,
				entries:    t.entries,
				required:   t.numLevels <= 2 && requiredTypes[t.name],
			}
		}
	}
	m.sections[fileTypes] = escapeMapName(info.name)
	m.mods = info.mods
	return nil
}
