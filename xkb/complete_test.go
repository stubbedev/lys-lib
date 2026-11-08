package xkb

import (
	"encoding/xml"
	"maps"
	"math/rand/v2"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	"unsafe"

	"github.com/ebitengine/purego"

	"github.com/stubbedev/lys-lib/clib"
	"github.com/stubbedev/lys-lib/testbuild"
)

type libxkbMore struct {
	asString2         func(km uintptr, format, flags int32) unsafe.Pointer
	consumed2         func(st uintptr, kc uint32, mode int32) uint32
	consumed          func(st uintptr, kc uint32) uint32
	isConsumed2       func(st uintptr, kc, idx uint32, mode int32) int32
	isConsumed        func(st uintptr, kc, idx uint32) int32
	removeConsumed    func(st uintptr, kc, mask uint32) uint32
	updateLatched     func(st uintptr, affLatchMods, latchMods uint32, affLatchLayout bool, latchLayout int32, affLockMods, lockMods uint32, affLockLayout bool, lockLayout int32) int32
	modIndexActive    func(st uintptr, idx uint32, comp int32) int32
	layoutIndexActive func(st uintptr, idx uint32, comp int32) int32
	layoutNameActive  func(st uintptr, name *byte, comp int32) int32
	ledIndexActive    func(st uintptr, idx uint32) int32
	modMask           func(km uintptr, name *byte) uint32
	modMask2          func(km uintptr, idx uint32) uint32
	modsForLevel      func(km uintptr, kc, layout, level uint32, out *uint32, size uintptr) uintptr
	keyByName         func(km uintptr, name *byte) uint32
	layoutIndex       func(km uintptr, name *byte) uint32
	ledIndex          func(km uintptr, name *byte) uint32
	modIndex          func(km uintptr, name *byte) uint32
	builderNew        func(ctx uintptr, rules, model *byte, flags int32) uintptr
	builderLayout     func(b uintptr, layout, variant *byte, options **byte, n uintptr) bool
	builderOption     func(b uintptr, option *byte) bool
	fromRMLVO         func(b uintptr, format, flags int32) uintptr
	componentsFrom    func(ctx uintptr, in, out *ruleNamesC, comps *componentsC) bool
	newFromBuffer     func(ctx uintptr, s *byte, n uintptr, format, flags int32) uintptr
}

type componentsC struct {
	keycodes, compat, geometry, symbols, types *byte
}

var (
	moreOnce sync.Once
	moreLib  *libxkbMore
	errMore  error
)

func loadMore(t testing.TB) *libxkbMore {
	t.Helper()
	moreOnce.Do(func() {
		h, err := clib.Open(clib.SystemCandidates("libxkbcommon.so.0"))
		if err != nil {
			errMore = err
			return
		}
		m := &libxkbMore{}
		for _, f := range []struct {
			fn   any
			name string
		}{
			{&m.asString2, "xkb_keymap_get_as_string2"},
			{&m.consumed2, "xkb_state_key_get_consumed_mods2"},
			{&m.consumed, "xkb_state_key_get_consumed_mods"},
			{&m.isConsumed2, "xkb_state_mod_index_is_consumed2"},
			{&m.isConsumed, "xkb_state_mod_index_is_consumed"},
			{&m.removeConsumed, "xkb_state_mod_mask_remove_consumed"},
			{&m.updateLatched, "xkb_state_update_latched_locked"},
			{&m.modIndexActive, "xkb_state_mod_index_is_active"},
			{&m.layoutIndexActive, "xkb_state_layout_index_is_active"},
			{&m.layoutNameActive, "xkb_state_layout_name_is_active"},
			{&m.ledIndexActive, "xkb_state_led_index_is_active"},
			{&m.modMask, "xkb_keymap_mod_get_mask"},
			{&m.modMask2, "xkb_keymap_mod_get_mask2"},
			{&m.modsForLevel, "xkb_keymap_key_get_mods_for_level"},
			{&m.keyByName, "xkb_keymap_key_by_name"},
			{&m.layoutIndex, "xkb_keymap_layout_get_index"},
			{&m.ledIndex, "xkb_keymap_led_get_index"},
			{&m.modIndex, "xkb_keymap_mod_get_index"},
			{&m.builderNew, "xkb_rmlvo_builder_new"},
			{&m.builderLayout, "xkb_rmlvo_builder_append_layout"},
			{&m.builderOption, "xkb_rmlvo_builder_append_option"},
			{&m.fromRMLVO, "xkb_keymap_new_from_rmlvo"},
			{&m.componentsFrom, "xkb_components_names_from_rules"},
			{&m.newFromBuffer, "xkb_keymap_new_from_buffer"},
		} {
			if _, err := purego.Dlsym(h, f.name); err != nil {
				errMore = err
				return
			}
			purego.RegisterLibFunc(f.fn, h, f.name)
		}
		moreLib = m
	})
	if errMore != nil {
		t.Fatalf("libxkbcommon: %v", errMore)
	}
	return moreLib
}

var formats = []KeymapFormat{FormatTextV1, FormatTextV2}

func (m *libxkbMore) textOf(km uintptr, format KeymapFormat, flags SerializeFlags) string {
	p := m.asString2(km, int32(format), int32(flags))
	if p == nil {
		return ""
	}
	return goStr((*byte)(p))
}

func quietContext(t testing.TB) *Context {
	t.Helper()
	ctx := testData(t)
	ctx.SetLogLevel(LogCritical)
	return ctx
}

func compareConsumed(t *testing.T, l *libxkb, more *libxkbMore, p *stateProbe, where string, limit uint32) {
	t.Helper()
	for kc := l.minKeycode(p.c); kc <= min(l.maxKeycode(p.c), limit); kc++ {
		k := Keycode(kc)
		for _, mode := range []ConsumedMode{ConsumedModeXKB, ConsumedModeGTK} {
			if a, b := uint32(p.st.KeyGetConsumedMods(k, mode)), more.consumed2(p.cs, kc, int32(mode)); a != b {
				t.Errorf("%s: key %d consumed mode %d: go %#x, c %#x", where, kc, mode, a, b)
				return
			}
			for i := range p.km.NumMods() {
				consumed, ok := p.st.ModIndexIsConsumed(k, i, mode)
				want := more.isConsumed2(p.cs, kc, i, int32(mode))
				if ok != (want >= 0) || (ok && consumed != (want == 1)) {
					t.Errorf("%s: key %d mod %d consumed mode %d: go %v/%v, c %d", where, kc, i, mode, consumed, ok, want)
					return
				}
			}
		}
		if a, b := uint32(p.st.ModMaskRemoveConsumed(k, 0xffffffff)), more.removeConsumed(p.cs, kc, 0xffffffff); a != b {
			t.Errorf("%s: key %d remove consumed: go %#x, c %#x", where, kc, a, b)
			return
		}
	}
}

func compareQueries(t *testing.T, l *libxkb, more *libxkbMore, p *stateProbe, where string) {
	t.Helper()
	comps := []StateComponent{StateModDepressed, StateModLatched, StateModLocked, StateModEffective, StateModDepressed | StateModLocked}
	for i := range p.km.NumMods() + 1 {
		for _, comp := range comps {
			active, ok := p.st.ModIndexIsActive(i, comp)
			want := more.modIndexActive(p.cs, i, int32(comp))
			if ok != (want >= 0) || (ok && active != (want == 1)) {
				t.Errorf("%s: mod %d in %#x: go %v/%v, c %d", where, i, comp, active, ok, want)
			}
		}
	}
	for i := range p.km.NumLayouts() + 1 {
		for _, comp := range []StateComponent{StateLayoutDepressed, StateLayoutLatched, StateLayoutLocked, StateLayoutEffective} {
			active, ok := p.st.LayoutIndexIsActive(i, comp)
			want := more.layoutIndexActive(p.cs, i, int32(comp))
			if ok != (want >= 0) || (ok && active != (want == 1)) {
				t.Errorf("%s: layout %d in %#x: go %v/%v, c %d", where, i, comp, active, ok, want)
			}
		}
	}
	for i := range p.km.NumLEDs() + 1 {
		ok := i < p.km.NumLEDs() && p.km.LEDName(i) != ""
		want := more.ledIndexActive(p.cs, i)
		if ok != (want >= 0) || (ok && p.st.LEDIndexIsActive(i) != (want == 1)) {
			t.Errorf("%s: LED %d: go %v/%v, c %d", where, i, p.st.LEDIndexIsActive(i), ok, want)
		}
	}
}

func compareStatics(t *testing.T, l *libxkb, more *libxkbMore, g *Keymap, c uintptr) {
	t.Helper()
	for i := range g.NumMods() {
		name := g.ModName(i)
		if a, b := uint32(g.ModMaskOf(name)), more.modMask(c, cstr(name)); a != b {
			t.Errorf("mod %q mask: go %#x, c %#x", name, a, b)
		}
		if a, b := uint32(g.ModMaskOfIndex(i)), more.modMask2(c, i); a != b {
			t.Errorf("mod %d mask: go %#x, c %#x", i, a, b)
		}
		if idx, ok := g.ModIndex(name); !ok || idx != more.modIndex(c, cstr(name)) {
			t.Errorf("mod %q index: go %d, c %d", name, idx, more.modIndex(c, cstr(name)))
		}
	}
	for i := range g.NumLayouts() {
		name := g.LayoutName(i)
		if name == "" {
			continue
		}
		idx, ok := g.LayoutIndex(name)
		want := more.layoutIndex(c, cstr(name))
		if !ok || idx != want {
			t.Errorf("layout %q index: go %d/%v, c %d", name, idx, ok, want)
		}
	}
	var buf [64]uint32
	g.KeyForEach(func(k Keycode) {
		kc := uint32(k)
		name := g.KeyGetName(k)
		if got, ok := g.KeyByName(name); name != "" && (!ok || uint32(got) != more.keyByName(c, cstr(name))) {
			t.Errorf("key %q by name: go %d, c %d", name, got, more.keyByName(c, cstr(name)))
		}
		for layout := range l.numLayoutsKey(c, uintptr(kc)) + 2 {
			for lvl := range g.NumLevels(k, layout) + 1 {
				n := more.modsForLevel(c, kc, layout, lvl, &buf[0], uintptr(len(buf)))
				got := g.KeyGetModsForLevel(k, layout, lvl)
				if uintptr(len(got)) != n {
					t.Errorf("key %d layout %d level %d mods: go %v, c %v", kc, layout, lvl, got, buf[:n])
					continue
				}
				for i := range got {
					if uint32(got[i]) != buf[i] {
						t.Errorf("key %d layout %d level %d mods: go %v, c %v", kc, layout, lvl, got, buf[:n])
						break
					}
				}
			}
		}
	})
}

func compareTexts(t *testing.T, more *libxkbMore, g *Keymap, c uintptr) {
	t.Helper()
	for _, format := range formats {
		for _, flags := range []SerializeFlags{0, SerializePretty, SerializeKeepUnused, SerializePretty | SerializeKeepUnused} {
			got, err := g.AsString(format, flags)
			if err != nil {
				t.Fatal(err)
			}
			if a, b := normalizeText(got), normalizeText(more.textOf(c, format, flags)); a != b {
				t.Errorf("text in format %d flags %d differs at %s", format, flags, firstDiff(a, b))
			}
		}
	}
}

func runShortScript(t *testing.T, l *libxkb, more *libxkbMore, g *Keymap, c uintptr, seed uint64, steps, deepEvery int, consumedLimit uint32) {
	t.Helper()
	p := newProbe(l, g, c)
	defer l.stateUnref(p.cs)
	rng := rand.New(rand.NewPCG(seed, 0x9e3779b97f4a7c15))
	var keys []uint32
	g.KeyForEach(func(k Keycode) {
		if kc := uint32(k); kc <= 700 && l.numLayoutsKey(c, uintptr(kc)) > 0 {
			keys = append(keys, kc)
			var sp *uint32
			n := l.symsByLevel(c, kc, 0, 0, &sp)
			if slices.ContainsFunc(symsSlice(sp, n), func(s uint32) bool { return KeysymIsModifier(Keysym(s)) }) {
				keys = append(keys, kc, kc, kc, kc, kc)
			}
		}
	})
	if len(keys) == 0 {
		return
	}
	held := map[uint32]bool{}
	for step := range steps {
		kc := keys[rng.IntN(len(keys))]
		pressed := !held[kc]
		dir, cdir := KeyReleased, int32(0)
		if pressed {
			dir, cdir = KeyPressed, 1
		}
		held[kc] = pressed
		if a, b := uint32(p.st.UpdateKey(Keycode(kc), dir)), uint32(l.updateKey(p.cs, kc, cdir)); a != b {
			t.Errorf("step %d key %d: changed: go %#x, c %#x", step, kc, a, b)
			return
		}
		if !p.compare(t, "step") {
			return
		}
		if step%deepEvery == 0 {
			if !p.compareKeys(t, "keys") {
				return
			}
			compareConsumed(t, l, more, p, "consumed", consumedLimit)
			compareQueries(t, l, more, p, "queries")
		}
		if step%33 == 0 {
			affLatch, latch, affLock, lock := rng.Uint32()&0xff, rng.Uint32()&0xff, rng.Uint32()&0xff, rng.Uint32()&0xff
			latchL, lockL := int32(rng.IntN(5)-2), int32(rng.IntN(5)-2)
			a := uint32(p.st.UpdateLatchedLocked(ModMask(affLatch), ModMask(latch), step%2 == 0, latchL, ModMask(affLock), ModMask(lock), step%3 == 0, lockL))
			b := uint32(more.updateLatched(p.cs, affLatch, latch, step%2 == 0, latchL, affLock, lock, step%3 == 0, lockL))
			if a != b {
				t.Errorf("step %d update latched locked: changed: go %#x, c %#x", step, a, b)
				return
			}
			if !p.compare(t, "update latched locked") {
				return
			}
		}
	}
}

type registryXML struct {
	Models []struct {
		Name string `xml:"configItem>name"`
	} `xml:"modelList>model"`
	Layouts []struct {
		Name     string `xml:"configItem>name"`
		Variants []struct {
			Name string `xml:"configItem>name"`
		} `xml:"variantList>variant"`
	} `xml:"layoutList>layout"`
	Groups []struct {
		Options []struct {
			Name string `xml:"configItem>name"`
		} `xml:"option"`
	} `xml:"optionList>group"`
}

func registryNames(t *testing.T) (models []string, layouts []Names, options []string) {
	t.Helper()
	root := os.Getenv("XKB_CONFIG_ROOT")
	for _, file := range []string{"evdev.xml", "evdev.extras.xml"} {
		src, err := os.ReadFile(filepath.Join(root, "rules", file))
		if err != nil {
			t.Fatal(err)
		}
		var reg registryXML
		if err := xml.Unmarshal(src, &reg); err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		for _, m := range reg.Models {
			models = append(models, m.Name)
		}
		for _, l := range reg.Layouts {
			layouts = append(layouts, Names{Layout: l.Name})
			for _, v := range l.Variants {
				layouts = append(layouts, Names{Layout: l.Name, Variant: v.Name})
			}
		}
		for _, g := range reg.Groups {
			for _, o := range g.Options {
				options = append(options, o.Name)
			}
		}
	}
	return models, layouts, options
}

func compareNames(t *testing.T, l *libxkb, more *libxkbMore, cctx uintptr, ctx *Context, n Names, format KeymapFormat, steps int) {
	t.Helper()
	n = fillNames(n)
	names := &ruleNamesC{cstr(n.Rules), cstr(n.Model), cstr(n.Layout), cstr(n.Variant), cstr(n.Options)}
	c := l.newFromNames2(cctx, names, int32(format), 0)
	g, err := ctx.NewKeymapFromNamesFormat(n, format)
	if (c == 0) != (err != nil) {
		t.Errorf("%+v format %d: libxkbcommon ok=%v, go: %v", n, format, c != 0, err)
		return
	}
	if c == 0 {
		return
	}
	defer l.keymapUnref(c)
	compareKeymaps(t, l, g, c)
	compareStatics(t, l, more, g, c)
	compareTexts(t, more, g, c)
	if steps > 0 {
		runShortScript(t, l, more, g, c, 5, steps, 39, 255)
	}
}

type matrixCase struct {
	names  Names
	format KeymapFormat
	steps  int
}

// TestEveryRegistryEntryMatchesLibxkbcommon holds every layout, variant,
// option and model evdev.xml and evdev.extras.xml offer to libxkbcommon, in
// both keymap formats.
func TestEveryRegistryEntryMatchesLibxkbcommon(t *testing.T) {
	t.Parallel()
	l := loadLibxkb(t)
	more := loadMore(t)
	models, layouts, options := registryNames(t)
	t.Logf("%d models, %d layouts and variants, %d options", len(models), len(layouts), len(options))
	var cases []matrixCase
	for _, format := range formats {
		for _, n := range layouts {
			cases = append(cases, matrixCase{n, format, 40})
		}
		for _, o := range options {
			cases = append(cases, matrixCase{Names{Options: o}, format, 40})
		}
		for _, m := range models {
			cases = append(cases, matrixCase{Names{Model: m}, format, 0})
		}
	}
	work := make(chan matrixCase)
	var wg sync.WaitGroup
	for range runtime.GOMAXPROCS(0) {
		wg.Go(func() {
			cctx := l.contextNew(0)
			setCLogLevel(cctx, 10)
			ctx := quietContext(t)
			for c := range work {
				compareNames(t, l, more, cctx, ctx, c.names, c.format, c.steps)
				if t.Failed() {
					t.Errorf("stopped at %+v format %d", c.names, c.format)
					return
				}
			}
		})
	}
	for i, c := range cases {
		if testbuild.Race && i%16 != 0 {
			continue
		}
		select {
		case work <- c:
		case <-time.After(time.Minute):
			t.Error("no worker took a case for a minute")
		}
		if t.Failed() {
			break
		}
	}
	close(work)
	wg.Wait()
}

func setCLogLevel(cctx uintptr, level int32) {
	cLogOnce.Do(cLogBind)
	cSetLevel(cctx, level)
}

var (
	cLogOnce     sync.Once
	cSetLevel    func(ctx uintptr, level int32)
	cSetVerb     func(ctx uintptr, verbosity int32)
	cSetLogFn    func(ctx, fn uintptr)
	cVsnprintf   func(buf *byte, size uintptr, format, args unsafe.Pointer) int32
	cLogMu       sync.Mutex
	cLogMessages []string
)

func cLogBind() {
	h, err := clib.Open(clib.SystemCandidates("libxkbcommon.so.0"))
	if err != nil {
		panic(err)
	}
	purego.RegisterLibFunc(&cSetLevel, h, "xkb_context_set_log_level")
	purego.RegisterLibFunc(&cSetVerb, h, "xkb_context_set_log_verbosity")
	purego.RegisterLibFunc(&cSetLogFn, h, "xkb_context_set_log_fn")
	c, err := clib.Open([]string{"libc.so.6"})
	if err != nil {
		panic(err)
	}
	purego.RegisterLibFunc(&cVsnprintf, c, "vsnprintf")
}

var cLogCallback = sync.OnceValue(func() uintptr {
	return purego.NewCallback(func(_ uintptr, level int32, format, args unsafe.Pointer) uintptr {
		var buf [2048]byte
		n := cVsnprintf(&buf[0], uintptr(len(buf)), format, args)
		if n > 0 {
			cLogMu.Lock()
			cLogMessages = append(cLogMessages, itoa(int(level))+" "+string(buf[:min(int(n), len(buf)-1)]))
			cLogMu.Unlock()
		}
		return 0
	})
})

var logCodeRE = regexp.MustCompile(`\[XKB-\d+\]`)

// parserLogged are the codes the parser logs. A file included twice is parsed
// once here and logs them once, so only their presence is compared, as with
// errors, where one failure here carries in its value what C logs line by line.
var parserLogged = []string{"[XKB-301]", "[XKB-302]", "[XKB-107]", "[XKB-082]", "[XKB-193]", "[XKB-607]", "[XKB-645]", "[XKB-433]", "[XKB-172]"}

func logSummary(messages []string) map[string]int {
	out := map[string]int{}
	for _, m := range messages {
		level, rest, _ := strings.Cut(m, " ")
		code := logCodeRE.FindString(rest)
		if level == "20" || slices.Contains(parserLogged, code) {
			out[level+" "+code] = 1
			continue
		}
		out[level+" "+code]++
	}
	return out
}

var logCorpus = []string{
	`xkb_keymap { xkb_keycodes { <A> = 8; <A> = 9; <B> = 9; alias <C> = <A>; alias <C> = <B>; alias <D> = <NOPE>; alias <A> = <B>; indicator 1 = "x"; indicator 1 = "y"; indicator 2 = "y"; }; xkb_types { type "ONE_LEVEL" { modifiers= none; }; type "ONE_LEVEL" { modifiers= none; }; type "T" { modifiers= Shift; map[Shift]= 2; map[Shift]= 2; map[Shift]= 3; map[Lock]= 2; preserve[Shift]= Lock; preserve[Shift]= Shift; level_name[1]= "a"; level_name[1]= "a"; level_name[1]= "b"; }; }; xkb_compatibility { interpret a { repeat= true; }; interpret a { repeat= false; }; indicator "x" { modifiers= Shift; }; indicator "x" { modifiers= Lock; }; }; xkb_symbols { name[1]= "q"; name[1]= "r"; key <A> { [ a ] }; key <A> { [ b ] }; key <NOPE> { [ a ] }; key <B> { type= "MISSING", [ a, b ] }; key <C> { [ a, b, c, d, e, f, g, h, i ] }; modifier_map Shift { <A> }; modifier_map Lock { <A> }; modifier_map Shift { a }; modifier_map Lock { a }; modifier_map Shift { <NOPE> }; modifier_map Shift { nosuchkeysym }; }; };`,
	`xkb_keymap { xkb_keycodes { <A> = 8; }; xkb_types { }; xkb_compatibility { }; xkb_symbols { key <A> { locks= true, radiogroup= 1, overlay1= <A>, [ Arabic_farsi_yeh, 0x8a2, nosuchsym ] }; }; xkb_geometry { description= "x"; }; };`,
	`xkb_keymap { xkb_keycodes { <A> = 8; }; xkb_types { }; xkb_compatibility { }; xkb_symbols { key <A> { [ a ], actions[1]= [ RedirectKey(key=<A>) ] }; name[2]= "x"; name= "y"; }; };`,
	`xkb_keymap { xkb_keycodes { <A> = 8; indicator 99 = "x"; }; xkb_types { type "X" { foo= 1; }; }; xkb_compatibility { interpret a { foo= 1; }; }; xkb_symbols { key <A> { foo= 1 }; }; };`,
	`xkb_keymap { xkb_keycodes { }; };`,
	`not a keymap`,
	`xkb_keymap { xkb_keycodes { <A> = 8; foo = 1; minimum.x = 1; }; xkb_types { };  xkb_compatibility { }; xkb_symbols { }; };`,
	`xkb_keymap { xkb_keycodes { <A> = 8; }; xkb_types { type "T" { modifiers= NoSuchMod; map[NoSuchMod]= 1; map[Shift]= 9999; map[Shift]= "x"; level_name[0]= "x"; foo= 1; bar.baz= 1; }; virtual_modifiers Shift; virtual_modifiers V=NoSuch; }; xkb_compatibility { }; xkb_symbols { }; };`,
	`xkb_keymap { xkb_keycodes { <A> = 8; }; xkb_types { }; xkb_compatibility { interpret a { action= Foo(); }; interpret a { action= SetMods(foo=1); }; interpret a { action= SetMods(modifiers=NoSuch); }; interpret a { action= SetGroup(group=99); }; interpret a+Foo(Shift) { }; interpret.foo= 1; indicator "x" { foo= 1; groups= Group99; modifiers= NoSuch; }; foo.bar= 1; }; xkb_symbols { }; };`,
	`xkb_keymap { xkb_keycodes { <A> = 8; }; xkb_types { }; xkb_compatibility { }; xkb_symbols { key <A> { type[99]= "x", symbols[99]= [ a ], actions[99]= [ NoAction() ], groupsRedirect= 99, foo= 1, repeat= maybe }; key <A> { actions[1]= [ Foo() ], [ a ] }; key.foo= 1; modifier_map NoSuch { <A> }; name[99]= "x"; name[1]= 3; foo.bar= 1; }; };`,
	`xkb_keymap { xkb_keycodes { include "nosuchfile" }; xkb_types { include "nosuchfile(x)" }; xkb_compatibility { include "complete(nosuchmap)" }; xkb_symbols { include "pc:99" }; };`,
	`xkb_keymap { xkb_keycodes { <A> = 8; }; xkb_keycodes { <B> = 9; }; xkb_types { }; xkb_compatibility { }; xkb_symbols { }; };`,
	`xkb_keymap { xkb_compatibility { }; };`,
	`xkb_keycodes { <A> = 8; };`,
}

func compareLogs(t *testing.T, l *libxkb, label string, compile func(cctx uintptr), compileGo func(ctx *Context)) {
	t.Helper()
	cLogOnce.Do(cLogBind)
	cctx := l.contextNew(0)
	for _, verbosity := range []int32{0, 1, 2, 5, 10} {
		cLogMu.Lock()
		cLogMessages = nil
		cLogMu.Unlock()
		cSetLevel(cctx, 30)
		cSetVerb(cctx, verbosity)
		cSetLogFn(cctx, cLogCallback())
		compile(cctx)
		cLogMu.Lock()
		want := logSummary(cLogMessages)
		cLogMu.Unlock()
		ctx := testData(t)
		ctx.SetLogLevel(LogWarning)
		ctx.SetLogVerbosity(int(verbosity))
		var got []string
		ctx.SetLogFunc(func(level LogLevel, message string) { got = append(got, itoa(int(level))+" "+message) })
		compileGo(ctx)
		have := logSummary(got)
		for key := range want {
			if strings.HasSuffix(key, " ") {
				delete(want, key)
			}
		}
		for key := range have {
			if strings.HasSuffix(key, " ") {
				delete(have, key)
			}
		}
		if !maps.Equal(have, want) {
			t.Errorf("%s verbosity %d: log codes: go %v, c %v\ngo: %q", label, verbosity, have, want, got)
		}
	}
}

// TestLogsMatchLibxkbcommon holds the warnings and errors a compile logs, by
// level and code, to libxkbcommon's for a corpus that trips them and for real
// keymaps, at every verbosity that changes what is logged.
func TestLogsMatchLibxkbcommon(t *testing.T) {
	l := loadLibxkb(t)
	for i, text := range logCorpus {
		for _, format := range formats {
			compareLogs(t, l, "corpus "+itoa(i)+" format "+itoa(int(format)), func(cctx uintptr) {
				if km := l.newFromString(cctx, cstr(text), int32(format), 0); km != 0 {
					l.keymapUnref(km)
				}
			}, func(ctx *Context) { _, _ = ctx.NewKeymapFromBuffer([]byte(text), format) })
		}
	}
	for _, n := range namesMatrix {
		n := fillNames(n)
		for _, format := range formats {
			compareLogs(t, l, namesLabel(n)+" format "+itoa(int(format)), func(cctx uintptr) {
				names := &ruleNamesC{cstr(n.Rules), cstr(n.Model), cstr(n.Layout), cstr(n.Variant), cstr(n.Options)}
				if km := l.newFromNames2(cctx, names, int32(format), 0); km != 0 {
					l.keymapUnref(km)
				}
			}, func(ctx *Context) { _, _ = ctx.NewKeymapFromNamesFormat(n, format) })
		}
	}
}

func libxkbHandle(t testing.TB) uintptr {
	t.Helper()
	h, err := clib.Open(clib.SystemCandidates("libxkbcommon.so.0"))
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func bindC(t testing.TB, h uintptr, fn any, name string) {
	t.Helper()
	if _, err := purego.Dlsym(h, name); err != nil {
		t.Fatal(err)
	}
	purego.RegisterLibFunc(fn, h, name)
}

// TestStateQueriesAcrossTheMatrix drives the full state script, with every
// consumed-modifier and active query on every key, over the keymap matrix in
// both formats. The registry-wide test does this more thinly per keymap.
func TestStateQueriesAcrossTheMatrix(t *testing.T) {
	t.Parallel()
	l := loadLibxkb(t)
	more := loadMore(t)
	ctx := quietContext(t)
	cctx := l.contextNew(0)
	setCLogLevel(cctx, 10)
	for _, n := range namesMatrix {
		n := fillNames(n)
		for _, format := range formats {
			names := &ruleNamesC{cstr(n.Rules), cstr(n.Model), cstr(n.Layout), cstr(n.Variant), cstr(n.Options)}
			c := l.newFromNames2(cctx, names, int32(format), 0)
			g, err := ctx.NewKeymapFromNamesFormat(n, format)
			if c == 0 || err != nil {
				t.Fatalf("%+v format %d: c %v, go %v", n, format, c != 0, err)
			}
			runShortScript(t, l, more, g, c, 9, 120, 40, keycodeMax)
			runScript(t, l, g, c, 3)
			l.keymapUnref(c)
		}
	}
}
