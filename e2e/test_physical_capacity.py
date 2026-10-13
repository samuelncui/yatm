"""Tiny capacity-probe checks with fake owner metrics and no device access."""

import hashlib
import json
from pathlib import Path
import tempfile
from types import SimpleNamespace
import unittest
from unittest import mock

import physical_capacity_probe as probe
from physical_performance_fixture import decode_log_page


def capacity_page():
    body = b"".join(code.to_bytes(2, "big") + b"\x60\x04" + (100 + code).to_bytes(4, "big")
                    for code in (4, 2, 1, 3))
    return b"\x31\x00" + len(body).to_bytes(2, "big") + body


class CapacityProbeTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name).resolve()
        self.mount, self.work = self.root / "mount", self.root / "work"
        self.mount.mkdir()
        self.work.mkdir()
        self.output = self.root / "evidence.jsonl"
        self.stack = []
        for patch in (
                mock.patch.object(probe, "sample", side_effect=lambda work, pages=None: {
                    "status": "mounted", "mount": str(self.mount), "pid": 42,
                    "pages": {"34": {"raw": "00"}, "0C": {"raw": "00"},
                              "31": {"raw": capacity_page().hex(), "parameters": decode_log_page(capacity_page(), 0x31)}}}),
                mock.patch.object(probe.os, "getxattr", return_value=capacity_page(), create=True),
                mock.patch.object(probe.os, "setxattr", create=True),
                mock.patch.object(probe.os, "statvfs", return_value=SimpleNamespace(
                    f_frsize=4096, f_blocks=100, f_bfree=80, f_bavail=70)),
                mock.patch.object(probe, "SCENE_BYTES", 64),
                mock.patch.object(probe, "SMALL_BYTES", 16),
                mock.patch.object(probe, "BUFFER_BYTES", 16)):
            self.stack.append(patch.start())
            self.addCleanup(patch.stop)

    def test_explicit_snapshot_owns_timestamped_statvfs_reading(self):
        with mock.patch.object(probe.time, "time_ns", return_value=123456):
            observation = probe.snapshot(self.work, self.mount)
        self.stack[0].assert_called_once_with(str(self.work), pages=("34", "0C", "31"))
        self.stack[3].assert_called_once_with(self.mount)
        self.assertEqual(observation["statvfs"], {
            "timestamp_ns": 123456, "fragment_bytes": 4096, "total_bytes": 409600,
            "free_bytes": 327680, "available_bytes": 286720})

    def test_six_equal_scenes_hash_real_bytes_and_sync_once_each(self):
        directory = probe.run_probe(self.work, self.mount, self.output, "operator-unknown")
        rows = [json.loads(line) for line in self.output.read_text().splitlines()]
        scenes = [row for row in rows if row["event"] == "ordinary-close"]
        self.assertEqual(len(scenes), 6)
        self.assertEqual([len(row["files"]) for row in scenes], [4, 1, 4, 1, 4, 1])
        self.assertEqual(self.stack[2].call_count, 7)
        clean_sync = [row for row in rows if row.get("clean_index_probe")]
        self.assertEqual(len(clean_sync), 1)
        self.assertEqual(clean_sync[0]["scene"], "final-clean")
        self.assertTrue(clean_sync[0]["success"])
        self.assertEqual([row["event"] for row in rows[-4:]],
                         ["clean-sync-before", "explicit-sync", "clean-sync-after", "completed"])
        self.assertEqual(rows[-1], {"event": "completed", "logical_bytes": 384})
        for scene in scenes:
            self.assertEqual(scene["logical_bytes"], 64)
            self.assertGreaterEqual(scene["write_end_ns"], scene["write_start_ns"])
            self.assertGreaterEqual(scene["write_duration_ns"], scene["close_duration_ns"])
            self.assertEqual(scene["after"]["statvfs"]["available_bytes"], 70 * 4096)
            self.assertEqual(scene["after"]["pages"]["31"]["parameters"],
                             {str(code): 100 + code for code in range(1, 5)})
            for row in scene["files"]:
                path = Path(row["path"])
                self.assertIn(directory, path.parents)
                self.assertEqual(path.stat().st_size, row["size"])
                self.assertEqual(hashlib.sha256(path.read_bytes()).hexdigest(), row["sha256"])

    def test_sync_failure_is_recorded_and_raised_without_completion(self):
        self.stack[2].side_effect = OSError(5, "sync failed")
        with self.assertRaisesRegex(OSError, "sync failed"):
            probe.run_probe(self.work, self.mount, self.output, "off")
        rows = [json.loads(line) for line in self.output.read_text().splitlines()]
        self.assertEqual(rows[-1]["event"], "failed")
        self.assertFalse(next(row for row in rows if row["event"] == "explicit-sync")["success"])
        self.assertNotIn("completed", [row["event"] for row in rows])
        self.assertTrue(list(self.mount.glob("yatm-capacity-probe-*")))

    def test_clean_sync_failure_cannot_report_completion(self):
        self.stack[2].side_effect = [None] * 6 + [OSError(5, "clean sync failed")]
        with self.assertRaisesRegex(OSError, "clean sync failed"):
            probe.run_probe(self.work, self.mount, self.output, "off")
        rows = [json.loads(line) for line in self.output.read_text().splitlines()]
        self.assertEqual(rows[-1]["event"], "failed")
        clean_sync = next(row for row in rows if row.get("clean_index_probe"))
        self.assertFalse(clean_sync["success"])
        self.assertNotIn("completed", [row["event"] for row in rows])

    def test_wrong_owner_and_output_on_tape_rejected_before_writes(self):
        self.stack[0].return_value = {"status": "not-mounted"}
        self.stack[0].side_effect = None
        with self.assertRaisesRegex(RuntimeError, "exact LTFS"):
            probe.run_probe(self.work, self.mount, self.output, "on")
        self.assertFalse(self.output.exists())
        with self.assertRaisesRegex(ValueError, "outside"):
            probe.run_probe(self.work, self.mount, self.mount / "evidence", "on")
        self.assertFalse(list(self.mount.iterdir()))

    def test_existing_evidence_never_overwritten(self):
        self.output.write_text("preserved")
        with self.assertRaises(FileExistsError):
            probe.run_probe(self.work, self.mount, self.output, "on")
        self.assertEqual(self.output.read_text(), "preserved")
        self.assertFalse(list(self.mount.iterdir()))

    def test_unknown_capacity_keeps_raw_or_error_without_zero(self):
        self.stack[0].side_effect = lambda work, pages=None: {"status": "mounted", "mount": str(self.mount),
                                                           "pages": {"31": {"errno": 95, "error": "unsupported"}}}
        reading = probe.snapshot(self.work, self.mount)["pages"]["31"]
        self.assertEqual(reading["errno"], 95)
        self.assertNotIn("parameters", reading)
        self.stack[0].side_effect = lambda work, pages=None: {"status": "mounted", "mount": str(self.mount),
                                                           "pages": {"31": {"raw": "bad", "decode_error": "bad"}}}
        reading = probe.snapshot(self.work, self.mount)["pages"]["31"]
        self.assertIn("decode_error", reading)
        self.assertNotIn("parameters", reading)

    def test_capacity_records_reject_truncation_widths_duplicates_and_missing(self):
        good = capacity_page()
        invalid = [good[:3], good[:-1], good + b"extra", b"\x30" + good[1:],
                   good[:1] + b"\x01" + good[2:], b"\x31\0\0\0"]
        duplicate = good[4:12] * 2
        invalid.append(b"\x31\0" + len(duplicate).to_bytes(2, "big") + duplicate)
        for raw in invalid:
            with self.subTest(raw=raw), self.assertRaises(ValueError):
                decode_log_page(raw, 0x31)

    def test_content_is_deterministic_and_alternates_compressibility(self):
        random = probe.block("scene", 0, 0, 16, "random")
        self.assertEqual(random, probe.block("scene", 0, 0, 16, "random"))
        self.assertEqual(random, probe.block("scene", 0, 0, 16, "alternating"))
        repeated = probe.block("scene", 1, 0, 16, "alternating")
        self.assertEqual(repeated[:8], repeated[8:])

    def test_write_failure_remains_primary_when_close_also_fails(self):
        target = mock.Mock()
        target.write.side_effect = OSError(28, "write full")
        target.close.side_effect = OSError(5, "close error")
        with mock.patch.object(Path, "open", return_value=target):
            with self.assertRaisesRegex(OSError, "write full") as raised:
                probe.write_file(self.mount / "file", "scene", 0, 16, "random")
        self.assertEqual(raised.exception.errno, 28)
        self.assertEqual(raised.exception.__cause__.errno, 5)


if __name__ == "__main__":
    unittest.main()
