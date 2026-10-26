#define _GNU_SOURCE
#include <dlfcn.h>
#include <stdatomic.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

/*
 * LD_PRELOAD shim: llvmpipe hands every module to LLVM through
 * LLVMRunPasses, first with "default<O0>". This writes each module's IR to
 * $LYS_IR_DIR/module-NNNN-<identifier>.ll at that first call, before any
 * optimization, then runs the real LLVMRunPasses. It sees modules that
 * GALLIVM_DEBUG=ir never prints. libLLVM comes in with the Vulkan driver,
 * outside the global scope RTLD_NEXT searches, so it is looked up by name.
 */

typedef void *module_ref;
typedef void *error_ref;

static atomic_int counter;

static void *llvm(const char *name)
{
    void *lib = dlopen("libLLVM.so.21.1", RTLD_NOW | RTLD_NOLOAD);
    void *fn = lib ? dlvsym(lib, name, "LLVM_21.1") : NULL;
    if (fn == NULL) {
        fprintf(stderr, "irdump: %s@LLVM_21.1 not found in the loaded libLLVM.so.21.1\n", name);
        abort();
    }
    return fn;
}

error_ref LLVMRunPasses(module_ref m, const char *passes, void *tm, void *opts)
{
    error_ref (*run)(module_ref, const char *, void *, void *) = llvm("LLVMRunPasses");
    const char *dir = getenv("LYS_IR_DIR");
    if (dir != NULL && strcmp(passes, "default<O0>") == 0) {
        const char *(*ident)(module_ref, size_t *) = llvm("LLVMGetModuleIdentifier");
        int (*print)(module_ref, const char *, char **) = llvm("LLVMPrintModuleToFile");
        size_t len = 0;
        const char *id = ident(m, &len);
        char path[4096];
        snprintf(path, sizeof path, "%s/module-%04d-%.*s.ll", dir, atomic_fetch_add(&counter, 1), (int)(len > 64 ? 64 : len), id);
        for (char *c = path + strlen(dir) + 1; *c; c++)
            if (*c == '/')
                *c = '_';
        char *err = NULL;
        if (print(m, path, &err) != 0) {
            fprintf(stderr, "irdump: write %s: %s\n", path, err ? err : "?");
            abort();
        }
    }
    return run(m, passes, tm, opts);
}
