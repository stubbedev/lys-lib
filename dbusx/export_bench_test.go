package dbusx

import (
	"fmt"
	"runtime"
	"testing"

	"github.com/stubbedev/lys-lib/dbustest"
	dbus "github.com/stubbedev/lys-lib/third_party/godbus"
)

type benchItem struct{}

func (benchItem) Delete() (dbus.ObjectPath, *dbus.Error)                    { return "/", nil }
func (benchItem) GetSecret(dbus.ObjectPath) (string, *dbus.Error)           { return "", nil }
func (benchItem) SetSecret(string, []byte, string) *dbus.Error              { return nil }
func (benchItem) Lock(a []dbus.ObjectPath) ([]dbus.ObjectPath, *dbus.Error) { return a, nil }

func BenchmarkExportItems(b *testing.B) {
	bus := dbustest.Start(b)
	conn := bus.Conn(b)
	getters := Getters{"Label": func() any { return "x" }, "Locked": func() any { return false }, "Created": func() any { return uint64(1) }}
	n := 0
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	b.ReportAllocs()
	for b.Loop() {
		n++
		path := dbus.ObjectPath(fmt.Sprintf("/org/freedesktop/secrets/collection/login/%d", n))
		if _, err := Export(conn, path, Interface{Name: "org.freedesktop.Secret.Item", Methods: &benchItem{}, Properties: getters}); err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	runtime.GC()
	runtime.ReadMemStats(&after)
	b.ReportMetric(float64(after.HeapAlloc-before.HeapAlloc)/float64(n), "heap/object")
}
