# github.com/neurlang/wayland v0.4.5, patched for lys-lib

A copy of the packages of github.com/neurlang/wayland at v0.4.5
(9245b77, MIT, LICENSE) that lys-lib and lys-ui use, their non-test
sources only: `wl`, `os`, `xdg`, `wlclient`, `wlcursor`,
`wlcursor/xcursor`, `external/swizzle`, `unstable/text-input-v3`,
`unstable/xdg-decoration-v1`, and `cmd/wayland-scanner`, which
generates `wlr`'s bindings (`go tool wayland-scanner`). They are
carried as packages of the lys-lib module and imported as
`github.com/stubbedev/lys-lib/third_party/wayland/...`; the only change
to every file is that import path.

## Why it lives inside the module

As with `third_party/vk` and `third_party/oksvg`: Go ignores a
dependency's `replace` directives, so a patched copy wired in through
go.mod would reach lys-lib's own builds and none of its consumers'
(lys-ui, lys-shell, lys-wm). As packages of the lys-lib module, the
patches reach every application importing them, and every one of them
shares the same `wl` types.

## Patches

Each bug fix goes upstream as its own pull request; drop it here once
merged.

### An absolute WAYLAND_DISPLAY connects as is

Upstream: https://github.com/neurlang/wayland/pull/46

`wl/context.go`, `Connect`: it always joined `XDG_RUNTIME_DIR` and the
display name, so an absolute `WAYLAND_DISPLAY`, which lys-wm hands the
programs it starts, dialed `/run/user/1000//run/user/1000/wayland-0`
and failed with ENOENT. As in libwayland's `wl_display_connect`, an
absolute name is the socket's path and `XDG_RUNTIME_DIR` is needed only
for a relative one (lys-lib#10). `wl/connect_test.go` is the upstream
pull request's test.

### A nil object argument goes out as id 0

Upstream: https://github.com/neurlang/wayland/pull/45

`wl/request.go`, `SendRequest`: its scan for new ids called `Id()` on
every proxy argument, so a typed-nil one (`wl_data_device.set_selection`
with no source, `xdg_toplevel.set_parent` with no parent) panicked with
the send lock held and deadlocked every later request. `PutProxy`
already writes a nil proxy as 0; the scan now skips it (lys-lib#13).
`wl/nil_argument_test.go` is the upstream pull request's test.

### Every received fd reaches its event

`wl/event.go`, `readEvent` and `Event.FD`: libwayland sends the fds of
every event in one flush together, in one SCM_RIGHTS message, but the
reader kept control messages and `FD` took only the first fd of each,
so the second of two `wl_data_source.send` events flushed together got
`ErrNoControlMsgs` and its fd leaked; its 24-byte control buffer also
truncated anything past two fds. Received fds now queue in order per
connection and each fd argument takes the next one; the buffer fits
libwayland's MAX_FDS_OUT (28), a truncated control message fails the
read, the payload is read with the same fd-collecting read, and `Close`
closes the fds no event took (lys-lib#24). `wl/fd_batch_test.go` is
the test.

### Byte order comes from encoding/binary

`wl/event.go`, `wl/request.go`: the wire's native byte order is
`encoding/binary.NativeEndian` instead of
github.com/yalue/native_endian, which kept a dependency for what the
standard library has had since Go 1.21. This is a lys-lib integration
choice rather than an upstream bug.
