package gpu

import (
	"errors"
	"fmt"
	"image"
	"sync"

	"github.com/stubbedev/lys-lib/render"
	"github.com/stubbedev/lys-lib/third_party/vk"
)

// stillWorkerBytes is the size from which a still's upload leaves the frame
// path: a full-screen image's copy, a millisecond or more of a frame.
const stillWorkerBytes = 8 << 20

type stillUpload struct {
	key     imageKey
	w, h    int
	staging *buffer
	surface *surface
	dropped bool
}

// stillUploads are the large stills a worker is copying into staging memory of
// their own, the device's goroutine recording the transfer when one is done.
type stillUploads struct {
	pending map[imageKey]*stillUpload
	wg      sync.WaitGroup
	mu      sync.Mutex
	done    []*stillUpload
	err     error
}

func (st *stillUploads) wait() { st.wg.Wait() }

func (st *stillUploads) forget(gone map[render.ImageID]struct{}) {
	for key, u := range st.pending {
		if _, ok := gone[key.id]; ok {
			u.dropped = true
		}
	}
}

func (d *Device) startStillUpload(key imageKey, img *image.RGBA) {
	st := &d.images.stills
	if _, ok := st.pending[key]; ok {
		return
	}
	if st.pending == nil {
		st.pending = map[imageKey]*stillUpload{}
	}
	b := img.Rect
	u := &stillUpload{key: key, w: b.Dx(), h: b.Dy()}
	st.pending[key] = u
	release := render.Hold()
	st.wg.Go(func() {
		defer release()
		err := d.prepareStill(u, img)
		st.mu.Lock()
		defer st.mu.Unlock()
		if err != nil {
			st.err = errors.Join(st.err, fmt.Errorf("gpu: a %dx%d image's upload: %w", u.w, u.h, err))
			return
		}
		st.done = append(st.done, u)
	})
}

func (d *Device) prepareStill(u *stillUpload, img *image.RGBA) error {
	staging, err := d.newBuffer(u.w*u.h*4, vk.BufferUsageTransferSrcBit)
	if err != nil {
		return err
	}
	copyRows(staging.mapped, img, img.Rect)
	s, err := d.newImageSurface(u.w, u.h)
	if err != nil {
		staging.close()
		return err
	}
	u.staging, u.surface = staging, s
	return nil
}

// adoptStills records the transfer of each still the worker finished, in a
// submission of its own: it draws once that has finished.
func (d *Device) adoptStills() error {
	st := &d.images.stills
	st.mu.Lock()
	done, err := st.done, st.err
	st.done, st.err = nil, nil
	st.mu.Unlock()
	for _, u := range done {
		delete(st.pending, u.key)
		if err == nil && !u.dropped {
			err = d.adoptStill(u)
		} else {
			u.surface.close()
		}
		d.retire(u.staging)
	}
	if err != nil {
		return err
	}
	if len(done) > 0 {
		return d.flushBatch()
	}
	return nil
}

func (d *Device) adoptStill(u *stillUpload) error {
	ims := &d.images
	if ims.limit == 0 {
		ims.limit = maxImages
	}
	if len(ims.byImage) >= ims.limit {
		d.evictLRU()
	}
	if err := u.surface.ready(); err != nil {
		u.surface.close()
		return err
	}
	cb, err := d.batchCommands()
	if err != nil {
		u.surface.close()
		return err
	}
	u.surface.upload(cb, image.Rect(0, 0, u.w, u.h), u.staging, 0, u.w)
	u.surface.to(cb, vk.ImageLayoutShaderReadOnlyOptimal)
	if ims.byImage == nil {
		ims.byImage = map[imageKey]*imageTexture{}
	}
	ims.byImage[u.key] = &imageTexture{s: u.surface, used: ims.frames, readyAfter: d.submitted + 1}
	return nil
}

func (d *Device) closeStills() {
	st := &d.images.stills
	for _, u := range st.done {
		u.surface.close()
		u.staging.close()
	}
	st.done = nil
}

// FrameIncomplete reports that the last Render left out an image still on its
// way to the GPU: the caller keeps that frame's damage for its next one.
func (d *Device) FrameIncomplete() bool { return d.incomplete }
