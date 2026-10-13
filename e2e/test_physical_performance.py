"""Bounded performance regressions without processes, services or Tape access."""

import hashlib
import os
from pathlib import Path
import tempfile
from types import SimpleNamespace
import unittest
from unittest import mock

from package_physical_performance import manifest_pages, run_stage, sample_process
import subprocess
import signal
import physical_performance_fixture as fixture
import test_package_physical_lifecycle


class PerformanceControllerTests(test_package_physical_lifecycle.PhysicalControllerTests):
    def test_performance_prepare_does_not_require_preview(self):
        args = self.arguments(**{"physical-stage": "performance-prepare", "preview-archive": None})
        self.assertIsNone(args.preview_archive)

    def test_performance_source_mode_must_match_saved_state(self):
        from physical_package_acceptance import validate_state
        args = self.resumed_arguments()
        args.physical_stage = "performance-run"
        args.performance_source_read_mode = "mapped"
        state = self.owned_state()
        state.update(performance=True, performance_source_read_mode="buffered",
                     completed_stages=["performance-prepare", "performance-format"])
        with self.assertRaisesRegex(RuntimeError, "read mode differs"):
            validate_state(args, state)

    def test_performance_size_must_match_saved_state(self):
        from physical_package_acceptance import validate_state
        args = self.resumed_arguments()
        args.physical_stage = "performance-run"
        args.performance_size_gib = 32
        state = self.owned_state()
        state.update(performance=True, performance_source_read_mode="buffered", performance_size_gib=4,
                     completed_stages=["performance-prepare", "performance-format"])
        with self.assertRaisesRegex(RuntimeError, "fixture size differs"):
            validate_state(args, state)

    def test_performance_state_cannot_enter_baseline_restore(self):
        from physical_package_acceptance import validate_state
        args = self.resumed_arguments()
        args.physical_stage = "restore"
        state = self.owned_state()
        state.update(performance=True, completed_stages=["performance-prepare", "performance-format"])
        with self.assertRaisesRegex(RuntimeError, "Cannot mix"):
            validate_state(args, state)


class PerformanceFixtureTests(unittest.TestCase):
    def test_capacity_clones_have_full_distinct_deterministic_bytes(self):
        # Native FICLONE is checked on the selected Linux filesystem before hardware use.
        # This substitute exercises content preparation on platforms without FICLONE.
        def clone(target, command, source):
            self.assertEqual(command, 0x40049409)
            data = os.pread(source, os.fstat(source).st_size, 0)
            self.assertEqual(os.pwrite(target, data, 0), len(data))

        sizes = [256 * 1024, 256 * 1024, 96 * 1024]
        with tempfile.TemporaryDirectory() as root, mock.patch.object(fixture.sys, "platform", "linux"), \
                mock.patch.object(fixture.fcntl, "ioctl", side_effect=clone) as ioctl:
            first, second = Path(root) / "one", Path(root) / "two"
            manifest = fixture.capacity_reflink(first, sizes)
            self.assertEqual(fixture.capacity_reflink(second, sizes), manifest)
            contents = [(first / name).read_bytes() for name in manifest]
            self.assertEqual([len(data) for data in contents], sizes)
            self.assertEqual(len({row["sha256"] for row in manifest.values()}), 3)
            for name, data in zip(manifest, contents):
                self.assertEqual(hashlib.sha256(data).hexdigest(), manifest[name]["sha256"])
            self.assertEqual(contents[0][64 * 1024:96 * 1024], contents[2][64 * 1024:])
            self.assertNotEqual(contents[0][:64 * 1024], contents[1][:64 * 1024])
            self.assertEqual(ioctl.call_count, 6)
            with self.assertRaisesRegex(ValueError, "empty"):
                fixture.capacity_reflink(first, sizes)
            self.assertEqual((first / "chunk-000.bin").read_bytes(), contents[0])

    def test_capacity_clone_failure_is_not_silently_copied(self):
        with tempfile.TemporaryDirectory() as root, mock.patch.object(fixture.sys, "platform", "linux"), \
                mock.patch.object(fixture.fcntl, "ioctl", side_effect=OSError("clone unavailable")):
            with self.assertRaisesRegex(OSError, "clone unavailable"):
                fixture.capacity_reflink(Path(root) / "source", [4096])
            self.assertEqual((Path(root) / "source/chunk-000.bin").stat().st_size, 0)
            self.assertEqual(list(Path(root).iterdir()), [Path(root) / "source"])

    def test_deterministic_fixture_and_batched_restore_check(self):
        with tempfile.TemporaryDirectory() as root, mock.patch.dict(fixture.MATRIX, {"tiny": (2, 256 * 1024, True)}):
            first, second = Path(root) / "one", Path(root) / "two"
            manifest = fixture.generate(first, "tiny")
            self.assertEqual(fixture.generate(second, "tiny"), manifest)
            for name, expected in manifest.items():
                data = (first / name).read_bytes()
                self.assertEqual(len(data), expected["size"])
                self.assertEqual(hashlib.sha256(data).hexdigest(), expected["sha256"])
                for offset in range(0, len(data), 8192):
                    self.assertEqual(data[offset:offset + 4096], data[offset + 4096:offset + 8192])
            rows = [{"path": str(first / name), **value} for name, value in manifest.items()]
            self.assertEqual(fixture.verify(rows), rows)
            (first / next(iter(manifest))).write_bytes(b"damaged")
            self.assertNotEqual(fixture.verify(rows), rows)

    def test_metric_probe_uses_only_exact_job_owner(self):
        with tempfile.TemporaryDirectory() as root:
            proc = Path(root)
            entries = []
            for pid, directory in (("41", "/owned/job"), ("42", "/other/job")):
                process = proc / pid
                process.mkdir()
                (process / "cmdline").write_bytes(b"/opt/ltfs\0-o\0work_directory=" + directory.encode() + b"\0/mount\0")
                (process / "status").write_text("VmRSS:\t12 kB\nVmHWM:\t24 kB\n")
                (process / "fd").mkdir()
                entries.append(process / "cmdline")
            real_path = Path
            with mock.patch.object(fixture, "Path", side_effect=lambda value: SimpleNamespace(glob=lambda pattern: entries)
                                   if value == "/proc" else real_path(value)), \
                    mock.patch.object(fixture, "read_log_xattr", return_value=b"raw", create=True) as xattr, \
                    mock.patch.object(fixture.os, "statvfs") as statvfs:
                sample = fixture.sample("/owned/job")
                self.assertEqual(sample["pid"], 41)
                reading = sample["pages"]["34"]
                self.assertEqual(reading["raw"], b"raw".hex())
                self.assertGreaterEqual(reading["request_end_ns"], reading["request_start_ns"])
                self.assertGreaterEqual(reading["request_duration_ns"], 0)
                self.assertIn("decode_error", reading)
                self.assertNotIn("parameters", reading)
                self.assertEqual(sample["memory_kib"], {"VmRSS": 12, "VmHWM": 24})
                self.assertEqual(xattr.call_args_list, [
                    mock.call("/mount", "user.ltfs.vendor.IBM.logPage.34.00"),
                    mock.call("/mount", "user.ltfs.vendor.IBM.logPage.0C.00")])
                self.assertNotIn("statvfs", sample)
                self.assertEqual(fixture.sample("/absent/job")["status"], "not-mounted")
                self.assertEqual(xattr.call_count, 2)
                statvfs.assert_not_called()

                raw = LogPageTests().page(0x02, [(code, 0x40, code.to_bytes(4, "big")) for code in range(7)])
                for reply in (raw, raw[:-1], b"unsupported", OSError(95, "unsupported")):
                    with self.subTest(reply=reply):
                        xattr.side_effect = [reply]
                        reading = fixture.sample("/owned/job", pages=("02",))["pages"]["02"]
                        xattr.assert_called_with("/mount", "user.ltfs.vendor.IBM.logPage.02.00")
                        self.assertGreaterEqual(reading["request_end_ns"], reading["request_start_ns"])
                        self.assertGreaterEqual(reading["request_duration_ns"], 0)
                        if isinstance(reply, OSError):
                            self.assertEqual(reading["errno"], 95)
                            self.assertEqual(reading["error"], str(reply))
                            self.assertNotIn("raw", reading)
                        else:
                            self.assertEqual(reading["raw"], reply.hex())
                            if reply == raw:
                                self.assertEqual(reading["parameters"], {str(code): code for code in range(7)})
                            else:
                                self.assertIn("decode_error", reading)
                        if reply != raw:
                            self.assertNotIn("parameters", reading)
                statvfs.assert_not_called()

    def test_unknown_xattr_is_not_zero(self):
        with tempfile.TemporaryDirectory() as root:
            process = Path(root) / "41"
            process.mkdir()
            cmdline = process / "cmdline"
            cmdline.write_bytes(b"/opt/ltfs\0work_directory=/owned/job\0/mount\0")
            real_path = Path
            with mock.patch.object(fixture, "Path", side_effect=lambda value: SimpleNamespace(glob=lambda pattern: [cmdline])
                                   if value == "/proc" else real_path(value)), mock.patch.object(fixture, "read_log_xattr", side_effect=OSError(95, "unsupported"), create=True):
                sample = fixture.sample("/owned/job")
                self.assertEqual(sample["pages"]["34"]["errno"], 95)
                self.assertNotIn("raw", sample["pages"]["34"])

    def test_sampler_timeout_kills_owned_process_group(self):
        process = mock.Mock(pid=123, returncode=-9)
        process.communicate.side_effect = [subprocess.TimeoutExpired(["helper"], 20), (b"partial", b"")]
        process.poll.return_value = -9
        with mock.patch("package_physical_performance.subprocess.Popen", return_value=process) as spawn, \
                mock.patch("package_physical_performance.os.killpg") as kill:
            result = sample_process(["helper"])
            self.assertTrue(result["timed_out"])
            kill.assert_called_once_with(123, signal.SIGKILL)
            self.assertTrue(spawn.call_args.kwargs["start_new_session"])
            process.wait.assert_called_once_with(timeout=2)
            process.stdout.close.assert_called_once()


    def test_paginated_selection_and_nonadvancing_page(self):
        test = SimpleNamespace(one=mock.Mock(side_effect=[{"items": [1, 2], "has_more": True}, {"items": [3]}]))
        self.assertEqual(list(manifest_pages(test, "archive", {"id": "7"})), [[1, 2], [3]])
        self.assertEqual(test.one.call_args.args[-1], "2")
        test.one.side_effect = [{"items": [], "has_more": True}]
        with self.assertRaisesRegex(RuntimeError, "did not advance"):
            list(manifest_pages(test, "archive", {"id": "7"}))

    def test_stage_rejects_device_change_before_operations(self):
        test = SimpleNamespace(args=SimpleNamespace(physical_stage="performance-run", physical_device="/dev/nst0",
                                                    physical_barcode="ABC001"), root="/owned")
        with self.assertRaisesRegex(RuntimeError, "identity differs"):
            run_stage(test, {"physical_device": "/dev/nst1"}, lambda: None)


class LargeXattrTests(unittest.TestCase):
    def test_full_page_uses_one_bounded_call(self):
        raw = b"page" * 129
        def getxattr(path, name, buffer, size):
            self.assertEqual((path, name, size), (b"/mount", b"user.test", 65536))
            fixture.ctypes.memmove(buffer, raw, len(raw))
            return len(raw)
        call = mock.Mock(side_effect=getxattr)
        with mock.patch.object(fixture.sys, "platform", "linux"), \
                mock.patch.object(fixture.ctypes, "CDLL", return_value=SimpleNamespace(getxattr=call)):
            self.assertEqual(fixture.read_log_xattr("/mount", "user.test"), raw)
        call.assert_called_once()

    def test_kernel_error_is_preserved_without_retry(self):
        call = mock.Mock(return_value=-1)
        with mock.patch.object(fixture.sys, "platform", "linux"), \
                mock.patch.object(fixture.ctypes, "CDLL", return_value=SimpleNamespace(getxattr=call)), \
                mock.patch.object(fixture.ctypes, "get_errno", return_value=5):
            with self.assertRaises(OSError) as raised:
                fixture.read_log_xattr("/mount", "user.test")
        self.assertEqual(raised.exception.errno, 5)
        call.assert_called_once()

    @unittest.skipUnless(fixture.sys.platform == "linux", "Linux getxattr ABI")
    def test_native_filesystem_large_attribute(self):
        with tempfile.TemporaryDirectory() as root:
            path = Path(root) / "fixture"
            path.touch()
            raw = bytes(range(256)) * 3
            fixture.os.setxattr(path, "user.yatm_probe", raw)
            self.assertEqual(fixture.read_log_xattr(path, "user.yatm_probe"), raw)


class LogPageTests(unittest.TestCase):
    def page(self, page, parameters):
        body = b"".join(code.to_bytes(2, "big") + bytes([control, len(value)]) + value
                        for code, control, value in parameters)
        return bytes([page, 0]) + len(body).to_bytes(2, "big") + body

    def test_valid_zero_rate_and_large_byte_counters(self):
        for value in (0, 41):
            raw = self.page(0x34, [(77, 0, b"other"), (0, 0x60, value.to_bytes(2, "big"))])
            self.assertEqual(fixture.decode_log_page(raw, 0x34), {"0": value})
        expected = {str(code): 2**40 + code for code in range(4)}
        raw = self.page(0x0C, [(code, 0x60, expected[str(code)].to_bytes(8, "big")) for code in (3, 1, 0, 2)])
        self.assertEqual(fixture.decode_log_page(raw, 0x0C), expected)

    def test_optional_performance_rates_when_present(self):
        rows = [(code, 0x60, (1400 + code).to_bytes(2, "big")) for code in (4, 1, 0, 3, 2)]
        self.assertEqual(fixture.decode_log_page(self.page(0x34, rows), 0x34),
                         {str(code): 1400 + code for code in range(5)})
        self.assertEqual(fixture.decode_log_page(self.page(0x34, rows[:2] + [rows[2]]), 0x34)["0"], 1400)
        for optional in ((1, 0x60, b"x"), (1, 0, b"xx"), (4, 0, b"xx")):
            self.assertEqual(fixture.decode_log_page(self.page(0x34, [(0, 0x60, b"xx"), optional]), 0x34),
                             {"0": int.from_bytes(b"xx", "big")})
        actual = bytes.fromhex("3400001e000060020000000160020107000260021770000360020107000440020578")
        self.assertEqual(fixture.decode_log_page(actual, 0x34), {"0": 0, "1": 263, "2": 6000, "3": 263, "4": 1400})
        duplicate = self.page(0x34, [(0, 0x60, b"xx"), (1, 0x60, b"xx"), (1, 0x60, b"xx")])
        self.assertNotIn("1", fixture.decode_log_page(duplicate, 0x34))

    def test_optional_capacity_parameters(self):
        required = [(code, 0x60, (2**40 + code).to_bytes(8, "big")) for code in range(4)]
        expected = {str(code): 2**40 + code for code in range(4)}
        optional = [(code, 0x60, value.to_bytes(4, "big"))
                    for code, value in zip(range(4, 9), (0, 1, 2**31, 2**32 - 1, 128))]
        self.assertEqual(fixture.decode_log_page(self.page(0x0C, required), 0x0C), expected)
        self.assertEqual(fixture.decode_log_page(self.page(0x0C, optional + required), 0x0C),
                         {**expected, "4": 0, "5": 1, "6": 2**31, "7": 2**32 - 1, "8": 128})
        for code, control, value in optional:
            good = (code, control, value)
            invalid = [(code, 0x60, bytes(width)) for width in (0, 1, 2, 3, 5, 8)]
            invalid += [(code, control, value) for control in (0, 0x40, 0x61)]
            for bad in invalid:
                for rows in ([bad], [bad, good], [good, bad], [good, good, good]):
                    with self.subTest(code=code, rows=rows):
                        self.assertEqual(fixture.decode_log_page(self.page(0x0C, required + rows), 0x0C), expected)
        # A malformed optional field may be skipped only if the complete page is parseable.
        truncated = self.page(0x0C, required + optional)[:-1]
        truncated = truncated[:2] + (len(truncated) - 4).to_bytes(2, "big") + truncated[4:]
        for raw in (truncated, self.page(0x0C, required[:-1] + optional)):
            with self.subTest(raw=raw), self.assertRaises(ValueError):
                fixture.decode_log_page(raw, 0x0C)

    def test_complete_hp_write_error_counters(self):
        expected = {"0": 0, "1": 2**31, "2": 17, "3": 12, "4": 23, "5": 2**32 - 1, "6": 5}
        rows = [(int(code), 0x40, value.to_bytes(4, "big")) for code, value in expected.items()]
        raw = self.page(0x02, rows[::-1] + [(77, 0, b"unknown")])
        self.assertEqual(fixture.decode_log_page(raw, 0x02), expected)
        for index, row in enumerate(rows):
            invalid_rows = [rows[:index] + rows[index + 1:], rows + [row]]
            invalid_rows += [rows[:index] + [(row[0], row[1], bytes(width))] + rows[index + 1:]
                             for width in (0, 2, 3, 5, 8)]
            for invalid in invalid_rows:
                with self.subTest(code=row[0], rows=invalid), self.assertRaises(ValueError):
                    fixture.decode_log_page(self.page(0x02, invalid), 0x02)
        for invalid in (raw[:-1], raw + b"extra", b"\x03" + raw[1:], self.page(0x02, [])):
            with self.subTest(raw=invalid), self.assertRaises(ValueError):
                fixture.decode_log_page(invalid, 0x02)

    def test_rejects_malformed_pages(self):
        good = self.page(0x34, [(0, 0x60, b"\0\x01")])
        malformed = [b"", good[:3], good[:-1], good + b"extra", bytes([0x0C]) + good[1:],
                     bytes([0x34, 1]) + good[2:], self.page(0x34, []),
                     self.page(0x34, [(0, 0x60, b"x")]), self.page(0x34, [(0, 0, b"xx")]),
                     self.page(0x34, [(0, 0x60, b"xx"), (0, 0x60, b"xx")]),
                     bytes([0x34, 0, 0, 3]) + b"abc",
                     bytes([0x34, 0, 0, 5]) + b"\0\0\x60\x02x"]
        for raw in malformed:
            with self.subTest(raw=raw), self.assertRaises(ValueError):
                fixture.decode_log_page(raw, 0x34)
        with self.assertRaises(ValueError):
            fixture.decode_log_page(self.page(0x0C, [(0, 0x60, bytes(8))]), 0x0C)


if __name__ == "__main__":
    unittest.main()
