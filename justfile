# lys-lib dev tasks.

# List all recipes with their descriptions.
default:
    @just --list

# Directory for headless test sessions: compositor socket, logs, pids.
test_dir := "/tmp/lys-lib-test-env"

# Run every release gate in order: vet, lint, test.
check: vet lint test

vet:
    go vet ./...

# Lint every package; settings live in .golangci.yml.
lint:
    golangci-lint run

# Run the test suite for every package.
test:
    go test ./...

# Run every benchmark once, tests skipped.
bench:
    go test -bench . -run '^$' -benchmem ./...

# Regenerate the render golden images after a deliberate visual change;
# review the diff (git diff -- '*testdata/golden*') before committing.
goldens:
    UPDATE_GOLDEN=1 go test ./render

# Rasterize the SVG references (render/testdata/svgref) with
# rsvg-convert, the independent renderer render.LoadSVG is held to; the
# PNG's size is the size the test renders at.
svg-refs:
    #!/usr/bin/env bash
    set -euo pipefail
    for svg in render/testdata/svgref/*.svg; do
        rsvg-convert -w 64 -h 64 "$svg" -o "${svg%.svg}.png"
    done

# Compile the GPU renderer's GLSL to the SPIR-V package gpu embeds, and
# regenerate the Wayland protocol bindings.
generate:
    go generate ./gpu ./wlr

# Format every Go source in place: gofumpt and gci as configured in
# .golangci.yml, which also excludes third_party/ (upstream code carried
# verbatim) and generated files.
fmt:
    golangci-lint fmt ./...

# The formatting gate: fail, showing the diff, when any source is not
# formatted.
fmt-check:
    golangci-lint fmt --diff ./...

# The GPU tests on lavapipe, Mesa's CPU Vulkan driver, so they gate
# without a GPU. `go test ./gpu/...` alone runs them on the host's
# device, or skips them without one. Verbose: a driver crash kills the
# test binary without a Go trace, and the last test it started is the
# only name the log then has.
gpu:
    LYS_TEST_GPU=lavapipe go test -count=1 -v ./gpu/...

# The GPU paths on this host's own GPU, under the validation layer:
# the renderer, dmabuf export and the video encoder, then the dmabuf
# capture tests against a headless sway rendering on the same GPU. For
# AMD and Intel machines, which CI's lavapipe cannot stand in for:
# lavapipe has no video encoder and no compositor-side dmabuf import.
gpu-host:
    #!/bin/sh
    if ! out=$(LYS_TEST_GPU=host go test -count=1 -run '^TestDeviceCloseClosesTargets$' ./gpu 2>&1); then
        printf '%s\n' "$out" | grep -E 'GPU device|vkCreate|Vulkan' | head -n 3
        echo "gpu-host: Vulkan opens no device that exports dmabufs here (VK_DRIVER_FILES=${VK_DRIVER_FILES:-unset})."
        echo "Off NixOS the dev shell points the loader at nixpkgs' Mesa (AMD, Intel); NVIDIA's driver needs nixGL."
        exit 1
    fi
    rc=0
    LYS_TEST_GPU=host go test -count=1 -v ./gpu/... 2>&1 | grep -vE '^=== (RUN|PAUSE|CONT)' || rc=1
    dir=$(mktemp -d /tmp/lys-lib-gpu-host.XXXXXX)
    LYS_TEST_WLR_RENDERER=vulkan just test-env "$dir" || exit 1
    trap 'just test-env-stop "$dir"' EXIT INT TERM
    . "$dir/client.env"
    export WAYLAND_DISPLAY DBUS_SESSION_BUS_ADDRESS XDG_RUNTIME_DIR="$dir" LYS_TEST_HEADLESS=1
    go test -count=1 -v -run Dmabuf ./capture 2>&1 | grep -vE '^=== (RUN|PAUSE|CONT)' || rc=1
    exit $rc

# Run the suite under the race detector. It needs cgo, so only this
# recipe enables it, with the dev shell's pinned C compiler
# (LYS_RACE_CC).
race:
    CC="$LYS_RACE_CC" CGO_ENABLED=1 go test -race ./...

# The compositor-in-the-loop tests: boots a private headless sway with
# the test-env recipe, then runs capture and virtual input against it
# with LYS_TEST_HEADLESS=1. The env is always torn down, pass or fail.
# Needs no display and never touches the desktop session.
headless:
    #!/bin/sh
    dir="{{test_dir}}"
    just test-env "$dir" || exit 1
    trap 'just test-env-stop "$dir"' EXIT INT TERM
    . "$dir/client.env"
    export WAYLAND_DISPLAY DBUS_SESSION_BUS_ADDRESS XDG_RUNTIME_DIR="$dir" LYS_TEST_HEADLESS=1
    # The packages share one compositor: run them one at a time, so no
    # package's surfaces show in another's captures.
    go test -p 1 ./capture ./vinput -count=1

# Boot a private headless sway in dir: its socket, logs and pid live
# there; client.env names the display and session bus.
test-env dir=test_dir:
    #!/bin/sh
    dir="{{dir}}"
    if [ -f "$dir/sway.pid" ] && kill -0 "$(cat "$dir/sway.pid")" 2>/dev/null; then
        echo "test compositor already running in $dir"
        exit 0
    fi
    rm -rf "$dir"
    # A daemonizing compositor can outlive its pid file: sweep anything
    # still bound to this dir's config before booting a fresh sway, or
    # the new socket and the old one race for clients.
    pkill -9 -f "$dir/sway.cfg" 2>/dev/null
    pkill -f "$dir/bus.conf" 2>/dev/null
    mkdir -p "$dir" && chmod 700 "$dir"
    printf '%s\n' 'output * mode 1280x800 scale 1' \
        'default_border none' \
        'default_floating_border none' \
        > "$dir/sway.cfg"
    # The pid file must hold sway itself, not the dev shell wrapper:
    # a wrapper that runs the command through a shell without exec'ing
    # can die with the kill test's SIGKILL while sway lives on holding
    # the display socket - the client never sees an EOF and parks
    # forever. A shell that writes its own pid and then execs sway
    # records the process that owns the socket, whatever the wrapper's
    # fork/exec choice is.
    # A private session bus from a config written here (the
    # dbustest helper's shape): nixpkgs' sway wrapper runs
    # dbus-run-session when no bus is set, which needs the host's
    # /etc/dbus-1/session.conf - absent on CI runners.
    printf '%s\n' '<!DOCTYPE busconfig PUBLIC "-//freedesktop//DTD D-BUS Bus Configuration 1.0//EN" "http://www.freedesktop.org/standards/dbus/1.0/busconfig.dtd">' \
        "<busconfig><type>session</type><listen>unix:path=$dir/bus</listen><auth>EXTERNAL</auth>" \
        '<policy context="default"><allow send_destination="*" eavesdrop="true"/><allow eavesdrop="true"/><allow own="*"/></policy></busconfig>' \
        > "$dir/bus.conf"
    bus=$(dbus-daemon --config-file="$dir/bus.conf" --fork --nopidfile --print-address=1)
    if [ -z "$bus" ]; then
        echo "private session bus failed to start"; exit 1
    fi
    # Inside the dev shell (CI runs `nix develop -c just headless`)
    # sway is already on PATH, so the dev shell is only the fallback
    # for a host shell.
    # LYS_TEST_WLR_RENDERER picks sway's renderer: pixman (the
    # default, no GPU) or gles2 / vulkan on the host's GPU, which the
    # dmabuf capture tests need (gpu-host).
    boot="exec sway --unsupported-gpu -c '$dir/sway.cfg'"
    if ! command -v sway >/dev/null 2>&1; then
        boot="exec nix develop -c sway --unsupported-gpu -c '$dir/sway.cfg'"
    fi
    DBUS_SESSION_BUS_ADDRESS="$bus" XDG_RUNTIME_DIR="$dir" WLR_BACKENDS=headless WLR_LIBINPUT_NO_DEVICES=1 \
        WLR_RENDERER="${LYS_TEST_WLR_RENDERER:-pixman}" sh -c "echo \$\$ > '$dir/sway.pid'; $boot" \
        >"$dir/sway.log" 2>&1 &
    sock=""
    for i in $(seq 1 50); do
        sock=$(ls "$dir"/wayland-[0-9]* 2>/dev/null | head -1)
        [ -n "$sock" ] && break
        sleep 0.2
    done
    if [ -z "$sock" ]; then
        echo "sway failed to start; log: $dir/sway.log"
        tail -n 40 "$dir/sway.log"
        exit 1
    fi
    printf 'WAYLAND_DISPLAY=%s\nDBUS_SESSION_BUS_ADDRESS=%s\n' "${sock##*/}" "$bus" >"$dir/client.env"
    echo "test compositor up: $(cat "$dir/client.env") XDG_RUNTIME_DIR=$dir"

# Stop the headless test compositor and wipe its runtime dir.
test-env-stop dir=test_dir:
    #!/bin/sh
    dir="{{dir}}"
    if [ -f "$dir/sway.pid" ]; then
        pid="$(cat "$dir/sway.pid")"
        kill "$pid" 2>/dev/null
        for i in $(seq 1 20); do
            kill -0 "$pid" 2>/dev/null || break
            sleep 0.1
        done
        kill -9 "$pid" 2>/dev/null
    fi
    # The recorded pid can be a parent whose forked child is the real
    # compositor holding the display socket; sweep anything still bound
    # to this dir's config so no survivor outlives the teardown.
    pkill -9 -f "$dir/sway.cfg" 2>/dev/null
    pkill -f "$dir/bus.conf" 2>/dev/null
    rm -rf "$dir"
    echo "test compositor stopped"
