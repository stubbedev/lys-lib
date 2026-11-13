package xkb

import (
	"cmp"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestRMLVOBuilderMatchesLibxkbcommon(t *testing.T) {
	l := loadLibxkb(t)
	more := loadMore(t)
	setCLogLevel(l.ctx, 10)
	ctx := quietContext(t)
	type layout struct {
		name, variant string
		options       []string
	}
	for _, c := range []struct {
		layouts []layout
		options []string
	}{
		{},
		{layouts: []layout{{name: "us"}}},
		{layouts: []layout{{name: "us"}, {name: "de", variant: "neo"}}, options: []string{"grp:alt_shift_toggle", "grp:alt_shift_toggle"}},
		{layouts: []layout{{name: "us", options: []string{"compat:lock"}}, {name: "ru"}, {name: "gr"}, {name: "il"}, {name: "fr"}, {name: "dk"}}, options: []string{"ctrl:nocaps"}},
		{options: []string{"caps:escape"}},
		{layouts: []layout{{name: "nosuchlayout"}}},
	} {
		b := ctx.NewRMLVOBuilder("", "")
		cb := more.builderNew(l.ctx, nil, nil, 0)
		for _, x := range c.layouts {
			if err := b.AppendLayout(x.name, x.variant, x.options...); err != nil {
				t.Fatal(err)
			}
			var opts []*byte
			for _, o := range x.options {
				opts = append(opts, cstr(o))
			}
			var p **byte
			if len(opts) > 0 {
				p = &opts[0]
			}
			var v *byte
			if x.variant != "" {
				v = cstr(x.variant)
			}
			if !more.builderLayout(cb, cstr(x.name), v, p, uintptr(len(opts))) {
				t.Fatal("xkb_rmlvo_builder_append_layout failed")
			}
		}
		for _, o := range c.options {
			if err := b.AppendOption(o); err != nil {
				t.Fatal(err)
			}
			if !more.builderOption(cb, cstr(o)) {
				t.Fatal("xkb_rmlvo_builder_append_option failed")
			}
		}
		for _, format := range formats {
			g, err := b.NewKeymap(format)
			km := more.fromRMLVO(cb, int32(format), 0)
			if (km == 0) != (err != nil) {
				t.Fatalf("%+v format %d: c ok=%v, go %v", c, format, km != 0, err)
			}
			if km == 0 {
				continue
			}
			compareKeymaps(t, l, g, km)
			compareTexts(t, more, g, km)
			l.keymapUnref(km)
		}
	}
}

func TestBuilderLimitsLayouts(t *testing.T) {
	b := quietContext(t).NewRMLVOBuilder("", "")
	for range maxGroups {
		if err := b.AppendLayout("us", ""); err != nil {
			t.Fatal(err)
		}
	}
	if err := b.AppendLayout("us", ""); err == nil {
		t.Error("the 33rd layout was accepted")
	}
	if err := b.AppendOption(""); err == nil {
		t.Error("an empty option was accepted")
	}
}

func TestComponentsFromNamesMatchLibxkbcommon(t *testing.T) {
	l := loadLibxkb(t)
	more := loadMore(t)
	setCLogLevel(l.ctx, 10)
	ctx := quietContext(t)
	for _, n := range append([]Names{{}, {Layout: "nosuch"}, {Rules: "nosuchrules"}}, namesMatrix...) {
		in := &ruleNamesC{cstr(n.Rules), cstr(n.Model), cstr(n.Layout), cstr(n.Variant), cstr(n.Options)}
		if n.Options == "" {
			in.options = nil
		}
		var out ruleNamesC
		var comps componentsC
		ok := more.componentsFrom(l.ctx, in, &out, &comps)
		names, got, err := ctx.ComponentsFromNames(n)
		if ok != (err == nil) {
			t.Errorf("%+v: c %v, go %v", n, ok, err)
			continue
		}
		if !ok {
			continue
		}
		want := ComponentNames{goStr(comps.keycodes), goStr(comps.types), goStr(comps.compat), goStr(comps.symbols), goStr(comps.geometry)}
		if got != want {
			t.Errorf("%+v: components: go %+v, c %+v", n, got, want)
		}
		if names.Rules != goStr(out.rules) || names.Model != goStr(out.model) || names.Layout != goStr(out.layout) || names.Variant != goStr(out.variant) || names.Options != goStr(out.options) {
			t.Errorf("%+v: sanitized: go %+v", n, names)
		}
	}
}

func TestFromFileAndFormatErrors(t *testing.T) {
	ctx := quietContext(t)
	if _, err := ctx.NewKeymapFromFile(strings.NewReader(actionKeymap), FormatTextV1); err != nil {
		t.Error(err)
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := errors.Join(w.Close(), r.Close()); err != nil {
		t.Fatal(err)
	}
	if _, err := ctx.NewKeymapFromFile(r, FormatTextV1); err == nil {
		t.Error("reading a closed file succeeded")
	}
	if _, err := ctx.NewKeymapFromBuffer([]byte(actionKeymap), FormatOriginal); err == nil {
		t.Error("compiling in FormatOriginal succeeded")
	}
	if _, err := ctx.NewKeymapFromNamesFormat(Names{}, KeymapFormat(9)); err == nil {
		t.Error("compiling in an unknown format succeeded")
	}
	km, err := ctx.NewKeymapFromBuffer([]byte(actionKeymap), FormatTextV2)
	if err != nil {
		t.Fatal(err)
	}
	if km.Format() != FormatTextV2 || km.Context() != ctx {
		t.Errorf("format %d context %p", km.Format(), km.Context())
	}
	if _, err := km.AsString(KeymapFormat(9), 0); err == nil {
		t.Error("serializing in an unknown format succeeded")
	}
	if _, err := km.AsString(FormatTextV1, SerializeFlags(0x80)); err == nil {
		t.Error("unknown serialize flags were accepted")
	}
	original, err := km.AsString(FormatOriginal, 0)
	if err != nil {
		t.Fatal(err)
	}
	v2, _ := km.AsString(FormatTextV2, 0)
	if original != v2 {
		t.Error("FormatOriginal is not the compile format")
	}
}

func TestIncludePathAPI(t *testing.T) {
	dir := t.TempDir()
	xdg := filepath.Join(dir, "config")
	root := os.Getenv("XKB_CONFIG_ROOT")
	for _, sub := range []string{"config/xkb", "home/.xkb", "extra", "ver/one", "ver/two", "unver/two", "unver/three"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	env := map[string]string{
		"XDG_CONFIG_HOME":                        xdg,
		"HOME":                                   filepath.Join(dir, "home"),
		"XKB_CONFIG_EXTRA_PATH":                  filepath.Join(dir, "extra"),
		"XKB_CONFIG_ROOT":                        root,
		"XKB_CONFIG_VERSIONED_EXTENSIONS_PATH":   filepath.Join(dir, "ver"),
		"XKB_CONFIG_UNVERSIONED_EXTENSIONS_PATH": filepath.Join(dir, "unver"),
	}
	c := NewContext(func(n string) string { return env[n] })
	c.SetLogLevel(LogCritical)
	want := []string{
		filepath.Join(xdg, "xkb"), filepath.Join(dir, "home/.xkb"), filepath.Join(dir, "extra"),
		filepath.Join(dir, "ver/one"), filepath.Join(dir, "ver/two"), filepath.Join(dir, "unver/three"), root,
	}
	if got := c.IncludePaths(); !slices.Equal(got, want) {
		t.Errorf("default paths: %v, want %v", got, want)
	}
	if n := c.NumIncludePaths(); n != uint32(len(want)) {
		t.Errorf("num = %d", n)
	}
	if p, ok := c.IncludePath(0); !ok || p != want[0] {
		t.Errorf("path 0 = %q %v", p, ok)
	}
	if _, ok := c.IncludePath(99); ok {
		t.Error("path 99 exists")
	}
	if c.AppendIncludePath(filepath.Join(dir, "missing")) {
		t.Error("appended a missing directory")
	}
	if !c.AppendIncludePath(dir) {
		t.Error("could not append a directory")
	}
	c.ClearIncludePaths()
	if n := c.NumIncludePaths(); n != 0 {
		t.Errorf("after clear: %d", n)
	}
	if !c.ResetDefaultIncludePaths() {
		t.Error("reset failed")
	}
	if got := c.IncludePaths(); !slices.Equal(got, want) {
		t.Errorf("after reset: %v", got)
	}
	none := NewContextFlags(func(n string) string { return env[n] }, ContextNoDefaultIncludes)
	if n := none.NumIncludePaths(); n != 0 {
		t.Errorf("no default includes: %d", n)
	}
}

func TestLogConfiguration(t *testing.T) {
	env := map[string]string{"XKB_LOG_LEVEL": "warning", "XKB_LOG_VERBOSITY": "7"}
	c := NewContext(func(n string) string { return env[n] })
	if c.LogLevel() != LogWarning || c.LogVerbosity() != 7 {
		t.Errorf("from the environment: %d %d", c.LogLevel(), c.LogVerbosity())
	}
	for in, want := range map[string]LogLevel{"crit": LogCritical, "ERROR": LogError, "warn": LogWarning, "info": LogInfo, "debug": LogDebug, "dbg": LogDebug, "35": 35, "junk": LogError} {
		if got := ParseLogLevel(in); got != want {
			t.Errorf("ParseLogLevel(%q) = %d, want %d", in, got, want)
		}
	}
	var got []string
	c.SetLogFunc(func(_ LogLevel, m string) { got = append(got, m) })
	c.SetLogLevel(LogError)
	c.warn(0, "dropped\n")
	c.log(LogError, 0, 42, "kept %d\n", 1)
	c.SetLogVerbosity(1)
	c.SetLogLevel(LogWarning)
	c.verbose(5, 0, "too verbose\n")
	if !slices.Equal(got, []string{"[XKB-042] kept 1\n"}) {
		t.Errorf("logged %q", got)
	}
	c.SetUserData("x")
	if c.UserData() != "x" {
		t.Error("user data")
	}
	var errored []string
	c.SetLogFunc(func(_ LogLevel, m string) { errored = append(errored, m) })
	c.SetLogLevel(LogError)
	if _, err := c.NewKeymapFromString([]byte(`xkb_keymap { xkb_symbols { foo= 1; bar= 2; }; };`)); err == nil {
		t.Fatal("a bad keymap compiled")
	} else if !strings.Contains(err.Error(), "foo") || !strings.Contains(err.Error(), "bar") {
		t.Errorf("not every error was collected: %v", err)
	}
	if len(errored) < 3 {
		t.Errorf("logged %q", errored)
	}
}

func TestEnvironmentNames(t *testing.T) {
	root := os.Getenv("XKB_CONFIG_ROOT")
	env := map[string]string{"XKB_CONFIG_ROOT": root, "XKB_DEFAULT_LAYOUT": "dk", "XKB_DEFAULT_VARIANT": "nodeadkeys", "XKB_DEFAULT_OPTIONS": "ctrl:nocaps", "XKB_DEFAULT_MODEL": "pc104", "XKB_DEFAULT_RULES": "evdev"}
	c := NewContext(func(n string) string { return env[n] })
	names, _, err := c.ComponentsFromNames(Names{})
	if err != nil {
		t.Fatal(err)
	}
	if want := (Names{Rules: "evdev", Model: "pc104", Layout: "dk", Variant: "nodeadkeys", Options: "ctrl:nocaps"}); names != want {
		t.Errorf("sanitized %+v, want %+v", names, want)
	}
	if names, _, _ = c.ComponentsFromNames(Names{OptionsSet: true}); names.Options != "" {
		t.Errorf("OptionsSet did not keep empty options: %q", names.Options)
	}
	off := NewContextFlags(func(n string) string { return env[n] }, ContextNoEnvironmentNames)
	if names, _, _ = off.ComponentsFromNames(Names{}); names.Layout != "us" || names.Model != "pc105" {
		t.Errorf("no environment names: %+v", names)
	}
}

func TestComposeEntriesMatchLibxkbcommonIterator(t *testing.T) {
	l := loadLibxkb(t)
	h := libxkbHandle(t)
	var (
		iterNew  func(table uintptr) uintptr
		iterNext func(it uintptr) uintptr
		entrySeq func(e uintptr, n *uintptr) *uint32
		entrySym func(e uintptr) uint32
		entryUTF func(e uintptr) *byte
	)
	bindC(t, h, &iterNew, "xkb_compose_table_iterator_new")
	bindC(t, h, &iterNext, "xkb_compose_table_iterator_next")
	bindC(t, h, &entrySeq, "xkb_compose_table_entry_sequence")
	bindC(t, h, &entrySym, "xkb_compose_table_entry_keysym")
	bindC(t, h, &entryUTF, "xkb_compose_table_entry_utf8")
	ctx := testData(t)
	const text = `<a> <b> : "x" X
<a> <c> : "y"
<d> : Y
<a> <b> : "z" Z
<e> <f> <g> : "ü" udiaeresis
`
	g, err := ctx.NewComposeTableFromBuffer([]byte(text), "C")
	if err != nil {
		t.Fatal(err)
	}
	src := append([]byte(text), 0)
	table := l.composeBuffer(l.ctx, &src[0], uintptr(len(text)), cstr("C"), 1, 0)
	if table == 0 {
		t.Fatal("xkb_compose_table_new_from_buffer failed")
	}
	type entry struct {
		seq  string
		sym  uint32
		utf8 string
	}
	key := func(seq []uint32) string {
		var b strings.Builder
		for _, s := range seq {
			b.WriteString(KeysymGetName(Keysym(s)) + " ")
		}
		return b.String()
	}
	var want, got []entry
	it := iterNew(table)
	for e := iterNext(it); e != 0; e = iterNext(it) {
		var n uintptr
		p := entrySeq(e, &n)
		want = append(want, entry{key(symsSlice(p, int32(n))), entrySym(e), goStr(entryUTF(e))})
	}
	for e := range g.Entries() {
		got = append(got, entry{key(toU32(e.Sequence)), uint32(e.Sym), e.UTF8})
	}
	byKey := func(a, b entry) int { return cmp.Compare(a.seq, b.seq) }
	slices.SortFunc(want, byKey)
	slices.SortFunc(got, byKey)
	if !slices.Equal(got, want) {
		t.Errorf("entries: go %+v, c %+v", got, want)
	}
	if st := g.NewState(); st.Table() != g {
		t.Error("the state's table")
	}
	stopped := 0
	for range g.Entries() {
		stopped++
		break
	}
	if stopped != 1 {
		t.Errorf("Entries did not stop: %d", stopped)
	}
	if _, err := ctx.NewComposeTableFromReader(strings.NewReader(text), "C"); err != nil {
		t.Error(err)
	}
}

func TestLocaleInstalledCheck(t *testing.T) {
	dir := t.TempDir()
	loc := filepath.Join(dir, "xx_YY.utf8")
	if err := os.MkdirAll(loc, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(loc, "LC_CTYPE"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	getenv := func(name string) string {
		if name == "LOCPATH" {
			return dir
		}
		return ""
	}
	for locale, want := range map[string]bool{"C": true, "POSIX": true, "xx_YY.UTF-8": true, "xx_YY.utf8": true, "zz_ZZ.UTF-8": false} {
		if got, err := localeInstalled(installedLocales, getenv, locale); err != nil || got != want {
			t.Errorf("localeInstalled(%q) = %v, %v; want %v", locale, got, err, want)
		}
	}
	if got, err := localeInstalled(everyLocale, getenv, "zz_ZZ.UTF-8"); err != nil || !got {
		t.Errorf("under musl an unknown locale is installed = %v, %v; want true, as musl's newlocale accepts every name", got, err)
	}
}

func TestTheLocaleRuleIsTheRunningProcesssLibc(t *testing.T) {
	for interp, want := range map[string]localeRule{
		"/lib/ld-musl-x86_64.so.1":                    everyLocale,
		"/lib/ld-musl-aarch64.so.1":                   everyLocale,
		"/lib64/ld-linux-x86-64.so.2":                 installedLocales,
		"/nix/store/x-glibc/lib/ld-linux-x86-64.so.2": installedLocales,
		"": installedLocales,
	} {
		if got := localeRuleOf(interp); got != want {
			t.Errorf("localeRuleOf(%q) = %v, want %v", interp, got, want)
		}
	}
	interp, err := programInterpreter(selfExe)
	if err != nil {
		t.Fatal(err)
	}
	if rule, err := processLocaleRule(); err != nil || rule != localeRuleOf(interp) {
		t.Fatalf("the process's rule is %v, %v; want %v from its interpreter %q, whatever loaders the host has on disk", rule, err, localeRuleOf(interp), interp)
	}
	if _, err := programInterpreter(filepath.Join(t.TempDir(), "missing")); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Errorf("a program that is not there: %v", err)
	}
}

func TestUnknownComposeLocaleFails(t *testing.T) {
	xlocale := os.Getenv("XLOCALEDIR")
	if xlocale == "" {
		t.Fatal("XLOCALEDIR is not set: the dev shell points it at libX11's locale data")
	}
	dir := t.TempDir()
	getenv := func(name string) string {
		switch name {
		case "XLOCALEDIR":
			return xlocale
		case "LOCPATH":
			return dir
		case "XKB_CONFIG_ROOT":
			return os.Getenv(name)
		}
		return ""
	}
	c := NewContext(getenv)
	c.SetLogLevel(LogCritical)
	if _, err := c.NewComposeTableFromLocale("zz_ZZ.UTF-8"); err == nil {
		t.Error("an unknown, uninstalled locale got a Compose table")
	}
	if _, err := c.NewComposeTableFromLocale("en_US.UTF-8"); err != nil {
		t.Errorf("en_US.UTF-8: %v", err)
	}
}

func TestKeycodeLegality(t *testing.T) {
	if !KeycodeIsLegalExt(keycodeMax) || KeycodeIsLegalExt(keycodeMax+1) {
		t.Error("KeycodeIsLegalExt")
	}
	if !KeycodeIsLegalX11(8) || !KeycodeIsLegalX11(255) || KeycodeIsLegalX11(7) || KeycodeIsLegalX11(256) {
		t.Error("KeycodeIsLegalX11")
	}
}

func TestMatchModes(t *testing.T) {
	ctx := quietContext(t)
	km, err := ctx.NewKeymapFromNames(Names{Layout: "us"})
	if err != nil {
		t.Fatal(err)
	}
	st := km.NewState()
	st.UpdateMask(km.ModMaskOf("Shift")|km.ModMaskOf("Control"), 0, 0, 0, 0, 0)
	for _, c := range []struct {
		match  StateMatch
		names  []string
		active bool
		ok     bool
	}{
		{MatchAll, []string{"Shift", "Control"}, true, true},
		{MatchAll, []string{"Shift"}, false, true},
		{MatchAll | MatchNonExclusive, []string{"Shift"}, true, true},
		{MatchAny | MatchNonExclusive, []string{"Shift", "Mod1"}, true, true},
		{MatchAny, []string{"Shift", "Mod1"}, false, true},
		{MatchAll, []string{"NoSuchMod"}, false, false},
	} {
		active, ok := st.ModNamesAreActive(StateModEffective, c.match, c.names...)
		if active != c.active || ok != c.ok {
			t.Errorf("%#x %v: %v %v", c.match, c.names, active, ok)
		}
		var idx []uint32
		for _, n := range c.names {
			i, known := km.ModIndex(n)
			if !known {
				i = 99
			}
			idx = append(idx, i)
		}
		active, ok = st.ModIndicesAreActive(StateModEffective, c.match, idx...)
		if active != c.active || ok != c.ok {
			t.Errorf("indices %#x %v: %v %v", c.match, idx, active, ok)
		}
	}
	if st.Keymap() != km {
		t.Error("the state's keymap")
	}
	if active, ok := st.LayoutNameIsActive("English (US)", StateLayoutEffective); !active || !ok {
		t.Errorf("layout by name: %v %v", active, ok)
	}
	if _, ok := st.LayoutNameIsActive("nope", StateLayoutEffective); ok {
		t.Error("an unknown layout is known")
	}
}

const v2Keymap = `xkb_keymap {
xkb_keycodes { <A> = 8; <B> = 9; <C> = 10; <D> = 11; <E> = 12; <F> = 13; <G> = 14; <H> = 15; indicator 1 = "L"; };
xkb_types {
	virtual_modifiers Unbound, Bound=Mod3, None=none;
	type "ONE_LEVEL" { modifiers= none; };
	type "TWO_LEVEL" { modifiers= Shift; map[Shift]= 2; };
	type "U" { modifiers= Unbound; map[Unbound]= 2; level_name[1]= "a"; };
};
xkb_compatibility {
	interpret a { action= { SetMods(modifiers=Shift), SetGroup(group=+1) }; };
	indicator "L" { groups= Group7; whichGroupState= locked; };
};
xkb_symbols {
	name[1]= "one"; name[5]= "five"; name[32]= "thirty-two";
	key <A> { [ a, A ] };
	key <B> { [ SetMods(modifiers=Lock, clearLocks, unlockOnPress) ] };
	key <C> { [ LatchMods(modifiers=Shift, latchOnPress, clearLocks) ] };
	key <D> { [ LockMods(modifiers=Control, unlockOnPress) ] };
	key <E> { [ LockGroup(group=+1, lockOnRelease) ] };
	key <F> { [ { SetMods(modifiers=Mod1), SetGroup(group=+2) } ] };
	key <G> { symbols[5]= [ x ], actions[5]= [ LatchGroup(group=32) ], symbols[32]= [ y ] };
	key <H> { type= "U", [ VoidAction(), LatchMods(modifiers=Unbound, latchToLock) ] };
};
};`

func TestV2FeaturesMatchLibxkbcommon(t *testing.T) {
	l := loadLibxkb(t)
	more := loadMore(t)
	setCLogLevel(l.ctx, 10)
	ctx := quietContext(t)
	c := l.newFromString(l.ctx, cstr(v2Keymap), int32(FormatTextV2), 0)
	if c == 0 {
		t.Fatal("libxkbcommon refuses the V2 keymap")
	}
	defer l.keymapUnref(c)
	g, err := ctx.NewKeymapFromBuffer([]byte(v2Keymap), FormatTextV2)
	if err != nil {
		t.Fatal(err)
	}
	compareKeymaps(t, l, g, c)
	compareStatics(t, l, more, g, c)
	compareTexts(t, more, g, c)
	for seed := range uint64(8) {
		runScript(t, l, g, c, seed)
		runShortScript(t, l, more, g, c, seed, 300, 20, keycodeMax)
	}
	if bad := l.newFromString(l.ctx, cstr(v2Keymap), int32(FormatTextV1), 0); bad != 0 {
		l.keymapUnref(bad)
		t.Error("libxkbcommon took V2 fields in V1")
	}
	if _, err := ctx.NewKeymapFromBuffer([]byte(v2Keymap), FormatTextV1); err == nil {
		t.Error("V2 fields compiled in V1")
	}
	text, err := g.AsString(FormatTextV2, 0)
	if err != nil {
		t.Fatal(err)
	}
	back := l.newFromString(l.ctx, cstr(text), int32(FormatTextV2), 0)
	if back == 0 {
		t.Fatalf("libxkbcommon refuses the serialized V2 keymap:\n%s", text)
	}
	defer l.keymapUnref(back)
	compareKeymaps(t, l, g, back)
	again, err := ctx.NewKeymapFromBuffer([]byte(text), FormatTextV2)
	if err != nil {
		t.Fatal(err)
	}
	compareKeymaps(t, l, again, back)
	compareTexts(t, more, again, back)
}
