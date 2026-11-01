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

func (m *Keymap) writeSectionHeader(b *strings.Builder, keyword string, typ fileType) {
	if name := m.sections[typ]; name != "" {
		fmt.Fprintf(b, "%s \"%s\" {\n", keyword, name)
		return
	}
	fmt.Fprintf(b, "%s {\n", keyword)
}

func (m *Keymap) writeVmods(b *strings.Builder) {
	hasSome := false
	for i := uint32(8); i < m.mods.num; i++ {
		if !hasSome {
			b.WriteString("\tvirtual_modifiers ")
			hasSome = true
		} else {
			b.WriteByte(',')
		}
		b.WriteString(m.mods.mods[i].name)
		if m.mods.explicit&(1<<i) != 0 && m.mods.mods[i].mapping != 0 {
			fmt.Fprintf(b, "=%s", m.modMaskText(modReal, m.mods.mods[i].mapping))
		}
	}
	if hasSome {
		b.WriteString(";\n\n")
	}
}

func (m *Keymap) writeLEDMap(b *strings.Builder, l *led) {
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

const voidActionV1 = "LockControls(controls=none,affect=neither)"

func signPrefix(absolute bool, positive bool) string {
	if !absolute && positive {
		return "+"
	}
	return ""
}

func (m *Keymap) actionText(a *action) string {
	name := actionTypeText(a.typ)
	switch a.typ {
	case actModSet, actModLatch, actModLock:
		return m.modActionText(name, a)
	case actGroupSet, actGroupLatch, actGroupLock:
		return groupActionText(name, a)
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
		return voidActionV1
	}
	d := a.privData
	return fmt.Sprintf("%s(type=0x%02x,data[0]=0x%02x,data[1]=0x%02x,data[2]=0x%02x,data[3]=0x%02x,data[4]=0x%02x,data[5]=0x%02x,data[6]=0x%02x)",
		name, uint8(a.typ), d[0], d[1], d[2], d[3], d[4], d[5], d[6])
}

func (m *Keymap) modActionText(name string, a *action) string {
	args := "modMapMods"
	if a.flags&flagLookupModmap == 0 {
		args = m.modMaskText(modBoth, a.mods)
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
	return fmt.Sprintf("%s(modifiers=%s%s)", name, args, extra)
}

func groupActionText(name string, a *action) string {
	absolute := a.flags&flagAbsoluteSwitch != 0
	magnitude := a.group
	if magnitude < 0 {
		magnitude = -magnitude
	}
	limit := int32(maxGroupsV1)
	if !absolute {
		limit++
	}
	if magnitude >= limit {
		return voidActionV1
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

func (m *Keymap) writeCompat(b *strings.Builder) {
	m.writeSectionHeader(b, "xkb_compatibility", fileCompat)
	m.writeVmods(b)
	b.WriteString("\tinterpret.useModMapMods= AnyLevel;\n")
	b.WriteString("\tinterpret.repeat= False;\n")
	interps := m.interprets
	if !m.interpsAny {
		interps = []*interpret{&defaultInterpret}
	}
	for _, si := range interps {
		m.writeInterpret(b, si)
	}
	for i := range m.leds {
		l := &m.leds[i]
		if l.whichGroups != 0 || l.groups != 0 || l.whichMods != 0 || l.mods != 0 || l.ctrls != 0 {
			m.writeLEDMap(b, l)
		}
	}
	b.WriteString("};\n\n")
}

func (m *Keymap) writeInterpret(b *strings.Builder, si *interpret) {
	match := lookupValue(matchNames, uint32(si.match))
	mods := m.modMaskText(modBoth, si.mods)
	if si.sym == KeyNoSymbol {
		fmt.Fprintf(b, "\tinterpret Any+%s(%s) {", match, mods)
	} else {
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
	switch len(si.actions) {
	case 0:
	case 1:
		fmt.Fprintf(b, "\n\t\taction= %s;", m.actionText(&si.actions[0]))
		explicit = true
	default:
		fmt.Fprintf(b, "\n\t\taction= %s;", voidActionV1)
		explicit = true
	}
	if explicit {
		b.WriteString("\n\t};\n")
	} else {
		b.WriteString("\n\t\taction= NoAction();\n\t};\n")
	}
}

func (m *Keymap) writeKeycodes(b *strings.Builder) {
	m.writeSectionHeader(b, "xkb_keycodes", fileKeycodes)
	fmt.Fprintf(b, "\tminimum = %d;\n", min(m.minKey, 8))
	fmt.Fprintf(b, "\tmaximum = %d;\n", max(m.maxKey, 255))
	for i := range m.flatKeys {
		k := &m.flatKeys[i]
		if k.nameLen > 0 {
			fmt.Fprintf(b, "\t<%s> = %d;\n", m.keyName(k), k.keycode)
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
		fmt.Fprintf(b, "\talias <%s> = <%s>;\n", m.nameBlob[a.alias:a.alias+uint32(a.aliasSize)], m.nameBlob[a.real:a.real+uint32(a.realSize)])
	}
	b.WriteString("};\n\n")
}

func (m *Keymap) writeTypes(b *strings.Builder) {
	m.writeSectionHeader(b, "xkb_types", fileTypes)
	m.writeVmods(b)
	for _, t := range m.types {
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

func (m *Keymap) writeActions(b *strings.Builder, g *flatGroup) {
	for lvl := range m.groupNumLevels(g) {
		if lvl != 0 {
			b.WriteString(", ")
		}
		actions := m.levelActions(&m.flatLevels[g.firstLevel+lvl])
		text := "NoAction()"
		switch {
		case len(actions) == 1:
			text = m.actionText(&actions[0])
		case len(actions) > 1:
			text = voidActionV1
		}
		fmt.Fprintf(b, "%30s", text)
	}
}

func (m *Keymap) writeKeysyms(b *strings.Builder, g *flatGroup) {
	for lvl := range m.groupNumLevels(g) {
		if lvl != 0 {
			b.WriteString(", ")
		}
		syms := m.levelSyms(&m.flatLevels[g.firstLevel+lvl])
		switch len(syms) {
		case 0:
			b.WriteString("NoSymbol")
		case 1:
			fmt.Fprintf(b, "0x%x", uint32(syms[0]))
		default:
			b.WriteByte('{')
			for s, sym := range syms {
				if s != 0 {
					b.WriteByte(',')
				}
				fmt.Fprintf(b, "0x%x", uint32(sym))
			}
			b.WriteByte('}')
		}
	}
}

func (m *Keymap) writeKey(b *strings.Builder, k *flatKey) {
	simple := true
	groups := m.flatGroups[k.firstGroup : k.firstGroup+uint32(k.numGroups)]
	numGroups := uint32(min(k.numGroups, maxGroupsV1))
	fmt.Fprintf(b, "\tkey <%s> {", m.keyName(k))
	if k.explicit&explicitTypes != 0 {
		simple = false
		m.writeKeyTypes(b, groups[:numGroups])
	}
	showActions := k.explicit&explicitInterp != 0
	if k.explicit&explicitRepeat != 0 || showActions {
		if k.repeats {
			b.WriteString("\n\t\trepeat= Yes,")
		} else {
			b.WriteString("\n\t\trepeat= No,")
		}
	}
	if k.explicit&explicitVmodmap != 0 || (showActions && k.vmodmap != 0) {
		fmt.Fprintf(b, "\n\t\tvirtualMods= %s,", m.modMaskText(modBoth, k.vmodmap))
	}
	switch k.rangeAct {
	case rangeSaturate:
		b.WriteString("\n\t\tgroupsClamp,")
	case rangeRedirect:
		if uint32(k.rangeNum) < numGroups {
			fmt.Fprintf(b, "\n\t\tgroupsRedirect= %d,", uint32(k.rangeNum)+1)
		}
	}
	if numGroups > 1 || showActions {
		simple = false
	}
	if simple {
		m.writeSimpleKey(b, k, groups[:numGroups])
		return
	}
	for i := range groups[:numGroups] {
		if i != 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(b, "\n\t\tsymbols[%d]= [ ", i+1)
		m.writeKeysyms(b, &groups[i])
		b.WriteString(" ]")
		if showActions {
			fmt.Fprintf(b, ",\n\t\tactions[%d]= [ ", i+1)
			m.writeActions(b, &groups[i])
			b.WriteString(" ]")
		}
	}
	b.WriteString("\n\t};\n")
}

func (m *Keymap) writeKeyTypes(b *strings.Builder, groups []flatGroup) {
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

func (m *Keymap) writeSimpleKey(b *strings.Builder, k *flatKey, groups []flatGroup) {
	onlySymbols := k.explicit == explicitSymbols
	if len(groups) == 0 {
		text := b.String()
		if text[len(text)-1] == ',' {
			b.Reset()
			b.WriteString(text[:len(text)-1])
		}
	} else {
		if !onlySymbols {
			b.WriteString("\n\t")
		}
		b.WriteString("\t[ ")
		m.writeKeysyms(b, &groups[0])
		b.WriteString(" ]")
	}
	if onlySymbols {
		b.WriteString(" };\n")
	} else {
		b.WriteString("\n\t};\n")
	}
}

func (m *Keymap) writeSymbols(b *strings.Builder) {
	m.writeSectionHeader(b, "xkb_symbols", fileSymbols)
	hasNames := false
	for group, name := range m.groupNames {
		if group >= maxGroupsV1 || name == "" {
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
			m.writeKey(b, &m.flatKeys[i])
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

func (m *Keymap) serialize() string {
	var b strings.Builder
	b.WriteString("xkb_keymap {\n")
	m.writeKeycodes(&b)
	m.writeTypes(&b)
	m.writeCompat(&b)
	m.writeSymbols(&b)
	b.WriteString("};\n")
	return b.String()
}
