package gpu

import (
	"fmt"
	"image"
)

// refFrameSlots are the reference frame slots of AV1 and VP9 streams;
// refSlotPictures the DPB slots they need, a picture for each slot and
// the one being decoded.
const (
	refFrameSlots   = 8
	refSlotPictures = refFrameSlots + 1
)

// refSlots is the reference state of a codec whose frames refresh
// numbered reference frame slots (AV1, VP9): each slot names the DPB
// slot of the picture it holds, -1 for none, and each DPB slot's
// picture counts the slots holding it.
type refSlots struct {
	slotOf   [refFrameSlots]int
	pictures [refSlotPictures]slotPicture
}

// slotPicture is a DPB slot's picture: how many reference frame slots
// hold it, and its output frame while the stream may still show it (a
// frame decoded hidden, to be shown by a later frame header) with the
// picture's rectangle in it.
type slotPicture struct {
	users int
	frame *VideoFrame
	rect  image.Rectangle
}

// forget drops every reference picture.
func (r *refSlots) forget() {
	for i := range r.slotOf {
		r.slotOf[i] = -1
	}
	for i := range r.pictures {
		r.release(i)
		r.pictures[i].users = 0
	}
}

// release drops slot's hidden frame, which nothing can show any more.
func (r *refSlots) release(slot int) {
	p := &r.pictures[slot]
	if p.frame != nil {
		p.frame.free(0)
		p.frame = nil
	}
}

// free is a DPB slot no reference frame slot holds, for the next
// picture to set up.
func (r *refSlots) free() int {
	for i := range r.pictures {
		if r.pictures[i].users == 0 {
			return i
		}
	}
	panic("gpu: every DPB slot is held, more than there are reference frame slots")
}

// refresh points the reference frame slots flags names at DPB slot
// setup, releasing the pictures no slot holds any more.
func (r *refSlots) refresh(setup int, flags uint8) {
	for i := range r.slotOf {
		if flags>>i&1 == 0 {
			continue
		}
		if old := r.slotOf[i]; old >= 0 {
			if r.pictures[old].users--; r.pictures[old].users == 0 {
				r.release(old)
			}
		}
		r.slotOf[i] = setup
		r.pictures[setup].users++
	}
}

// keep holds f, decoded hidden into DPB slot setup, for a later frame
// header to show when the stream may show it and a slot holds it; else
// f goes back to the free frames.
func (r *refSlots) keep(setup int, f *VideoFrame, showable bool) {
	if p := &r.pictures[setup]; showable && p.users > 0 {
		p.frame = f
		return
	}
	f.free(0)
}

// show hands out a frame with the picture reference frame slot idx
// holds, and the picture's DPB slot, for a frame header showing it: the
// frame the picture was decoded hidden into, or, once that has gone
// out, as where the picture was shown when it was decoded, a frame v
// copies the picture into again (copied), which has none of the film
// grain a shown picture takes.
func (r *refSlots) show(v *VideoDecoder, idx uint8) (f *VideoFrame, slot int, copied bool, err error) {
	slot = r.slotOf[idx]
	if slot < 0 {
		return nil, 0, false, fmt.Errorf("gpu: a frame header shows reference frame slot %d, which holds no decoded picture", idx)
	}
	p := &r.pictures[slot]
	f, p.frame = p.frame, nil
	if f == nil {
		copied = true
		if f, err = v.again(slot); err != nil {
			return nil, 0, false, fmt.Errorf("reference frame slot %d: %w", idx, err)
		}
	}
	f.rect = p.rect
	return f, slot, copied, nil
}
