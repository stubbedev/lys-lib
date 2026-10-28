package dbusx_test

import (
	"encoding/xml"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stubbedev/lys-lib/dbustest"
	"github.com/stubbedev/lys-lib/dbusx"
	dbus "github.com/stubbedev/lys-lib/third_party/godbus"
	"github.com/stubbedev/lys-lib/third_party/godbus/introspect"
)

type moved struct {
	Handle string   `dbus:"handle"`
	X      int32    `dbus:"x"`
	Y      int32    `dbus:"y"`
	Tags   []string `dbus:"tags"`
	Pair   pair     `dbus:"pair"`
	Note   string
	Skip   string `dbus:"-"`
	hidden string
}

type pair struct {
	Key   string
	Value uint32
}

type ended struct{}

var (
	movedSignal = dbusx.NewSignal[moved](iface, "Moved")
	endedSignal = dbusx.NewSignal[ended](iface, "Ended")
)

func TestSignalsAreIntrospected(t *testing.T) {
	bus := dbustest.Start(t)
	server, client := bus.Conn(t), bus.Conn(t)
	unexport, err := dbusx.Export(server, path, dbusx.Interface{
		Name: iface, Methods: echo{}, Signals: []dbusx.SignalDecl{movedSignal, endedSignal},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer unexport()
	var data string
	if err := client.Object(server.Names()[0], path).Call("org.freedesktop.DBus.Introspectable.Introspect", 0).Store(&data); err != nil {
		t.Fatal(err)
	}
	var node introspect.Node
	if err := xml.Unmarshal([]byte(data), &node); err != nil {
		t.Fatalf("introspection is not XML: %v\n%s", err, data)
	}
	want := []introspect.Signal{
		{Name: "Moved", Args: []introspect.Arg{
			{Name: "handle", Type: "s"},
			{Name: "x", Type: "i"},
			{Name: "y", Type: "i"},
			{Name: "tags", Type: "as"},
			{Name: "pair", Type: "(su)"},
			{Type: "s"},
		}},
		{Name: "Ended"},
	}
	for _, i := range node.Interfaces {
		if i.Name != iface {
			if len(i.Signals) != 0 {
				t.Errorf("%s has signals %+v", i.Name, i.Signals)
			}
			continue
		}
		if !reflect.DeepEqual(i.Signals, want) {
			t.Errorf("signals = %+v, want %+v", i.Signals, want)
		}
		return
	}
	t.Fatalf("introspection lacks %s:\n%s", iface, data)
}

func TestExportRefusesASignalOfAnotherInterface(t *testing.T) {
	_, err := dbusx.Export(dbustest.Start(t).Conn(t), path, dbusx.Interface{
		Name: iface, Methods: echo{}, Signals: []dbusx.SignalDecl{dbusx.NewSignal[ended]("dev.stubbe.lys.Other1", "Ended")},
	})
	if err == nil || !strings.Contains(err.Error(), "dev.stubbe.lys.Other1.Ended") {
		t.Errorf("Export = %v, want an error naming the foreign signal", err)
	}
}

func TestNewSignalRefusesANonStruct(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("a signal carrying a string was declared")
		}
	}()
	dbusx.NewSignal[string](iface, "Bad")
}

func TestSignalEmitsAndDecodes(t *testing.T) {
	bus := dbustest.Start(t)
	server, client := bus.Conn(t), bus.Conn(t)
	signals := make(chan *dbus.Signal, 4)
	client.Signal(signals)
	if err := client.AddMatchSignal(dbus.WithMatchInterface(iface)); err != nil {
		t.Fatal(err)
	}
	sent := moved{Handle: "/r/1", X: 3, Y: -4, Tags: []string{"a"}, Pair: pair{"k", 7}, Note: "n", Skip: "dropped", hidden: "dropped"}
	if err := movedSignal.Emit(server, path, sent); err != nil {
		t.Fatal(err)
	}
	if err := endedSignal.Emit(server, path, ended{}); err != nil {
		t.Fatal(err)
	}
	sent.Skip, sent.hidden = "", ""
	got := receive(t, signals)
	if got.Name != movedSignal.Name() {
		t.Fatalf("received %s", got.Name)
	}
	if v, err := movedSignal.Decode(got); err != nil || !reflect.DeepEqual(v, sent) {
		t.Errorf("Decode = %+v, %v; want %+v", v, err, sent)
	}
	if _, err := endedSignal.Decode(got); err == nil {
		t.Error("Moved decoded as Ended")
	}
	if _, err := endedSignal.Decode(receive(t, signals)); err != nil {
		t.Errorf("Decode Ended: %v", err)
	}
}

func TestSignalDecodeRefusesAnotherBody(t *testing.T) {
	name := movedSignal.Name()
	good := []any{"/r/1", int32(1), int32(2), []string{}, []any{"k", uint32(7)}, "n"}
	for _, body := range [][]any{
		nil,
		good[:5],
		append(append([]any{}, good...), "extra"),
		{"/r/1", uint32(1), int32(2), []string{}, []any{"k", uint32(7)}, "n"},
		{"/r/1", int32(1), int32(2), []int32{}, []any{"k", uint32(7)}, "n"},
		{"/r/1", int32(1), int32(2), []string{}, []any{"k", "seven"}, "n"},
		{"/r/1", int32(1), int32(2), []string{}, []any{"k"}, "n"},
	} {
		if v, err := movedSignal.Decode(&dbus.Signal{Name: name, Body: body}); err == nil {
			t.Errorf("decoded %#v as %+v", body, v)
		}
	}
	if _, err := movedSignal.Decode(&dbus.Signal{Name: name, Body: good}); err != nil {
		t.Errorf("decode the well-formed body: %v", err)
	}
}

func receive(t *testing.T, signals <-chan *dbus.Signal) *dbus.Signal {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case sig := <-signals:
			if sig.Path == path {
				return sig
			}
		case <-timeout:
			t.Fatalf("no signal at %s within 5s", path)
			return nil
		}
	}
}
