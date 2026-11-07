package vinput

import (
	"bytes"
	"errors"
	"os"
	"testing"
)

// The devices against the headless sway the just headless recipe
// boots (LYS_TEST_HEADLESS set); without it they skip.
func TestDialArmsTheDevices(t *testing.T) {
	if os.Getenv("LYS_TEST_HEADLESS") == "" {
		t.Skip("LYS_TEST_HEADLESS is not set; vinput tests run under just check-headless")
	}
	fallback := []byte("xkb_keymap { xkb_keycodes { include \"evdev\" }; xkb_types { include \"complete\" }; xkb_compat { include \"complete\" }; xkb_symbols { include \"pc+us\" }; };\n")
	d, err := Dial("", Options{Keyboard: true, FallbackKeymap: fallback})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if len(d.Keymap()) == 0 {
		t.Error("no keymap armed")
	}
	if w, h := d.OutputSize(); w <= 0 || h <= 0 {
		t.Errorf("output size %dx%d", w, h)
	}
	for name, err := range map[string]error{
		"motion":   d.Motion(3, 4),
		"absolute": d.MotionAbsolute(10, 10, 100, 100),
		"button":   d.Button(0x110, true),
		"release":  d.Button(0x110, false),
		"axis":     d.Axis(SourceFinger, AxisVertical, 15),
		"stop":     d.AxisStop(SourceFinger, AxisVertical),
		"discrete": d.AxisDiscrete(SourceWheel, AxisHorizontal, 15, 1),
		"key":      d.Key(30, true),
		"key up":   d.Key(30, false),
		"mods":     d.Modifiers(0, 0, 0, 0),
	} {
		if err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	if err := d.Roundtrip(); err != nil {
		t.Fatalf("the compositor rejected the input: %v", err)
	}

	p, err := Dial("", Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if p.Keymap() != nil || p.Key(30, true) == nil {
		t.Error("a pointer-only device has a keyboard")
	}
}

// A closed device refuses pointer requests instead of writing to the
// gone connection.
func TestClosedDeviceRefusesInput(t *testing.T) {
	if os.Getenv("LYS_TEST_HEADLESS") == "" {
		t.Skip("LYS_TEST_HEADLESS is not set; vinput tests run under just check-headless")
	}
	d, err := Dial("", Options{})
	if err != nil {
		t.Fatal(err)
	}
	d.Close()
	if err := d.AxisStop(SourceFinger, AxisVertical); !errors.Is(err, errClosed) {
		t.Errorf("AxisStop on a closed device = %v, want errClosed", err)
	}
	d.Close()
}

func TestDialWithoutACompositor(t *testing.T) {
	if _, err := Dial("wayland-vinput-nonexistent", Options{}); err == nil {
		t.Error("dialed a missing socket")
	}
}

func TestAKeymapOfItsOwnWinsOverTheSeats(t *testing.T) {
	if os.Getenv("LYS_TEST_HEADLESS") == "" {
		t.Skip("LYS_TEST_HEADLESS is not set; vinput tests run under just check-headless")
	}
	own := []byte("xkb_keymap { xkb_keycodes { <K1> = 9; }; xkb_types { include \"complete\" }; xkb_compat { include \"complete\" }; xkb_symbols { key <K1> {[Return]}; }; };\n")
	d, err := Dial("", Options{Keyboard: true, Keymap: own, FallbackKeymap: []byte("unused")})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if !bytes.Equal(d.Keymap(), own) {
		t.Errorf("the keyboard carries %q", d.Keymap())
	}
	if err := errors.Join(d.Key(1, true), d.Key(1, false), d.Roundtrip()); err != nil {
		t.Fatal(err)
	}
}

// A touchscreen asked of a compositor without lys_virtual_touch_v1 (the
// headless sway) fails Dial with ErrNoTouch rather than arming a device
// whose touches would go nowhere.
func TestTouchWithoutTheProtocolIsRefused(t *testing.T) {
	if os.Getenv("LYS_TEST_HEADLESS") == "" {
		t.Skip("LYS_TEST_HEADLESS is not set; vinput tests run under just check-headless")
	}
	d, err := Dial("", Options{Touch: true})
	if !errors.Is(err, ErrNoTouch) {
		if d != nil {
			d.Close()
		}
		t.Fatalf("Dial with touch = %v, want ErrNoTouch", err)
	}
}
