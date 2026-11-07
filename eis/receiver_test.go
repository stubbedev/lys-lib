package eis

import (
	"errors"
	"slices"
	"strings"
	"syscall"
	"testing"

	"github.com/stubbedev/lys-lib/eis/eistest"
)

const (
	testKeymap        = "xkb_keymap { xkb_keycodes { include \"evdev\" }; };"
	changedTestKeymap = "xkb_keymap { xkb_keycodes { include \"evdev+aliases(qwerty)\" }; };"
)

var receiverCaps = []uintptr{eistest.CapPointer, eistest.CapKeyboard, eistest.CapButton, eistest.CapScroll}

func newTestReceiver(t *testing.T, keymap string) (*Receiver, int) {
	t.Helper()
	recv, client, err := NewReceiver(testSeat, keymap)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = recv.Close() })
	return recv, takeFd(t, client.Fd(), client.Close)
}

func takeFd(t *testing.T, fd uintptr, closeFile func() error) int {
	t.Helper()
	dup, err := syscall.Dup(int(fd))
	if err != nil {
		t.Fatal(err)
	}
	if err := closeFile(); err != nil {
		t.Fatal(err)
	}
	return dup
}

func must(t *testing.T, errs ...error) {
	t.Helper()
	if err := errors.Join(errs...); err != nil {
		t.Fatal(err)
	}
}

func resumed(t *testing.T, fd int) *eistest.Client {
	t.Helper()
	c := eistest.Receive(t, fd, testSeat, receiverCaps...)
	c.Until("both devices resumed", c.Saw("connect", "seat "+testSeat, "resumed keyboard", "resumed pointer"))
	return c
}

func TestLibeiReceiverGetsTheCapturedInput(t *testing.T) {
	recv, fd := newTestReceiver(t, testKeymap)
	c := resumed(t, fd)
	if got, want := c.Keymaps["keyboard"], testKeymap+"\x00"; got != want {
		t.Errorf("keymap %q, want %q", got, want)
	}
	must(t, recv.Start(42), recv.Motion(1.5, -2), recv.Button(0x110, true), recv.Scroll(0, 3), recv.ScrollStop(false, true),
		recv.ScrollDiscrete(0, -240), recv.PointerFrame(), recv.Modifiers(1, 0, 2, 0), recv.Key(30, true), recv.Key(30, false), recv.Stop())
	c.Until("the emulation to stop", c.Saw("stop keyboard", "stop pointer"))
	want := []string{
		"start keyboard 42", "start pointer 42",
		"motion 1.5 -2", "button 272 true", "scroll 0 3", "scroll-stop false true", "discrete 0 -240", "frame pointer",
		"modifiers 1 0 2 0", "key 30 true", "frame keyboard", "key 30 false", "frame keyboard",
		"stop keyboard", "stop pointer",
	}
	if got := c.Since("start keyboard 42"); !slices.Equal(got, want) {
		t.Errorf("libei received\n%q\nwant\n%q", got, want)
	}
}

func TestEventsOutsideAnEmulationAreRefused(t *testing.T) {
	recv, fd := newTestReceiver(t, testKeymap)
	resumed(t, fd)
	for name, err := range map[string]error{
		"motion": recv.Motion(1, 1), "key": recv.Key(30, true), "frame": recv.PointerFrame(), "stop": recv.Stop(),
	} {
		if err == nil {
			t.Errorf("%s outside an emulation was taken", name)
		}
	}
	must(t, recv.Start(1))
	if err := recv.Start(2); err == nil {
		t.Error("a second start inside an emulation was taken")
	}
	must(t, recv.Stop())
}

func TestAReceiverStartsDevicesBoundMidEmulation(t *testing.T) {
	recv, fd := newTestReceiver(t, testKeymap)
	must(t, recv.Start(7))
	c := eistest.Receive(t, fd, testSeat, receiverCaps...)
	c.Until("the late devices to start", c.Saw("start keyboard 7", "start pointer 7"))
	must(t, recv.Key(30, true))
	c.Until("the key", c.Saw("key 30 true"))
}

func TestANewKeymapReannouncesTheKeyboard(t *testing.T) {
	recv, fd := newTestReceiver(t, testKeymap)
	c := resumed(t, fd)
	must(t, recv.Start(3), recv.SetKeymap(changedTestKeymap))
	c.Until("the new keyboard started", func() bool { return len(c.Since("removed keyboard")) >= 3 })
	if got, want := c.Since("removed keyboard")[:3], []string{"removed keyboard", "resumed keyboard", "start keyboard 3"}; !slices.Equal(got, want) {
		t.Errorf("after the new keymap libei saw %q, want %q", got, want)
	}
	if got, want := c.Keymaps["keyboard"], changedTestKeymap+"\x00"; got != want {
		t.Errorf("keymap %q, want %q", got, want)
	}
	must(t, recv.Key(30, true))
	c.Until("a key on the new keyboard", c.Saw("key 30 true"))
}

func TestTheContextTypesDoNotCross(t *testing.T) {
	_, fd := newTestReceiver(t, testKeymap)
	sender := eistest.Send(t, fd, testSeat, receiverCaps...)
	sender.Until("the receiver to refuse a sender", sender.Saw("disconnect"))
	if slices.Contains(sender.Log, "seat "+testSeat) {
		t.Errorf("a sender got the receiver's seat: %q", sender.Log)
	}

	client, err := Pair(&recorder{}, testSeat, nil)
	if err != nil {
		t.Fatal(err)
	}
	receiver := eistest.Receive(t, takeFd(t, client.Fd(), client.Close), testSeat, receiverCaps...)
	receiver.Until("the sender server to refuse a receiver", receiver.Saw("disconnect"))
	if slices.Contains(receiver.Log, "seat "+testSeat) {
		t.Errorf("a receiver got the sender server's seat: %q", receiver.Log)
	}
}

func TestAReceiverNeedsASeatAndAKeymap(t *testing.T) {
	if _, f, err := NewReceiver("", testKeymap); err == nil || f != nil {
		t.Errorf("NewReceiver without a seat = %v, %v", f, err)
	}
	if _, f, err := NewReceiver(testSeat, ""); err == nil || f != nil || !strings.Contains(err.Error(), "the keymap is empty") {
		t.Errorf("NewReceiver without a keymap = %v, %v", f, err)
	}
}
