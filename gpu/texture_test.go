package gpu

import (
	"bytes"
	"image"
	"testing"

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
		fullBarrier(d, cb, tx.image, tx.layout, vk.ImageLayoutTransferSrcOptimal, vk.QueueFamilyIgnored, vk.QueueFamilyIgnored)
		region := vk.BufferImageCopy{
			ImageSubresource: vk.ImageSubresourceLayers{AspectMask: vk.ImageAspectFlags(vk.ImageAspectColorBit), LayerCount: 1},
			ImageExtent:      extent3D(tx.w, tx.h),
		}
		d.cmds.CmdCopyImageToBuffer(cb, tx.image, vk.ImageLayoutTransferSrcOptimal, staging.buf, 1, &region)
		fullBarrier(d, cb, tx.image, vk.ImageLayoutTransferSrcOptimal, tx.layout, vk.QueueFamilyIgnored, vk.QueueFamilyIgnored)
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
