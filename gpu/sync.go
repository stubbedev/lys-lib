package gpu

import (
	"errors"
	"fmt"
	"runtime"
	"slices"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-lib/drm"
	"github.com/stubbedev/lys-lib/intconv"
	"github.com/stubbedev/lys-lib/third_party/vk"
)

// ErrNoSyncFile reports a device that exports or imports no sync_file.
var ErrNoSyncFile = errors.New("gpu: the device has no sync_file semaphores")

// ErrBusy reports a frame Render refused, having done nothing, because
// the GPU is too far behind for one more: try again once the frames ahead
// of it finish, as FrameDone or a compositor's release will show.
var ErrBusy = errors.New("gpu: frames in flight are at their bound")

// ErrNoFrameTime reports a device whose GPU timestamps do not map onto
// CLOCK_MONOTONIC (no calibrated timestamps).
var ErrNoFrameTime = errors.New("gpu: the device has no calibrated timestamps")

const queryRing = 64

// fenceTimeout bounds a wait for the GPU: past it the GPU has hung and the
// wait is an error instead of a frozen process.
const fenceTimeout = 10 * time.Second

type timing struct {
	timingExts []string
	syncFiles  bool
	// implicitFences is whether a target's dmabuf carries a fence for its
	// consumer, so that handing it over does not wait.
	implicitFences bool
	calibrated     bool
	period         float64
	validBits      uint32
	lost           bool
	queries        vk.QueryPool
	nextQuery      uint32
	inflight       []inflight
	submitted      uint64
	completed      uint64
	graves         []grave
	blockingWaits  uint64
	opaqueRuns     uint64
	framing        bool
	reaps          uint64
	incomplete     bool
	waitLimit      time.Duration
	waits          []vk.Semaphore
	passes         uint64
	done           uint64
	doneQuery      int
	timed          uint64
	doneAt         time.Duration
	clocks         clockSample
	clocksAt       time.Time
	doneErr        error
	// heldSemaphores are what the submissions in flight wait on and
	// signal, the oldest submission's first.
	heldSemaphores []vk.Semaphore
	// timelineWaits are the points on other queues' timeline semaphores
	// the next submission waits for (awaitTimeline), timelineSignals the
	// points it signals for them (signalTimeline).
	timelineWaits   []timelineWait
	timelineSignals []timelineWait
	// fault is a result a test makes the device's next submission, on
	// any of its queues, return in place of submitting (LoseForTesting);
	// VK_SUCCESS, the zero value, for none. faultAfter is how many
	// submissions go through before that one.
	fault      vk.Result
	faultAfter int
	// spareFences, spareBuffers and spareSemaphores are the objects of
	// finished submissions, for the next ones.
	spareFences     []vk.Fence
	spareBuffers    []vk.CommandBuffer
	spareSemaphores []vk.Semaphore
	// batch is the open command buffer holding the uploads no submission
	// carries yet, batched of them in all, ending at batchEnd in the
	// staging ring. The next submission takes them along, so a frame's
	// small uploads and its pass reach the kernel together; flushBatchBytes
	// of them go at once, for the GPU to finish before the frame needs them.
	// batchPass marks a batch holding ended passes, the newest timed by
	// batchQuery (-1 for none): they reach the queue in the one submission
	// that takes the batch along.
	batch      vk.CommandBuffer
	batchOps   int
	batched    int
	batchEnd   int
	batchBytes int
	batchPass  bool
	batchQuery int
	// batchTargets are the targets the batch's passes drew into, held
	// until the batch ends and handed to their consumers then: a release
	// and reacquire within one command buffer leaves the image's layout
	// where the release found it.
	batchTargets []*Target
	// frameSignal is the semaphore the newest pass signals that no export
	// has consumed, held out of the spares; passes signal one once
	// passesExport says SyncFile is in use. lastSyncFile is the sync_file
	// that covers everything submitted, when lastSyncFileSet.
	frameSignal     vk.Semaphore
	passesExport    bool
	lastSyncFile    int
	lastSyncFileSet bool
	// args is what commands point C at. C reads a pointer argument only
	// during the call, and one on the stack would escape to the heap,
	// so the device keeps one of each.
	args callArgs
}

// callArgs holds the pointer arguments of the commands every frame runs.
type callArgs struct {
	fence       vk.Fence
	cb          vk.CommandBuffer
	stages      []vk.PipelineStageFlags
	begin       vk.CommandBufferBeginInfo
	submit      vk.SubmitInfo
	imp         vk.ImportSemaphoreFdInfoKHR
	getFd       vk.SemaphoreGetFdInfoKHR
	fd          int32
	barrier     vk.ImageMemoryBarrier
	memory      vk.MemoryBarrier
	allocCB     vk.CommandBufferAllocateInfo
	createFence vk.FenceCreateInfo
	region      vk.BufferImageCopy
	copy        vk.ImageCopy
	passBegin   vk.RenderPassBeginInfo
	viewport    vk.Viewport
	scissor     vk.Rect2D
	frame       frameConstants
	clear       vk.ClearAttachment
	clearRects  []vk.ClearRect
	timestamp   uint64
	infos       [2]vk.CalibratedTimestampInfoKHR
	now         [2]uint64
	deviation   uint64
	// waits and values are a submission's wait semaphores and the values
	// it waits for on the timeline ones among them, signals and
	// signalValues what it signals, with timeline to say so; waitInfo,
	// timelineSemaphore and timelineValue are a wait for, or a look at,
	// one timeline semaphore from here.
	waits             []vk.Semaphore
	values            []uint64
	signals           []vk.Semaphore
	signalValues      []uint64
	timeline          vk.TimelineSemaphoreSubmitInfo
	waitInfo          vk.SemaphoreWaitInfo
	timelineSemaphore vk.Semaphore
	timelineValue     uint64
	// planes are the copies of an encoder's converted planes. dependency
	// to signal2 are barrier2's and submit2's: the video queues' and the
	// encoder's conversion's, every frame.
	planes     [2]vk.BufferImageCopy
	dependency vk.DependencyInfo
	memory2    vk.MemoryBarrier2
	images2    []vk.ImageMemoryBarrier2
	buffer2    vk.CommandBufferSubmitInfo
	submit2    vk.SubmitInfo2
	wait2      vk.SemaphoreSubmitInfo
	signal2    vk.SemaphoreSubmitInfo
}

type inflight struct {
	seq   uint64
	fence vk.Fence
	cb    vk.CommandBuffer
	// sems counts this submission's share of heldSemaphores.
	sems  int
	query int
	pass  uint64
	// buffers is the frame segment this submission reads, nil for none.
	buffers *frameBuffers
	// staged marks a submission reading the staging ring up to
	// stagingEnd.
	staged     bool
	stagingEnd int
}

func (d *Device) probeTiming(p vk.PhysicalDevice, have map[string]bool, props *vk.PhysicalDeviceProperties, fam vk.QueueFamilyProperties) []string {
	d.syncFiles, d.calibrated = false, false
	var exts []string
	if have["VK_KHR_external_semaphore_fd"] {
		info := vk.PhysicalDeviceExternalSemaphoreInfo{SType: vk.StructureTypePhysicalDeviceExternalSemaphoreInfo, HandleType: vk.ExternalSemaphoreHandleTypeSyncFdBit}
		out := vk.ExternalSemaphoreProperties{SType: vk.StructureTypeExternalSemaphoreProperties}
		d.cmds.GetPhysicalDeviceExternalSemaphoreProperties(p, &info, &out)
		both := vk.ExternalSemaphoreFeatureFlags(vk.ExternalSemaphoreFeatureExportableBit | vk.ExternalSemaphoreFeatureImportableBit)
		if out.ExternalSemaphoreFeatures&both == both {
			d.syncFiles = true
			exts = append(exts, "VK_KHR_external_semaphore_fd")
		}
	}
	ext := ""
	for _, e := range []string{"VK_KHR_calibrated_timestamps", "VK_EXT_calibrated_timestamps"} {
		if have[e] {
			ext = e
			break
		}
	}
	if ext != "" && fam.TimestampValidBits > 0 && props.Limits.TimestampPeriod > 0 && d.monotonicDomain(p) {
		d.calibrated, d.period, d.validBits = true, float64(props.Limits.TimestampPeriod), fam.TimestampValidBits
		exts = append(exts, ext)
	}
	return exts
}

func (d *Device) monotonicDomain(p vk.PhysicalDevice) bool {
	var n uint32
	if d.cmds.GetPhysicalDeviceCalibrateableTimeDomainsKHR(p, &n, nil) != vk.Success || n == 0 {
		return false
	}
	domains := make([]vk.TimeDomainKHR, n)
	if d.cmds.GetPhysicalDeviceCalibrateableTimeDomainsKHR(p, &n, &domains[0]) != vk.Success {
		return false
	}
	domains = domains[:n]
	return slices.Contains(domains, vk.TimeDomainDeviceKhr) && slices.Contains(domains, vk.TimeDomainClockMonotonicKhr)
}

// SyncFiles reports whether SyncFile and WaitSyncFile work here.
func (d *Device) SyncFiles() bool { return d.syncFiles }

// spareSemaphore is an unsignalled binary semaphore that exports
// sync_files and takes temporary imports. One whose submission finished
// is unsignalled again: its wait consumed the import, or its export
// consumed the signal.
func (d *Device) spareSemaphore() (vk.Semaphore, error) {
	if n := len(d.spareSemaphores); n > 0 {
		s := d.spareSemaphores[n-1]
		d.spareSemaphores = d.spareSemaphores[:n-1]
		return s, nil
	}
	exp := vk.ExportSemaphoreCreateInfo{SType: vk.StructureTypeExportSemaphoreCreateInfo, HandleTypes: vk.ExternalSemaphoreHandleTypeFlags(vk.ExternalSemaphoreHandleTypeSyncFdBit)}
	sci := vk.SemaphoreCreateInfo{SType: vk.StructureTypeSemaphoreCreateInfo, PNext: (*uintptr)(unsafe.Pointer(&exp))}
	var s vk.Semaphore
	r := d.cmds.CreateSemaphore(d.device, &sci, nil, &s)
	runtime.KeepAlive(&exp)
	if r != vk.Success {
		return 0, fmt.Errorf("gpu: vkCreateSemaphore: %w", resultError(r))
	}
	return s, nil
}

// WaitSyncFile makes the device's next submission (the one carrying the
// passes ended since the last, an upload, a copy) wait for the fence in
// the sync_file fd before it runs: a client's acquire fence, or a DRM
// syncobj point exported as one. The device takes fd, closing it even on
// error; -1 is a fence already signalled, and one that has signalled
// costs the submission nothing.
func (d *Device) WaitSyncFile(fd int) error {
	if !d.syncFiles {
		if fd >= 0 {
			_ = unix.Close(fd)
		}
		return ErrNoSyncFile
	}
	if fd < 0 {
		return nil
	}
	signalled, err := drm.SyncFileSignalled(fd)
	if err != nil {
		_ = unix.Close(fd)
		return fmt.Errorf("gpu: fd %d is no sync_file: %w", fd, err)
	}
	if signalled {
		_ = unix.Close(fd)
		return nil
	}
	s, err := d.spareSemaphore()
	if err != nil {
		_ = unix.Close(fd)
		return err
	}
	info := &d.args.imp
	*info = vk.ImportSemaphoreFdInfoKHR{
		SType: vk.StructureTypeImportSemaphoreFdInfoKhr, Semaphore: s, Flags: vk.SemaphoreImportFlags(vk.SemaphoreImportTemporaryBit),
		HandleType: vk.ExternalSemaphoreHandleTypeSyncFdBit, Fd: intconv.Must[int32](fd),
	}
	if r := d.cmds.ImportSemaphoreFdKHR(d.device, info); r != vk.Success {
		d.spareSemaphores = append(d.spareSemaphores, s)
		_ = unix.Close(fd)
		return fmt.Errorf("gpu: vkImportSemaphoreFdKHR sync_file %d: %w", fd, resultError(r))
	}
	d.waits = append(d.waits, s)
	return nil
}

// SyncFile returns a sync_file, the caller's to close, that signals
// once everything submitted to the device so far has finished: a
// pass's out-fence for KMS or a linux-drm-syncobj release point. -1 with
// no error means it has all finished already: there is nothing to wait
// on, as for WaitSyncFile. It costs a submission only when nothing submitted can answer: the newest
// pass signals a semaphore once SyncFile has been asked for, and until
// the next submission every later call shares that one fence.
func (d *Device) SyncFile() (int, error) {
	if !d.syncFiles {
		return -1, ErrNoSyncFile
	}
	if len(d.waits) == 0 && len(d.timelineWaits) == 0 && d.batch == 0 {
		if d.lastSyncFileSet {
			return dupSyncFile(d.lastSyncFile)
		}
		if d.frameSignal != 0 {
			fd, err := d.exportFrameSignal()
			if err != nil {
				return -1, err
			}
			return d.keepSyncFile(fd)
		}
	}
	s, err := d.spareSemaphore()
	if err != nil {
		return -1, err
	}
	cb, err := d.endBatch()
	if err != nil {
		d.spareSemaphores = append(d.spareSemaphores, s)
		return -1, err
	}
	if err := d.queue1(cb, s, submission{}); err != nil {
		if cb != 0 {
			d.dropBatch(cb)
		}
		d.spareSemaphores = append(d.spareSemaphores, s)
		return -1, err
	}
	d.passesExport = true
	fd, err := d.exportSemaphore(s)
	if err != nil {
		d.forgetSignal()
		return -1, err
	}
	return d.keepSyncFile(fd)
}

func (d *Device) exportSemaphore(s vk.Semaphore) (int, error) {
	gi := &d.args.getFd
	*gi = vk.SemaphoreGetFdInfoKHR{SType: vk.StructureTypeSemaphoreGetFdInfoKhr, Semaphore: s, HandleType: vk.ExternalSemaphoreHandleTypeSyncFdBit}
	if r := d.cmds.GetSemaphoreFdKHR(d.device, gi, &d.args.fd); r != vk.Success {
		return -1, fmt.Errorf("gpu: vkGetSemaphoreFdKHR (sync_file): %w", resultError(r))
	}
	fd := int(d.args.fd)
	if fd < 0 {
		return fd, nil
	}
	if err := closeOnExec(fd); err != nil {
		_ = unix.Close(fd)
		return -1, err
	}
	return fd, nil
}

// exportFrameSignal exports the newest pass's signal, and files its semaphore with the
// submission that signals it. Nothing has been submitted since that
// pass, so the newest in flight is that pass, or it has finished.
func (d *Device) exportFrameSignal() (int, error) {
	s := d.frameSignal
	d.frameSignal = 0
	fd, err := d.exportSemaphore(s)
	if err != nil {
		_ = d.idle()
		d.cmds.DestroySemaphore(d.device, s, nil)
		return -1, err
	}
	n := len(d.inflight)
	if n == 0 {
		d.spareSemaphores = append(d.spareSemaphores, s)
		return fd, nil
	}
	d.heldSemaphores = append(d.heldSemaphores, s)
	d.inflight[n-1].sems++
	return fd, nil
}

// keepSyncFile holds fd for later calls and returns a duplicate for the
// caller to own. The spec lets a driver export -1 for a signal that has
// completed, and lavapipe without a device node exports it for a pending
// one, so -1 waits for the queue and then is true on either driver.
func (d *Device) keepSyncFile(fd int) (int, error) {
	if fd < 0 {
		if err := d.idle(); err != nil {
			return -1, err
		}
		d.lastSyncFile, d.lastSyncFileSet = -1, true
		return -1, nil
	}
	out, err := dupSyncFile(fd)
	if err != nil {
		_ = unix.Close(fd)
		return -1, err
	}
	d.lastSyncFile, d.lastSyncFileSet = fd, true
	return out, nil
}

func dupSyncFile(fd int) (int, error) {
	if fd < 0 {
		return -1, nil
	}
	out, err := unix.FcntlInt(uintptr(fd), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return -1, fmt.Errorf("gpu: duplicating sync_file %d: %w", fd, err)
	}
	return out, nil
}

func (d *Device) closeLastSyncFile() {
	if d.lastSyncFileSet && d.lastSyncFile >= 0 {
		_ = unix.Close(d.lastSyncFile)
	}
	d.lastSyncFileSet = false
}

// retireSyncFile forgets what SyncFile could answer from, for a new
// submission that it would not cover. A pass nobody asked about has its
// signal consumed, and the next pass signals nothing until SyncFile is
// called again.
func (d *Device) retireSyncFile() error {
	d.closeLastSyncFile()
	if d.frameSignal == 0 {
		return nil
	}
	d.passesExport = false
	fd, err := d.exportFrameSignal()
	if err != nil {
		return err
	}
	if fd >= 0 {
		_ = unix.Close(fd)
	}
	return nil
}

// forgetSignal destroys the semaphore the newest submission signals,
// whose export failed: nothing consumed the signal, so it cannot be
// used again.
func (d *Device) forgetSignal() {
	f := &d.inflight[len(d.inflight)-1]
	last := len(d.heldSemaphores) - 1
	s := d.heldSemaphores[last]
	d.heldSemaphores, f.sems = d.heldSemaphores[:last], f.sems-1
	_ = d.idle()
	d.cmds.DestroySemaphore(d.device, s, nil)
}

// timelineWait is a point on a timeline semaphore: the value a
// submission waits for it to reach.
type timelineWait struct {
	semaphore vk.Semaphore
	value     uint64
}

// timelineSemaphore makes a timeline semaphore at zero: what another
// queue's submissions signal with rising values, for the graphics queue
// (awaitTimeline) or this goroutine (waitTimeline) to wait on any of
// them, any number of times. The device has them where it decodes video.
func (d *Device) timelineSemaphore() (vk.Semaphore, error) {
	kind := vk.SemaphoreTypeCreateInfo{SType: vk.StructureTypeSemaphoreTypeCreateInfo, SemaphoreType: vk.SemaphoreTypeTimeline}
	sci := vk.SemaphoreCreateInfo{SType: vk.StructureTypeSemaphoreCreateInfo, PNext: (*uintptr)(unsafe.Pointer(&kind))}
	var s vk.Semaphore
	r := d.cmds.CreateSemaphore(d.device, &sci, nil, &s)
	runtime.KeepAlive(&kind)
	if r != vk.Success {
		return 0, fmt.Errorf("gpu: vkCreateSemaphore (timeline): %w", resultError(r))
	}
	return s, nil
}

// awaitTimeline makes the device's next submission (the one carrying
// the passes ended since the last, an upload, a copy) wait on the GPU
// until w's timeline semaphore reaches w's value: for the work of
// another queue that signals it, as a video decode does for the picture
// a pass samples. Nothing waits here, and the zero value asks for no
// wait. The semaphore stays its owner's, who retires it once done
// (retireTimeline) instead of destroying it under a submission.
func (d *Device) awaitTimeline(w timelineWait) {
	if w.semaphore == 0 {
		return
	}
	for i := range d.timelineWaits {
		if have := &d.timelineWaits[i]; have.semaphore == w.semaphore {
			have.value = max(have.value, w.value)
			return
		}
	}
	d.timelineWaits = append(d.timelineWaits, w)
}

// signalTimeline makes the device's next submission signal w's timeline
// semaphore with w's value when it finishes: for another queue's work
// to wait on, as a video encode does for the conversion of the frame it
// codes. Values rise from one call to the next.
func (d *Device) signalTimeline(w timelineWait) {
	d.timelineSignals = append(d.timelineSignals, w)
}

// retireTimeline destroys s once the submissions that wait for it have
// finished. Its owner has seen every value it signals reached, so the
// submission to come waits for it no longer.
func (d *Device) retireTimeline(s vk.Semaphore) {
	d.timelineWaits = slices.DeleteFunc(d.timelineWaits, func(w timelineWait) bool { return w.semaphore == s })
	d.retire(&retiredSemaphore{d: d, semaphore: s})
}

// retiredSemaphore is a semaphore to destroy once nothing in flight
// waits on it.
type retiredSemaphore struct {
	d         *Device
	semaphore vk.Semaphore
}

func (r *retiredSemaphore) destroy() { r.d.cmds.DestroySemaphore(r.d.device, r.semaphore, nil) }

// timelineValue is the value s has reached, without waiting.
func (d *Device) timelineValue(s vk.Semaphore) (uint64, error) {
	if err := d.checkLost(); err != nil {
		return 0, err
	}
	a := &d.args
	if r := d.cmds.GetSemaphoreCounterValue(d.device, s, &a.timelineValue); r != vk.Success {
		if r == vk.ErrorDeviceLost {
			d.lost = true
		}
		return 0, fmt.Errorf("gpu: vkGetSemaphoreCounterValue: %w", resultError(r))
	}
	return a.timelineValue, nil
}

// waitTimeline waits here until s reaches value, the device's wait
// limit at most.
func (d *Device) waitTimeline(s vk.Semaphore, value uint64) error {
	if err := d.checkLost(); err != nil {
		return err
	}
	d.blockingWaits++
	a := &d.args
	a.timelineSemaphore, a.timelineValue = s, value
	a.waitInfo = vk.SemaphoreWaitInfo{SType: vk.StructureTypeSemaphoreWaitInfo, SemaphoreCount: 1, PSemaphores: &a.timelineSemaphore, PValues: &a.timelineValue}
	if r := d.cmds.WaitSemaphores(d.device, &a.waitInfo, intconv.Must[uint64](d.waitLimit.Nanoseconds())); r != vk.Success {
		if r == vk.ErrorDeviceLost {
			d.lost = true
		}
		return fmt.Errorf("gpu: vkWaitSemaphores for value %d, %v at most: %w", value, d.waitLimit, resultError(r))
	}
	return nil
}

// queue1 submits cb (0 for none) after the pending waits, signalling
// signal (0 for none) and the pending timeline signals. cb carries the
// open batch, and with it the passes it holds. A submission that fails
// takes its waits and signals with it: they were its own, and the next
// one neither waits on a fence meant for work that never ran nor
// signals for it.
func (d *Device) queue1(cb vk.CommandBuffer, signal vk.Semaphore, sub submission) error {
	err := d.submit1(cb, signal, sub)
	if err != nil {
		d.dropWaits()
	}
	return err
}

// dropWaits forgets what the next submission was to wait for and
// signal. The semaphores holding imported sync_files are destroyed:
// nothing waited on them, so they still hold their fences.
func (d *Device) dropWaits() {
	for _, s := range d.waits {
		d.cmds.DestroySemaphore(d.device, s, nil)
	}
	d.waits, d.timelineWaits, d.timelineSignals = d.waits[:0], d.timelineWaits[:0], d.timelineSignals[:0]
}

// takeFault is the result a test set for the next submission, which it
// then is no more: VK_SUCCESS for none, or while faultAfter submissions
// are still to go through first.
func (d *Device) takeFault() vk.Result {
	if d.fault != vk.Success && d.faultAfter > 0 {
		d.faultAfter--
		return vk.Success
	}
	r := d.fault
	d.fault = vk.Success
	return r
}

// submitResult is the result r of a submission as an error naming call,
// nil for VK_SUCCESS. A device the driver reports lost is remembered:
// nothing submitted to it after runs.
func (d *Device) submitResult(call string, r vk.Result) error {
	if r == vk.Success {
		return nil
	}
	if r == vk.ErrorDeviceLost {
		d.lost = true
	}
	return fmt.Errorf("gpu: %s: %w", call, resultError(r))
}

// LoseForTesting arms a device loss, for tests of what handles one: the
// device's next submission on any of its queues (a pass's, an upload's,
// a video decode's or encode's) fails with ErrDeviceLost as if the
// driver had answered VK_ERROR_DEVICE_LOST, and so does everything
// after it that submits or waits for the GPU, a VideoDecoder's and an
// Encoder's calls too, until the device is closed. The GPU itself is
// untouched: the next device opened on it works. A device nobody armed
// never takes this path.
func (d *Device) LoseForTesting() { d.fault = vk.ErrorDeviceLost }

func (d *Device) submit1(cb vk.CommandBuffer, signal vk.Semaphore, sub submission) error {
	if err := d.retireSyncFile(); err != nil {
		return err
	}
	if err := d.checkLost(); err != nil {
		return err
	}
	if len(d.inflight) >= queryRing {
		d.reap(false)
	}
	for len(d.inflight) >= queryRing {
		if err := d.waitOldest(); err != nil {
			return err
		}
	}
	fence, err := d.spareFence()
	if err != nil {
		return err
	}
	pass := d.batchPass
	frame := signal == 0 && (sub.signals || pass && d.passesExport)
	if frame {
		if signal, err = d.spareSemaphore(); err != nil {
			d.spareFences = append(d.spareFences, fence)
			return err
		}
	}
	a := &d.args
	si := &a.submit
	*si = vk.SubmitInfo{SType: vk.StructureTypeSubmitInfo}
	a.timeline = vk.TimelineSemaphoreSubmitInfo{SType: vk.StructureTypeTimelineSemaphoreSubmitInfo}
	a.waits = append(a.waits[:0], d.waits...)
	if len(d.timelineWaits) > 0 {
		a.values = append(a.values[:0], make([]uint64, len(d.waits))...)
		for _, w := range d.timelineWaits {
			a.waits, a.values = append(a.waits, w.semaphore), append(a.values, w.value)
		}
		a.timeline.WaitSemaphoreValueCount, a.timeline.PWaitSemaphoreValues = count(a.values), &a.values[0]
		si.PNext = (*uintptr)(unsafe.Pointer(&a.timeline))
	}
	a.signals = a.signals[:0]
	if signal != 0 {
		a.signals = append(a.signals, signal)
	}
	if len(d.timelineSignals) > 0 {
		a.signalValues = append(a.signalValues[:0], make([]uint64, len(a.signals))...)
		for _, w := range d.timelineSignals {
			a.signals, a.signalValues = append(a.signals, w.semaphore), append(a.signalValues, w.value)
		}
		a.timeline.SignalSemaphoreValueCount, a.timeline.PSignalSemaphoreValues = count(a.signalValues), &a.signalValues[0]
		si.PNext = (*uintptr)(unsafe.Pointer(&a.timeline))
	}
	if n := len(a.signals); n > 0 {
		si.SignalSemaphoreCount, si.PSignalSemaphores = count(a.signals), &a.signals[0]
	}
	if n := len(a.waits); n > 0 {
		for len(a.stages) < n {
			a.stages = append(a.stages, vk.PipelineStageFlags(vk.PipelineStageAllCommandsBit))
		}
		si.WaitSemaphoreCount, si.PWaitSemaphores, si.PWaitDstStageMask = count(a.waits), &a.waits[0], &a.stages[0]
	}
	if cb != 0 {
		a.cb = cb
		si.CommandBufferCount, si.PCommandBuffers = 1, &a.cb
	}
	r := d.takeFault()
	if r == vk.Success {
		r = d.cmds.QueueSubmit(d.queue, 1, si, fence)
	}
	if err := d.submitResult("vkQueueSubmit", r); err != nil {
		d.cmds.DestroyFence(d.device, fence, nil)
		if frame {
			d.spareSemaphores = append(d.spareSemaphores, signal)
		}
		return err
	}
	d.submitted++
	f := inflight{seq: d.submitted, fence: fence, cb: cb, sems: len(d.waits), query: -1}
	if pass {
		d.passes++
		f.pass, f.query = d.passes, d.batchQuery
	}
	d.batchOps, d.batchPass = 0, false
	if d.batched > 0 {
		f.staged, f.stagingEnd, d.batched, d.batchBytes = true, d.batchEnd, 0, 0
	}
	d.heldSemaphores = append(d.heldSemaphores, d.waits...)
	d.waits, d.timelineWaits, d.timelineSignals = d.waits[:0], d.timelineWaits[:0], d.timelineSignals[:0]
	switch {
	case frame:
		d.frameSignal = signal
	case signal != 0:
		d.heldSemaphores = append(d.heldSemaphores, signal)
		f.sems++
	}
	d.inflight = append(d.inflight, f)
	return nil
}

// submission is what a submission is besides its commands: whether it
// must signal the frame semaphore for an export right after it.
type submission struct {
	signals bool
}

func (d *Device) submitAsync(record func(cb vk.CommandBuffer), sub submission) error {
	cb, err := d.recording()
	if err != nil {
		return err
	}
	record(cb)
	d.releaseTargets(cb)
	if r := d.cmds.EndCommandBuffer(cb); r != vk.Success {
		d.dropBatch(cb)
		return fmt.Errorf("gpu: vkEndCommandBuffer: %w", resultError(r))
	}
	if err := d.queue1(cb, 0, sub); err != nil {
		d.dropBatch(cb)
		return err
	}
	return nil
}

// batchPassCommands records a pass into the open batch, timing it on
// the batch's query, which the newest pass in the batch overwrites.
func (d *Device) batchPassCommands(record func(cb vk.CommandBuffer)) error {
	if err := d.checkLost(); err != nil {
		return err
	}
	cb, err := d.batchCommands()
	if err != nil {
		return err
	}
	if !d.batchPass {
		d.batchQuery = -1
	}
	if d.calibrated {
		if err := d.ensureQueries(); err != nil {
			return err
		}
		if d.batchQuery < 0 {
			d.batchQuery = int(d.nextQuery)
			d.nextQuery = (d.nextQuery + 1) % queryRing
		}
		d.cmds.CmdResetQueryPool(cb, d.queries, intconv.Must[uint32](d.batchQuery), 1)
	}
	record(cb)
	if d.batchQuery >= 0 {
		d.cmds.CmdWriteTimestamp(cb, vk.PipelineStageBottomOfPipeBit, d.queries, intconv.Must[uint32](d.batchQuery))
	}
	d.batchPass = true
	return d.endBatchOp()
}

// holdTarget keeps t, which a pass in the open batch drew into, from
// its consumer until the batch ends.
func (d *Device) holdTarget(t *Target) {
	if !slices.Contains(d.batchTargets, t) {
		d.batchTargets = append(d.batchTargets, t)
	}
}

// releaseTargets records handing the targets the open batch held back
// to their consumers, at the end of cb.
func (d *Device) releaseTargets(cb vk.CommandBuffer) {
	for _, t := range d.batchTargets {
		if t.held {
			t.release(cb)
		}
	}
	clear(d.batchTargets)
	d.batchTargets = d.batchTargets[:0]
}

// recording is a command buffer to record into, the open batch's if
// there is one, which the caller now owns.
func (d *Device) recording() (vk.CommandBuffer, error) {
	if d.batch != 0 {
		cb := d.batch
		d.batch = 0
		return cb, nil
	}
	cb, err := d.spareBuffer()
	if err != nil {
		return 0, err
	}
	if err := begin(d, cb); err != nil {
		d.spareBuffers = append(d.spareBuffers, cb)
		return 0, err
	}
	return cb, nil
}

// endBatch ends the open batch's command buffer for a submission to
// carry; 0 when no uploads wait.
func (d *Device) endBatch() (vk.CommandBuffer, error) {
	if d.batch == 0 {
		return 0, nil
	}
	cb := d.batch
	d.batch = 0
	d.releaseTargets(cb)
	if r := d.cmds.EndCommandBuffer(cb); r != vk.Success {
		d.dropBatch(cb)
		return 0, fmt.Errorf("gpu: vkEndCommandBuffer: %w", resultError(r))
	}
	return cb, nil
}

// flushBatch submits the open batch, if any, on its own.
func (d *Device) flushBatch() error {
	cb, err := d.endBatch()
	if err != nil || cb == 0 {
		return err
	}
	if err := d.queue1(cb, 0, submission{}); err != nil {
		d.dropBatch(cb)
		return err
	}
	return nil
}

// Flush submits the passes ended and the uploads made since the last
// submission, without waiting for them: what a consumer that waits on
// no SyncFile calls before handing a target over. SyncFile, Finish and
// a CPU read submit them too.
func (d *Device) Flush() error { return d.flushBatch() }

// dropBatch takes back cb, whose submission failed, with the uploads it
// carried, their staging bytes free again, and what the submission was
// to wait for and signal.
func (d *Device) dropBatch(cb vk.CommandBuffer) {
	d.dropWaits()
	d.spareBuffers = append(d.spareBuffers, cb)
	d.batchOps, d.batchPass = 0, false
	if d.batched > 0 {
		d.batched, d.batchBytes = 0, 0
		d.uploads.pending--
	}
}

// spareBuffer is a command buffer for begin to overwrite.
func (d *Device) spareBuffer() (vk.CommandBuffer, error) {
	if n := len(d.spareBuffers); n > 0 {
		cb := d.spareBuffers[n-1]
		d.spareBuffers = d.spareBuffers[:n-1]
		return cb, nil
	}
	return d.commandBuffer(d.pool)
}

// spareFence is an unsignalled fence.
func (d *Device) spareFence() (vk.Fence, error) {
	if n := len(d.spareFences); n > 0 {
		f := d.spareFences[n-1]
		d.spareFences = d.spareFences[:n-1]
		return f, nil
	}
	return d.fence()
}

func (d *Device) ensureQueries() error {
	if d.queries != 0 {
		return nil
	}
	qci := vk.QueryPoolCreateInfo{SType: vk.StructureTypeQueryPoolCreateInfo, QueryType: vk.QueryTypeTimestamp, QueryCount: queryRing}
	if r := d.cmds.CreateQueryPool(d.device, &qci, nil, &d.queries); r != vk.Success {
		return fmt.Errorf("gpu: vkCreateQueryPool (timestamps): %w", resultError(r))
	}
	return nil
}

func (d *Device) waitFence(f vk.Fence) error {
	if err := d.checkLost(); err != nil {
		return err
	}
	d.blockingWaits++
	d.args.fence = f
	if r := d.cmds.WaitForFences(d.device, 1, &d.args.fence, vk.True, intconv.Must[uint64](d.waitLimit.Nanoseconds())); r != vk.Success {
		if r == vk.ErrorDeviceLost {
			d.lost = true
		}
		return fmt.Errorf("gpu: vkWaitForFences, %d submissions in flight, %v at most: %w", len(d.inflight), d.waitLimit, resultError(r))
	}
	return nil
}

func (d *Device) waitOldest() error {
	if err := d.waitFence(d.inflight[0].fence); err != nil {
		return err
	}
	d.reap(false)
	return d.checkLost()
}

// reached reports, without waiting, whether the device's submission seq
// has finished; an error for a device found lost.
func (d *Device) reached(seq uint64) (bool, error) {
	if seq > d.completed {
		d.reap(false)
	}
	return seq <= d.completed, d.checkLost()
}

// finished is how many of the oldest submissions have finished. One
// queue signals its fences in submission order, so the newest fence
// answers for all of them and otherwise the first unsignalled one ends
// the walk: asking enters the kernel on most drivers.
func (d *Device) finished() int {
	n := len(d.inflight)
	if n == 0 || d.fenceFinished(d.inflight[n-1].fence) {
		return n
	}
	for i := range n - 1 {
		if !d.fenceFinished(d.inflight[i].fence) {
			return i
		}
	}
	return n - 1
}

// fenceFinished asks whether f signalled. A device lost on the way is
// remembered for the next call that can report it.
func (d *Device) fenceFinished(f vk.Fence) bool {
	r := d.cmds.GetFenceStatus(d.device, f)
	if r == vk.ErrorDeviceLost {
		d.lost = true
	}
	return r == vk.Success
}

// checkLost reports a device an earlier submission or fence poll found
// lost: a driver may go on accepting submissions that never run.
func (d *Device) checkLost() error {
	if d.lost {
		return fmt.Errorf("gpu: the device was lost: %w", resultError(vk.ErrorDeviceLost))
	}
	return nil
}

func (d *Device) reap(all bool) {
	d.reaps++
	n := len(d.inflight)
	if !all {
		n = d.finished()
	}
	if n == 0 {
		return
	}
	fences, sems := len(d.spareFences), 0
	d.completed = d.inflight[n-1].seq
	for i := range d.inflight[:n] {
		f := &d.inflight[i]
		if f.pass > d.done {
			d.done, d.doneQuery = f.pass, f.query
		}
		if f.cb != 0 {
			d.spareBuffers = append(d.spareBuffers, f.cb)
		}
		if f.staged {
			d.uploads.release(f.stagingEnd)
		}
		if f.buffers != nil {
			d.retireFrameBuffers(f.buffers)
		}
		sems += f.sems
		d.spareFences = append(d.spareFences, f.fence)
	}
	d.spareSemaphores = append(d.spareSemaphores, d.heldSemaphores[:sems]...)
	d.heldSemaphores = slices.Delete(d.heldSemaphores, 0, sems)
	d.inflight = slices.Delete(d.inflight, 0, n)
	reset := d.spareFences[fences:]
	if d.cmds.ResetFences(d.device, count(reset), &reset[0]) != vk.Success {
		for _, f := range reset {
			d.cmds.DestroyFence(d.device, f, nil)
		}
		d.spareFences = d.spareFences[:fences]
	}
	d.buryFinished(d.completed)
}

func (d *Device) idle() error {
	if err := d.flushBatch(); err != nil {
		return err
	}
	if len(d.inflight) == 0 {
		d.buryAll()
		return d.checkLost()
	}
	if err := d.waitFence(d.inflight[len(d.inflight)-1].fence); err != nil {
		return err
	}
	d.reap(true)
	d.buryAll()
	return d.checkLost()
}

func (d *Device) frameTime(query int) (time.Duration, error) {
	if query < 0 {
		return 0, ErrNoFrameTime
	}
	a := &d.args
	if r := d.cmds.GetQueryPoolResults(d.device, d.queries, intconv.Must[uint32](query), 1, 8, unsafe.Pointer(&a.timestamp), 8, vk.QueryResultFlags(vk.QueryResult64Bit)); r != vk.Success {
		return 0, fmt.Errorf("gpu: vkGetQueryPoolResults (timestamp): %w", resultError(r))
	}
	if err := d.calibrate(false); err != nil {
		return 0, err
	}
	at, near := d.clocks.monotonic(a.timestamp, d.validBits, d.period)
	if near {
		return at, nil
	}
	if err := d.calibrate(true); err != nil {
		return 0, err
	}
	at, _ = d.clocks.monotonic(a.timestamp, d.validBits, d.period)
	return at, nil
}

// calibrate samples the device and monotonic clocks together, unless a
// sample is still fresh: the call enters the kernel on some drivers and
// the clocks drift far less than a frame between samples.
func (d *Device) calibrate(force bool) error {
	now := time.Now()
	if !force && !d.clocksAt.IsZero() && now.Sub(d.clocksAt) < clockSampleEvery {
		return nil
	}
	a := &d.args
	a.infos = [2]vk.CalibratedTimestampInfoKHR{
		{SType: vk.StructureTypeCalibratedTimestampInfoKhr, TimeDomain: vk.TimeDomainDeviceKhr},
		{SType: vk.StructureTypeCalibratedTimestampInfoKhr, TimeDomain: vk.TimeDomainClockMonotonicKhr},
	}
	if r := d.cmds.GetCalibratedTimestampsKHR(d.device, 2, &a.infos[0], &a.now[0], &a.deviation); r != vk.Success {
		return fmt.Errorf("gpu: vkGetCalibratedTimestampsKHR: %w", resultError(r))
	}
	d.clocks = clockSample{device: a.now[0], at: time.Duration(intconv.Must[int64](a.now[1]))}
	d.clocksAt = now
	return nil
}

// FrameDone reports when the last ended Pass finished on the GPU, on
// CLOCK_MONOTONIC: what a compositor's latch budget samples. It reports
// false while that pass is still running, or before any pass. A pass
// not yet submitted is submitted, as Flush does.
func (d *Device) FrameDone() (time.Duration, bool, error) {
	if !d.calibrated {
		return 0, false, ErrNoFrameTime
	}
	if d.batchPass {
		if err := d.flushBatch(); err != nil {
			return 0, false, err
		}
	}
	d.reap(false)
	if err := d.checkLost(); err != nil {
		return 0, false, err
	}
	if d.passes == 0 || d.done < d.passes {
		return 0, false, nil
	}
	if d.timed != d.done {
		d.timed = d.done
		d.doneAt, d.doneErr = d.frameTime(d.doneQuery)
	}
	return d.doneAt, d.doneErr == nil, d.doneErr
}

func (d *Device) closeTiming() {
	d.reap(true)
	d.batch = 0
	d.buryAll()
	for _, f := range d.spareFences {
		d.cmds.DestroyFence(d.device, f, nil)
	}
	d.spareFences, d.spareBuffers = nil, nil
	for _, s := range d.waits {
		d.cmds.DestroySemaphore(d.device, s, nil)
	}
	for _, s := range d.spareSemaphores {
		d.cmds.DestroySemaphore(d.device, s, nil)
	}
	if d.frameSignal != 0 {
		d.cmds.DestroySemaphore(d.device, d.frameSignal, nil)
		d.frameSignal = 0
	}
	d.closeLastSyncFile()
	d.waits, d.spareSemaphores = nil, nil
	if d.queries != 0 {
		d.cmds.DestroyQueryPool(d.device, d.queries, nil)
		d.queries = 0
	}
}

// Finish waits until everything submitted to the device has finished:
// a CPU read of a target's dmabuf, a test.
func (d *Device) Finish() error { return d.idle() }
