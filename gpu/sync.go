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

// ErrNoFrameTime reports a device whose GPU timestamps do not map onto
// CLOCK_MONOTONIC (no calibrated timestamps).
var ErrNoFrameTime = errors.New("gpu: the device has no calibrated timestamps")

const queryRing = 64

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
	// spareFences, spareBuffers and spareSemaphores are the objects of
	// finished submissions, for the next ones.
	spareFences     []vk.Fence
	spareBuffers    []vk.CommandBuffer
	spareSemaphores []vk.Semaphore
	// batch is the open command buffer holding the uploads no submission
	// carries yet, batched of them in all, ending at batchEnd in the
	// staging ring. The next submission takes them along, so a frame's
	// uploads and its pass reach the kernel together.
	batch    vk.CommandBuffer
	batched  int
	batchEnd int
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
	signal      vk.Semaphore
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
}

type inflight struct {
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

// WaitSyncFile makes the device's next submission (a Pass's End, an
// upload, a copy) wait for the fence in the sync_file fd before it
// runs: a client's acquire fence, or a DRM syncobj point exported as
// one. The device takes fd, closing it even on error; -1 is a fence
// already signalled, and one that has signalled costs the submission
// nothing.
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
	if len(d.waits) == 0 && d.batch == 0 {
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
	if err := d.queue1(cb, s, -1, submission{}); err != nil {
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
	return int(d.args.fd), nil
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

// queue1 submits cb (0 for none) after the pending waits, signalling
// signal (0 for none).
func (d *Device) queue1(cb vk.CommandBuffer, signal vk.Semaphore, query int, sub submission) error {
	if err := d.retireSyncFile(); err != nil {
		return err
	}
	d.reap(false)
	if err := d.checkLost(); err != nil {
		return err
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
	frame := signal == 0 && (sub.signals || sub.pass && d.passesExport)
	if frame {
		if signal, err = d.spareSemaphore(); err != nil {
			d.spareFences = append(d.spareFences, fence)
			return err
		}
	}
	a := &d.args
	si := &a.submit
	*si = vk.SubmitInfo{SType: vk.StructureTypeSubmitInfo}
	if n := len(d.waits); n > 0 {
		for len(a.stages) < n {
			a.stages = append(a.stages, vk.PipelineStageFlags(vk.PipelineStageAllCommandsBit))
		}
		si.WaitSemaphoreCount, si.PWaitSemaphores, si.PWaitDstStageMask = count(d.waits), &d.waits[0], &a.stages[0]
	}
	if cb != 0 {
		a.cb = cb
		si.CommandBufferCount, si.PCommandBuffers = 1, &a.cb
	}
	if signal != 0 {
		a.signal = signal
		si.SignalSemaphoreCount, si.PSignalSemaphores = 1, &a.signal
	}
	if r := d.cmds.QueueSubmit(d.queue, 1, si, fence); r != vk.Success {
		d.cmds.DestroyFence(d.device, fence, nil)
		if frame {
			d.spareSemaphores = append(d.spareSemaphores, signal)
		}
		return fmt.Errorf("gpu: vkQueueSubmit: %w", resultError(r))
	}
	f := inflight{fence: fence, cb: cb, sems: len(d.waits), query: query}
	if d.batched > 0 {
		f.staged, f.stagingEnd, d.batched = true, d.batchEnd, 0
	}
	d.heldSemaphores = append(d.heldSemaphores, d.waits...)
	d.waits = d.waits[:0]
	switch {
	case frame:
		d.frameSignal = signal
	case signal != 0:
		d.heldSemaphores = append(d.heldSemaphores, signal)
		f.sems++
	}
	if sub.pass {
		d.passes++
		f.pass = d.passes
	}
	d.inflight = append(d.inflight, f)
	return nil
}

// submission is what a submission is besides its commands: a Pass, which
// FrameDone times, and whether it must signal the frame semaphore for an
// export right after it.
type submission struct {
	pass, signals bool
}

func (d *Device) submitAsync(record func(cb vk.CommandBuffer), sub submission) error {
	cb, err := d.recording()
	if err != nil {
		return err
	}
	query := -1
	if sub.pass && d.calibrated {
		if err := d.ensureQueries(); err != nil {
			d.dropBatch(cb)
			return err
		}
		query = int(d.nextQuery)
		d.cmds.CmdResetQueryPool(cb, d.queries, d.nextQuery, 1)
		d.nextQuery = (d.nextQuery + 1) % queryRing
	}
	record(cb)
	if query >= 0 {
		d.cmds.CmdWriteTimestamp(cb, vk.PipelineStageBottomOfPipeBit, d.queries, intconv.Must[uint32](query))
	}
	if r := d.cmds.EndCommandBuffer(cb); r != vk.Success {
		d.dropBatch(cb)
		return fmt.Errorf("gpu: vkEndCommandBuffer: %w", resultError(r))
	}
	if err := d.queue1(cb, 0, query, sub); err != nil {
		d.dropBatch(cb)
		return err
	}
	return nil
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
	if err := d.queue1(cb, 0, -1, submission{}); err != nil {
		d.dropBatch(cb)
		return err
	}
	return nil
}

// dropBatch takes back cb, whose submission failed, with the uploads it
// carried: their staging bytes are free again.
func (d *Device) dropBatch(cb vk.CommandBuffer) {
	d.spareBuffers = append(d.spareBuffers, cb)
	if d.batched > 0 {
		d.batched = 0
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
	d.args.fence = f
	if r := d.cmds.WaitForFences(d.device, 1, &d.args.fence, vk.True, ^uint64(0)); r != vk.Success {
		return fmt.Errorf("gpu: vkWaitForFences: %w", resultError(r))
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

// checkLost reports a device an earlier fence poll found lost: a driver
// may go on accepting submissions that never run.
func (d *Device) checkLost() error {
	if d.lost {
		return fmt.Errorf("gpu: a submission's fence: %w", resultError(vk.ErrorDeviceLost))
	}
	return nil
}

func (d *Device) reap(all bool) {
	n := len(d.inflight)
	if !all {
		n = d.finished()
	}
	if n == 0 {
		return
	}
	fences, sems := len(d.spareFences), 0
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
}

func (d *Device) idle() error {
	if err := d.flushBatch(); err != nil {
		return err
	}
	if len(d.inflight) == 0 {
		return d.checkLost()
	}
	if err := d.waitFence(d.inflight[len(d.inflight)-1].fence); err != nil {
		return err
	}
	if r := d.cmds.QueueWaitIdle(d.queue); r != vk.Success {
		return fmt.Errorf("gpu: vkQueueWaitIdle: %w", resultError(r))
	}
	d.reap(true)
	return nil
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
// false while that pass is still running, or before any pass.
func (d *Device) FrameDone() (time.Duration, bool, error) {
	if !d.calibrated {
		return 0, false, ErrNoFrameTime
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
