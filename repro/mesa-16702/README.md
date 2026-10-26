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
4. runs again with `GALLIUM_OVERRIDE_CPU_CAPS=avx` as a control.

`llc-check.sh <out-dir> [mcpu]` rebuilds the dumped `fs_variant` functions
offline with Mesa 26.2's pass pipeline and llc, for diffing against the JIT.
Add `-code-model=large` to match the JIT's code exactly.

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
