package pipewire

import (
	"sync/atomic"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-lib/pipewire/pwtest"
)

// driving makes the rig's node the driver of its graph, with itself as the
// target every cycle ends on, as the server sets it up.
func (r *cycleRig) driving(t *testing.T) (selfWake int) {
	t.Helper()
	r.pos = &r.act.position
	r.n.position = r.pos
	r.act.flags.Store(0)
	r.act.activeDriverID.Store(rigNode)
	r.act.state[0].required.Store(1)
	selfWake = rigEventfd(t)
	r.n.targets = []target{{id: rigNode, activation: r.act, fd: selfWake}}
	return selfWake
}

// follower adds a peer to the rig's targets, following driver in status.
func (r *cycleRig) follower(t *testing.T, id, driver, status, flags uint32) (*nodeActivation, int) {
	t.Helper()
	peer, err := view[nodeActivation](shared(unsafe.Sizeof(nodeActivation{})), 0)
	if err != nil {
		t.Fatal(err)
	}
	peer.state[0].required.Store(1)
	peer.activeDriverID.Store(driver)
	peer.status.Store(status)
	peer.flags.Store(flags)
	fd := rigEventfd(t)
	r.n.targets = append(r.n.targets, target{id: id, activation: peer, fd: fd})
	return peer, fd
}

func rigEventfd(t *testing.T) int {
	t.Helper()
	fd, err := unix.Eventfd(0, unix.EFD_CLOEXEC|unix.EFD_NONBLOCK)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = unix.Close(fd) })
	return fd
}

func woken(fd int) bool {
	var count [8]byte
	_, err := unix.Read(fd, count[:])
	return err == nil
}

// counted is a source that counts its fills and has a frame while ready.
type counted struct {
	fills atomic.Int32
	ready atomic.Bool
}

func (c *counted) Fill(dst []byte) ([]Rect, bool) {
	c.fills.Add(1)
	return nil, c.ready.Load()
}

// A driving stream begins a cycle only with a frame and someone to take it: a
// frame sent before the consumer runs is lost, and a still source has no other.
func TestAFrameWaitsForAFollower(t *testing.T) {
	src := &counted{}
	src.ready.Store(true)
	r := newCycleRig(t, src)
	self := r.driving(t)
	p := r.n.h.(*Producer)
	p.interval = time.Millisecond
	if wait, err := p.driveLocked(r.n); err != nil || wait != p.interval {
		t.Fatalf("with no follower: wait %v, %v", wait, err)
	}
	stopped, _ := r.follower(t, 9, rigNode, activationInactive, 0)
	if _, err := p.driveLocked(r.n); err != nil {
		t.Fatal(err)
	}
	if n := src.fills.Load(); n != 0 || r.pos.clock.cycle.Load() != 0 {
		t.Fatalf("%d fills and %d cycles before a follower ran", n, r.pos.clock.cycle.Load())
	}
	stopped.status.Store(activationFinished)
	if _, err := p.driveLocked(r.n); err != nil {
		t.Fatal(err)
	}
	if n := src.fills.Load(); n != 1 || r.pos.clock.cycle.Load() != 1 {
		t.Fatalf("%d fills and %d cycles with a follower", n, r.pos.clock.cycle.Load())
	}
	if got := loadIO(&r.io[directionOutput]); got.status != statusHaveData {
		t.Errorf("the slot the follower reads next cycle holds %+v", got)
	}
	if woken(self) {
		t.Error("the cycle ended before its follower finished")
	}
	if r.act.state[0].pending.Load() != 1 {
		t.Errorf("the driver waits on %d, want its one follower", r.act.state[0].pending.Load())
	}
}

// A cycle counts the node off every follower that takes part and ends once
// they finished: a stopped node, one of another driver and an asynchronous one
// hold nothing up, and only the ready ones wake.
func TestACycleWakesItsFollowers(t *testing.T) {
	src := &counted{}
	src.ready.Store(true)
	r := newCycleRig(t, src)
	self := r.driving(t)
	sync, syncFd := r.follower(t, 9, rigNode, activationFinished, 0)
	stopped, stoppedFd := r.follower(t, 10, rigNode, activationInactive, 0)
	other, otherFd := r.follower(t, 11, 99, activationFinished, 0)
	async, asyncFd := r.follower(t, 12, rigNode, activationFinished, activationFlagAsync)
	p := r.n.h.(*Producer)
	if _, err := p.driveLocked(r.n); err != nil {
		t.Fatal(err)
	}
	if !woken(syncFd) || sync.status.Load() != activationTriggered || sync.driverID.Load() != rigNode {
		t.Errorf("the follower: status %d, driver %d", sync.status.Load(), sync.driverID.Load())
	}
	if !woken(asyncFd) || async.status.Load() != activationTriggered {
		t.Errorf("the asynchronous follower: status %d", async.status.Load())
	}
	if woken(stoppedFd) || stopped.status.Load() != activationInactive {
		t.Errorf("the stopped node: status %d", stopped.status.Load())
	}
	if woken(otherFd) || other.status.Load() != activationFinished {
		t.Errorf("another driver's node: status %d", other.status.Load())
	}
	if got := r.act.state[0].pending.Load(); got != 1 {
		t.Fatalf("the driver waits on %d, want the synchronous follower", got)
	}
	if err := r.n.triggerSelfAsFollower(); err != nil {
		t.Fatal(err)
	}
	if !woken(self) {
		t.Fatal("the last follower did not end the cycle")
	}
	src.fills.Store(0)
	if err := r.n.cycle(); err != nil {
		t.Fatal(err)
	}
	if r.act.status.Load() != activationFinished || src.fills.Load() != 0 {
		t.Errorf("after the cycle's end: status %d, %d fills", r.act.status.Load(), src.fills.Load())
	}
}

// triggerSelfAsFollower does what the last follower does on finishing.
func (n *node) triggerSelfAsFollower() error {
	for i := range n.targets {
		if n.targets[i].activation == n.activation {
			one := []target{n.targets[i]}
			saved := n.targets
			n.targets = one
			defer func() { n.targets = saved }()
			return n.triggerTargets(n.now())
		}
	}
	return nil
}

// A new cycle ends one a follower has not finished rather than stall on it.
func TestAStuckFollowerDoesNotStopTheStream(t *testing.T) {
	src := &counted{}
	src.ready.Store(true)
	r := newCycleRig(t, src)
	r.driving(t)
	r.follower(t, 9, rigNode, activationFinished, 0)
	p := r.n.h.(*Producer)
	for i := uint32(1); i <= 3; i++ {
		p.next = time.Time{}
		if _, err := p.driveLocked(r.n); err != nil {
			t.Fatal(err)
		}
		if got := r.pos.clock.cycle.Load(); got != i {
			t.Fatalf("cycle %d after %d frames with a stuck follower", got, i)
		}
	}
}

// A source that says when it has a frame is asked then, and not on a timer.
func TestANotifyingSourceIsNotPolled(t *testing.T) {
	src := &counted{}
	r := newCycleRig(t, src)
	r.driving(t)
	r.follower(t, 9, rigNode, activationFinished, 0)
	p := r.n.h.(*Producer)
	p.notifies, p.useDmabuf = true, true
	wait, err := p.driveLocked(r.n)
	if err != nil || wait != 0 {
		t.Fatalf("no frame yet: wait %v, %v", wait, err)
	}
	src.ready.Store(true)
	wait, err = p.driveLocked(r.n)
	if err != nil || wait != 0 || r.pos.clock.cycle.Load() != 1 {
		t.Fatalf("a frame: wait %v, %v, cycle %d", wait, err, r.pos.clock.cycle.Load())
	}
}

// Before 1.2 a cycle has no driver ids or cycle count to touch, and every peer
// wakes.
func TestAVersion0CycleWakesItsPeers(t *testing.T) {
	src := &counted{}
	src.ready.Store(true)
	r := newCycleRig(t, src)
	self := r.driving(t)
	r.version0()
	peer, fd := r.follower(t, 9, 0, activationFinished, 0)
	p := r.n.h.(*Producer)
	if _, err := p.driveLocked(r.n); err != nil {
		t.Fatal(err)
	}
	if !woken(fd) || peer.status.Load() != activationTriggered || peer.driverID.Load() != 0 {
		t.Errorf("the peer: status %d, driver id %d", peer.status.Load(), peer.driverID.Load())
	}
	if r.pos.clock.cycle.Load() != 0 {
		t.Errorf("the padding that is a 1.2 cycle count was written: %d", r.pos.clock.cycle.Load())
	}
	if woken(self) {
		t.Error("the cycle ended before its peer finished")
	}
}

// The stream advertises a variable rate up to its pace, and takes a lower one
// a consumer negotiated.
func TestAStreamPacesToTheNegotiatedRate(t *testing.T) {
	p := &Producer{cfg: Config{Width: 64, Height: 32, Stride: 256, Format: VideoBGRx, FPS: 60}}
	p.pace(p.cfg.FPS)
	offered := formatPod(64, 32, 60, VideoBGRx, nil)
	if typ, v, ok := propValue(offered, formatVideoRate); ok || typ != 0 || v != nil {
		t.Errorf("the framerate is fixed: type %d", typ)
	}
	if _, ok := formatMaxRate(offered); ok {
		t.Error("an unfixed maximum rate read as negotiated")
	}
	fixed := func(rate pod) []byte {
		return object(objectFormat, paramFormat,
			prop{key: formatMediaType, value: podID(mediaTypeVideo)},
			prop{key: formatMediaSubtype, value: podID(mediaSubtypeRaw)},
			prop{key: formatVideoFormat, value: podID(uint32(VideoBGRx))},
			prop{key: formatVideoSize, value: podRectangle(64, 32)},
			prop{key: formatVideoMaxRate, value: rate})
	}
	for _, tc := range []struct {
		rate pod
		want time.Duration
	}{
		{podFraction(30, 1), time.Second / 30},
		{podFraction(30000, 1001), time.Second / 30},
		{podFraction(144, 1), time.Second / 60},
		{podFraction(0, 1), time.Second / 60},
	} {
		if err := p.setFormat(fixed(tc.rate)); err != nil {
			t.Fatal(err)
		}
		if p.interval != tc.want {
			t.Errorf("a maximum of %v paces at %v, want %v", tc.rate.body, p.interval, tc.want)
		}
	}
}

// Frames that land faster than the negotiated rate wait for it, on average
// as well: a 14 ms source is not let through at 71 a second by a stream of 60,
// and one that falls behind does not burst to catch up.
func TestAStreamHoldsItsRateWhateverTheSourcesCadence(t *testing.T) {
	src := &counted{}
	src.ready.Store(true)
	r := newCycleRig(t, src)
	p := r.n.h.(*Producer)
	p.pace(60)
	now := time.Now()
	sent := 0
	for range 1000 {
		now = now.Add(14 * time.Millisecond)
		if b := p.frame(r.n, now); b != nil {
			b.release()
			sent++
		}
	}
	if seconds := 14.0; sent > int(seconds*60)+1 || sent < int(seconds*60)-30 {
		t.Errorf("%d frames in %v seconds from a 14 ms source, want about 60 a second", sent, seconds)
	}
	now = now.Add(time.Second)
	if p.frame(r.n, now) == nil {
		t.Fatal("no frame after a long gap")
	}
	if b := p.frame(r.n, now.Add(time.Millisecond)); b != nil {
		t.Error("a second frame 1 ms after a long gap: the stream bursts to catch up")
	}
}

// On a real server a stream's rate is its source's, not one frame per the
// graph's quantum: 1024 samples at 48 kHz would hold it to 46.9 a second.
func TestAStreamIsNotHeldToTheQuantum(t *testing.T) {
	pwtest.Start(t)
	p, err := Start(Config{Application: testApplication, Name: testNode, Width: 64, Height: 32, Stride: 256, Format: VideoBGRx, FPS: 240, Source: solid{1}})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	c := linkedConsumer(t, 240)
	select {
	case <-c.frames:
	case <-time.After(5 * time.Second):
		t.Fatal("no first frame")
	}
	frames := 0
	for end := time.After(time.Second); ; {
		select {
		case <-c.frames:
			frames++
			continue
		case <-end:
		}
		break
	}
	if frames <= 60 {
		t.Fatalf("%d frames in a second at a pace of 240", frames)
	}
}

// oneFrame has one frame, for a still window's share.
type oneFrame struct{ sent atomic.Bool }

func (o *oneFrame) Fill(dst []byte) ([]Rect, bool) {
	return nil, !o.sent.Swap(true)
}

// A still source's only frame reaches the consumer, and after it the graph
// does not run.
func TestAStillSourceLeavesTheGraphIdle(t *testing.T) {
	pwtest.Start(t)
	p, err := Start(Config{Application: testApplication, Name: testNode, Width: 64, Height: 32, Stride: 256, Format: VideoBGRx, FPS: 240, Source: &oneFrame{}})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	c := linkedConsumer(t, 240)
	select {
	case f := <-c.frames:
		if f.size == 0 {
			t.Fatalf("an empty frame: %+v", f)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the still source's frame never arrived")
	}
	cycle := func() uint32 {
		p.n.mu.Lock()
		defer p.n.mu.Unlock()
		return p.n.activation.position.clock.cycle.Load()
	}
	before := cycle()
	select {
	case f := <-c.frames:
		t.Fatalf("a second frame from a still source: %+v", f)
	case <-time.After(300 * time.Millisecond):
	}
	if after := cycle(); after != before {
		t.Errorf("%d cycles of a still source in 300 ms", after-before)
	}
}

// linkedConsumer links a libpipewire consumer to the test's producer.
func linkedConsumer(t *testing.T, fps uint32) *consumer {
	t.Helper()
	l, _ := load()
	loop, err := startLoop(l)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(loop.stop)
	c := newConsumerOfFormat(t, loop, formatPod(64, 32, fps, VideoBGRx, nil))
	t.Cleanup(func() {
		loop.do(func() {
			l.streamDisconnect(c.stream)
			l.streamDestroy(c.stream)
		})
	})
	link(t)
	return c
}
