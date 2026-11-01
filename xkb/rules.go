package xkb

import (
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"unsafe"
)

type rulesToken uint8

const (
	rtEOF rulesToken = iota
	rtEOL
	rtIdent
	rtGroupName
	rtBang
	rtEquals
	rtStar
	rtNone
	rtSome
	rtAny
	rtInclude
)

type rulesLexer struct {
	name string
	src  string
	pos  int
	text string
}

func (l *rulesLexer) eol() bool {
	return l.pos < len(l.src) && l.src[l.pos] == '\n'
}

func isRulesIdent(c byte) bool { return c > ' ' && c < 0x7f && c != '\\' }

func (l *rulesLexer) next() (rulesToken, error) {
	for {
		for l.pos < len(l.src) && (l.src[l.pos] == ' ' || l.src[l.pos] == '\t' || l.src[l.pos] == '\r') {
			l.pos++
		}
		if strings.HasPrefix(l.src[l.pos:], "//") {
			if end := strings.IndexByte(l.src[l.pos:], '\n'); end >= 0 {
				l.pos += end
			} else {
				l.pos = len(l.src)
			}
		}
		if l.eol() {
			for l.eol() {
				l.pos++
			}
			return rtEOL, nil
		}
		if l.pos >= len(l.src) || l.src[l.pos] != '\\' {
			break
		}
		l.pos++
		if l.pos < len(l.src) && l.src[l.pos] == '\r' {
			l.pos++
		}
		if !l.eol() {
			return 0, fmt.Errorf("%s: an illegal new line escape", l.name)
		}
		l.pos++
	}
	if l.pos >= len(l.src) {
		return rtEOF, nil
	}
	rest := l.src[l.pos:]
	switch {
	case rest[0] == '!':
		l.pos++
		return rtBang, nil
	case rest[0] == '=':
		l.pos++
		return rtEquals, nil
	case rest[0] == '*':
		l.pos++
		return rtStar, nil
	case strings.HasPrefix(rest, "<none>"):
		l.pos += len("<none>")
		return rtNone, nil
	case strings.HasPrefix(rest, "<some>"):
		l.pos += len("<some>")
		return rtSome, nil
	case strings.HasPrefix(rest, "<any>"):
		l.pos += len("<any>")
		return rtAny, nil
	case rest[0] == '$':
		l.pos++
		l.text = l.ident()
		if l.text == "" {
			return 0, fmt.Errorf("%s: expected a name after '$'", l.name)
		}
		return rtGroupName, nil
	case strings.HasPrefix(rest, "include"):
		l.pos += len("include")
		return rtInclude, nil
	}
	if isRulesIdent(rest[0]) {
		l.text = l.ident()
		return rtIdent, nil
	}
	return 0, fmt.Errorf("%s: an unrecognized token", l.name)
}

func (l *rulesLexer) ident() string {
	start := l.pos
	for l.pos < len(l.src) && isRulesIdent(l.src[l.pos]) {
		l.pos++
	}
	return l.src[start:l.pos]
}

type mlvo uint8

const (
	mlvoModel mlvo = iota
	mlvoLayout
	mlvoVariant
	mlvoOption
	mlvoCount
)

var mlvoNames = [mlvoCount]string{"model", "layout", "variant", "option"}

type kccgst uint8

const (
	kcKeycodes kccgst = iota
	kcTypes
	kcCompat
	kcSymbols
	kcGeometry
	kcCount
)

var kccgstNames = [kcCount]string{"keycodes", "types", "compat", "symbols", "geometry"}

const allLayouts = maxGroups

type matchedValue struct {
	value   string
	matched bool
	layout  uint32
}

const (
	idxInvalid = ^uint32(0)
	idxAny     = idxInvalid - 1
	idxLater   = idxInvalid - 2
	idxFirst   = idxInvalid - 3
	idxSingle  = idxInvalid - 4
)

type mapping struct {
	mlvoAt       []mlvo
	definedMLVO  uint8
	hasRange     bool
	layoutIdx    uint32
	variantIdx   uint32
	layoutMin    uint32
	layoutMax    uint32
	active       bool
	candidates   uint32
	kccgstAt     []kccgst
	definedKcCGS uint8
}

type matchType uint8

const (
	matchNormal matchType = iota
	matchWildcardLegacy
	matchWildcardNone
	matchWildcardSome
	matchWildcardAny
	matchGroup
)

type rule struct {
	values  []string
	matches []matchType
	outputs []string
	skip    bool
}

type pendingSlice struct {
	value  string
	kccgst kccgst
	layout uint32
}

type matcher struct {
	ctx     *session
	model   matchedValue
	layouts []matchedValue
	vars    []matchedValue
	options []matchedValue
	groups  []ruleGroup
	mapping mapping
	rule    rule
	pending []pendingSlice
	out     [kcCount]string
}

type ruleGroup struct {
	name     string
	elements []string
}

func splitMLVO(which mlvo, s string) []matchedValue {
	var out []matchedValue
	for {
		end := strings.IndexAny(s, ",!")
		val := matchedValue{layout: allLayouts}
		if end < 0 {
			val.value = strings.TrimSpace(s)
			return append(out, val)
		}
		val.value = strings.TrimSpace(s[:end])
		rest := s[end:]
		if rest[0] == '!' {
			rest = rest[1:]
			next := strings.IndexByte(rest, ',')
			spec := rest
			if next >= 0 {
				spec = rest[:next]
				rest = rest[next:]
			} else {
				rest = ""
			}
			if v, err := strconv.ParseUint(spec, 10, 32); err == nil && v >= 1 && v <= maxGroups && which == mlvoOption {
				val.layout = uint32(v) - 1
			}
		}
		out = append(out, val)
		if rest == "" {
			return out
		}
		s = rest[1:]
	}
}

func newMatcher(ctx *session, names Names) *matcher {
	m := &matcher{ctx: ctx, model: matchedValue{value: names.Model, layout: allLayouts}}
	m.layouts = splitMLVO(mlvoLayout, names.Layout)
	m.vars = splitMLVO(mlvoVariant, names.Variant)
	m.options = splitMLVO(mlvoOption, names.Options)
	for len(m.vars) < len(m.layouts) {
		m.vars = append(m.vars, matchedValue{layout: allLayouts})
	}
	m.vars = m.vars[:len(m.layouts)]
	return m
}

func (m *matcher) startMapping() {
	m.mapping = mapping{layoutIdx: idxInvalid, variantIdx: idxInvalid, active: true}
}

func parseLayoutInt(s string) (uint32, int) {
	end := strings.IndexByte(s, ']')
	if end < 2 {
		return 0, -1
	}
	v, err := strconv.ParseUint(s[1:end], 10, 32)
	if err != nil || v == 0 || v > maxGroups {
		return 0, -1
	}
	return uint32(v) - 1, end + 1
}

func extractLayoutIndex(s string) (uint32, int) {
	if len(s) < 3 || s[0] != '[' {
		return 0, -1
	}
	if strings.HasPrefix(s, "[%i]") {
		return idxInvalid, 4
	}
	return parseLayoutInt(s)
}

func extractMappingIndex(s string) (uint32, int) {
	if len(s) < 3 || s[0] != '[' {
		return 0, -1
	}
	for _, n := range []struct {
		name string
		idx  uint32
	}{{"single]", idxSingle}, {"first]", idxFirst}, {"later]", idxLater}, {"any]", idxAny}} {
		if strings.HasPrefix(s[1:], n.name) {
			return n.idx, len(n.name) + 1
		}
	}
	return parseLayoutInt(s)
}

func (m *matcher) mlvoDefined(which mlvo) bool { return m.mapping.definedMLVO&(1<<which) != 0 }

func (m *matcher) mappingSetMLVO(ident string) {
	which := mlvoCount
	for i, name := range mlvoNames {
		if strings.HasPrefix(ident, name) {
			which = mlvo(i)
			break
		}
	}
	if which == mlvoCount || m.mlvoDefined(which) {
		m.mapping.active = false
		return
	}
	name := mlvoNames[which]
	if len(name) < len(ident) {
		idx, consumed := extractMappingIndex(ident[len(name):])
		if consumed != len(ident)-len(name) {
			m.mapping.active = false
			return
		}
		switch which {
		case mlvoLayout:
			m.mapping.layoutIdx = idx
		case mlvoVariant:
			m.mapping.variantIdx = idx
		default:
			m.mapping.active = false
			return
		}
	} else if which == mlvoLayout {
		m.mapping.layoutIdx = idxSingle
	} else if which == mlvoVariant {
		m.mapping.variantIdx = idxSingle
	}
	if ((which == mlvoLayout && m.mlvoDefined(mlvoVariant)) || (which == mlvoVariant && m.mlvoDefined(mlvoLayout))) &&
		m.mapping.layoutIdx != m.mapping.variantIdx {
		m.mapping.active = false
		return
	}
	m.mapping.mlvoAt = append(m.mapping.mlvoAt, which)
	m.mapping.definedMLVO |= 1 << which
}

func (m *matcher) mappingSetKcCGST(ident string) {
	i := slices.Index(kccgstNames[:], ident)
	if i < 0 || m.mapping.definedKcCGS&(1<<i) != 0 {
		m.mapping.active = false
		return
	}
	m.mapping.kccgstAt = append(m.mapping.kccgstAt, kccgst(i)) //nolint:gosec // bounded by the array
	m.mapping.definedKcCGS |= 1 << i
}

func indexApplies(idx uint32, values []matchedValue) bool {
	switch idx {
	case idxSingle:
		return len(values) <= 1
	case idxAny, idxLater, idxFirst:
		return true
	}
	return len(values) >= 2 && int64(idx) < int64(len(values))
}

func (m *matcher) mappingVerify() bool {
	ok := len(m.mapping.mlvoAt) > 0 && len(m.mapping.kccgstAt) > 0
	if ok && m.mlvoDefined(mlvoLayout) {
		ok = indexApplies(m.mapping.layoutIdx, m.layouts)
	}
	if ok && m.mlvoDefined(mlvoVariant) {
		ok = indexApplies(m.mapping.variantIdx, m.vars)
	}
	if !ok {
		m.mapping.active = false
	}
	return ok
}

func (m *matcher) mappingSetBounds() {
	idx := min(m.mapping.layoutIdx, m.mapping.variantIdx)
	mp := &m.mapping
	defer func() { mp.active = mp.candidates != 0 }()
	limit := uint32(min(maxGroups, len(m.layouts)))
	mask := uint32((uint64(1) << limit) - 1) //nolint:gosec // limit is at most maxGroups
	switch idx {
	case idxInvalid:
		mp.hasRange = false
		mp.layoutMin, mp.layoutMax = idxInvalid, idxInvalid
		mp.candidates = 1
	case idxLater:
		mp.hasRange = true
		mp.layoutMin, mp.layoutMax = 1, limit
		mp.candidates = mask &^ 1
	case idxAny:
		mp.hasRange = true
		mp.layoutMin, mp.layoutMax = 0, limit
		mp.candidates = mask
	default:
		if idx == idxSingle || idx == idxFirst {
			idx = 0
		}
		mp.hasRange = false
		mp.layoutMin, mp.layoutMax = idx, idx+1
		mp.candidates = 1 << idx
	}
}

func (m *matcher) ruleSetMLVO(value string, t matchType) {
	if len(m.rule.values) >= len(m.mapping.mlvoAt) {
		m.rule.skip = true
		return
	}
	m.rule.values = append(m.rule.values, value)
	m.rule.matches = append(m.rule.matches, t)
}

func (m *matcher) matchValue(value string, to *matchedValue, t matchType, wildcardAll bool) bool {
	matched := false
	switch t {
	case matchWildcardLegacy:
		matched = wildcardAll || to.value != ""
	case matchWildcardNone:
		matched = to.value == ""
	case matchWildcardSome:
		matched = to.value != ""
	case matchWildcardAny:
		matched = true
	case matchGroup:
		matched = m.matchGroup(value, to.value)
	default:
		matched = value == to.value
	}
	if matched {
		to.matched = true
	}
	return matched
}

func (m *matcher) matchGroup(name, to string) bool {
	for i := range m.groups {
		if m.groups[i].name == name {
			return slices.Contains(m.groups[i].elements, to)
		}
	}
	return false
}

func isMergePrefix(c byte) bool { return c == '+' || c == '|' || c == '^' }

func (m *matcher) expandPercent(value string, layoutIdx uint32, i *int, out *strings.Builder) error {
	s := value
	if s[*i] == 'i' && (*i+1 == len(s) || isMergePrefix(s[*i+1])) {
		if layoutIdx == idxInvalid {
			return errors.New("%i with no layout or variant in the rules header")
		}
		*i++
		out.WriteString(strconv.FormatUint(uint64(layoutIdx)+1, 10))
		return nil
	}
	var pfx, sfx byte
	if c := s[*i]; c == '(' || isMergePrefix(c) || c == '_' || c == '-' {
		pfx = c
		if c == '(' {
			sfx = ')'
		}
		*i++
		if *i >= len(s) {
			return errors.New("an invalid %-expansion")
		}
	}
	var which mlvo
	switch s[*i] {
	case 'm':
		which = mlvoModel
	case 'l':
		which = mlvoLayout
	case 'v':
		which = mlvoVariant
	default:
		return errors.New("an invalid %-expansion")
	}
	*i++
	idx := idxInvalid
	expandedIndex := false
	if *i < len(s) && s[*i] == '[' {
		if which != mlvoLayout && which != mlvoVariant {
			return errors.New("an index on a %m expansion")
		}
		parsed, consumed := extractLayoutIndex(s[*i:])
		if consumed < 0 {
			return errors.New("an invalid index in a %-expansion")
		}
		*i += consumed
		idx = parsed
		if idx == idxInvalid {
			idx = layoutIdx
			expandedIndex = true
		}
	}
	if sfx != 0 {
		if *i >= len(s) || s[*i] != sfx {
			return errors.New("an unterminated %-expansion")
		}
		*i++
	}
	expanded := m.expansionTarget(which, idx, expandedIndex)
	if expanded == nil || expanded.value == "" {
		return nil
	}
	if pfx != 0 {
		out.WriteByte(pfx)
	}
	out.WriteString(expanded.value)
	if sfx != 0 {
		out.WriteByte(sfx)
	}
	expanded.matched = true
	return nil
}

func (m *matcher) expansionTarget(which mlvo, idx uint32, expandedIndex bool) *matchedValue {
	var values []matchedValue
	switch which {
	case mlvoModel:
		return &m.model
	case mlvoLayout:
		values = m.layouts
	case mlvoVariant:
		values = m.vars
	}
	if idx == idxInvalid {
		if len(values) == 1 {
			return &values[0]
		}
		return nil
	}
	if int64(idx) < int64(len(values)) && (expandedIndex || len(values) > 1) {
		return &values[idx]
	}
	return nil
}

func concatKcCGST(into *string, from string) {
	if isMergePrefix(from[0]) || *into == "" {
		*into += from
		return
	}
	if isMergePrefix((*into)[0]) {
		*into = from + *into
	}
}

func (m *matcher) expandValue(value string, layoutIdx uint32) (string, bool) {
	var out strings.Builder
	lastItem := 0
	hasSeparator := false
	for i := 0; i < len(value); {
		switch c := value[i]; {
		case c == ':':
			out.WriteByte(c)
			i++
			m.expandQualifier(value, &out, hasSeparator, lastItem, &i)
		case c == '%':
			i++
			if i >= len(value) || m.expandPercent(value, layoutIdx, &i, &out) != nil {
				return "", false
			}
		case isMergePrefix(c):
			out.WriteByte(c)
			i++
			lastItem = out.Len() - 1
			hasSeparator = true
		default:
			out.WriteByte(c)
			i++
		}
	}
	return out.String(), true
}

func (m *matcher) expandQualifier(value string, out *strings.Builder, hasSeparator bool, prefixIdx int, i *int) {
	rest := value[*i:]
	if !strings.HasPrefix(rest, "all") || (len(rest) > 3 && !isMergePrefix(rest[3])) {
		return
	}
	*i += 3
	out.WriteByte('1')
	if len(m.layouts) <= 1 {
		return
	}
	current := out.String()
	prefix := current[prefixIdx : len(current)-1]
	for l := 1; l < min(maxGroups, len(m.layouts)); l++ {
		if !hasSeparator {
			out.WriteByte('+')
		}
		out.WriteString(prefix)
		out.WriteString(strconv.Itoa(l + 1))
	}
}

func (m *matcher) flushPending() {
	if !m.mapping.hasRange {
		return
	}
	for _, k := range m.mapping.kccgstAt {
		for layout := m.mapping.layoutMin; layout < m.mapping.layoutMax; layout++ {
			for _, p := range m.pending {
				if p.kccgst == k && p.layout == layout && p.value != "" {
					concatKcCGST(&m.out[k], p.value)
				}
			}
		}
	}
	m.mapping.hasRange = false
}

func (m *matcher) matchOption(value string, t matchType, idx uint32) bool {
	for i := range m.options {
		to := &m.options[i]
		if to.layout != allLayouts && to.layout != idx {
			continue
		}
		if m.matchValue(value, to, t, true) {
			return true
		}
	}
	return false
}

func (m *matcher) matchRangeColumn(which mlvo, value string, t matchType, candidates *uint32) bool {
	matched := false
	for idx := m.mapping.layoutMin; idx < m.mapping.layoutMax && *candidates != 0; idx++ {
		mask := uint32(1) << idx
		if *candidates&mask == 0 {
			continue
		}
		ok := false
		switch which {
		case mlvoLayout:
			ok = m.matchValue(value, &m.layouts[idx], t, false)
		case mlvoVariant:
			ok = m.matchValue(value, &m.vars[idx], t, false)
		default:
			ok = m.matchOption(value, t, idx)
		}
		if ok {
			matched = true
		} else {
			*candidates &^= mask
		}
	}
	return matched
}

func (m *matcher) ruleApply() {
	candidates := m.mapping.candidates
	for i, which := range m.mapping.mlvoAt {
		value, t := m.rule.values[i], m.rule.matches[i]
		matched := false
		switch {
		case which == mlvoModel:
			matched = m.matchValue(value, &m.model, t, true)
		case m.mapping.hasRange:
			matched = m.matchRangeColumn(which, value, t, &candidates)
		case which == mlvoLayout:
			matched = m.matchValue(value, &m.layouts[m.mapping.layoutMin], t, false)
		case which == mlvoVariant:
			matched = m.matchValue(value, &m.vars[m.mapping.layoutMin], t, false)
		default:
			matched = m.matchOption(value, t, m.mapping.layoutMin)
		}
		if !matched {
			return
		}
	}
	if m.mapping.hasRange {
		for idx := m.mapping.layoutMin; idx < m.mapping.layoutMax; idx++ {
			if candidates&(1<<idx) == 0 {
				continue
			}
			for i, k := range m.mapping.kccgstAt {
				if expanded, ok := m.expandValue(m.rule.outputs[i], idx); ok {
					m.pending = append(m.pending, pendingSlice{value: expanded, kccgst: k, layout: idx})
				}
			}
		}
	} else {
		for i, k := range m.mapping.kccgstAt {
			if expanded, ok := m.expandValue(m.rule.outputs[i], m.mapping.layoutMin); ok && expanded != "" {
				concatKcCGST(&m.out[k], expanded)
			}
		}
	}
	if !m.mlvoDefined(mlvoOption) {
		m.mapping.candidates &^= candidates
		m.mapping.active = m.mapping.candidates != 0
	}
}

func (m *matcher) include(name string, depth int) error {
	if depth >= rulesIncludeMax {
		return fmt.Errorf("exceeded the rules include depth of %d at %q", rulesIncludeMax, name)
	}
	target, expanded, err := m.ctx.expandPath(name, fileRules)
	if err != nil {
		return err
	}
	var candidates []string
	switch {
	case filepath.IsAbs(target):
		candidates = []string{target}
	case !expanded:
		for _, dir := range m.ctx.paths {
			candidates = append(candidates, filepath.Join(dir, "rules", target))
		}
	}
	for _, path := range candidates {
		src, err := m.ctx.read(path)
		if err != nil {
			continue
		}
		if err := m.readRules(path, src, depth+1); err == nil {
			return nil
		}
	}
	return nil
}

func (m *matcher) readRules(path string, src []byte, depth int) error {
	l := &rulesLexer{name: path, src: unsafe.String(unsafe.SliceData(src), len(src))} //nolint:gosec // the session keeps src and never writes to it
	return m.run(l, depth)
}

func (m *matcher) run(l *rulesLexer, depth int) error {
	inMapping := false
	for {
		tok, err := l.next()
		if err != nil {
			return err
		}
		switch tok {
		case rtEOL:
			continue
		case rtEOF:
			if inMapping {
				m.flushPending()
			}
			return nil
		case rtBang:
			if inMapping {
				m.flushPending()
			}
			mapped, err := m.runBang(l, depth)
			if err != nil {
				return err
			}
			inMapping = mapped
		default:
			if !inMapping {
				return fmt.Errorf("%s: an unexpected token", l.name)
			}
			if err := m.runRule(l, tok); err != nil {
				return err
			}
		}
	}
}

func (m *matcher) runBang(l *rulesLexer, depth int) (bool, error) {
	tok, err := l.next()
	if err != nil {
		return false, err
	}
	switch tok {
	case rtGroupName:
		return false, m.runGroup(l)
	case rtInclude:
		return false, m.runInclude(l, depth)
	case rtIdent:
		m.startMapping()
		m.mappingSetMLVO(l.text)
		return true, m.runMapping(l)
	}
	return false, fmt.Errorf("%s: an unexpected token after '!'", l.name)
}

func (m *matcher) runGroup(l *rulesLexer) error {
	m.groups = append(m.groups, ruleGroup{name: l.text})
	tok, err := l.next()
	if err != nil {
		return err
	}
	if tok != rtEquals {
		return fmt.Errorf("%s: expected '=' after a rules group name", l.name)
	}
	group := &m.groups[len(m.groups)-1]
	for {
		tok, err := l.next()
		if err != nil {
			return err
		}
		switch tok {
		case rtIdent:
			group.elements = append(group.elements, l.text)
		case rtEOL:
			return nil
		default:
			return fmt.Errorf("%s: an unexpected token in a rules group", l.name)
		}
	}
}

func (m *matcher) runInclude(l *rulesLexer, depth int) error {
	tok, err := l.next()
	if err != nil {
		return err
	}
	if tok != rtIdent {
		return fmt.Errorf("%s: expected a file after !include", l.name)
	}
	if err := m.include(l.text, depth); err != nil {
		return err
	}
	tok, err = l.next()
	if err != nil {
		return err
	}
	if tok != rtEOL {
		return fmt.Errorf("%s: expected the end of the line after !include", l.name)
	}
	return nil
}

func (m *matcher) runMapping(l *rulesLexer) error {
	for {
		tok, err := l.next()
		if err != nil {
			return err
		}
		if tok == rtEquals {
			break
		}
		if tok != rtIdent {
			return fmt.Errorf("%s: an unexpected token in a mapping", l.name)
		}
		if m.mapping.active {
			m.mappingSetMLVO(l.text)
		}
	}
	for {
		tok, err := l.next()
		if err != nil {
			return err
		}
		switch tok {
		case rtIdent:
			if m.mapping.active {
				m.mappingSetKcCGST(l.text)
			}
		case rtEOL:
			if m.mapping.active && m.mappingVerify() {
				m.mappingSetBounds()
				if m.mapping.hasRange {
					m.pending = m.pending[:0]
				}
			}
			return nil
		default:
			return fmt.Errorf("%s: an unexpected token in a mapping", l.name)
		}
	}
}

func (m *matcher) runRule(l *rulesLexer, first rulesToken) error {
	m.rule = rule{skip: !m.mapping.active}
	tok := first
	for {
		if !m.rule.skip {
			switch tok {
			case rtIdent:
				if l.text == "+" {
					m.ruleSetMLVO("", matchWildcardSome)
				} else {
					m.ruleSetMLVO(l.text, matchNormal)
				}
			case rtStar:
				m.ruleSetMLVO("", matchWildcardLegacy)
			case rtNone:
				m.ruleSetMLVO("", matchWildcardNone)
			case rtSome:
				m.ruleSetMLVO("", matchWildcardSome)
			case rtAny:
				m.ruleSetMLVO("", matchWildcardAny)
			case rtGroupName:
				m.ruleSetMLVO(l.text, matchGroup)
			}
		}
		switch tok {
		case rtIdent, rtStar, rtNone, rtSome, rtAny, rtGroupName:
		case rtEquals:
			return m.runRuleOutputs(l)
		default:
			return fmt.Errorf("%s: an unexpected token in a rule", l.name)
		}
		var err error
		if tok, err = l.next(); err != nil {
			return err
		}
	}
}

func (m *matcher) runRuleOutputs(l *rulesLexer) error {
	for {
		tok, err := l.next()
		if err != nil {
			return err
		}
		switch tok {
		case rtIdent:
			if !m.rule.skip {
				if len(m.rule.outputs) >= len(m.mapping.kccgstAt) {
					m.rule.skip = true
				} else {
					m.rule.outputs = append(m.rule.outputs, l.text)
				}
			}
		case rtEOL:
			if !m.rule.skip && (len(m.rule.values) != len(m.mapping.mlvoAt) || len(m.rule.outputs) != len(m.mapping.kccgstAt)) {
				m.rule.skip = true
			}
			if !m.rule.skip {
				m.ruleApply()
			}
			return nil
		default:
			return fmt.Errorf("%s: an unexpected token in a rule", l.name)
		}
	}
}

type components4 struct {
	keycodes, types, compat, symbols string
}

func (c *session) resolveRules(names Names) (components4, uint32, error) {
	m := newMatcher(c, names)
	var mainPath string
	var mainSrc []byte
	for _, dir := range c.paths {
		path := filepath.Join(dir, "rules", names.Rules)
		src, err := c.read(path)
		if err == nil {
			mainPath, mainSrc = path, src
			break
		}
	}
	if mainPath == "" {
		return components4{}, 0, fmt.Errorf("no rules file %q in %v", names.Rules, c.paths)
	}
	if err := m.readPartial(c, names.Rules+".pre"); err != nil {
		return components4{}, 0, err
	}
	if err := m.readRules(mainPath, mainSrc, 0); err != nil {
		return components4{}, 0, fmt.Errorf("parse rules: %w", err)
	}
	if err := m.readPartial(c, names.Rules+".post"); err != nil {
		return components4{}, 0, err
	}
	if m.out[kcKeycodes] == "" || m.out[kcTypes] == "" || m.out[kcCompat] == "" || m.out[kcSymbols] == "" {
		return components4{}, 0, fmt.Errorf("no components returned from rules %q", names.Rules)
	}
	return components4{m.out[kcKeycodes], m.out[kcTypes], m.out[kcCompat], m.out[kcSymbols]}, explicitLayouts(m.out[kcSymbols]), nil
}

func (m *matcher) readPartial(c *session, name string) error {
	for _, dir := range c.paths {
		path := filepath.Join(dir, "rules", name)
		src, err := c.read(path)
		if err != nil {
			continue
		}
		if err := m.readRules(path, src, 0); err != nil {
			return fmt.Errorf("parse rules: %w", err)
		}
	}
	return nil
}

func explicitLayouts(symbols string) uint32 {
	count := uint32(1)
	rest := symbols
	for {
		i := strings.IndexByte(rest, ':')
		if i < 0 || i+1 >= len(rest) {
			return count
		}
		rest = rest[i+1:]
		end := 0
		for end < len(rest) && isDigit(rest[end]) {
			end++
		}
		if end == 0 || (end < len(rest) && !isMergePrefix(rest[end])) {
			continue
		}
		if v, err := strconv.ParseUint(rest[:end], 10, 32); err == nil && v > 0 && v <= maxGroups {
			count = max(count, uint32(v))
			rest = rest[end:]
		}
	}
}
