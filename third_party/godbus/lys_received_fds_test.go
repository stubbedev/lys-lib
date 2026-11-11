package dbus

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"reflect"
	"testing"

	"golang.org/x/sys/unix"
)

// TestDescriptorsDecodeInsideEveryContainer is the received-descriptors
// patch (lys_received_fds.go): upstream built a dict or a nested array
// for UnixFDIndex and stored the UnixFD it had resolved into it, a
// reflect panic that Decode re-panicked. The first is the body any bus
// peer could send to kill the process.
func TestDescriptorsDecodeInsideEveryContainer(t *testing.T) {
	type entry struct {
		Name string
		FDs  map[string]UnixFD
	}
	for _, body := range []any{
		map[string]UnixFD{"a": 5},
		map[string][]UnixFD{"a": {5, 7}},
		[][]UnixFD{{5}, {7, 9}},
		map[UnixFD]string{5: "a"},
		map[string]map[string]UnixFD{"a": {"b": 5}},
		map[string]Variant{"fds": MakeVariant(map[string]UnixFD{"a": 5})},
	} {
		got, err := roundTrip(t, body)
		if err != nil {
			t.Errorf("%T: %v", body, err)
			continue
		}
		if len(got) != 1 || !reflect.DeepEqual(got[0], body) {
			t.Errorf("%T decoded as %#v", body, got)
		}
	}
	got, err := roundTrip(t, []entry{{Name: "n", FDs: map[string]UnixFD{"a": 5}}})
	want := [][]any{{"n", map[string]UnixFD{"a": 5}}}
	if err != nil || len(got) != 1 || !reflect.DeepEqual(got[0], want) {
		t.Errorf("a(sa{sh}) decoded as %#v, %v; want %#v", got, err, want)
	}
}

// Without the message's descriptors an 'h' stays the index the wire
// carries, in every container alike.
func TestDescriptorsStayIndicesWithoutTheMessagesDescriptors(t *testing.T) {
	for _, body := range []any{
		UnixFDIndex(3),
		[]UnixFDIndex{0, 1},
		map[string]UnixFDIndex{"a": 2},
	} {
		msg := &Message{Type: TypeMethodCall, Headers: map[HeaderField]Variant{
			FieldPath: MakeVariant(ObjectPath("/x")), FieldMember: MakeVariant("M"),
			FieldSignature: MakeVariant(SignatureOf(body)),
		}, Body: []any{body}}
		var buf bytes.Buffer
		if err := msg.EncodeTo(&buf, binary.LittleEndian); err != nil {
			t.Fatal(err)
		}
		got, err := DecodeMessage(&buf)
		if err != nil {
			t.Errorf("%T: %v", body, err)
			continue
		}
		if len(got.Body) != 1 || !reflect.DeepEqual(got.Body[0], body) {
			t.Errorf("%T decoded as %#v", body, got.Body)
		}
	}
}

// A dict keyed by any basic type decodes and encodes: upstream refused
// booleans and signatures, so an a{bs} from a peer ended the
// connection.
func TestEveryBasicTypeKeysADict(t *testing.T) {
	for _, body := range []any{
		map[bool]string{true: "yes", false: "no"},
		map[Signature]uint32{{"as"}: 1},
		map[ObjectPath]string{"/a": "a"},
		map[float64]string{1.5: "a"},
		map[byte]string{1: "a"},
	} {
		got, err := roundTrip(t, body)
		if err != nil {
			t.Errorf("%T: %v", body, err)
			continue
		}
		if len(got) != 1 || !reflect.DeepEqual(got[0], body) {
			t.Errorf("%T decoded as %#v", body, got)
		}
	}
	for _, sig := range []string{"a{vs}", "aa{vb}", "a{(s)s}", "a{ass}"} {
		if _, err := ParseSignature(sig); err == nil {
			t.Errorf("%q, a dict keyed by a container, parsed", sig)
		}
		dec := newDecoder(bytes.NewReader(make([]byte, 16)), binary.LittleEndian, nil)
		if vs, err := dec.Decode(Signature{sig}); err == nil {
			t.Errorf("%q, a dict keyed by a container, decoded as %#v", sig, vs)
		}
	}
}

func samePipe(t *testing.T, a, b int) bool {
	t.Helper()
	var sa, sb unix.Stat_t
	if err := errors.Join(unix.Fstat(a, &sa), unix.Fstat(b, &sb)); err != nil {
		t.Fatalf("descriptors %d and %d: %v", a, b, err)
	}
	return sa.Ino == sb.Ino
}

func openDescriptors(t testing.TB) int {
	t.Helper()
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	return len(entries) - 1
}

// A body naming one descriptor twice holds two descriptors, so that
// closing every UnixFD of a body closes each once.
func TestADescriptorNamedTwiceIsDecodedAsTwoDescriptors(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	before := openDescriptors(t)
	body := []byte{8, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}
	dec := newDecoder(bytes.NewReader(body), binary.LittleEndian, []int{int(w.Fd())})
	vs, err := dec.Decode(Signature{"ah"})
	if err != nil {
		t.Fatal(err)
	}
	fds, ok := vs[0].([]UnixFD)
	if !ok || len(fds) != 2 || fds[0] != UnixFD(w.Fd()) || fds[1] == fds[0] || fds[1] < 0 {
		t.Fatalf("decoded %#v from the pipe's descriptor %d named twice, want it and a duplicate", vs, w.Fd())
	}
	if !samePipe(t, int(fds[0]), int(fds[1])) {
		t.Errorf("descriptor %d is not a duplicate of %d", fds[1], fds[0])
	}
	if got := openDescriptors(t); got != before+1 {
		t.Errorf("%d descriptors open, want the %d before and the duplicate", got, before)
	}
	if err := unix.Close(int(fds[1])); err != nil {
		t.Error(err)
	}

	failing := append([]byte{12, 0, 0, 0}, 0, 0, 0, 0, 0, 0, 0, 0, 9, 0, 0, 0)
	dec = newDecoder(bytes.NewReader(failing), binary.LittleEndian, []int{int(w.Fd())})
	if vs, err := dec.Decode(Signature{"ah"}); err == nil {
		t.Errorf("an index past the descriptors decoded as %#v", vs)
	}
	if got := openDescriptors(t); got != before {
		t.Errorf("%d descriptors open after a body that failed to decode, want the %d before: its duplicate is nobody's", got, before)
	}
}

type panickingReader struct{ value any }

func (r panickingReader) Read([]byte) (int, error) { panic(r.value) }

// No panic leaves Decode: one that is not the decoder's own error costs
// the message, where upstream re-panicked it through the read loop.
func TestAPanicWhileDecodingIsThatMessagesError(t *testing.T) {
	var outOfRange []int
	for name, value := range map[string]any{
		"a reflect panic":  "reflect.Value.SetMapIndex: value of type dbus.UnixFD is not assignable to type dbus.UnixFDIndex",
		"a reflect error":  &reflect.ValueError{Method: "reflect.Value.Len", Kind: reflect.Int},
		"a runtime error":  func() (v any) { defer func() { v = recover() }(); return outOfRange[3] }(),
		"an unknown value": 7,
	} {
		dec := newDecoder(panickingReader{value}, binary.LittleEndian, nil)
		vs, err := dec.Decode(Signature{"s"})
		if _, invalid := err.(InvalidMessageError); !invalid || vs != nil {
			t.Errorf("%s: Decode = %#v, %#v; want an InvalidMessageError, which drops the message and keeps the connection", name, vs, err)
		}
	}
	for name, value := range map[string]error{
		"a format error":    FormatError("x"),
		"a reader's error":  os.ErrDeadlineExceeded,
		"a signature error": SignatureError{Sig: "x"},
	} {
		dec := newDecoder(panickingReader{value}, binary.LittleEndian, nil)
		if _, err := dec.Decode(Signature{"s"}); err != value {
			t.Errorf("%s: Decode = %#v, want it as it is", name, err)
		}
	}
	dec := newDecoder(panickingReader{io.EOF}, binary.LittleEndian, nil)
	if _, err := dec.Decode(Signature{"s"}); err != FormatError("unexpected EOF") {
		t.Errorf("end of input: Decode = %#v", err)
	}
}

// A header that does not decode leaves the body unread, so it must end
// the connection: skipping the message would read the body as the next.
func TestAnUndecodableHeaderIsNotASkippedMessage(t *testing.T) {
	if _, skipped := headerError(InvalidMessageError("x")).(InvalidMessageError); skipped {
		t.Error("an invalid header is an InvalidMessageError, for which the read loop reads on")
	}
	if err := headerError(io.ErrClosedPipe); err != io.ErrClosedPipe {
		t.Errorf("headerError changed %v into %v", io.ErrClosedPipe, err)
	}
}

// The specification allows no empty struct, and decoding an array of
// them never ended: no element takes a byte of the array's length.
func TestAnEmptyStructIsNoSignature(t *testing.T) {
	for _, sig := range []string{"()", "a()", "(i())", "a{s()}", "v()"} {
		if _, err := ParseSignature(sig); err == nil {
			t.Errorf("%q parsed", sig)
		}
	}
	if _, err := ParseSignature("(i)a(s)"); err != nil {
		t.Error(err)
	}
	body := []byte{0, 0, 0, 4, 0, 0, 0, 0}
	dec := newDecoder(bytes.NewReader(body), binary.BigEndian, nil)
	if vs, err := dec.Decode(Signature{"a()"}); err == nil {
		t.Errorf("an array of empty structs decoded as %#v", vs)
	}
}

// Lengths that sum past 2^32 wrapped below the message size limit.
func TestLengthsNearTheWrapAreTooLong(t *testing.T) {
	frame := []byte{'l', byte(TypeSignal), 0, protoVersion, 0x20, 0, 0, 0, 1, 0, 0, 0, 0xf0, 0xff, 0xff, 0xff}
	_, err := DecodeMessage(bytes.NewReader(frame))
	if err != InvalidMessageError("message is too long") {
		t.Errorf("DecodeMessage = %v, want the message refused as too long", err)
	}
}

// A NameLost or NameAcquired without its name is ignored: on a
// connection to a peer the peer picks sender and body, and upstream
// panicked on these.
func TestAnOwnershipSignalWithoutItsNameIsIgnored(t *testing.T) {
	for _, body := range [][]any{nil, {}, {uint32(1)}} {
		if name, ok := firstString(body); ok {
			t.Errorf("firstString(%#v) = %q", body, name)
		}
	}
	if name, ok := firstString([]any{"n", "m"}); !ok || name != "n" {
		t.Errorf("firstString = %q, %v", name, ok)
	}
	for _, member := range []string{"NameLost", "NameAcquired"} {
		conn := &Conn{names: newNameTracker(), signalHandler: NewDefaultSignalHandler()}
		conn.handleSignal(1, &Message{Type: TypeSignal, Headers: map[HeaderField]Variant{
			FieldPath:      MakeVariant(ObjectPath("/org/freedesktop/DBus")),
			FieldInterface: MakeVariant("org.freedesktop.DBus"),
			FieldMember:    MakeVariant(member),
			FieldSender:    MakeVariant("org.freedesktop.DBus"),
		}})
	}
}
