package xkb

import (
	"fmt"
	"strconv"
	"strings"
)

func (m *Keymap) modMaskText(typ modType, mask uint32) string {
	if mask == 0 {
		return "none"
	}
	if mask == realModMask {
		return "all"
	}
	var known uint32 = 0xffffffff
	if m.mods.num < 32 {
		known = (1 << m.mods.num) - 1
	}
	if (typ == modReal && mask&^realModMask != 0) || mask&^known != 0 {
		return "0x" + strconv.FormatUint(uint64(mask), 16)
	}
	var names []string
	for i := range m.mods.num {
		if mask&(1<<i) != 0 {
			names = append(names, m.mods.mods[i].name)
		}
	}
	return strings.Join(names, "+")
}

func ledStateMaskText(table []lookupEntry, mask uint32) string {
	if mask == 0 {
		return "0"
	}
	var names []string
	for i := range uint32(32) {
		if mask&(1<<i) != 0 {
			names = append(names, lookupValue(table, 1<<i))
		}
	}
	return strings.Join(names, "+")
}

func controlMaskText(mask uint32) string {
	if mask == 0 {
		return "none"
	}
	if mask == controlAll {
		return "all"
	}
	var names []string
	for i := range uint32(32) {
		if mask&(1<<i) != 0 {
			names = append(names, lookupValue(ctrlMaskNames, 1<<i))
		}
	}
	return strings.Join(names, "+")
}

func writeStringLiteral(b *strings.Builder, s string) {
	b.WriteByte('"')
	for i := range len(s) {
		switch c := s[i]; {
		case c == '\n':
			b.WriteString(`\n`)
		case c == '\\':
			b.WriteString(`\\`)
		case c < 0x20 || c == '"':
			fmt.Fprintf(b, `\00%d%d`, c>>3, c&7)
		default:
			b.WriteByte(c)
		}
	}
	b.WriteByte('"')
}

type dumper struct {
	m          *Keymap
	b          *strings.Builder
	format     KeymapFormat
	maxGroups  uint32
	pretty     bool
	dropUnused bool
}

const (
	symbolPadding = 15
	actionPadding = 30
)

const incompatibleTextFormat = 742

func (d *dumper) logMultipleActions() {
	d.m.ctx.log(LogError, 0, incompatibleTextFormat, "Cannot serialize multiple actions per level in keymap format %d; degrade to VoidAction()\n", d.format)
}

func (d *dumper) sectionHeader(keyword string, typ fileType) {
	if name := d.m.sections[typ]; name != "" {
		fmt.Fprintf(d.b, "%s \"%s\" {\n", keyword, name)
		return
	}
	fmt.Fprintf(d.b, "%s {\n", keyword)
}

func (d *dumper) vmods() {
	m := d.m
	hasSome := false
	autoCanonical := d.format >= FormatTextV2
	for i := uint32(8); i < m.mods.num; i++ {
		if !hasSome {
			d.b.WriteString("\tvirtual_modifiers ")
			hasSome = true
		} else {
			d.b.WriteByte(',')
		}
		d.b.WriteString(m.mods.mods[i].name)
		if m.mods.explicit&(1<<i) != 0 && (autoCanonical || m.mods.mods[i].mapping != 0) {
			fmt.Fprintf(d.b, "=%s", m.modMaskText(modReal, m.mods.mods[i].mapping))
		}
	}
	if hasSome {
		d.b.WriteString(";\n\n")
	}
}

func (d *dumper) ledMap(l *led) {
	m, b := d.m, d.b
	b.WriteString("\tindicator ")
	writeStringLiteral(b, l.name)
	b.WriteString(" {\n")
	if l.whichGroups != 0 {
		if l.whichGroups != StateLayoutEffective {
			fmt.Fprintf(b, "\t\twhichGroupState= %s;\n", ledStateMaskText(groupComponentMaskNames, uint32(l.whichGroups)))
		}
		fmt.Fprintf(b, "\t\tgroups= 0x%02x;\n", l.groups)
	}
	if l.whichMods != 0 {
		if l.whichMods != StateModEffective {
			fmt.Fprintf(b, "\t\twhichModState= %s;\n", ledStateMaskText(modComponentMaskNames, uint32(l.whichMods)))
		}
		fmt.Fprintf(b, "\t\tmodifiers= %s;\n", m.modMaskText(modBoth, l.mods))
	}
	if l.ctrls != 0 {
		fmt.Fprintf(b, "\t\tcontrols= %s;\n", controlMaskText(l.ctrls))
	}
	b.WriteString("\t};\n")
}

func affectText(flags actionFlags, showBoth bool) string {
	switch flags & (flagLockNoLock | flagLockNoUnlock) {
	case 0:
		if showBoth {
			return ",affect=both"
		}
		return ""
	case flagLockNoUnlock:
		return ",affect=lock"
	case flagLockNoLock:
		return ",affect=unlock"
	}
	return ",affect=neither"
}

func actionTypeText(t actionType) string {
	if name := lookupValue(actionTypeNames, uint32(t)); name != "" {
		return name
	}
	return "Private"
}

func (d *dumper) voidText() string {
	if d.format == FormatTextV1 {
		return "LockControls(controls=none,affect=neither)"
	}
	return "VoidAction()"
}

func signPrefix(absolute bool, positive bool) string {
	if !absolute && positive {
		return "+"
	}
	return ""
}

func (d *dumper) actionText(a *action) string {
	name := actionTypeText(a.typ)
	switch a.typ {
	case actModSet, actModLatch, actModLock:
		return d.modActionText(name, a)
	case actGroupSet, actGroupLatch, actGroupLock:
		return d.groupActionText(name, a)
	case actTerminate:
		return name + "()"
	case actPtrMove:
		accel := ",!accel"
		if a.flags&flagAccel != 0 {
			accel = ""
		}
		return fmt.Sprintf("%s(x=%s%d,y=%s%d%s)", name, signPrefix(a.flags&flagAbsoluteX != 0, a.x >= 0), a.x, signPrefix(a.flags&flagAbsoluteY != 0, a.y >= 0), a.y, accel)
	case actPtrLock, actPtrButton:
		return ptrButtonText(name, a)
	case actPtrDefault:
		return fmt.Sprintf("%s(affect=button,button=%s%d)", name, signPrefix(a.flags&flagAbsoluteSwitch != 0, a.value >= 0), a.value)
	case actSwitchVT:
		same := "!"
		if a.flags&flagSameScreen != 0 {
			same = ""
		}
		return fmt.Sprintf("%s(screen=%s%d,%ssame)", name, signPrefix(a.flags&flagAbsoluteSwitch != 0, a.screen >= 0), a.screen, same)
	case actCtrlSet:
		return fmt.Sprintf("%s(controls=%s)", name, controlMaskText(a.ctrls))
	case actCtrlLock:
		return fmt.Sprintf("%s(controls=%s%s)", name, controlMaskText(a.ctrls), affectText(a.flags, false))
	case actNone:
		return "NoAction()"
	case actVoid:
		return d.voidText()
	}
	p := a.privData
	return fmt.Sprintf("%s(type=0x%02x,data[0]=0x%02x,data[1]=0x%02x,data[2]=0x%02x,data[3]=0x%02x,data[4]=0x%02x,data[5]=0x%02x,data[6]=0x%02x)",
		name, uint8(a.typ), p[0], p[1], p[2], p[3], p[4], p[5], p[6])
}

func (d *dumper) modActionText(name string, a *action) string {
	args := "modMapMods"
	if a.flags&flagLookupModmap == 0 {
		args = d.m.modMaskText(modBoth, a.mods)
	}
	var extra string
	if a.typ != actModLock {
		if a.flags&flagLockClear != 0 {
			extra += ",clearLocks"
		}
		if a.flags&flagLatchToLock != 0 {
			extra += ",latchToLock"
		}
	} else {
		extra = affectText(a.flags, false)
	}
	if a.flags&flagUnlockOnPress != 0 {
		if d.format >= FormatTextV2 {
			extra += ",unlockOnPress"
		} else {
			d.m.ctx.log(LogError, 0, incompatibleTextFormat, "Cannot use \"%s(unlockOnPress=true)\" in keymap format %d\n", name, d.format)
		}
	}
	if a.typ == actModLatch && a.flags&flagLatchOnPress != 0 {
		if d.format >= FormatTextV2 {
			extra += ",latchOnPress"
		} else {
			d.m.ctx.log(LogError, 0, incompatibleTextFormat, "Cannot use \"LatchMods(latchOnPress=true)\" in keymap format %d\n", d.format)
		}
	}
	return fmt.Sprintf("%s(modifiers=%s%s)", name, args, extra)
}

func (d *dumper) groupActionText(name string, a *action) string {
	absolute := a.flags&flagAbsoluteSwitch != 0
	magnitude := a.group
	if magnitude < 0 {
		magnitude = -magnitude
	}
	limit := int32(d.maxGroups) //nolint:gosec // at most maxGroups
	if !absolute {
		limit++
	}
	if magnitude >= limit {
		return d.voidText()
	}
	value := a.group
	if absolute {
		value++
	}
	var extra string
	if a.typ != actGroupLock {
		if a.flags&flagLockClear != 0 {
			extra += ",clearLocks"
		}
		if a.flags&flagLatchToLock != 0 {
			extra += ",latchToLock"
		}
	} else if a.flags&flagLockOnRelease != 0 {
		if d.format >= FormatTextV2 {
			extra += ",lockOnRelease"
		} else {
			d.m.ctx.log(LogError, 0, incompatibleTextFormat, "Cannot use \"GroupLock(lockOnRelease=true)\" in keymap format %d\n", d.format)
		}
	}
	return fmt.Sprintf("%s(group=%s%d%s)", name, signPrefix(absolute, a.group > 0), value, extra)
}

func ptrButtonText(name string, a *action) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s(button=", name)
	if a.button > 0 && a.button <= 5 {
		fmt.Fprintf(&b, "%d", a.button)
	} else {
		b.WriteString("default")
	}
	if a.count != 0 {
		fmt.Fprintf(&b, ",count=%d", a.count)
	}
	if a.typ == actPtrLock {
		b.WriteString(affectText(a.flags, true))
	}
	b.WriteByte(')')
	return b.String()
}

func (d *dumper) compat() {
	m, b := d.m, d.b
	d.sectionHeader("xkb_compatibility", fileCompat)
	d.vmods()
	b.WriteString("\tinterpret.useModMapMods= AnyLevel;\n")
	b.WriteString("\tinterpret.repeat= False;\n")
	interps := m.allInterprets
	if len(interps) == 0 {
		interps = []*interpret{&defaultInterpret}
	}
	for _, si := range interps {
		if si.required || !d.dropUnused {
			d.interpret(si)
		}
	}
	for i := range m.leds {
		l := &m.leds[i]
		if l.whichGroups != 0 || l.groups != 0 || l.whichMods != 0 || l.mods != 0 || l.ctrls != 0 {
			d.ledMap(l)
		}
	}
	b.WriteString("};\n\n")
}

func (d *dumper) interpret(si *interpret) {
	m, b := d.m, d.b
	match := lookupValue(matchNames, uint32(si.match))
	mods := m.modMaskText(modBoth, si.mods)
	switch {
	case si.sym == KeyNoSymbol:
		fmt.Fprintf(b, "\tinterpret Any+%s(%s) {", match, mods)
	case d.pretty:
		fmt.Fprintf(b, "\tinterpret %s+%s(%s) {", keysymText(si.sym), match, mods)
	default:
		fmt.Fprintf(b, "\tinterpret 0x%x+%s(%s) {", uint32(si.sym), match, mods)
	}
	explicit := false
	if si.virtualMod != modIndexInvalid {
		fmt.Fprintf(b, "\n\t\tvirtualModifier= %s;", m.mods.mods[si.virtualMod].name)
		explicit = true
	}
	if si.levelOneOnly {
		b.WriteString("\n\t\tuseModMapMods=level1;")
		explicit = true
	}
	if si.repeat {
		b.WriteString("\n\t\trepeat= True;")
		explicit = true
	}
	switch {
	case len(si.actions) == 0:
	case len(si.actions) == 1:
		fmt.Fprintf(b, "\n\t\taction= %s;", d.actionText(&si.actions[0]))
		explicit = true
	case d.format < FormatTextV2:
		d.logMultipleActions()
		fmt.Fprintf(b, "\n\t\taction= %s;", d.voidText())
		explicit = true
	default:
		b.WriteString("\n\t\taction= {")
		for i := range si.actions {
			if i != 0 {
				b.WriteString(", ")
			}
			b.WriteString(d.actionText(&si.actions[i]))
		}
		b.WriteString("};")
		explicit = true
	}
	if explicit {
		b.WriteString("\n\t};\n")
	} else {
		b.WriteString("\n\t\taction= NoAction();\n\t};\n")
	}
}

func keysymText(sym Keysym) string {
	if name := KeysymGetName(sym); name != "" {
		return name
	}
	return "NoSymbol"
}

func (d *dumper) keycodes() {
	m, b := d.m, d.b
	d.sectionHeader("xkb_keycodes", fileKeycodes)
	fmt.Fprintf(b, "\tminimum = %d;\n", min(m.minKey, 8))
	fmt.Fprintf(b, "\tmaximum = %d;\n", max(m.maxKey, 255))
	for i := range m.flatKeys {
		k := &m.flatKeys[i]
		if k.nameLen == 0 {
			continue
		}
		name := "<" + m.keyName(k) + ">"
		if d.pretty {
			fmt.Fprintf(b, "\t%-20s = %d;\n", name, k.keycode)
		} else {
			fmt.Fprintf(b, "\t%s = %d;\n", name, k.keycode)
		}
	}
	for i := range m.leds {
		if m.leds[i].name != "" {
			fmt.Fprintf(b, "\tindicator %d = ", i+1)
			writeStringLiteral(b, m.leds[i].name)
			b.WriteString(";\n")
		}
	}
	for _, a := range m.aliasPairs {
		alias := "<" + m.nameBlob[a.alias:a.alias+uint32(a.aliasSize)] + ">"
		real := "<" + m.nameBlob[a.real:a.real+uint32(a.realSize)] + ">"
		if d.pretty {
			fmt.Fprintf(b, "\talias %-14s = %s;\n", alias, real)
		} else {
			fmt.Fprintf(b, "\talias %s = %s;\n", alias, real)
		}
	}
	b.WriteString("};\n\n")
}

func (d *dumper) types() {
	m, b := d.m, d.b
	d.sectionHeader("xkb_types", fileTypes)
	d.vmods()
	for _, t := range m.allTypes {
		if !t.required && d.dropUnused {
			continue
		}
		b.WriteString("\ttype ")
		writeStringLiteral(b, t.name)
		b.WriteString(" {\n")
		fmt.Fprintf(b, "\t\tmodifiers= %s;\n", m.modMaskText(modBoth, t.mods))
		for i := range t.entries {
			e := &t.entries[i]
			if e.level == 0 && e.preserve == 0 {
				continue
			}
			text := m.modMaskText(modBoth, e.mods)
			fmt.Fprintf(b, "\t\tmap[%s]= %d;\n", text, e.level+1)
			if e.preserve != 0 {
				fmt.Fprintf(b, "\t\tpreserve[%s]= %s;\n", text, m.modMaskText(modBoth, e.preserve))
			}
		}
		for n, name := range t.levelNames {
			if name != "" {
				fmt.Fprintf(b, "\t\tlevel_name[%d]= ", n+1)
				writeStringLiteral(b, name)
				b.WriteString(";\n")
			}
		}
		b.WriteString("\t};\n")
	}
	b.WriteString("};\n\n")
}

func (m *Keymap) groupNumLevels(g *flatGroup) uint32 {
	return uint32(m.flatTypes[g.typ].numLevels)
}

func (d *dumper) actions(g *flatGroup) {
	m, b := d.m, d.b
	for lvl := range m.groupNumLevels(g) {
		if lvl != 0 {
			b.WriteString(", ")
		}
		actions := m.levelActions(&m.flatLevels[g.firstLevel+lvl])
		var text string
		switch {
		case len(actions) == 0:
			text = "NoAction()"
		case len(actions) == 1:
			text = d.actionText(&actions[0])
		case d.format < FormatTextV2:
			d.logMultipleActions()
			text = d.voidText()
		default:
			var multi strings.Builder
			multi.WriteString("{ ")
			for k := range actions {
				if k != 0 {
					multi.WriteString(", ")
				}
				fmt.Fprintf(&multi, "%*s", actionPadding, d.actionText(&actions[k]))
			}
			multi.WriteString(" }")
			text = multi.String()
		}
		fmt.Fprintf(b, "%*s", actionPadding, text)
	}
}

func (d *dumper) keysyms(g *flatGroup, showActions bool) {
	m, b := d.m, d.b
	padding := 0
	if d.pretty {
		padding = symbolPadding
		if showActions {
			padding = actionPadding
		}
	}
	for lvl := range m.groupNumLevels(g) {
		if lvl != 0 {
			b.WriteString(", ")
		}
		syms := m.levelSyms(&m.flatLevels[g.firstLevel+lvl])
		switch {
		case len(syms) == 0:
			fmt.Fprintf(b, "%*s", padding, "NoSymbol")
		case len(syms) == 1 && (d.pretty || syms[0] == KeyNoSymbol):
			fmt.Fprintf(b, "%*s", padding, keysymText(syms[0]))
		case len(syms) == 1:
			fmt.Fprintf(b, "0x%x", uint32(syms[0]))
		case d.pretty:
			var multi strings.Builder
			multi.WriteString("{ ")
			inner := 0
			if showActions {
				inner = padding
			}
			for s, sym := range syms {
				if s != 0 {
					multi.WriteString(", ")
				}
				fmt.Fprintf(&multi, "%*s", inner, keysymText(sym))
			}
			multi.WriteString(" }")
			fmt.Fprintf(b, "%*s", padding, multi.String())
		default:
			b.WriteByte('{')
			for s, sym := range syms {
				if s != 0 {
					b.WriteByte(',')
				}
				if sym == KeyNoSymbol {
					b.WriteString("NoSymbol")
				} else {
					fmt.Fprintf(b, "0x%x", uint32(sym))
				}
			}
			b.WriteByte('}')
		}
	}
}

func (d *dumper) key(k *flatKey) {
	m, b := d.m, d.b
	simple := true
	groups := m.flatGroups[k.firstGroup : k.firstGroup+uint32(k.numGroups)]
	numGroups := min(uint32(k.numGroups), d.maxGroups)
	name := "<" + m.keyName(k) + ">"
	if d.pretty {
		fmt.Fprintf(b, "\tkey %-20s {", name)
	} else {
		fmt.Fprintf(b, "\tkey %s {", name)
	}
	trailingComma := false
	if k.explicit&explicitTypes != 0 {
		simple = false
		d.keyTypes(groups[:numGroups])
		trailingComma = true
	}
	showActions := k.explicit&explicitInterp != 0
	if k.explicit&explicitRepeat != 0 || showActions {
		if k.repeats {
			b.WriteString("\n\t\trepeat= Yes,")
		} else {
			b.WriteString("\n\t\trepeat= No,")
		}
		trailingComma = true
	}
	if k.explicit&explicitVmodmap != 0 || (showActions && k.vmodmap != 0) {
		fmt.Fprintf(b, "\n\t\tvirtualMods= %s,", m.modMaskText(modBoth, k.vmodmap))
		trailingComma = true
	}
	switch k.rangeAct {
	case rangeSaturate:
		b.WriteString("\n\t\tgroupsClamp,")
		trailingComma = true
	case rangeRedirect:
		if uint32(k.rangeNum) < numGroups {
			fmt.Fprintf(b, "\n\t\tgroupsRedirect= %d,", uint32(k.rangeNum)+1)
			trailingComma = true
		}
	}
	if numGroups > 1 || showActions {
		simple = false
	}
	if simple {
		d.simpleKey(k, groups[:numGroups], trailingComma)
		return
	}
	for i := range groups[:numGroups] {
		if i != 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(b, "\n\t\tsymbols[%d]= [ ", i+1)
		d.keysyms(&groups[i], showActions)
		b.WriteString(" ]")
		if showActions {
			fmt.Fprintf(b, ",\n\t\tactions[%d]= [ ", i+1)
			d.actions(&groups[i])
			b.WriteString(" ]")
		}
	}
	b.WriteString("\n\t};\n")
}

func (d *dumper) keyTypes(groups []flatGroup) {
	m, b := d.m, d.b
	multi := false
	for i := 1; i < len(groups); i++ {
		if groups[i].typ != groups[0].typ {
			multi = true
			break
		}
	}
	if !multi {
		b.WriteString("\n\t\ttype= ")
		writeStringLiteral(b, m.types[groups[0].typ].name)
		b.WriteByte(',')
		return
	}
	for i := range groups {
		if groups[i].flags&flatGroupExplicitType == 0 {
			continue
		}
		fmt.Fprintf(b, "\n\t\ttype[%d]= ", i+1)
		writeStringLiteral(b, m.types[groups[i].typ].name)
		b.WriteByte(',')
	}
}

func (d *dumper) simpleKey(k *flatKey, groups []flatGroup, trailingComma bool) {
	b := d.b
	onlySymbols := k.explicit == explicitSymbols
	if len(groups) == 0 {
		if trailingComma {
			text := b.String()
			b.Reset()
			b.WriteString(text[:len(text)-1])
		}
	} else {
		if !onlySymbols {
			b.WriteString("\n\t")
		}
		b.WriteString("\t[ ")
		d.keysyms(&groups[0], false)
		b.WriteString(" ]")
	}
	if onlySymbols {
		b.WriteString(" };\n")
	} else {
		b.WriteString("\n\t};\n")
	}
}

func (d *dumper) symbols() {
	m, b := d.m, d.b
	d.sectionHeader("xkb_symbols", fileSymbols)
	hasNames := false
	for group, name := range m.groupNames {
		if uint32(group) >= d.maxGroups || name == "" {
			continue
		}
		fmt.Fprintf(b, "\tname[%d]=", group+1)
		writeStringLiteral(b, name)
		b.WriteString(";\n")
		hasNames = true
	}
	if hasNames {
		b.WriteByte('\n')
	}
	for i := range m.flatKeys {
		if m.flatKeys[i].explicit != 0 {
			d.key(&m.flatKeys[i])
		}
	}
	for i := range uint32(8) {
		hadAny := false
		for k := range m.flatKeys {
			if m.flatKeys[k].modmap&(1<<i) == 0 {
				continue
			}
			if !hadAny {
				fmt.Fprintf(b, "\tmodifier_map %s { ", m.mods.mods[i].name)
			} else {
				b.WriteString(", ")
			}
			fmt.Fprintf(b, "<%s>", m.keyName(&m.flatKeys[k]))
			hadAny = true
		}
		if hadAny {
			b.WriteString(" };\n")
		}
	}
	b.WriteString("};\n\n")
}

func (m *Keymap) serialize(format KeymapFormat, flags SerializeFlags) string {
	var b strings.Builder
	d := dumper{
		m: m, b: &b, format: format, maxGroups: format.maxGroups(),
		pretty:     flags&SerializePretty != 0,
		dropUnused: flags&SerializeKeepUnused == 0,
	}
	if m.numGroups > d.maxGroups {
		m.ctx.log(LogError, 0, 237, "Cannot serialize %d groups in keymap format %d: maximum is %d; discarding unsupported groups\n", m.numGroups, format, d.maxGroups)
	}
	b.WriteString("xkb_keymap {\n")
	d.keycodes()
	d.types()
	d.compat()
	d.symbols()
	b.WriteString("};\n")
	return b.String()
}
