set pagination off
handle SIGFPE stop print
run
info registers rax rdx rcx rsi rdi r8 r9 k0 k1 k2 k3 k4 k5 k6 k7
x/80i $pc-260
info registers ymm0 ymm1 ymm2 ymm3 ymm4 ymm5 ymm6 ymm7 ymm8 ymm9 ymm10 ymm11 ymm12 ymm13 ymm14 ymm15 ymm16 ymm17 ymm18 ymm19 ymm20 ymm21 ymm22 ymm23 ymm24 ymm25 ymm26 ymm27 ymm28 ymm29 ymm30 ymm31
bt 8
