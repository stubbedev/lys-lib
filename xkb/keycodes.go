package xkb

import (
	"fmt"
	"slices"
	"strings"
)

type keycodesInfo struct {
	name     string
	depth    int
	byName   map[string]keyMatchInfo
	byCode   map[Keycode]string
	ledNames [maxLEDs]ledNameInfo
	numLEDs  uint32
	ctx      *session
}

type keyMatchInfo struct {
	alias bool
	real  string
	kc    Keycode
}

type ledNameInfo struct {
	merge mergeMode
	name  string
}

func newKeycodesInfo(ctx *session, depth int) *keycodesInfo {
	return &keycodesInfo{depth: depth, byName: map[string]keyMatchInfo{}, byCode: map[Keycode]string{}, ctx: ctx}
}

func (info *keycodesInfo) addLEDName(l ledNameInfo, idx uint32) {
	replace := l.merge != mergeAugment
	for i := range info.numLEDs {
		if info.ledNames[i].name != l.name {
			continue
		}
		if i == idx {
			return
		}
		if !replace {
			return
		}
		info.ledNames[i].name = ""
		break
	}
	if idx >= info.numLEDs {
		info.numLEDs = idx + 1
	}
	old := &info.ledNames[idx]
	if old.name != "" {
		if replace {
			*old = l
		}
		return
	}
	*old = l
}

func (info *keycodesInfo) addKeyName(kc Keycode, name string, merge mergeMode) {
	clobber := merge != mergeAugment
	if match, ok := info.byName[name]; ok {
		if match.alias {
			if !clobber {
				return
			}
			delete(info.byName, name)
		} else if match.kc != kc {
			if !clobber {
				return
			}
			delete(info.byName, name)
			delete(info.byCode, match.kc)
		}
	}
	old, named := info.byCode[kc]
	if named {
		if old == name {
			return
		}
		if !clobber {
			return
		}
		delete(info.byName, old)
	}
	info.byCode[kc] = name
	info.byName[name] = keyMatchInfo{kc: kc}
}

func (info *keycodesInfo) addAlias(alias, real string, merge mergeMode) {
	clobber := merge != mergeAugment
	if match, ok := info.byName[alias]; ok {
		if match.alias {
			if clobber {
				info.byName[alias] = keyMatchInfo{alias: true, real: real}
			}
			return
		}
		if !clobber {
			return
		}
		delete(info.byCode, match.kc)
	}
	info.byName[alias] = keyMatchInfo{alias: true, real: real}
}

func (info *keycodesInfo) merge(from *keycodesInfo, merge mergeMode) {
	if info.name == "" {
		info.name = from.name
	}
	if len(info.byName) == 0 && len(info.byCode) == 0 {
		info.byName, info.byCode = from.byName, from.byCode
		from.byName, from.byCode = map[string]keyMatchInfo{}, map[Keycode]string{}
	} else {
		codes := make([]Keycode, 0, len(from.byCode))
		for kc := range from.byCode {
			codes = append(codes, kc)
		}
		slices.Sort(codes)
		for _, kc := range codes {
			info.addKeyName(kc, from.byCode[kc], merge)
		}
		names := make([]string, 0, len(from.byName))
		for name := range from.byName {
			names = append(names, name)
		}
		slices.Sort(names)
		for _, name := range names {
			if m := from.byName[name]; m.alias {
				info.addAlias(name, m.real, merge)
			}
		}
	}
	if info.numLEDs == 0 {
		info.ledNames, info.numLEDs = from.ledNames, from.numLEDs
		from.numLEDs = 0
		return
	}
	for idx := range from.numLEDs {
		l := from.ledNames[idx]
		if l.name == "" {
			continue
		}
		l.merge = merge
		info.addLEDName(l, idx)
	}
}

func (info *keycodesInfo) handleFile(f *xkbFile) error {
	info.name = f.name
	for _, d := range f.decls {
		var err error
		switch v := d.(type) {
		case includeDecl:
			err = info.handleInclude(v)
		case keycodeDecl:
			if v.value < 0 || v.value > keycodeMax {
				return fmt.Errorf("illegal keycode %d for <%s>", v.value, v.name)
			}
			info.addKeyName(Keycode(v.value), v.name, v.merge)
		case aliasDecl:
			info.addAlias(v.alias, v.real, v.merge)
		case varDecl:
			err = checkKeycodesVar(v)
		case ledNameDecl:
			err = info.handleLEDName(v)
		default:
			err = fmt.Errorf("keycode files may define key and indicator names only, not %T", d)
		}
		if err != nil {
			return fmt.Errorf("%s: %w", mapDisplayName(f), err)
		}
	}
	return nil
}

func mapDisplayName(f *xkbFile) string {
	if f.name == "" {
		return "(unnamed map)"
	}
	return f.name
}

func checkKeycodesVar(v varDecl) error {
	l, err := resolveLhs(v.name)
	if err != nil {
		return err
	}
	if l.elem != "" {
		return fmt.Errorf("cannot set global defaults for %q", l.elem)
	}
	if !strings.EqualFold(l.field, "minimum") && !strings.EqualFold(l.field, "maximum") {
		return fmt.Errorf("default defined for unknown field %q", l.field)
	}
	return nil
}

func (info *keycodesInfo) handleLEDName(v ledNameDecl) error {
	if v.index < 1 || v.index > maxLEDs {
		return fmt.Errorf("illegal indicator index %d", v.index)
	}
	name, err := resolveString(v.name)
	if err != nil {
		return fmt.Errorf("indicator %d name: %w", v.index, err)
	}
	info.addLEDName(ledNameInfo{merge: v.merge, name: name}, uint32(v.index-1))
	return nil
}

func (info *keycodesInfo) handleInclude(inc includeDecl) error {
	if info.depth >= includeMaxDepth {
		return fmt.Errorf("exceeded the include depth of %d at %q", includeMaxDepth, inc.text)
	}
	included := newKeycodesInfo(info.ctx, 0)
	included.name = inc.text
	for _, stmt := range inc.stmts {
		f, err := info.ctx.processInclude(stmt, fileKeycodes)
		if err != nil {
			return err
		}
		next := newKeycodesInfo(info.ctx, info.depth+1)
		if err := next.handleFile(f); err != nil {
			return err
		}
		included.merge(next, stmt.merge)
	}
	info.merge(included, inc.stmts[0].merge)
	return nil
}

func (m *Keymap) compileKeycodes(f *xkbFile, ctx *session) error {
	info := newKeycodesInfo(ctx, 0)
	if f != nil {
		if err := info.handleFile(f); err != nil {
			return err
		}
	}
	m.copyKeycodes(info)
	m.sections[fileKeycodes] = escapeMapName(info.name)
	return nil
}

func (m *Keymap) copyKeycodes(info *keycodesInfo) {
	codes := make([]Keycode, 0, len(info.byCode))
	for kc := range info.byCode {
		codes = append(codes, kc)
	}
	slices.Sort(codes)
	var low, high []Keycode
	for _, kc := range codes {
		if kc <= maxContiguous {
			low = append(low, kc)
		} else {
			high = append(high, kc)
		}
	}
	lowEnd := Keycode(0)
	switch {
	case len(codes) == 0:
		m.minKey, m.maxKey = 8, 255
		lowEnd = 256
	default:
		m.minKey, m.maxKey = codes[0], codes[len(codes)-1]
		if len(low) > 0 {
			lowEnd = low[len(low)-1] + 1
		}
	}
	m.numLow = 0
	if lowEnd > m.minKey {
		m.numLow = int(lowEnd - m.minKey)
	}
	m.keys = make([]key, m.numLow+len(high))
	for i := range m.numLow {
		kc := m.minKey + Keycode(i)
		m.keys[i] = key{keycode: kc, name: info.byCode[kc]}
	}
	for i, kc := range high {
		m.keys[m.numLow+i] = key{keycode: kc, name: info.byCode[kc]}
	}
	m.names = make(map[string]keyMatch, len(info.byName))
	aliases := make([]string, 0, 64)
	for name, match := range info.byName {
		if match.alias {
			aliases = append(aliases, name)
			continue
		}
		idx := 0
		if match.kc <= maxContiguous {
			idx = int(match.kc - m.minKey)
		} else {
			idx = m.numLow + slices.Index(high, match.kc)
		}
		m.names[name] = keyMatch{index: idx}
	}
	slices.Sort(aliases)
	for _, alias := range aliases {
		real := info.byName[alias].real
		if target, ok := info.byName[real]; !ok || target.alias {
			continue
		}
		m.names[alias] = keyMatch{alias: true, real: real}
		m.aliases = append(m.aliases, keyAlias{alias: alias, real: real})
	}
	m.leds = make([]led, info.numLEDs)
	for i := range info.numLEDs {
		m.leds[i].name = info.ledNames[i].name
	}
}
