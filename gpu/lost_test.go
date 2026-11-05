package gpu

import (
	"errors"
	"image"
	"image/color"
	"reflect"
	"testing"
	"unsafe"

	"github.com/ebitengine/purego"

	"github.com/stubbedev/lys-lib/third_party/vk"
)

// loseDevice swaps the driver's entry points that report a lost device
// for ones that report it, until the test ends. A driver loses a device
// only on a GPU fault: lavapipe has none to induce.
func loseDevice(t *testing.T, d *Device, entries ...string) {
	t.Helper()
	lost := purego.NewCallback(func() int32 { return int32(vk.ErrorDeviceLost) })
	cmds := reflect.ValueOf(d.cmds).Elem()
	for _, name := range entries {
		field := cmds.FieldByName(name)
		slot := (*unsafe.Pointer)(unsafe.Pointer(field.UnsafeAddr()))
		real := *slot
		*slot = *(*unsafe.Pointer)(unsafe.Pointer(&lost))
		t.Cleanup(func() { *slot = real })
	}
}

func TestALostDeviceIsAnErrorCallersRecognise(t *testing.T) {
	for _, tc := range []struct {
		name string
		call func(d *Device, tg *Target) error
	}{
		{"End", func(d *Device, tg *Target) error {
			p, err := d.Begin(tg, []image.Rectangle{{Max: tg.Size()}})
			if err != nil {
				return err
			}
			p.Clear(color.RGBA{A: 0xff})
			if err := p.End(); err != nil {
				return err
			}
			return d.Flush()
		}},
		{"Finish", func(d *Device, _ *Target) error { return d.Finish() }},
		{"SyncFile", func(d *Device, _ *Target) error {
			_, err := d.SyncFile()
			return err
		}},
		{"WaitOldest", func(d *Device, _ *Target) error { return d.waitOldest() }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := openDevice(t)
			if tc.name == "SyncFile" && !d.SyncFiles() {
				t.Skipf("%s has no sync_file semaphores", d.Name())
			}
			tg, err := d.NewOffscreen(8, 8)
			if err != nil {
				t.Fatal(err)
			}
			runPass(t, d, tg, []image.Rectangle{{Max: tg.Size()}}, func(p *Pass) { p.Clear(color.RGBA{A: 0xff}) })
			if err := d.Flush(); err != nil {
				t.Fatal(err)
			}
			loseDevice(t, d, "queueSubmit", "queueSubmit2", "queueWaitIdle", "waitForFences", "getFenceStatus", "getSemaphoreFdKHR")
			if err := tc.call(d, tg); !errors.Is(err, ErrDeviceLost) {
				t.Fatalf("%v, want ErrDeviceLost", err)
			}
		})
	}
}

func TestAFencePollFindingALostDeviceFailsTheNextSubmission(t *testing.T) {
	d := openDevice(t)
	tg, err := d.NewOffscreen(8, 8)
	if err != nil {
		t.Fatal(err)
	}
	runPass(t, d, tg, []image.Rectangle{{Max: tg.Size()}}, func(p *Pass) { p.Clear(color.RGBA{A: 0xff}) })
	if err := d.Flush(); err != nil {
		t.Fatal(err)
	}
	loseDevice(t, d, "getFenceStatus")
	d.reap(false)
	p, err := d.Begin(tg, []image.Rectangle{{Max: tg.Size()}})
	if err != nil {
		t.Fatal(err)
	}
	p.Clear(color.RGBA{A: 0xff})
	if err := p.End(); !errors.Is(err, ErrDeviceLost) {
		t.Fatalf("a pass after a poll found the device lost: %v, want ErrDeviceLost", err)
	}
}

func TestAnotherVulkanResultIsNotALostDevice(t *testing.T) {
	if errors.Is(resultError(vk.ErrorOutOfDeviceMemory), ErrDeviceLost) {
		t.Fatal("out of device memory matches ErrDeviceLost")
	}
	if !errors.Is(resultError(vk.ErrorDeviceLost), ErrDeviceLost) {
		t.Fatal("VK_ERROR_DEVICE_LOST does not match ErrDeviceLost")
	}
}
