package xkb

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math/bits"
	"strings"
)

// X11Conn carries X11 requests that have one reply. It keeps this package off
// any X client library: package xkb/x11 implements it on a socket, and an
// application with its own connection, such as xgb, can adapt that one.
type X11Conn interface {
	// RoundTrip sends one little-endian X11 request and returns its reply,
	// all of it, from the leading 1 on, or the X error as an error.
	RoundTrip(request []byte) ([]byte, error)
}

// X11 is a connection with the XKB extension in use.
type X11 struct {
	conn   X11Conn
	opcode byte
	// ServerMajor and ServerMinor are the XKB version the server speaks.
	ServerMajor, ServerMinor uint16
	// BaseEvent and BaseError are where the extension's events and errors
	// start.
	BaseEvent, BaseError byte
}

const (
	x11QueryExtension = 98
	x11GetAtomName    = 17

	xkbUseExtension    = 0
	xkbGetState        = 4
	xkbGetControls     = 6
	xkbGetMap          = 8
	xkbGetCompatMap    = 10
	xkbGetIndicatorMap = 13
	xkbGetNames        = 17
	xkbGetDeviceInfo   = 24

	xkbUseCoreKbd = 0x100

	x11MajorVersion = 1
	x11MinorVersion = 0

	mapPartKeyTypes     = 0x01
	mapPartKeySyms      = 0x02
	mapPartModifierMap  = 0x04
	mapPartExplicit     = 0x08
	mapPartKeyActions   = 0x10
	mapPartKeyBehaviors = 0x20
	mapPartVirtualMods  = 0x40
	mapPartVModMap      = 0x80
	mapRequired         = mapPartKeyTypes | mapPartKeySyms | mapPartModifierMap | mapPartExplicit | mapPartKeyActions | mapPartVirtualMods | mapPartVModMap

	nameKeycodes        = 0x0001
	nameGeometry        = 0x0002
	nameSymbols         = 0x0004
	namePhysSymbols     = 0x0008
	nameTypes           = 0x0010
	nameCompat          = 0x0020
	nameKeyTypeNames    = 0x0040
	nameKTLevelNames    = 0x0080
	nameIndicatorNames  = 0x0100
	nameKeyNames        = 0x0200
	nameKeyAliases      = 0x0400
	nameVirtualModNames = 0x0800
	nameGroupNames      = 0x1000
	namesWanted         = nameKeycodes | nameSymbols | nameTypes | nameCompat | nameKeyTypeNames | nameKTLevelNames | nameIndicatorNames | nameKeyNames | nameKeyAliases | nameVirtualModNames | nameGroupNames
	namesRequired       = nameKeyTypeNames | nameKTLevelNames | nameKeyNames | nameVirtualModNames

	numX11VirtualMods = 16
	x11NoModifier     = 0xff
)

// X11MinVersion is the XKB version SetupX11 asks for.
const (
	X11MinMajorVersion = x11MajorVersion
	X11MinMinorVersion = x11MinorVersion
)

// SetupX11 starts using the XKB extension on a connection, asking for a
// version of it, X11MinMajorVersion.X11MinMinorVersion or later.
func SetupX11(conn X11Conn, major, minor uint16) (*X11, error) {
	const name = "XKEYBOARD"
	req := make([]byte, 8, 8+(len(name)+3)/4*4)
	req[0] = x11QueryExtension
	binary.LittleEndian.PutUint16(req[2:], uint16(2+(len(name)+3)/4))
	binary.LittleEndian.PutUint16(req[4:], uint16(len(name)))
	req = append(req, name...)
	for len(req)%4 != 0 {
		req = append(req, 0)
	}
	reply, err := conn.RoundTrip(req)
	if err != nil {
		return nil, fmt.Errorf("xkb: query the XKB extension: %w", err)
	}
	if len(reply) < 12 || reply[8] == 0 {
		return nil, errors.New("xkb: the X server has no XKB extension")
	}
	x := &X11{conn: conn, opcode: reply[9], BaseEvent: reply[10], BaseError: reply[11]}
	use := x.request(xkbUseExtension, 4)
	binary.LittleEndian.PutUint16(use[4:], major)
	binary.LittleEndian.PutUint16(use[6:], minor)
	if reply, err = conn.RoundTrip(use); err != nil {
		return nil, fmt.Errorf("xkb: use the XKB extension: %w", err)
	}
	if len(reply) < 12 || reply[1] == 0 {
		return nil, fmt.Errorf("xkb: the X server does not support XKB %d.%d", major, minor)
	}
	x.ServerMajor = binary.LittleEndian.Uint16(reply[8:])
	x.ServerMinor = binary.LittleEndian.Uint16(reply[10:])
	return x, nil
}

func (x *X11) request(minor byte, body int) []byte {
	req := make([]byte, 4+body)
	req[0], req[1] = x.opcode, minor
	binary.LittleEndian.PutUint16(req[2:], uint16((4+body)/4)) //nolint:gosec // fixed request sizes
	return req
}

// CoreKeyboardDeviceID is the device ID of the X server's core keyboard.
func (x *X11) CoreKeyboardDeviceID() (int32, error) {
	req := x.request(xkbGetDeviceInfo, 12)
	binary.LittleEndian.PutUint16(req[4:], xkbUseCoreKbd)
	reply, err := x.conn.RoundTrip(req)
	if err != nil {
		return -1, fmt.Errorf("xkb: get the core keyboard: %w", err)
	}
	if len(reply) < 2 {
		return -1, errors.New("xkb: get the core keyboard: a short reply")
	}
	return int32(reply[1]), nil
}

// NewStateFromDevice opens a state over a keymap, holding what the device's
// state is on the server now.
func (x *X11) NewStateFromDevice(m *Keymap, deviceID int32) (*State, error) {
	if deviceID < 0 || deviceID > 255 {
		return nil, fmt.Errorf("xkb: illegal device ID %d", deviceID)
	}
	req := x.request(xkbGetState, 4)
	binary.LittleEndian.PutUint16(req[4:], uint16(deviceID))
	reply, err := x.conn.RoundTrip(req)
	if err != nil {
		return nil, fmt.Errorf("xkb: get the state of device %d: %w", deviceID, err)
	}
	if len(reply) < 18 {
		return nil, fmt.Errorf("xkb: get the state of device %d: a short reply", deviceID)
	}
	s := m.NewState()
	baseGroup := int32(int16(binary.LittleEndian.Uint16(reply[14:])))                                                                   //nolint:gosec // a signed wire field
	latchedGroup := int32(int16(binary.LittleEndian.Uint16(reply[16:])))                                                                //nolint:gosec // a signed wire field
	s.UpdateMask(ModMask(reply[9]), ModMask(reply[10]), ModMask(reply[11]), uint32(baseGroup), uint32(latchedGroup), uint32(reply[13])) //nolint:gosec // two's complement offsets
	return s, nil
}

// NewKeymapFromDevice reads a device's keymap from the X server, the way
// xkb_x11_keymap_new_from_device does. The keymap is FormatTextV1.
func (x *X11) NewKeymapFromDevice(c *Context, deviceID int32) (*Keymap, error) {
	if deviceID < 0 || deviceID > 127 {
		c.log(LogError, 0, 0, "illegal device ID: %d\n", deviceID)
		return nil, fmt.Errorf("xkb: illegal device ID %d", deviceID)
	}
	m := newKeymap(c, FormatTextV1)
	r := x11Reader{x: x, m: m, device: uint16(deviceID), atoms: map[uint32]string{}}
	for _, step := range []struct {
		name string
		fn   func() error
	}{{"XkbGetMap", r.getMap}, {"XkbGetIndicatorMap", r.getIndicators}, {"XkbGetCompatMap", r.getCompat}, {"XkbGetNames", r.getNames}, {"XkbGetControls", r.getControls}} {
		if err := step.fn(); err != nil {
			c.log(LogError, 0, 0, "x11: failed to get keymap from X server: %s request failed: %v\n", step.name, err)
			return nil, fmt.Errorf("xkb: %s of device %d: %w", step.name, deviceID, err)
		}
	}
	for i := range m.types {
		m.types[i].required = true
	}
	m.canonicalMask = realModMask
	m.buildFlat()
	return m, nil
}

type x11Reader struct {
	x        *X11
	m        *Keymap
	device   uint16
	atoms    map[uint32]string
	widths   []wireKeySyms
	firstSym int
}

func (r *x11Reader) atom(atom uint32) (string, error) {
	if atom == 0 {
		return "", nil
	}
	if name, ok := r.atoms[atom]; ok {
		return name, nil
	}
	req := make([]byte, 8)
	req[0] = x11GetAtomName
	binary.LittleEndian.PutUint16(req[2:], 2)
	binary.LittleEndian.PutUint32(req[4:], atom)
	reply, err := r.x.conn.RoundTrip(req)
	if err != nil {
		return "", fmt.Errorf("the name of atom %d: %w", atom, err)
	}
	if len(reply) < 32 {
		return "", fmt.Errorf("the name of atom %d: a short reply", atom)
	}
	n := int(binary.LittleEndian.Uint16(reply[8:]))
	if len(reply) < 32+n {
		return "", fmt.Errorf("the name of atom %d: a short reply", atom)
	}
	name := string(reply[32 : 32+n])
	r.atoms[atom] = name
	return name, nil
}

type wireReader struct {
	b   []byte
	pos int
	err error
}

func (w *wireReader) need(n int) []byte {
	if w.err != nil {
		return make([]byte, n)
	}
	if n < 0 || w.pos+n > len(w.b) {
		w.err = fmt.Errorf("a reply ends %d bytes short at offset %d", w.pos+n-len(w.b), w.pos)
		return make([]byte, max(n, 0))
	}
	out := w.b[w.pos : w.pos+n]
	w.pos += n
	return out
}

func (w *wireReader) u8() byte { return w.need(1)[0] }

func (w *wireReader) u32() uint32 { return binary.LittleEndian.Uint32(w.need(4)) }

func (w *wireReader) align() {
	if rem := w.pos % 4; rem != 0 {
		w.need(4 - rem)
	}
}

func translateMods(real byte, low, high uint16) uint32 {
	return uint32(real) | uint32(low)<<8 | uint32(high)<<16
}

const wireIgnoreGroupLock = 0x1000

func translateControls(wire uint32) uint32 {
	out := wire & 0x3ff
	if wire&wireIgnoreGroupLock != 0 {
		out |= 1 << 10
	}
	return out
}

func translateAction(w []byte) action {
	var a action
	t := w[0]
	flags := w[1]
	u16be := func(hi, lo byte) uint16 { return uint16(hi)<<8 | uint16(lo) }
	switch t {
	case 1, 2, 3:
		a.typ = map[byte]actionType{1: actModSet, 2: actModLatch, 3: actModLock}[t]
		a.mods = translateMods(w[3], uint16(w[5]), uint16(w[4]))
		a.mask = uint32(w[2])
		if t == 3 {
			if flags&1 != 0 {
				a.flags |= flagLockNoLock
			}
			if flags&2 != 0 {
				a.flags |= flagLockNoUnlock
			}
		} else {
			if flags&1 != 0 {
				a.flags |= flagLockClear
			}
			if flags&2 != 0 {
				a.flags |= flagLatchToLock
			}
		}
		if flags&4 != 0 {
			a.flags |= flagLookupModmap
		}
	case 4, 5, 6:
		a.typ = map[byte]actionType{4: actGroupSet, 5: actGroupLatch, 6: actGroupLock}[t]
		a.group = int32(int8(w[2])) //nolint:gosec // a signed wire field
		if t != 6 {
			if flags&1 != 0 {
				a.flags |= flagLockClear
			}
			if flags&2 != 0 {
				a.flags |= flagLatchToLock
			}
		}
		if flags&4 != 0 {
			a.flags |= flagAbsoluteSwitch
		}
	case 7:
		a.typ = actPtrMove
		a.x = int16(u16be(w[2], w[3])) //nolint:gosec // a signed wire field
		a.y = int16(u16be(w[4], w[5])) //nolint:gosec // a signed wire field
		if flags&1 == 0 {
			a.flags |= flagAccel
		}
		if flags&2 != 0 {
			a.flags |= flagAbsoluteX
		}
		if flags&4 != 0 {
			a.flags |= flagAbsoluteY
		}
	case 8:
		a.typ = actPtrButton
		a.count, a.button = w[2], w[3]
	case 9:
		a.typ = actPtrLock
		a.button = w[3]
		if flags&1 != 0 {
			a.flags |= flagLockNoLock
		}
		if flags&2 != 0 {
			a.flags |= flagLockNoUnlock
		}
	case 10:
		a.typ = actPtrDefault
		a.value = int8(w[3]) //nolint:gosec // a signed wire field
		if flags&4 != 0 {
			a.flags |= flagAbsoluteSwitch
		}
	case 12:
		a.typ = actTerminate
	case 13:
		a.typ = actSwitchVT
		a.screen = int8(w[2]) //nolint:gosec // a signed wire field
		if flags&1 == 0 {
			a.flags |= flagSameScreen
		}
		if flags&4 != 0 {
			a.flags |= flagAbsoluteSwitch
		}
	case 14, 15:
		a.typ = actCtrlSet
		if t == 15 {
			a.typ = actCtrlLock
		}
		a.ctrls = translateControls(uint32(u16be(w[4], w[5])))
	case 0, 11, 16, 17, 18, 19, 20:
		a.typ = actNone
	default:
		if t < byte(actPrivate) {
			a.typ = actNone
			break
		}
		a.typ = actionType(t)
		copy(a.privData[:], w[1:8])
	}
	return a
}

func (r *x11Reader) getMap() error {
	req := r.x.request(xkbGetMap, 24)
	binary.LittleEndian.PutUint16(req[4:], r.device)
	binary.LittleEndian.PutUint16(req[6:], mapRequired)
	reply, err := r.x.conn.RoundTrip(req)
	if err != nil {
		return err
	}
	if len(reply) < 40 {
		return errors.New("a short reply")
	}
	present := binary.LittleEndian.Uint16(reply[12:])
	if present&mapRequired != mapRequired {
		return fmt.Errorf("the reply holds only parts %#x", present)
	}
	if reply[14] != 0 {
		return errors.New("the types do not start at 0")
	}
	m := r.m
	w := &wireReader{b: reply, pos: 40}
	minKey, maxKey := reply[10], reply[11]
	if minKey > maxKey {
		return fmt.Errorf("key codes run from %d to %d", minKey, maxKey)
	}
	m.minKey, m.maxKey = Keycode(minKey), Keycode(maxKey)
	m.numLow = int(maxKey) - int(minKey) + 1
	m.keys = make([]key, m.numLow)
	for i := range m.keys {
		m.keys[i].keycode = Keycode(int(minKey) + i) //nolint:gosec // one wire byte
	}
	if err := r.readTypes(w, int(reply[15])); err != nil {
		return err
	}
	firstSym, nSyms := reply[17], int(reply[20])
	if firstSym < minKey || int(firstSym)+nSyms > int(maxKey)+1 {
		return fmt.Errorf("keysyms of keys %d+%d are outside %d..%d", firstSym, nSyms, minKey, maxKey)
	}
	if err := r.readSyms(w, firstSym, nSyms); err != nil {
		return err
	}
	if reply[21] != minKey || int(reply[21])+int(reply[24]) != int(maxKey)+1 {
		return errors.New("the actions do not cover every key")
	}
	r.readActions(w, reply[21], int(reply[24]))
	if present&mapPartKeyBehaviors != 0 {
		w.need(4 * int(reply[27]))
	}
	r.readVmods(w, binary.LittleEndian.Uint16(reply[38:]))
	r.readExplicit(w, int(reply[30]))
	r.readModmaps(w, int(reply[33]))
	r.readVmodmaps(w, int(reply[36]))
	return w.err
}

func (r *x11Reader) readTypes(w *wireReader, n int) error {
	m := r.m
	m.types = make([]*keyType, n)
	for i := range n {
		h := w.need(8)
		t := &keyType{mods: translateMods(h[1], binary.LittleEndian.Uint16(h[2:]), 0), mask: uint32(h[0]), numLevels: uint32(h[4]), required: true}
		if t.numLevels == 0 {
			return fmt.Errorf("type %d has no levels", i)
		}
		entries, hasPreserve := int(h[5]), h[6] != 0
		t.entries = make([]typeEntry, entries)
		for j := range entries {
			e := w.need(8)
			if uint32(e[2]) >= t.numLevels {
				return fmt.Errorf("type %d maps to level %d of %d", i, e[2], t.numLevels)
			}
			t.entries[j] = typeEntry{level: uint32(e[2]), mods: translateMods(e[3], binary.LittleEndian.Uint16(e[4:]), 0), mask: uint32(e[1])}
		}
		if hasPreserve {
			for j := range entries {
				p := w.need(4)
				t.entries[j].preserve = translateMods(p[1], binary.LittleEndian.Uint16(p[2:]), 0)
				t.entries[j].preserveMask = uint32(p[0])
			}
		}
		m.types[i] = t
	}
	m.allTypes = m.types
	return w.err
}

func (r *x11Reader) keyAt(kc int) *key { return &r.m.keys[kc-int(r.m.minKey)] }

type wireKeySyms struct {
	width int
}

func (r *x11Reader) readSyms(w *wireReader, first byte, n int) error {
	m := r.m
	widths := make([]wireKeySyms, n)
	for i := range n {
		h := w.need(8)
		k := r.keyAt(int(first) + i)
		info := h[4]
		groups := int(info & 0x0f)
		if groups > 4 {
			return fmt.Errorf("key %d has %d groups", k.keycode, groups)
		}
		width := int(h[5])
		nSyms := int(binary.LittleEndian.Uint16(h[6:]))
		if nSyms != width*groups {
			return fmt.Errorf("key %d has %d keysyms for %d groups of %d", k.keycode, nSyms, groups, width)
		}
		widths[i].width = width
		k.numGroups = uint32(groups)
		k.groups = make([]keyGroup, groups)
		for g := range groups {
			if int(h[g]) >= len(m.types) {
				return fmt.Errorf("key %d group %d has type %d of %d", k.keycode, g, h[g], len(m.types))
			}
			k.groups[g].typ = m.types[h[g]]
			k.groups[g].levels = make([]level, k.groups[g].typ.numLevels)
		}
		k.rangeNum = uint32(info&0x30) >> 4
		if k.rangeNum > k.numGroups {
			return fmt.Errorf("key %d redirects to group %d of %d", k.keycode, k.rangeNum, groups)
		}
		switch {
		case info&0x40 != 0:
			k.rangeAct = rangeSaturate
		case info&0x80 != 0:
			k.rangeAct = rangeRedirect
		default:
			k.rangeAct = rangeWrap
		}
		if nSyms > 0 {
			k.explicit |= explicitSymbols
		}
		for g := range groups {
			for lvl := range width {
				sym := Keysym(w.u32())
				if lvl < len(k.groups[g].levels) {
					l := &k.groups[g].levels[lvl]
					l.syms = []Keysym{sym}
					l.upper = l.syms
					if upper := KeysymToUpper(sym); upper != sym {
						l.upper = []Keysym{upper}
					}
				}
			}
		}
	}
	r.widths = widths
	r.firstSym = int(first)
	return w.err
}

func (r *x11Reader) readActions(w *wireReader, first byte, n int) {
	counts := w.need(n)
	w.align()
	for i := range n {
		if int(counts[i]) == 0 {
			continue
		}
		k := r.keyAt(int(first) + i)
		symIndex := int(first) + i - r.firstSym
		if symIndex < 0 || symIndex >= len(r.widths) {
			w.err = fmt.Errorf("key %d has actions but no keysyms", k.keycode)
			return
		}
		width := r.widths[symIndex].width
		if int(counts[i]) != width*int(k.numGroups) {
			w.err = fmt.Errorf("key %d has %d actions for %d keysyms", k.keycode, counts[i], width*int(k.numGroups))
			return
		}
		for g := range k.groups {
			for lvl := range width {
				a := translateAction(w.need(8))
				if lvl >= len(k.groups[g].levels) {
					continue
				}
				l := &k.groups[g].levels[lvl]
				l.actions = []action{a}
				if a.typ == actNone && len(l.syms) == 1 && l.syms[0] == KeyNoSymbol {
					l.syms, l.upper, l.actions = nil, nil, nil
				}
			}
		}
	}
}

func (r *x11Reader) readVmods(w *wireReader, mask uint16) {
	m := r.m
	m.mods.num = 8 + uint32(min(bits.Len16(mask), numX11VirtualMods)) //nolint:gosec // at most 16 bits of mask
	for i := range uint32(numX11VirtualMods) {
		if mask&(1<<i) != 0 {
			mod := &m.mods.mods[8+i]
			mod.typ = modVirtual
			mod.mapping = uint32(w.u8())
		}
	}
	w.align()
}

func (r *x11Reader) readExplicit(w *wireReader, n int) {
	for range n {
		e := w.need(2)
		if !r.keyInRange(w, e[0]) {
			return
		}
		k := r.keyAt(int(e[0]))
		for g := range min(int(k.numGroups), 4) {
			if e[1]&(1<<g) != 0 {
				k.groups[g].explicitType = true
				k.explicit |= explicitTypes
			}
		}
		if e[1]&0x10 != 0 {
			k.explicit |= explicitInterp
			for g := range k.groups {
				k.groups[g].explicitActions = true
			}
		}
		if e[1]&0x20 != 0 {
			k.explicit |= explicitRepeat
		}
		if e[1]&0x80 != 0 {
			k.explicit |= explicitVmodmap
		}
	}
	w.align()
}

func (r *x11Reader) keyInRange(w *wireReader, kc byte) bool {
	if Keycode(kc) >= r.m.minKey && Keycode(kc) <= r.m.maxKey {
		return true
	}
	if w.err == nil {
		w.err = fmt.Errorf("key %d is outside %d..%d", kc, r.m.minKey, r.m.maxKey)
	}
	return false
}

func (r *x11Reader) readModmaps(w *wireReader, n int) {
	for range n {
		e := w.need(2)
		if !r.keyInRange(w, e[0]) {
			return
		}
		r.keyAt(int(e[0])).modmap = uint32(e[1])
	}
	w.align()
}

func (r *x11Reader) readVmodmaps(w *wireReader, n int) {
	for range n {
		e := w.need(4)
		if !r.keyInRange(w, e[0]) {
			return
		}
		r.keyAt(int(e[0])).vmodmap = translateMods(0, binary.LittleEndian.Uint16(e[2:]), 0)
	}
}

func translateWhich(wire byte, base, latched, locked, effective StateComponent) StateComponent {
	var out StateComponent
	if wire&1 != 0 {
		out |= base
	}
	if wire&2 != 0 {
		out |= latched
	}
	if wire&4 != 0 {
		out |= locked
	}
	if wire&(8|16) != 0 {
		out |= effective
	}
	return out
}

func (r *x11Reader) getIndicators() error {
	req := r.x.request(xkbGetIndicatorMap, 8)
	binary.LittleEndian.PutUint16(req[4:], r.device)
	binary.LittleEndian.PutUint32(req[8:], 0xffffffff)
	reply, err := r.x.conn.RoundTrip(req)
	if err != nil {
		return err
	}
	if len(reply) < 32 {
		return errors.New("a short reply")
	}
	which := binary.LittleEndian.Uint32(reply[8:])
	m := r.m
	m.leds = make([]led, bits.Len32(which))
	w := &wireReader{b: reply, pos: 32}
	for i := range m.leds {
		if which&(1<<i) == 0 {
			continue
		}
		e := w.need(12)
		l := &m.leds[i]
		l.whichGroups = translateWhich(e[1], StateLayoutDepressed, StateLayoutLatched, StateLayoutLocked, StateLayoutEffective)
		l.groups = uint32(e[2])
		l.whichMods = translateWhich(e[3], StateModDepressed, StateModLatched, StateModLocked, StateModEffective)
		l.mods = translateMods(e[5], binary.LittleEndian.Uint16(e[6:]), 0)
		l.mask = uint32(e[4])
		l.ctrls = translateControls(binary.LittleEndian.Uint32(e[8:]))
	}
	return w.err
}

func (r *x11Reader) getCompat() error {
	req := r.x.request(xkbGetCompatMap, 8)
	binary.LittleEndian.PutUint16(req[4:], r.device)
	req[7] = 1
	reply, err := r.x.conn.RoundTrip(req)
	if err != nil {
		return err
	}
	if len(reply) < 32 {
		return errors.New("a short reply")
	}
	first, n, total := binary.LittleEndian.Uint16(reply[10:]), int(binary.LittleEndian.Uint16(reply[12:])), int(binary.LittleEndian.Uint16(reply[14:]))
	if first != 0 || n != total {
		return fmt.Errorf("the reply holds interpretations %d+%d of %d", first, n, total)
	}
	m := r.m
	w := &wireReader{b: reply, pos: 32}
	m.interprets = make([]*interpret, n)
	for i := range n {
		e := w.need(16)
		si := &interpret{sym: Keysym(binary.LittleEndian.Uint32(e)), mods: uint32(e[4]), repeat: e[7]&1 != 0, required: true, levelOneOnly: e[5]&0x80 != 0}
		switch e[5] & 0x7f {
		case 0:
			si.match = matchNone
		case 1:
			si.match = matchAnyOrNone
		case 2:
			si.match = matchAny
		case 3:
			si.match = matchAll
		case 4:
			si.match = matchExactly
		default:
			return fmt.Errorf("unrecognized interpret match value %#x", e[5]&0x7f)
		}
		si.virtualMod = modIndexInvalid
		if e[6] != x11NoModifier {
			si.virtualMod = 8 + uint32(e[6])
		}
		if a := translateAction(e[8:]); a.typ != actNone {
			si.actions = []action{a}
		}
		m.interprets[i] = si
	}
	m.allInterprets = m.interprets
	return w.err
}

func wireKeyName(b []byte) string {
	if i := strings.IndexByte(string(b), 0); i >= 0 {
		b = b[:i]
	}
	return string(b)
}

func (r *x11Reader) getNames() error {
	req := r.x.request(xkbGetNames, 8)
	binary.LittleEndian.PutUint16(req[4:], r.device)
	binary.LittleEndian.PutUint32(req[8:], namesWanted)
	reply, err := r.x.conn.RoundTrip(req)
	if err != nil {
		return err
	}
	if len(reply) < 32 {
		return errors.New("a short reply")
	}
	which := binary.LittleEndian.Uint32(reply[8:])
	if which&namesRequired != namesRequired {
		return fmt.Errorf("the reply holds only names %#x", which)
	}
	m := r.m
	w := &wireReader{b: reply, pos: 32}
	if int(reply[14]) != len(m.types) {
		return fmt.Errorf("the reply names %d types of %d", reply[14], len(m.types))
	}
	if err := r.readSectionNames(w, which); err != nil {
		return err
	}
	if err := r.readTypeNames(w, int(reply[14])); err != nil {
		return err
	}
	if err := r.readIndexedNames(w, reply, which); err != nil {
		return err
	}
	return r.readKeyNames(w, reply, which)
}

func (r *x11Reader) readSectionNames(w *wireReader, which uint32) error {
	m := r.m
	for _, f := range []struct {
		bit uint32
		typ fileType
	}{{nameKeycodes, fileKeycodes}, {nameGeometry, 0}, {nameSymbols, fileSymbols}, {namePhysSymbols, 0}, {nameTypes, fileTypes}, {nameCompat, fileCompat}} {
		if which&f.bit == 0 {
			continue
		}
		atom := w.u32()
		if f.bit == nameGeometry || f.bit == namePhysSymbols {
			continue
		}
		name, err := r.atom(atom)
		if err != nil {
			return err
		}
		m.sections[f.typ] = escapeMapName(name)
	}
	return w.err
}

func (r *x11Reader) readTypeNames(w *wireReader, n int) error {
	m := r.m
	atoms := make([]uint32, n)
	for i := range atoms {
		atoms[i] = w.u32()
	}
	levels := w.need(n)
	w.align()
	for i, t := range m.types {
		if t.numLevels < uint32(levels[i]) {
			return fmt.Errorf("type %d names %d levels of %d", i, levels[i], t.numLevels)
		}
		name, err := r.atom(atoms[i])
		if err != nil {
			return err
		}
		t.name = name
		t.levelNames = make([]string, t.numLevels)
		for j := range int(levels[i]) {
			if t.levelNames[j], err = r.atom(w.u32()); err != nil {
				return err
			}
		}
	}
	return w.err
}

func (r *x11Reader) readIndexedNames(w *wireReader, reply []byte, which uint32) error {
	m := r.m
	indicators := binary.LittleEndian.Uint32(reply[20:])
	if bits.Len32(indicators) > len(m.leds) {
		return fmt.Errorf("the reply names indicators %#x of %d", indicators, len(m.leds))
	}
	var err error
	if which&nameIndicatorNames != 0 {
		for i := range uint32(maxLEDs) {
			if indicators&(1<<i) != 0 {
				if m.leds[i].name, err = r.atom(w.u32()); err != nil {
					return err
				}
			}
		}
	}
	vmods := binary.LittleEndian.Uint16(reply[16:])
	m.mods.num = 8 + uint32(min(bits.Len16(vmods), numX11VirtualMods)) //nolint:gosec // one wire byte
	for i := range uint32(numX11VirtualMods) {
		if vmods&(1<<i) != 0 {
			if m.mods.mods[8+i].name, err = r.atom(w.u32()); err != nil {
				return err
			}
		}
	}
	groups := reply[15]
	m.groupNames = make([]string, bits.Len8(groups))
	if which&nameGroupNames != 0 {
		count := bits.OnesCount8(groups)
		for i := range count {
			if m.groupNames[i], err = r.atom(w.u32()); err != nil {
				return err
			}
		}
	}
	return w.err
}

func (r *x11Reader) readKeyNames(w *wireReader, reply []byte, which uint32) error {
	m := r.m
	if Keycode(reply[12]) != m.minKey || Keycode(reply[13]) != m.maxKey || Keycode(reply[18]) != m.minKey || int(reply[18])+int(reply[19])-1 != int(m.maxKey) {
		return errors.New("the key names do not cover every key")
	}
	m.names = make(map[string]keyMatch, len(m.keys))
	for i := range int(reply[19]) {
		name := wireKeyName(w.need(4))
		m.keys[i].name = name
		if name != "" {
			m.names[name] = keyMatch{index: i}
		}
	}
	if which&nameKeyAliases != 0 {
		for range int(reply[25]) {
			e := w.need(8)
			realName, alias := wireKeyName(e[:4]), wireKeyName(e[4:])
			m.names[alias] = keyMatch{alias: true, real: realName}
			m.aliases = append(m.aliases, keyAlias{alias: alias, real: realName})
		}
	}
	return w.err
}

func (r *x11Reader) getControls() error {
	req := r.x.request(xkbGetControls, 4)
	binary.LittleEndian.PutUint16(req[4:], r.device)
	reply, err := r.x.conn.RoundTrip(req)
	if err != nil {
		return err
	}
	if len(reply) < 92 {
		return errors.New("a short reply")
	}
	m := r.m
	if reply[9] == 0 || reply[9] > 4 {
		return fmt.Errorf("the keyboard has %d groups", reply[9])
	}
	m.enabledCtrls = translateControls(binary.LittleEndian.Uint32(reply[56:]))
	m.numGroups = uint32(reply[9])
	repeat := reply[60:92]
	for i := range m.keys {
		kc := m.keys[i].keycode
		m.keys[i].repeats = repeat[kc/8]&(1<<(kc%8)) != 0
	}
	return nil
}
