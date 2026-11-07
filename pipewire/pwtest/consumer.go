package pwtest

import (
	"encoding/binary"
	"math"
	"os/exec"
	"runtime"
	"sync"
	"testing"
	"time"
	"unsafe"

	"github.com/ebitengine/purego"
)

// Consumer is a reference consumer: a libpipewire input stream, linked
// to a node's first output port, asking for the header and cursor metas
// as screen recorders do, and reporting every buffer it dequeues.
type Consumer struct {
	buffers chan Buffer
}

// Buffer is what one dequeued buffer carried.
type Buffer struct {
	// Size is the chunk's size: 0 for a buffer that holds no frame.
	Size uint32
	// Corrupted is the chunk's SPA_CHUNK_FLAG_CORRUPTED.
	Corrupted bool
	// Seq is the header meta's sequence number: a buffer dequeued again
	// without the producer queueing it anew carries the same one.
	Seq uint64
	// Cursor is the cursor meta, nil when the buffers have none.
	Cursor *Cursor
}

// Cursor is a cursor meta: an ID of 0 is no news.
type Cursor struct {
	ID                 uint32
	X, Y               int32
	HotspotX, HotspotY int32
	// Bitmap is the bitmap the meta carried, nil when it carried none.
	Bitmap *Bitmap
}

// Bitmap is a cursor meta's bitmap: a 0 x 0 one hides the pointer.
type Bitmap struct {
	Format, Width, Height uint32
	Stride                int32
}

// consumerName is the node name every reference consumer takes.
const consumerName = "pwtest-consumer"

var (
	callbackOnce sync.Once
	onProcess    uintptr
	onParam      uintptr
	consumers    sync.Map
	nextHandle   uintptr
	handleMu     sync.Mutex
)

// streamEvents mirrors struct pw_stream_events (version 2) on LP64,
// up to process.
type streamEvents struct {
	version      uint32
	_            uint32
	_            [4]uintptr
	paramChanged uintptr
	_            [2]uintptr
	process      uintptr
	_            [3]uintptr
}

// pwBuffer, spaBuffer, spaMeta, spaData and spaChunk mirror the heads
// of the libpipewire structs a stream dequeues.
type pwBuffer struct{ buffer *spaBuffer }

type spaBuffer struct {
	nMetas uint32
	_      uint32
	metas  *spaMeta
	datas  *spaData
}

type spaMeta struct {
	typ  uint32
	_    uint32
	data unsafe.Pointer
}

type spaData struct {
	_     [2]uint32
	_     int64
	_     [2]uint32
	_     unsafe.Pointer
	chunk *spaChunk
}

type spaChunk struct {
	_, size uint32
	_       int32
	flags   int32
}

// spaMetaHeader mirrors struct spa_meta_header.
type spaMetaHeader struct {
	_, _ uint32
	_    int64
	_    int64
	seq  uint64
}

// spaMetaCursor and spaMetaBitmap mirror struct spa_meta_cursor and
// struct spa_meta_bitmap.
type spaMetaCursor struct {
	id, _        uint32
	x, y         int32
	hotX, hotY   int32
	bitmapOffset uint32
}

type spaMetaBitmap struct {
	format, width, height uint32
	stride                int32
	_                     uint32
}

const (
	metaHeader         = 1
	metaCursor         = 5
	chunkFlagCorrupted = 1
	paramFormat        = 4
)

// consumerState is one consumer's stream as its loop thread sees it.
type consumerState struct {
	lib     *Lib
	stream  uintptr
	buffers chan Buffer
}

// Consume connects a reference consumer offering any size of BGRx video
// and links node's output_0 to it with pw-link. The daemon must be
// running; the consumer goes away with the test.
func Consume(t *testing.T, node string) *Consumer {
	t.Helper()
	l, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	loop, err := l.StartLoop()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(loop.Stop)
	callbackOnce.Do(func() {
		onProcess = purego.NewCallback(process)
		onParam = purego.NewCallback(paramChanged)
	})
	handleMu.Lock()
	nextHandle++
	handle := nextHandle
	handleMu.Unlock()
	st := &consumerState{lib: l, buffers: make(chan Buffer, 1024)}
	consumers.Store(handle, st)
	t.Cleanup(func() { consumers.Delete(handle) })
	events := &streamEvents{version: 2, paramChanged: onParam, process: onProcess}
	var pinner runtime.Pinner
	pinner.Pin(events)
	t.Cleanup(pinner.Unpin)
	var rc int32
	loop.Do(func() {
		props := l.PropertiesNew("media.type=Video media.category=Capture node.name=" + consumerName)
		st.stream = l.StreamNewSimple(loop.Handle(), consumerName, props, unsafe.Pointer(events), handle) //nolint:gosec
		rc = WithPods([][]byte{anyBGRx()}, func(params unsafe.Pointer, n uint32) int32 {
			return l.StreamConnect(st.stream, 0, 0xffffffff, 1<<2, params, n)
		})
	})
	t.Cleanup(func() {
		loop.Do(func() {
			l.StreamDisconnect(st.stream)
			l.StreamDestroy(st.stream)
		})
	})
	if rc < 0 {
		t.Fatalf("pwtest: connect the consumer: %d", rc)
	}
	link(t, node+":output_0", consumerName+":input_0")
	return &Consumer{buffers: st.buffers}
}

func state(handle uintptr) *consumerState {
	v, ok := consumers.Load(handle)
	if !ok {
		return nil
	}
	st, _ := v.(*consumerState)
	return st
}

// paramChanged is the consumers' pw_stream_events.param_changed: once a
// format is set, it asks for the header and cursor metas.
func paramChanged(handle uintptr, id uint32, param unsafe.Pointer) {
	st := state(handle)
	if st == nil || param == nil || id != paramFormat {
		return
	}
	WithPods([][]byte{headerMeta(), cursorMeta()}, func(params unsafe.Pointer, n uint32) int32 {
		return st.lib.StreamUpdate(st.stream, params, n)
	})
}

// process is the consumers' pw_stream_events.process.
func process(handle uintptr) {
	st := state(handle)
	if st == nil {
		return
	}
	raw := st.lib.StreamDequeue(st.stream)
	if raw == nil {
		return
	}
	defer st.lib.StreamQueue(st.stream, raw)
	buf := (*pwBuffer)(raw).buffer
	chunk := buf.datas.chunk
	got := Buffer{Size: chunk.size, Corrupted: chunk.flags&chunkFlagCorrupted != 0}
	for _, m := range unsafe.Slice(buf.metas, buf.nMetas) { //nolint:gosec // the metas libpipewire laid out
		switch m.typ {
		case metaHeader:
			got.Seq = (*spaMetaHeader)(m.data).seq
		case metaCursor:
			got.Cursor = readCursor(m)
		}
	}
	select {
	case st.buffers <- got:
	default:
	}
}

func readCursor(m spaMeta) *Cursor {
	head := (*spaMetaCursor)(m.data)
	c := &Cursor{ID: head.id, X: head.x, Y: head.y, HotspotX: head.hotX, HotspotY: head.hotY}
	if head.id == 0 || head.bitmapOffset == 0 {
		return c
	}
	bm := (*spaMetaBitmap)(unsafe.Add(m.data, head.bitmapOffset))
	c.Bitmap = &Bitmap{Format: bm.format, Width: bm.width, Height: bm.height, Stride: bm.stride}
	return c
}

// Buffer waits for the next buffer.
func (c *Consumer) Buffer(t *testing.T) Buffer {
	t.Helper()
	select {
	case b := <-c.buffers:
		return b
	case <-time.After(5 * time.Second):
		t.Fatal("pwtest: the consumer received no buffer")
		return Buffer{}
	}
}

// Frame waits for the next buffer that holds a frame and returns its
// size.
func (c *Consumer) Frame(t *testing.T) uint32 {
	t.Helper()
	for {
		if b := c.Buffer(t); b.Size > 0 {
			return b.Size
		}
	}
}

// link joins two ports with pw-link, retrying while they appear.
func link(t *testing.T, output, input string) {
	t.Helper()
	bin, err := exec.LookPath("pw-link")
	if err != nil {
		t.Fatalf("pw-link not on PATH: %v", err)
	}
	var out []byte
	for range 100 {
		out, err = exec.Command(bin, output, input).CombinedOutput() //nolint:gosec
		if err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("pw-link %s %s: %v: %s", output, input, err, out)
}

// podWriter builds the pods a reference consumer offers.
type podWriter struct{ body []byte }

func (w *podWriter) u32(vs ...uint32) {
	for _, v := range vs {
		w.body = binary.LittleEndian.AppendUint32(w.body, v)
	}
}

func (w *podWriter) prop(key, typ uint32, value []byte) {
	w.u32(key, 0, size(value), typ)
	w.body = append(w.body, value...)
	for len(w.body)%8 != 0 {
		w.body = append(w.body, 0)
	}
}

// object is the writer's props as an Object pod of type and id.
func (w *podWriter) object(typ, id uint32) []byte {
	body := append(words(typ, id), w.body...)
	return append(words(size(body), typeObject), body...)
}

func words(vs ...uint32) []byte {
	var b []byte
	for _, v := range vs {
		b = binary.LittleEndian.AppendUint32(b, v)
	}
	return b
}

func size(b []byte) uint32 { return uint32(min(len(b), math.MaxUint32)) }

const (
	typeID, typeInt, typeRectangle, typeFraction = 3, 4, 10, 11
	typeObject, typeChoice, choiceRange          = 15, 19, 1
)

func rangeOf(childType, childSize uint32, values ...uint32) []byte {
	return append(words(choiceRange, 0, childSize, childType), words(values...)...)
}

// anyBGRx is an EnumFormat pod for raw BGRx video of any size and rate,
// as an application's consumer offers.
func anyBGRx() []byte {
	const (
		objectFormat, paramEnumFormat           = 0x40003, 3
		keyMediaType, keySubtype                = 1, 2
		keyFormat, keySize, keyRate             = 0x20001, 0x20003, 0x20004
		mediaVideo, subtypeRaw, videoFormatBGRx = 2, 1, 8
	)
	var w podWriter
	w.prop(keyMediaType, typeID, words(mediaVideo))
	w.prop(keySubtype, typeID, words(subtypeRaw))
	w.prop(keyFormat, typeID, words(videoFormatBGRx))
	w.prop(keySize, typeChoice, rangeOf(typeRectangle, 8, 640, 480, 1, 1, 8192, 8192))
	w.prop(keyRate, typeChoice, rangeOf(typeFraction, 8, 30, 1, 0, 1, 1000, 1))
	return w.object(objectFormat, paramEnumFormat)
}

// headerMeta is a Meta param pod asking for the header meta.
func headerMeta() []byte {
	const objectParamMeta, paramMeta, keyType, keySize, size = 0x40005, 6, 1, 2, 32
	var w podWriter
	w.prop(keyType, typeID, words(metaHeader))
	w.prop(keySize, typeInt, words(size))
	return w.object(objectParamMeta, paramMeta)
}

// cursorMeta is a Meta param pod asking for the cursor meta with room for
// a 256 x 256 bitmap, as OBS asks for it.
func cursorMeta() []byte {
	const (
		objectParamMeta, paramMeta = 0x40005, 6
		keyType, keySize           = 1, 2
		headers, side              = 28 + 20, 256
	)
	var w podWriter
	w.prop(keyType, typeID, words(metaCursor))
	w.prop(keySize, typeChoice, rangeOf(typeInt, 4, headers+side*side*4, headers+4, headers+side*side*4))
	return w.object(objectParamMeta, paramMeta)
}
