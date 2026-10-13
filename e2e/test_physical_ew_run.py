import argparse
import json
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

from physical_ew_run import run


class EWRunTests(unittest.TestCase):
    def args(self, root):
        return argparse.Namespace(output=root / "new-run", device=Path("/dev/test-tape"),
                                  sg_device=Path("/dev/test-sg"), barcode="ABC123", authorize_erase="ABC123",
                                  ltfs=root / "ltfs", mkltfs=root / "mkltfs", lto_info=root / "info",
                                  trace_library=root / "observer.so", expected_native_min_bytes=1024**4,
                                  file_bytes=1024**3, max_bytes=2 * 1024**4)

    def test_different_cartridge_never_formats(self):
        with tempfile.TemporaryDirectory() as directory:
            args, calls = self.args(Path(directory)), []
            def command(argv, **kwargs):
                calls.append(argv)
                if argv[0] == str(args.lto_info):
                    kwargs["stdout"].write(b"Barcode: XYZ789\n")
                return subprocess.CompletedProcess(argv, 0)
            with patch("physical_ew_run.idle"), patch("physical_ew_run.subprocess.run", side_effect=command):
                with self.assertRaisesRegex(RuntimeError, "identity"):
                    run(args)
            self.assertEqual(len(calls), 2)
            self.assertFalse(any(str(args.mkltfs) in call for call in calls))
            self.assertEqual(json.loads((args.output / "status.json").read_text())["phase"], "needs-inspection")

    def test_existing_run_cannot_replay_destructive_stage(self):
        with tempfile.TemporaryDirectory() as directory:
            args = self.args(Path(directory))
            args.output.mkdir()
            with patch("physical_ew_run.subprocess.run") as command:
                with self.assertRaises(FileExistsError):
                    run(args)
                command.assert_not_called()


if __name__ == "__main__":
    unittest.main()
