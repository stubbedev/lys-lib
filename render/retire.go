package render

import (
	"runtime"
	"sync"
)

// The free protocol. A List refers to the rasters its calls were given
// rather than copying them, from the call that records them until its
// last Play, and canvases record on several goroutines. A raster whose
// memory its owner frees deterministically (off the Go heap, back to a
// pool) must therefore outlive every List that may hold it:
//
//  1. The owner makes the raster unreachable for new draws: it drops it
//     from its cache, so no recording that starts later is handed it.
//  2. It may call Forget on each GPU device that drew it, which drops
//     the texture or atlas slot kept under its ImageID once that
//     device's current or next frame is done, instead of after the
//     device's idle timeout.
//  3. It calls Retire with the function that frees the memory. Retire
//     runs it once every List whose recording started before the call
//     has ended, at once when none is open.
//
// A List is open from NewRecorder or ResetRecorder until the next
// ResetRecorder of its canvas, its End, or its collection by the
// garbage collector, whichever comes first. A renderer reads a raster's
// pixels only while it plays a List (a GPU device copies them into its
// staging memory and keeps the texture by ImageID, never by address),
// so once the List ends nothing reads the freed memory. An ImageID is
// never reused, so a raster later made in the same memory has a new one
// and never draws a texture made from the old pixels.

// fence tracks the open recordings and the frees waiting for them.
type fence struct {
	mu sync.Mutex
	// next is the sequence number the next recording opens with.
	next uint64
	// open are the open recordings' sequence numbers.
	open map[uint64]struct{}
	// retired are the frees not yet run, in Retire order, so their
	// cutoffs never decrease.
	retired []retirement
}

// recordings is the process's fence: every List is open in it.
var recordings fence

// retirement is a free waiting for every recording numbered below
// cutoff to end.
type retirement struct {
	cutoff uint64
	free   func()
}

// recording is a List's place in its fence: its sequence number while
// it is open, guarded by the fence's lock. It is the List's cleanup
// argument, so it must not refer to the List.
type recording struct {
	f    *fence
	seq  uint64
	open bool
}

// newRecording opens a recording in f for a new List l, ended when l is
// collected without having ended.
func (f *fence) newRecording(l *List) *recording {
	r := &recording{f: f}
	r.restart()
	runtime.AddCleanup(l, (*recording).end, r)
	return r
}

// restart ends r if it is open and opens it again as the newest
// recording, then runs the frees its end released.
func (r *recording) restart() {
	f := r.f
	f.mu.Lock()
	r.close()
	r.seq, r.open = f.next, true
	f.next++
	if f.open == nil {
		f.open = map[uint64]struct{}{}
	}
	f.open[r.seq] = struct{}{}
	run := f.ready()
	f.mu.Unlock()
	runAll(run)
}

// end ends r if it is open and runs the frees that released.
func (r *recording) end() {
	f := r.f
	f.mu.Lock()
	r.close()
	run := f.ready()
	f.mu.Unlock()
	runAll(run)
}

// close removes r from the open recordings; the fence's lock is held.
func (r *recording) close() {
	if r.open {
		delete(r.f.open, r.seq)
		r.open = false
	}
}

// retire queues free behind every recording open now and runs what is
// ready.
func (f *fence) retire(free func()) {
	f.mu.Lock()
	f.retired = append(f.retired, retirement{cutoff: f.next, free: free})
	run := f.ready()
	f.mu.Unlock()
	runAll(run)
}

// ready takes the frees no open recording holds back off the queue;
// the lock is held.
func (f *fence) ready() []func() {
	oldest := f.next
	for seq := range f.open {
		oldest = min(oldest, seq)
	}
	n := 0
	for n < len(f.retired) && f.retired[n].cutoff <= oldest {
		n++
	}
	if n == 0 {
		return nil
	}
	run := make([]func(), n)
	for i, rt := range f.retired[:n] {
		run[i] = rt.free
	}
	f.retired = append(f.retired[:0], f.retired[n:]...)
	return run
}

func runAll(frees []func()) {
	for _, free := range frees {
		free()
	}
}

// Retire runs free, which releases the memory of rasters their owner
// has already made unreachable for new draws, once every List whose
// recording started before the call has ended: at once, on the calling
// goroutine, when none is open; otherwise on the goroutine that ends
// the last of them. Frees run in the order they were retired. It is
// safe from any goroutine.
func Retire(free func()) { recordings.retire(free) }

// End ends l's recording after its last Play, releasing the frees
// retired while it was open; neither recording into l nor playing it
// is allowed afterwards, short of ResetRecorder. A renderer whose
// canvas repaints rarely ends each frame's List once it is drawn, so
// it holds no free back while idle. Ending an ended List does nothing.
func (l *List) End() {
	l.ended = true
	l.pending.end()
}
