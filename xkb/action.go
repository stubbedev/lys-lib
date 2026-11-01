package xkb

import (
	"errors"
	"fmt"
	"math"
	"strings"
)

var actionTypeNames = []lookupEntry{
	{"NoAction", uint32(actNone)},
	{"VoidAction", uint32(actVoid)},
	{"SetMods", uint32(actModSet)},
	{"LatchMods", uint32(actModLatch)},
	{"LockMods", uint32(actModLock)},
	{"SetGroup", uint32(actGroupSet)},
	{"LatchGroup", uint32(actGroupLatch)},
	{"LockGroup", uint32(actGroupLock)},
	{"MovePtr", uint32(actPtrMove)},
	{"MovePointer", uint32(actPtrMove)},
	{"PtrBtn", uint32(actPtrButton)},
	{"PointerButton", uint32(actPtrButton)},
	{"LockPtrBtn", uint32(actPtrLock)},
	{"LockPtrButton", uint32(actPtrLock)},
	{"LockPointerButton", uint32(actPtrLock)},
	{"LockPointerBtn", uint32(actPtrLock)},
	{"SetPtrDflt", uint32(actPtrDefault)},
	{"SetPointerDefault", uint32(actPtrDefault)},
	{"Terminate", uint32(actTerminate)},
	{"TerminateServer", uint32(actTerminate)},
	{"SwitchScreen", uint32(actSwitchVT)},
	{"SetControls", uint32(actCtrlSet)},
	{"LockControls", uint32(actCtrlLock)},
	{"Private", uint32(actPrivate)},
	{"RedirectKey", uint32(actLegacy)},
	{"Redirect", uint32(actLegacy)},
	{"ISOLock", uint32(actLegacy)},
	{"ActionMessage", uint32(actLegacy)},
	{"MessageAction", uint32(actLegacy)},
	{"Message", uint32(actLegacy)},
	{"DeviceBtn", uint32(actLegacy)},
	{"DevBtn", uint32(actLegacy)},
	{"DevButton", uint32(actLegacy)},
	{"DeviceButton", uint32(actLegacy)},
	{"LockDeviceBtn", uint32(actLegacy)},
	{"LockDevBtn", uint32(actLegacy)},
	{"LockDevButton", uint32(actLegacy)},
	{"LockDeviceButton", uint32(actLegacy)},
	{"DeviceValuator", uint32(actLegacy)},
	{"DevVal", uint32(actLegacy)},
	{"DeviceVal", uint32(actLegacy)},
	{"DevValuator", uint32(actLegacy)},
}

var ctrlMaskNames = []lookupEntry{
	{"RepeatKeys", 1 << 0},
	{"Repeat", 1 << 0},
	{"AutoRepeat", 1 << 0},
	{"SlowKeys", 1 << 1},
	{"BounceKeys", 1 << 2},
	{"StickyKeys", 1 << 3},
	{"MouseKeys", 1 << 4},
	{"MouseKeysAccel", 1 << 5},
	{"AccessXKeys", 1 << 6},
	{"AccessXTimeout", 1 << 7},
	{"AccessXFeedback", 1 << 8},
	{"AudibleBell", 1 << 9},
	{"IgnoreGroupLock", 1 << 10},
	{"all", controlAll},
	{"none", 0},
	{"Overlay1", 0},
	{"Overlay2", 0},
}

var modComponentMaskNames = []lookupEntry{
	{"base", uint32(StateModDepressed)},
	{"latched", uint32(StateModLatched)},
	{"locked", uint32(StateModLocked)},
	{"effective", uint32(StateModEffective)},
	{"compat", uint32(StateModEffective)},
	{"any", uint32(StateModEffective)},
	{"none", 0},
}

var groupComponentMaskNames = []lookupEntry{
	{"base", uint32(StateLayoutDepressed)},
	{"latched", uint32(StateLayoutLatched)},
	{"locked", uint32(StateLayoutLocked)},
	{"effective", uint32(StateLayoutEffective)},
	{"any", uint32(StateLayoutEffective)},
	{"none", 0},
}

var groupMaskNames = []lookupEntry{{"none", 0}, {"all", allGroupsMask}}

var buttonNames = []lookupEntry{
	{"Button1", 1}, {"Button2", 2}, {"Button3", 3}, {"Button4", 4}, {"Button5", 5}, {"default", 0},
}

var useModMapValueNames = []lookupEntry{{"LevelOne", 1}, {"Level1", 1}, {"AnyLevel", 0}, {"any", 0}}

var matchNames = []lookupEntry{
	{"NoneOf", uint32(matchNone)},
	{"AnyOfOrNone", uint32(matchAnyOrNone)},
	{"AnyOf", uint32(matchAny)},
	{"AllOf", uint32(matchAll)},
	{"Exactly", uint32(matchExactly)},
}

var lockWhich = []lookupEntry{
	{"both", 0},
	{"lock", uint32(flagLockNoUnlock)},
	{"neither", uint32(flagLockNoLock | flagLockNoUnlock)},
	{"unlock", uint32(flagLockNoLock)},
}

var ptrDefaults = []lookupEntry{{"dfltbtn", 1}, {"defaultbutton", 1}, {"button", 1}}

type actionField uint8

const (
	fieldClearLocks actionField = iota
	fieldLatchToLock
	fieldGenKeyEvent
	fieldReport
	fieldDefault
	fieldAffect
	fieldIncrement
	fieldModifiers
	fieldGroup
	fieldX
	fieldY
	fieldAccel
	fieldButton
	fieldValue
	fieldControls
	fieldType
	fieldCount
	fieldScreen
	fieldSame
	fieldData
	fieldDevice
	fieldKeycode
	fieldModsToClear
	fieldLockOnRelease
	fieldUnlockOnPress
	fieldLatchOnPress
)

var fieldNames = []lookupEntry{
	{"clearLocks", uint32(fieldClearLocks)},
	{"latchToLock", uint32(fieldLatchToLock)},
	{"genKeyEvent", uint32(fieldGenKeyEvent)},
	{"generateKeyEvent", uint32(fieldGenKeyEvent)},
	{"report", uint32(fieldReport)},
	{"default", uint32(fieldDefault)},
	{"affect", uint32(fieldAffect)},
	{"increment", uint32(fieldIncrement)},
	{"modifiers", uint32(fieldModifiers)},
	{"mods", uint32(fieldModifiers)},
	{"group", uint32(fieldGroup)},
	{"x", uint32(fieldX)},
	{"y", uint32(fieldY)},
	{"accel", uint32(fieldAccel)},
	{"accelerate", uint32(fieldAccel)},
	{"repeat", uint32(fieldAccel)},
	{"button", uint32(fieldButton)},
	{"value", uint32(fieldValue)},
	{"controls", uint32(fieldControls)},
	{"ctrls", uint32(fieldControls)},
	{"type", uint32(fieldType)},
	{"count", uint32(fieldCount)},
	{"screen", uint32(fieldScreen)},
	{"same", uint32(fieldSame)},
	{"sameServer", uint32(fieldSame)},
	{"data", uint32(fieldData)},
	{"device", uint32(fieldDevice)},
	{"dev", uint32(fieldDevice)},
	{"key", uint32(fieldKeycode)},
	{"keycode", uint32(fieldKeycode)},
	{"kc", uint32(fieldKeycode)},
	{"clearmods", uint32(fieldModsToClear)},
	{"clearmodifiers", uint32(fieldModsToClear)},
	{"lockOnRelease", uint32(fieldLockOnRelease)},
	{"unlockOnPress", uint32(fieldUnlockOnPress)},
	{"latchOnPress", uint32(fieldLatchOnPress)},
}

type actionDefaults [actPrivate + 1]action

func newActionDefaults() *actionDefaults {
	var d actionDefaults
	for t := range d {
		d[t].typ = actionType(t)
	}
	d[actPtrDefault].value = 1
	d[actPtrMove].flags = flagAccel
	d[actSwitchVT].flags = flagSameScreen
	return &d
}

func (d *actionDefaults) setField(mods *modSet, elem, field string, index, value expr, merge mergeMode) error {
	typ, ok := lookupName(actionTypeNames, elem)
	if !ok {
		return fmt.Errorf("unknown action %q", elem)
	}
	f, ok := lookupName(fieldNames, field)
	if !ok {
		return fmt.Errorf("%q is not a legal action field", field)
	}
	from := d[typ]
	if err := setActionField(actionType(typ), mods, &from, actionField(f), index, value); err != nil { //nolint:gosec // the table's own values
		return err
	}
	if merge != mergeAugment {
		d[typ] = from
	}
	return nil
}

func (d *actionDefaults) parse(mods *modSet, def expr) (action, error) {
	call, ok := def.(exprAction)
	if !ok {
		return action{}, fmt.Errorf("expected an action definition, found %T", def)
	}
	typ, ok := lookupName(actionTypeNames, call.name)
	if !ok {
		return action{}, fmt.Errorf("unknown action %q", call.name)
	}
	handler := actionType(typ) //nolint:gosec // the table's own values
	a := d[typ]
	if handler == actLegacy {
		a.typ = actNone
	}
	for _, arg := range call.args {
		fieldExpr, value := splitActionArg(arg)
		l, err := resolveLhs(fieldExpr)
		if err != nil {
			return action{}, fmt.Errorf("action %s: %w", call.name, err)
		}
		if l.elem != "" {
			return action{}, fmt.Errorf("action %s: cannot change defaults in an action definition", call.name)
		}
		f, ok := lookupName(fieldNames, l.field)
		if !ok {
			return action{}, fmt.Errorf("action %s: unknown field %q", call.name, l.field)
		}
		if err := setActionField(handler, mods, &a, actionField(f), l.index, value); err != nil { //nolint:gosec // the table's own values
			return action{}, fmt.Errorf("action %s: %w", call.name, err)
		}
	}
	return a, nil
}

func splitActionArg(arg expr) (expr, expr) {
	switch v := arg.(type) {
	case exprAssign:
		return v.left, v.right
	case exprUnary:
		if v.op == tokExclam || v.op == tokInvert {
			return v.child, exprBool{value: false}
		}
	}
	return arg, exprBool{value: true}
}

var errNotArray = errors.New("this action field is not an array")

func setBoolFlag(flag actionFlags, index, value expr, flags *actionFlags) error {
	if index != nil {
		return errNotArray
	}
	set, err := resolveBoolean(value)
	if err != nil {
		return err
	}
	if set {
		*flags |= flag
	} else {
		*flags &^= flag
	}
	return nil
}

func setAffect(index, value expr, flags *actionFlags) error {
	if index != nil {
		return errNotArray
	}
	v, err := resolveEnum(value, lockWhich)
	if err != nil {
		return err
	}
	*flags &^= flagLockNoLock | flagLockNoUnlock
	*flags |= actionFlags(v) //nolint:gosec // the table's own values
	return nil
}

func setActionField(handler actionType, mods *modSet, a *action, f actionField, index, value expr) error {
	switch handler {
	case actNone, actVoid, actTerminate:
		return errors.New("this action takes no argument")
	case actModSet, actModLatch, actModLock:
		return setModsField(handler, mods, a, f, index, value)
	case actGroupSet, actGroupLatch, actGroupLock:
		return setGroupField(handler, a, f, index, value)
	case actPtrMove:
		return setMovePtrField(a, f, index, value)
	case actPtrButton, actPtrLock:
		return setPtrBtnField(handler, a, f, index, value)
	case actPtrDefault:
		return setPtrDfltField(a, f, index, value)
	case actSwitchVT:
		return setSwitchField(a, f, index, value)
	case actCtrlSet, actCtrlLock:
		return setCtrlsField(handler, a, f, index, value)
	case actLegacy:
		return nil
	case actPrivate:
		return setPrivateField(a, f, index, value)
	}
	return fmt.Errorf("no handler for action type %d", handler)
}

func setModsField(handler actionType, mods *modSet, a *action, f actionField, index, value expr) error {
	switch {
	case f == fieldModifiers:
		if index != nil {
			return errNotArray
		}
		if id, ok := value.(exprIdent); ok {
			if n := strings.ToLower(id.name); n == "usemodmapmods" || n == "modmapmods" {
				a.mods = 0
				a.flags |= flagLookupModmap
				return nil
			}
		}
		mask, err := resolveModMask(value, modBoth, mods)
		if err != nil {
			return err
		}
		a.flags &^= flagLookupModmap
		a.mods = mask
		return nil
	case f == fieldUnlockOnPress:
		return errors.New("unlockOnPress requires keymap text format 2")
	case f == fieldClearLocks && (handler == actModSet || handler == actModLatch):
		return setBoolFlag(flagLockClear, index, value, &a.flags)
	case f == fieldLatchToLock && handler == actModLatch:
		return setBoolFlag(flagLatchToLock, index, value, &a.flags)
	case f == fieldLatchOnPress && handler == actModLatch:
		return errors.New("latchOnPress requires keymap text format 2")
	case f == fieldAffect && handler == actModLock:
		return setAffect(index, value, &a.flags)
	}
	return fmt.Errorf("field %s is not defined for this action", lookupValue(fieldNames, uint32(f)))
}

func setGroupField(handler actionType, a *action, f actionField, index, value expr) error {
	switch {
	case f == fieldGroup:
		if index != nil {
			return errNotArray
		}
		spec := value
		flags := a.flags | flagAbsoluteSwitch
		unary, signed := value.(exprUnary)
		signed = signed && (unary.op == tokMinus || unary.op == tokPlus)
		if signed {
			flags &^= flagAbsoluteSwitch
			spec = unary.child
		}
		idx, err := resolveGroup(spec)
		if err != nil {
			return err
		}
		switch {
		case signed && unary.op == tokMinus:
			a.group = -int32(idx) //nolint:gosec // at most maxGroupsV1
		case signed:
			a.group = int32(idx) //nolint:gosec // at most maxGroupsV1
		default:
			a.group = int32(idx - 1) //nolint:gosec // at most maxGroupsV1
		}
		a.flags = flags
		return nil
	case f == fieldClearLocks && (handler == actGroupSet || handler == actGroupLatch):
		return setBoolFlag(flagLockClear, index, value, &a.flags)
	case f == fieldLatchToLock && handler == actGroupLatch:
		return setBoolFlag(flagLatchToLock, index, value, &a.flags)
	case f == fieldLockOnRelease && handler == actGroupLock:
		return errors.New("lockOnRelease requires keymap text format 2")
	}
	return fmt.Errorf("field %s is not defined for this action", lookupValue(fieldNames, uint32(f)))
}

func isSigned(value expr) bool {
	u, ok := value.(exprUnary)
	return ok && (u.op == tokMinus || u.op == tokPlus)
}

func setMovePtrField(a *action, f actionField, index, value expr) error {
	switch f {
	case fieldX, fieldY:
		if index != nil {
			return errNotArray
		}
		v, err := resolveInteger(value, nil)
		if err != nil {
			return err
		}
		if v < math.MinInt16 || v > math.MaxInt16 {
			return fmt.Errorf("pointer offset %d is out of range", v)
		}
		absolute := !isSigned(value)
		if f == fieldX {
			if absolute {
				a.flags |= flagAbsoluteX
			}
			a.x = int16(v)
		} else {
			if absolute {
				a.flags |= flagAbsoluteY
			}
			a.y = int16(v)
		}
		return nil
	case fieldAccel:
		return setBoolFlag(flagAccel, index, value, &a.flags)
	}
	return fmt.Errorf("field %s is not defined for this action", lookupValue(fieldNames, uint32(f)))
}

func setPtrBtnField(handler actionType, a *action, f actionField, index, value expr) error {
	switch {
	case f == fieldButton:
		if index != nil {
			return errNotArray
		}
		btn, err := resolveInteger(value, simpleLookup(buttonNames))
		if err != nil {
			return err
		}
		if btn < 0 || btn > 5 {
			return fmt.Errorf("illegal button value %d", btn)
		}
		a.button = uint8(btn)
		return nil
	case f == fieldAffect && handler == actPtrLock:
		return setAffect(index, value, &a.flags)
	case f == fieldCount:
		if index != nil {
			return errNotArray
		}
		v, err := resolveInteger(value, nil)
		if err != nil {
			return err
		}
		if v < 0 || v > 255 {
			return fmt.Errorf("illegal count %d", v)
		}
		a.count = uint8(v)
		return nil
	}
	return fmt.Errorf("field %s is not defined for this action", lookupValue(fieldNames, uint32(f)))
}

func setPtrDfltField(a *action, f actionField, index, value expr) error {
	switch f {
	case fieldAffect:
		if index != nil {
			return errNotArray
		}
		_, err := resolveEnum(value, ptrDefaults)
		return err
	case fieldButton, fieldValue:
		if index != nil {
			return errNotArray
		}
		button := value
		if isSigned(value) {
			a.flags &^= flagAbsoluteSwitch
			button = value.(exprUnary).child //nolint:errcheck,forcetypeassert // isSigned checked it
		} else {
			a.flags |= flagAbsoluteSwitch
		}
		btn, err := resolveInteger(button, simpleLookup(buttonNames))
		if err != nil {
			return err
		}
		if btn < 1 || btn > 5 {
			return fmt.Errorf("illegal default button value %d", btn)
		}
		if u, ok := value.(exprUnary); ok && u.op == tokMinus {
			btn = -btn
		}
		a.value = int8(btn)
		return nil
	}
	return fmt.Errorf("field %s is not defined for this action", lookupValue(fieldNames, uint32(f)))
}

func setSwitchField(a *action, f actionField, index, value expr) error {
	switch f {
	case fieldScreen:
		if index != nil {
			return errNotArray
		}
		scrn := value
		if isSigned(value) {
			a.flags &^= flagAbsoluteSwitch
			scrn = value.(exprUnary).child //nolint:errcheck,forcetypeassert // isSigned checked it
		} else {
			a.flags |= flagAbsoluteSwitch
		}
		v, err := resolveInteger(scrn, nil)
		if err != nil {
			return err
		}
		if u, ok := value.(exprUnary); ok && u.op == tokMinus {
			v = -v
		}
		if v < math.MinInt8 || v > math.MaxInt8 {
			return fmt.Errorf("illegal screen value %d", v)
		}
		a.screen = int8(v)
		return nil
	case fieldSame:
		return setBoolFlag(flagSameScreen, index, value, &a.flags)
	}
	return fmt.Errorf("field %s is not defined for this action", lookupValue(fieldNames, uint32(f)))
}

func setCtrlsField(handler actionType, a *action, f actionField, index, value expr) error {
	switch {
	case f == fieldControls:
		if index != nil {
			return errNotArray
		}
		mask, err := resolveMask(value, simpleLookup(ctrlMaskNames))
		if err != nil {
			return err
		}
		a.ctrls = mask
		return nil
	case f == fieldAffect && handler == actCtrlLock:
		return setAffect(index, value, &a.flags)
	}
	return fmt.Errorf("field %s is not defined for this action", lookupValue(fieldNames, uint32(f)))
}

func setPrivateField(a *action, f actionField, index, value expr) error {
	switch f {
	case fieldType:
		if index != nil {
			return errNotArray
		}
		v, err := resolveInteger(value, nil)
		if err != nil {
			return err
		}
		if v < 0 || v > 255 {
			return fmt.Errorf("illegal private action type %d", v)
		}
		if v < int64(actPrivate) {
			a.typ = actNone
		} else {
			a.typ = actionType(v)
		}
		return nil
	case fieldData:
		return setPrivateData(a, index, value)
	}
	return fmt.Errorf("field %s is not defined for this action", lookupValue(fieldNames, uint32(f)))
}

func setPrivateData(a *action, index, value expr) error {
	if index == nil {
		s, err := resolveString(value)
		if err != nil {
			return err
		}
		if len(s) < 1 || len(s) > len(a.privData) {
			return fmt.Errorf("a private action has %d data bytes", len(a.privData))
		}
		a.privData = [7]byte{}
		copy(a.privData[:], s)
		return nil
	}
	i, err := resolveInteger(index, nil)
	if err != nil {
		return err
	}
	if i < 0 || i >= int64(len(a.privData)) {
		return fmt.Errorf("private action data index %d is out of range", i)
	}
	v, err := resolveInteger(value, nil)
	if err != nil {
		return err
	}
	if v < 0 || v > 255 {
		return fmt.Errorf("private action datum %d is out of range", v)
	}
	a.privData[i] = byte(v)
	return nil
}
