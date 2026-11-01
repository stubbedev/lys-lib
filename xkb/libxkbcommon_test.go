package xkb

import (
	"os"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"unsafe"

	"github.com/ebitengine/purego"

	"github.com/stubbedev/lys-lib/clib"
)

const (
	formatTextV1  = 1
	stateModsEff  = 1 << 3
	stateLayoutEf = 1 << 7
)

type ruleNamesC struct {
	rules, model, layout, variant, options *byte
}

type libxkb struct {
	contextNew      func(flags int32) uintptr
	newFromNames2   func(ctx uintptr, names *ruleNamesC, format, flags int32) uintptr
	newFromString   func(ctx uintptr, s *byte, format, flags int32) uintptr
	keymapString    func(km uintptr, format int32) unsafe.Pointer
	keymapUnref     func(km uintptr)
	minKeycode      func(km uintptr) uint32
	maxKeycode      func(km uintptr) uint32
	numLayouts      func(km uintptr) uint32
	numLayoutsKey   func(km, kc uintptr) uint32
	numLevels       func(km uintptr, kc, layout uint32) uint32
	symsByLevel     func(km uintptr, kc, layout, level uint32, syms **uint32) int32
	keyName         func(km uintptr, kc uint32) *byte
	keyRepeats      func(km uintptr, kc uint32) int32
	numMods         func(km uintptr) uint32
	modName         func(km uintptr, i uint32) *byte
	numLEDs         func(km uintptr) uint32
	ledName         func(km uintptr, i uint32) *byte
	layoutName      func(km uintptr, i uint32) *byte
	stateNew        func(km uintptr) uintptr
	stateUnref      func(st uintptr)
	updateKey       func(st uintptr, kc uint32, dir int32) int32
	updateMask      func(st uintptr, dep, lat, lock, depL, latL, lockL uint32) int32
	oneSym          func(st uintptr, kc uint32) uint32
	stateSyms       func(st uintptr, kc uint32, syms **uint32) int32
	stateUTF8       func(st uintptr, kc uint32, buf *byte, size uintptr) int32
	stateUTF32      func(st uintptr, kc uint32) uint32
	keyLayout       func(st uintptr, kc uint32) uint32
	keyLevel        func(st uintptr, kc, layout uint32) uint32
	serializeMods   func(st uintptr, comp int32) uint32
	serializeLayout func(st uintptr, comp int32) uint32
	ledActive       func(st uintptr, name *byte) int32
	modActive       func(st uintptr, name *byte, comp int32) int32
	keysymFromName  func(name *byte, flags int32) uint32
	keysymGetName   func(sym uint32, buf *byte, size uintptr) int32
	keysymUTF32     func(sym uint32) uint32
	utf32Keysym     func(ucs uint32) uint32
	keysymUpper     func(sym uint32) uint32
	keysymLower     func(sym uint32) uint32
	composeBuffer   func(ctx uintptr, s *byte, n uintptr, locale *byte, format, flags int32) uintptr
	composeState    func(table uintptr, flags int32) uintptr
	composeFeed     func(st uintptr, sym uint32) int32
	composeStatus   func(st uintptr) int32
	composeUTF8     func(st uintptr, buf *byte, size uintptr) int32
	composeOneSym   func(st uintptr) uint32
	ctx             uintptr
}

var (
	libxkbOnce sync.Once
	libxkbLib  *libxkb
	errLibxkb  error
)

func loadLibxkb(t testing.TB) *libxkb {
	t.Helper()
	libxkbOnce.Do(func() { libxkbLib, errLibxkb = openLibxkb() })
	if errLibxkb != nil {
		t.Fatalf("libxkbcommon: %v", errLibxkb)
	}
	return libxkbLib
}

func openLibxkb() (*libxkb, error) {
	h, err := clib.Open(clib.SystemCandidates("libxkbcommon.so.0"))
	if err != nil {
		return nil, err
	}
	l := &libxkb{}
	for _, f := range []struct {
		fn   any
		name string
	}{
		{&l.contextNew, "xkb_context_new"},
		{&l.newFromNames2, "xkb_keymap_new_from_names2"},
		{&l.newFromString, "xkb_keymap_new_from_string"},
		{&l.keymapString, "xkb_keymap_get_as_string"},
		{&l.keymapUnref, "xkb_keymap_unref"},
		{&l.minKeycode, "xkb_keymap_min_keycode"},
		{&l.maxKeycode, "xkb_keymap_max_keycode"},
		{&l.numLayouts, "xkb_keymap_num_layouts"},
		{&l.numLayoutsKey, "xkb_keymap_num_layouts_for_key"},
		{&l.numLevels, "xkb_keymap_num_levels_for_key"},
		{&l.symsByLevel, "xkb_keymap_key_get_syms_by_level"},
		{&l.keyName, "xkb_keymap_key_get_name"},
		{&l.keyRepeats, "xkb_keymap_key_repeats"},
		{&l.numMods, "xkb_keymap_num_mods"},
		{&l.modName, "xkb_keymap_mod_get_name"},
		{&l.numLEDs, "xkb_keymap_num_leds"},
		{&l.ledName, "xkb_keymap_led_get_name"},
		{&l.layoutName, "xkb_keymap_layout_get_name"},
		{&l.stateNew, "xkb_state_new"},
		{&l.stateUnref, "xkb_state_unref"},
		{&l.updateKey, "xkb_state_update_key"},
		{&l.updateMask, "xkb_state_update_mask"},
		{&l.oneSym, "xkb_state_key_get_one_sym"},
		{&l.stateSyms, "xkb_state_key_get_syms"},
		{&l.stateUTF8, "xkb_state_key_get_utf8"},
		{&l.stateUTF32, "xkb_state_key_get_utf32"},
		{&l.keyLayout, "xkb_state_key_get_layout"},
		{&l.keyLevel, "xkb_state_key_get_level"},
		{&l.serializeMods, "xkb_state_serialize_mods"},
		{&l.serializeLayout, "xkb_state_serialize_layout"},
		{&l.ledActive, "xkb_state_led_name_is_active"},
		{&l.modActive, "xkb_state_mod_name_is_active"},
		{&l.keysymFromName, "xkb_keysym_from_name"},
		{&l.keysymGetName, "xkb_keysym_get_name"},
		{&l.keysymUTF32, "xkb_keysym_to_utf32"},
		{&l.utf32Keysym, "xkb_utf32_to_keysym"},
		{&l.keysymUpper, "xkb_keysym_to_upper"},
		{&l.keysymLower, "xkb_keysym_to_lower"},
		{&l.composeBuffer, "xkb_compose_table_new_from_buffer"},
		{&l.composeState, "xkb_compose_state_new"},
		{&l.composeFeed, "xkb_compose_state_feed"},
		{&l.composeStatus, "xkb_compose_state_get_status"},
		{&l.composeUTF8, "xkb_compose_state_get_utf8"},
		{&l.composeOneSym, "xkb_compose_state_get_one_sym"},
	} {
		if _, err := purego.Dlsym(h, f.name); err != nil {
			return nil, err
		}
		purego.RegisterLibFunc(f.fn, h, f.name)
	}
	if l.ctx = l.contextNew(0); l.ctx == 0 {
		return nil, os.ErrInvalid
	}
	return l, nil
}

func cstr(s string) *byte {
	b := append([]byte(s), 0)
	return &b[0]
}

func goStr(p *byte) string { return clib.GoString(p) }

func (l *libxkb) compile(t testing.TB, n Names) uintptr {
	t.Helper()
	names := &ruleNamesC{cstr(n.Rules), cstr(n.Model), cstr(n.Layout), cstr(n.Variant), cstr(n.Options)}
	km := l.newFromNames2(l.ctx, names, formatTextV1, 0)
	runtime.KeepAlive(names)
	if km == 0 {
		t.Fatalf("libxkbcommon cannot compile %+v", n)
	}
	return km
}

func (l *libxkb) parse(text string) uintptr {
	return l.newFromString(l.ctx, cstr(text), formatTextV1, 0)
}

func (l *libxkb) text(t testing.TB, km uintptr) string {
	t.Helper()
	p := l.keymapString(km, formatTextV1)
	if p == nil {
		t.Fatal("xkb_keymap_get_as_string returned NULL")
	}
	return goStr((*byte)(p))
}

func testData(t testing.TB) *Context {
	t.Helper()
	root := os.Getenv("XKB_CONFIG_ROOT")
	if root == "" {
		t.Fatal("XKB_CONFIG_ROOT is not set: the dev shell points it at xkeyboard-config")
	}
	return NewContext(func(name string) string {
		if name == "XKB_CONFIG_ROOT" {
			return root
		}
		return ""
	})
}

func symsSlice(p *uint32, n int32) []uint32 {
	if n <= 0 {
		return nil
	}
	return unsafe.Slice(p, n)
}

func toU32(s []Keysym) []uint32 {
	out := make([]uint32, len(s))
	for i, v := range s {
		out[i] = uint32(v)
	}
	return out
}

func normalizeText(s string) string {
	lines := strings.Split(s, "\n")
	var out []string
	var aliases []string
	flush := func() {
		slices.Sort(aliases)
		out = append(out, aliases...)
		aliases = nil
	}
	for _, line := range lines {
		if strings.HasPrefix(line, "\talias <") {
			aliases = append(aliases, line)
			continue
		}
		flush()
		out = append(out, line)
	}
	flush()
	return strings.Join(out, "\n")
}

func firstDiff(a, b string) string {
	la, lb := strings.Split(a, "\n"), strings.Split(b, "\n")
	for i := range min(len(la), len(lb)) {
		if la[i] != lb[i] {
			return "line " + itoa(i+1) + ":\n  go: " + la[i] + "\n  c:  " + lb[i]
		}
	}
	return "line counts " + itoa(len(la)) + " and " + itoa(len(lb))
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	s := ""
	for ; v > 0; v /= 10 {
		s = string(rune('0'+v%10)) + s
	}
	return s
}

func compareKeymaps(t *testing.T, l *libxkb, g *Keymap, c uintptr) {
	t.Helper()
	if lo, hi := l.minKeycode(c), l.maxKeycode(c); uint32(g.MinKeycode()) != lo || uint32(g.MaxKeycode()) != hi {
		t.Errorf("keycodes: go %d..%d, c %d..%d", g.MinKeycode(), g.MaxKeycode(), lo, hi)
	}
	if n := l.numLayouts(c); g.NumLayouts() != n {
		t.Errorf("layouts: go %d, c %d", g.NumLayouts(), n)
	}
	for i := range l.numLayouts(c) {
		if a, b := g.LayoutName(i), goStr(l.layoutName(c, i)); a != b {
			t.Errorf("layout %d name: go %q, c %q", i, a, b)
		}
	}
	if n := l.numMods(c); g.NumMods() != n {
		t.Errorf("mods: go %d, c %d", g.NumMods(), n)
	}
	for i := range min(g.NumMods(), l.numMods(c)) {
		if a, b := g.ModName(i), goStr(l.modName(c, i)); a != b {
			t.Errorf("mod %d: go %q, c %q", i, a, b)
		}
	}
	if n := l.numLEDs(c); g.NumLEDs() != n {
		t.Errorf("leds: go %d, c %d", g.NumLEDs(), n)
	}
	for i := range min(g.NumLEDs(), l.numLEDs(c)) {
		if a, b := g.LEDName(i), goStr(l.ledName(c, i)); a != b {
			t.Errorf("led %d: go %q, c %q", i, a, b)
		}
	}
	for kc := l.minKeycode(c); kc <= l.maxKeycode(c); kc++ {
		compareKey(t, l, g, c, kc)
	}
}

func compareKey(t *testing.T, l *libxkb, g *Keymap, c uintptr, kc uint32) {
	t.Helper()
	k := Keycode(kc)
	if a, b := g.KeyGetName(k), goStr(l.keyName(c, kc)); a != b {
		t.Errorf("key %d name: go %q, c %q", kc, a, b)
	}
	if a, b := g.KeyRepeats(k), l.keyRepeats(c, kc) == 1; a != b {
		t.Errorf("key %d <%s> repeats: go %v, c %v", kc, g.KeyGetName(k), a, b)
	}
	layouts := l.numLayoutsKey(c, uintptr(kc))
	if g.NumLayoutsForKey(k) != layouts {
		t.Errorf("key %d <%s> layouts: go %d, c %d", kc, g.KeyGetName(k), g.NumLayoutsForKey(k), layouts)
		return
	}
	for layout := range layouts {
		levels := l.numLevels(c, kc, layout)
		if g.NumLevels(k, layout) != levels {
			t.Errorf("key %d <%s> layout %d levels: go %d, c %d", kc, g.KeyGetName(k), layout, g.NumLevels(k, layout), levels)
			continue
		}
		for lvl := range levels {
			var p *uint32
			n := l.symsByLevel(c, kc, layout, lvl, &p)
			if a, b := toU32(g.KeySymsByLevel(k, layout, lvl)), symsSlice(p, n); !slices.Equal(a, b) {
				t.Errorf("key %d <%s> layout %d level %d: go %#x, c %#x", kc, g.KeyGetName(k), layout, lvl, a, b)
			}
		}
	}
}

type stateProbe struct {
	l  *libxkb
	km *Keymap
	c  uintptr
	st *State
	cs uintptr
}

func newProbe(l *libxkb, g *Keymap, c uintptr) *stateProbe {
	return &stateProbe{l: l, km: g, c: c, st: g.NewState(), cs: l.stateNew(c)}
}

func (p *stateProbe) compare(t *testing.T, where string) bool {
	t.Helper()
	ok := true
	for _, comp := range []StateComponent{StateModDepressed, StateModLatched, StateModLocked, StateModEffective} {
		if a, b := uint32(p.st.SerializeMods(comp)), p.l.serializeMods(p.cs, int32(comp)); a != b {
			t.Errorf("%s: mods %#x: go %#x, c %#x", where, comp, a, b)
			ok = false
		}
	}
	for _, comp := range []StateComponent{StateLayoutDepressed, StateLayoutLatched, StateLayoutLocked, StateLayoutEffective} {
		if a, b := p.st.SerializeLayout(comp), p.l.serializeLayout(p.cs, int32(comp)); a != b {
			t.Errorf("%s: layout %#x: go %d, c %d", where, comp, a, b)
			ok = false
		}
	}
	for i := range p.km.NumLEDs() {
		name := p.km.LEDName(i)
		if name == "" {
			continue
		}
		if a, b := p.st.LEDNameIsActive(name), p.l.ledActive(p.cs, cstr(name)) == 1; a != b {
			t.Errorf("%s: LED %q: go %v, c %v", where, name, a, b)
			ok = false
		}
	}
	for i := range p.km.NumMods() {
		name := p.km.ModName(i)
		if a, b := p.st.ModNameIsActive(name, StateModEffective), p.l.modActive(p.cs, cstr(name), stateModsEff) == 1; a != b {
			t.Errorf("%s: mod %q active: go %v, c %v", where, name, a, b)
			ok = false
		}
	}
	return ok
}

func (p *stateProbe) compareKeys(t *testing.T, where string) bool {
	t.Helper()
	ok := true
	var buf [64]byte
	for kc := p.l.minKeycode(p.c); kc <= p.l.maxKeycode(p.c); kc++ {
		k := Keycode(kc)
		if a, b := uint32(p.st.KeyGetOneSym(k)), p.l.oneSym(p.cs, kc); a != b {
			t.Errorf("%s: key %d <%s> one sym: go %#x, c %#x", where, kc, p.km.KeyGetName(k), a, b)
			ok = false
		}
		var sp *uint32
		n := p.l.stateSyms(p.cs, kc, &sp)
		if a, b := toU32(p.st.KeyGetSyms(k)), symsSlice(sp, n); !slices.Equal(a, b) {
			t.Errorf("%s: key %d syms: go %#x, c %#x", where, kc, a, b)
			ok = false
		}
		m := p.l.stateUTF8(p.cs, kc, &buf[0], uintptr(len(buf)))
		c := ""
		if m > 0 {
			c = string(buf[:m])
		}
		if a := p.st.KeyGetUTF8(k); a != c {
			t.Errorf("%s: key %d <%s> utf8: go %q, c %q", where, kc, p.km.KeyGetName(k), a, c)
			ok = false
		}
		if a, b := uint32(p.st.KeyGetUTF32(k)), p.l.stateUTF32(p.cs, kc); a != b {
			t.Errorf("%s: key %d utf32: go %#x, c %#x", where, kc, a, b)
			ok = false
		}
		if a, okG := p.st.KeyGetLayout(k); (okG && a != p.l.keyLayout(p.cs, kc)) || (!okG && p.l.keyLayout(p.cs, kc) != layoutInvalid) {
			t.Errorf("%s: key %d layout: go %d/%v, c %d", where, kc, a, okG, p.l.keyLayout(p.cs, kc))
			ok = false
		}
	}
	return ok
}
