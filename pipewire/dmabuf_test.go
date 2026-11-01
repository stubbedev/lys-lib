package pipewire

import (
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-lib/fourcc"
	"github.com/stubbedev/lys-lib/pipewire/pwtest"
)

func TestObjectPropFindsTheModifier(t *testing.T) {
	mod := fourcc.Invalid
	if objectProp(formatPod(8, 8, 30, VideoBGRx, &mod), formatVideoModifier) == nil {
		t.Error("a dmabuf format lost its modifier")
	}
	if objectProp(formatPod(8, 8, 30, VideoBGRx, nil), formatVideoModifier) != nil {
		t.Error("a plain format claims a modifier")
	}
	if objectProp(formatPod(8, 8, 30, VideoBGRx, nil), formatVideoRate) == nil {
		t.Error("the last property was not found")
	}
	if objectProp([]byte{1, 2, 3}, formatVideoModifier) != nil || objectProp(podInt(1).bytes(), formatVideoModifier) != nil {
		t.Error("garbage or a non-object carries a modifier")
	}
}

// A format's size reads back as written; a pod without one has none.
func TestFormatSize(t *testing.T) {
	if w, h, ok := formatSize(formatPod(640, 360, 30, VideoBGRx, nil)); !ok || w != 640 || h != 360 {
		t.Errorf("formatSize = %d x %d, %v", w, h, ok)
	}
	// Fixation leaves the size a None choice of one rectangle.
	fixated := pod{typeChoice, append(append(le32(choiceNone), le32(0)...), append(append(le32(8), le32(typeRectangle)...), append(le32(1280), le32(720)...)...)...)}
	if w, h, ok := formatSize(object(objectFormat, paramFormat, prop{key: formatVideoSize, value: fixated})); !ok || w != 1280 || h != 720 {
		t.Errorf("a fixated size = %d x %d, %v", w, h, ok)
	}
	noSize := object(objectFormat, paramFormat, prop{key: formatVideoFormat, value: podID(uint32(VideoBGRx))})
	if _, _, ok := formatSize(noSize); ok {
		t.Error("a format without a size has one")
	}
}

// memDmabuf poses a memfd as a GPU buffer: PipeWire passes the fd
// through whatever its kind.
type memDmabuf struct {
	fd       int
	released *atomic.Int32
}

func (m memDmabuf) Fd() int        { return m.fd }
func (m memDmabuf) Offset() uint32 { return 0 }
func (m memDmabuf) Stride() uint32 { return 256 }
func (m memDmabuf) Release()       { _ = unix.Close(m.fd); m.released.Add(1) }

// gpuSource is a DmabufSource over memDmabufs, counting what it did.
type gpuSource struct {
	solid
	allocs, fills, released atomic.Int32
}

func (g *gpuSource) Modifier() fourcc.Modifier { return fourcc.Linear }

func (g *gpuSource) DmabufVideoFormat() VideoFormat { return VideoBGRx }

func (g *gpuSource) AllocDmabuf() (Dmabuf, error) {
	fd, _, err := memfd(testNode, 256*32)
	if err != nil {
		return nil, err
	}
	g.allocs.Add(1)
	return memDmabuf{fd: fd, released: &g.released}, nil
}

func (g *gpuSource) FillDmabuf(Dmabuf) ([]Rect, bool) {
	g.fills.Add(1)
	return nil, true
}

// streamTo runs src into a consumer offering modifier (nil: the plain
// format) and returns the first non-empty buffer it received.
func streamTo(t *testing.T, src Source, modifier *fourcc.Modifier) (consumed, *Producer) {
	t.Helper()
	pwtest.Start(t)
	p, err := Start(Config{Application: testApplication, Name: testNode, Width: 64, Height: 32, Stride: 256, Format: VideoBGRx, FPS: 30, Source: src})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	l, _ := load()
	loop, err := startLoop(l)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(loop.stop)
	c := newConsumerOf(t, loop, 64, 32, modifier)
	t.Cleanup(func() {
		loop.do(func() {
			l.streamDisconnect(c.stream)
			l.streamDestroy(c.stream)
		})
	})
	link(t)
	deadline := time.After(5 * time.Second)
	for {
		select {
		case f := <-c.frames:
			if f.size > 0 {
				return f, p
			}
		case <-deadline:
			t.Fatal("no frame arrived")
			return consumed{}, nil
		}
	}
}

func TestADmabufSourceStreamsGPUBuffersToADmabufConsumer(t *testing.T) {
	src := &gpuSource{solid: solid{0x11}}
	mod := fourcc.Linear
	f, p := streamTo(t, src, &mod)
	if f.dataType != dataDmaBuf || f.fd < 0 || f.size != 256*32 {
		t.Errorf("frame = %+v, want a %d-byte DmaBuf with its fd", f, 256*32)
	}
	if src.allocs.Load() == 0 || src.fills.Load() == 0 {
		t.Errorf("allocs %d fills %d, want the buffers bound and filled in place", src.allocs.Load(), src.fills.Load())
	}
	p.Close()
	if r, a := src.released.Load(), src.allocs.Load(); r != a {
		t.Errorf("released %d of %d GPU buffers on close", r, a)
	}
}

func TestADmabufSourceFallsBackToSharedMemory(t *testing.T) {
	src := &gpuSource{solid: solid{0x22}}
	f, _ := streamTo(t, src, nil)
	if f.dataType != dataMemFd || f.first != 0x22 || f.size != 256*32 {
		t.Errorf("frame = %+v, want producer-allocated memfd filled by Fill", f)
	}
	if src.allocs.Load() != 0 || src.fills.Load() != 0 {
		t.Errorf("a plain consumer got GPU buffers (allocs %d fills %d)", src.allocs.Load(), src.fills.Load())
	}
}

// lateSource is a DmabufSource whose frames land apart from the graph,
// after a few handovers of the buffer they fill, as a capture waiting
// for the compositor does.
type lateSource struct {
	gpuSource
	handovers int
	filling   Dmabuf
	prepared  bool
	switched  chan struct{}
	sent      atomic.Int32
}

func (l *lateSource) FillDmabuf(d Dmabuf) ([]Rect, bool) {
	if l.filling == nil {
		l.filling = d
	}
	if l.filling != d {
		select {
		case l.switched <- struct{}{}:
		default:
		}
		l.filling = d
	}
	l.handovers++
	if l.handovers < 4 {
		return nil, false
	}
	l.handovers, l.filling = 0, nil
	l.sent.Add(1)
	return nil, true
}

// PrepareDmabuf starts on the next buffer: its handovers count from here.
func (l *lateSource) PrepareDmabuf(d Dmabuf) {
	l.filling, l.prepared = d, true
}

// A buffer a fill has not finished comes back on the next cycle, instead
// of the fill landing only when the free buffers have gone round, and
// the buffer prepared after a frame is the one the next cycle fills.
func TestAnUnfinishedFillKeepsItsBuffer(t *testing.T) {
	src := &lateSource{switched: make(chan struct{}, 1)}
	mod := fourcc.Linear
	_, p := streamTo(t, src, &mod)
	deadline := time.After(5 * time.Second)
	for src.sent.Load() < 3 {
		select {
		case <-src.switched:
			t.Fatal("a fill in progress lost its buffer to another")
		case <-deadline:
			t.Fatalf("%d frames sent in 5 s", src.sent.Load())
		case <-time.After(10 * time.Millisecond):
		}
	}
	p.Close()
	if !src.prepared {
		t.Error("the source was never asked to prepare the next buffer")
	}
}

type restarts struct {
	solid
	n int
}

func (r *restarts) Restart() { r.n++ }

// A source hears of dropped buffers, as new ones hold nothing, but not
// when they drop because the stream ends. Its unfinished buffer is gone.
func TestDroppedBuffersRestartTheSource(t *testing.T) {
	src := &restarts{}
	p := &Producer{cfg: Config{Source: src}, closing: make(chan struct{}), pending: &buffer{}}
	p.buffersCleared()
	if src.n != 1 {
		t.Errorf("%d restarts for dropped buffers, want 1", src.n)
	}
	if p.pending != nil {
		t.Error("the producer kept a dropped buffer")
	}
	close(p.closing)
	p.buffersCleared()
	if src.n != 1 {
		t.Errorf("%d restarts after the stream ended, want 1", src.n)
	}
}
