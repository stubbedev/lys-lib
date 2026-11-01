# github.com/neurlang/wayland v0.4.6, patched for lys-lib

A copy of the packages of github.com/neurlang/wayland at v0.4.6
(be3122b, MIT, LICENSE) that lys-lib and lys-ui use, with upstream's
tests: `wl`, `os`, `xdg`, `wlclient`, `wlcursor`, `wlcursor/xcursor`,
`external/swizzle`, `unstable/text-input-v3`,
`unstable/xdg-decoration-v1`, and `cmd/wayland-scanner`, which
generates `wlr`'s bindings (`go tool wayland-scanner`). They are
carried as packages of the lys-lib module and imported as
`github.com/stubbedev/lys-lib/third_party/wayland/...`. A file differs
from upstream by that import path and the patches below, nothing else.

## Why it lives inside the module

As with `third_party/vk` and `third_party/oksvg`: Go ignores a
dependency's `replace` directives, so a patched copy wired in through
go.mod would reach lys-lib's own builds and none of its consumers'
(lys-ui, lys-shell, lys-wm). As packages of the lys-lib module, the
patches reach every application importing them, and every one of them
shares the same `wl` types.

## Patches

Each bug fix goes upstream as its own pull request; drop it here once
merged. neurlang/wayland#45 to #49 (a nil object argument, an absolute
`WAYLAND_DISPLAY`, the fd queue, fd ownership per event, one read per
burst) are merged and released in v0.4.6, so they are upstream's code
here now.

### A destructor request unregisters its proxy

Not upstream yet.

`cmd/wayland-scanner/golang_schema.go`, `xml_read.go`, and every
generated binding: a destructor request was a plain send, so a destroyed
proxy stayed in the client's object table with every handler it held,
and through those handlers a closed window's whole widget tree stayed
reachable. As `wl_proxy_destroy` does, the generated method now
unregisters the proxy once sent. `wl/destructor_test.go` holds the
tests of this patch and the next two.

### delete_id forgets objects the server destroyed

Not upstream yet.

`wl/context.go`, `idReaper`: `wl_display.delete_id` only reaped zombies,
so an object the server destroyed on its own, such as the frame callback
of a destroyed surface, stayed registered with its handlers. As in
libwayland, `delete_id` now drops the proxy too.

### An event in flight for a destroyed object is ignored

Not upstream yet.

`wl/context.go`, `Unregister`, `Context.run`; `wl/event.go`: with
destructors unregistering, an event the compositor sent before it saw
the destroy, such as `wl_buffer.release`, ended `Run` with
`ErrContextRunProxyNil`. As in libwayland, every destroyed id stays a
zombie until `delete_id` retires it and `Run` ignores its events, while
an id nothing holds is still an error (lys-lib#58). `wl/fd_batch_test.go`
expects no error for the destroyed object accordingly.

### Byte order comes from encoding/binary

`wl/event.go`, `wl/request.go`: the wire's native byte order is
`encoding/binary.NativeEndian` instead of
github.com/yalue/native_endian, which kept a dependency for what the
standard library has had since Go 1.21. This is a lys-lib integration
choice rather than an upstream bug.

### Dispatching what is pending without blocking

`wl/event.go`, `Context.Buffered` and `Context.ReadReady`: `Run` reads
one event and blocks when none is buffered, and nothing else could
tell a caller whether it would, so a loop could only dispatch one
event per pass. As libwayland's `wl_display_dispatch_pending` and
`wl_display_prepare_read` pair lets a client do, `Buffered` reports a
whole event already read, and `ReadReady` polls the socket with a zero
timeout and reads once when it has data. A loop drains a burst with
them in one pass and a bounded number of reads (lys-ui#432).
`wl/pending_test.go` holds the tests. This is a lys-lib integration
choice rather than an upstream bug.

### A read deadline wakes a parked loop

Context.SetReadDeadline exposes the socket's read deadline, so a loop parked in
Run can wake for its own timer without a wl_display.sync round trip through the
compositor. Run now reports a deadline as ErrContextRunTimeout: upstream's check
never matched, since the error reaches it wrapped.

### Requests are buffered until something can depend on them

`wl/request.go`: every request was a `sendmsg` of its own, waking the
compositor to read one message, and an allocation per argument. A
window's frame (attach, damage, frame, commit) took four of each.
Requests now encode into one buffer on the context; a proxy implementing
`CommitDeferred` names the requests that only set state a later request
applies (`wl_surface`'s other than commit and destroy, `wp_viewport`'s
in `wlr`), and those stay in the buffer. Nothing can wait on them, so a
client needs no flush call as libwayland's do; any other request carries
them ahead of itself, in order. `Request` and `BytePool` are gone. A
`[]int32` argument now states its length in bytes, as the protocol says.
A write larger than the socket takes at once continues rather than
failing. `wl/request_test.go` holds the tests. This is a lys-lib
integration choice rather than an upstream bug.
