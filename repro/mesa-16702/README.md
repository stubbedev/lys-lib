# mesa#16702 reproduction

https://gitlab.freedesktop.org/mesa/mesa/-/work_items/16702

This branch sits on lys-lib 890babc, the last commit before the workaround,
and replaces CI with one workflow, `.github/workflows/mesa-16702.yml`. Each of
its twelve jobs lands on whatever CPU GitHub's `ubuntu-latest` hands it
(AMD EPYC 7763, AMD EPYC 9V74/9V45 Zen 4, Intel Xeon 8370C/8573C/6973P), and:

1. on AVX-512 hosts, runs `hi16.c`, an asm-only loop checking that ymm0-31
   keep their values while many threads are preempted;
2. runs `TestRenderTiles` then `TestRenderBlendModes` on lavapipe under gdb
   (`run.sh`, `crash.gdb`) with `GALLIVM_DEBUG=ir,asm`, saving the IR and JIT
   disassembly, the registers (including ymm0-31 and k0-7) and 104 KiB of the
   code around the faulting pc;
3. on AVX-512 hosts, runs again with `cpuname.c` preloaded, which overrides
   `llvm::sys::getHostCPUName()` so llvmpipe compiles for the other vendor's
   CPU (znver4 on Intel, sapphirerapids on AMD);
4. runs again with `GALLIUM_OVERRIDE_CPU_CAPS=avx` as a control;
5. where a run faulted, rebuilds the faulting code offline and checks it
   (below).

`llc-check.sh <out-dir> [mcpu]` rebuilds the dumped `fs_variant` functions
offline with Mesa 26.2's pass pipeline and
`llc -O2 -mcpu=<mcpu> -mattr=<llvmpipe's list> -code-model=large`, which is
what llvmpipe's JIT runs, and compiles each once more under LLVM's machine
verifier (`-verify-machineinstrs`). `fault-check.sh <out-dir> [mcpu]` then
compares `fault-window.bin` with each rebuild, instruction by instruction at
the same offset into the function. Neither needs AVX-512: any x86-64 host can
run them on a downloaded artifact.

## Results (runs 40384260347, 40386295534, 40388063210, 40389627012, 40391359714)

| Host | Code compiled for | Result |
| --- | --- | --- |
| Zen 4 with AVX-512 exposed (20 runs) | znver4 | SIGFPE every time |
| Zen 4 with AVX-512 hidden by the VM | its own, without AVX-512 | pass |
| Zen 4 with AVX-512 | sapphirerapids (cpuname.c) | pass |
| Intel Xeon 8370C (Ice Lake) | its own | pass |
| Intel Xeon 8370C (Ice Lake) | znver4 (cpuname.c) | SIGFPE |
| Intel Xeon 8573C, 6973P | their own | pass |
| any host | `GALLIUM_OVERRIDE_CPU_CAPS=avx` | pass |
| EPYC 7763 (Zen 3) | its own | pass |

`hi16.c` never saw a vector register change, on Zen 4 or Intel.

So the crash follows code compiled with `-mcpu=znver4` and AVX-512 onto Intel
hardware, and goes away on Zen 4 with Intel tuning: it is in the generated
code, not the CPU or the hypervisor.

At the fault, the zero-divisor replacement is there:

```
vpsubd    %ymm27,%ymm25,%ymm28      # divisor
vpcmpeqd  %ymm27,%ymm25,%k1         # divisor == 0
vmovdqa32 %ymm20,%ymm28{%k1}        # meant to be -1
...
idiv      %esi                      # esi = lane 4 = 0
```

but ymm20 holds `{0, 0, 0, 0x512, 0, 0x47f, 0x55a, 0x558}`, not -1, so the
lanes the replacement selects (k1 = 0x17) become 0.

The faulting function is not one of the functions `GALLIVM_DEBUG=ir,asm`
printed: every printed `fs_variant_partial` (four identical compiles) reads
the -1 from ymm7 at that point and is correct; a dataflow over llc's
reproduction of them finds the constant on every path. The captured code
around the fault (`fault-window.bin`) is a different compile, with more
divisions, that never went through the IR or asm dump.

## Run 40406582460: every module captured

`irdump.c` (LD_PRELOAD, wrapping `LLVMRunPasses`) wrote every module llvmpipe
handed to LLVM, before optimization. The crashing Zen 4 run produced the same
four `fs_variant_partial` modules as every passing run, with no fifth compile.
Compiled offline with Mesa's pass pipeline and
`llc -O2 -mcpu=znver4 -mattr=<llvmpipe's list> -code-model=large`, none of
them contains the faulting `vmovdqa32 %ymm20,%ymm28{%k1}` sequence, and none
matches the `GALLIVM_DEBUG=asm` disassembly of the same run either. That
disassembly in turn matches llc on the IR text `GALLIVM_DEBUG=ir` prints, and
reads the -1 from ymm7 at the same spot. So llvmpipe's JIT does not produce,
from this IR, the code an offline llc does, and the faulting code is neither.
What differs between the two (target options, the module as the JIT sees it,
or code being replaced after the disassembly) is the open question.

## Correction: the faulting function was printed, past the listing's end

The two sections above are wrong about the faulting code, for two reasons:

- `lp_disassemble()` (`lp_bld_debug.cpp`) stops at `extent = 96 * 1024`, so
  every `GALLIVM_DEBUG=asm` listing ends at `17ffc: invalid`. The
  `fs_variant_partial` functions are about 140 KiB (their executable mapping
  is 0x23000 bytes), and the fault is at offset 0x187c2, past the end of the
  listing. The 242 `idiv`s in a listing
  are the first 96 KiB of the 416 in the function, not a smaller compile.
- The `irdump.c` modules carry no `target triple`, so `opt` ran Mesa's passes
  without the x86 target and produced different IR. With the triple added,
  they compile to the same code as the IR text `GALLIVM_DEBUG=ir` prints.

The fault is in `fs329_variant0`, the `TestRenderBlendModes` compile of
`fs164`. Its code starts at the start of the executable mapping that holds the
pc, right after the page holding its constant pool, which is the one the
`fs329` listing's `movabs` immediates point to. Over the 104 KiB that
`fault-window.bin` holds, the JIT's code is the same as the llc rebuild,
instruction for instruction, apart from absolute addresses. This is true for
every faulting attempt checked (40406582460 #9 and 40408043360 #1, #5 and
#10, on EPYC 9V74 and 9V45). So offline llc reproduces the faulting code, and
from IR that GALLIVM_DEBUG prints.

## Cause: LLVM 21 puts a VEX-only -1 in ymm20

LLVM's machine verifier rejects all four `fs_variant_partial` compiles for
znver4, right after the greedy register allocator:

```
*** Bad machine code: Illegal virtual register for instruction ***
- function:    fs_variant_partial
- instruction: %27625:vr256x = AVX2_SETALLONES
```

`AVX2_SETALLONES` (all-ones by `vpcmpeqd`, VEX-encoded) is only valid for the
VR256 class, ymm0-15, but the allocator rematerialized it into a VR256X
virtual register, which may be ymm16-31. Just before pseudo expansion, the
block that rematerializes it reads `renamable $ymm20 = AVX2_SETALLONES`. VEX
has no bit for register 20's high bit: the encoder emits `c4 a1 5d 76 e4`,
putting the bit in VEX.X, which a register-register instruction ignores, so
the CPU runs `vpcmpeqd %ymm4,%ymm4,%ymm4`. ymm4 becomes -1, ymm20 keeps
whatever it held, and the zero-divisor replacement
`vmovdqa32 %ymm20,%ymm28{%k1}` writes that value into the lanes whose divisor
is 0.

In the code, that block is at 0x185a5. A path from 0x17994 reaches the fault
at 0x18772 through it (`je` at 0x17ac5) with no other write to ymm20; the
other path in sets ymm20 from ymm7 at 0x17d22. The ymm20 at the fault is the
masked merge from 0x17994, not -1.

This explains why the crash follows znver4 code and not the CPU. Built for
sapphirerapids, the same four functions pass the verifier
(`llc-check.sh <out-dir> sapphirerapids`), and an AVX2 target has no
ymm16-31 to hand out. The workaround in lys-lib (every such divisor at least 1)
holds because the miscompiled select then has no zero lane to replace. Why
`TestRenderBlendModes` crashes only after `TestRenderTiles` is not explained
yet: the code is the same in both, so the difference must be in which path
reaches the fault, or in what ymm20 holds when it does.

Run 41097735029 confirmed this on all twelve runners. Every SIGFPE (seven of
them) ran code built for znver4 or znver5, including on Intel Xeon 8370C and
8573C forced to znver4. Every such compile fails the verifier on
`AVX2_SETALLONES`, and every fault matches the llc rebuild. Every pass ran
code built for sapphirerapids or AVX2. LLVM reports the EPYC 9V45 as znver5.

## Reduced: `setallones-ymm16-31.ll`

`llvm-reduce` (with the verifier error as the test) cut the 535 KB module down
to 191 lines, which shows the bug without any Mesa code:

```
llc -O2 -mcpu=znver4 -code-model=large -verify-machineinstrs -filetype=null setallones-ymm16-31.ll
```

LLVM 21.1.8 rejects `%584:vr256x = AVX2_SETALLONES`. Without the verifier, it
allocates `$ymm29` to that value and emits `c4 01 15 76 ed`, which is
`vpcmpeqd %ymm13,%ymm13,%ymm13`. `-code-model=large`, which llvmpipe uses, is
needed for the reduced file to fail.

## Fixed in LLVM 22

LLVM 22.1.8 and 23.1.3 compile both the reduced file and all four full
modules without a verifier error. They materialize the all-ones values in
ymm16-31 with `AVX512_256_SETALLONES`, which expands to EVEX `vpternlogd`
for those registers. That pseudo came from llvm/llvm-project#169009
(d14840779bf9, "[X86][AVX512] Add pseudos for `AVX512_*_SETALLONES`"), which
is in llvmorg-22.1.0 but not in 21.1.8. The patch applies cleanly to 21.1.8's
source; no patched 21.1.8 has been built to test it.

Any Mesa built against LLVM 21 for an AVX-512 host is affected. nixpkgs
builds Mesa with the default `llvmPackages`, which is `llvmPackages_21`.
