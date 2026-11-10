package dbus_test

import (
	"errors"
	"io"
	"os"
	"slices"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	dbus "github.com/stubbedev/lys-lib/third_party/godbus"
)

const (
	handIface  = "dev.stubbe.lys.Hand"
	handSignal = handIface + ".Hand"
	handPath   = dbus.ObjectPath("/dev/stubbe/lys/Hand")
	handWait   = 5 * time.Second
)

func openDescriptors(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	return len(entries) - 1
}

func inode(t *testing.T, fd int) uint64 {
	t.Helper()
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		t.Fatalf("descriptor %d is not open: %v", fd, err)
	}
	return st.Ino
}

type handPipe struct {
	r, w *os.File
}

func newHandPipe(t *testing.T) handPipe {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = r.Close()
		_ = w.Close()
	})
	return handPipe{r: r, w: w}
}

func (p handPipe) fd() dbus.UnixFD { return dbus.UnixFD(p.w.Fd()) }

func (p handPipe) wantEveryCopyClosed(t *testing.T, what string) {
	t.Helper()
	if err := p.w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := p.r.SetReadDeadline(time.Now().Add(handWait)); err != nil {
		t.Fatal(err)
	}
	if n, err := p.r.Read(make([]byte, 1)); n != 0 || !errors.Is(err, io.EOF) {
		t.Errorf("%s: read the pipe = %d, %v; want end of file, every copy of its write end closed", what, n, err)
	}
}

type handed struct {
	FD   dbus.UnixFD
	Note string
}

func emitHand(t *testing.T, server *dbus.Conn, fd dbus.UnixFD) {
	t.Helper()
	if err := server.Emit(handPath, handSignal,
		fd,
		[]dbus.UnixFD{fd, fd},
		handed{FD: fd, Note: "struct"},
		dbus.MakeVariant(fd),
		map[string]dbus.Variant{"fd": dbus.MakeVariant(fd), "n": dbus.MakeVariant(uint32(7))},
		[]handed{{FD: fd, Note: "array of structs"}},
	); err != nil {
		t.Fatal(err)
	}
}

const handDescriptors = 7

func descriptorsOf(t *testing.T, sig *dbus.Signal) []int {
	t.Helper()
	if sig.Name != handSignal || len(sig.Body) != 6 {
		t.Fatalf("received %s carrying %d arguments", sig.Name, len(sig.Body))
	}
	var fds []int
	add := func(v any) {
		fd, ok := v.(dbus.UnixFD)
		if !ok || fd < 0 {
			t.Fatalf("%#v in %#v is not a descriptor", v, sig.Body)
		}
		fds = append(fds, int(fd))
	}
	add(sig.Body[0])
	for _, fd := range sig.Body[1].([]dbus.UnixFD) {
		add(fd)
	}
	add(sig.Body[2].([]any)[0])
	add(sig.Body[3].(dbus.Variant).Value())
	add(sig.Body[4].(map[string]dbus.Variant)["fd"].Value())
	add(sig.Body[5].([][]any)[0][0])
	return fds
}

func awaitHand(t *testing.T, ch <-chan *dbus.Signal) *dbus.Signal {
	t.Helper()
	select {
	case sig, ok := <-ch:
		if !ok {
			t.Fatal("the signal channel closed")
		}
		return sig
	case <-time.After(handWait):
		t.Fatalf("no %s within %s", handSignal, handWait)
		return nil
	}
}

// TestEachSignalChannelOwnsItsDescriptors is the descriptors patch
// (lys_signal_fds.go): upstream hands one Signal, and so the same
// descriptors, to every channel of a connection, where two readers
// close one descriptor twice or use it after the other closed it.
func TestEachSignalChannelOwnsItsDescriptors(t *testing.T) {
	server, client := pairedConns(t)
	if err := client.AddMatchSignal(dbus.WithMatchInterface(handIface)); err != nil {
		t.Fatal(err)
	}
	channels := []chan *dbus.Signal{make(chan *dbus.Signal, 1), make(chan *dbus.Signal, 1), make(chan *dbus.Signal, 1)}
	for _, ch := range channels {
		client.Signal(ch)
	}
	pipe := newHandPipe(t)
	want := inode(t, int(pipe.fd()))
	before := openDescriptors(t)

	emitHand(t, server, pipe.fd())
	received := make([]*dbus.Signal, len(channels))
	var all []int
	for i, ch := range channels {
		received[i] = awaitHand(t, ch)
		fds := descriptorsOf(t, received[i])
		if len(fds) != handDescriptors {
			t.Fatalf("channel %d got %d descriptors, want %d", i, len(fds), handDescriptors)
		}
		for _, fd := range fds {
			if got := inode(t, fd); got != want {
				t.Errorf("channel %d: descriptor %d is inode %d, want the pipe's %d", i, fd, got, want)
			}
		}
		all = append(all, fds...)
	}
	slices.Sort(all)
	if distinct := slices.Compact(slices.Clone(all)); len(distinct) != len(all) {
		t.Fatalf("the channels were handed %v: a descriptor reached two places", all)
	}
	if got := openDescriptors(t); got != before+len(all) {
		t.Errorf("%d descriptors open with the signal delivered, want the %d before and one per descriptor handed out (%d)", got, before, len(all))
	}

	if err := received[0].CloseFDs(); err != nil {
		t.Errorf("the first channel closing its descriptors: %v", err)
	}
	for _, fd := range descriptorsOf(t, received[1]) {
		if err := unix.Close(fd); err != nil {
			t.Errorf("the second channel closing descriptor %d: %v", fd, err)
		}
	}
	if err := dbus.CloseFDs(received[2].Body); err != nil {
		t.Errorf("the third channel closing its descriptors: %v", err)
	}
	if got := openDescriptors(t); got != before {
		t.Errorf("%d descriptors open once every channel closed its own, want the %d before", got, before)
	}
	pipe.wantEveryCopyClosed(t, "each channel closed its copies")
}

// A signal no channel takes, because none is registered or because
// its channel was removed with the signal still waiting to be read,
// has its descriptors closed: nobody else could.
func TestDescriptorsOfASignalNoChannelTakesAreClosed(t *testing.T) {
	server, client := pairedConns(t)
	if err := client.AddMatchSignal(dbus.WithMatchInterface(handIface)); err != nil {
		t.Fatal(err)
	}
	unread := newHandPipe(t)
	emitHand(t, server, unread.fd())
	unread.wantEveryCopyClosed(t, "a connection with no signal channel")

	stuck, reader := make(chan *dbus.Signal), make(chan *dbus.Signal, 1)
	client.Signal(stuck)
	client.Signal(reader)
	removed := newHandPipe(t)
	emitHand(t, server, removed.fd())
	if err := awaitHand(t, reader).CloseFDs(); err != nil {
		t.Fatal(err)
	}
	client.RemoveSignal(stuck)
	removed.wantEveryCopyClosed(t, "a channel removed with the signal undelivered")
}

// A signal without descriptors is still one Signal for every channel:
// nothing is copied for it.
func TestASignalWithoutDescriptorsIsSharedAsUpstream(t *testing.T) {
	server, client := pairedConns(t)
	if err := client.AddMatchSignal(dbus.WithMatchInterface(handIface)); err != nil {
		t.Fatal(err)
	}
	first, second := make(chan *dbus.Signal, 1), make(chan *dbus.Signal, 1)
	client.Signal(first)
	client.Signal(second)
	if err := server.Emit(handPath, handIface+".Plain", "no descriptor"); err != nil {
		t.Fatal(err)
	}
	a, b := awaitPlain(t, first), awaitPlain(t, second)
	if a != b {
		t.Error("a signal without descriptors was copied per channel")
	}
	if err := a.CloseFDs(); err != nil {
		t.Errorf("CloseFDs of a signal without descriptors = %v", err)
	}
}

func awaitPlain(t *testing.T, ch <-chan *dbus.Signal) *dbus.Signal {
	t.Helper()
	select {
	case sig := <-ch:
		return sig
	case <-time.After(handWait):
		t.Fatalf("no signal within %s", handWait)
		return nil
	}
}
