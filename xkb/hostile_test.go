package xkb

import (
	"strings"
	"testing"
)

func hostileContext(t testing.TB) *Context {
	t.Helper()
	dir := t.TempDir()
	return NewContext(func(k string) string {
		switch k {
		case "XKB_CONFIG_ROOT", "HOME":
			return dir
		}
		return ""
	})
}

func TestHostileKeymapTextFailsWithoutCrashing(t *testing.T) {
	const n = 2_000_000
	keycodes := func(value string) string { return "xkb_keymap { xkb_keycodes { <a> = " + value + "; }; };" }
	for name, text := range map[string]string{
		"nested parens":   keycodes(strings.Repeat("(", n) + "1" + strings.Repeat(")", n)),
		"nested unary":    keycodes(strings.Repeat("-", n) + "1"),
		"long chain":      keycodes("1" + strings.Repeat("+1", n)),
		"chained assigns": "xkb_keymap { xkb_keycodes { " + strings.Repeat("a=", n) + "1; }; };",
		"nested action":   "xkb_keymap { xkb_compat { interpret Any+AnyOf(all) { action = " + strings.Repeat("A(", n) + "; }; }; };",
		"nested lists":    "xkb_keymap { xkb_symbols { key <a> { " + strings.Repeat("[", n) + " }; }; };",
		"nested blocks":   "xkb_keymap { xkb_symbols { key <a> { " + strings.Repeat("{", n) + " }; }; };",
		"unterminated":    `xkb_keymap { xkb_keycodes "`,
		"nul":             "\x00\x00\x00",
		"empty":           "",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := hostileContext(t).NewKeymapFromString([]byte(text)); err == nil {
				t.Fatal("compiled")
			}
		})
	}
}

func TestDeepButReasonableExpressionsStillCompile(t *testing.T) {
	const n = 100
	text := "xkb_keymap { xkb_keycodes { <a> = 9; }; xkb_types {}; xkb_compat { indicator \"Caps Lock\" { modifiers = " + strings.Repeat("(", n) + "Lock" + strings.Repeat(")", n) + "; }; }; xkb_symbols {}; };"
	if _, err := hostileContext(t).NewKeymapFromString([]byte(text)); err != nil {
		t.Fatal(err)
	}
}

func FuzzNewKeymapFromString(f *testing.F) {
	for _, seed := range []string{
		"xkb_keymap { xkb_keycodes { <a> = 9; indicator 1 = \"Caps Lock\"; }; xkb_types { virtual_modifiers NumLock; type \"X\" { modifiers = Shift+NumLock; map[Shift] = Level2; level_name[Level1] = \"a\"; }; }; xkb_compat { interpret a+AnyOf(all) { action = SetMods(modifiers=Shift,clearLocks); repeat = true; }; indicator \"Caps Lock\" { modifiers = Lock; }; }; xkb_symbols { name[Group1] = \"x\"; key <a> { type = \"X\", symbols[Group1] = [ a, A ], actions[Group1] = [ LockGroup(group=+1), NoAction() ] }; modifier_map Control { <a> }; }; };",
		`xkb_keymap { xkb_keycodes { include "evdev+aliases(qwerty)" }; xkb_types { include "complete" }; xkb_compat { include "complete" }; xkb_symbols { include "pc+us" }; };`,
	} {
		f.Add([]byte(seed))
	}
	c := hostileContext(f)
	f.Fuzz(func(t *testing.T, text []byte) {
		m, err := c.NewKeymapFromString(text)
		if err != nil {
			return
		}
		_ = m.Text()
		s := m.NewState()
		last := min(m.MaxKeycode(), m.MinKeycode()+300)
		for kc := m.MinKeycode(); kc <= last; kc++ {
			s.UpdateKey(kc, KeyPressed)
			s.KeyGetSyms(kc)
			s.KeyGetUTF8(kc)
			s.UpdateKey(kc, KeyReleased)
		}
		s.UpdateMask(0xffffffff, 0xffffffff, 0xffffffff, 0xffffffff, 0x80000000, 0x7fffffff)
		for kc := m.MinKeycode(); kc <= last; kc++ {
			s.KeyGetOneSym(kc)
		}
	})
}
