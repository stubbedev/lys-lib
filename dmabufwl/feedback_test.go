package dmabufwl_test

import (
	"slices"
	"testing"

	"github.com/stubbedev/lys-lib/dmabufwl"
	"github.com/stubbedev/lys-lib/fourcc"
	"github.com/stubbedev/lys-lib/third_party/wayland/wl"
	"github.com/stubbedev/lys-lib/wlr"
	"github.com/stubbedev/lys-lib/wltest"
)

const (
	mainDevice    = uint64(226<<8 | 128)
	scanoutDevice = uint64(226 << 8)
	tiled         = fourcc.Modifier(0x0100000000000001)
)

var (
	scanoutTranche = wltest.FeedbackTranche{Device: scanoutDevice, Flags: wlr.ZwpDmabufFeedbackV1TrancheFlagsScanout, Formats: []wltest.FormatModifier{{Format: fourcc.XRGB8888, Modifier: fourcc.Linear}}}
	renderTranche  = wltest.FeedbackTranche{Device: mainDevice, Formats: []wltest.FormatModifier{{Format: fourcc.XRGB8888, Modifier: tiled}, {Format: fourcc.XRGB8888, Modifier: fourcc.Linear}, {Format: fourcc.ARGB8888, Modifier: tiled}}}
)

type feedbackClient struct {
	display *wl.Display
	reg     *wl.Registry
	comp    *wl.Compositor
	dmabuf  *wlr.ZwpDmabufV1
}

func (c *feedbackClient) HandleRegistryGlobal(ev wl.RegistryGlobalEvent) {
	switch ev.Interface {
	case "wl_compositor":
		c.comp = wl.NewCompositor(c.display.Context())
		_ = c.reg.Bind(ev.Name, ev.Interface, 4, c.comp)
	case "zwp_linux_dmabuf_v1":
		c.dmabuf = wlr.NewZwpDmabufV1(c.display.Context())
		_ = c.reg.Bind(ev.Name, ev.Interface, 4, c.dmabuf)
	}
}

func (c *feedbackClient) HandleRegistryGlobalRemove(wl.RegistryGlobalRemoveEvent) {}

func (c *feedbackClient) roundtrip() error {
	cb, err := c.display.Sync()
	if err != nil {
		return err
	}
	return c.display.Context().RunTill(cb)
}

func connectFeedbackClient(t *testing.T) *feedbackClient {
	t.Helper()
	display, err := wl.Connect("")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = display.Context().Close() })
	c := &feedbackClient{display: display}
	if c.reg, err = display.GetRegistry(); err != nil {
		t.Fatal(err)
	}
	c.reg.AddGlobalHandler(c)
	if err := c.roundtrip(); err != nil {
		t.Fatal(err)
	}
	if c.comp == nil || c.dmabuf == nil {
		t.Fatal("the fake offers no wl_compositor or zwp_linux_dmabuf_v1")
	}
	return c
}

func TestASurfaceFeedbackReportsEachUpdateWithItsTrancheFlags(t *testing.T) {
	f := wltest.NewDmabufFeedback(t, mainDevice, scanoutTranche, renderTranche)
	c := connectFeedbackClient(t)
	surface, err := c.comp.CreateSurface()
	if err != nil {
		t.Fatal(err)
	}
	var got []dmabufwl.Feedback
	w, err := dmabufwl.WatchSurfaceFeedback(c.dmabuf, 4, surface, func(fb dmabufwl.Feedback, err error) {
		if err != nil {
			t.Errorf("feedback %d: %v", len(got), err)
		}
		got = append(got, fb)
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.roundtrip(); err != nil {
		t.Fatal(err)
	}
	f.SetFeedback(mainDevice, renderTranche)
	if err := c.roundtrip(); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("%d updates, want the first feedback and the change", len(got))
	}
	first, second := got[0], got[1]
	if first.Device != mainDevice || len(first.Tranches) != 2 || !first.Tranches[0].Scanout() || first.Tranches[0].Device != scanoutDevice || first.Tranches[1].Scanout() {
		t.Errorf("the first feedback is %+v, want a scanout tranche and then a render one", first)
	}
	if pick, ok := first.Preferred(fourcc.XRGB8888); !ok || !pick.Scanout() || !slices.Equal(pick.Modifiers[fourcc.XRGB8888], []fourcc.Modifier{fourcc.Linear}) {
		t.Errorf("XRGB8888 prefers %+v, %v, want the scanout tranche", pick, ok)
	}
	if pick, ok := first.Preferred(fourcc.ARGB8888); !ok || pick.Scanout() || pick.Device != mainDevice {
		t.Errorf("ARGB8888 prefers %+v, %v, want the render tranche", pick, ok)
	}
	if len(second.Tranches) != 1 || second.Tranches[0].Scanout() || !slices.Equal(second.Tranches[0].Modifiers[fourcc.XRGB8888], []fourcc.Modifier{tiled, fourcc.Linear}) {
		t.Errorf("the feedback without its scanout tranche is %+v", second)
	}
	if first.Equal(second) || !second.Equal(second) {
		t.Error("Equal does not tell the feedbacks apart")
	}
	if err := w.Destroy(); err != nil {
		t.Fatal(err)
	}
	if err := c.roundtrip(); err != nil {
		t.Fatal(err)
	}
	f.SetFeedback(mainDevice, scanoutTranche)
	if err := c.roundtrip(); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || f.Sent() != 2 {
		t.Errorf("after Destroy: %d updates, %d feedbacks sent", len(got), f.Sent())
	}
}

func TestTheDefaultFeedbackKeepsItsTranches(t *testing.T) {
	wltest.NewDmabufFeedback(t, mainDevice, scanoutTranche, renderTranche)
	c := connectFeedbackClient(t)
	fb, err := dmabufwl.DefaultFeedback(c.dmabuf, 4, c.roundtrip)
	if err != nil {
		t.Fatal(err)
	}
	if len(fb.Tranches) != 2 || !fb.Tranches[0].Scanout() {
		t.Errorf("tranches %+v", fb.Tranches)
	}
	if !slices.Equal(fb.Modifiers[fourcc.XRGB8888], []fourcc.Modifier{fourcc.Linear, tiled}) || !slices.Equal(fb.Modifiers[fourcc.ARGB8888], []fourcc.Modifier{tiled}) {
		t.Errorf("modifiers %v, want each format's across the tranches in order", fb.Modifiers)
	}
	if _, err := dmabufwl.WatchSurfaceFeedback(c.dmabuf, 3, nil, nil); err == nil {
		t.Error("a version 3 manager watched a surface's feedback")
	}
}
