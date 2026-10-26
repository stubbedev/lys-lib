#define _GNU_SOURCE
#include <pthread.h>
#include <sched.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>

/*
 * Checks that ymm16..ymm31, the registers only EVEX (AVX-512VL) code can
 * use, keep their values while threads are preempted and rescheduled.
 * The C code is built without AVX-512, so only the asm touches them.
 */

#define REGS 16
#define LANES 8

static long rounds = 2000000;


static void load(const uint32_t *src)
{
    __asm__ volatile(
        "vmovdqu32 0*32(%0), %%ymm16\n" "vmovdqu32 1*32(%0), %%ymm17\n"
        "vmovdqu32 2*32(%0), %%ymm18\n" "vmovdqu32 3*32(%0), %%ymm19\n"
        "vmovdqu32 4*32(%0), %%ymm20\n" "vmovdqu32 5*32(%0), %%ymm21\n"
        "vmovdqu32 6*32(%0), %%ymm22\n" "vmovdqu32 7*32(%0), %%ymm23\n"
        "vmovdqu32 8*32(%0), %%ymm24\n" "vmovdqu32 9*32(%0), %%ymm25\n"
        "vmovdqu32 10*32(%0), %%ymm26\n" "vmovdqu32 11*32(%0), %%ymm27\n"
        "vmovdqu32 12*32(%0), %%ymm28\n" "vmovdqu32 13*32(%0), %%ymm29\n"
        "vmovdqu32 14*32(%0), %%ymm30\n" "vmovdqu32 15*32(%0), %%ymm31\n"
        :: "r"(src) : "memory");
}

static void store(uint32_t *dst)
{
    __asm__ volatile(
        "vmovdqu32 %%ymm16, 0*32(%0)\n" "vmovdqu32 %%ymm17, 1*32(%0)\n"
        "vmovdqu32 %%ymm18, 2*32(%0)\n" "vmovdqu32 %%ymm19, 3*32(%0)\n"
        "vmovdqu32 %%ymm20, 4*32(%0)\n" "vmovdqu32 %%ymm21, 5*32(%0)\n"
        "vmovdqu32 %%ymm22, 6*32(%0)\n" "vmovdqu32 %%ymm23, 7*32(%0)\n"
        "vmovdqu32 %%ymm24, 8*32(%0)\n" "vmovdqu32 %%ymm25, 9*32(%0)\n"
        "vmovdqu32 %%ymm26, 10*32(%0)\n" "vmovdqu32 %%ymm27, 11*32(%0)\n"
        "vmovdqu32 %%ymm28, 12*32(%0)\n" "vmovdqu32 %%ymm29, 13*32(%0)\n"
        "vmovdqu32 %%ymm30, 14*32(%0)\n" "vmovdqu32 %%ymm31, 15*32(%0)\n"
        :: "r"(dst) : "memory");
}

static void *worker(void *arg)
{
    long id = (long)arg;
    uint32_t want[REGS * LANES], got[REGS * LANES];
    long bad = 0;
    for (int i = 0; i < REGS * LANES; i++)
        want[i] = 0x9e3779b9u * (uint32_t)(id * 131 + i + 1);
    load(want);
    for (long r = 0; r < rounds; r++) {
        if (r % 64 == 0)
            sched_yield();
        store(got);
        if (memcmp(got, want, sizeof want) != 0) {
            for (int g = 0; g < REGS; g++)
                for (int l = 0; l < LANES; l++)
                    if (got[g * LANES + l] != want[g * LANES + l] && bad < 8)
                        fprintf(stderr, "thread %ld round %ld: ymm%d lane %d is %08x, want %08x\n",
                                id, r, 16 + g, l, got[g * LANES + l], want[g * LANES + l]);
            bad++;
            load(want);
        }
    }
    return (void *)bad;
}

int main(int argc, char **argv)
{
    long threads = 4 * sysconf(_SC_NPROCESSORS_ONLN);
    if (argc > 1)
        rounds = atol(argv[1]);
    pthread_t t[256];
    if (threads > 256)
        threads = 256;
    for (long i = 0; i < threads; i++)
        pthread_create(&t[i], NULL, worker, (void *)i);
    long bad = 0;
    for (long i = 0; i < threads; i++) {
        void *r;
        pthread_join(t[i], &r);
        bad += (long)r;
    }
    printf("hi16: %ld threads x %ld rounds, %ld rounds saw ymm16-31 change\n", threads, rounds, bad);
    return bad != 0;
}
