package xdg

import (
	"github.com/stubbedev/lys-lib/third_party/wayland/wl"
	xdgshell "github.com/stubbedev/lys-lib/third_party/wayland/xdg"
)

type BaseProxy = wl.BaseProxy
type Context = wl.Context
type Event = wl.Event
type Toplevel = xdgshell.Toplevel
