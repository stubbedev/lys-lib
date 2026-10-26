#!/usr/bin/env bash
# Runs lys-lib's crashing test pair on lavapipe under gdb, with llvmpipe's
# IR and JIT disassembly dumped, and writes everything to the output dir.
# Usage: repro/mesa-16702/run.sh <out-dir> [extra env, e.g. GALLIUM_OVERRIDE_CPU_CAPS=avx]
set -euo pipefail

out=$(realpath -m "${1:?usage: run.sh <out-dir> [VAR=value...]}")
shift
mkdir -p "$out"
root=$(git rev-parse --show-toplevel)

{
    echo "commit: $(git -C "$root" rev-parse HEAD)"
    echo "extra env: $*"
    grep -m1 'model name' /proc/cpuinfo
    echo "avx512 flags: $(grep -m1 '^flags' /proc/cpuinfo | grep -o 'avx512[a-z_0-9]*' | tr '\n' ' ')"
} > "$out/system.txt"

nix develop "$root" -c go test -c -o "$out/gpu.test" "$root/gpu"
nix develop "$root" -c bash -c 'vulkaninfo --summary 2>/dev/null | grep -E "deviceName|driverInfo" || true' >> "$out/system.txt"

status=0
(
    cd "$root/gpu"
    env LYS_TEST_GPU=lavapipe MESA_SHADER_CACHE_DISABLE=true GALLIVM_DEBUG=ir,asm "$@" \
        nix develop "$root" -c nix shell --inputs-from "$root" nixpkgs#gdb -c \
        gdb -q -batch -x "$root/repro/mesa-16702/crash.gdb" \
            --args "$out/gpu.test" -test.count=1 -test.v -test.run 'TestRenderTiles|TestRenderBlendModes'
) > "$out/gdb.txt" 2> "$out/gallivm.txt" || status=$?

if [[ -f "$root/gpu/fault-window.bin" ]]; then
    mv "$root/gpu/fault-window.bin" "$out/"
fi
signal=$(grep -oE 'received signal SIG[A-Z]+' "$out/gdb.txt" | head -1 | awk '{print $3}' || true)
if [[ -n "$signal" ]]; then
    echo "result: $signal" | tee -a "$out/system.txt"
elif grep -q '^PASS' "$out/gdb.txt"; then
    echo "result: pass" | tee -a "$out/system.txt"
else
    echo "result: other (exit $status)" | tee -a "$out/system.txt"
fi
rm -f "$out/gpu.test"
