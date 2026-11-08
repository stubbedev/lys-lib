// Package wmipc is the wire contract of lys-wm's IPC socket: JSON lines,
// each request answered by a Response, and a watch stream of events. The
// compositor serves it and its clients speak it. Output, Workspace and
// Window are the views clients decode, a subset of what the compositor
// sends, so that a list of windows does not decode every window's icon.
package wmipc

import "encoding/json"

// SocketEnv is the variable lys-wm exports its socket path in.
const SocketEnv = "LYS_WM_SOCKET"

// Version is the protocol version every Response carries.
const Version = 1

// Kind names a request.
type Kind string

// The requests.
const (
	KindVersion        Kind = "version"
	KindOutputs        Kind = "outputs"
	KindWorkspaces     Kind = "workspaces"
	KindWindows        Kind = "windows"
	KindActiveWindow   Kind = "activewindow"
	KindSubmap         Kind = "submap"
	KindLayout         Kind = "layout"
	KindLocks          Kind = "locks"
	KindReducedMotion  Kind = "reducedmotion"
	KindDispatch       Kind = "dispatch"
	KindWatch          Kind = "watch"
	KindIcon           Kind = "icon"
	KindOverview       Kind = "overview"
	KindScratchpads    Kind = "scratchpads"
	KindConfigProblems Kind = "configproblems"
	KindUpgrade        Kind = "upgrade"
	KindCreateOutput   Kind = "createoutput"
	KindRemoveOutput   Kind = "removeoutput"
	KindCapture        Kind = "capture"
	KindLayers         Kind = "layers"
	KindDevices        Kind = "devices"
	KindPickWindow     Kind = "pickwindow"
	KindCastTarget     Kind = "casttarget"
	KindBinds          Kind = "binds"
)

// EventKind names one event of the watch stream.
type EventKind string

// The watch events.
const (
	EventOutput         EventKind = "output"
	EventWorkspace      EventKind = "workspace"
	EventFocusedOutput  EventKind = "focusedoutput"
	EventOpen           EventKind = "open"
	EventClose          EventKind = "close"
	EventMove           EventKind = "move"
	EventTitle          EventKind = "title"
	EventAppID          EventKind = "appid"
	EventRect           EventKind = "rect"
	EventIcon           EventKind = "icon"
	EventUrgent         EventKind = "urgent"
	EventMinimized      EventKind = "minimized"
	EventMode           EventKind = "mode"
	EventUnresponsive   EventKind = "unresponsive"
	EventPin            EventKind = "pin"
	EventSwallow        EventKind = "swallow"
	EventStack          EventKind = "stack"
	EventFocus          EventKind = "focus"
	EventSubmap         EventKind = "submap"
	EventLayout         EventKind = "layout"
	EventLocks          EventKind = "locks"
	EventReducedMotion  EventKind = "reducedmotion"
	EventDispatch       EventKind = "dispatch"
	EventOverview       EventKind = "overview"
	EventConfigProblems EventKind = "configproblems"
	EventWindowMenu     EventKind = "windowmenu"
	EventShowDesktop    EventKind = "showdesktop"
	EventScratchpad     EventKind = "scratchpad"
	EventLayer          EventKind = "layer"
	EventGroup          EventKind = "group"
	EventBell           EventKind = "bell"
	EventCastTarget     EventKind = "casttarget"
	EventBinds          EventKind = "binds"
	EventMRU            EventKind = "mru"
	EventSwitcher       EventKind = "switcher"
)

// CaptureEventKind names one event of an input capture stream.
type CaptureEventKind string

// The input capture events.
const (
	CaptureKeymap         CaptureEventKind = "keymap"
	CaptureActivated      CaptureEventKind = "activated"
	CaptureDeactivated    CaptureEventKind = "deactivated"
	CaptureDisabled       CaptureEventKind = "disabled"
	CaptureMotion         CaptureEventKind = "motion"
	CaptureButton         CaptureEventKind = "button"
	CaptureScroll         CaptureEventKind = "scroll"
	CaptureScrollDiscrete CaptureEventKind = "scroll_discrete"
	CaptureScrollStop     CaptureEventKind = "scroll_stop"
	CaptureFrame          CaptureEventKind = "frame"
	CaptureKey            CaptureEventKind = "key"
	CaptureModifiers      CaptureEventKind = "modifiers"
	CaptureError          CaptureEventKind = "error"
	CaptureApplied        CaptureEventKind = "applied"
)

// Response answers one request: Error when the compositor refused it,
// otherwise Result.
type Response struct {
	Version int             `json:"version"`
	Error   string          `json:"error,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
}

// Rect is a rectangle in logical pixels of the output layout.
type Rect struct {
	X      int `json:"x"`
	Y      int `json:"y"`
	Width  int `json:"width"`
	Height int `json:"height"`
}

// Mode is an output mode: its size in pixels and its refresh rate in
// millihertz.
type Mode struct {
	Width      int   `json:"width"`
	Height     int   `json:"height"`
	RefreshMHz int32 `json:"refresh_mhz"`
}

// Layout is the active keyboard layout group.
type Layout struct {
	Index uint32 `json:"index"`
	Name  string `json:"name"`
}

// Locks is the lock keys' state as the keymap's LEDs show it.
type Locks struct {
	Caps   bool `json:"caps"`
	Num    bool `json:"num"`
	Scroll bool `json:"scroll"`
}

// CapturePoint is a point of the layout in logical pixels.
type CapturePoint struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// Output is a client's view of one entry of the outputs result.
type Output struct {
	Name        string  `json:"name"`
	Description string  `json:"description"`
	Rect        Rect    `json:"rect"`
	Scale       float64 `json:"scale"`
	Mode        Mode    `json:"mode"`
	Workspace   int     `json:"workspace"`
	Focused     bool    `json:"focused"`
	Powered     bool    `json:"powered"`
}

// Workspace is a client's view of one open workspace.
type Workspace struct {
	ID      int    `json:"id"`
	Output  string `json:"output"`
	Windows int    `json:"windows"`
	Visible bool   `json:"visible"`
	Focused bool   `json:"focused"`
}

// Window is a client's view of one mapped window.
type Window struct {
	ID         uint64 `json:"id"`
	AppID      string `json:"app_id"`
	Title      string `json:"title"`
	Workspace  int    `json:"workspace"`
	Output     string `json:"output"`
	Rect       Rect   `json:"rect"`
	Floating   bool   `json:"floating"`
	Fullscreen bool   `json:"fullscreen"`
	Minimized  bool   `json:"minimized"`
	Urgent     bool   `json:"urgent"`
	Focused    bool   `json:"focused"`
	// Stack is the window's front-to-back index among the windows its
	// output shows, 0 on top; nil while no output shows it.
	Stack *int `json:"stack,omitempty"`
}
