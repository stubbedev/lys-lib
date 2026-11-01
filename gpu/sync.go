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
	calibrated bool
	period     float64
	validBits  uint32
	queries    vk.QueryPool
	nextQuery  uint32
	inflight   []inflight
	waits      []vk.Semaphore
	passes     uint64
	done       uint64
	doneQuery  int
	timed      uint64
	doneAt     time.Duration
	clocks     clockSample
	clocksAt   time.Time
	doneErr    error
	// heldSemaphores are what the submissions in flight wait on and
	// signal, the oldest submission's first.
	heldSemaphores []vk.Semaphore
	// spareFences, spareBuffers and spareSemaphores are the objects of
	// finished submissions, for the next ones.
	spareFences     []vk.Fence
	spareBuffers    []vk.CommandBuffer
	spareSemaphores []vk.Semaphore
	// args is what commands point C at. C reads a pointer argument only
	// during the call, and one on the stack would escape to the heap,
	// so the device keeps one of each.
	args callArgs
}

// callArgs holds the pointer arguments of the commands every frame runs.
type callArgs struct {
	fence     vk.Fence
	cb        vk.CommandBuffer
	signal    vk.Semaphore
	stages    []vk.PipelineStageFlags
	begin     vk.CommandBufferBeginInfo
	submit    vk.SubmitInfo
	imp       vk.ImportSemaphoreFdInfoKHR
	getFd     vk.SemaphoreGetFdInfoKHR
	fd        int32
	barrier   vk.ImageMemoryBarrier
	region    vk.BufferImageCopy
	passBegin vk.RenderPassBeginInfo
	viewport  vk.Viewport
	scissor   vk.Rect2D
	timestamp uint64
	infos     [2]vk.CalibratedTimestampInfoKHR
	now       [2]uint64
	deviation uint64
}

type inflight struct {
	fence vk.Fence
	cb    vk.CommandBuffer
	// sems counts this submission's share of heldSemaphores.
	sems  int
	query int
	pass  uint64
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
		return 0, fmt.Errorf("gpu: vkCreateSemaphore: %d", r)
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
		return fmt.Errorf("gpu: vkImportSemaphoreFdKHR sync_file %d: %d", fd, r)
	}
	d.waits = append(d.waits, s)
	return nil
}

// SyncFile returns a sync_file, the caller's to close, that signals
// once everything submitted to the device so far has finished: a
// pass's out-fence for KMS or a linux-drm-syncobj release point.
func (d *Device) SyncFile() (int, error) {
	if !d.syncFiles {
		return -1, ErrNoSyncFile
	}
	s, err := d.spareSemaphore()
	if err != nil {
		return -1, err
	}
	if err := d.queue1(0, s, -1, false); err != nil {
		d.spareSemaphores = append(d.spareSemaphores, s)
		return -1, err
	}
	gi := &d.args.getFd
	*gi = vk.SemaphoreGetFdInfoKHR{SType: vk.StructureTypeSemaphoreGetFdInfoKhr, Semaphore: s, HandleType: vk.ExternalSemaphoreHandleTypeSyncFdBit}
	if r := d.cmds.GetSemaphoreFdKHR(d.device, gi, &d.args.fd); r != vk.Success {
		d.forgetSignal()
		return -1, fmt.Errorf("gpu: vkGetSemaphoreFdKHR (sync_file): %d", r)
	}
	if d.args.fd < 0 {
		d.forgetSignal()
		return -1, errors.New("gpu: the driver exported no sync_file for a pending signal")
	}
	return int(d.args.fd), nil
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
func (d *Device) queue1(cb vk.CommandBuffer, signal vk.Semaphore, query int, pass bool) error {
	d.reap(false)
	for len(d.inflight) >= queryRing {
		if err := d.waitOldest(); err != nil {
			return err
		}
	}
	fence, err := d.spareFence()
	if err != nil {
		return err
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
		return fmt.Errorf("gpu: vkQueueSubmit: %d", r)
	}
	f := inflight{fence: fence, cb: cb, sems: len(d.waits), query: query}
	d.heldSemaphores = append(d.heldSemaphores, d.waits...)
	d.waits = d.waits[:0]
	if signal != 0 {
		d.heldSemaphores = append(d.heldSemaphores, signal)
		f.sems++
	}
	if pass {
		d.passes++
		f.pass = d.passes
	}
	d.inflight = append(d.inflight, f)
	return nil
}

func (d *Device) submitAsync(record func(cb vk.CommandBuffer), pass bool) error {
	cb, err := d.spareBuffer()
	if err != nil {
		return err
	}
	if err := begin(d, cb); err != nil {
		d.spareBuffers = append(d.spareBuffers, cb)
		return err
	}
	query := -1
	if pass && d.calibrated {
		if err := d.ensureQueries(); err != nil {
			d.spareBuffers = append(d.spareBuffers, cb)
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
		d.spareBuffers = append(d.spareBuffers, cb)
		return fmt.Errorf("gpu: vkEndCommandBuffer: %d", r)
	}
	if err := d.queue1(cb, 0, query, pass); err != nil {
		d.spareBuffers = append(d.spareBuffers, cb)
		return err
	}
	return nil
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
		return fmt.Errorf("gpu: vkCreateQueryPool (timestamps): %d", r)
	}
	return nil
}

func (d *Device) waitOldest() error {
	d.args.fence = d.inflight[0].fence
	if r := d.cmds.WaitForFences(d.device, 1, &d.args.fence, vk.True, ^uint64(0)); r != vk.Success {
		return fmt.Errorf("gpu: vkWaitForFences: %d", r)
	}
	d.reap(false)
	return nil
}

// finished is how many of the oldest submissions have finished. One
// queue signals its fences in submission order, so the newest fence
// answers for all of them and otherwise the first unsignalled one ends
// the walk: asking enters the kernel on most drivers.
func (d *Device) finished() int {
	n := len(d.inflight)
	if n == 0 || d.cmds.GetFenceStatus(d.device, d.inflight[n-1].fence) == vk.Success {
		return n
	}
	for i := range n - 1 {
		if d.cmds.GetFenceStatus(d.device, d.inflight[i].fence) != vk.Success {
			return i
		}
	}
	return n - 1
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
	if len(d.inflight) == 0 {
		return nil
	}
	r := d.cmds.QueueWaitIdle(d.queue)
	d.reap(true)
	if r != vk.Success {
		return fmt.Errorf("gpu: vkQueueWaitIdle: %d", r)
	}
	return nil
}

func (d *Device) frameTime(query int) (time.Duration, error) {
	if query < 0 {
		return 0, ErrNoFrameTime
	}
	a := &d.args
	if r := d.cmds.GetQueryPoolResults(d.device, d.queries, intconv.Must[uint32](query), 1, 8, unsafe.Pointer(&a.timestamp), 8, vk.QueryResultFlags(vk.QueryResult64Bit)); r != vk.Success {
		return 0, fmt.Errorf("gpu: vkGetQueryPoolResults (timestamp): %d", r)
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
		return fmt.Errorf("gpu: vkGetCalibratedTimestampsKHR: %d", r)
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
	d.waits, d.spareSemaphores = nil, nil
	if d.queries != 0 {
		d.cmds.DestroyQueryPool(d.device, d.queries, nil)
		d.queries = 0
	}
}

// Finish waits until everything submitted to the device has finished:
// a CPU read of a target's dmabuf, a test.
func (d *Device) Finish() error { return d.idle() }
