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
	doneAt     time.Duration
	doneErr    error
}

type inflight struct {
	fence vk.Fence
	cb    vk.CommandBuffer
	sems  []vk.Semaphore
	query int
	pass  uint64
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

func (d *Device) semaphore(export bool) (vk.Semaphore, error) {
	exp := vk.ExportSemaphoreCreateInfo{SType: vk.StructureTypeExportSemaphoreCreateInfo, HandleTypes: vk.ExternalSemaphoreHandleTypeFlags(vk.ExternalSemaphoreHandleTypeSyncFdBit)}
	sci := vk.SemaphoreCreateInfo{SType: vk.StructureTypeSemaphoreCreateInfo}
	if export {
		sci.PNext = (*uintptr)(unsafe.Pointer(&exp))
	}
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
// already signalled.
func (d *Device) WaitSyncFile(fd int) error {
	if !d.syncFiles {
		if fd >= 0 {
			_ = unix.Close(fd)
		}
		return ErrNoSyncFile
	}
	if fd >= 0 {
		if _, _, err := drm.SyncFileSignalTime(fd); err != nil {
			_ = unix.Close(fd)
			return fmt.Errorf("gpu: fd %d is no sync_file: %w", fd, err)
		}
	}
	s, err := d.semaphore(false)
	if err != nil {
		if fd >= 0 {
			_ = unix.Close(fd)
		}
		return err
	}
	info := vk.ImportSemaphoreFdInfoKHR{
		SType: vk.StructureTypeImportSemaphoreFdInfoKhr, Semaphore: s, Flags: vk.SemaphoreImportFlags(vk.SemaphoreImportTemporaryBit),
		HandleType: vk.ExternalSemaphoreHandleTypeSyncFdBit, Fd: intconv.Must[int32](fd),
	}
	if r := d.cmds.ImportSemaphoreFdKHR(d.device, &info); r != vk.Success {
		d.cmds.DestroySemaphore(d.device, s, nil)
		if fd >= 0 {
			_ = unix.Close(fd)
		}
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
	s, err := d.semaphore(true)
	if err != nil {
		return -1, err
	}
	if err := d.queue1(0, []vk.Semaphore{s}, -1, false); err != nil {
		d.cmds.DestroySemaphore(d.device, s, nil)
		return -1, err
	}
	gi := vk.SemaphoreGetFdInfoKHR{SType: vk.StructureTypeSemaphoreGetFdInfoKhr, Semaphore: s, HandleType: vk.ExternalSemaphoreHandleTypeSyncFdBit}
	var fd int32
	if r := d.cmds.GetSemaphoreFdKHR(d.device, &gi, &fd); r != vk.Success {
		return -1, fmt.Errorf("gpu: vkGetSemaphoreFdKHR (sync_file): %d", r)
	}
	if fd < 0 {
		return -1, errors.New("gpu: the driver exported no sync_file for a pending signal")
	}
	return int(fd), nil
}

func (d *Device) queue1(cb vk.CommandBuffer, signal []vk.Semaphore, query int, pass bool) error {
	d.reap(false)
	for len(d.inflight) >= queryRing {
		if err := d.waitOldest(); err != nil {
			return err
		}
	}
	fence, err := d.fence()
	if err != nil {
		return err
	}
	waits := d.waits
	stages := make([]vk.PipelineStageFlags, len(waits))
	for i := range stages {
		stages[i] = vk.PipelineStageFlags(vk.PipelineStageAllCommandsBit)
	}
	si := vk.SubmitInfo{SType: vk.StructureTypeSubmitInfo}
	if len(waits) > 0 {
		si.WaitSemaphoreCount, si.PWaitSemaphores, si.PWaitDstStageMask = count(waits), &waits[0], &stages[0]
	}
	if cb != 0 {
		si.CommandBufferCount, si.PCommandBuffers = 1, &cb
	}
	if len(signal) > 0 {
		si.SignalSemaphoreCount, si.PSignalSemaphores = count(signal), &signal[0]
	}
	r := d.cmds.QueueSubmit(d.queue, 1, &si, fence)
	runtime.KeepAlive(stages)
	if r != vk.Success {
		d.cmds.DestroyFence(d.device, fence, nil)
		return fmt.Errorf("gpu: vkQueueSubmit: %d", r)
	}
	d.waits = nil
	f := inflight{fence: fence, cb: cb, sems: append(waits, signal...), query: query}
	if pass {
		d.passes++
		f.pass = d.passes
	}
	d.inflight = append(d.inflight, f)
	return nil
}

func (d *Device) submitAsync(record func(cb vk.CommandBuffer), pass bool) error {
	cb, err := d.commandBuffer(d.pool)
	if err != nil {
		return err
	}
	if err := begin(d, cb); err != nil {
		d.cmds.FreeCommandBuffers(d.device, d.pool, 1, &cb)
		return err
	}
	query := -1
	if pass && d.calibrated {
		if err := d.ensureQueries(); err != nil {
			d.cmds.FreeCommandBuffers(d.device, d.pool, 1, &cb)
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
		d.cmds.FreeCommandBuffers(d.device, d.pool, 1, &cb)
		return fmt.Errorf("gpu: vkEndCommandBuffer: %d", r)
	}
	if err := d.queue1(cb, nil, query, pass); err != nil {
		d.cmds.FreeCommandBuffers(d.device, d.pool, 1, &cb)
		return err
	}
	return nil
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
	f := d.inflight[0].fence
	if r := d.cmds.WaitForFences(d.device, 1, &f, vk.True, ^uint64(0)); r != vk.Success {
		return fmt.Errorf("gpu: vkWaitForFences: %d", r)
	}
	d.reap(false)
	return nil
}

func (d *Device) reap(all bool) {
	keep := d.inflight[:0]
	for _, f := range d.inflight {
		if !all && d.cmds.GetFenceStatus(d.device, f.fence) != vk.Success {
			keep = append(keep, f)
			continue
		}
		if f.pass > d.done {
			d.done = f.pass
			d.doneAt, d.doneErr = d.frameTime(f.query)
		}
		if f.cb != 0 {
			d.cmds.FreeCommandBuffers(d.device, d.pool, 1, &f.cb)
		}
		for _, s := range f.sems {
			d.cmds.DestroySemaphore(d.device, s, nil)
		}
		d.cmds.DestroyFence(d.device, f.fence, nil)
	}
	clear(d.inflight[len(keep):])
	d.inflight = keep
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
	var ts uint64
	if r := d.cmds.GetQueryPoolResults(d.device, d.queries, intconv.Must[uint32](query), 1, 8, unsafe.Pointer(&ts), 8, vk.QueryResultFlags(vk.QueryResult64Bit)); r != vk.Success {
		return 0, fmt.Errorf("gpu: vkGetQueryPoolResults (timestamp): %d", r)
	}
	infos := [2]vk.CalibratedTimestampInfoKHR{
		{SType: vk.StructureTypeCalibratedTimestampInfoKhr, TimeDomain: vk.TimeDomainDeviceKhr},
		{SType: vk.StructureTypeCalibratedTimestampInfoKhr, TimeDomain: vk.TimeDomainClockMonotonicKhr},
	}
	var now [2]uint64
	var deviation uint64
	if r := d.cmds.GetCalibratedTimestampsKHR(d.device, 2, &infos[0], &now[0], &deviation); r != vk.Success {
		return 0, fmt.Errorf("gpu: vkGetCalibratedTimestampsKHR: %d", r)
	}
	mask := ^uint64(0)
	if d.validBits < 64 {
		mask = 1<<d.validBits - 1
	}
	ago := time.Duration(float64((now[0]-ts)&mask) * d.period)
	return time.Duration(intconv.Must[int64](now[1])) - ago, nil
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
	return d.doneAt, d.doneErr == nil, d.doneErr
}

func (d *Device) closeTiming() {
	d.reap(true)
	for _, s := range d.waits {
		d.cmds.DestroySemaphore(d.device, s, nil)
	}
	d.waits = nil
	if d.queries != 0 {
		d.cmds.DestroyQueryPool(d.device, d.queries, nil)
		d.queries = 0
	}
}

// Finish waits until everything submitted to the device has finished:
// a CPU read of a target's dmabuf, a test.
func (d *Device) Finish() error { return d.idle() }
