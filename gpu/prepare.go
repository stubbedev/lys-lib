package gpu

import (
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"unsafe"

	"github.com/stubbedev/lys-lib/atomicfile"
	"github.com/stubbedev/lys-lib/debug"
	"github.com/stubbedev/lys-lib/fourcc"
	"github.com/stubbedev/lys-lib/third_party/vk"
	"github.com/stubbedev/lys-lib/xdg"
)

// pipelineKey names a pipeline being built: which of owner's, for which
// target format.
type pipelineKey struct {
	owner  any
	target vk.Format
	which  int
}

type pipelineJob struct {
	key     pipelineKey
	build   func() (vk.Pipeline, error)
	publish func(vk.Pipeline)
}

type builtPipeline struct {
	job      pipelineJob
	pipeline vk.Pipeline
}

// preparer builds pipelines on a worker once Prepare has been called, so a
// frame never compiles one: the device's goroutine publishes what is done
// at the next Begin, and until then Pass.Incomplete reports what it skipped.
type preparer struct {
	cache  vk.PipelineCache
	path   string
	builds atomic.Uint64
	// async is whether Prepare was called; queued is what is on its way,
	// both the device goroutine's.
	async  bool
	queued map[pipelineKey]struct{}

	mu      sync.Mutex
	jobs    []pipelineJob
	built   []builtPipeline
	err     error
	running bool
	stop    bool
	idle    sync.Cond
}

// lazyPipeline is the pipeline have, or else one build makes: at once on a
// device that never called Prepare, else on the worker, with 0 for now.
func (d *Device) lazyPipeline(key pipelineKey, have vk.Pipeline, build func() (vk.Pipeline, error), publish func(vk.Pipeline)) (vk.Pipeline, error) {
	if have != 0 {
		return have, nil
	}
	pr := &d.prep
	if !pr.async {
		p, err := build()
		if err != nil {
			return 0, err
		}
		publish(p)
		return p, nil
	}
	if _, ok := pr.queued[key]; ok {
		return 0, nil
	}
	if pr.queued == nil {
		pr.queued = map[pipelineKey]struct{}{}
	}
	pr.queued[key] = struct{}{}
	pr.mu.Lock()
	defer pr.mu.Unlock()
	pr.jobs = append(pr.jobs, pipelineJob{key: key, build: build, publish: publish})
	if !pr.running {
		pr.running = true
		go d.buildPipelines()
	}
	return 0, nil
}

func (d *Device) buildPipelines() {
	pr := &d.prep
	for {
		pr.mu.Lock()
		if len(pr.jobs) == 0 || pr.stop {
			pr.mu.Unlock()
			if err := d.savePipelineCache(); err != nil {
				debug.Log("gpu", "pipeline cache not saved: %v", err)
			}
			pr.mu.Lock()
			if len(pr.jobs) > 0 && !pr.stop {
				pr.mu.Unlock()
				continue
			}
			pr.running = false
			if pr.idle.L != nil {
				pr.idle.Broadcast()
			}
			pr.mu.Unlock()
			return
		}
		job := pr.jobs[0]
		pr.jobs = pr.jobs[1:]
		pr.mu.Unlock()
		p, err := job.build()
		pr.mu.Lock()
		if err != nil {
			pr.err = errors.Join(pr.err, fmt.Errorf("gpu: a pipeline built ahead of its first draw: %w", err))
		} else {
			pr.built = append(pr.built, builtPipeline{job: job, pipeline: p})
		}
		pr.mu.Unlock()
	}
}

// publishPipelines hands the pipelines the worker finished to their owners.
func (d *Device) publishPipelines() error {
	pr := &d.prep
	if !pr.async {
		return nil
	}
	pr.mu.Lock()
	built, err := pr.built, pr.err
	pr.built, pr.err = nil, nil
	pr.mu.Unlock()
	for _, b := range built {
		b.job.publish(b.pipeline)
		delete(pr.queued, b.job.key)
	}
	return err
}

// Prepare builds, on a worker, the pipelines drawing into targets of formats
// takes, including the ones a device builds only when first drawn, and from
// then on any pipeline a frame finds missing goes to the worker too: nothing
// compiles while a pass records. A pass that meets a pipeline not built yet
// skips that draw and reports Incomplete. Prepared reports when the worker has
// nothing left. Bad formats fail at once; a driver's refusal comes from a later
// Begin.
func (d *Device) Prepare(formats ...fourcc.Format) error {
	for _, f := range formats {
		if _, ok := lookupPixelFormat(f); !ok {
			return fmt.Errorf("gpu: Prepare of DRM format %s, no target format", f)
		}
	}
	cs, err := d.colorInit()
	if err != nil {
		return err
	}
	d.prep.async = true
	for _, f := range formats {
		pf, _ := lookupPixelFormat(f)
		ct, err := d.compTargetOf(pf.vk)
		if err != nil {
			return err
		}
		if _, err := d.glassPipeline(ct); err != nil {
			return err
		}
		for _, kind := range []opaqueKind{opaqueTexture, opaqueFill} {
			if _, _, err := d.opaquePipeline(ct, kind); err != nil {
				return err
			}
		}
		if _, err := d.colorGradPipeline(ct); err != nil {
			return err
		}
		if _, err := d.colorGlassPipeline(ct); err != nil {
			return err
		}
		if _, _, err := d.colorPlainPipeline(cs, ct); err != nil {
			return err
		}
	}
	return nil
}

// Prepared reports whether the worker has built everything asked of it; the
// pipelines reach passes from the next Begin.
func (d *Device) Prepared() (bool, error) {
	if err := d.publishPipelines(); err != nil {
		return false, err
	}
	pr := &d.prep
	pr.mu.Lock()
	defer pr.mu.Unlock()
	return len(pr.jobs) == 0 && !pr.running && len(pr.built) == 0, nil
}

func (d *Device) stopPipelines() {
	pr := &d.prep
	pr.mu.Lock()
	pr.stop = true
	if pr.idle.L == nil {
		pr.idle.L = &pr.mu
	}
	for pr.running {
		pr.idle.Wait()
	}
	pr.mu.Unlock()
	if err := d.publishPipelines(); err != nil {
		debug.Log("gpu", "closing: %v", err)
	}
}

func pipelineCachePath(uuid [16]uint8) (string, bool) {
	dir, ok := xdg.OS.CacheDir("lys-lib")
	if !ok {
		return "", false
	}
	return filepath.Join(dir, "vulkan-pipelines-"+hex.EncodeToString(uuid[:])+".bin"), true
}

// openPipelineCache starts the cache from what an earlier run saved for this
// driver and device; its UUID is in the file's name, and the driver checks the
// data's own header.
func (d *Device) openPipelineCache(uuid [16]uint8) error {
	pr := &d.prep
	var data []byte
	if path, ok := pipelineCachePath(uuid); ok {
		pr.path = path
		var err error
		if data, err = os.ReadFile(path); err != nil && !errors.Is(err, os.ErrNotExist) { //nolint:gosec // the path is the user's own cache directory
			debug.Log("gpu", "pipeline cache %s not read: %v", path, err)
			data = nil
		}
	}
	ci := vk.PipelineCacheCreateInfo{SType: vk.StructureTypePipelineCacheCreateInfo}
	if len(data) > 0 {
		ci.InitialDataSize = uintptr(len(data))
		ci.PInitialData = (*uintptr)(unsafe.Pointer(unsafe.SliceData(data)))
	}
	if r := d.cmds.CreatePipelineCache(d.device, &ci, nil, &pr.cache); r != vk.Success {
		return fmt.Errorf("gpu: vkCreatePipelineCache from %d bytes: %w", len(data), resultError(r))
	}
	return nil
}

func (d *Device) savePipelineCache() error {
	pr := &d.prep
	if pr.cache == 0 || pr.path == "" {
		return nil
	}
	var n uintptr
	if r := d.cmds.GetPipelineCacheData(d.device, pr.cache, &n, nil); r != vk.Success {
		return fmt.Errorf("gpu: vkGetPipelineCacheData: %w", resultError(r))
	}
	if n == 0 {
		return nil
	}
	data := make([]byte, n)
	if r := d.cmds.GetPipelineCacheData(d.device, pr.cache, &n, (*uintptr)(unsafe.Pointer(unsafe.SliceData(data)))); r != vk.Success && r != vk.Incomplete {
		return fmt.Errorf("gpu: vkGetPipelineCacheData of %d bytes: %w", n, resultError(r))
	}
	if err := os.MkdirAll(filepath.Dir(pr.path), 0o700); err != nil {
		return fmt.Errorf("gpu: the pipeline cache's directory: %w", err)
	}
	if err := atomicfile.WriteFile(pr.path, data[:n], 0o600); err != nil {
		return fmt.Errorf("gpu: the pipeline cache %s: %w", pr.path, err)
	}
	return nil
}
