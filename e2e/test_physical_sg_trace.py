"""Native Linux fake-ioctl chain; no tape/device opens, network or LTFS needed.

Run: python3 -B -m unittest discover -s e2e -p test_physical_sg_trace.py -v
Builds the observer, fake next LD_PRELOAD library and caller in a temporary
directory with the host C compiler. A non-Linux run explicitly skips this suite.
The fake checks SG request buffers and errno independently of JSON assertions.
"""

import json
import os
from pathlib import Path
import shlex
import subprocess
import sys
import tempfile
import unittest


STUB = r'''
#define _GNU_SOURCE
#include <assert.h>
#include <dlfcn.h>
#include <errno.h>
#include <scsi/sg.h>
#include <stdarg.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/ioctl.h>
#include <sys/syscall.h>
#include <time.h>
#include <unistd.h>

#ifndef PROBE
static int mode, fail_write;
static uint64_t tick = 1000000000ULL;
void stub_mode(int value) { mode = value; }
void stub_time(uint64_t value) { tick = value; }
int clock_gettime(clockid_t clock, struct timespec *ts) {
    uint64_t value = tick + (clock == CLOCK_REALTIME ? 1700000000000000000ULL : 0);
    ts->tv_sec = value / 1000000000ULL;
    ts->tv_nsec = value % 1000000000ULL;
    errno = ERANGE; /* Deliberately clobber errno in observer bookkeeping. */
    return 0;
}
ssize_t write(int fd, const void *buf, size_t size) {
    if (fail_write && fd > 2) { errno = ENOSPC; return -1; }
    return syscall(SYS_write, fd, buf, size);
}
int ioctl(int fd, unsigned long request, ...) {
    va_list args;
    va_start(args, request);
    void *arg = va_arg(args, void *);
    va_end(args);
    if (request != SG_IO) { assert(errno == EDOM); assert(arg == (void *)123); errno = EBUSY; return 17; }
    sg_io_hdr_t *r = arg;
    if (r->interface_id != 'S') { assert(errno == EDOM); errno = ENOTTY; return -1; }
    if (r->cmdp[0] != 0x34) assert(errno == EDOM); /* Original entry errno is preserved. */
    printf("call %02x %d\n", r->cmdp[0], fd);
    tick += 1000000;
    if (r->cmdp[0] == 0x34) {
        assert(fd == 71 && r->cmd_len == 10 && r->dxfer_len == 20);
        assert(r->dxfer_direction == SG_DXFER_FROM_DEV && r->timeout == 60000);
        assert(!r->flags && !r->iovec_count);
        for (int i = 1; i < 10; ++i) assert(!r->cmdp[i]);
        memset(r->dxferp, 0, 20);
        ((unsigned char *)r->dxferp)[0] = 0x30; /* LOCU/BYCU: unknown occupancy. */
        ((unsigned char *)r->dxferp)[7] = 42;
        r->duration = 1;
        if (mode == 9) { r->status = 2; r->host_status = 3; errno = EIO; return -1; }
        errno = E2BIG;
        return 0;
    }
    r->duration = 7;
    if (r->dxferp && !r->iovec_count && !(r->flags & ~1U)) {
        memset(r->dxferp, 0xa5, r->dxfer_len);
        if (r->cmdp[0] == 0x4d) {
            unsigned char *p = r->dxferp;
            p[0] = r->cmdp[2] & 0x3f; p[1] = 0; p[2] = 0; p[3] = 4;
            if (r->dxfer_len == 8192) { p[2] = 0x1f; p[3] = 0xfc; }
        }
    }
    if (mode == 1 || mode == 2) {
        unsigned char sense[] = {0x70,0,0x40,0,0,0,0,10,0,0,0,0,0,2,0,0,0,0};
        assert(r->mx_sb_len >= sizeof(sense));
        memcpy(r->sbp, sense, sizeof(sense));
        r->sb_len_wr = sizeof(sense); r->resid = 7;
        r->status = 2; r->masked_status = 1; r->msg_status = 4;
        r->host_status = 5; r->driver_status = 8; r->info = 1;
    }
    if (mode == 3) r->sb_len_wr = 1; /* Even zero-valued returned sense blocks injection. */
    if (mode == 4) r->host_status = 7;
    if (mode == 5) r->driver_status = 8;
    if (mode == 6) r->info = 1;
    if (mode == 7) r->resid = -1;
    if (mode == 8) r->resid = (int)r->dxfer_len - 2;
    if (mode == 10) fail_write = 1;
    errno = mode == 2 ? EIO : EDOM;
    return mode == 2 ? -1 : 0;
}
#else
static void (*set_mode)(int);
static void (*set_time)(uint64_t);
static void invoke(unsigned char opcode, int mode, unsigned int flags, int iovec, int size) {
    unsigned char cdb[10] = {0}, sense[255] = {0}, data[8192] = {0};
    cdb[0] = opcode; cdb[2] = 0x71;
    sg_io_hdr_t r = {0};
    r.interface_id = 'S'; r.cmd_len = sizeof(cdb); r.cmdp = cdb;
    r.mx_sb_len = sizeof(sense); r.sbp = sense; r.dxferp = data;
    r.dxfer_len = size; r.dxfer_direction = opcode == 0x0a ? SG_DXFER_TO_DEV : SG_DXFER_FROM_DEV;
    r.flags = flags; r.iovec_count = iovec; r.timeout = 4321;
    r.pack_id = 314; r.usr_ptr = (void *)456;
    sg_io_hdr_t expected = r;
    expected.duration = 7;
    if (mode == 1 || mode == 2) {
        expected.sb_len_wr = 18; expected.resid = 7; expected.status = 2;
        expected.masked_status = 1; expected.msg_status = 4; expected.host_status = 5;
        expected.driver_status = 8; expected.info = 1;
    }
    if (mode == 3) expected.sb_len_wr = 1;
    if (mode == 4) expected.host_status = 7;
    if (mode == 5) expected.driver_status = 8;
    if (mode == 6) expected.info = 1;
    if (mode == 7) expected.resid = -1;
    if (mode == 8) expected.resid = size - 2;
    set_mode(mode);
    errno = EDOM;
    int rc = ioctl(71, SG_IO, &r);
    assert(rc == (mode == 2 ? -1 : 0));
    assert(errno == (mode == 2 ? EIO : EDOM));
    assert(!memcmp(&r, &expected, sizeof(r)));
    assert(cdb[0] == opcode && cdb[2] == 0x71);
    if (mode == 1 || mode == 2) {
        assert(sense[0] == 0x70 && sense[2] == 0x40 && sense[13] == 2);
    }
    if (opcode == 0x4d && !iovec && !(flags & ~1U)) {
        assert(data[0] == 0x31 && data[1] == 0 && data[3] == (size == 8192 ? 0xfc : 4) && data[7] == 0xa5);
    }
}
int main(int argc, char **argv) {
    assert(argc == 2);
    set_mode = dlsym(RTLD_DEFAULT, "stub_mode");
    set_time = dlsym(RTLD_DEFAULT, "stub_time");
    assert(set_mode && set_time);
    if (!strcmp(argv[1], "order")) {
        invoke(0x0a, 0, 1, 0, 32);
        set_time(2000000000ULL); invoke(0x4d, 0, 0, 0, 32);
        set_time(2500000000ULL); invoke(0x4d, 0, 0, 0, 32);
        set_time(3001000000ULL); invoke(0x4d, 0, 0, 0, 32);
    } else if (!strcmp(argv[1], "errors")) {
        for (int i = 1; i <= 8; ++i) { set_time(i * 2000000000ULL); invoke(0x4d, i, 0, 0, 32); }
        invoke(0x0a, 1, 1, 0, 32);
    } else if (!strcmp(argv[1], "layout")) {
        invoke(0x4d, 0, 0x80000000U, 0, 32);
        invoke(0x4d, 0, 4U, 0, 32); /* Kernel SG_FLAG_MMAP_IO; absent from some libc headers. */
        invoke(0x4d, 0, SG_FLAG_NO_DXFER, 0, 32);
        invoke(0x4d, 0, 0, 1, 32);
        invoke(0x08, 0, 0, 0, 32); /* Never capture READ file content. */
        invoke(0x4d, 0, 0, 0, 8192);
    } else if (!strcmp(argv[1], "injected-error")) {
        invoke(0x4d, 9, 0, 0, 32);
        set_time(9000000000ULL); invoke(0x4d, 0, 0, 0, 32);
    } else if (!strcmp(argv[1], "log-error")) {
        invoke(0x4d, 10, 0, 0, 32);
    } else if (!strcmp(argv[1], "abi")) {
        int guard = 'Q';
        errno = EDOM; assert(ioctl(71, SG_IO, &guard) == -1); assert(errno == ENOTTY);
        errno = EDOM; assert(ioctl(71, SG_SET_RESERVED_SIZE, (void *)123) == 17); assert(errno == EBUSY);
    } else {
        invoke(0x4d, 0, 0, 0, 32);
    }
    return 0;
}
#endif
'''


@unittest.skipUnless(sys.platform == "linux", "Native Linux SG ABI and LD_PRELOAD required")
class PhysicalSGTraceTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.temporary = tempfile.TemporaryDirectory(prefix="yatm-sg-trace-test-")
        cls.addClassCleanup(cls.temporary.cleanup)
        cls.root = Path(cls.temporary.name)
        stub = cls.root / "stub.c"
        stub.write_text(STUB)
        cc = shlex.split(os.environ.get("CC", "cc"))
        common = cc + ["-std=c11", "-O2", "-Wall", "-Wextra", "-Werror"]
        observer = Path(__file__).with_name("physical_sg_trace.c")
        for command in (
            common + ["-shared", "-fPIC", str(observer), "-o", str(cls.root / "trace.so"), "-ldl", "-pthread"],
            common + ["-shared", "-fPIC", str(stub), "-o", str(cls.root / "stub.so"), "-ldl"],
            common + ["-DPROBE", str(stub), "-o", str(cls.root / "probe"), "-ldl"],
        ):
            try:
                subprocess.run(command, check=True, capture_output=True, text=True, timeout=30)
            except subprocess.CalledProcessError as error:
                raise RuntimeError(
                    f"Native test build failed: {shlex.join(command)}\n{error.stdout or ''}{error.stderr or ''}"
                ) from error

    def run_probe(self, scenario, *, enabled=True, position=True, existing=False):
        with tempfile.TemporaryDirectory(dir=self.root) as directory:
            log = Path(directory) / "trace.jsonl"
            if existing:
                log.write_text("retained\n")
            env = {key: value for key, value in os.environ.items()
                   if key not in ("LD_PRELOAD", "YATM_SG_TRACE", "YATM_SG_TRACE_POSITION")}
            env["LD_PRELOAD"] = f"{self.root / 'trace.so'}:{self.root / 'stub.so'}"
            if enabled:
                env["YATM_SG_TRACE"] = str(log)
            env["YATM_SG_TRACE_POSITION"] = "1" if position else "0"
            result = subprocess.run([str(self.root / "probe"), scenario], env=env,
                                    capture_output=True, text=True, timeout=10)
            self.assertEqual(result.returncode, 0, result.stderr + result.stdout)
            raw = log.read_text() if log.exists() else ""
            rows = [] if existing else [json.loads(line) for line in raw.splitlines()]
            marker = Path(str(log) + ".error")
            error = marker.read_text() if marker.exists() else None
            return rows, result.stdout.splitlines(), error, raw

    def test_order_rate_schema_and_exact_original_buffers(self):
        rows, calls, error, _ = self.run_probe("order")
        self.assertIsNone(error)
        self.assertEqual(rows[0]["event"], "init")
        self.assertTrue(rows[0]["ready"])
        self.assertTrue(rows[0]["position_enabled"])
        commands = rows[1:]
        self.assertEqual(calls, ["call 0a 71", "call 4d 71", "call 34 71", "call 4d 71", "call 4d 71", "call 34 71"])
        self.assertEqual([r["cdb"][:2] for r in commands], ["0a", "4d", "34", "4d", "4d", "34"])
        required = {"request_start_ns", "request_end_ns", "monotonic_start_ns", "monotonic_end_ns",
                    "cdb", "rc", "errno", "status", "host_status", "driver_status", "resid",
                    "dxfer_len", "direction", "sense", "data", "injected", "pid", "tid"}
        for row in commands:
            self.assertLessEqual(required, row.keys())
            self.assertEqual(row["monotonic_end_ns"] - row["monotonic_start_ns"], 1_000_000)
            self.assertEqual(row["request_end_ns"] - row["request_start_ns"], 1_000_000)
        positions = [r for r in commands if r["injected"]]
        self.assertGreaterEqual(positions[1]["monotonic_start_ns"] - positions[0]["monotonic_start_ns"], 1_000_000_000)
        self.assertEqual(commands[0]["data"], "")
        for row in positions:
            self.assertEqual(row["cdb"], "34000000000000000000")
            self.assertEqual(len(bytes.fromhex(row["data"])), 20)
            self.assertEqual(bytes.fromhex(row["data"])[0], 0x30)
            self.assertEqual(row["data"][26:], "00000000000000")  # Unknown zero buffer counts retained raw.

    def test_errors_sense_and_transport_never_inject(self):
        rows, calls, error, _ = self.run_probe("errors")
        self.assertIsNone(error)
        self.assertEqual(len(calls), 9)
        self.assertFalse(any(row.get("injected") for row in rows))
        write = rows[-1]
        self.assertEqual(write["cdb"][:2], "0a")
        self.assertEqual(write["sense"], "700040000000000a00000000000200000000")
        self.assertEqual((write["status"], write["driver_status"], write["resid"]), (2, 8, 7))
        self.assertEqual(rows[2]["rc"], -1)
        self.assertFalse(rows[2]["outputs_valid"])

    def test_unknown_flags_iovecs_and_payload_are_not_captured(self):
        rows, calls, error, _ = self.run_probe("layout")
        self.assertIsNone(error)
        self.assertEqual([r["data"] for r in rows[1:6]], [""] * 5)
        self.assertEqual(rows[1]["flags"], 0x80000000)
        self.assertEqual(len(bytes.fromhex(rows[6]["data"])), 4096)
        self.assertTrue(rows[6]["data_truncated"])
        self.assertEqual(calls.count("call 34 71"), 1)

    def test_injected_failure_preserves_owner_result_and_disables_injection(self):
        rows, calls, error, _ = self.run_probe("injected-error")
        self.assertEqual(calls, ["call 4d 71", "call 34 71", "call 4d 71"])
        self.assertIn("injected READ POSITION failed", error)
        self.assertEqual(rows[1]["rc"], 0)
        self.assertEqual(rows[2]["rc"], -1)

    def test_write_failure_marks_loss_without_injection(self):
        rows, calls, error, _ = self.run_probe("log-error")
        self.assertIsNotNone(error)  # Marker presence survives a failed marker-content write.
        self.assertEqual(calls, ["call 4d 71"])
        self.assertEqual(len(rows), 1)

    def test_exclusive_log_creation_retains_existing_file(self):
        _, calls, error, raw = self.run_probe("single", existing=True)
        self.assertEqual(raw, "retained\n")
        self.assertIn("exclusive trace creation failed", error)
        self.assertEqual(calls, ["call 4d 71"])

    def test_opt_in_and_passive_mode(self):
        rows, calls, error, _ = self.run_probe("single", enabled=False)
        self.assertEqual(rows, [])
        self.assertEqual(calls, ["call 4d 71"])
        self.assertIsNone(error)
        rows, calls, error, _ = self.run_probe("single", position=False)
        self.assertFalse(rows[0]["position_enabled"])
        self.assertEqual(calls, ["call 4d 71"])
        self.assertIsNone(error)

    def test_unknown_abi_marks_gap_and_non_sg_is_forwarded(self):
        rows, calls, error, _ = self.run_probe("abi")
        self.assertIn("unsupported SG_IO ABI", error)
        self.assertEqual(calls, [])
        self.assertEqual(len(rows), 1)


if __name__ == "__main__":
    unittest.main()
