//go:build linux

package dbus

import (
	"bytes"
	"encoding/binary"
	"io"
	"testing"

	"golang.org/x/sys/unix"
)

// storeTargets are places a receiver stores a decoded argument: what an
// exported method's parameters and a reply's destinations look like.
func storeTargets() []any {
	type pair struct {
		Key   string
		Value Variant
	}
	type handle struct {
		FD   UnixFD
		Note string
	}
	return []any{
		new(any), new(Variant), new(string), new(uint32), new(bool), new(float64), new(ObjectPath), new(Signature),
		new(UnixFD), new([]UnixFD), new(map[string]UnixFD), new(map[UnixFD]string),
		new([]byte), new([]string), new([]any), new([]Variant), new([][]any), new([4]uint32),
		new(map[string]Variant), new(map[string]any), new(map[string]string), new(map[uint32][]string), new(map[bool]Variant),
		new(pair), new([]pair), new(map[string]pair), new(handle), new([]handle), new(*handle), new(struct{}),
	}
}

func encodedBody(t testing.TB, order binary.ByteOrder, vs ...any) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := newEncoder(&buf, order, nil).Encode(vs...); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// FuzzDecodeBodyWithDescriptors decodes message bodies a peer chose,
// with descriptors attached, and does with each what a receiver does:
// store its arguments, take its signature, encode it again and close
// its descriptors. None of it may panic, whatever the bytes: the read
// loop and every method call run it on what a peer sent. And every
// descriptor that came with the body is the decoded body's or left to
// the caller, with no duplicate outliving a body that failed.
func FuzzDecodeBodyWithDescriptors(f *testing.F) {
	type handle struct {
		FD   UnixFDIndex
		Note string
	}
	for _, seed := range []struct {
		fds  uint8
		body any
	}{
		{1, map[string]UnixFDIndex{"a": 0}},
		{2, map[string][]UnixFDIndex{"a": {0, 1}}},
		{2, [][]UnixFDIndex{{0}, {1, 1}}},
		{1, map[UnixFDIndex]string{0: "a"}},
		{3, []UnixFDIndex{0, 0, 2, 9}},
		{1, MakeVariant(map[string]Variant{"fd": MakeVariant(UnixFDIndex(0))})},
		{2, []handle{{0, "a"}, {1, "b"}}},
		{0, UnixFDIndex(0)},
		{0, map[bool]string{true: "a"}},
		{0, map[Signature]Variant{{"a{sv}"}: MakeVariant([]string{"s"})}},
		{0, map[string]map[string]Variant{"i": {"p": MakeVariant(uint32(1)), "q": MakeVariant(ObjectPath("/o"))}}},
		{0, []any{"s", uint32(1), []byte{1, 2}, 1.5, int16(-1), uint64(1), true}},
	} {
		f.Add(SignatureOf(seed.body).String(), encodedBody(f, binary.LittleEndian, seed.body), false, seed.fds)
		f.Add(SignatureOf(seed.body).String(), encodedBody(f, binary.BigEndian, seed.body), true, seed.fds)
	}
	f.Add("aa{vb}", make([]byte, 4), false, uint8(3))
	f.Add("a(yv)", []byte{8, 0, 0, 0, 0, 0, 0, 0, 1, 1, 'h', 0, 0, 0, 0, 0}, false, uint8(1))

	null, err := unix.Open("/dev/null", unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		f.Fatal(err)
	}
	f.Cleanup(func() { _ = unix.Close(null) })

	f.Fuzz(func(t *testing.T, signature string, body []byte, bigEndian bool, attached uint8) {
		sig, err := ParseSignature(signature)
		if err != nil {
			return
		}
		var order binary.ByteOrder = binary.LittleEndian
		if bigEndian {
			order = binary.BigEndian
		}
		before := openDescriptors(t)
		fds := make([]int, attached%4)
		for i := range fds {
			if fds[i], err = unix.FcntlInt(uintptr(null), unix.F_DUPFD_CLOEXEC, 0); err != nil {
				t.Fatal(err)
			}
		}
		dec := newDecoder(bytes.NewReader(body), order, fds)
		vs, err := dec.Decode(sig)
		if err != nil {
			if vs != nil {
				t.Errorf("Decode failed with %v and returned %#v", err, vs)
			}
			for _, fd := range fds {
				if err := unix.Close(fd); err != nil {
					t.Errorf("descriptor %d of a body that failed to decode: %v", fd, err)
				}
			}
		} else {
			dec.closeUnreferencedFDs()
			for _, v := range vs {
				for _, target := range storeTargets() {
					_ = Store([]any{v}, target)
				}
			}
			_ = SignatureOf(vs...)
			_ = newEncoder(io.Discard, order, nil).Encode(vs...)
			if err := CloseFDs(vs); err != nil {
				t.Errorf("closing the descriptors of %#v: %v", vs, err)
			}
		}
		if after := openDescriptors(t); after != before {
			t.Errorf("%d descriptors open after %q with %d attached, %d before", after, signature, len(fds), before)
		}
	})
}
