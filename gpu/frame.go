package gpu

import (
	"cmp"
	"fmt"
	"image"
	"math"
	"unsafe"

	"github.com/stubbedev/lys-lib/debug"
	"github.com/stubbedev/lys-lib/intconv"
	"github.com/stubbedev/lys-lib/render"
	"github.com/stubbedev/lys-lib/third_party/typesetting/font"
	"github.com/stubbedev/lys-lib/third_party/vk"
)

// stepKind is what a recorded step does on the GPU.
type stepKind uint8

const (
	// stepDraw draws prims [first, first+count) into dst sampling tex.
	stepDraw stepKind = iota
	// stepCopy copies src's region into dst at the same place.
	stepCopy
	// stepDownload copies dst's region into the readback buffer at
	// offset.
	stepDownload
)

// step is one GPU operation of a frame, recorded in order.
type step struct {
	kind         stepKind
	dst, src     *surface
	tex, tex2    *surface
	pipe         pipe
	first, count uint32
	offset       int
	region       image.Rectangle
}

// upload copies staged bytes into a surface region. A segment's
// uploads run before its steps: their bytes never depend on drawing.
type upload struct {
	dst       *surface
	region    image.Rectangle
	offset    int
	rowLength int
}

// frame plays a render.List onto a target: the drawing calls the GPU
// does natively become primitives, the rest the CPU rasterizes into
// sprites the GPU draws, and the layer calls the GPU cannot do it runs
// on the CPU over the layer's pixels read back. It is a render.Player;
// the first error stops it, and Render reports it.
type frame struct {
	d          *Device
	target     *Target
	num, denom int
	prims      []prim
	// words are the primitives' variable-length data: gradient stops.
	words       []uint32
	steps       []step
	uploads     []upload
	carries     []Carry
	filterSteps []render.FilterStep
	explicit    bool
	syncFile    int
	cpuCanvases []*render.Canvas
	pathScratch render.PathScratch
	copies      []vk.ImageCopy
	selfCopies  []vk.ImageCopy
	// staged is how many bytes of the segment's staging buffer its
	// uploads fill.
	staged int
	// downloads is how many bytes the segment's downloads take.
	downloads int
	// stats counts what the frame drew how, for the gpu debug line.
	stats frameStats
	// page is the sprite page being packed: x, y and the shelf height.
	px, py, shelf int
	err           error
}

// Render draws l onto t and hands t back to its consumer, without
// waiting for the GPU where the device can tell the consumer when it is
// done (publish). l is recorded at t's size; its layers keep their GPU
// surfaces on this device across frames. ErrBusy, before anything is
// done, means the GPU is too far behind for one more frame. carry is copied into t first, in
// the frame's own submission.
func (d *Device) Render(l *render.List, t *Target, carry ...Carry) error {
	return d.render(l, t, false, carry)
}

// RenderExplicit is Render for a consumer that takes its fence explicitly,
// never waiting for the GPU: the sync_file it returns, the caller's to close,
// signals when the frame is done, for wp_linux_drm_syncobj_surface_v1's acquire
// point (drm.Device.ImportSyncFile then SetAcquirePoint). -1 means it is done
// already. ErrNoSyncFile reports a device with no sync_files.
func (d *Device) RenderExplicit(l *render.List, t *Target, carry ...Carry) (int, error) {
	if !d.syncFiles {
		return -1, ErrNoSyncFile
	}
	if err := d.render(l, t, true, carry); err != nil {
		return -1, err
	}
	fd := d.frame.syncFile
	d.frame.syncFile = -1
	return fd, nil
}

func (d *Device) render(l *render.List, t *Target, explicit bool, carry []Carry) error {
	if t.s.vf != surfaceFormat {
		return fmt.Errorf("gpu: Render draws into ARGB8888 and XRGB8888 targets, not %s", t.buf.Format)
	}
	for _, c := range carry {
		if err := d.checkCarry(t, c); err != nil {
			return err
		}
	}
	d.reap(false)
	d.adoptTargets()
	if err := d.checkLost(); err != nil {
		return err
	}
	if d.framesBusy() {
		return ErrBusy
	}
	d.freeDeadLayers()
	d.incomplete = false
	if err := d.adoptStills(); err != nil {
		return err
	}
	d.images.frames++
	defer d.forgetImages()
	if err := d.ensureScratch(t.s.w, t.s.h); err != nil {
		return err
	}
	if err := d.takeFrameBuffers(); err != nil {
		return err
	}
	if err := t.s.ready(); err != nil {
		return err
	}
	num, denom := l.Scale()
	f := &d.frame
	f.d, f.target, f.num, f.denom, f.err, f.explicit = d, t, num, denom, nil, explicit
	f.clear()
	f.mapSegment()
	f.carries = append(f.carries[:0], carry...)
	f.stats.reset()
	d.framing = true
	defer d.endFraming()
	l.Play(f)
	if f.err != nil {
		return f.err
	}
	f.stats.prims += len(f.prims)
	err := f.flush(true)
	if debug.Enabled {
		debug.Log("gpu", "frame %dx%d: %d prims, %d segments, %d readbacks, %d texture uploads, sprite pixels %v", t.s.w, t.s.h, f.stats.prims, f.stats.segments, f.stats.readbacks, f.stats.textureUploads, f.stats.sprites)
	}
	return err
}

// frameStats is what a frame drew how: its primitives, the segments it
// submitted in, its readbacks, the image textures and atlas masks it
// uploaded, and the pixels of CPU sprites per call.
type frameStats struct {
	prims, segments, readbacks, textureUploads, imageBytes int
	sprites                                                map[string]int
}

// reset empties the stats for a new frame, keeping the map.
func (s *frameStats) reset() {
	sprites := s.sprites
	if debug.Enabled {
		if sprites == nil {
			sprites = map[string]int{}
		}
		clear(sprites)
	}
	*s = frameStats{sprites: sprites}
}

// fail records the frame's first error.
func (f *frame) fail(err error) {
	if f.err == nil {
		f.err = err
	}
}

// stage reserves the next n bytes of the segment's staging buffer for
// an upload, returning them and their offset; nil after an error.
// Uploads write straight into the buffer the GPU copies from, which no
// submission holds while its segment records, so growing it copies only
// what this segment staged.
func (f *frame) stage(n int) ([]byte, int) {
	d, fb := f.d, f.d.current
	at, need := f.staged, f.staged+n
	if len(fb.staging.mapped) < need {
		grown, err := d.newBuffer(max(2*need, stagingMin), vk.BufferUsageTransferSrcBit)
		if err != nil {
			f.fail(fmt.Errorf("gpu: staging buffer of %d bytes: %w", need, err))
			return nil, 0
		}
		copy(grown.mapped, fb.staging.mapped[:at])
		fb.staging.close()
		fb.staging = grown
	}
	f.staged = need
	return fb.staging.mapped[at:need], at
}

func (f *frame) bounds() render.Rect { return render.Rect{W: f.target.s.w, H: f.target.s.h} }

// on is the surface s names: the target, or a layer's.
func (f *frame) on(s render.State) *surface {
	if s.Layer == nil {
		return &f.target.s
	}
	return f.layer(s.Layer)
}

// layer is the GPU surface backing the recorded l.
func (f *frame) layer(l *render.Layer) *surface {
	s, ok := l.Backing().(*layerSurface)
	if !ok {
		panic(fmt.Sprintf("gpu: a recorded layer backed by %T, not a GPU surface", l.Backing()))
	}
	if s.s.d != f.d {
		panic("gpu: a recorded layer backed by another device's surface")
	}
	return s.s
}

// draw adds p, drawn into dst sampling tex (nil for none) through the
// pipeline pp, to the frame, joining the previous draw when it shares
// all three.
func (f *frame) draw(dst, tex *surface, pp pipe, p prim) {
	f.draw2(dst, tex, nil, pp, p)
}

// draw2 is draw sampling a second surface, tex2, as well: an in-place
// layer call's other layer or mask.
func (f *frame) draw2(dst, tex, tex2 *surface, pp pipe, p prim) {
	if f.err != nil {
		return
	}
	for _, t := range [2]*surface{tex, tex2} {
		if t == nil {
			continue
		}
		if err := t.ready(); err != nil {
			f.fail(err)
			return
		}
	}
	if !f.roomFor(1, 0) {
		return
	}
	f.prims = append(f.prims, p)
	area := image.Rect(int(p.quad[0]), int(p.quad[1]), int(p.quad[2]), int(p.quad[3]))
	if n := len(f.steps); n > 0 {
		last := &f.steps[n-1]
		if last.kind == stepDraw && last.dst == dst && last.pipe == pp && samplesJoin(last.tex, tex) && samplesJoin(last.tex2, tex2) {
			last.tex, last.tex2 = cmp.Or(last.tex, tex), cmp.Or(last.tex2, tex2)
			last.region = last.region.Union(area)
			last.count++
			return
		}
	}
	f.steps = append(f.steps, step{kind: stepDraw, dst: dst, tex: tex, tex2: tex2, pipe: pp, first: count(f.prims) - 1, count: 1, region: area})
}

// samplesJoin reports whether draws sampling a and b can share a binding:
// nil is a kind of primitive that reads no texture.
func samplesJoin(a, b *surface) bool { return a == b || a == nil || b == nil }

// sprite rasterizes one call on the CPU, within s's bounds, and draws
// the result: exact for the source-over calls, whose pixels blended
// onto transparency and then over the surface are the pixels blended
// over it directly.
func (f *frame) sprite(s render.State, call string, paint func(cv *render.Canvas)) {
	b := s.Bounds
	if f.err != nil || b.Empty() {
		return
	}
	if debug.Enabled {
		f.stats.sprites[call] += b.W * b.H
	}
	at, ok := f.pageSlot(b.W, b.H)
	if !ok {
		return
	}
	sc, err := f.d.scratchCanvas(0, f.target.s.w, f.target.s.h, f.num, f.denom)
	if err != nil {
		f.fail(err)
		return
	}
	sc.cv.ApplyState(render.State{Clip: b, Alpha: s.Alpha, Bright: s.Bright})
	paint(sc.cv)
	out, off := f.stage(b.W * b.H * 4)
	if out == nil {
		return
	}
	f.uploads = append(f.uploads, upload{dst: f.d.page, region: image.Rect(at.X, at.Y, at.X+b.W, at.Y+b.H), offset: off, rowLength: b.W})
	sc.take(out, b)
	p := prim{quad: quadOf(b), inner: [4]float32{float32(b.X - at.X), float32(b.Y - at.Y)}, info: [4]uint32{kindTexture, 255, 0, flagRaw}}
	f.draw(f.on(s), f.d.page, pipeOver, p)
}

// pageSlot is where a w x h upload goes on the sprite page this
// segment, flushing the segment first when the page is full; false
// after an error.
func (f *frame) pageSlot(w, h int) (image.Point, bool) {
	at, ok := f.place(w, h)
	if ok {
		return at, true
	}
	if f.err = f.flush(false); f.err != nil {
		return image.Point{}, false
	}
	at, _ = f.place(w, h)
	return at, true
}

// place packs a w x h sprite onto the page, false when it is full.
func (f *frame) place(w, h int) (image.Point, bool) {
	pw, ph := f.d.page.w, f.d.page.h
	if f.px+w > pw {
		f.px, f.py, f.shelf = 0, f.py+f.shelf, 0
	}
	if f.py+h > ph {
		return image.Point{}, false
	}
	at := image.Pt(f.px, f.py)
	f.px += w
	f.shelf = max(f.shelf, h)
	return at, true
}

// cpu runs fn, a layer call the GPU does not do, on the CPU: it reads
// back the regions of the surfaces fn reads (the first is the one it
// writes) into scratch canvases, runs fn over them, and uploads the
// written region back.
func (f *frame) cpu(region render.Rect, surfaces []*surface, fn func(cvs []*render.Canvas)) {
	region = region.Intersect(f.bounds())
	if f.err != nil || region.Empty() {
		return
	}
	r := rectOf(region)
	var offsets [2]int
	for i, s := range surfaces {
		offsets[i] = f.downloads
		f.steps = append(f.steps, step{kind: stepDownload, dst: s, region: r, offset: f.downloads})
		f.downloads += r.Dx() * r.Dy() * 4
	}
	f.stats.readbacks++
	f.stats.prims += len(f.prims)
	if f.err = f.flush(false); f.err != nil {
		return
	}
	if f.err = f.d.idle(); f.err != nil {
		return
	}
	if f.err = f.d.readback.invalidate(); f.err != nil {
		return
	}
	f.cpuCanvases = f.cpuCanvases[:0]
	for i := range surfaces {
		sc, err := f.d.scratchCanvas(i, f.target.s.w, f.target.s.h, f.num, f.denom)
		if err != nil {
			f.fail(err)
			return
		}
		sc.put(region, f.d.readback.mapped[offsets[i]:])
		f.cpuCanvases = append(f.cpuCanvases, sc.cv)
	}
	fn(f.cpuCanvases)
	out, off := f.stage(r.Dx() * r.Dy() * 4)
	if out == nil {
		return
	}
	f.uploads = append(f.uploads, upload{dst: surfaces[0], region: r, offset: off, rowLength: r.Dx()})
	f.d.scratches[0].take(out, region)
	for _, sc := range f.d.scratches[1:len(surfaces)] {
		sc.clear(region)
	}
}

// flush submits the recorded segment, without waiting for it, with the
// buffers it read handed to its submission; final hands the target back
// to its consumer.
func (f *frame) flush(final bool) error {
	d, fb := f.d, f.d.current
	var err error
	if d.readback, err = d.readback.fit(f.downloads); err != nil {
		return err
	}
	f.stats.segments++
	fb.staged = f.staged
	publishes := final && (f.explicit || d.publishes(f.target))
	err = d.submitAsync(func(cb vk.CommandBuffer) { f.record(cb, final) }, submission{signals: publishes})
	f.clear()
	if err != nil {
		return err
	}
	d.inflight[len(d.inflight)-1].buffers = fb
	d.current = nil
	if final && f.explicit {
		fd, err := d.exportFrameSignal()
		if err != nil {
			return err
		}
		if fd < 0 {
			err = d.idle()
		}
		f.syncFile = fd
		return err
	}
	if final {
		return d.publish(f.target, publishes)
	}
	if err := d.takeFrameBuffers(); err != nil {
		return err
	}
	f.mapSegment()
	return nil
}

// mapSegment points the segment's primitives and words at the buffers the
// GPU reads, so a draw writes them in place.
func (f *frame) mapSegment() {
	fb := f.d.current
	f.prims = unsafe.Slice((*prim)(unsafe.Pointer(unsafe.SliceData(fb.prims.mapped))), len(fb.prims.mapped)/primSize)[:0]
	f.words = unsafe.Slice((*uint32)(unsafe.Pointer(unsafe.SliceData(fb.words.mapped))), len(fb.words.mapped)/4)[:0]
}

// roomFor makes room for n more primitives and words in the segment's
// buffers, moving what it holds when they grow; false after an error.
func (f *frame) roomFor(prims, words int) bool {
	np, nw := len(f.prims), len(f.words)
	if np+prims <= cap(f.prims) && nw+words <= cap(f.words) {
		return true
	}
	d, fb := f.d, f.d.current
	if np+prims > cap(f.prims) {
		grown, err := d.newBuffer(2*(np+prims)*primSize, vk.BufferUsageStorageBufferBit)
		if err != nil {
			f.fail(fmt.Errorf("gpu: a segment's %d primitives: %w", np+prims, err))
			return false
		}
		copy(grown.mapped, fb.prims.mapped[:np*primSize])
		fb.prims.close()
		fb.prims = grown
	}
	if nw+words > cap(f.words) {
		grown, err := d.newBuffer(2*(nw+words)*4, vk.BufferUsageStorageBufferBit)
		if err != nil {
			f.fail(fmt.Errorf("gpu: a segment's %d words: %w", nw+words, err))
			return false
		}
		copy(grown.mapped, fb.words.mapped[:nw*4])
		fb.words.close()
		fb.words = grown
	}
	d.bindPrims(fb)
	f.mapSegment()
	f.prims, f.words = f.prims[:np], f.words[:nw]
	return true
}

// clear empties the recorded segment, keeping its slices' capacity.
func (f *frame) clear() {
	f.prims, f.words = nil, nil
	f.steps, f.uploads, f.staged = f.steps[:0], f.uploads[:0], 0
	f.px, f.py, f.shelf, f.downloads = 0, 0, 0, 0
}

// record records the segment: its uploads, then its steps, draws
// sharing a surface in one render pass.
func (f *frame) record(cb vk.CommandBuffer, final bool) {
	d := f.d
	f.target.acquire(cb)
	for _, c := range f.carries {
		f.recordCarry(cb, c)
	}
	clear(f.carries)
	f.carries = f.carries[:0]
	for _, u := range f.uploads {
		u.dst.upload(cb, u.region, d.current.staging, u.offset, u.rowLength)
	}
	var pass *surface
	var pipe vk.Pipeline
	var tex, tex2 *surface
	end := func() {
		if pass != nil {
			d.cmds.CmdEndRenderPass(cb)
			pass = nil
		}
	}
	for i := range f.steps {
		st := &f.steps[i]
		switch st.kind {
		case stepCopy:
			end()
			st.src.to(cb, vk.ImageLayoutTransferSrcOptimal)
			st.dst.to(cb, vk.ImageLayoutTransferDstOptimal)
			layers := vk.ImageSubresourceLayers{AspectMask: vk.ImageAspectFlags(vk.ImageAspectColorBit), LayerCount: 1}
			at := offset3D(st.region.Min)
			d.args.copy = vk.ImageCopy{
				SrcSubresource: layers, SrcOffset: at, DstSubresource: layers, DstOffset: at,
				Extent: extent3D(st.region.Dx(), st.region.Dy()),
			}
			d.cmds.CmdCopyImage(cb, st.src.image, vk.ImageLayoutTransferSrcOptimal, st.dst.image, vk.ImageLayoutTransferDstOptimal, 1, &d.args.copy)
		case stepDownload:
			end()
			st.dst.download(cb, st.region, d.readback, st.offset)
		case stepDraw:
			sampled, sampled2 := st.tex, st.tex2
			if sampled == nil {
				sampled = d.blank
			}
			if sampled2 == nil {
				sampled2 = d.blank
			}
			for _, t := range [2]*surface{sampled, sampled2} {
				if t.layout != vk.ImageLayoutShaderReadOnlyOptimal {
					end()
					t.to(cb, vk.ImageLayoutShaderReadOnlyOptimal)
				}
			}
			if pass != st.dst {
				end()
				f.begin(cb, st.dst, f.passArea(i))
				pass, pipe, tex, tex2 = st.dst, 0, nil, nil
			}
			want := d.pipelineOf(st.pipe)
			if pipe != want {
				d.cmds.CmdBindPipeline(cb, vk.PipelineBindPointGraphics, want)
				pipe = want
			}
			if tex != sampled {
				d.cmds.CmdBindDescriptorSets(cb, vk.PipelineBindPointGraphics, d.layout, 1, 1, &sampled.set, 0, nil)
				tex = sampled
			}
			if tex2 != sampled2 {
				d.cmds.CmdBindDescriptorSets(cb, vk.PipelineBindPointGraphics, d.layout, 2, 1, &sampled2.set, 0, nil)
				tex2 = sampled2
			}
			d.cmds.CmdDraw(cb, 4, st.count, 0, st.first)
		}
	}
	end()
	if final {
		f.target.release(cb)
	}
}

// passArea is what the draws from step i on into its surface touch.
func (f *frame) passArea(i int) image.Rectangle {
	dst := f.steps[i].dst
	area := f.steps[i].region
	for j := i + 1; j < len(f.steps) && f.steps[j].kind == stepDraw && f.steps[j].dst == dst; j++ {
		area = area.Union(f.steps[j].region)
	}
	return area
}

// begin opens a render pass on s over area with the frame's viewport and
// bindings.
func (f *frame) begin(cb vk.CommandBuffer, s *surface, area image.Rectangle) {
	d := f.d
	s.to(cb, vk.ImageLayoutColorAttachmentOptimal)
	d.args.scissor = rect2D(area.Intersect(image.Rect(0, 0, s.w, s.h)))
	d.args.passBegin = vk.RenderPassBeginInfo{SType: vk.StructureTypeRenderPassBeginInfo, RenderPass: d.pass, Framebuffer: s.fb, RenderArea: d.args.scissor}
	d.cmds.CmdBeginRenderPass(cb, &d.args.passBegin, vk.SubpassContentsInline)
	d.args.viewport = vk.Viewport{Width: float32(s.w), Height: float32(s.h), MaxDepth: 1}
	d.cmds.CmdSetViewport(cb, 0, 1, &d.args.viewport)
	d.cmds.CmdSetScissor(cb, 0, 1, &d.args.scissor)
	d.cmds.CmdBindDescriptorSets(cb, vk.PipelineBindPointGraphics, d.layout, 0, 1, &d.current.set, 0, nil)
	d.args.frame = frameConstants{w: float32(s.w), h: float32(s.h)}
	d.cmds.CmdPushConstants(cb, d.layout, vk.ShaderStageFlags(vk.ShaderStageVertexBit|vk.ShaderStageFragmentBit), 0, uint32(unsafe.Sizeof(d.args.frame)), unsafe.Pointer(&d.args.frame))
}

func (f *frame) ClearDevice(s render.State, r render.Rect, col render.Color) {
	p := prim{quad: quadOf(s.Bounds), cols: [4]uint32{uint32(col)}, info: [4]uint32{kindRect, 255, 0, flagRaw}}
	f.draw(f.on(s), nil, pipeReplace, p)
}

func (f *frame) FillRectDevice(s render.State, r render.Rect, col render.Color) {
	if col == 0 {
		return
	}
	p := shaded(kindRect, s, 0)
	p.quad, p.cols[0] = quadOf(s.Bounds), uint32(col)
	f.draw(f.on(s), nil, pipeOver, p)
}

func (f *frame) DrawCanvasDevice(s render.State, src *render.Canvas, x, y int) {
	f.sprite(s, "DrawCanvasDevice", func(cv *render.Canvas) { cv.DrawCanvasDevice(src, x, y) })
}

func (f *frame) DrawLayerAt(s render.State, src *render.Layer, x, y int) {
	p := shaded(kindTexture, s, flagOpaqueStores)
	p.quad, p.inner = quadOf(s.Bounds), [4]float32{float32(x), float32(y)}
	f.draw(f.on(s), f.layer(src), pipeOver, p)
}

func (f *frame) RoundedRect(s render.State, r render.Rect, radius int, col render.Color) {
	full := render.MapRect(r, f.num, f.denom)
	p := shaded(kindRounded, s, 0)
	p.quad, p.box, p.radii[0], p.cols[0] = quadOf(s.Bounds), quadOf(full), render.RoundedRadius(full, radius, f.num, f.denom), uint32(col)
	f.draw(f.on(s), nil, pipeOver, p)
}

func (f *frame) Line(s render.State, x0, y0, x1, y1, width int, col render.Color) {
	if width < 1 {
		return
	}
	g := render.LineOf(x0, y0, x1, y1, width, f.num, f.denom)
	p := shaded(kindLine, s, 0)
	p.quad, p.cols[0] = quadOf(s.Bounds), uint32(col)
	p.box, p.radii = [4]float32{g.AX, g.AY, g.DX, g.DY}, [4]float32{g.InvLen2, g.Half}
	f.draw(f.on(s), nil, pipeOver, p)
}

func (f *frame) Shadow(s render.State, r render.Rect, cornerRadius, blur int, col render.Color) {
	if f.err != nil || col.A() == 0 {
		return
	}
	m, box, kw, kh, ok := render.RectShadow(r, cornerRadius, blur, f.num, f.denom)
	if !ok {
		return
	}
	if !f.stretched(s, m, box, kw, kh, col) {
		f.sprite(s, "Shadow", func(cv *render.Canvas) { cv.Shadow(r, cornerRadius, blur, col) })
	}
}

// stretched draws col through m, the mask of a kw x kh core box, over
// the device box; false when no atlas page holds m.
func (f *frame) stretched(s render.State, m *render.ShadowMask, box render.Rect, kw, kh int, col render.Color) bool {
	pos, ok := f.maskSlot(m)
	if !ok {
		return false
	}
	x0, y0 := m.Origin()
	p := shaded(kindStretch, s, 0)
	p.quad, p.box, p.cols[0] = quadOf(s.Bounds), [4]float32{float32(box.X), float32(box.Y), float32(box.W), float32(box.H)}, uint32(col)
	p.radii, p.inner = [4]float32{float32(kw), float32(kh)}, [4]float32{float32(pos.X - x0), float32(pos.Y - y0)}
	f.draw(f.on(s), f.d.masks.page, pipeOver, p)
	return true
}

func (f *frame) DrawImage(s render.State, img image.Image, x, y int) {
	b := img.Bounds()
	f.scaledImage(s, img, render.MapRect(render.Rect{X: x, Y: y, W: b.Dx(), H: b.Dy()}, f.num, f.denom), func(cv *render.Canvas) { cv.DrawImage(img, x, y) })
}

// texture is img's texture when the GPU keeps one for it: a still or
// versioned image's.
func (f *frame) texture(img image.Image) (*surface, bool) {
	if f.err != nil {
		return nil, false
	}
	switch v := img.(type) {
	case render.StillImage:
		return f.imageTexture(v.RGBA, v.ID, 0, 0, image.Rectangle{})
	case render.VersionedImage:
		return f.imageTexture(v.RGBA, v.ID, v.Version, v.DirtySince, v.Dirty)
	}
	return nil, false
}

// scaledImage draws img nearest-sampled into the device rect full, as
// render's DrawImage and DrawImageRect map it: a pixel's texel is its
// offset into full scaled by the image's size over full's, in integers.
// An image the GPU keeps no texture for draws as a sprite through
// raster, from its visible pixels.
func (f *frame) scaledImage(s render.State, img image.Image, full render.Rect, raster func(cv *render.Canvas)) {
	tex, ok := f.texture(img)
	if !ok {
		f.sprite(s, "scaled image", raster)
		return
	}
	if tex == nil {
		return
	}
	p := shaded(kindImage, s, 0)
	p.quad, p.box = quadOf(s.Bounds), [4]float32{float32(full.X), float32(full.Y), float32(full.W), float32(full.H)}
	p.radii = [4]float32{float32(tex.w), float32(tex.h)}
	f.draw(f.on(s), tex, pipeOver, p)
}

func (f *frame) DrawImageCover(s render.State, img image.Image, r render.Rect, radii render.Corners) {
	if img.Bounds().Empty() || r.Empty() {
		return
	}
	tex, ok := f.texture(img)
	if !ok {
		f.sprite(s, "DrawImageCover", func(cv *render.Canvas) { cv.DrawImageCover(img, r, radii) })
		return
	}
	if tex == nil {
		return
	}
	g := render.CoverOf(img.Bounds(), r, radii, f.num, f.denom)
	p := shaded(kindCover, s, 0)
	p.quad, p.box = quadOf(s.Bounds), quadOf(g.Full)
	p.radii, p.inner = [4]float32{g.Rad, g.OffX, g.OffY, g.InvScale}, [4]float32{float32(g.W), float32(g.H)}
	f.draw(f.on(s), tex, pipeOver, p)
}

func (f *frame) DrawImageDevice(s render.State, img image.Image, x, y int) {
	tex, ok := f.texture(img)
	if !ok {
		f.sprite(s, "DrawImageDevice", func(cv *render.Canvas) { cv.DrawImageDevice(img, x, y) })
		return
	}
	if tex == nil {
		return
	}
	p := shaded(kindTexture, s, flagOpaqueStores)
	p.quad, p.inner = quadOf(s.Bounds), [4]float32{float32(x), float32(y)}
	f.draw(f.on(s), tex, pipeOver, p)
}

func (f *frame) DrawImageRect(s render.State, img image.Image, r render.Rect) {
	f.scaledImage(s, img, render.MapRect(r, f.num, f.denom), func(cv *render.Canvas) { cv.DrawImageRect(img, r) })
}

func (f *frame) DrawIcon(s render.State, ic *render.Icon, x, y int, m render.Affine) {
	mp, invertible := render.MappedOf(ic.Xform(x, y, m, f.num, f.denom))
	img := ic.Still()
	tex, ok := (*surface)(nil), invertible && f.err == nil
	if ok {
		tex, ok = f.imageTexture(img.RGBA, img.ID, 0, 0, image.Rectangle{})
	}
	if !ok {
		f.sprite(s, "DrawIcon", func(cv *render.Canvas) { ic.DrawXformed(cv, x, y, m) })
		return
	}
	if tex == nil {
		return
	}
	p := shaded(kindComposite, s, 0)
	p.quad, p.inner = quadOf(s.Bounds), [4]float32{0, 0, float32(img.Rect.Dx()), float32(img.Rect.Dy())}
	p.m0, p.m1, p.m2, p.widths = mappedRows(&mp)
	f.draw(f.on(s), tex, pipeOver, p)
}

func (f *frame) Arc(s render.State, cx, cy, radius, width, start, sweep float64, col render.Color) {
	if width <= 0 || radius <= 0 || sweep <= 0 || col == 0 {
		return
	}
	g := render.ArcOf(cx, cy, radius, width, start, sweep, f.num, f.denom)
	full := float32(0)
	if g.Full {
		full = 1
	}
	p := shaded(kindArc, s, 0)
	p.quad, p.cols[0] = quadOf(s.Bounds), uint32(col)
	p.box, p.radii, p.inner = [4]float32{g.CX, g.CY, g.R, g.Half}, [4]float32{g.Start, g.Sweep, full}, [4]float32{g.SX, g.SY, g.EX, g.EY}
	f.draw(f.on(s), nil, pipeOver, p)
}

func (f *frame) BoxShadow(s render.State, r render.Rect, radii render.Corners, sh render.BoxShadow) {
	if f.err != nil || sh.Color.A() == 0 {
		return
	}
	m, box, kw, kh := render.BoxShadowMask(r, radii, sh, f.num, f.denom)
	if !f.stretched(s, m, box, kw, kh, sh.Color) {
		f.sprite(s, "BoxShadow", func(cv *render.Canvas) { cv.BoxShadow(r, radii, sh) })
	}
}

func (f *frame) PaintGrain(s render.State, r render.Rect, radii render.Corners, opacity float64, ox, oy int) {
	if opacity <= 0 {
		return
	}
	box, rc := render.DeviceBox(r, radii, f.num, f.denom)
	o := render.MapRect(render.Rect{X: ox, Y: oy}, f.num, f.denom)
	p := shaded(kindGrain, s, 0)
	p.quad, p.box, p.radii = quadOf(s.Bounds), box, rc
	p.cols = [4]uint32{render.GrainStrength(opacity), intconv.Uint32Bits(intconv.Must[int32](o.X)), intconv.Uint32Bits(intconv.Must[int32](o.Y))}
	f.draw(f.on(s), nil, pipeOver, p)
}

func (f *frame) RoundedBorderSides(s render.State, r render.Rect, radii render.Corners, widths render.Insets, cols [4]render.Color) {
	if widths.Zero() {
		return
	}
	outer, oradii, inner, iradii, w := render.BorderRing(r, radii, widths, f.num, f.denom)
	p := shaded(kindBorder, s, 0)
	p.quad, p.box, p.radii, p.inner, p.iradii, p.widths = quadOf(s.Bounds), outer, oradii, inner, iradii, render.SideReciprocals(w)
	p.cols = [4]uint32{uint32(cols[0]), uint32(cols[1]), uint32(cols[2]), uint32(cols[3])}
	f.draw(f.on(s), nil, pipeOver, p)
}

func (f *frame) RoundedRectCorners(s render.State, r render.Rect, radii render.Corners, col render.Color) {
	if col == 0 {
		return
	}
	box, rc := render.DeviceBox(r, radii, f.num, f.denom)
	p := shaded(kindRRect, s, 0)
	p.quad, p.box, p.radii, p.cols[0] = quadOf(s.Bounds), box, rc, uint32(col)
	f.draw(f.on(s), nil, pipeOver, p)
}

func (f *frame) PaintGradient(s render.State, r render.Rect, radii render.Corners, g render.Gradient) {
	if len(g.Stops) == 0 {
		return
	}
	m, ok := g.DeviceMap(r, radii, f.num, f.denom)
	if !ok {
		return
	}
	box, rc := render.DeviceBox(r, radii, f.num, f.denom)
	p := shaded(kindGradient, s, 0)
	p.quad, p.box, p.radii = quadOf(s.Bounds), box, rc
	f.gradientStops(&p, m, g)
	f.draw(f.on(s), nil, pipeOver, p)
}

// gradientStops sets p's gradient mapping m and appends g's stops to the
// frame's words, as the shader's gradientAt and sampleStops read them.
func (f *frame) gradientStops(p *prim, m render.GradientMap, g render.Gradient) {
	p.m0 = [4]float32{float32(m.CX), float32(m.CY), float32(m.DX), float32(m.DY)}
	p.m1 = [4]float32{m.InvLength, m.InvRX, m.InvRY, float32(m.From)}
	p.m2 = [4]float32{float32(m.Kind), float32(g.Extend), float32(len(f.words)), float32(len(g.Stops))}
	if !f.roomFor(0, 1+3*len(g.Stops)) {
		return
	}
	// The stops as the shader reads them: the stop range's reciprocal,
	// then each stop's position, color, and its segment's reciprocal.
	lo, hi := float32(g.Stops[0].Pos), float32(g.Stops[len(g.Stops)-1].Pos)
	f.words = append(f.words, math.Float32bits(render.Reciprocal(hi-lo)))
	prev := lo
	for _, st := range g.Stops {
		pos, inv := float32(st.Pos), float32(0)
		if pos-prev > 0 {
			inv = render.Reciprocal(pos - prev)
		}
		f.words = append(f.words, math.Float32bits(pos), uint32(st.Color), math.Float32bits(inv))
		prev = pos
	}
}

// tiled is a primitive of kind over g's tiles, as the shader's tileAt
// reads them.
func tiled(kind uint32, s render.State, g render.TileGeometry) prim {
	p := shaded(kind, s, 0)
	p.quad, p.box, p.radii = quadOf(s.Bounds), g.Box, g.Radii
	p.inner = [4]float32{g.X0, g.Y0, g.W, g.H}
	p.iradii = [4]float32{g.PeriodX, g.PeriodY, g.InvPeriodX, g.InvPeriodY}
	if g.RepeatX {
		p.widths[0] = 1
	}
	if g.RepeatY {
		p.widths[1] = 1
	}
	return p
}

func (f *frame) PaintGradientTiles(s render.State, g render.Gradient, t render.Tile, clip render.Rect, radii render.Corners) {
	if len(g.Stops) == 0 {
		return
	}
	m, ok := g.TileMap(t, f.num, f.denom)
	if !ok {
		return
	}
	tg, ok := render.TileGeometryOf(t, clip, radii, f.num, f.denom)
	if !ok {
		return
	}
	p := tiled(kindGradientTiles, s, tg)
	f.gradientStops(&p, m, g)
	f.draw(f.on(s), nil, pipeOver, p)
}

// PaintImageTiles samples a still or versioned image's texture over
// the tiles; any other image draws as a sprite.
func (f *frame) PaintImageTiles(s render.State, img image.Image, t render.Tile, clip render.Rect, radii render.Corners) {
	ib := img.Bounds()
	tg, ok := render.TileGeometryOf(t, clip, radii, f.num, f.denom)
	if ib.Empty() || !ok {
		return
	}
	tex, ok := f.texture(img)
	if !ok {
		f.sprite(s, "PaintImageTiles", func(cv *render.Canvas) { cv.PaintImageTiles(img, t, clip, radii) })
		return
	}
	if tex == nil {
		return
	}
	p := tiled(kindImageTiles, s, tg)
	p.widths[2], p.widths[3] = render.TexelScale(ib.Dx(), tg.W), render.TexelScale(ib.Dy(), tg.H)
	p.cols[0], p.cols[1] = intconv.Must[uint32](ib.Dx()), intconv.Must[uint32](ib.Dy())
	f.draw(f.on(s), tex, pipeOver, p)
}

func (f *frame) FillPath(s render.State, path *render.Path, col render.Color) {
	if f.err != nil || col == 0 {
		return
	}
	if r, radii, ok := path.RoundedRect(f.num, f.denom); ok {
		f.RoundedRectCorners(s, r, radii, col)
		return
	}
	if m, at, ok := render.PathCoverageMask(path, f.num, f.denom); ok {
		if pos, placed := f.maskSlot(m); placed {
			p := shaded(kindMask, s, 0)
			p.quad, p.inner, p.cols[0] = quadOf(s.Bounds.Intersect(at)), [4]float32{float32(at.X - pos.X), float32(at.Y - pos.Y)}, uint32(col)
			f.draw(f.on(s), f.d.masks.page, pipeOver, p)
			return
		}
	}
	mask, box := f.pathScratch.PathMask(path, s.Bounds, f.num, f.denom)
	if mask == nil {
		return
	}
	at, ok := f.maskUpload(mask, box)
	if !ok {
		return
	}
	p := shaded(kindMask, s, 0)
	p.quad, p.inner, p.cols[0] = quadOf(box), [4]float32{float32(box.X - at.X), float32(box.Y - at.Y)}, uint32(col)
	f.draw(f.on(s), f.d.page, pipeOver, p)
}

// maskUpload uploads mask, box's coverage, to the sprite page this
// segment and returns where; false after an error.
func (f *frame) maskUpload(mask *image.Alpha, box render.Rect) (image.Point, bool) {
	at, ok := f.pageSlot(box.W, box.H)
	if !ok {
		return image.Point{}, false
	}
	out, off := f.stage(box.W * box.H * 4)
	if out == nil {
		return image.Point{}, false
	}
	f.uploads = append(f.uploads, upload{dst: f.d.page, region: image.Rect(at.X, at.Y, at.X+box.W, at.Y+box.H), offset: off, rowLength: box.W})
	o := 0
	for y := range box.H {
		for _, c := range mask.Pix[y*mask.Stride : y*mask.Stride+box.W] {
			out[o], out[o+1], out[o+2], out[o+3] = c, c, c, c
			o += 4
		}
	}
	return at, true
}

func (f *frame) Glyph(s render.State, face *render.Typeface, gid font.GID, scale, penX, penY float64, col render.Color, m *render.GlyphMask) {
	if f.err != nil {
		return
	}
	var pos image.Point
	ok := m != nil
	if ok {
		pos, ok = f.maskSlot(m)
	}
	if !ok {
		f.sprite(s, "Glyph", func(cv *render.Canvas) { cv.DrawGlyph(face, gid, scale, penX, penY, col) })
		return
	}
	at := m.Bounds(penX, penY)
	p := shaded(kindMask, s, 0)
	p.quad, p.inner, p.cols[0] = quadOf(s.Bounds), [4]float32{float32(at.X - pos.X), float32(at.Y - pos.Y)}, uint32(col)
	f.draw(f.on(s), f.d.masks.page, pipeOver, p)
}

func (f *frame) Layer(s render.State, l *render.Layer, region render.Rect) {
	f.readyLayer(l, region, false)
}

func (f *frame) ScratchLayer(s render.State, l *render.Layer, region render.Rect) {
	f.readyLayer(l, region, true)
}

// readyLayer backs l with a surface at least target-sized, its own when
// it fits, else a pooled one (scratch) or a new one, and clears region;
// a kept layer new to this target size clears whole, as a new raster
// layer starts, while a scratch layer's region is all that reads it
// (List.Play trims it to that). A surface another device made is that device's to free.
func (f *frame) readyLayer(l *render.Layer, region render.Rect, scratch bool) {
	if f.err != nil {
		return
	}
	w, h := f.target.s.w, f.target.s.h
	ls, _ := l.Backing().(*layerSurface)
	if ls != nil && ls.s.d != f.d {
		ls = nil
	}
	if ls == nil || !ls.fits(w, h) {
		var have image.Point
		if ls != nil {
			have = image.Pt(ls.s.w, ls.s.h)
			f.d.dropLayer(ls)
		}
		var err error
		if ls, err = f.d.takeLayer(l, w, h, have, scratch); err != nil {
			f.fail(err)
			return
		}
	}
	if !scratch && (ls.w != w || ls.h != h) {
		region = render.Rect{W: w, H: h}
	}
	ls.w, ls.h = w, h
	if err := ls.s.ready(); err != nil {
		f.fail(err)
		return
	}
	clear := region.Intersect(f.bounds())
	if clear.Empty() {
		return
	}
	f.draw(ls.s, nil, pipeReplace, prim{quad: quadOf(clear), info: [4]uint32{kindRect, 255, 0, flagRaw}})
}

func (f *frame) Snapshot(s render.State, l *render.Layer, region render.Rect) {
	r := region.Intersect(f.bounds())
	if f.err != nil || r.Empty() {
		return
	}
	f.steps = append(f.steps, step{kind: stepCopy, src: f.on(s), dst: f.layer(l), region: rectOf(r)})
}

func (f *frame) Release(l *render.Layer) {
	ls, ok := l.Backing().(*layerSurface)
	if !ok {
		panic(fmt.Sprintf("gpu: Release of a layer backed by %T", l.Backing()))
	}
	f.d.poolLayer(ls)
	l.SetBacking(nil)
}

func (f *frame) Composite(s render.State, l *render.Layer, src render.Rect, m render.Affine, alpha float64) {
	f.composite(s, l, src, render.Affine3(m), alpha)
}

func (f *frame) CompositeProjective(s render.State, l *render.Layer, src render.Rect, m render.M3, alpha float64) {
	f.composite(s, l, src, m, alpha)
}

// composite is Composite and CompositeProjective: each covered pixel
// inverse-maps into src and samples it bilinearly.
func (f *frame) composite(s render.State, l *render.Layer, src render.Rect, m render.M3, alpha float64) {
	ls := f.layer(l)
	src = src.Intersect(f.bounds())
	mp, ok := render.MappedOf(m)
	a := s.Alpha * min(1, max(0, alpha))
	if src.Empty() || !ok || alphaScale(a) == 0 {
		return
	}
	s.Alpha = a
	p := shaded(kindComposite, s, 0)
	p.quad, p.inner = quadOf(s.Bounds.Intersect(f.bounds())), quadOf(src)
	p.m0, p.m1, p.m2, p.widths = mappedRows(&mp)
	f.draw(f.on(s), ls, pipeOver, p)
}

func (f *frame) DrawLayer(s render.State, l *render.Layer, src render.Rect, mode render.BlendMode) {
	if mode != render.BlendNormal {
		f.blendLayer(s, l, src, mode)
		return
	}
	p := shaded(kindTexture, s, flagNoBright)
	p.quad = quadOf(s.Bounds)
	f.draw(f.on(s), f.layer(l), pipeOver, p)
}

// blendLayer draws l's src rect over the backdrop under mode in place,
// as render's DrawLayer blends: the backdrop region copied aside, then
// every pixel through the shader's blendPixel. A region past the
// filter surfaces' reach blends on the CPU.
func (f *frame) blendLayer(s render.State, l *render.Layer, src render.Rect, mode render.BlendMode) {
	dst, ls := f.on(s), f.layer(l)
	r := s.Bounds.Intersect(src).Intersect(f.bounds())
	a := alphaScale(s.Alpha)
	if r.Empty() || a == 0 || f.err != nil {
		return
	}
	if !f.filterRoom(r.X+r.W, r.Y+r.H) {
		f.cpu(s.Bounds, []*surface{dst, ls}, func(cvs []*render.Canvas) {
			cvs[0].ApplyState(s)
			cvs[0].DrawLayer(render.LayerOf(cvs[1]), src, mode)
		})
		return
	}
	var p prim
	p.cols[0], p.cols[2] = uint32(mode), a
	if mode == render.BlendSoftLight {
		if !f.roomFor(0, len(render.SoftLightTable)) {
			return
		}
		p.cols[1] = count(f.words)
		for _, v := range render.SoftLightTable {
			f.words = append(f.words, intconv.Uint32Bits(v))
		}
	}
	f.inPlace(dst, r, ls, kindBlend, p)
}

func (f *frame) CrossFade(l, other *render.Layer, region render.Rect, t float64) {
	o := f.layer(other)
	var p prim
	p.cols[0] = uint32(math.Round(min(1, max(0, t)) * 255))
	f.inPlace(f.layer(l), region.Intersect(f.bounds()), o, kindCrossFade, p)
}

func (f *frame) ApplyMaskLayer(l, mask *render.Layer, region render.Rect, luminance bool) {
	m := f.layer(mask)
	var p prim
	if luminance {
		p.cols[0] = 1
	}
	f.inPlace(f.layer(l), region.Intersect(f.bounds()), m, kindMaskLayer, p)
}

func (f *frame) ApplyPathMask(l *render.Layer, path *render.Path, region render.Rect) {
	s := f.layer(l)
	r := region.Intersect(f.bounds())
	if f.err != nil || r.Empty() {
		return
	}
	var p prim
	if rr, radii, ok := path.RoundedRect(f.num, f.denom); ok {
		p.box, p.radii = render.DeviceBox(rr, radii, f.num, f.denom)
		f.inPlace(s, r, nil, kindRRectMask, p)
		return
	}
	if m, at, ok := render.PathCoverageMask(path, f.num, f.denom); ok {
		if pos, placed := f.maskSlot(m); placed {
			p.box, p.radii = quadOf(at), [4]float32{float32(pos.X), float32(pos.Y)}
			f.inPlace(s, r, f.d.masks.page, kindPathMask, p)
			return
		}
	}
	mask, box := f.pathScratch.PathMask(path, r, f.num, f.denom)
	var page *surface
	if mask != nil {
		at, ok := f.maskUpload(mask, box)
		if !ok {
			return
		}
		p.box, p.radii, page = quadOf(box), [4]float32{float32(at.X), float32(at.Y)}, f.d.page
	}
	f.inPlace(s, r, page, kindPathMask, p)
}

func (f *frame) ApplyFilters(l *render.Layer, region render.Rect, fl *render.Filters, shape render.FilterShape) {
	s := f.layer(l)
	region = region.Intersect(f.bounds())
	f.filterSteps = fl.Steps(f.filterSteps[:0], shape, f.num, f.denom)
	if f.gpuSteps(f.filterSteps, region) {
		for i := range f.filterSteps {
			st := &f.filterSteps[i]
			switch st.Kind {
			case render.StepMatrix:
				f.matrixStep(s, region, st.Matrix)
			case render.StepBlur:
				f.blur(s, region, st.Blur)
			case render.StepShadow:
				f.dropShadow(s, region, st.Shadow)
			case render.StepRefract:
				f.refract(s, region, st.Refract)
			}
		}
		return
	}
	f.cpu(region, []*surface{s}, func(cvs []*render.Canvas) {
		render.ApplyFilters(render.LayerOf(cvs[0]), region, fl, shape)
	})
}

// gpuSteps reports whether the GPU runs steps on region: the
// shader's int32 fixed point holds every linear color step (a
// channel's three weighted products and the rounding half stay below
// 2^31), and every shadow's silhouette fits a filter surface the
// device can make.
func (f *frame) gpuSteps(steps []render.FilterStep, region render.Rect) bool {
	for _, st := range steps {
		switch st.Kind {
		case render.StepBlur:
			continue
		case render.StepRefract:
			if !f.filterRoom(region.X+region.W, region.Y+region.H) {
				return false
			}
			continue
		case render.StepShadow:
			if w, h := st.Shadow.Size(region); !f.filterRoom(w, h) {
				return false
			}
			continue
		}
		for row := range 3 {
			var sum int64
			for _, k := range st.Matrix.K[row*3 : row*3+3] {
				sum += max(int64(k), -int64(k)) * 255
			}
			if sum+32768 >= 1<<31 {
				return false
			}
		}
	}
	return true
}

// refract bends s's region in place on the GPU through p, as render's
// refractRegion does: the region copied aside, every bending pixel
// sampled bilinearly from the copy at its displacement.
func (f *frame) refract(s *surface, region render.Rect, p render.RefractPlan) {
	var pr prim
	pr.box, pr.radii = p.Box, p.Radii
	pr.inner = [4]float32{float32(region.X), float32(region.Y), float32(region.W), float32(region.H)}
	pr.m0 = [4]float32{p.InvBezel, p.Disp}
	f.inPlace(s, region, nil, kindRefract, pr)
}

// matrixStep applies one color matrix step to s's region in place on
// the GPU, as render's applyMatrixRegion computes it.
func (f *frame) matrixStep(s *surface, region render.Rect, st render.MatrixStep) {
	if st.Identity {
		return
	}
	var p prim
	m := st.M
	p.box, p.radii, p.inner, p.iradii, p.widths = [4]float32(m[0:4]), [4]float32(m[4:8]), [4]float32(m[8:12]), [4]float32(m[12:16]), [4]float32(m[16:20])
	if st.Linear {
		k := st.K
		p.m0 = [4]float32{float32(k[0]), float32(k[1]), float32(k[2]), 1}
		p.m1 = [4]float32{float32(k[3]), float32(k[4]), float32(k[5])}
		p.m2 = [4]float32{float32(k[6]), float32(k[7]), float32(k[8])}
	}
	p.m1[3] = render.Inv255
	f.inPlace(s, region, nil, kindMatrix, p)
}

// blur blurs s's region in place on the GPU by render's plan, step for
// step: at full resolution the region copies aside and the box passes
// ping-pong between the two filter surfaces before copying back; at
// 1/D the region area-averages into the first, the passes run there,
// and it samples back into s.
func (f *frame) blur(s *surface, region render.Rect, pl render.BlurPlan) {
	r := region.Intersect(f.bounds())
	if f.err != nil || r.Empty() {
		return
	}
	t1, t2 := f.d.filterTemp, f.d.filterTemp2
	work := r
	if pl.D == 1 {
		f.steps = append(f.steps, step{kind: stepCopy, src: s, dst: t1, region: rectOf(r)})
	} else {
		work = render.Rect{X: r.X, Y: r.Y, W: (r.W + pl.D - 1) / pl.D, H: (r.H + pl.D - 1) / pl.D}
		p := prim{quad: quadOf(work), box: quadOf(r), info: [4]uint32{kindDownsample, 255, 0, flagRaw}}
		p.radii[0] = float32(pl.D)
		f.draw(t1, s, pipeReplace, p)
	}
	f.boxPasses(t1, t2, work, pl.Boxes)
	if pl.D == 1 {
		f.steps = append(f.steps, step{kind: stepCopy, src: t1, dst: s, region: rectOf(r)})
		return
	}
	p := prim{quad: quadOf(r), box: quadOf(work), info: [4]uint32{kindUpsample, 255, 0, flagRaw}}
	p.radii[0] = float32(pl.D)
	f.draw(s, t1, pipeReplace, p)
}

// filterRoom reports whether the filter surfaces hold w x h, growing
// them when the device allows, after flushing the segment that may
// still use the old ones.
func (f *frame) filterRoom(w, h int) bool {
	d := f.d
	if w <= d.filterW && h <= d.filterH {
		return true
	}
	if f.err != nil || w > d.maxDim || h > d.maxDim {
		return false
	}
	if f.err = f.flush(false); f.err != nil {
		return false
	}
	f.err = d.fitFilters(w, h)
	return f.err == nil
}

// dropShadow draws pl's shadow under s's region on the GPU, as
// render's dropShadowRegion does: the region's alpha silhouette goes
// into the second filter surface at the origin, the box passes
// ping-pong it with the first (an even count, so it ends where it
// started), and the region composites over it in place.
func (f *frame) dropShadow(s *surface, r render.Rect, pl render.ShadowPlan) {
	w, h := pl.Size(r)
	if f.err != nil || r.Empty() || w <= 0 || h <= 0 {
		return
	}
	work := render.Rect{W: w, H: h}
	p := prim{quad: quadOf(work), box: quadOf(r), info: [4]uint32{kindSilhouette, 255, 0, flagRaw}}
	p.radii[0] = float32(pl.Margin)
	t1, t2 := f.d.filterTemp2, f.d.filterTemp
	f.draw(t1, s, pipeReplace, p)
	f.boxPasses(t1, t2, work, pl.Boxes)
	p = prim{box: quadOf(r), radii: [4]float32{float32(pl.Margin - pl.DX), float32(pl.Margin - pl.DY), float32(w), float32(h)}}
	p.cols[0] = uint32(pl.Tint)
	f.inPlace(s, r, t1, kindShadow, p)
}

// boxPasses runs render's box passes of radii over t1's work rect,
// ping-ponging through t2; each pass pair leaves the result in t1.
func (f *frame) boxPasses(t1, t2 *surface, work render.Rect, radii [3]int) {
	for _, rad := range radii {
		if rad < 1 {
			continue
		}
		half, recip := render.BoxDivisor(2*rad + 1)
		for _, kind := range [2]uint32{kindBoxH, kindBoxV} {
			p := prim{quad: quadOf(work), box: quadOf(work), cols: [4]uint32{half, recip}, info: [4]uint32{kind, 255, 0, flagRaw}}
			p.radii[0] = float32(rad)
			f.draw(t2, t1, pipeReplace, p)
			t1, t2 = t2, t1
		}
	}
}

// inPlace reworks s's region r in place: it copies the region aside
// and draws a prim of kind over it that reads the copy (and tex2, the
// other layer, mask or coverage it combines with), writing as is.
func (f *frame) inPlace(s *surface, r render.Rect, tex2 *surface, kind uint32, p prim) {
	r = r.Intersect(f.bounds())
	if f.err != nil || r.Empty() {
		return
	}
	f.steps = append(f.steps, step{kind: stepCopy, src: s, dst: f.d.filterTemp, region: rectOf(r)})
	p.quad, p.info = quadOf(r), [4]uint32{kind, 255, 0, flagRaw}
	f.draw2(s, f.d.filterTemp, tex2, pipeReplace, p)
}
