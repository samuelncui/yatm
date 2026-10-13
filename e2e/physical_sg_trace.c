/* Test-only Linux SG v3 observer. Build on the LTFS host:
 * cc -std=c11 -O2 -Wall -Wextra -Werror -shared -fPIC -o physical_sg_trace.so \
 *    physical_sg_trace.c -ldl -pthread
 * Set YATM_SG_TRACE=/absolute/new.jsonl on that LTFS process only. Optional
 * YATM_SG_TRACE_POSITION=1 inserts short READ POSITION after clean page 31.
 * The harness must require the init record and reject any <log>.error file,
 * missing/truncated records or loader errors. Run foreground, retain stderr,
 * and keep evidence on a local filesystem outside the LTFS mount.
 *
 * Lock evidence, LTFS 4c0e6faae661e0bfd50bc9707f59ecd88c90b7eb:
 * src/libltfs/xattr.c:829-850 -> ltfs_logpage, ltfs.c:4349-4357 takes
 * tape_device_lock around tape_logsense. tape.c:531-554 owns backend_mutex.
 * sg_scsi_tape.c:187-202 calls synchronous ioctl before returning to that lock.
 * This hook inherits that lock; it neither obtains nor releases an LTFS lock.
 * Scope: the verified mounted Linux SG owner, not arbitrary SG callers.
 *
 * HP LTO-5 Host Interface Guide, April 2011, pp.148-150: opcode 34h, service
 * action 00h, CDB allocation length zero, 20 response bytes. LOCU/BYCU mean
 * UNKNOWN, not zero occupancy; EOP is EW reached, not a prediction. A command
 * can consume deferred sense even without motion: injected errors invalidate
 * the experiment and disable further injection. Never inject REQUEST SENSE.
 * https://docs.oracle.com/cd/E21419_04/en/LTO5_Vol3_E5b/LTO5_Vol3_E5b.pdf
 */
#define _GNU_SOURCE
#include <dlfcn.h>
#include <errno.h>
#include <fcntl.h>
#include <limits.h>
#include <pthread.h>
#include <scsi/sg.h>
#include <stdarg.h>
#include <stdatomic.h>
#include <stdbool.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/ioctl.h>
#include <sys/syscall.h>
#include <time.h>
#include <unistd.h>

#define DATA_LIMIT 4096
#define NS_PER_SECOND UINT64_C(1000000000)

static int (*next_ioctl)(int, unsigned long, ...);
static int trace_fd = -1;
static char error_path[PATH_MAX];
static bool position_enabled;
static atomic_bool failed;
static atomic_uint_fast64_t next_position_ns;
static pthread_mutex_t log_mutex = PTHREAD_MUTEX_INITIALIZER;
static _Thread_local bool in_hook;

static void fail_trace(const char *reason)
{
    int saved = errno;
    if (!atomic_exchange(&failed, true)) {
        int fd = error_path[0] ? open(error_path, O_WRONLY | O_CREAT | O_EXCL | O_CLOEXEC, 0600) : -1;
        if (fd >= 0) {
            /* Presence alone signals failure, even if this write also fails. */
            (void)write(fd, reason, strlen(reason));
            (void)close(fd);
        }
        (void)write(STDERR_FILENO, "YATM SG trace failure: ", 23);
        (void)write(STDERR_FILENO, reason, strlen(reason));
    }
    errno = saved;
}

static uint64_t now_ns(clockid_t clock)
{
    struct timespec ts;
    if (clock_gettime(clock, &ts)) {
        fail_trace("clock_gettime failed\n");
        return 0;
    }
    return (uint64_t)ts.tv_sec * NS_PER_SECOND + (uint64_t)ts.tv_nsec;
}

static void emit(const char *line, size_t length)
{
    pthread_mutex_lock(&log_mutex);
    size_t offset = 0;
    while (offset < length) {
        ssize_t n = write(trace_fd, line + offset, length - offset);
        if (n < 0 && errno == EINTR)
            continue;
        if (n <= 0) {
            fail_trace("trace write failed; evidence incomplete\n");
            break;
        }
        offset += (size_t)n;
    }
    pthread_mutex_unlock(&log_mutex);
}

__attribute__((constructor)) static void initialize(void)
{
    int saved = errno;
    next_ioctl = (int (*)(int, unsigned long, ...))dlsym(RTLD_NEXT, "ioctl");
    const char *path = getenv("YATM_SG_TRACE");
    if (!path || !*path) {
        errno = saved;
        return;
    }
    if (path[0] != '/' || snprintf(error_path, sizeof(error_path), "%s.error", path) >= (int)sizeof(error_path)) {
        error_path[0] = '\0';
        fail_trace("trace path must be absolute and fit PATH_MAX with .error suffix\n");
        errno = saved;
        return;
    }
    if (!next_ioctl) {
        fail_trace("RTLD_NEXT ioctl unavailable\n");
        errno = saved;
        return;
    }
    trace_fd = open(path, O_WRONLY | O_CREAT | O_EXCL | O_CLOEXEC, 0600);
    if (trace_fd < 0) {
        fail_trace("exclusive trace creation failed\n");
        errno = saved;
        return;
    }
    if (access(error_path, F_OK) == 0)
        fail_trace("existing error marker; use a new trace path\n");
    const char *position = getenv("YATM_SG_TRACE_POSITION");
    position_enabled = position && !strcmp(position, "1");
    if (position && strcmp(position, "1") && strcmp(position, "0"))
        fail_trace("YATM_SG_TRACE_POSITION must be 0 or 1\n");
    char line[512];
    int n = snprintf(line, sizeof(line),
        "{\"event\":\"init\",\"schema_version\":1,\"pid\":%ld,\"tid\":%ld,"
        "\"request_start_ns\":%llu,\"sg_io_hdr_size\":%zu,\"data_limit\":%d,"
        "\"position_enabled\":%s,\"ready\":%s}\n",
        (long)getpid(), (long)syscall(SYS_gettid), (unsigned long long)now_ns(CLOCK_REALTIME),
        sizeof(sg_io_hdr_t), DATA_LIMIT, position_enabled ? "true" : "false",
        atomic_load(&failed) ? "false" : "true");
    if (n > 0 && n < (int)sizeof(line))
        emit(line, (size_t)n);
    else
        fail_trace("init record overflow\n");
    errno = saved;
}

static void hex(char *out, const unsigned char *in, size_t length)
{
    static const char digits[] = "0123456789abcdef";
    for (size_t i = 0; i < length; ++i) {
        out[2 * i] = digits[in[i] >> 4];
        out[2 * i + 1] = digits[in[i] & 15];
    }
    out[2 * length] = '\0';
}

static bool clean(const sg_io_hdr_t *r, int rc)
{
    return rc == 0 && !r->status && !r->masked_status && !r->msg_status &&
        !r->host_status && !r->driver_status && !r->sb_len_wr && !(r->info & SG_INFO_OK_MASK);
}

/* Unknown flags, mmap and iovecs are passed through but never dereferenced as
 * flat response buffers. Use the host's native header, never a packed copy.
 */
static bool flat_response(const sg_io_hdr_t *r)
{
    return r->dxfer_direction == SG_DXFER_FROM_DEV && !r->iovec_count &&
        !(r->flags & ~(unsigned int)(SG_FLAG_DIRECT_IO | 0x10 | 0x20)) && r->dxferp;
}

static int observe(int fd, sg_io_hdr_t *r, bool injected)
{
    int entry_errno = errno;
    sg_io_hdr_t before = *r;
    unsigned char cdb[255];
    size_t cdb_length = before.cmdp ? before.cmd_len : 0;
    if (cdb_length)
        memcpy(cdb, before.cmdp, cdb_length);
    uint64_t start = now_ns(CLOCK_REALTIME), mono_start = now_ns(CLOCK_MONOTONIC);
    errno = entry_errno;
    int rc = next_ioctl(fd, SG_IO, r);
    int result_errno = errno;
    uint64_t mono_end = now_ns(CLOCK_MONOTONIC), end = now_ns(CLOCK_REALTIME);
    size_t sense_length = r->sb_len_wr < before.mx_sb_len ? r->sb_len_wr : before.mx_sb_len;
    if (!before.sbp)
        sense_length = 0;
    size_t available = 0, response_length = 0, data_length = 0;
    if (rc == 0 && flat_response(&before) && r->resid >= 0 && (unsigned int)r->resid <= before.dxfer_len &&
        cdb_length && (cdb[0] == 0x4d || cdb[0] == 0x34)) {
        available = before.dxfer_len - (unsigned int)r->resid;
        response_length = available;
        if (cdb[0] == 0x4d && available >= 4) {
            const unsigned char *data = before.dxferp;
            size_t declared = 4U + data[2] * 256U + data[3];
            if (declared < response_length)
                response_length = declared;
        }
        data_length = response_length < DATA_LIMIT ? response_length : DATA_LIMIT;
    }
    char cdb_hex[511], sense_hex[511], data_hex[DATA_LIMIT * 2 + 1], line[11000];
    hex(cdb_hex, cdb, cdb_length);
    hex(sense_hex, before.sbp, sense_length);
    hex(data_hex, before.dxferp, data_length);
    int n = snprintf(line, sizeof(line),
        "{\"event\":\"command\",\"request_start_ns\":%llu,\"request_end_ns\":%llu,"
        "\"monotonic_start_ns\":%llu,\"monotonic_end_ns\":%llu,\"cdb\":\"%s\","
        "\"rc\":%d,\"errno\":%d,\"status\":%u,\"masked_status\":%u,\"msg_status\":%u,"
        "\"host_status\":%u,\"driver_status\":%u,\"resid\":%d,\"dxfer_len\":%u,"
        "\"direction\":%d,\"sense\":\"%s\",\"data\":\"%s\",\"injected\":%s,"
        "\"pid\":%ld,\"tid\":%ld,\"fd\":%d,\"flags\":%u,\"info\":%u,"
        "\"duration_ms\":%u,\"timeout_ms\":%u,\"sb_len_wr\":%u,\"iovec_count\":%u,"
        "\"data_truncated\":%s,\"outputs_valid\":%s}\n",
        (unsigned long long)start, (unsigned long long)end,
        (unsigned long long)mono_start, (unsigned long long)mono_end, cdb_hex,
        rc, result_errno, r->status, r->masked_status, r->msg_status, r->host_status,
        r->driver_status, r->resid, before.dxfer_len, before.dxfer_direction, sense_hex, data_hex,
        injected ? "true" : "false", (long)getpid(), (long)syscall(SYS_gettid), fd,
        before.flags, r->info, r->duration, before.timeout, r->sb_len_wr, before.iovec_count,
        response_length > data_length ? "true" : "false", rc >= 0 ? "true" : "false");
    if (n > 0 && n < (int)sizeof(line))
        emit(line, (size_t)n);
    else
        fail_trace("command record overflow\n");

    /* Same thread and fd, before the original owner call and its lock return.
     * No WRITE/error/REQUEST SENSE interception is used to trigger commands.
     */
    if (!injected && position_enabled && !atomic_load(&failed) && clean(r, rc) &&
        cdb_length == 10 && cdb[0] == 0x4d && (cdb[2] & 0x3f) == 0x31 && !cdb[3] &&
        available >= 4 && ((unsigned char *)before.dxferp)[0] == 0x31 &&
        ((unsigned char *)before.dxferp)[1] == 0 &&
        4U + ((unsigned char *)before.dxferp)[2] * 256U + ((unsigned char *)before.dxferp)[3] <= available) {
        uint64_t due = atomic_load(&next_position_ns);
        uint64_t now = now_ns(CLOCK_MONOTONIC);
        if (now >= due && atomic_compare_exchange_strong(&next_position_ns, &due, now + NS_PER_SECOND)) {
            unsigned char position_cdb[10] = {0x34}, position_data[20] = {0}, position_sense[255] = {0};
            sg_io_hdr_t position = {0};
            position.interface_id = 'S';
            position.cmd_len = sizeof(position_cdb);
            position.cmdp = position_cdb;
            position.dxfer_direction = SG_DXFER_FROM_DEV;
            position.dxfer_len = sizeof(position_data);
            position.dxferp = position_data;
            position.mx_sb_len = sizeof(position_sense);
            position.sbp = position_sense;
            position.timeout = 60000; /* milliseconds, not the sampling interval */
            int position_rc = observe(fd, &position, true);
            /* Bound from completion as well, so slow probes cannot cause a
             * catch-up burst or two starts less than a second apart. */
            atomic_store(&next_position_ns, now_ns(CLOCK_MONOTONIC) + NS_PER_SECOND);
            if (!clean(&position, position_rc) || position.resid != 0)
                fail_trace("injected READ POSITION failed; further injection disabled\n");
        }
    }
    errno = result_errno;
    return rc;
}

int ioctl(int fd, unsigned long request, ...)
{
    int saved = errno;
    va_list args;
    va_start(args, request);
    /* Linux libc ioctl forwards one machine-word vararg, including legacy
     * requests without _IOC size bits. Do not guess arity from _IOC_SIZE.
     * This follows the native Linux ABI; it is not a portable varargs wrapper.
     */
    void *arg = va_arg(args, void *);
    va_end(args);
    if (!next_ioctl) {
        fail_trace("ioctl resolver unavailable\n");
        _exit(127);
    }
    if (in_hook || trace_fd < 0 || request != SG_IO) {
        errno = saved;
        return next_ioctl(fd, request, arg);
    }
    in_hook = true;
    int guard = 0;
    if (arg)
        memcpy(&guard, arg, sizeof(guard));
    if (guard != 'S') {
        fail_trace("unsupported SG_IO ABI; command not decoded\n");
        in_hook = false;
        errno = saved;
        return next_ioctl(fd, request, arg);
    }
    errno = saved;
    int rc = observe(fd, arg, false);
    int result_errno = errno;
    in_hook = false;
    errno = result_errno;
    return rc;
}
