package xkb

const (
	breaksModLatch actionFlags = 1 << iota
	breaksGroupLatch
)

const (
	syntheticLatchKey int32 = -1
	syntheticBreakKey int32 = -2
)

var noActions = [1]action{{typ: actNone}}

func (s *State) keyActions(k *flatKey) []action {
	if k == nil {
		return s.synth[:]
	}
	layout := s.keyLayout(k)
	lvl := s.keyLevel(k, layout)
	if lvl == levelInvalid {
		return noActions[:]
	}
	l := s.keymap.keyLevel(k, layout, lvl)
	if l == nil || l.numActions == 0 {
		return noActions[:]
	}
	return s.keymap.levelActions(l)
}

func breaksLatch(a *action, flag actionFlags, mask uint32) bool {
	switch a.typ {
	case actNone, actVoid, actPtrButton, actPtrLock, actCtrlSet, actCtrlLock, actSwitchVT, actTerminate:
		return true
	case actInternal:
		return a.flags&flag != 0 && a.mask&mask == mask
	}
	return false
}

func (s *State) filterNew(f *filter) {
	switch f.kind {
	case actModSet:
		s.modSetNew(f)
	case actModLock:
		s.modLockNew(f)
	case actModLatch:
		s.modLatchNew(f)
	case actGroupSet:
		f.priv = uint32(s.c.baseGroup) //nolint:gosec // a bit-for-bit copy, restored as int32
		applyGroupDelta(f, &s.c.baseGroup)
	case actGroupLock:
		if f.act.flags&flagLockOnRelease == 0 {
			applyGroupDelta(f, &s.c.lockedGroup)
		}
	case actGroupLatch:
		f.latch = latchKeyDown
		f.delta = f.act.group
		if f.act.flags&flagAbsoluteSwitch != 0 {
			f.delta = f.act.group - s.c.baseGroup
		}
		applyGroupDelta(f, &s.c.baseGroup)
	}
}

func (s *State) modSetNew(f *filter) {
	if f.act.flags&(flagUnlockOnPress|flagLockClear) == flagUnlockOnPress|flagLockClear {
		f.priv = f.act.mask &^ s.c.lockedMods
		s.c.lockedMods &^= f.act.mask
	} else {
		f.priv = f.act.mask
	}
	s.setMods |= f.priv
}

func (s *State) modSetFunc(f *filter, ki int32, dir KeyDirection) bool {
	if ki != f.key {
		f.act.flags &^= flagLockClear
		return false
	}
	if dir == KeyPressed {
		f.refcnt++
		return true
	}
	f.refcnt--
	if f.refcnt > 0 {
		return true
	}
	s.clearMods |= f.priv
	if f.act.flags&(flagUnlockOnPress|flagLockClear) == flagLockClear {
		s.c.lockedMods &^= f.act.mask
	}
	s.release(f)
	return false
}

func (s *State) modLockNew(f *filter) {
	f.priv = s.c.lockedMods & f.act.mask
	if f.priv != 0 && f.act.flags&flagUnlockOnPress != 0 {
		if f.act.flags&flagLockNoUnlock == 0 {
			s.c.lockedMods &^= f.priv
		}
		s.release(f)
		return
	}
	s.setMods |= f.act.mask
	if f.act.flags&flagLockNoLock == 0 {
		s.c.lockedMods |= f.act.mask
	}
}

func (s *State) modLockFunc(f *filter, ki int32, dir KeyDirection) bool {
	if ki != f.key {
		return false
	}
	if dir == KeyPressed {
		f.refcnt++
		return true
	}
	f.refcnt--
	if f.refcnt > 0 {
		return true
	}
	s.clearMods |= f.act.mask
	if f.act.flags&flagLockNoUnlock == 0 {
		s.c.lockedMods &^= f.priv
	}
	s.release(f)
	return false
}

func (s *State) modLatchNew(f *filter) {
	switch {
	case f.act.flags&flagLockClear != 0 && f.act.flags&(flagUnlockOnPress|flagLatchOnPress) != 0 && s.c.lockedMods&f.act.mask == f.act.mask:
		s.c.lockedMods &^= f.act.mask
		s.release(f)
	case f.act.flags&flagLatchOnPress != 0:
		f.latch = latchPending
		s.c.latchedMods |= f.act.mask
	default:
		f.latch = latchKeyDown
		s.setMods |= f.act.mask
	}
}

func (s *State) latchSurvivesPress(k *flatKey, flag actionFlags, mask uint32) bool {
	if s.keymap.format == FormatTextV1 {
		return false
	}
	actions := s.keyActions(k)
	for i := range actions {
		if breaksLatch(&actions[i], flag, mask) {
			return false
		}
	}
	return true
}

func (s *State) modLatchFunc(f *filter, ki int32, k *flatKey, dir KeyDirection) bool {
	latch := f.latch
	switch {
	case dir == KeyPressed && latch == latchKeyDown:
		if !s.latchSurvivesPress(k, breaksModLatch, f.act.mask) {
			latch = noLatch
		}
	case dir == KeyPressed && latch == latchPending:
		actions := s.keyActions(k)
		for i := range actions {
			a := &actions[i]
			if a.typ == actModLatch && a.flags == f.act.flags && a.mask == f.act.mask {
				f.act = *a
				if a.flags&flagLatchToLock != 0 {
					f.act.typ, f.kind = actModLock, actModLock
				} else {
					f.act.typ, f.kind = actModSet, actModSet
				}
				s.filterNew(f)
				f.key = ki
				s.c.latchedMods &^= f.act.mask
				return true
			}
			if breaksLatch(a, breaksModLatch, f.act.mask) {
				s.c.latchedMods &^= f.act.mask
				s.release(f)
				return false
			}
		}
	case dir == KeyReleased && ki == f.key:
		switch {
		case f.act.flags&flagLockClear != 0 && f.act.flags&(flagUnlockOnPress|flagLatchOnPress) == 0 && s.c.lockedMods&f.act.mask == f.act.mask:
			if latch == latchPending {
				s.c.latchedMods &^= f.act.mask
			} else {
				s.clearMods |= f.act.mask
			}
			s.c.lockedMods &^= f.act.mask
			s.release(f)
		case latch == noLatch:
			s.clearMods |= f.act.mask
			s.release(f)
		case f.act.flags&flagLatchOnPress == 0:
			latch = latchPending
			s.clearMods |= f.act.mask
			s.c.latchedMods |= f.act.mask
		}
	}
	f.latch = latch
	return false
}

func (s *State) groupLockFunc(f *filter, ki int32, dir KeyDirection) bool {
	if ki != f.key {
		if f.act.flags&flagLockOnRelease != 0 && dir == KeyPressed {
			f.act.flags &^= flagLockOnRelease
		}
		return false
	}
	if dir == KeyPressed {
		f.refcnt++
		return true
	}
	f.refcnt--
	if f.refcnt > 0 {
		return true
	}
	if f.act.flags&flagLockOnRelease != 0 {
		applyGroupDelta(f, &s.c.lockedGroup)
	}
	s.release(f)
	return false
}

func (s *State) groupLatchFunc(f *filter, ki int32, k *flatKey, dir KeyDirection) bool {
	latch := f.latch
	switch {
	case dir == KeyPressed && latch == latchKeyDown:
		if !s.latchSurvivesPress(k, breaksGroupLatch, 0) {
			latch = noLatch
		}
	case dir == KeyPressed && latch == latchPending:
		actions := s.keyActions(k)
		for i := range actions {
			a := &actions[i]
			if a.typ == actGroupLatch && a.group == f.act.group && a.flags == f.act.flags {
				f.act = *a
				if a.flags&flagLatchToLock != 0 && a.group != 0 {
					f.act.typ, f.kind = actGroupLock, actGroupLock
					s.filterNew(f)
					s.c.latchedGroup -= f.delta
					f.key = ki
					return true
				}
				continue
			}
			if breaksLatch(a, breaksGroupLatch, 0) {
				s.c.latchedGroup -= f.delta
				s.release(f)
				return false
			}
		}
	case dir == KeyReleased && ki == f.key:
		switch {
		case f.act.flags&flagLockClear != 0 && s.c.lockedGroup != 0:
			if latch == latchPending {
				s.c.latchedGroup -= f.delta
			} else {
				s.c.baseGroup -= f.delta
			}
			s.c.lockedGroup = 0
			s.release(f)
		case latch == noLatch:
			s.c.baseGroup -= f.delta
			s.release(f)
		case latch == latchKeyDown:
			latch = latchPending
			s.c.baseGroup -= f.delta
			s.c.latchedGroup += f.delta
		}
	}
	f.latch = latch
	return false
}

// UpdateLatchedLocked changes latches and locks directly, the way a virtual
// keyboard or an accessibility tool does, and reports what changed. Only
// the modifiers in an affect mask change, and a layout changes only when its
// affect flag is set.
func (s *State) UpdateLatchedLocked(affectLatchedMods, latchedMods ModMask, affectLatchedLayout bool, latchedLayout int32, affectLockedMods, lockedMods ModMask, affectLockedLayout bool, lockedLayout int32) StateComponent {
	prev := s.c
	if affect := s.keymap.resolveCanonical(uint32(affectLockedMods)); affect != 0 {
		locked := s.keymap.resolveCanonical(uint32(lockedMods))
		s.c.lockedMods = s.c.lockedMods&^affect | locked&affect
	}
	if affectLockedLayout {
		s.c.lockedGroup = lockedLayout
	}
	if affect := s.keymap.resolveCanonical(uint32(affectLatchedMods)); affect != 0 {
		s.updateLatchModifiers(affect, s.keymap.resolveCanonical(uint32(latchedMods)))
	}
	if affectLatchedLayout {
		s.updateLatchGroup(latchedLayout)
	}
	s.updateDerived()
	return componentChanges(&prev, &s.c)
}

func (s *State) updateLatchModifiers(mask, latches uint32) {
	clear := mask &^ latches
	s.c.latchedMods &^= clear
	s.synth[0] = action{typ: actInternal, flags: breaksModLatch, mask: clear}
	s.applyFilters(syntheticBreakKey, nil, KeyPressed)
	f := s.newFilter()
	f.key, f.kind = syntheticLatchKey, actModLatch
	f.act = action{typ: actModLatch, mask: mask & latches}
	s.filterNew(f)
	s.modLatchFunc(f, syntheticLatchKey, nil, KeyReleased)
}

func (s *State) updateLatchGroup(group int32) {
	s.synth[0] = action{typ: actInternal, flags: breaksGroupLatch}
	s.applyFilters(syntheticBreakKey, nil, KeyPressed)
	f := s.newFilter()
	f.key, f.kind = syntheticLatchKey, actGroupLatch
	f.act = action{typ: actGroupLatch, flags: flagAbsoluteSwitch, group: group}
	s.filterNew(f)
	s.groupLatchFunc(f, syntheticLatchKey, nil, KeyReleased)
}
