#define _GNU_SOURCE
#include <pthread.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <unistd.h>

/*
 * Every iteration writes a fresh counter into all 32 ymm registers, then
 * checks that ymm1-ymm31 still equal ymm16 (and ymm16 the counter), all in
 * asm with no calls in between; a raw sched_yield every 256 iterations and
 * many threads per CPU force preemption. A register that comes back with
 * an older value means the vector state was restored stale.
 * Build without AVX-512 (-mavx2): only this asm touches ymm16-ymm31 and
 * k1, so they need no clobbers.
 */

static long rounds = 50000000;

static void *worker(void *arg)
{
    (void)arg;
    uint32_t bad_at = 0;
    uint64_t left = (uint64_t)rounds;
    uint32_t stale = 0;
    __asm__ volatile(
        "1:\n"
        "mov %[left], %%rax\n"
        "vpbroadcastd %%eax, %%ymm16\n"
        "vpbroadcastd %%eax, %%ymm17\n"
        "vpbroadcastd %%eax, %%ymm18\n"
        "vpbroadcastd %%eax, %%ymm19\n"
        "vpbroadcastd %%eax, %%ymm20\n"
        "vpbroadcastd %%eax, %%ymm21\n"
        "vpbroadcastd %%eax, %%ymm22\n"
        "vpbroadcastd %%eax, %%ymm23\n"
        "vpbroadcastd %%eax, %%ymm24\n"
        "vpbroadcastd %%eax, %%ymm25\n"
        "vpbroadcastd %%eax, %%ymm26\n"
        "vpbroadcastd %%eax, %%ymm27\n"
        "vpbroadcastd %%eax, %%ymm28\n"
        "vpbroadcastd %%eax, %%ymm29\n"
        "vpbroadcastd %%eax, %%ymm30\n"
        "vpbroadcastd %%eax, %%ymm31\n"
        "vmovd %%eax, %%xmm0\n"
        "vpbroadcastd %%xmm0, %%ymm0\n"
        "vmovdqa %%ymm0, %%ymm1\n"
        "vmovdqa %%ymm0, %%ymm2\n"
        "vmovdqa %%ymm0, %%ymm3\n"
        "vmovdqa %%ymm0, %%ymm4\n"
        "vmovdqa %%ymm0, %%ymm5\n"
        "vmovdqa %%ymm0, %%ymm6\n"
        "vmovdqa %%ymm0, %%ymm7\n"
        "vmovdqa %%ymm0, %%ymm8\n"
        "vmovdqa %%ymm0, %%ymm9\n"
        "vmovdqa %%ymm0, %%ymm10\n"
        "vmovdqa %%ymm0, %%ymm11\n"
        "vmovdqa %%ymm0, %%ymm12\n"
        "vmovdqa %%ymm0, %%ymm13\n"
        "vmovdqa %%ymm0, %%ymm14\n"
        "vmovdqa %%ymm0, %%ymm15\n"
        "vpcmpeqd %%ymm0, %%ymm16, %%k1\n" "kortestb %%k1, %%k1\n" "jnc 9f\n"
        "vpcmpeqd %%ymm1, %%ymm16, %%k1\n" "kortestb %%k1, %%k1\n" "jnc 9f\n"
        "vpcmpeqd %%ymm2, %%ymm16, %%k1\n" "kortestb %%k1, %%k1\n" "jnc 9f\n"
        "vpcmpeqd %%ymm3, %%ymm16, %%k1\n" "kortestb %%k1, %%k1\n" "jnc 9f\n"
        "vpcmpeqd %%ymm4, %%ymm16, %%k1\n" "kortestb %%k1, %%k1\n" "jnc 9f\n"
        "vpcmpeqd %%ymm5, %%ymm16, %%k1\n" "kortestb %%k1, %%k1\n" "jnc 9f\n"
        "vpcmpeqd %%ymm6, %%ymm16, %%k1\n" "kortestb %%k1, %%k1\n" "jnc 9f\n"
        "vpcmpeqd %%ymm7, %%ymm16, %%k1\n" "kortestb %%k1, %%k1\n" "jnc 9f\n"
        "vpcmpeqd %%ymm8, %%ymm16, %%k1\n" "kortestb %%k1, %%k1\n" "jnc 9f\n"
        "vpcmpeqd %%ymm9, %%ymm16, %%k1\n" "kortestb %%k1, %%k1\n" "jnc 9f\n"
        "vpcmpeqd %%ymm10, %%ymm16, %%k1\n" "kortestb %%k1, %%k1\n" "jnc 9f\n"
        "vpcmpeqd %%ymm11, %%ymm16, %%k1\n" "kortestb %%k1, %%k1\n" "jnc 9f\n"
        "vpcmpeqd %%ymm12, %%ymm16, %%k1\n" "kortestb %%k1, %%k1\n" "jnc 9f\n"
        "vpcmpeqd %%ymm13, %%ymm16, %%k1\n" "kortestb %%k1, %%k1\n" "jnc 9f\n"
        "vpcmpeqd %%ymm14, %%ymm16, %%k1\n" "kortestb %%k1, %%k1\n" "jnc 9f\n"
        "vpcmpeqd %%ymm15, %%ymm16, %%k1\n" "kortestb %%k1, %%k1\n" "jnc 9f\n"
        "vpcmpeqd %%ymm17, %%ymm16, %%k1\n" "kortestb %%k1, %%k1\n" "jnc 9f\n"
        "vpcmpeqd %%ymm18, %%ymm16, %%k1\n" "kortestb %%k1, %%k1\n" "jnc 9f\n"
        "vpcmpeqd %%ymm19, %%ymm16, %%k1\n" "kortestb %%k1, %%k1\n" "jnc 9f\n"
        "vpcmpeqd %%ymm20, %%ymm16, %%k1\n" "kortestb %%k1, %%k1\n" "jnc 9f\n"
        "vpcmpeqd %%ymm21, %%ymm16, %%k1\n" "kortestb %%k1, %%k1\n" "jnc 9f\n"
        "vpcmpeqd %%ymm22, %%ymm16, %%k1\n" "kortestb %%k1, %%k1\n" "jnc 9f\n"
        "vpcmpeqd %%ymm23, %%ymm16, %%k1\n" "kortestb %%k1, %%k1\n" "jnc 9f\n"
        "vpcmpeqd %%ymm24, %%ymm16, %%k1\n" "kortestb %%k1, %%k1\n" "jnc 9f\n"
        "vpcmpeqd %%ymm25, %%ymm16, %%k1\n" "kortestb %%k1, %%k1\n" "jnc 9f\n"
        "vpcmpeqd %%ymm26, %%ymm16, %%k1\n" "kortestb %%k1, %%k1\n" "jnc 9f\n"
        "vpcmpeqd %%ymm27, %%ymm16, %%k1\n" "kortestb %%k1, %%k1\n" "jnc 9f\n"
        "vpcmpeqd %%ymm28, %%ymm16, %%k1\n" "kortestb %%k1, %%k1\n" "jnc 9f\n"
        "vpcmpeqd %%ymm29, %%ymm16, %%k1\n" "kortestb %%k1, %%k1\n" "jnc 9f\n"
        "vpcmpeqd %%ymm30, %%ymm16, %%k1\n" "kortestb %%k1, %%k1\n" "jnc 9f\n"
        "vpcmpeqd %%ymm31, %%ymm16, %%k1\n" "kortestb %%k1, %%k1\n" "jnc 9f\n"
        "vmovd %%xmm16, %%ecx\n"
        "cmp %%eax, %%ecx\n"
        "jne 9f\n"
        "test $255, %%eax\n"
        "jnz 2f\n"
        "mov $24, %%eax\n"
        "syscall\n"
        "2:\n"
        "decq %[left]\n"
        "jnz 1b\n"
        "jmp 8f\n"
        "9:\n"
        "mov %%eax, %[bad]\n"
        "vmovd %%xmm16, %[stale]\n"
        "8:\n"
        : [left] "+m"(left), [bad] "=m"(bad_at), [stale] "=m"(stale)
        :
        : "rax", "rcx", "r11", "memory", "cc",
          "xmm0", "xmm1", "xmm2", "xmm3", "xmm4", "xmm5", "xmm6", "xmm7",
          "xmm8", "xmm9", "xmm10", "xmm11", "xmm12", "xmm13", "xmm14", "xmm15");
    return (void *)(uintptr_t)(left != 0);
}

int main(int argc, char **argv)
{
    long threads = 4 * sysconf(_SC_NPROCESSORS_ONLN);
    if (argc > 1)
        rounds = atol(argv[1]);
    if (threads > 256)
        threads = 256;
    pthread_t t[256];
    for (long i = 0; i < threads; i++)
        pthread_create(&t[i], NULL, worker, NULL);
    long bad = 0;
    for (long i = 0; i < threads; i++) {
        void *r;
        pthread_join(t[i], &r);
        bad += (long)(uintptr_t)r;
    }
    printf("hi16: %ld threads x %ld rounds, %ld threads saw a vector register come back stale\n", threads, rounds, bad);
    return bad != 0;
}
