#include "quickjs.h"
#include <stdint.h>
#include <stdlib.h>
#include <string.h>
#include <wasi/api.h>

#define EXPORT(name) __attribute__((export_name(name)))
#define MAX_OUTPUT (32u * 1024u * 1024u)
#define MAX_LOG 16384u
#define MAX_ERROR 4096u

static const char *output;
static uint32_t output_length;
static char error_text[MAX_ERROR];
static char logs[MAX_LOG];
static uint32_t logs_length;
static uint32_t polls;
static int exhausted;

__wasi_errno_t __wrap___wasi_clock_time_get(__wasi_clockid_t id, __wasi_timestamp_t precision, __wasi_timestamp_t *time) {
    *time = UINT64_C(1704067200000000000);
    return 0;
}
__wasi_errno_t __wrap___wasi_fd_close(__wasi_fd_t fd) { return __WASI_ERRNO_BADF; }
__wasi_errno_t __wrap___wasi_fd_seek(__wasi_fd_t fd, __wasi_filedelta_t offset, __wasi_whence_t whence, __wasi_filesize_t *new_offset) { return __WASI_ERRNO_BADF; }
__wasi_errno_t __wrap___wasi_fd_write(__wasi_fd_t fd, const __wasi_ciovec_t *iovs, size_t n, __wasi_size_t *written) { return __WASI_ERRNO_BADF; }
__wasi_errno_t __wrap___wasi_fd_fdstat_get(__wasi_fd_t fd, __wasi_fdstat_t *stat) { return __WASI_ERRNO_BADF; }
__wasi_errno_t __wrap___wasi_environ_sizes_get(__wasi_size_t *n, __wasi_size_t *size) { *n = 0; *size = 0; return 0; }
__wasi_errno_t __wrap___wasi_environ_get(uint8_t **env, uint8_t *buffer) { return 0; }
_Noreturn void __wrap___wasi_proc_exit(__wasi_exitcode_t status) { __builtin_trap(); }

static int interrupt(JSRuntime *runtime, void *opaque) {
    if (++polls > 10000) exhausted = 1;
    return exhausted;
}

static void fail_text(const char *text) {
    size_t n = strlen(text);
    if (n >= MAX_ERROR) n = MAX_ERROR - 1;
    memcpy(error_text, text, n);
    error_text[n] = 0;
    output = error_text;
    output_length = n;
}

static void fail_exception(JSContext *ctx) {
    if (exhausted) { fail_text("Script instruction budget exceeded"); return; }
    JSValue exception = JS_GetException(ctx);
    const char *text = JS_ToCString(ctx, exception);
    fail_text(text ? text : "Script evaluation failed or exceeded memory budget");
    if (text) JS_FreeCString(ctx, text);
    JS_FreeValue(ctx, exception);
}

static JSValue console_write(JSContext *ctx, JSValueConst self, int argc, JSValueConst *argv) {
    for (int i = 0; i < argc; i++) {
        JSValue json = JS_UNDEFINED;
        JSValueConst value = argv[i];
        if (!JS_IsString(value)) {
            json = JS_JSONStringify(ctx, value, JS_UNDEFINED, JS_UNDEFINED);
            if (JS_IsException(json)) { JS_FreeValue(ctx, JS_GetException(ctx)); json = JS_UNDEFINED; }
            if (JS_IsUndefined(json)) json = JS_NewString(ctx, "[unserializable]");
            value = json;
        }
        size_t n;
        const char *text = JS_ToCStringLen(ctx, &n, value);
        JS_FreeValue(ctx, json);
        if (!text) return JS_EXCEPTION;
        if (n + 1 > MAX_LOG - logs_length) {
            JS_FreeCString(ctx, text);
            return JS_ThrowRangeError(ctx, "Script console output limit exceeded");
        }
        memcpy(logs + logs_length, text, n);
        logs_length += n;
        logs[logs_length++] = i + 1 == argc ? '\n' : ' ';
        JS_FreeCString(ctx, text);
    }
    return JS_UNDEFINED;
}

EXPORT("allocate") uint32_t allocate(uint32_t length) {
    if (length > MAX_OUTPUT) return 0;
    return (uint32_t)(uintptr_t)calloc(1, (size_t)length + 1);
}
EXPORT("output_pointer") uint32_t output_pointer(void) { return (uint32_t)(uintptr_t)output; }
EXPORT("output_length") uint32_t result_length(void) { return output_length; }
EXPORT("logs_pointer") uint32_t logs_pointer(void) { return (uint32_t)(uintptr_t)logs; }
EXPORT("logs_length") uint32_t log_length(void) { return logs_length; }
EXPORT("evaluate") int evaluate(const char *script, uint32_t script_length, const char *config, uint32_t config_length, const char *name, uint32_t name_length) {
    if (script_length > MAX_OUTPUT || config_length > MAX_OUTPUT || name_length > 4096) { fail_text("Input limit exceeded"); return 1; }
    JSRuntime *rt = JS_NewRuntime();
    if (!rt) { fail_text("Memory budget exceeded"); return 1; }
    JS_SetMemoryLimit(rt, 192u * 1024u * 1024u);
    JS_SetMaxStackSize(rt, 512u * 1024u);
    JS_SetInterruptHandler(rt, interrupt, NULL);
    JSContext *ctx = JS_NewContext(rt);
    if (!ctx) { fail_text("Memory budget exceeded"); return 1; }
    const char *levels[] = {"log", "info", "warn", "error", "debug", "trace"};
    JSValue console = JS_NewObject(ctx);
    for (int i = 0; i < 6; i++) {
        if (JS_SetPropertyStr(ctx, console, levels[i], JS_NewCFunction(ctx, console_write, levels[i], 1)) < 0) goto fail;
    }
    JSValue global = JS_GetGlobalObject(ctx);
    if (JS_SetPropertyStr(ctx, global, "console", console) < 0) goto fail;
    JS_FreeValue(ctx, global);
    JSValue compiled = JS_Eval(ctx, script, script_length, "profile-script.js", JS_EVAL_TYPE_GLOBAL);
    if (JS_IsException(compiled) || exhausted) goto fail;
    JS_FreeValue(ctx, compiled);
    JSValue main = JS_Eval(ctx, "main", 4, "entry.js", JS_EVAL_TYPE_GLOBAL);
    if (JS_IsException(main) || !JS_IsFunction(ctx, main)) { fail_text("Script does not define main()"); return 1; }
    JSValue args[2] = {JS_ParseJSON(ctx, config, config_length, "profile.json"), JS_NewStringLen(ctx, name, name_length)};
    if (JS_IsException(args[0]) || JS_IsException(args[1])) goto fail;
    JSValue result = JS_Call(ctx, main, JS_UNDEFINED, 2, args);
    if (JS_IsException(result) || exhausted) goto fail;
    if (JS_IsPromise(result)) {
        while (JS_PromiseState(ctx, result) == JS_PROMISE_PENDING) {
            JSContext *job = ctx;
            int pending = JS_ExecutePendingJob(rt, &job);
            if (exhausted) goto fail;
            if (pending < 0) { fail_exception(job); return 1; }
            if (pending == 0) { fail_text("main() returned a Promise that did not settle"); return 1; }
        }
        if (JS_PromiseState(ctx, result) == JS_PROMISE_REJECTED) {
            JS_Throw(ctx, JS_PromiseResult(ctx, result));
            goto fail;
        }
        result = JS_PromiseResult(ctx, result);
    }
    if (JS_IsNull(result) || JS_IsUndefined(result)) {
        output = config;
        output_length = config_length;
        return 0;
    }
    JSValue json = JS_JSONStringify(ctx, result, JS_UNDEFINED, JS_UNDEFINED);
    if (JS_IsException(json) || exhausted) goto fail;
    if (JS_IsUndefined(json)) { fail_text("main() returned a value that is not JSON"); return 1; }
    size_t n;
    output = JS_ToCStringLen(ctx, &n, json);
    if (!output) goto fail;
    if (n > MAX_OUTPUT) { fail_text("Script output limit exceeded"); return 2; }
    output_length = n;
    return 0;
fail:
    fail_exception(ctx);
    return 1;
}
