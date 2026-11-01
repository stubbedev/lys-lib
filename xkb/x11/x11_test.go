package x11_test

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unsafe"

	"github.com/ebitengine/purego"

	"github.com/stubbedev/lys-lib/clib"
	"github.com/stubbedev/lys-lib/xkb"
	"github.com/stubbedev/lys-lib/xkb/x11"
)

type cLib struct {
	context      func(flags int32) uintptr
	xcbConnect   func(display *byte, screen *int32) uintptr
	setup        func(conn uintptr, major, minor uint16, flags int32, a, b *uint16, c, d *byte) int32
	deviceID     func(conn uintptr) int32
	fromDevice   func(ctx, conn uintptr, device, flags int32) uintptr
	stateDevice  func(km, conn uintptr, device int32) uintptr
	asString     func(km uintptr, format int32, flags int32) unsafe.Pointer
	serializeMod func(st uintptr, comp int32) uint32
	serializeGrp func(st uintptr, comp int32) uint32
	ledIndex     func(st uintptr, idx uint32) int32
	numLEDs      func(km uintptr) uint32
}

func openC(t *testing.T) *cLib {
	t.Helper()
	x, err := clib.Open(clib.SystemCandidates("libxkbcommon-x11.so.0"))
	if err != nil {
		t.Fatalf("libxkbcommon-x11: %v", err)
	}
	k, err := clib.Open(clib.SystemCandidates("libxkbcommon.so.0"))
	if err != nil {
		t.Fatalf("libxkbcommon: %v", err)
	}
	xcb, err := clib.Open(clib.SystemCandidates("libxcb.so.1"))
	if err != nil {
		t.Fatalf("libxcb: %v", err)
	}
	l := &cLib{}
	purego.RegisterLibFunc(&l.context, k, "xkb_context_new")
	purego.RegisterLibFunc(&l.xcbConnect, xcb, "xcb_connect")
	purego.RegisterLibFunc(&l.setup, x, "xkb_x11_setup_xkb_extension")
	purego.RegisterLibFunc(&l.deviceID, x, "xkb_x11_get_core_keyboard_device_id")
	purego.RegisterLibFunc(&l.fromDevice, x, "xkb_x11_keymap_new_from_device")
	purego.RegisterLibFunc(&l.stateDevice, x, "xkb_x11_state_new_from_device")
	purego.RegisterLibFunc(&l.asString, k, "xkb_keymap_get_as_string2")
	purego.RegisterLibFunc(&l.serializeMod, k, "xkb_state_serialize_mods")
	purego.RegisterLibFunc(&l.serializeGrp, k, "xkb_state_serialize_layout")
	purego.RegisterLibFunc(&l.ledIndex, k, "xkb_state_led_index_is_active")
	purego.RegisterLibFunc(&l.numLEDs, k, "xkb_keymap_num_leds")
	return l
}

func startXvfb(t *testing.T) string {
	t.Helper()
	bin, err := exec.LookPath("Xvfb")
	if err != nil {
		t.Fatalf("Xvfb is not on PATH: the dev shell provides it: %v", err)
	}
	display := fmt.Sprintf(":%d", 90+os.Getpid()%400)
	dir := t.TempDir()
	cmd := exec.Command(bin, display, "-screen", "0", "64x64x24", "-nolisten", "tcp", "-noreset", "-auth", filepath.Join(dir, "none"))
	var logs bytes.Buffer
	cmd.Stdout, cmd.Stderr = &logs, &logs
	if err := cmd.Start(); err != nil {
		t.Fatalf("start Xvfb: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	socket := "/tmp/.X11-unix/X" + strings.TrimPrefix(display, ":")
	deadline := time.Now().Add(15 * time.Second)
	for {
		if _, err := os.Stat(socket); err == nil {
			return display
		}
		if time.Now().After(deadline) {
			t.Fatalf("Xvfb did not start: %s", logs.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func cstr(s string) *byte {
	b := append([]byte(s), 0)
	return &b[0]
}

func goStr(p unsafe.Pointer) string {
	return clib.GoString((*byte)(p))
}

func normalize(s string) string {
	lines := strings.Split(s, "\n")
	out := lines[:0]
	var aliases []string
	for _, l := range lines {
		if strings.HasPrefix(l, "\talias <") {
			aliases = append(aliases, l)
			continue
		}
		out = append(out, l)
	}
	return strings.Join(out, "\n") + strings.Join(sorted(aliases), "\n")
}

func sorted(s []string) []string {
	for i := range s {
		for j := i + 1; j < len(s); j++ {
			if s[j] < s[i] {
				s[i], s[j] = s[j], s[i]
			}
		}
	}
	return s
}

func firstDiff(a, b string) string {
	la, lb := strings.Split(a, "\n"), strings.Split(b, "\n")
	for i := range la {
		if i >= len(lb) || la[i] != lb[i] {
			other := ""
			if i < len(lb) {
				other = lb[i]
			}
			return fmt.Sprintf("line %d: go %q, c %q", i+1, la[i], other)
		}
	}
	return fmt.Sprintf("c has %d more lines", len(lb)-len(la))
}

// TestKeymapsFromAnXServerMatchLibxkbcommonX11 uploads keymaps to a private
// Xvfb and reads them back through this package and libxkbcommon-x11.
func TestKeymapsFromAnXServerMatchLibxkbcommonX11(t *testing.T) {
	c := openC(t)
	display := startXvfb(t)
	xkbcomp, err := exec.LookPath("xkbcomp")
	if err != nil {
		t.Fatalf("xkbcomp is not on PATH: the dev shell provides it: %v", err)
	}
	setxkbmap, err := exec.LookPath("setxkbmap")
	if err != nil {
		t.Fatalf("setxkbmap is not on PATH: the dev shell provides it: %v", err)
	}
	root := os.Getenv("XKB_CONFIG_ROOT")
	getenv := func(name string) string {
		if name == "XKB_CONFIG_ROOT" {
			return os.Getenv(name)
		}
		return ""
	}
	conn, err := x11.Dial(display, getenv)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	ctx := xkb.NewContextFlags(getenv, xkb.ContextNoEnvironmentNames)
	x, err := xkb.SetupX11(conn, xkb.X11MinMajorVersion, xkb.X11MinMinorVersion)
	if err != nil {
		t.Fatal(err)
	}
	var cMajor, cMinor uint16
	var cEvent, cError byte
	var screen int32
	cConn := c.xcbConnect(cstr(display), &screen)
	if c.setup(cConn, 1, 0, 0, &cMajor, &cMinor, &cEvent, &cError) != 1 {
		t.Fatal("xkb_x11_setup_xkb_extension failed")
	}
	if x.ServerMajor != cMajor || x.ServerMinor != cMinor || x.BaseEvent != cEvent || x.BaseError != cError {
		t.Errorf("setup: go %d.%d %d %d, c %d.%d %d %d", x.ServerMajor, x.ServerMinor, x.BaseEvent, x.BaseError, cMajor, cMinor, cEvent, cError)
	}
	device, err := x.CoreKeyboardDeviceID()
	if err != nil {
		t.Fatal(err)
	}
	if want := c.deviceID(cConn); device != want {
		t.Errorf("device: go %d, c %d", device, want)
	}
	cctx := c.context(0)
	for _, n := range []xkb.Names{
		{},
		{Layout: "us"},
		{Layout: "dk"},
		{Layout: "de", Variant: "neo"},
		{Layout: "us,ru", Options: "grp:alt_shift_toggle"},
		{Layout: "us,de,fr,gr", Options: "grp:caps_toggle,grp_led:scroll"},
		{Layout: "us", Options: "ctrl:nocaps"},
		{Layout: "jp"},
		{Layout: "ara"},
		{Layout: "fr"},
		{Layout: "us", Options: "lv3:ralt_switch,compose:menu"},
	} {
		label := fmt.Sprintf("%+v", n)
		if n != (xkb.Names{}) {
			args := []string{"-print", "-rules", "evdev", "-model", "pc105", "-layout", n.Layout}
			if n.Variant != "" {
				args = append(args, "-variant", n.Variant)
			}
			if n.Options != "" {
				args = append(args, "-option", n.Options)
			}
			set := exec.Command(setxkbmap, append(args, "-I", root, "-display", display)...)
			printed, err := set.CombinedOutput()
			if err != nil {
				t.Fatalf("%s: setxkbmap: %v: %s", label, err, printed)
			}
			comp := exec.Command(xkbcomp, "-w", "0", "-I"+root, "-", display)
			comp.Stdin = bytes.NewReader(printed)
			if out, err := comp.CombinedOutput(); err != nil {
				t.Fatalf("%s: xkbcomp: %v: %s", label, err, out)
			}
		}
		g, err := x.NewKeymapFromDevice(ctx, device)
		if err != nil {
			t.Fatalf("%s: %v", label, err)
		}
		cm := c.fromDevice(cctx, cConn, device, 0)
		if cm == 0 {
			t.Fatalf("%s: xkb_x11_keymap_new_from_device failed", label)
		}
		for _, format := range []xkb.KeymapFormat{xkb.FormatTextV1, xkb.FormatTextV2} {
			for _, flags := range []xkb.SerializeFlags{0, xkb.SerializePretty, xkb.SerializeKeepUnused} {
				got, err := g.AsString(format, flags)
				if err != nil {
					t.Fatal(err)
				}
				a, b := normalize(got), normalize(goStr(c.asString(cm, int32(format), int32(flags))))
				if a != b {
					t.Errorf("%s: text format %d flags %d: %s", label, format, flags, firstDiff(a, b))
				}
			}
		}
		st, err := x.NewStateFromDevice(g, device)
		if err != nil {
			t.Fatalf("%s: %v", label, err)
		}
		cs := c.stateDevice(cm, cConn, device)
		if cs == 0 {
			t.Fatalf("%s: xkb_x11_state_new_from_device failed", label)
		}
		for _, comp := range []xkb.StateComponent{xkb.StateModDepressed, xkb.StateModLatched, xkb.StateModLocked, xkb.StateModEffective} {
			if a, b := uint32(st.SerializeMods(comp)), c.serializeMod(cs, int32(comp)); a != b {
				t.Errorf("%s: mods %#x: go %#x, c %#x", label, comp, a, b)
			}
		}
		for _, comp := range []xkb.StateComponent{xkb.StateLayoutDepressed, xkb.StateLayoutLatched, xkb.StateLayoutLocked, xkb.StateLayoutEffective} {
			if a, b := st.SerializeLayout(comp), c.serializeGrp(cs, int32(comp)); a != b {
				t.Errorf("%s: layout %#x: go %#x, c %#x", label, comp, a, b)
			}
		}
		for i := range c.numLEDs(cm) {
			if a, b := st.LEDIndexIsActive(i), c.ledIndex(cs, i) == 1; a != b {
				t.Errorf("%s: LED %d: go %v, c %v", label, i, a, b)
			}
		}
	}
}
