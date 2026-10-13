"""Check passive boundary collection without a Tape device."""

import json
from pathlib import Path
import tempfile
import unittest
from unittest import mock

import physical_capacity_boundary as boundary


class BoundaryTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name).resolve()
        self.mount = self.root / "mount"
        self.mount.mkdir()
        self.output = self.root / "observation.json"

    def test_files_include_partial_and_completed_outputs_without_following_links(self):
        (self.mount / "nested").mkdir()
        (self.mount / "nested" / "complete.bin").write_bytes(b"complete")
        (self.mount / ".tmp_partial").write_bytes(b"part")
        (self.mount / "loop").symlink_to(self.mount, target_is_directory=True)
        value = boundary.files_snapshot(self.mount)
        rows = {row["path"]: row for row in value["rows"]}
        self.assertEqual(set(rows), {"nested", "nested/complete.bin", ".tmp_partial", "loop"})
        self.assertEqual(rows[".tmp_partial"]["size"], 4)
        self.assertGreaterEqual(value["request_end_ns"], value["request_start_ns"])
        self.assertEqual(value["errors"], [])

    def test_oversized_fixture_is_explicitly_bounded(self):
        for index in range(130):
            (self.mount / str(index)).touch()
        value = boundary.files_snapshot(self.mount)
        self.assertEqual(len(value["rows"]), 128)
        self.assertIn("128-entry", value["errors"][0])

    def test_unavailable_optional_fields_preserve_required_preflight(self):
        observation = {"status": "mounted", "pages": {"31": {"parameters": {"2": 5}},
                        "0C": {"parameters": {"0": 7, "4": 0, "5": 100, "7": 0}},
                        "17": {"raw": "17000000", "decode_error": "Unsupported metric page"},
                        "2E": {"error": "unsupported"}},
                       "statvfs": {"available_bytes": 42}}
        with mock.patch.object(boundary, "snapshot", return_value=observation), \
                mock.patch.object(boundary.os, "getxattr", side_effect=OSError(95, "unsupported"), create=True):
            result = boundary.capture("/owned/work", self.mount, self.output, "before-copy")
        self.assertTrue(result["required_probes_ok"])
        self.assertEqual(json.loads(self.output.read_text()), result)
        self.assertIn("error", result["attributes"]["volumeCompression"])

    def test_preflight_failure_is_saved_and_final_observation_still_returns(self):
        observation = {"status": "mounted", "pages": {"0C": {"raw": "abcd"}},
                       "statvfs": {"error": "I/O error"}}
        with mock.patch.object(boundary, "snapshot", return_value=observation), \
                mock.patch.object(boundary.os, "getxattr", return_value=b"value", create=True):
            with self.assertRaisesRegex(RuntimeError, "preflight failed"):
                boundary.capture("/owned/work", self.mount, self.output, "before-copy")
            result = boundary.capture("/owned/work", self.mount, self.root / "final.json", "before-unmount")
        self.assertFalse(result["required_probes_ok"])
        self.assertEqual(result["snapshot"]["pages"]["0C"]["raw"], "abcd")

    def test_large_page_failure_stops_before_copy(self):
        observation = {"status": "mounted", "pages": {"31": {"parameters": {"2": 5}},
                       "0C": {"parameters": {"4": 0, "5": 100, "7": 0}},
                       "17": {"error": "I/O error"}}, "statvfs": {"available_bytes": 42}}
        with mock.patch.object(boundary, "snapshot", return_value=observation), \
                mock.patch.object(boundary.os, "getxattr", return_value=b"value", create=True):
            with self.assertRaisesRegex(RuntimeError, "preflight failed"):
                boundary.capture("/owned/work", self.mount, self.output, "before-copy")
        self.assertFalse(json.loads(self.output.read_text())["required_probes_ok"])

    def test_output_cannot_touch_tape_or_replace_existing_evidence(self):
        with self.assertRaisesRegex(ValueError, "outside"):
            boundary.capture("/owned/work", self.mount, self.mount / "evidence", "before-copy")
        self.output.write_text("preserved")
        with self.assertRaises(FileExistsError):
            boundary.capture("/owned/work", self.mount, self.output, "before-copy")
        self.assertEqual(self.output.read_text(), "preserved")


if __name__ == "__main__":
    unittest.main()
