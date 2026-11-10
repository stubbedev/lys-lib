package logind

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/stubbedev/lys-lib/dbusx"
	dbus "github.com/stubbedev/lys-lib/third_party/godbus"
)

// PowerAction is what Manager.Power asks of the system.
type PowerAction uint8

// The power actions.
const (
	Suspend PowerAction = iota + 1
	Reboot
	PowerOff
)

var powerActions = [...]struct{ name, method string }{
	Suspend:  {"suspend", "Suspend"},
	Reboot:   {"reboot", "Reboot"},
	PowerOff: {"poweroff", "PowerOff"},
}

func (a PowerAction) valid() bool { return a >= Suspend && int(a) < len(powerActions) }

// String is the action as systemctl names it.
func (a PowerAction) String() string {
	if !a.valid() {
		return fmt.Sprintf("PowerAction(%d)", uint8(a))
	}
	return powerActions[a].name
}

func (a PowerAction) method() (string, error) {
	if !a.valid() {
		return "", fmt.Errorf("logind: no power action %s", a)
	}
	return powerActions[a].method, nil
}

// InhibitWhat is the set of operations an inhibitor holds off.
type InhibitWhat uint8

// The operations an inhibitor can hold off.
const (
	InhibitShutdown InhibitWhat = 1 << iota
	InhibitSleep
	InhibitIdle
	InhibitLidSwitch
	inhibitAll = InhibitShutdown | InhibitSleep | InhibitIdle | InhibitLidSwitch
)

var inhibitNames = [...]struct {
	what InhibitWhat
	name string
}{
	{InhibitShutdown, "shutdown"},
	{InhibitSleep, "sleep"},
	{InhibitIdle, "idle"},
	{InhibitLidSwitch, "handle-lid-switch"},
}

// String is the set as logind's Inhibit takes it: the names joined by
// colons.
func (w InhibitWhat) String() string {
	names := make([]string, 0, len(inhibitNames)+1)
	for _, n := range inhibitNames {
		if w&n.what != 0 {
			names = append(names, n.name)
		}
	}
	if rest := w &^ inhibitAll; rest != 0 {
		names = append(names, fmt.Sprintf("InhibitWhat(%#x)", uint8(rest)))
	}
	return strings.Join(names, ":")
}

// InhibitMode is how an inhibitor holds its operations off.
type InhibitMode uint8

// The inhibitor modes: InhibitBlock refuses the operations while held,
// InhibitDelay holds them off until released, at most logind's
// InhibitDelayMaxSec.
const (
	InhibitBlock InhibitMode = iota + 1
	InhibitDelay
)

// String is the mode as logind's Inhibit takes it.
func (m InhibitMode) String() string {
	switch m {
	case InhibitBlock:
		return "block"
	case InhibitDelay:
		return "delay"
	}
	return fmt.Sprintf("InhibitMode(%d)", uint8(m))
}

var errNothingInhibited = errors.New("nothing to inhibit")

// Inhibit takes an inhibitor on what for who, saying why: it holds
// until the returned file is closed.
func (m Manager) Inhibit(ctx context.Context, what InhibitWhat, who, why string, mode InhibitMode) (*os.File, error) {
	switch {
	case what == 0:
		return nil, fmt.Errorf("logind: inhibit for %s: %w", who, errNothingInhibited)
	case what&^inhibitAll != 0:
		return nil, fmt.Errorf("logind: inhibit %s for %s: an unknown operation", what, who)
	case mode != InhibitBlock && mode != InhibitDelay:
		return nil, fmt.Errorf("logind: inhibit %s for %s: no mode %s", what, who, mode)
	}
	var fd dbus.UnixFD
	if err := m.obj.call(ctx, ManagerInterface, "Inhibit", []any{&fd}, what.String(), who, why, mode.String()); err != nil {
		return nil, err
	}
	f, err := dbusx.File(fd, "logind "+what.String()+" "+mode.String()+" inhibitor")
	if err != nil {
		return nil, fmt.Errorf("logind: inhibit %s for %s: %w", what, who, err)
	}
	return f, nil
}
