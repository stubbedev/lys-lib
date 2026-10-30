#!/usr/bin/env bash
# Compares the JIT code gdb saved around the fault (fault-window.bin, from
# pc-0x18000) with llc-check.sh's rebuild of each fs_variant function,
# instruction by instruction at the same offset into the function. The
# function is taken to start at the start of the executable mapping that
# holds the pc, as llvmpipe's memory manager gives each module its own
# code pages. Run llc-check.sh on the same out-dir first.
# Usage: repro/mesa-16702/fault-check.sh <out-dir> [mcpu, default znver4]
set -euo pipefail

out=$(realpath "${1:?usage: fault-check.sh <out-dir> [mcpu]}")
mcpu=${2:-znver4}
root=$(git rev-parse --show-toplevel)
binutils=$(nix build --inputs-from "$root" --no-link --print-out-paths 'nixpkgs#binutils-unwrapped^out')

pc=$(sed -n 's/^[$]1 = \(0x[0-9a-f]*\)$/\1/p' "$out/gdb.txt")
[[ -n "$pc" ]] || { echo "fault-check: no faulting pc in $out/gdb.txt"; exit 1; }
map=$(awk -v pc="$pc" '$5 ~ /x/ && strtonum($1) <= strtonum(pc) && strtonum(pc) < strtonum($2) { print $1; exit }' "$out/gdb.txt")
[[ -n "$map" ]] || { echo "fault-check: no executable mapping holds $pc"; exit 1; }
window=$((pc - 0x18000 - map))
end=$((window + $(stat -c %s "$out/fault-window.bin") - 16))
printf 'fault at %s, offset 0x%x into the function at %s; window from offset 0x%x\n' "$pc" $((pc - map)) "$map" "$window"

"$binutils/bin/objdump" -D -b binary -m i386:x86-64 --no-show-raw-insn --adjust-vma="$window" "$out/fault-window.bin" |
    sed -n 's/^ *\([0-9a-f]*\):\t\(.*\)$/\1\t\2/p' > "$out/fault-window.dis"

for dis in "$out"/llc/fs*."$mcpu".dis; do
    sed -n 's/^ *\([0-9a-f]*\):\t\(.*\)$/\1\t\2/p' "$dis" |
        sed 's/\b\(j[a-z]*\|call\) *\([0-9a-f]*\) <[^>]*>/\1 0x\2/' |
        awk -F'\t' -v from=$((window + 0x40)) -v to="$end" -v name="${dis##*/}" '
            function norm(s) { gsub(/[ \t]+/, " ", s); sub(/ *#.*/, "", s); return s }
            NR == FNR { fw[$1] = norm($2); next }
            strtonum("0x" $1) >= from && strtonum("0x" $1) < to && ($1 in fw) {
                if (fw[$1] ~ /nop/ && $2 ~ /nop/) next
                same += norm($2) == fw[$1]
                total++
                if (norm($2) != fw[$1] && fw[$1] !~ /^movabs/) diff++
            }
            END { printf "%s: %d of %d instructions identical, %d differ beyond movabs immediates\n", name, same, total, diff }
        ' "$out/fault-window.dis" -
done
