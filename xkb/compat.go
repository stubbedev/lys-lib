package xkb

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

type siField uint8

const (
	siVirtualMod siField = 1 << iota
	siAction
	siRepeat
	siLevelOneOnly
)

type interpInfo struct {
	defined siField
	merge   mergeMode
	in      interpret
}

type ledField uint8

const (
	ledMods ledField = 1 << iota
	ledGroups
	ledCtrls
)

type ledInfo struct {
	defined ledField
	merge   mergeMode
	led     led
}

type compatInfo struct {
	name          string
	depth         int
	defaultInterp interpInfo
	interps       []*interpInfo
	defaultLED    ledInfo
	leds          []*ledInfo
	format        KeymapFormat
	defaults      *actionDefaults
	mods          modSet
	ctx           *session
}

func newCompatInfo(ctx *session, format KeymapFormat, depth int, mods *modSet) *compatInfo {
	info := &compatInfo{depth: depth, format: format, mods: *mods, ctx: ctx, defaults: newActionDefaults()}
	info.defaultInterp.in.virtualMod = modIndexInvalid
	if depth > 0 {
		info.mods.resetVirtual()
	}
	return info
}

func useNewField[T ~uint8](field, old, new T, clobber bool) bool {
	if old&field == 0 {
		return new&field != 0
	}
	return new&field != 0 && clobber
}

func collides[T ~uint8](report bool, fields, old, new T) bool {
	return report && old&new&fields != 0
}

func (info *compatInfo) mergeInterp(old, new *interpInfo, sameFile bool) {
	report := info.ctx.report(sameFile)
	if new.merge == mergeReplace {
		if report {
			info.ctx.warn(0, "Multiple definitions for \"%s\"; Earlier interpretation ignored\n", KeysymGetName(new.in.sym))
		}
		*old = *new
		return
	}
	clobber := new.merge != mergeAugment
	if collides(report, siVirtualMod|siAction|siRepeat|siLevelOneOnly, old.defined, new.defined) {
		info.ctx.warn(0, "Multiple interpretations of \"%s\"; Using %s definition for duplicate fields\n", KeysymGetName(old.in.sym), useFirstOrLast(clobber))
	}
	if useNewField(siVirtualMod, old.defined, new.defined, clobber) {
		old.in.virtualMod = new.in.virtualMod
		old.defined |= siVirtualMod
	}
	if useNewField(siAction, old.defined, new.defined, clobber) {
		old.in.actions = new.in.actions
		old.defined |= siAction
	}
	if useNewField(siRepeat, old.defined, new.defined, clobber) {
		old.in.repeat = new.in.repeat
		old.defined |= siRepeat
	}
	if useNewField(siLevelOneOnly, old.defined, new.defined, clobber) {
		old.in.levelOneOnly = new.in.levelOneOnly
		old.defined |= siLevelOneOnly
	}
}

func (info *compatInfo) addInterp(new *interpInfo, sameFile bool) {
	for _, old := range info.interps {
		if old.in.sym == new.in.sym && old.in.mods == new.in.mods && old.in.match == new.in.match {
			info.mergeInterp(old, new, sameFile)
			return
		}
	}
	info.interps = append(info.interps, new)
}

func (info *compatInfo) mergeLED(old, new *ledInfo, sameFile bool) {
	report := info.ctx.report(sameFile)
	if old.led.mods == new.led.mods && old.led.groups == new.led.groups && old.led.ctrls == new.led.ctrls &&
		old.led.whichMods == new.led.whichMods && old.led.whichGroups == new.led.whichGroups {
		old.defined |= new.defined
		return
	}
	if new.merge == mergeReplace {
		if report {
			info.ctx.warn(0, "Map for indicator %s redefined; Earlier definition ignored\n", old.led.name)
		}
		*old = *new
		return
	}
	clobber := new.merge != mergeAugment
	if collides(report, ledMods|ledGroups|ledCtrls, old.defined, new.defined) {
		info.ctx.warn(0, "Map for indicator %s redefined; Using %s definition for duplicate fields\n", old.led.name, useFirstOrLast(clobber))
	}
	if useNewField(ledMods, old.defined, new.defined, clobber) {
		old.led.whichMods = new.led.whichMods
		old.led.mods = new.led.mods
		old.defined |= ledMods
	}
	if useNewField(ledGroups, old.defined, new.defined, clobber) {
		old.led.whichGroups = new.led.whichGroups
		old.led.groups = new.led.groups
		old.defined |= ledGroups
	}
	if useNewField(ledCtrls, old.defined, new.defined, clobber) {
		old.led.ctrls = new.led.ctrls
		old.defined |= ledCtrls
	}
}

func (info *compatInfo) addLED(new *ledInfo, sameFile bool) error {
	for _, old := range info.leds {
		if old.led.name == new.led.name {
			info.mergeLED(old, new, sameFile)
			return nil
		}
	}
	if len(info.leds) >= maxLEDs {
		return fmt.Errorf("more than %d LEDs defined", maxLEDs)
	}
	info.leds = append(info.leds, new)
	return nil
}

func (info *compatInfo) merge(from *compatInfo, merge mergeMode) error {
	mergeModSets(&info.mods, &from.mods, merge)
	if info.name == "" {
		info.name = from.name
	}
	if len(info.interps) == 0 {
		info.interps = from.interps
	} else {
		for _, si := range from.interps {
			si.merge = merge
			info.addInterp(si, false)
		}
	}
	if len(info.leds) == 0 {
		info.leds = from.leds
		return nil
	}
	for _, l := range from.leds {
		l.merge = merge
		if err := info.addLED(l, false); err != nil {
			return err
		}
	}
	return nil
}

func (info *compatInfo) handleInclude(inc includeDecl) error {
	if info.depth >= includeMaxDepth {
		return fmt.Errorf("exceeded the include depth of %d at %q", includeMaxDepth, inc.text)
	}
	included := newCompatInfo(info.ctx, info.format, info.depth+1, &info.mods)
	included.name = inc.text
	for _, stmt := range inc.stmts {
		f, err := info.ctx.processInclude(stmt, fileCompat)
		if err != nil {
			return err
		}
		next := newCompatInfo(info.ctx, info.format, info.depth+1, &included.mods)
		next.defaultInterp = info.defaultInterp
		next.defaultLED = info.defaultLED
		if err := next.handleFile(f); err != nil {
			return err
		}
		if err := included.merge(next, stmt.merge); err != nil {
			return err
		}
	}
	return info.merge(included, inc.stmts[0].merge)
}

func (info *compatInfo) handleFile(f *xkbFile) error {
	info.name = f.name
	var errs sectionErrors
	for _, d := range f.decls {
		var err error
		switch v := d.(type) {
		case includeDecl:
			err = info.handleInclude(v)
		case interpDecl:
			err = info.handleInterp(v)
		case groupCompatDecl:
		case ledMapDecl:
			err = info.handleLEDMap(v)
		case varDecl:
			err = info.handleGlobalVar(v)
		case vmodDecl:
			err = handleVMod(&info.mods, v)
		default:
			err = fmt.Errorf("compat files may not include %T", d)
		}
		if err != nil && errs.add(info.ctx.Context, fmt.Errorf("%s: %w", mapDisplayName(f), err), "compatibility map", mapDisplayName(f)) {
			break
		}
	}
	return errs.err()
}

func (info *compatInfo) resolvePredicate(e expr) (matchOp, uint32, error) {
	if e == nil {
		return matchAnyOrNone, realModMask, nil
	}
	pred := matchExactly
	switch v := e.(type) {
	case exprAction:
		p, ok := lookupName(matchNames, v.name)
		if !ok || len(v.args) != 1 {
			return 0, 0, fmt.Errorf("illegal modifier predicate %q", v.name)
		}
		pred = matchOp(p) //nolint:gosec // the table's own values
		e = v.args[0]
	case exprIdent:
		if strings.EqualFold(v.name, "any") {
			return matchAny, realModMask, nil
		}
	}
	mods, err := resolveModMask(e, modReal, &info.mods)
	return pred, mods, err
}

func (info *compatInfo) setInterpField(si *interpInfo, l lhs, value expr) error {
	switch strings.ToLower(l.field) {
	case "action":
		if l.index != nil {
			return errNotArray
		}
		actions, err := info.parseInterpActions(value)
		if err != nil {
			return err
		}
		si.in.actions = actions
		si.defined |= siAction
	case "virtualmodifier", "virtualmod":
		if l.index != nil {
			return errNotArray
		}
		idx, err := resolveVirtualMod(value, &info.mods)
		if err != nil {
			return err
		}
		si.in.virtualMod = idx
		si.defined |= siVirtualMod
	case "repeat":
		if l.index != nil {
			return errNotArray
		}
		set, err := resolveBoolean(value)
		if err != nil {
			return err
		}
		si.in.repeat = set
		si.defined |= siRepeat
	case "locking":
	case "usemodmap", "usemodmapmods":
		if l.index != nil {
			return errNotArray
		}
		v, err := resolveEnum(value, useModMapValueNames)
		if err != nil {
			return err
		}
		si.in.levelOneOnly = v != 0
		si.defined |= siLevelOneOnly
	default:
		return fmt.Errorf("unknown symbol interpretation field %q", l.field)
	}
	return nil
}

func (info *compatInfo) parseInterpActions(value expr) ([]action, error) {
	list, isList := value.(exprActionList)
	if !isList {
		a, err := info.defaults.parse(info.ctx.Context, info.format, &info.mods, value)
		if err != nil {
			return nil, err
		}
		if a.typ == actNone {
			return nil, nil
		}
		return []action{a}, nil
	}
	var actions []action
	for _, def := range list.actions {
		a, err := info.defaults.parse(info.ctx.Context, info.format, &info.mods, def)
		if err != nil {
			return nil, err
		}
		if a.typ != actNone {
			actions = append(actions, a)
		}
	}
	return actions, nil
}

func (info *compatInfo) setLEDField(l *ledInfo, f lhs, value expr) error {
	field := strings.ToLower(f.field)
	switch field {
	case "allowexplicit", "driveskbd", "driveskeyboard", "leddriveskbd", "leddriveskeyboard", "indicatordriveskbd", "indicatordriveskeyboard", "index":
		return nil
	}
	if f.index != nil {
		return errNotArray
	}
	switch field {
	case "modifiers", "mods":
		mods, err := resolveModMask(value, modBoth, &info.mods)
		if err != nil {
			return coded(codeWrongFieldType, err)
		}
		l.led.mods = mods
		l.defined |= ledMods
	case "groups":
		mask, err := resolveMask(value, groupMaskLookup(info.format.maxGroups()))
		if err != nil {
			return coded(codeWrongFieldType, err)
		}
		l.led.groups = mask
		l.defined |= ledGroups
	case "controls", "ctrls":
		mask, err := resolveMask(value, simpleLookup(ctrlMaskNames))
		if err != nil {
			return err
		}
		l.led.ctrls = mask
		l.defined |= ledCtrls
	case "whichmodstate", "whichmodifierstate":
		mask, err := resolveMask(value, simpleLookup(modComponentMaskNames))
		if err != nil {
			return err
		}
		l.led.whichMods = StateComponent(mask)
	case "whichgroupstate":
		mask, err := resolveMask(value, simpleLookup(groupComponentMaskNames))
		if err != nil {
			return err
		}
		l.led.whichGroups = StateComponent(mask)
	default:
		return fmt.Errorf("unknown indicator map field %q", f.field)
	}
	return nil
}

func groupMaskLookup(limit uint32) identLookup {
	return func(name string) (uint32, bool) {
		if len(name) > len("Group") && strings.EqualFold(name[:len("Group")], "Group") {
			if v, err := strconv.ParseUint(name[len("Group"):], 10, 32); err == nil {
				if v < 1 || v > uint64(limit) {
					return 0, false
				}
				return 1 << (v - 1), true
			}
		}
		return lookupName(groupMaskNames, name)
	}
}

func (info *compatInfo) handleGlobalVar(v varDecl) error {
	l, err := resolveLhs(v.name)
	if err != nil {
		return err
	}
	switch {
	case strings.EqualFold(l.elem, "interpret"):
		temp := interpInfo{merge: v.merge}
		temp.in.virtualMod = modIndexInvalid
		if err := info.setInterpField(&temp, l, v.value); err != nil {
			return err
		}
		info.mergeInterp(&info.defaultInterp, &temp, true)
		return nil
	case strings.EqualFold(l.elem, "indicator"):
		temp := ledInfo{merge: v.merge}
		if err := info.setLEDField(&temp, l, v.value); err != nil {
			return err
		}
		info.mergeLED(&info.defaultLED, &temp, true)
		return nil
	case l.elem != "":
		return info.defaults.setField(info.format, &info.mods, l.elem, l.field, l.index, v.value, v.merge)
	}
	return codedf(codeUnknownDefault, "default defined for unknown field %q", l.field)
}

func (info *compatInfo) handleInterp(d interpDecl) error {
	pred, mods, err := info.resolvePredicate(d.match)
	if err != nil {
		return fmt.Errorf("interpret: %w", err)
	}
	si := info.defaultInterp
	si.merge = d.merge
	si.in.sym = d.sym
	si.in.match = pred
	si.in.mods = mods
	var errs []error
	for _, v := range d.body {
		l, err := resolveLhs(v.name)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if l.elem != "" {
			errs = append(errs, errors.New("cannot set a global default inside an interpret statement"))
			continue
		}
		if err := info.setInterpField(&si, l, v.value); err != nil {
			errs = append(errs, fmt.Errorf("interpret %s: %w", KeysymGetName(d.sym), err))
		}
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	info.addInterp(&si, true)
	return nil
}

func (info *compatInfo) handleLEDMap(d ledMapDecl) error {
	l := info.defaultLED
	l.merge = d.merge
	l.led.name = d.name
	var errs []error
	for _, v := range d.body {
		f, err := resolveLhs(v.name)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if f.elem != "" {
			errs = append(errs, fmt.Errorf("cannot set defaults for %q in an indicator map", f.elem))
			continue
		}
		if err := info.setLEDField(&l, f, v.value); err != nil {
			errs = append(errs, fmt.Errorf("indicator %s: %w", d.name, err))
		}
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return info.addLED(&l, true)
}

func (m *Keymap) compileCompat(f *xkbFile, ctx *session) error {
	info := newCompatInfo(ctx, m.format, 0, &m.mods)
	if f != nil {
		if err := info.handleFile(f); err != nil {
			return err
		}
	}
	m.sections[fileCompat] = escapeMapName(info.name)
	m.mods = info.mods
	for _, needSym := range []bool{true, false} {
		for _, pred := range []matchOp{matchExactly, matchAll, matchNone, matchAny, matchAnyOrNone} {
			for _, si := range info.interps {
				if si.in.match == pred && (si.in.sym != KeyNoSymbol) == needSym {
					in := si.in
					m.interprets = append(m.interprets, &in)
				}
			}
		}
	}
	m.copyLEDMaps(info)
	return nil
}

func (m *Keymap) copyLEDMaps(info *compatInfo) {
	for _, l := range info.leds {
		idx := slices.IndexFunc(m.leds, func(o led) bool { return o.name == l.led.name })
		if idx < 0 {
			idx = slices.IndexFunc(m.leds, func(o led) bool { return o.name == "" })
		}
		if idx < 0 {
			if len(m.leds) >= maxLEDs {
				continue
			}
			m.leds = append(m.leds, led{})
			idx = len(m.leds) - 1
		}
		out := l.led
		if out.groups != 0 && out.whichGroups == 0 {
			out.whichGroups = StateLayoutEffective
		}
		if out.mods != 0 && out.whichMods == 0 {
			out.whichMods = StateModEffective
		}
		m.leds[idx] = out
	}
}
