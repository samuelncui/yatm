"""Observe-helper boundaries without a physical device."""

import json
from pathlib import Path
import stat
from types import SimpleNamespace
import tempfile
import unittest
from unittest import mock

import physical_tape_observe as probe


class TapeObserveTests(unittest.TestCase):
    def test_supported_pages_requires_complete_response(self):
        self.assertEqual(probe.supported_pages(b"\0\0\0\3\x00\x0c\x31"), {0, 12, 49})
        for raw in (b"", b"\0\0\0", b"\x31\0\0\0", b"\0\0\0\2\x31", b"\0\0\0\0\x31"):
            with self.subTest(raw=raw), self.assertRaises(ValueError):
                probe.supported_pages(raw)

    def test_busy_or_failed_owner_check_rejects_device(self):
        for code in (0, 2):
            with self.subTest(code=code), \
                 mock.patch.object(Path, "stat", return_value=SimpleNamespace(st_mode=stat.S_IFCHR)), \
                 mock.patch.object(Path, "read_text", return_value="1\n"), \
                 mock.patch.object(Path, "iterdir", return_value=iter([Path("nst0")])), \
                 mock.patch.object(probe.subprocess, "run", return_value=SimpleNamespace(returncode=code, stderr=b"owner")):
                with self.assertRaisesRegex(RuntimeError, "in use"):
                    probe.idle(Path("/dev/sg0"))

    def test_existing_evidence_is_preserved_before_queries(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            device = root / "device"
            device.touch()
            with mock.patch.object(probe, "idle"), mock.patch.object(probe.subprocess, "run") as run:
                with self.assertRaises(FileExistsError):
                    probe.observe(device, root)
                run.assert_not_called()

    def test_raw_failures_preserved_and_new_owner_stops_collection(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            device = root / "device"
            device.touch()
            output = root / "evidence"
            results = [SimpleNamespace(returncode=2, stdout=b"", stderr=b"not ready"),
                       SimpleNamespace(returncode=0, stdout=b"\0\0\0\2\x00\x31", stderr=b"")]
            with mock.patch.object(probe, "idle", side_effect=[None, None, None, RuntimeError("busy")]), \
                 mock.patch.object(probe.subprocess, "run", side_effect=results) as run:
                with self.assertRaisesRegex(RuntimeError, "busy"):
                    probe.observe(device, output)
            self.assertEqual(run.call_count, 2)
            rows = [json.loads(line) for line in (output / "commands.jsonl").read_text().splitlines()]
            self.assertEqual(rows[0]["returncode"], 2)
            self.assertEqual(rows[0]["stderr"], "not ready")
            self.assertEqual((output / "log-00.bin").read_bytes(), b"\0\0\0\2\x00\x31")
            self.assertFalse((output / "log-31.bin").exists())

    def test_cm_reads_reported_length_within_documented_bound(self):
        for size in (0, 8160, 8192, 8193):
            with self.subTest(size=size), tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary)
                device = root / "device"
                device.touch()
                output = root / "evidence"

                def reply(args, **kwargs):
                    raw = b""
                    if args[-10:-7] == ["4d", "00", "40"]:
                        raw = b"\0\0\0\1\0"
                    elif args[-10:-7] == ["3c", "03", "10"]:
                        raw = b"\0" + size.to_bytes(3, "big")
                    elif args[-10:-7] == ["3c", "02", "10"]:
                        self.assertIn(size, (8160, 8192))
                        self.assertIn("--request=" + str(size), args)
                        self.assertEqual(int("".join(args[-4:-1]), 16), size)
                        raw = bytes(size)
                    return SimpleNamespace(returncode=0, stdout=raw, stderr=b"")

                with mock.patch.object(probe, "idle"), mock.patch.object(probe.subprocess, "run", side_effect=reply):
                    probe.observe(device, output, cartridge_memory=True)
                cm = output / "cm.bin"
                self.assertEqual(cm.exists(), size in (8160, 8192))
                if cm.exists():
                    self.assertEqual(cm.stat().st_size, size)

    def test_position_probe_preserves_short_sense_and_unsupported_form(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            device = root / "device"
            device.touch()
            output = root / "evidence"
            fixed = bytes.fromhex("70 00 00 00 00 00 00 10") + bytes(16)

            def reply(args, **kwargs):
                raw, status, error = b"", 0, b""
                if args[-6:] == ["03", "00", "00", "00", "ff", "00"]:
                    self.assertIn("--request=255", args)
                    raw = fixed
                elif args[-10:-8] == ["34", "01"]:
                    status, error = 5, b"Illegal request: invalid field in cdb"
                elif args[-10:-7] == ["4d", "00", "40"]:
                    raw = b"\0\0\0\1\0"
                return SimpleNamespace(returncode=status, stdout=raw, stderr=error)

            with mock.patch.object(probe, "idle"), mock.patch.object(probe.subprocess, "run", side_effect=reply):
                probe.observe(device, output, position_probe=True)
            rows = [json.loads(line) for line in (output / "commands.jsonl").read_text().splitlines()]
            self.assertEqual(rows[0]["name"], "sense-fixed")
            self.assertEqual(rows[0]["bytes"], 24)
            self.assertEqual((output / "sense-fixed.bin").read_bytes(), fixed)
            vendor = next(row for row in rows if row["name"] == "position-vendor")
            self.assertEqual(vendor["returncode"], 5)
            self.assertIn("invalid field", vendor["stderr"])
            self.assertTrue((output / "position-long.bin").exists())


if __name__ == "__main__":
    unittest.main()
