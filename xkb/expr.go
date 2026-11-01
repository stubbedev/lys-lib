package xkb

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

type lookupEntry struct {
	name  string
	value uint32
}

func lookupName(table []lookupEntry, name string) (uint32, bool) {
	for _, e := range table {
		if strings.EqualFold(e.name, name) {
			return e.value, true
		}
	}
	return 0, false
}

func lookupValue(table []lookupEntry, value uint32) string {
	for _, e := range table {
		if e.value == value {
			return e.name
		}
	}
	return ""
}

type identLookup func(name string) (uint32, bool)

func simpleLookup(table []lookupEntry) identLookup {
	return func(name string) (uint32, bool) { return lookupName(table, name) }
}

type lhs struct {
	elem, field string
	index       expr
}

func resolveLhs(e expr) (lhs, error) {
	switch v := e.(type) {
	case exprIdent:
		return lhs{field: v.name}, nil
	case exprField:
		return lhs{elem: v.elem, field: v.field}, nil
	case exprArray:
		return lhs(v), nil
	}
	return lhs{}, fmt.Errorf("%T is not assignable", e)
}

func resolveBoolean(e expr) (bool, error) {
	switch v := e.(type) {
	case exprBool:
		return v.value, nil
	case exprIdent:
		switch strings.ToLower(v.name) {
		case "true", "yes", "on":
			return true, nil
		case "false", "no", "off":
			return false, nil
		}
		return false, codedf(codeInvalidIdentifier, "identifier %q of type boolean is unknown", v.name)
	case exprUnary:
		if v.op == tokExclam || v.op == tokInvert {
			b, err := resolveBoolean(v.child)
			return !b, err
		}
	}
	return false, codedf(codeWrongFieldType, "found %T where a boolean was expected", e)
}

func resolveInteger(e expr, lookup identLookup) (int64, error) {
	switch v := e.(type) {
	case exprInt:
		return v.value, nil
	case exprIdent:
		if lookup != nil {
			if u, ok := lookup(v.name); ok {
				return int64(u), nil
			}
		}
		return 0, codedf(codeInvalidIdentifier, "identifier %q of type int is unknown", v.name)
	case exprBinary:
		l, err := resolveInteger(v.left, lookup)
		if err != nil {
			return 0, err
		}
		r, err := resolveInteger(v.right, lookup)
		if err != nil {
			return 0, err
		}
		return integerOp(v.op, l, r)
	case exprUnary:
		child, err := resolveInteger(v.child, lookup)
		if err != nil {
			return 0, err
		}
		switch v.op {
		case tokMinus:
			return -child, nil
		case tokInvert:
			return ^child, nil
		case tokPlus:
			return child, nil
		}
		return 0, errors.New("the ! operator cannot be applied to an integer")
	}
	return 0, codedf(codeWrongFieldType, "found %T where an int was expected", e)
}

func integerOp(op tokenKind, l, r int64) (int64, error) {
	switch op {
	case tokPlus:
		s := l + r
		if (s > l) != (r > 0) {
			return 0, codedf(codeIntegerOverflow, "%d + %d overflows", l, r)
		}
		return s, nil
	case tokMinus:
		s := l - r
		if (s < l) != (r > 0) {
			return 0, codedf(codeIntegerOverflow, "%d - %d overflows", l, r)
		}
		return s, nil
	case tokTimes:
		if l != 0 && (l*r)/l != r {
			return 0, codedf(codeIntegerOverflow, "%d * %d overflows", l, r)
		}
		return l * r, nil
	case tokDivide:
		if r == 0 {
			return 0, fmt.Errorf("cannot divide %d by zero", l)
		}
		return l / r, nil
	}
	return 0, fmt.Errorf("operator %d of integers not permitted", op)
}

func namedIndexLookup(prefix string, minimum, maximum uint32, entries []lookupEntry) identLookup {
	return func(name string) (uint32, bool) {
		if len(name) > len(prefix) && strings.EqualFold(name[:len(prefix)], prefix) {
			if v, err := strconv.ParseUint(name[len(prefix):], 10, 32); err == nil {
				if uint32(v) < minimum || uint32(v) > maximum {
					return 0, false
				}
				return uint32(v), true
			}
		}
		return lookupName(entries, name)
	}
}

func resolveGroup(e expr, limit uint32) (uint32, error) {
	v, err := resolveInteger(e, namedIndexLookup("Group", 1, limit, nil))
	if err != nil {
		return 0, err
	}
	if v < 1 || v > int64(limit) {
		return 0, codedf(codeGroupIndex, "group index %d is out of range (1..%d)", v, limit)
	}
	return uint32(v), nil //nolint:gosec // checked in range above
}

const levelMaxImpl = 2048

func resolveLevel(e expr) (uint32, error) {
	v, err := resolveInteger(e, namedIndexLookup("Level", 1, levelMaxImpl, nil))
	if err != nil {
		return 0, err
	}
	if v < 1 || v > levelMaxImpl {
		return 0, codedf(codeShiftLevel, "shift level %d is out of range (1..%d)", v, levelMaxImpl)
	}
	return uint32(v - 1), nil
}

func resolveString(e expr) (string, error) {
	v, ok := e.(exprString)
	if !ok {
		return "", codedf(codeWrongFieldType, "found %T, expected a string", e)
	}
	return v.value, nil
}

func resolveEnum(e expr, table []lookupEntry) (uint32, error) {
	v, ok := e.(exprIdent)
	if !ok {
		return 0, codedf(codeWrongFieldType, "found %T where an enumerated value was expected", e)
	}
	value, ok := lookupName(table, v.name)
	if !ok {
		return 0, codedf(codeInvalidIdentifier, "illegal identifier %q", v.name)
	}
	return value, nil
}

func resolveMask(e expr, lookup identLookup) (uint32, error) {
	switch v := e.(type) {
	case exprInt:
		if v.value < 0 || v.value > math.MaxUint32 {
			return 0, fmt.Errorf("mask %d is out of range", v.value)
		}
		return uint32(v.value), nil
	case exprIdent:
		value, ok := lookup(v.name)
		if !ok {
			return 0, codedf(codeInvalidIdentifier, "identifier %q of type mask is unknown", v.name)
		}
		return value, nil
	case exprBinary:
		l, err := resolveMask(v.left, lookup)
		if err != nil {
			return 0, err
		}
		r, err := resolveMask(v.right, lookup)
		if err != nil {
			return 0, err
		}
		switch v.op {
		case tokPlus:
			return l | r, nil
		case tokMinus:
			return l &^ r, nil
		}
		return 0, errors.New("cannot multiply or divide masks")
	case exprUnary:
		child, err := resolveInteger(v.child, lookup)
		if err != nil {
			return 0, err
		}
		if v.op != tokInvert {
			return 0, errors.New("this unary operator cannot be used with a mask")
		}
		if child < 0 || child > math.MaxUint32 {
			return 0, fmt.Errorf("mask %d is out of range", child)
		}
		return ^uint32(child), nil
	}
	return 0, codedf(codeWrongFieldType, "found %T where a mask was expected", e)
}

func resolveModMask(e expr, typ modType, mods *modSet) (uint32, error) {
	return resolveMask(e, func(name string) (uint32, bool) {
		switch strings.ToLower(name) {
		case "all":
			return 0xff, true
		case "none":
			return 0, true
		}
		i, ok := mods.index(name, typ)
		if !ok {
			return 0, false
		}
		return 1 << i, true
	})
}

func resolveVirtualMod(e expr, mods *modSet) (uint32, error) {
	v, ok := e.(exprIdent)
	if !ok {
		return 0, codedf(codeWrongFieldType, "found %T where a virtual modifier name was expected", e)
	}
	i, ok := mods.index(v.name, modVirtual)
	if !ok {
		return 0, codedf(codeUndeclaredVmod, "virtual modifier %q was not declared", v.name)
	}
	return i, nil
}
