package logind

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	dbus "github.com/stubbedev/lys-lib/third_party/godbus"
)

const (
	owner   = ":1.7"
	forger  = ":1.9"
	watched = dbus.ObjectPath("/org/freedesktop/login1/session/c2")
)

func decodeAll(t *testing.T, sig *dbus.Signal) ([]Event, error) {
	t.Helper()
	var got []Event
	err := decoder{owner: owner, session: watched}.decode(sig, func(e Event) { got = append(got, e) })
	return got, err
}

func changed(props map[string]dbus.Variant) *dbus.Signal {
	return &dbus.Signal{Sender: owner, Path: watched, Name: propertiesInterface + ".PropertiesChanged", Body: []any{SessionInterface, props, []string{}}}
}

func TestDecodeReadsBothPropertiesOfOneChangeInOrder(t *testing.T) {
	t.Parallel()
	got, err := decodeAll(t, changed(map[string]dbus.Variant{
		"LockedHint": dbus.MakeVariant(true),
		"Active":     dbus.MakeVariant(false),
		"IdleHint":   dbus.MakeVariant(true),
	}))
	want := []Event{ActiveChanged{Active: false}, LockedHintChanged{Locked: true}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("decode = %v, %v; want %v", got, err, want)
	}
}

func TestDecodeRefusesAPropertyOfTheWrongType(t *testing.T) {
	t.Parallel()
	got, err := decodeAll(t, changed(map[string]dbus.Variant{
		"LockedHint": dbus.MakeVariant("yes"),
		"Active":     dbus.MakeVariant(true),
	}))
	if err == nil || !strings.Contains(err.Error(), "LockedHint of "+string(watched)+" changed to a s, want b") {
		t.Errorf("decode error %v, want the LockedHint type named", err)
	}
	if !reflect.DeepEqual(got, []Event{ActiveChanged{Active: true}}) {
		t.Errorf("decode = %v, want the well-typed Active still read", got)
	}
}

func TestDecodeIgnoresWhatIsNotTheWatchedSession(t *testing.T) {
	t.Parallel()
	for name, sig := range map[string]*dbus.Signal{
		"another session":   {Sender: owner, Path: "/org/freedesktop/login1/session/_33", Name: Lock.Name()},
		"another interface": {Sender: owner, Path: watched, Name: propertiesInterface + ".PropertiesChanged", Body: []any{"org.example.Other", map[string]dbus.Variant{"Active": dbus.MakeVariant(true)}, []string{}}},
		"another member":    {Sender: owner, Path: watched, Name: SessionInterface + ".Other"},
		"a forged Lock":     {Sender: forger, Path: watched, Name: Lock.Name()},
		"a forged sleep":    {Sender: forger, Path: ManagerPath, Name: PrepareForSleep.Name(), Body: []any{true}},
	} {
		if got, err := decodeAll(t, sig); err != nil || len(got) != 0 {
			t.Errorf("%s: decode = %v, %v; want nothing", name, got, err)
		}
	}
}

func pipe(t *testing.T) (read int, write dbus.UnixFD) {
	t.Helper()
	fds := make([]int, 2)
	if err := unix.Pipe2(fds, unix.O_CLOEXEC|unix.O_NONBLOCK); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = unix.Close(fds[0]) })
	return fds[0], dbus.UnixFD(fds[1])
}

func eof(read int) (bool, error) {
	n, err := unix.Read(read, make([]byte, 1))
	if errors.Is(err, unix.EAGAIN) {
		return false, nil
	}
	return n == 0 && err == nil, err
}

func closed(t *testing.T, read int) bool {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if done, err := eof(read); done || err != nil {
			return done
		}
	}
	return false
}

func TestDecodeClosesTheDescriptorOfAResumeDeviceItDoesNotHandOver(t *testing.T) {
	t.Parallel()
	read, write := pipe(t)
	got, err := decodeAll(t, &dbus.Signal{Sender: forger, Path: watched, Name: ResumeDevice.Name(), Body: []any{uint32(1), uint32(3), write}})
	if err != nil || len(got) != 0 {
		t.Errorf("a forged ResumeDevice: decode = %v, %v; want nothing", got, err)
	}
	if !closed(t, read) {
		t.Error("the forged ResumeDevice's descriptor stayed open")
	}

	read, write = pipe(t)
	got, err = decodeAll(t, &dbus.Signal{Sender: owner, Path: watched, Name: ResumeDevice.Name(), Body: []any{uint32(1), write}})
	if err == nil || len(got) != 0 {
		t.Errorf("a short ResumeDevice: decode = %v, %v; want an error", got, err)
	}
	if !closed(t, read) {
		t.Error("the malformed ResumeDevice's descriptor stayed open")
	}
}

func TestDecodeHandsOverTheDescriptorOfAResumeDevice(t *testing.T) {
	t.Parallel()
	read, write := pipe(t)
	got, err := decodeAll(t, &dbus.Signal{Sender: owner, Path: watched, Name: ResumeDevice.Name(), Body: []any{uint32(1), uint32(3), write}})
	if err != nil || !reflect.DeepEqual(got, []Event{DeviceResumed{Major: 1, Minor: 3, FD: write}}) {
		t.Fatalf("decode = %v, %v", got, err)
	}
	if done, err := eof(read); done || err != nil {
		t.Errorf("the handed-over descriptor was closed: %v", err)
	}
	if err := unix.Close(int(write)); err != nil {
		t.Fatal(err)
	}
}

func TestDecodeRefusesAMalformedSignal(t *testing.T) {
	t.Parallel()
	for name, sig := range map[string]*dbus.Signal{
		"PauseDevice":       {Sender: owner, Path: watched, Name: PauseDevice.Name(), Body: []any{uint32(1), uint32(3)}},
		"PrepareForSleep":   {Sender: owner, Path: ManagerPath, Name: PrepareForSleep.Name(), Body: []any{"yes"}},
		"PropertiesChanged": {Sender: owner, Path: watched, Name: propertiesInterface + ".PropertiesChanged", Body: []any{SessionInterface}},
	} {
		if got, err := decodeAll(t, sig); err == nil || len(got) != 0 {
			t.Errorf("%s: decode = %v, %v; want an error", name, got, err)
		}
	}
}

func TestDecodeReadsAPauseKind(t *testing.T) {
	t.Parallel()
	got, err := decodeAll(t, &dbus.Signal{Sender: owner, Path: watched, Name: PauseDevice.Name(), Body: []any{uint32(226), uint32(1), "gone"}})
	if err != nil || !reflect.DeepEqual(got, []Event{DevicePaused{Major: 226, Minor: 1, Kind: PauseGone}}) {
		t.Errorf("decode = %v, %v", got, err)
	}
}

func TestDescribeQuotesStrings(t *testing.T) {
	t.Parallel()
	if got := describe(ManagerInterface, "Inhibit", []any{"sleep", "who", uint32(3), true}); got != `Manager.Inhibit("sleep", "who", 3, true)` {
		t.Errorf("describe = %s", got)
	}
}
