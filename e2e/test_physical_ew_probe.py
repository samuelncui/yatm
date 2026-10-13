import errno
import io
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

from physical_ew_probe import copy_stream, trace_preflight


class EWProbeTests(unittest.TestCase):
    def test_short_writes_are_completed_and_hashed(self):
        import hashlib
        with tempfile.TemporaryDirectory() as root:
            events = io.StringIO()
            result = copy_stream(io.BytesIO(b"abcdefgh"), Path(root), events, 4, 8,
                                 write=lambda fd, data: os.write(fd, data[:2]))
            self.assertEqual(result["status"], "workload-bound-reached")
            self.assertEqual(result["total_accepted_bytes"], 8)
            self.assertEqual((Path(root) / "000001.bin").read_bytes(), b"efgh")
            self.assertEqual(result["completed"][0]["sha256"], hashlib.sha256(b"abcd").hexdigest())
            self.assertEqual(len([json.loads(line) for line in events.getvalue().splitlines()
                                  if json.loads(line)["event"] == "write"]), 4)

    def test_full_stops_without_retry_and_records_failed_cleanup(self):
        with tempfile.TemporaryDirectory() as root:
            events, calls = io.StringIO(), []
            def write(fd, data):
                calls.append(len(data))
                if len(calls) == 1:
                    return os.write(fd, data)
                raise OSError(errno.ENOSPC, "full")
            with patch("physical_ew_probe.os.unlink", side_effect=OSError(errno.EROFS, "read only")):
                result = copy_stream(io.BytesIO(b"abcdefgh"), Path(root), events, 4, 8, write=write)
            self.assertEqual(calls, [4, 4])
            self.assertEqual(result["total_accepted_bytes"], 4)
            self.assertEqual(result["failure"]["errno"], errno.ENOSPC)
            self.assertIn("delete_error", result["failure"])
            self.assertEqual(len(result["completed"]), 1)
            self.assertTrue(list(Path(root).glob(".tmp_*")))

    def test_close_error_is_not_completed_and_descriptor_not_closed_twice(self):
        with tempfile.TemporaryDirectory() as root:
            calls = []
            def close(fd):
                calls.append(fd)
                os.close(fd)
                raise OSError(errno.EIO, "close failed")
            result = copy_stream(io.BytesIO(b"abcd"), Path(root), io.StringIO(), 4, 4, close=close)
            self.assertEqual(len(calls), 1)
            self.assertEqual(result["completed"], [])
            self.assertEqual(result["failure"]["errno"], errno.EIO)
            self.assertEqual(list(Path(root).iterdir()), [])

    def test_source_failure_retains_a_reason(self):
        with tempfile.TemporaryDirectory() as root:
            result = copy_stream(io.BytesIO(b"ab"), Path(root), io.StringIO(), 4, 4)
            self.assertEqual(result["total_accepted_bytes"], 0)
            self.assertIn("Synthetic source", result["failure"]["error"])

    def test_position_preflight_requires_success_and_full_payload(self):
        with tempfile.TemporaryDirectory() as root:
            path = Path(root) / "sg.jsonl"
            row = dict(cdb="34000000000000000000", rc=0, status=0, host_status=0, data="00" * 19)
            initial = json.dumps(dict(event="init", ready=True, position_enabled=True)) + "\n"
            path.write_text(initial + json.dumps(row) + "\n")
            with self.assertRaisesRegex(RuntimeError, "READ POSITION"):
                trace_preflight(path)
            row["data"] += "00"
            path.write_text(initial + json.dumps(row) + "\n")
            trace_preflight(path)


if __name__ == "__main__":
    unittest.main()
