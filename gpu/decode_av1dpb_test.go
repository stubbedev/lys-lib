package gpu

import "testing"

// Refreshing reference frame slots counts the slots holding each DPB
// picture, and a hidden frame no slot holds any more is handed back.
func TestAV1RefreshReleasesHiddenFrames(t *testing.T) {
	c := &av1Decoder{}
	c.forget()
	c.refresh(0, 0xff)
	if c.pictures[0].users != 8 {
		t.Fatalf("a key frame's picture is held by %d slots, want 8", c.pictures[0].users)
	}
	hidden := &VideoFrame{state: frameQueued}
	c.pictures[1].frame = hidden
	c.refresh(1, 0x01)
	if c.pictures[0].users != 7 || c.pictures[1].users != 1 || c.slotOf[0] != 1 {
		t.Fatalf("users %d and %d, slot 0 in DPB slot %d; want 7, 1 and 1", c.pictures[0].users, c.pictures[1].users, c.slotOf[0])
	}
	if hidden.state != frameQueued {
		t.Fatal("a hidden frame a slot still holds was handed back")
	}
	c.refresh(2, 0x01)
	if c.pictures[1].users != 0 || hidden.state != frameFree || c.pictures[1].frame != nil {
		t.Errorf("after its last slot moved on: users %d, frame state %d; want 0 and free", c.pictures[1].users, hidden.state)
	}
	c.pictures[2].frame = &VideoFrame{state: frameQueued}
	kept := c.pictures[2].frame
	c.reset()
	if kept.state != frameFree || c.slotOf != [8]int{-1, -1, -1, -1, -1, -1, -1, -1} {
		t.Errorf("after a reset: frame state %d, slots %v", kept.state, c.slotOf)
	}
}
