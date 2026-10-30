#!/usr/bin/env bash
# Rebuilds llvmpipe's compile of every fs_variant function in a gallivm.txt
# that run.sh wrote: Mesa 26.2's own pass pipeline, then llc with the CPU,
# target attributes and code model llvmpipe gives LLVM, which reproduces
# the JIT's machine code. Each function's code is disassembled for
# fault-check.sh, and compiled once more under LLVM's machine verifier.
# Usage: repro/mesa-16702/llc-check.sh <out-dir> [mcpu, default znver4]
set -euo pipefail

out=$(realpath "${1:?usage: llc-check.sh <out-dir> [mcpu]}")
mcpu=${2:-znver4}
root=$(git rev-parse --show-toplevel)
llvm=$(nix build --inputs-from "$root" --no-link --print-out-paths 'nixpkgs#llvmPackages_21.llvm^out')
binutils=$(nix build --inputs-from "$root" --no-link --print-out-paths 'nixpkgs#binutils-unwrapped^out')
attrs='+64bit,+sse,+sse2,+sse3,+ssse3,+sse4.1,+sse4.2,+avx,+f16c,+fma,+avx2,+avx512f,+avx512cd,+avx512bw,+avx512dq,+avx512vl,+avx512vbmi'
passes='sroa,early-cse,simplifycfg,reassociate,mem2reg,instsimplify,instcombine<no-verify-fixpoint>'
llc=("$llvm/bin/llc" -O2 -mtriple=x86_64-unknown-linux-gnu -mcpu="$mcpu" -mattr="$attrs" -code-model=large)

mkdir -p "$out/llc"
n=0
while IFS=: read -r start _; do
    n=$((n + 1))
    ll="$out/llc/fs$n.ll"
    {
        printf 'target datalayout = "e-m:e-p270:32:32-p271:32:32-p272:64:64-i64:64-i128:128-f80:128-n8:16:32:64-S128"\n'
        printf 'target triple = "x86_64-unknown-linux-gnu"\n\n'
        awk -v s="$start" 'NR >= s { print } NR > s && /^}/ { exit }' "$out/gallivm.txt"
    } > "$ll"
    "$llvm/bin/opt" -passes='default<O0>' "$ll" |
        "$llvm/bin/opt" -passes="$passes" -o "$out/llc/fs$n.opt.bc"
    "$llvm/bin/llvm-dis" "$out/llc/fs$n.opt.bc" -o "$out/llc/fs$n.opt.ll"
    "${llc[@]}" -filetype=obj "$out/llc/fs$n.opt.bc" -o "$out/llc/fs$n.$mcpu.o"
    "$binutils/bin/objdump" -d --no-show-raw-insn "$out/llc/fs$n.$mcpu.o" > "$out/llc/fs$n.$mcpu.dis"
    verifier=ok
    if ! ("${llc[@]}" -verify-machineinstrs -filetype=null "$out/llc/fs$n.opt.bc"; exit $?) 2> "$out/llc/fs$n.verify.txt"; then
        verifier=$(grep -m1 -A3 '^\*\*\* Bad machine code' "$out/llc/fs$n.verify.txt" | sed -n 's/^- instruction: *[0-9]*B\t*//p')
    fi
    echo "fs$n: $(grep -c ' sdiv ' "$ll") sdiv, $(grep -c idiv "$out/llc/fs$n.$mcpu.dis") idiv in llc -mcpu=$mcpu; machine verifier: ${verifier:-failed, see fs$n.verify.txt}"
done < <(grep -n '^define void @fs_variant' "$out/gallivm.txt")
