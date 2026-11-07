// Package pipewire is the PipeWire video producer a ScreenCast portal
// streams through: a node exported over PipeWire's native
// protocol (a client-node, as libpipewire's pw_stream exports one), which
// drives its own graph: a cycle runs when a captured frame is ready, stamped
// with the header, transform and damage metas. The protocol, the shared memory
// and the realtime cycle are spoken here in Go; no libpipewire.
package pipewire

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"sync"
	"time"

	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-lib/fourcc"
	"github.com/stubbedev/lys-lib/intconv"
)

// Rect is a damaged region in buffer pixels.
type Rect struct{ X, Y, Width, Height int }

// Source fills one frame: Fill copies stride-byte rows into dst and
// reports what changed (nil or empty is the whole frame); ok false
// skips this cycle (nothing new, or the capture failed).
type Source interface {
	Fill(dst []byte) (damage []Rect, ok bool)
}

// Resizable is a Source whose frames change size (a window being
// resized): Size is the layout of the frames it has now. When that
// differs from the stream's, the producer offers the new size and
// holds frames back until the consumer has renegotiated.
type Resizable interface {
	Source
	Size() (width, height, stride uint32)
}

// DmabufSource is a Source that also fills GPU buffers: the producer
// offers its modifier's format first and, when the consumer takes it,
// binds one buffer from AllocDmabuf to each PipeWire buffer for the
// stream's life and fills it in place (zero copy). A consumer that
// takes the plain format gets producer-allocated shared memory, filled
// through Fill.
type DmabufSource interface {
	Source
	Modifier() fourcc.Modifier
	// DmabufVideoFormat is the GPU buffers' format, which need not be
	// the shared-memory one (a GPU compositor hands out 24-bit shm and
	// 32-bit dmabufs).
	DmabufVideoFormat() VideoFormat
	AllocDmabuf() (Dmabuf, error)
	FillDmabuf(Dmabuf) (damage []Rect, ok bool)
}

// FrameNotifier is a DmabufSource whose GPU frames land on their own time. On
// those buffers the producer does not poll it: it calls NotifyFrames once,
// before any fill, and the source calls wake, from any goroutine and without
// blocking, whenever FillDmabuf may now produce a frame or a copy it started
// has ended.
type FrameNotifier interface {
	NotifyFrames(wake func())
}

// DmabufPreparer is a DmabufSource whose fills land apart from the graph
// (FillDmabuf false while one runs). PrepareDmabuf starts filling the
// buffer the next cycle's FillDmabuf gets, so a frame goes out each cycle
// and not every other one. It must not block.
type DmabufPreparer interface {
	PrepareDmabuf(Dmabuf)
}

// Restarter is a Source that must know when the consumer's buffers are
// dropped: the buffers that replace them hold nothing, so the next fill
// has to produce a frame even if the source has not changed.
type Restarter interface {
	Restart()
}

// Dmabuf is one GPU buffer's single plane.
type Dmabuf interface {
	Fd() int
	Offset() uint32
	Stride() uint32
	Release()
}

// Config is one producer's identity and fixed stream geometry.
type Config struct {
	// Application is the client's application.name, the program the
	// server and its tools list the stream under. Required.
	Application string
	// Name is the node name the consumer sees and links to, and the
	// name of the stream's shared-memory buffers. Required.
	Name                  string
	Width, Height, Stride uint32
	Format                VideoFormat
	// FPS is the most frames a second the stream sends, advertised as its
	// maximum and paced. The rate is variable: a frame goes out when the
	// source has one, not on a clock. A consumer may ask for less. Required.
	FPS uint32
	// Transform is the constant spa_meta_videotransform value (the
	// output's wl_output transform; 0 for a window).
	Transform uint32
	Source    Source
	// Cursor, when set, is the pointer the buffers carry as a cursor meta
	// (see Cursor); the frames then hold no pointer of their own.
	Cursor Cursor
}

// Producer is one running PipeWire video output stream. Its Source is
// called from one goroutine at a time.
type Producer struct {
	k *core
	n *node

	done      chan struct{}
	endOnce   sync.Once
	errMu     sync.Mutex
	err       error
	closeOnce sync.Once
	closing   chan struct{}

	cfg Config
	// dmabuf is the source's GPU side when it has one; useDmabuf is set
	// once the consumer negotiated the modifier format.
	dmabuf    DmabufSource
	useDmabuf bool
	// resizable is the source's size when it can change; renegotiating
	// holds frames back from a size change until the consumer took the
	// new format.
	resizable     Resizable
	renegotiating bool
	format        []byte
	// cursor tells the consumer of Config.Cursor; nil without one.
	cursor *cursorSender
	// pending is the buffer a fill did not finish: it stays this
	// producer's until it goes out, as a source may be filling it.
	pending *buffer

	// kick wakes the drive loop; driven closes when it has ended.
	kick   chan struct{}
	driven chan struct{}
	// notifies is whether the source tells when its GPU frames land.
	notifies bool

	start time.Time
	// interval is the time between frames at the negotiated rate, and next
	// when the next one is due: frames that land early wait for it, and a late
	// one does not move the ones after it, so the rate is the negotiated one
	// whatever the source's cadence.
	interval time.Duration
	next     time.Time
	seq      uint64
	damage   []Rect
}

// connectTimeout bounds each wait on the server while starting.
const connectTimeout = 5 * time.Second

// Start exports the stream's node and returns once the server has
// registered it, so a consumer can link to NodeID.
func Start(cfg Config) (*Producer, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	p := &Producer{
		cfg: cfg, done: make(chan struct{}), closing: make(chan struct{}), start: time.Now(),
		kick: make(chan struct{}, 1), driven: make(chan struct{}),
		damage: make([]Rect, 0, maxDamageRegions),
	}
	p.pace(cfg.FPS)
	if notifier, ok := cfg.Source.(FrameNotifier); ok {
		p.notifies = true
		notifier.NotifyFrames(p.scheduleChanged)
	}
	if cfg.Cursor != nil {
		p.cursor = &cursorSender{source: cfg.Cursor}
		cfg.Cursor.NotifyCursor(p.scheduleChanged)
	}
	p.dmabuf, _ = cfg.Source.(DmabufSource)
	p.resizable, _ = cfg.Source.(Resizable)
	k, err := connectCore(connectTimeout, cfg.Application, p.stopped)
	if err != nil {
		return nil, err
	}
	var portFlags uint64
	if p.dmabuf != nil {
		portFlags = portFlagCanAlloc
	}
	p.k = k
	p.n, err = newNode(k, p, directionOutput, []dictItem{
		{"media.type", "Video"},
		{"media.category", "Capture"},
		{"media.role", "Screen"},
		{"media.class", "Stream/Output/Video"},
		{"media.name", cfg.Name},
		{"node.name", cfg.Name},
		{"node.async", "true"},
		{"node.driver", "true"},
		{"node.want-driver", "true"},
		{"stream.is-live", "true"},
	}, portFlags)
	if err != nil {
		k.close()
		return nil, fmt.Errorf("pipewire: create the stream node: %w", err)
	}
	go p.drive()
	if err := p.n.export(connectTimeout); err != nil {
		p.Close()
		return nil, fmt.Errorf("pipewire: export the stream node: %w", err)
	}
	return p, nil
}

// validate refuses a config missing a name the stream is known by or
// its frame rate.
func (c Config) validate() error {
	if c.Application == "" {
		return errors.New("pipewire: Config.Application is empty: the client needs the application name it is listed under")
	}
	if c.Name == "" {
		return errors.New("pipewire: Config.Name is empty: the stream needs the node name its consumer links to")
	}
	if c.FPS == 0 {
		return errors.New("pipewire: Config.FPS is 0: the stream needs the frame rate it advertises and paces")
	}
	return nil
}

// NodeID is the node the consumer connects to.
func (p *Producer) NodeID() uint32 { return p.n.global }

// Serial is the node's object.serial, which a consumer targets
// (PW_KEY_TARGET_OBJECT) instead of NodeID: ids are reused once a
// node goes, serials never are.
func (p *Producer) Serial() uint64 { return p.n.objectSerial }

// Done is closed once the stream has ended: closed, or cut off by the
// server (Err says why).
func (p *Producer) Done() <-chan struct{} { return p.done }

// Err is why the stream ended by itself; nil while it runs and after
// Close.
func (p *Producer) Err() error {
	p.errMu.Lock()
	defer p.errMu.Unlock()
	return p.err
}

// stopped is the event loop's end: the server went away or broke the
// protocol, or Close stopped it.
func (p *Producer) stopped(err error) {
	select {
	case <-p.closing:
	default:
		p.errMu.Lock()
		p.err = err
		p.errMu.Unlock()
	}
	p.endOnce.Do(func() { close(p.done) })
}

// Close stops the stream and releases everything it holds.
func (p *Producer) Close() {
	p.closeOnce.Do(func() {
		close(p.closing)
		<-p.driven
		if err := p.n.deactivate(); err != nil {
			p.k.abort(err)
		}
		p.k.close()
		p.n.release()
	})
}

// portParams is the formats the stream offers and, once one is
// negotiated, its buffer layout and metas. A DmabufSource's modifier
// format comes first.
func (p *Producer) portParams() []portParam {
	var enum [][]byte
	if p.dmabuf != nil {
		mod := p.dmabuf.Modifier()
		enum = append(enum, formatPod(p.cfg.Width, p.cfg.Height, p.cfg.FPS, p.dmabuf.DmabufVideoFormat(), &mod))
	}
	enum = append(enum, formatPod(p.cfg.Width, p.cfg.Height, p.cfg.FPS, p.cfg.Format, nil))
	params := []portParam{
		{id: paramEnumFormat, flags: paramInfoRead, pods: enum},
		{id: paramMeta, flags: paramInfoRead, pods: [][]byte{busyMetaPod()}},
		{id: paramFormat, flags: paramInfoWrite},
		{id: paramBuffers},
	}
	if p.format == nil {
		return params
	}
	layout := bufferPodsOf(p.cfg.Stride, p.cfg.Height, p.dataTypes())
	params[1].pods = append(params[1].pods, layout[1:]...)
	if p.cursor != nil {
		params[1].pods = append(params[1].pods, cursorMetaPod())
	}
	params[2] = portParam{id: paramFormat, flags: paramInfoRead | paramInfoWrite, pods: [][]byte{p.format}}
	params[3] = portParam{id: paramBuffers, flags: paramInfoRead, pods: layout[:1]}
	return params
}

// dataTypes is the spa_data types the buffers' data comes in: a
// DmabufSource allocates every buffer itself, a GPU buffer or a memfd
// per the negotiated format; otherwise the server's shared memory.
func (p *Producer) dataTypes() uint32 {
	switch {
	case p.dmabuf == nil:
		return 1<<dataMemFd | 1<<dataMemPtr
	case p.useDmabuf:
		return 1 << dataDmaBuf
	}
	return 1 << dataMemFd
}

// setFormat takes the format the consumer and the server settled on,
// which must be one the stream offers.
func (p *Producer) setFormat(f []byte) error {
	if f == nil {
		p.format, p.useDmabuf = nil, false
		return nil
	}
	if typ, v, ok := propValue(f, formatMediaType); !ok || typ != typeID || len(v) < 4 || binary.LittleEndian.Uint32(v) != mediaTypeVideo {
		return errors.New("not a video format")
	}
	if typ, v, ok := propValue(f, formatMediaSubtype); !ok || typ != typeID || len(v) < 4 || binary.LittleEndian.Uint32(v) != mediaSubtypeRaw {
		return errors.New("not raw video")
	}
	typ, v, ok := propValue(f, formatVideoFormat)
	if !ok || typ != typeID || len(v) < 4 {
		return errors.New("no video format")
	}
	video := VideoFormat(binary.LittleEndian.Uint32(v))
	mtyp, mv, dmabuf := propValue(f, formatVideoModifier)
	want := p.cfg.Format
	if dmabuf {
		if p.dmabuf == nil {
			return errors.New("a dmabuf format, and the source has no GPU buffers")
		}
		if mtyp != typeLong || len(mv) < 8 || fourcc.Modifier(binary.LittleEndian.Uint64(mv)) != p.dmabuf.Modifier() {
			return fmt.Errorf("modifier is not the offered %s", p.dmabuf.Modifier())
		}
		want = p.dmabuf.DmabufVideoFormat()
	}
	if video != want {
		return fmt.Errorf("video format %d is not the offered %d", video, want)
	}
	w, h, ok := formatSize(f)
	if !ok || w != p.cfg.Width || h != p.cfg.Height {
		return fmt.Errorf("size %dx%d is not the offered %dx%d", w, h, p.cfg.Width, p.cfg.Height)
	}
	p.format, p.useDmabuf, p.renegotiating = append([]byte(nil), f...), dmabuf, false
	rate := p.cfg.FPS
	if asked, ok := formatMaxRate(f); ok {
		rate = min(rate, asked)
	}
	p.pace(rate)
	return nil
}

// allocData gives a buffer its memory: a GPU buffer when the consumer
// took the modifier format, else a memfd.
func (p *Producer) allocData(b *buffer) error {
	if p.useDmabuf {
		d, err := p.dmabuf.AllocDmabuf()
		if err != nil {
			return err
		}
		b.dmabuf, b.fd = d, d.Fd()
		b.dataType, b.dataFlags = dataDmaBuf, dataFlagReadable
		b.maxsize = d.Stride() * p.cfg.Height
		stride, err := chunkStride(d.Stride())
		if err != nil {
			d.Release()
			return err
		}
		b.chunk.offset, b.chunk.stride, b.chunk.size = d.Offset(), stride, 0
		return nil
	}
	size := p.cfg.Stride * p.cfg.Height
	fd, mem, err := memfd(p.cfg.Name, int(size))
	if err != nil {
		return err
	}
	b.fd, b.data, b.maxsize = fd, mem, size
	b.dataType, b.dataFlags = dataMemFd, dataFlagReadable|dataFlagMappable
	return nil
}

func (p *Producer) releaseData(b *buffer) {
	if b.dmabuf != nil {
		b.dmabuf.Release()
	} else {
		_ = unix.Munmap(b.data)
		_ = unix.Close(b.fd)
	}
	b.dmabuf, b.data, b.fd = nil, nil, -1
}

// paceSlack is how early a frame may go out: less than the wake-up jitter of
// a timer or a compositor, so neither costs a frame.
const paceSlack = time.Millisecond

// pace sets the stream's frame interval for a rate.
func (p *Producer) pace(rate uint32) {
	p.interval = time.Second / time.Duration(rate)
}

// scheduleChanged wakes the drive loop.
func (p *Producer) scheduleChanged() {
	select {
	case p.kick <- struct{}{}:
	default:
	}
}

// drive runs the stream's graph while the node is its driver, as PipeWire's
// quantum, an audio period, says nothing of when frames exist: a cycle begins
// when there is a frame, so a share runs at its source's rate up to the
// negotiated one, and a still source wakes no one. A source is asked once per
// frame interval unless it is a FrameNotifier on GPU buffers.
func (p *Producer) drive() {
	defer close(p.driven)
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-p.closing:
			return
		case <-p.kick:
		case <-timer.C:
		}
		wait, err := p.driveOnce()
		if err != nil {
			p.k.abort(err)
			return
		}
		if wait > 0 {
			timer.Reset(wait)
		}
	}
}

// driveOnce begins a cycle if a frame is there for it, and says when to look
// again; 0 is when woken.
func (p *Producer) driveOnce() (time.Duration, error) {
	n := p.n
	n.mu.Lock()
	wait, err := p.driveLocked(n)
	dirty := n.portDirty
	n.portDirty = false
	n.mu.Unlock()
	if err == nil && dirty {
		err = n.updatePort()
	}
	return wait, err
}

func (p *Producer) driveLocked(n *node) (time.Duration, error) {
	if n.activation == nil || !n.prepared || !n.driving() {
		return 0, nil
	}
	if !n.followed() {
		return p.interval, nil
	}
	now := time.Now()
	if early := p.early(now); early > 0 {
		return early, nil
	}
	b := p.frame(n, now)
	if b == nil {
		if p.notifies && p.useDmabuf {
			return 0, nil
		}
		return p.interval, nil
	}
	slot, _ := n.cycleSlot()
	n.queue(slot, b)
	err := n.begin()
	p.prepare(n)
	if p.notifies && p.useDmabuf {
		return 0, err
	}
	return max(time.Until(p.next), time.Nanosecond), err
}

// process is one cycle of the stream when another node drives its graph.
func (p *Producer) process(n *node) int32 {
	slot, ok := n.cycleSlot()
	if !ok {
		return statusNeedData
	}
	b := p.frame(n, time.Now())
	if b == nil {
		n.idle(slot)
		return statusNeedData
	}
	n.queue(slot, b)
	p.prepare(n)
	return statusHaveData
}

// frame fills the buffer an earlier fill did not finish, else the next free
// one, unless it is too early or the source's size is being renegotiated. Nil
// is no frame.
func (p *Producer) frame(n *node, now time.Time) *buffer {
	if p.early(now) > 0 || !p.sizeSettled(n) {
		return nil
	}
	b := p.pending
	if b == nil {
		b = n.dequeue()
	}
	if b == nil {
		return nil
	}
	if !p.fill(b, now) {
		p.pending = b
		return p.cursorOnly(n, now)
	}
	p.pending = nil
	p.paced(now)
	return b
}

// paced moves the next frame's time on past a buffer that went out at now.
func (p *Producer) paced(now time.Time) {
	p.next = p.next.Add(p.interval)
	if p.next.Before(now) {
		p.next = now.Add(p.interval)
	}
}

// cursorOnly is a buffer holding no frame, only the pointer's news, when
// there is news and a free buffer to carry it; nil otherwise.
func (p *Producer) cursorOnly(n *node, now time.Time) *buffer {
	if p.cursor == nil || !p.cursor.pending() {
		return nil
	}
	b := n.dequeue()
	if b == nil {
		return nil
	}
	if b.chunk == nil || b.cursor.head == nil {
		b.release()
		return nil
	}
	b.chunk.size, b.chunk.flags = 0, chunkFlagCorrupted
	p.stampCommon(b, now)
	if b.damage != nil {
		writeDamage(b.damage, nil)
	}
	p.paced(now)
	return b
}

// early is how long until a frame may go out.
func (p *Producer) early(now time.Time) time.Duration {
	if !now.Before(p.next) {
		return 0
	}
	return p.next.Sub(now) - paceSlack
}

// prepare has a source that fills apart from the graph start on the next
// frame while this one is on its way.
func (p *Producer) prepare(n *node) {
	prep, ok := p.cfg.Source.(DmabufPreparer)
	if !ok || !p.useDmabuf || p.pending != nil {
		return
	}
	if next := n.dequeue(); next != nil {
		p.pending = next
		prep.PrepareDmabuf(next.dmabuf)
	}
}

// buffersCleared forgets the buffers the node just dropped and, unless
// the stream is ending, tells the source its frames are gone with them.
func (p *Producer) buffersCleared() {
	p.pending = nil
	if p.cursor != nil {
		p.cursor.reset()
	}
	select {
	case <-p.closing:
		return
	default:
	}
	if r, ok := p.cfg.Source.(Restarter); ok {
		r.Restart()
	}
}

// sizeSettled is whether frames of the stream's size may go out: a
// resized source has the new size offered, and frames wait until the
// consumer took it.
func (p *Producer) sizeSettled(n *node) bool {
	if p.resizable == nil {
		return true
	}
	if w, h, stride := p.resizable.Size(); w != p.cfg.Width || h != p.cfg.Height || stride != p.cfg.Stride {
		p.cfg.Width, p.cfg.Height, p.cfg.Stride = w, h, stride
		p.renegotiating = true
		n.markPortChanged()
	}
	return !p.renegotiating
}

// fill writes one frame into b and stamps its metas; false leaves b
// unsent (nothing new, or the capture failed).
func (p *Producer) fill(b *buffer, now time.Time) bool {
	if b.chunk == nil {
		return false
	}
	stride := p.cfg.Stride
	if b.dmabuf != nil {
		stride = b.dmabuf.Stride()
	}
	chunk, err := chunkStride(stride)
	if err != nil {
		p.k.abort(err)
		return false
	}
	var damage []Rect
	var ok bool
	if b.dmabuf != nil {
		damage, ok = p.dmabuf.FillDmabuf(b.dmabuf)
		b.chunk.offset, b.chunk.stride, b.chunk.flags = b.dmabuf.Offset(), chunk, 0
		b.chunk.size = 0
		if ok {
			b.chunk.size = b.dmabuf.Stride() * p.cfg.Height
		}
	} else {
		if b.data == nil {
			return false
		}
		damage, ok = p.cfg.Source.Fill(b.data)
		b.chunk.offset, b.chunk.stride, b.chunk.flags = 0, chunk, 0
		b.chunk.size = 0
		if ok {
			b.chunk.size = min(p.cfg.Stride*p.cfg.Height, uint32(min(len(b.data), math.MaxUint32)))
		}
	}
	if !ok {
		return false
	}
	p.stamp(b, now, damage)
	return true
}

// stamp writes the metas the consumer negotiated on a buffer holding a
// frame that changed by damage.
func (p *Producer) stamp(b *buffer, now time.Time, damage []Rect) {
	p.stampCommon(b, now)
	if b.damage != nil {
		p.damage = ClampDamage(p.damage, damage, int(p.cfg.Width), int(p.cfg.Height), maxDamageRegions)
		writeDamage(b.damage, p.damage)
	}
}

// stampCommon writes the metas every buffer carries, frame or not: the
// header, the transform and the pointer. A cursor that laid its bitmap
// out wrong ends the stream.
func (p *Producer) stampCommon(b *buffer, now time.Time) {
	if b.header != nil {
		*b.header = cMetaHeader{pts: now.Sub(p.start).Nanoseconds(), seq: p.seq}
		p.seq++
	}
	if b.transform != nil {
		*b.transform = p.cfg.Transform
	}
	if p.cursor == nil || b.cursor.head == nil {
		return
	}
	if err := p.cursor.stamp(b.cursor); err != nil {
		p.k.abort(err)
	}
}

// ClampDamage clips rects to the frame into out; none, none left, or
// more than max collapse to the whole frame. It appends at most max
// rects to out[:0], so an out of that capacity never grows.
func ClampDamage(out, rects []Rect, width, height, max int) []Rect {
	whole := Rect{0, 0, width, height}
	out = out[:0]
	if len(rects) == 0 || max == 0 {
		return append(out, whole)
	}
	for _, r := range rects {
		if r.X < 0 || r.Y < 0 || r.X >= width || r.Y >= height {
			continue
		}
		w, h := min(r.Width, width-r.X), min(r.Height, height-r.Y)
		if w <= 0 || h <= 0 {
			continue
		}
		if len(out) == max {
			return append(out[:0], whole)
		}
		out = append(out, Rect{r.X, r.Y, w, h})
	}
	if len(out) == 0 {
		return append(out, whole)
	}
	return out
}

// writeDamage fills the meta's region slots and terminates the list
// with a zero-size region, leaving room for it.
func writeDamage(slots []cMetaRegion, rects []Rect) {
	if len(slots) == 0 {
		return
	}
	i := 0
	for ; i < len(rects) && i+1 < len(slots); i++ {
		r := rects[i]
		slots[i] = metaRegion(r)
	}
	slots[i] = cMetaRegion{}
}

// memfd is a size-byte sealed-size shared memory file called name,
// mapped.
func memfd(name string, size int) (int, []byte, error) {
	fd, err := unix.MemfdCreate(name, unix.MFD_CLOEXEC|unix.MFD_ALLOW_SEALING)
	if err != nil {
		return -1, nil, fmt.Errorf("memfd: %w", err)
	}
	if err := unix.Ftruncate(fd, int64(size)); err != nil {
		_ = unix.Close(fd)
		return -1, nil, fmt.Errorf("memfd: size %d: %w", size, err)
	}
	mem, err := unix.Mmap(fd, 0, size, unix.PROT_READ|unix.PROT_WRITE, unix.MAP_SHARED)
	if err != nil {
		_ = unix.Close(fd)
		return -1, nil, fmt.Errorf("memfd: map %d bytes: %w", size, err)
	}
	return fd, mem, nil
}

func chunkStride(stride uint32) (int32, error) {
	s, err := intconv.Exact[int32](stride)
	if err != nil {
		return 0, fmt.Errorf("pipewire: stride %d exceeds spa_chunk's int32: %w", stride, err)
	}
	return s, nil
}

func metaRegion(r Rect) cMetaRegion {
	return cMetaRegion{
		x:      int32(min(max(r.X, 0), math.MaxInt32)),
		y:      int32(min(max(r.Y, 0), math.MaxInt32)),
		width:  uint32(min(max(r.Width, 0), math.MaxUint32)),
		height: uint32(min(max(r.Height, 0), math.MaxUint32)),
	}
}
