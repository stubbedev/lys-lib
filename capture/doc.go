// Package capture copies screen contents out of the compositor: whole
// outputs and output regions through wlr-screencopy, single windows
// through ext-image-copy-capture (with the ext-foreign-toplevel-list
// enumeration and its capture sources) or Hyprland's toplevel-export,
// and continuous damage-driven output capture through an
// ext-image-copy-capture session (Stream). Screencopy frames can also
// land in a caller-allocated dmabuf imported through linux-dmabuf, the
// zero-copy path a screencast producer wants.
//
// Capture runs on a private Wayland connection (Client), independent
// of the application's own session and event loop: every call blocks
// the calling goroutine until the compositor finished the copy, so run
// captures off the UI goroutine. Frames arrive in shared memory, are
// copied into a Go slice (Frame.Data), and convert to image.RGBA with
// Frame.Image for the 8-bit and 10-bit packed formats compositors hand
// out.
//
// Protocols are feature-detected: Connect succeeds on any compositor,
// the Has* methods report what is offered, and a capture path whose
// protocol is missing fails with ErrUnsupported naming it.
package capture
