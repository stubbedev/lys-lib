package xkb

import (
	"errors"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

var namesMatrix = []Names{
	{Layout: "us"},
	{Layout: "dk"},
	{Layout: "de", Variant: "nodeadkeys"},
	{Layout: "fr"},
	{Layout: "us", Variant: "dvorak"},
	{Layout: "us,ru", Options: "grp:alt_shift_toggle"},
	{Layout: "us", Options: "ctrl:nocaps"},
	{Layout: "us", Options: "caps:escape"},
	{Layout: "us", Options: "compose:ralt"},
	{Layout: "gb"},
	{Layout: "ru"},
	{Layout: "us", Variant: "intl"},
	{Layout: "fi"},
	{Layout: "ca", Variant: "multix"},
	{Layout: "il"},
	{Layout: "ara"},
	{Layout: "jp"},
	{Layout: "us,de,fr", Options: "grp:caps_toggle"},
	{Layout: "us,de,ru,gr", Variant: ",nodeadkeys,,", Options: "grp:alt_shift_toggle,grp_led:scroll"},
	{Layout: "us", Options: "lv3:ralt_switch,compose:menu"},
	{Layout: "de", Options: "lv5:caps_switch"},
	{Layout: "us", Options: "shift:both_capslock,ctrl:swapcaps"},
	{Model: "pc104", Layout: "us"},
	{Model: "macbook79", Layout: "us"},
	{Layout: "de", Variant: "neo"},
}

func namesLabel(n Names) string {
	return n.Model + "/" + n.Layout + "/" + n.Variant + "/" + n.Options
}

func fillNames(n Names) Names { return n.withDefaults() }

func TestCompileFromNamesMatchesLibxkbcommon(t *testing.T) {
	l := loadLibxkb(t)
	ctx := testData(t)
	for _, n := range namesMatrix {
		t.Run(namesLabel(n), func(t *testing.T) {
			n := fillNames(n)
			g, err := ctx.NewKeymapFromNamesFormat(n, FormatTextV1)
			if err != nil {
				t.Fatal(err)
			}
			c := l.compile(t, n)
			defer l.keymapUnref(c)
			compareKeymaps(t, l, g, c)
			goText, cText := normalizeText(g.Text()), normalizeText(l.text(t, c))
			if goText != cText {
				t.Errorf("serialized text differs at %s", firstDiff(goText, cText))
			}
			runScript(t, l, g, c, 7)
		})
	}
}

func TestSerializedTextRoundTripsThroughLibxkbcommon(t *testing.T) {
	l := loadLibxkb(t)
	ctx := testData(t)
	for _, n := range namesMatrix {
		t.Run(namesLabel(n), func(t *testing.T) {
			g, err := ctx.NewKeymapFromNamesFormat(fillNames(n), FormatTextV1)
			if err != nil {
				t.Fatal(err)
			}
			c := l.parse(g.Text())
			if c == 0 {
				t.Fatal("libxkbcommon refuses the serialized keymap")
			}
			defer l.keymapUnref(c)
			compareKeymaps(t, l, g, c)
			runScript(t, l, g, c, 11)
			back, err := ctx.NewKeymapFromString([]byte(l.text(t, c)))
			if err != nil {
				t.Fatalf("parsing libxkbcommon's own text: %v", err)
			}
			compareKeymaps(t, l, back, c)
			runScript(t, l, back, c, 13)
		})
	}
}

func runScript(t *testing.T, l *libxkb, g *Keymap, c uintptr, seed uint64) {
	t.Helper()
	p := newProbe(l, g, c)
	defer l.stateUnref(p.cs)
	if !p.compare(t, "initial") || !p.compareKeys(t, "initial") {
		return
	}
	rng := rand.New(rand.NewPCG(seed, 0x9e3779b97f4a7c15))
	lo, hi := l.minKeycode(c), min(l.maxKeycode(c), 700)
	held := map[uint32]bool{}
	var keys []uint32
	for kc := lo; kc <= hi; kc++ {
		var sp *uint32
		if l.numLayoutsKey(c, uintptr(kc)) > 0 {
			n := l.symsByLevel(c, kc, 0, 0, &sp)
			for _, s := range symsSlice(sp, n) {
				if KeysymIsModifier(Keysym(s)) {
					keys = append(keys, kc, kc, kc, kc)
					break
				}
			}
			keys = append(keys, kc)
		}
	}
	if len(keys) == 0 {
		return
	}
	for step := range 600 {
		kc := keys[rng.IntN(len(keys))]
		pressed := !held[kc]
		if rng.IntN(20) == 0 {
			pressed = !pressed
		}
		dir, cdir := KeyReleased, int32(0)
		if pressed {
			dir, cdir = KeyPressed, 1
		}
		held[kc] = pressed
		a, b := uint32(p.st.UpdateKey(Keycode(kc), dir)), uint32(l.updateKey(p.cs, kc, cdir))
		where := "step " + strconv.Itoa(step) + " key " + strconv.Itoa(int(kc)) + " <" + g.KeyGetName(Keycode(kc)) + ">"
		if a != b {
			t.Errorf("%s: changed: go %#x, c %#x", where, a, b)
			return
		}
		if !p.compare(t, where) {
			return
		}
		if step%25 == 0 && !p.compareKeys(t, where) {
			return
		}
	}
	for step := range 200 {
		dep, lat, lock := rng.Uint32()&0xff, rng.Uint32()&0xff, rng.Uint32()&0xff
		depL, latL, lockL := rng.Uint32()%3, rng.Uint32()%3, rng.Uint32()%6
		a := uint32(p.st.UpdateMask(ModMask(dep), ModMask(lat), ModMask(lock), depL, latL, lockL))
		b := uint32(l.updateMask(p.cs, dep, lat, lock, depL, latL, lockL))
		where := "mask step " + strconv.Itoa(step)
		if a != b {
			t.Errorf("%s: changed: go %#x, c %#x", where, a, b)
			return
		}
		if !p.compare(t, where) || (step%20 == 0 && !p.compareKeys(t, where)) {
			return
		}
	}
}

const actionKeymap = `xkb_keymap {
xkb_keycodes "test" {
	minimum = 8;
	maximum = 255;
	<LFSH> = 50;
	<RTSH> = 62;
	<LCTL> = 37;
	<LALT> = 64;
	<CAPS> = 66;
	<A1> = 10;
	<A2> = 11;
	<A3> = 12;
	<A4> = 13;
	<A5> = 14;
	<A6> = 15;
	<A7> = 16;
	<A8> = 17;
	<A9> = 18;
	<B1> = 19;
	<B2> = 20;
	<B3> = 21;
	<C1> = 24;
	<C2> = 25;
	indicator 1 = "Caps Lock";
	indicator 2 = "Group 2";
	indicator 3 = "Latched Shift";
	indicator 4 = "Base Group";
	indicator 5 = "Any Group";
	alias <SHFT> = <LFSH>;
};
xkb_types "test" {
	virtual_modifiers LevelThree,NumLock=Mod2;
	type "ONE_LEVEL" { modifiers= none; };
	type "TWO_LEVEL" { modifiers= Shift; map[Shift]= 2; level_name[1]= "Base"; level_name[2]= "Shift"; };
	type "ALPHABETIC" { modifiers= Shift+Lock; map[Shift]= 2; map[Lock]= 2; preserve[Lock]= Lock; };
	type "KEYPAD" { modifiers= Shift+NumLock; map[Shift]= 2; map[NumLock]= 2; };
	type "CTRL_FOUR" { modifiers= Shift+Control; map[Shift]= 2; map[Control]= 3; map[Shift+Control]= 4; preserve[Control]= Control; };
	type "THREE" { modifiers= Shift+LevelThree; map[Shift]= 2; map[LevelThree]= 3; };
};
xkb_compatibility "test" {
	virtual_modifiers LevelThree,NumLock=Mod2;
	interpret.useModMapMods= AnyLevel;
	interpret.repeat= False;
	interpret ISO_Level3_Shift+AnyOf(all) { virtualModifier= LevelThree; useModMapMods=level1; action= SetMods(modifiers=LevelThree,clearLocks); };
	interpret Shift_L+AnyOf(all) { action= SetMods(modifiers=modMapMods,clearLocks); };
	interpret Any+Exactly(Lock) { action= LockMods(modifiers=Lock); };
	interpret Any + AnyOf(all) { action= SetMods(modifiers=modMapMods); };
	indicator "Caps Lock" { !allowExplicit; whichModState= Locked; modifiers= Lock; };
	indicator "Group 2" { groups= 0xfe; };
	indicator "Latched Shift" { whichModState= Latched; modifiers= Shift; };
	indicator "Base Group" { whichGroupState= base; groups= 0x01; };
	indicator "Any Group" { whichGroupState= latched+locked; groups= all; };
};
xkb_symbols "test" {
	name[1]= "One";
	name[2]= "Two";
	name[3]= "Three";
	key.repeat= No;
	key <LFSH> { [ Shift_L ] };
	key <RTSH> { [ ISO_Level3_Shift ] };
	key <LCTL> { [ Control_L ] };
	key <CAPS> { [ Caps_Lock ] };
	key <LALT> { [ Alt_L, Meta_L ] };
	key <A1> { type= "ONE_LEVEL", symbols[1]= [ NoSymbol ], actions[1]= [ LatchMods(modifiers=Shift) ] };
	key <A2> { type= "ONE_LEVEL", symbols[1]= [ NoSymbol ], actions[1]= [ LatchMods(modifiers=Control,clearLocks,latchToLock) ] };
	key <A3> { type= "ONE_LEVEL", symbols[1]= [ NoSymbol ], actions[1]= [ LatchGroup(group=+1) ] };
	key <A4> { type= "ONE_LEVEL", symbols[1]= [ NoSymbol ], actions[1]= [ LatchGroup(group=2,clearLocks,latchToLock) ] };
	key <A5> { type= "ONE_LEVEL", symbols[1]= [ NoSymbol ], actions[1]= [ LockGroup(group=+1) ] };
	key <A6> { type= "ONE_LEVEL", symbols[1]= [ NoSymbol ], actions[1]= [ LockGroup(group=-1) ] };
	key <A7> { type= "ONE_LEVEL", symbols[1]= [ NoSymbol ], actions[1]= [ SetGroup(group=+1,clearLocks) ] };
	key <A8> { type= "ONE_LEVEL", symbols[1]= [ NoSymbol ], actions[1]= [ LockMods(modifiers=Mod4,affect=lock) ] };
	key <A9> { type= "ONE_LEVEL", symbols[1]= [ NoSymbol ], actions[1]= [ LockMods(modifiers=Mod4,affect=unlock) ] };
	key <B1> { type= "ONE_LEVEL", symbols[1]= [ NoSymbol ], actions[1]= [ MovePtr(x=+1,y=-1,!accel) ] };
	key <B2> { type= "ONE_LEVEL", symbols[1]= [ NoSymbol ], actions[1]= [ PtrBtn(button=1,count=2) ] };
	key <B3> { type= "ONE_LEVEL", symbols[1]= [ NoSymbol ], actions[1]= [ SwitchScreen(screen=+1,!same) ] };
	key <C1> { repeat= Yes, symbols[1]= [ a, A ], symbols[2]= [ Cyrillic_a, Cyrillic_A ], symbols[3]= [ Greek_alpha, Greek_ALPHA ] };
	key <C2> { groupsClamp, type[1]= "CTRL_FOUR", type[2]= "THREE", symbols[1]= [ b, B, { c, d }, { C, D } ], symbols[2]= [ e, E, f ] };
	modifier_map Shift { <LFSH> };
	modifier_map Lock { <CAPS> };
	modifier_map Control { <LCTL> };
	modifier_map Mod1 { Alt_L };
	modifier_map Mod5 { <RTSH> };
};
};
`

func TestLatchesLocksLEDsAndGroupsMatchLibxkbcommon(t *testing.T) {
	l := loadLibxkb(t)
	ctx := testData(t)
	g, err := ctx.NewKeymapFromString([]byte(actionKeymap))
	if err != nil {
		t.Fatal(err)
	}
	c := l.parse(actionKeymap)
	if c == 0 {
		t.Fatal("libxkbcommon refuses the test keymap")
	}
	defer l.keymapUnref(c)
	compareKeymaps(t, l, g, c)
	if a, b := normalizeText(g.Text()), normalizeText(l.text(t, c)); a != b {
		t.Errorf("serialized text differs at %s", firstDiff(a, b))
	}
	for seed := range uint64(8) {
		runScript(t, l, g, c, seed)
	}
	script := [][2]uint32{{10, 1}, {10, 0}, {24, 1}, {24, 0}, {11, 1}, {11, 0}, {11, 1}, {11, 0}, {24, 1}, {24, 0}, {12, 1}, {12, 0}, {24, 1}, {24, 0}, {14, 1}, {14, 0}, {66, 1}, {66, 0}, {16, 1}, {25, 1}, {25, 0}, {16, 0}, {66, 1}, {66, 0}}
	p := newProbe(l, g, c)
	defer l.stateUnref(p.cs)
	for i, ev := range script {
		dir := KeyReleased
		if ev[1] == 1 {
			dir = KeyPressed
		}
		p.st.UpdateKey(Keycode(ev[0]), dir)
		l.updateKey(p.cs, ev[0], int32(ev[1]))
		where := "latch script step " + strconv.Itoa(i)
		p.compare(t, where)
		p.compareKeys(t, where)
	}
}

const wtypeKeymap = `xkb_keymap {
xkb_keycodes "(unnamed)" {
minimum = 8;
maximum = 12;
<K1> = 9;
<K2> = 10;
<K3> = 11;
};
xkb_types "(unnamed)" { include "complete" };
xkb_compatibility "(unnamed)" { include "complete" };
xkb_symbols "(unnamed)" {
key <K1> {[Return]};
key <K2> {[a]};
key <K3> {[U1F600]};
};
};
`

func TestWtypeKeymapWithIncludesMatchesLibxkbcommon(t *testing.T) {
	l := loadLibxkb(t)
	ctx := testData(t)
	g, err := ctx.NewKeymapFromString([]byte(wtypeKeymap))
	if err != nil {
		t.Fatal(err)
	}
	if sym := g.NewState().KeyGetOneSym(9); sym != KeyReturn {
		t.Errorf("keycode 9 is %s, want Return", KeysymGetName(sym))
	}
	c := l.parse(wtypeKeymap)
	if c == 0 {
		t.Fatal("libxkbcommon refuses the wtype keymap")
	}
	defer l.keymapUnref(c)
	compareKeymaps(t, l, g, c)
	if a, b := normalizeText(g.Text()), normalizeText(l.text(t, c)); a != b {
		t.Errorf("serialized text differs at %s", firstDiff(a, b))
	}
	runScript(t, l, g, c, 3)
}

func TestBadKeymapsFailLoudly(t *testing.T) {
	ctx := testData(t)
	for name, text := range map[string]string{
		"empty":           "",
		"garbage":         "not a keymap",
		"truncated":       "xkb_keymap { xkb_keycodes { <A> = 9;",
		"missing include": `xkb_keymap { xkb_types { include "no_such_types_file" }; };`,
		"missing map":     `xkb_keymap { xkb_types { include "complete(no_such_map)" }; };`,
		"bad field":       `xkb_keymap { xkb_types { type "T" { nonsense = 1; }; }; };`,
		"section alone":   `xkb_keycodes { <A> = 9; };`,
	} {
		t.Run(name, func(t *testing.T) {
			km, err := ctx.NewKeymapFromString([]byte(text))
			if err == nil || km != nil {
				t.Fatalf("got keymap %v and error %v, want an error and no keymap", km, err)
			}
		})
	}
}

func TestNoDataIsAnError(t *testing.T) {
	empty := t.TempDir()
	ctx := NewContext(func(name string) string {
		if name == "XKB_CONFIG_ROOT" || name == "XKB_CONFIG_EXTRA_PATH" {
			return filepath.Join(empty, "missing")
		}
		return ""
	})
	if _, err := ctx.NewKeymapFromNames(Names{}); !errors.Is(err, ErrNoData) {
		t.Errorf("from names: %v, want ErrNoData", err)
	}
	if _, err := ctx.NewKeymapFromString([]byte(wtypeKeymap)); !errors.Is(err, ErrNoData) {
		t.Errorf("wtype keymap: %v, want ErrNoData", err)
	}
}

func TestKeysymTablesMatchLibxkbcommon(t *testing.T) {
	l := loadLibxkb(t)
	var buf [128]byte
	cname := func(sym uint32) string {
		n := l.keysymGetName(sym, &buf[0], uintptr(len(buf)))
		if n < 0 {
			return "Invalid"
		}
		return string(buf[:n])
	}
	check := func(sym uint32) {
		s := Keysym(sym)
		if a, b := KeysymGetName(s), cname(sym); a != b {
			t.Errorf("name of %#x: go %q, c %q", sym, a, b)
		}
		if a, b := uint32(KeysymToUTF32(s)), l.keysymUTF32(sym); a != b {
			t.Errorf("utf32 of %#x: go %#x, c %#x", sym, a, b)
		}
		if a, b := uint32(KeysymToUpper(s)), l.keysymUpper(sym); a != b {
			t.Errorf("upper of %#x: go %#x, c %#x", sym, a, b)
		}
		if a, b := uint32(KeysymToLower(s)), l.keysymLower(sym); a != b {
			t.Errorf("lower of %#x: go %#x, c %#x", sym, a, b)
		}
	}
	for sym := range uint32(0x20000) {
		check(sym)
	}
	for sym := uint32(keysymUnicode); sym <= keysymUnicodeMax+0x10; sym++ {
		check(sym)
	}
	for _, e := range keysymCanonical {
		check(uint32(e.sym))
	}
	rng := rand.New(rand.NewPCG(1, 2))
	for range 200000 {
		check(rng.Uint32() & 0x3fffffff)
	}
	for cp := range uint32(0x110011) {
		if a, b := uint32(UTF32ToKeysym(rune(cp))), l.utf32Keysym(cp); a != b {
			t.Errorf("keysym of U+%04X: go %#x, c %#x", cp, a, b)
		}
	}
	lookup := func(name string) {
		cn := cstr(name)
		if a, b := uint32(KeysymFromName(name, KeysymNoFlags)), l.keysymFromName(cn, 0); a != b {
			t.Errorf("from name %q: go %#x, c %#x", name, a, b)
		}
		if a, b := uint32(KeysymFromName(name, KeysymNameCaseInsensitive)), l.keysymFromName(cn, 1); a != b {
			t.Errorf("from name %q case-insensitively: go %#x, c %#x", name, a, b)
		}
	}
	for _, e := range keysymNames {
		lookup(e.name)
		lookup(strings.ToLower(e.name))
		lookup(strings.ToUpper(e.name))
	}
	for _, name := range []string{"", "U", "U0", "U41", "u41", "U10FFFF", "U110000", "U123456789", "0x", "0x61", "0X61", "0xffffffff", "0x1fffffff", "XF86_Back", "xf86_back", "XF86_", "NoSuchKeysym", "U+41"} {
		lookup(name)
	}
}

func TestComposeMatchesLibxkbcommon(t *testing.T) {
	l := loadLibxkb(t)
	ctx := testData(t)
	dir := os.Getenv("XLOCALEDIR")
	if dir == "" {
		t.Fatal("XLOCALEDIR is not set: the dev shell points it at libX11's locale data")
	}
	src, err := os.ReadFile(filepath.Join(dir, "en_US.UTF-8", "Compose"))
	if err != nil {
		t.Fatal(err)
	}
	g, err := ctx.NewComposeTableFromBuffer(src, "en_US.UTF-8")
	if err != nil {
		t.Fatal(err)
	}
	c := l.composeBuffer(l.ctx, &src[0], uintptr(len(src)), cstr("en_US.UTF-8"), 1, 0)
	if c == 0 {
		t.Fatal("libxkbcommon refuses the Compose file")
	}
	gs, cs := g.NewState(), l.composeState(c, 0)
	var buf [512]byte
	feed := func(sym Keysym, where string) {
		t.Helper()
		ga, ca := gs.Feed(sym), l.composeFeed(cs, uint32(sym))
		if (ga == ComposeFeedAccepted) != (ca == 1) {
			t.Fatalf("%s: feed %s: go %d, c %d", where, KeysymGetName(sym), ga, ca)
		}
		if a, b := int32(gs.Status()), l.composeStatus(cs); a != b {
			t.Fatalf("%s: feed %s: status go %d, c %d", where, KeysymGetName(sym), a, b)
		}
		n := l.composeUTF8(cs, &buf[0], uintptr(len(buf)))
		if a, b := gs.UTF8(), string(buf[:max(n, 0)]); a != b {
			t.Fatalf("%s: feed %s: text go %q, c %q", where, KeysymGetName(sym), a, b)
		}
		if a, b := uint32(gs.OneSym()), l.composeOneSym(cs); a != b {
			t.Fatalf("%s: feed %s: sym go %#x, c %#x", where, KeysymGetName(sym), a, b)
		}
	}
	sequences := 0
	var walk func(n *composeNode, path []Keysym)
	walk = func(n *composeNode, path []Keysym) {
		if n.leaf {
			sequences++
			for _, s := range path {
				feed(s, "sequence")
			}
			feed(Keya, "after")
			return
		}
		for sym, child := range n.children {
			walk(child, append(slices.Clip(path), sym))
		}
	}
	walk(g.root, nil)
	if sequences < 1000 {
		t.Errorf("only %d sequences in the system file", sequences)
	}
	rng := rand.New(rand.NewPCG(5, 6))
	alphabet := []Keysym{KeyMultiKey, KeyDeadAcute, KeyDeadGrave, KeyDeadTilde, Keya, Keye, Keyo, KeyShiftL, KeySpace, KeyApostrophe, KeyQuotedbl}
	for range 100000 {
		feed(alphabet[rng.IntN(len(alphabet))], "stream")
	}
	t.Logf("%d sequences", sequences)
}

func TestSystemComposeFromLocale(t *testing.T) {
	dir := os.Getenv("XLOCALEDIR")
	if dir == "" {
		t.Fatal("XLOCALEDIR is not set: the dev shell points it at libX11's locale data")
	}
	ctx := NewContext(func(name string) string {
		if name == "XLOCALEDIR" {
			return dir
		}
		return ""
	})
	table, err := ctx.NewComposeTableFromLocale("en_US.UTF-8")
	if err != nil {
		t.Fatal(err)
	}
	st := table.NewState()
	st.Feed(KeyDeadAcute)
	if st.Status() != ComposeComposing {
		t.Fatalf("status %d after dead_acute", st.Status())
	}
	st.Feed(Keye)
	if st.Status() != ComposeComposed || st.UTF8() != "é" {
		t.Errorf("status %d, text %q, want é", st.Status(), st.UTF8())
	}
}

func TestKeyEventPathAllocatesNothing(t *testing.T) {
	var textSink string
	ctx := testData(t)
	km, err := ctx.NewKeymapFromNames(Names{Layout: "us,ru", Options: "grp:alt_shift_toggle"})
	if err != nil {
		t.Fatal(err)
	}
	st := km.NewState()
	buf := make([]byte, 0, 64)
	var sink uint32
	for name, fn := range map[string]func(){
		"UpdateKey letter": func() { st.UpdateKey(38, KeyPressed); st.UpdateKey(38, KeyReleased) },
		"UpdateKey shift":  func() { st.UpdateKey(50, KeyPressed); st.UpdateKey(50, KeyReleased) },
		"UpdateKey caps":   func() { st.UpdateKey(66, KeyPressed); st.UpdateKey(66, KeyReleased) },
		"UpdateMask":       func() { st.UpdateMask(1, 0, 2, 0, 0, 1) },
		"KeyGetOneSym":     func() { sink += uint32(st.KeyGetOneSym(38)) },
		"KeyGetSyms":       func() { sink += uint32(len(st.KeyGetSyms(38))) },
		"KeyGetUTF32":      func() { sink += uint32(st.KeyGetUTF32(38)) },
		"AppendKeyUTF8":    func() { sink += uint32(len(st.AppendKeyUTF8(buf[:0], 38))) },
		"KeyGetUTF8":       func() { textSink = st.KeyGetUTF8(38) },
		"KeyGetUTF8 ctrl": func() {
			st.UpdateMask(4, 0, 0, 0, 0, 0)
			textSink = st.KeyGetUTF8(38)
			st.UpdateMask(0, 0, 0, 0, 0, 0)
		},
		"KeyGetConsumedMods XKB": func() { sink += uint32(st.KeyGetConsumedMods(38, ConsumedModeXKB)) },
		"KeyGetConsumedMods GTK": func() { sink += uint32(st.KeyGetConsumedMods(38, ConsumedModeGTK)) },
		"ModMaskRemoveConsumed":  func() { sink += uint32(st.ModMaskRemoveConsumed(38, 3)) },
		"UpdateLatchedLocked": func() {
			st.UpdateLatchedLocked(1, 1, true, 1, 2, 2, true, 1)
			st.UpdateMask(0, 0, 0, 0, 0, 0)
		},
		"SerializeMods":   func() { sink += uint32(st.SerializeMods(StateModEffective)) },
		"SerializeLayout": func() { sink += st.SerializeLayout(StateLayoutEffective) },
		"LEDNameIsActive": func() {
			if st.LEDNameIsActive("Caps Lock") {
				sink++
			}
		},
		"ModNameIsActive": func() {
			if st.ModNameIsActive("Shift", StateModEffective) {
				sink++
			}
		},
		"KeysymFromName": func() { sink += uint32(KeysymFromName("Return", KeysymNoFlags)) },
	} {
		if a := testing.AllocsPerRun(1000, fn); a != 0 {
			t.Errorf("%s allocates %v times", name, a)
		}
	}
	_, _ = sink, textSink
}
