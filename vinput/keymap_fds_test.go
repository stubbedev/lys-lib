package vinput

import (
	"encoding/binary"
	"errors"
	"maps"
	"net"
	"os"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/stubbedev/lys-lib/keymapfd"
	"github.com/stubbedev/lys-lib/third_party/wayland/wl"
	"github.com/stubbedev/lys-lib/third_party/wayland/wlclient"
	"github.com/stubbedev/lys-lib/wlr"
	"github.com/stubbedev/lys-lib/wltest"
)

const (
	keymapEvents       = 32
	keymapFormatNone   = 0
	keymapFormatXkbV1  = 1
	keymapRelayRequest = 16
	keymapPeerWait     = 10 * time.Second
)

// descriptors is what every descriptor of the process is open on. The
// directory read's own descriptor is gone by the time it is resolved.
func descriptors(t *testing.T) map[int]string {
	t.Helper()
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	open := make(map[int]string, len(entries))
	for _, e := range entries {
		fd, err := strconv.Atoi(e.Name())
		if err != nil {
			t.Fatalf("descriptor %q: %v", e.Name(), err)
		}
		target, err := os.Readlink("/proc/self/fd/" + e.Name())
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		open[fd] = target
	}
	return open
}

func keymapEvent(keyboard wl.ProxyId, format, size uint32) []byte {
	msg := binary.NativeEndian.AppendUint32(nil, uint32(keyboard))
	msg = binary.NativeEndian.AppendUint32(msg, 16<<16)
	msg = binary.NativeEndian.AppendUint32(msg, format)
	return binary.NativeEndian.AppendUint32(msg, size)
}

// relayedKeymap reads the one keymap request the device sent and the
// text behind the descriptor it carries, which it closes.
func relayedKeymap(t *testing.T, compositor *net.UnixConn, size uint32) []byte {
	t.Helper()
	if err := compositor.SetReadDeadline(time.Now().Add(keymapPeerWait)); err != nil {
		t.Fatal(err)
	}
	body, oob := make([]byte, keymapRelayRequest), make([]byte, syscall.CmsgSpace(4))
	n, oobn, _, _, err := compositor.ReadMsgUnix(body, oob)
	if err != nil || n != keymapRelayRequest {
		t.Fatalf("read the relayed keymap request = %d bytes, %v; want %d", n, err, keymapRelayRequest)
	}
	scms, err := syscall.ParseSocketControlMessage(oob[:oobn])
	if err != nil || len(scms) != 1 {
		t.Fatalf("the relayed keymap request carries %d control messages, %v; want one", len(scms), err)
	}
	fds, err := syscall.ParseUnixRights(&scms[0])
	if err != nil || len(fds) != 1 {
		t.Fatalf("the relayed keymap request carries descriptors %v, %v; want one", fds, err)
	}
	defer keymapfd.Close(uintptr(fds[0]))
	text, err := keymapfd.Read(uintptr(fds[0]), size)
	if err != nil {
		t.Fatal(err)
	}
	return text
}

func TestKeymapEventsLeaveNoDescriptorBehind(t *testing.T) {
	const text = "xkb_keymap { };"
	for _, tc := range []struct {
		name    string
		format  uint32
		relayed bool
	}{
		{"a keymap the keyboard relays", keymapFormatXkbV1, true},
		{"a seat without a keymap", keymapFormatNone, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ln, _ := wltest.LoopbackDisplay(t, "wayland-vinput-keymap")
			defer func() { _ = ln.Close() }()
			d, err := wl.Connect("")
			if err != nil {
				t.Fatal(err)
			}
			ctx := d.Context()
			defer func() { _ = ctx.Close() }()
			compositor, err := ln.AcceptUnix()
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = compositor.Close() }()
			keymap, err := os.CreateTemp(t.TempDir(), "keymap")
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = keymap.Close() }()
			if _, err := keymap.WriteString(text + "\x00"); err != nil {
				t.Fatal(err)
			}
			size := uint32(len(text) + 1)

			v := &Device{d: d, ctx: ctx, vk: wlr.NewZwpKeyboardV1(ctx)}
			keyboard := wl.NewKeyboard(ctx)
			wlclient.KeyboardAddListener(keyboard, &listener{v: v})
			event := keymapEvent(keyboard.Id(), tc.format, size)
			rights := syscall.UnixRights(int(keymap.Fd()))

			before := descriptors(t)
			for i := range keymapEvents {
				if _, _, err := compositor.WriteMsgUnix(event, rights, nil); err != nil {
					t.Fatal(err)
				}
				if err := ctx.Run(); err != nil {
					t.Fatalf("keymap event %d: %v", i, err)
				}
				if v.keymapErr != nil {
					t.Fatalf("keymap event %d: %v", i, v.keymapErr)
				}
				if v.keymapArmed != tc.relayed {
					t.Fatalf("keymap event %d armed the keyboard = %v, want %v", i, v.keymapArmed, tc.relayed)
				}
				if !tc.relayed {
					continue
				}
				if pending, err := ctx.Flush(); err != nil || pending {
					t.Fatalf("flush relay %d = pending %v, %v", i, pending, err)
				}
				if got := relayedKeymap(t, compositor, size); string(got) != text || string(v.keymap) != text {
					t.Fatalf("relay %d carries %q and the device keeps %q, want %q", i, got, v.keymap, text)
				}
			}
			if after := descriptors(t); !maps.Equal(before, after) {
				t.Errorf("%d keymap events changed the open descriptors\nbefore: %v\nafter:  %v", keymapEvents, before, after)
			}
		})
	}
}
