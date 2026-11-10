package dbus

import (
	"reflect"
	"testing"
)

// TestMapFDsReachesEveryDescriptorOfADecodedBody is the walk the
// per-receiver duplicates and CloseFDs share: every shape decode
// yields, with what holds a changed descriptor copied and the rest,
// like the original, left as it was.
func TestMapFDsReachesEveryDescriptorOfADecodedBody(t *testing.T) {
	plus100 := func(fd UnixFD) UnixFD { return fd + 100 }
	shared := []string{"untouched"}
	tests := []struct {
		name     string
		in, want any
	}{
		{"h", UnixFD(1), UnixFD(101)},
		{"ah", []UnixFD{1, 2}, []UnixFD{101, 102}},
		{"(hs)", []any{UnixFD(1), "s"}, []any{UnixFD(101), "s"}},
		{"v", MakeVariant(UnixFD(1)), MakeVariant(UnixFD(101))},
		{"a{sv}", map[string]Variant{"fd": MakeVariant(UnixFD(1)), "n": MakeVariant(uint32(1))}, map[string]Variant{"fd": MakeVariant(UnixFD(101)), "n": MakeVariant(uint32(1))}},
		{"v of a{sv}", MakeVariant(map[string]Variant{"fd": MakeVariant(UnixFD(1))}), MakeVariant(map[string]Variant{"fd": MakeVariant(UnixFD(101))})},
		{"a(hs)", [][]any{{UnixFD(1), "a"}, {UnixFD(2), "b"}}, [][]any{{UnixFD(101), "a"}, {UnixFD(102), "b"}}},
		{"av", []Variant{MakeVariant(UnixFD(1)), MakeVariant("s")}, []Variant{MakeVariant(UnixFD(101)), MakeVariant("s")}},
		{"a{hs}", map[UnixFD]string{1: "a", 2: "b"}, map[UnixFD]string{101: "a", 102: "b"}},
		{"a{sah}", map[string][]UnixFD{"fds": {1, 2}}, map[string][]UnixFD{"fds": {101, 102}}},
		{"a{s(ash)}", map[string][]any{"k": {shared, UnixFD(1)}}, map[string][]any{"k": {shared, UnixFD(101)}}},
	}
	for _, tc := range tests {
		before := reflect.ValueOf(tc.in)
		got, changed := mapFDs(tc.in, plus100)
		if !changed || !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: mapFDs = %#v, %v; want %#v", tc.name, got, changed, tc.want)
		}
		if again, changed := mapFDs(tc.in, plus100); !reflect.DeepEqual(again, tc.want) || !changed {
			t.Errorf("%s: the first walk wrote to its argument: a second gives %#v", tc.name, again)
		}
		if same, changed := mapFDs(tc.in, func(fd UnixFD) UnixFD { return fd }); changed || !sameValue(before, reflect.ValueOf(same)) {
			t.Errorf("%s: a walk that changes nothing returned a copy", tc.name)
		}
	}
	for _, v := range []any{"s", uint32(1), []byte{1}, []string{"a"}, map[string]string{"a": "b"}, ObjectPath("/"), UnixFDIndex(3), MakeVariant("s"), nil} {
		if got, changed := mapFDs(v, plus100); changed || !reflect.DeepEqual(got, v) {
			t.Errorf("%T holds no descriptor, yet mapFDs = %#v, %v", v, got, changed)
		}
	}
}

func sameValue(a, b reflect.Value) bool {
	switch a.Kind() {
	case reflect.Slice, reflect.Map:
		return a.Pointer() == b.Pointer()
	}
	return reflect.DeepEqual(a.Interface(), b.Interface())
}

// TestClosingTheDescriptorsOfABodyWithoutAnyAllocatesNothing: a watch
// sees every signal of its connection and closes what it does not
// route, so the walk over an fd-less body must cost nothing.
func TestClosingTheDescriptorsOfABodyWithoutAnyAllocatesNothing(t *testing.T) {
	bodies := [][]any{
		{"org.example.Iface", map[string]Variant{"Active": MakeVariant(true), "Name": MakeVariant("n")}, []string{"Gone"}},
		{uint32(1), []any{"s", ObjectPath("/o")}, []byte{1, 2}},
		{"name", "", ":1.5"},
	}
	wire := &Signal{Body: []any{map[string]map[string]Variant{"i": {"p": MakeVariant(uint32(1))}}}, bare: true}
	allocs := testing.AllocsPerRun(100, func() {
		for _, body := range bodies {
			if err := CloseFDs(body); err != nil {
				t.Error(err)
			}
		}
		if err := wire.CloseFDs(); err != nil {
			t.Error(err)
		}
	})
	if allocs != 0 {
		t.Errorf("CloseFDs allocated %v times over bodies that hold no descriptor, want none", allocs)
	}
}
