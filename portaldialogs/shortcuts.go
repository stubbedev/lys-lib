package portaldialogs

import (
	"errors"
	"fmt"
	"slices"
)

// ShortcutRow is one shortcut of a BindShortcuts dialog, (ssss): its
// id, what it does, the trigger it starts with in the accelerator label
// form ("Ctrl+Alt+P", "" for unassigned), and why the compositor
// refused that trigger ("" when it did not).
type ShortcutRow struct {
	ID, Description, Trigger, Refusal string
}

// ShortcutsRequest is one BindShortcuts dialog, (ssa(ssss)): its
// title, a note under it ("" for none), and the shortcuts, each id
// once.
type ShortcutsRequest struct {
	Title, Note string
	Rows        []ShortcutRow
}

// ShortcutTrigger is the trigger the user left on one shortcut, (ss),
// in the accelerator label form, "" for unassigned.
type ShortcutTrigger struct {
	ID, Trigger string
}

// check refuses a request without shortcuts or with an id twice.
func (r ShortcutsRequest) check() error {
	if len(r.Rows) == 0 {
		return errors.New("no shortcuts")
	}
	for i, row := range r.Rows {
		if slices.ContainsFunc(r.Rows[:i], func(o ShortcutRow) bool { return o.ID == row.ID }) {
			return fmt.Errorf("shortcut %q twice", row.ID)
		}
	}
	return nil
}

// checkReply refuses a granted reply that does not give every
// shortcut of r exactly one trigger.
func (r ShortcutsRequest) checkReply(triggers []ShortcutTrigger) error {
	if len(triggers) != len(r.Rows) {
		return fmt.Errorf("%d triggers for %d shortcuts", len(triggers), len(r.Rows))
	}
	for _, row := range r.Rows {
		if n := countFunc(triggers, func(t ShortcutTrigger) bool { return t.ID == row.ID }); n != 1 {
			return fmt.Errorf("%d triggers for shortcut %q", n, row.ID)
		}
	}
	return nil
}

func countFunc[T any](s []T, f func(T) bool) int {
	n := 0
	for _, v := range s {
		if f(v) {
			n++
		}
	}
	return n
}
