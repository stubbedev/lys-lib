package gpu

import (
	"bytes"
	"image"
	"image/color"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-lib/fourcc"
	"github.com/stubbedev/lys-lib/third_party/vk"
)

func textureBytes(t *testing.T, tx *Texture) []byte {
	t.Helper()
	d := tx.d
	n := tx.w * tx.h * tx.pf.size
	staging, err := d.newReadBuffer(n, vk.BufferUsageTransferDstBit, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer staging.close()
	err = d.submit(func(cb vk.CommandBuffer) {
		fullBarrier(d, cb, tx.image, vk.ImageLayoutShaderReadOnlyOptimal, vk.ImageLayoutTransferSrcOptimal, vk.QueueFamilyIgnored, vk.QueueFamilyIgnored)
		region := vk.BufferImageCopy{
			ImageSubresource: vk.ImageSubresourceLayers{AspectMask: vk.ImageAspectFlags(vk.ImageAspectColorBit), LayerCount: 1},
			ImageExtent:      extent3D(tx.w, tx.h),
		}
		d.cmds.CmdCopyImageToBuffer(cb, tx.image, vk.ImageLayoutTransferSrcOptimal, staging.buf, 1, &region)
		fullBarrier(d, cb, tx.image, vk.ImageLayoutTransferSrcOptimal, vk.ImageLayoutShaderReadOnlyOptimal, vk.QueueFamilyIgnored, vk.QueueFamilyIgnored)
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := staging.invalidate(); err != nil {
		t.Fatal(err)
	}
	return bytes.Clone(staging.mapped[:n])
}

func fill(w, h, stride int, px [4]byte) []byte {
	b := make([]byte, stride*h)
	for y := range h {
		for x := range w {
			copy(b[y*stride+x*4:], px[:])
		}
	}
	return b
}

func TestTextureUpdatesOnlyItsRect(t *testing.T) {
	d := openDevice(t)
	for _, f := range d.TextureFormats() {
		tx, err := d.NewTexture(f, 8, 6)
		if err != nil {
			t.Fatal(err)
		}
		a, b := [4]byte{1, 2, 3, 4}, [4]byte{50, 60, 70, 80}
		if err := tx.Update(image.Rect(0, 0, 8, 6), 32, fill(8, 6, 32, a)); err != nil {
			t.Fatal(err)
		}
		r := image.Rect(2, 1, 5, 4)
		if err := tx.Update(r, 20, fill(3, 3, 20, b)[:20*2+12]); err != nil {
			t.Fatal(err)
		}
		got := textureBytes(t, tx)
		for y := range 6 {
			for x := range 8 {
				want := a
				if image.Pt(x, y).In(r) {
					want = b
				}
				if px := [4]byte(got[(y*8+x)*4:]); px != want {
					t.Fatalf("%s: pixel %d,%d is %v, want %v", f, x, y, px, want)
				}
			}
		}
		if tx.Size() != image.Pt(8, 6) || tx.Format() != f {
			t.Errorf("texture %v %s, want 8x6 %s", tx.Size(), tx.Format(), f)
		}
		tx.Close()
	}
}

func TestTextureStartsTransparent(t *testing.T) {
	d := openDevice(t)
	tx, err := d.NewTexture(fourcc.ARGB8888, 4, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Close()
	if err := tx.Update(image.Rect(1, 1, 2, 2), 4, []byte{9, 9, 9, 9}); err != nil {
		t.Fatal(err)
	}
	got := textureBytes(t, tx)
	for i := 0; i < len(got); i += 4 {
		want := [4]byte{}
		if i == (1*4+1)*4 {
			want = [4]byte{9, 9, 9, 9}
		}
		if [4]byte(got[i:]) != want {
			t.Fatalf("pixel %d is %v, want %v", i/4, got[i:i+4], want)
		}
	}
}

func TestTextureRefusesBadRequests(t *testing.T) {
	d := openDevice(t)
	for _, f := range []fourcc.Format{fourcc.NV12, fourcc.Format(0)} {
		if _, err := d.NewTexture(f, 4, 4); err == nil {
			t.Errorf("a %s texture was made", f)
		}
	}
	if _, err := d.NewTexture(fourcc.ARGB8888, 0, 4); err == nil {
		t.Error("a zero-width texture was made")
	}
	tx, err := d.NewTexture(fourcc.ARGB8888, 4, 4)
	if err != nil {
		t.Fatal(err)
	}
	px := make([]byte, 64)
	for name, err := range map[string]error{
		"outside":      tx.Update(image.Rect(2, 2, 5, 3), 12, px),
		"empty":        tx.Update(image.Rect(1, 1, 1, 3), 4, px),
		"short stride": tx.Update(image.Rect(0, 0, 4, 1), 12, px),
		"odd stride":   tx.Update(image.Rect(0, 0, 2, 2), 10, px),
		"short pixels": tx.Update(image.Rect(0, 0, 4, 4), 16, px[:63]),
	} {
		if err == nil {
			t.Errorf("%s update taken", name)
		}
	}
	tx.Close()
	if err := tx.Update(image.Rect(0, 0, 1, 1), 4, px); err == nil {
		t.Error("a closed texture took an update")
	}
	tx.Close()
}

func TestDeviceCloseClosesTextures(t *testing.T) {
	d := openDevice(t)
	tx, err := d.NewTexture(fourcc.XBGR8888, 2, 2)
	if err != nil {
		t.Fatal(err)
	}
	d.Close()
	if !tx.closed {
		t.Fatal("the device closed and left its texture open")
	}
	tx.Close()
}

func TestManyUpdatesInARowLandInOrder(t *testing.T) {
	d := openDevice(t)
	tx, err := d.NewTexture(fourcc.ARGB8888, 64, 64)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Close()
	pix := make([]byte, 64*64*4)
	for i := range 500 {
		v := byte(i)
		for j := range pix {
			pix[j] = v
		}
		if err := tx.Update(image.Rect(0, 0, 64, 64), 64*4, pix); err != nil {
			t.Fatal(err)
		}
		clear(pix)
	}
	want := byte(499 % 256)
	for i, b := range textureBytes(t, tx) {
		if b != want {
			t.Fatalf("byte %d is %d, want the last update's %d", i, b, want)
		}
	}
}

func TestAnUpdateLargerThanTheRingGrowsIt(t *testing.T) {
	d := openDevice(t)
	small, err := d.NewTexture(fourcc.ARGB8888, 8, 8)
	if err != nil {
		t.Fatal(err)
	}
	defer small.Close()
	large, err := d.NewTexture(fourcc.ARGB8888, 512, 512)
	if err != nil {
		t.Fatal(err)
	}
	defer large.Close()
	a, b := [4]byte{1, 2, 3, 4}, [4]byte{9, 8, 7, 6}
	if err := small.Update(image.Rect(0, 0, 8, 8), 32, fill(8, 8, 32, a)); err != nil {
		t.Fatal(err)
	}
	if err := large.Update(image.Rect(0, 0, 512, 512), 2048, fill(512, 512, 2048, b)); err != nil {
		t.Fatal(err)
	}
	if px := [4]byte(textureBytes(t, small)); px != a {
		t.Errorf("the small texture holds %v, want %v", px, a)
	}
	if px := [4]byte(textureBytes(t, large)); px != b {
		t.Errorf("the large texture holds %v, want %v", px, b)
	}
}

func TestASteadyUpdateDoesNotWaitOrAllocate(t *testing.T) {
	if raceEnabled {
		t.Skip("the race detector allocates on its own")
	}
	d := openDevice(t)
	tx, err := d.NewTexture(fourcc.ARGB8888, 64, 64)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Close()
	pix := make([]byte, 64*64*4)
	update := func() {
		if err := tx.Update(image.Rect(0, 0, 64, 64), 64*4, pix); err != nil {
			t.Fatal(err)
		}
	}
	for range 100 {
		update()
	}
	if n := testing.AllocsPerRun(100, update); n != 0 {
		t.Fatalf("a steady update allocated %v times", n)
	}
}

func TestAnUpdateWaitsForNoSubmission(t *testing.T) {
	d := openDevice(t)
	tx, err := d.NewTexture(fourcc.ARGB8888, 8, 8)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Close()
	if err := d.Finish(); err != nil {
		t.Fatal(err)
	}
	if err := tx.Update(image.Rect(0, 0, 8, 8), 32, make([]byte, 8*8*4)); err != nil {
		t.Fatal(err)
	}
	if d.batched != 1 || len(d.inflight) != 0 {
		t.Fatalf("after an update: %d uploads batched, %d submissions in flight", d.batched, len(d.inflight))
	}
}

// A frame's uploads and the pass that shows them are one submission, and
// the pass samples what they wrote.
func TestUploadsTravelWithTheirPass(t *testing.T) {
	d := openDevice(t)
	tg, err := d.NewOffscreen(8, 8)
	if err != nil {
		t.Fatal(err)
	}
	defer tg.Close()
	textures := make([]*Texture, 3)
	for i := range textures {
		if textures[i], err = d.NewTexture(fourcc.ARGB8888, 2, 2); err != nil {
			t.Fatal(err)
		}
		defer textures[i].Close()
	}
	all := []image.Rectangle{{Max: tg.Size()}}
	runPass(t, d, tg, all, func(p *Pass) { p.Clear(color.RGBA{A: 255}) })
	if err := d.Finish(); err != nil {
		t.Fatal(err)
	}
	for i, tx := range textures {
		if err := tx.Update(image.Rect(0, 0, 2, 2), 8, fill(2, 2, 8, [4]byte{0, 0, byte(50 * (i + 1)), 255})); err != nil {
			t.Fatal(err)
		}
	}
	runPass(t, d, tg, all, func(p *Pass) {
		for i, tx := range textures {
			p.DrawTexture(tx, FRect{W: 2, H: 2}, image.Rect(i*2, 0, i*2+2, 8), TransformNormal, 1, Mask{})
		}
	})
	if len(d.inflight) != 1 || d.batch != 0 {
		t.Errorf("%d submissions for three uploads and a pass, batch %d still open", len(d.inflight), d.batch)
	}
	img := readTarget(t, tg)
	for i := range textures {
		want := color.RGBA{uint8(50 * (i + 1)), 0, 0, 255}
		if got := img.RGBAAt(i*2+1, 3); got != want {
			t.Errorf("window %d shows %v, want %v", i, got, want)
		}
	}
}

// Everything that waits or signals takes the open batch along.
func TestUploadsLeaveWithTheNextSubmission(t *testing.T) {
	d := openDevice(t)
	tx, err := d.NewTexture(fourcc.ARGB8888, 2, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Close()
	px := fill(2, 2, 8, [4]byte{9, 8, 7, 255})
	update := func() {
		t.Helper()
		if err := tx.Update(image.Rect(0, 0, 2, 2), 8, px); err != nil {
			t.Fatal(err)
		}
	}
	update()
	if err := d.Finish(); err != nil {
		t.Fatal(err)
	}
	if d.batch != 0 || d.batched != 0 || d.uploads.pending != 0 {
		t.Errorf("after Finish: batch %d of %d, %d pending on the ring", d.batch, d.batched, d.uploads.pending)
	}
	if !d.SyncFiles() {
		return
	}
	update()
	fd, err := d.SyncFile()
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	if d.batch != 0 || d.batched != 0 {
		t.Errorf("after SyncFile: batch %d of %d", d.batch, d.batched)
	}
	if err := d.Finish(); err != nil {
		t.Fatal(err)
	}
	if d.uploads.pending != 0 {
		t.Errorf("after SyncFile and Finish, %d uploads hold the ring", d.uploads.pending)
	}
}

// Uploads that outgrow the ring with no pass to carry them submit
// themselves, in order.
func TestUploadsWithNoPassOutrunTheRing(t *testing.T) {
	d := openDevice(t)
	const w, h = 512, 512
	tx, err := d.NewTexture(fourcc.ARGB8888, w, h)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Close()
	var last byte
	for i := range 200 {
		last = byte(i)
		if err := tx.Update(image.Rect(0, 0, w, h), w*4, fill(w, h, w*4, [4]byte{last, 0, 0, 255})); err != nil {
			t.Fatal(err)
		}
	}
	if got := textureBytes(t, tx); got[0] != last {
		t.Errorf("the texture holds %d, want the last update's %d", got[0], last)
	}
}

func TestManyTinyUploadsBoundTheBatch(t *testing.T) {
	d := openDevice(t)
	tx, err := d.NewTexture(fourcc.ARGB8888, 2, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Close()
	px := fill(2, 2, 8, [4]byte{1, 2, 3, 255})
	for range 3 * maxBatchedUploads {
		if err := tx.Update(image.Rect(0, 0, 2, 2), 8, px); err != nil {
			t.Fatal(err)
		}
		if d.batched >= maxBatchedUploads {
			t.Fatalf("%d uploads batched, the bound is %d", d.batched, maxBatchedUploads)
		}
	}
}

func TestAWideStrideUploadsOnlyItsRows(t *testing.T) {
	d := openDevice(t)
	tx, err := d.NewTexture(fourcc.ARGB8888, 8, 6)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Close()
	if err := tx.Update(image.Rect(0, 0, 1, 1), 4, make([]byte, 4)); err != nil {
		t.Fatal(err)
	}
	if err := d.Finish(); err != nil {
		t.Fatal(err)
	}
	const stride = 4096
	r := image.Rect(2, 1, 5, 4)
	px := [4]byte{50, 60, 70, 80}
	if err := tx.Update(r, stride, fill(3, 3, stride, px)[:stride*2+12]); err != nil {
		t.Fatal(err)
	}
	if used, want := d.uploads.head-d.uploads.tail, 3*3*4; used != want {
		t.Errorf("staging took %d bytes for %d bytes of rows", used, want)
	}
	got := textureBytes(t, tx)
	for y := range 6 {
		for x := range 8 {
			if image.Pt(x, y).In(r) && [4]byte(got[(y*8+x)*4:]) != px {
				t.Fatalf("pixel %d,%d is %v, want %v", x, y, got[(y*8+x)*4:(y*8+x)*4+4], px)
			}
		}
	}
}
