#include <stdlib.h>
#include <string.h>

/*
 * LD_PRELOAD shim: llvmpipe asks LLVM which CPU to compile for through
 * llvm::sys::getHostCPUName(). This answers $LYS_FAKE_CPU instead, so the
 * same host can run code tuned for another CPU (llvm::StringRef is a
 * pointer and a length, returned in rax:rdx).
 */

struct string_ref {
    const char *data;
    size_t length;
};

struct string_ref _ZN4llvm3sys14getHostCPUNameEv(void)
{
    const char *cpu = getenv("LYS_FAKE_CPU");
    if (cpu == NULL || *cpu == '\0')
        abort();
    return (struct string_ref){cpu, strlen(cpu)};
}
