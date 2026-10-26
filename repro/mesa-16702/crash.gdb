set pagination off
handle SIGFPE stop print
run
info registers rax rdx rcx rsi rdi r8 r9 k0 k1 k2 k3 k4 k5 k6 k7
x/80i $pc-260
bt 8
