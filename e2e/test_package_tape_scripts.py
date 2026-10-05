"""Exercise shipped Tape shell adapters without a drive or LTFS installation."""

import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest


class FormatScriptTests(unittest.TestCase):
    def test_format_preserves_arguments_and_reports_failure(self):
        script = Path(__file__).resolve().parents[1] / "scripts" / "mkfs"
        for exit_code in (0, 16):
            with self.subTest(exit_code=exit_code), tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary)
                adapters = root / "Tape adapters"
                adapters.mkdir()
                shutil.copy2(script, adapters / "mkfs")
                commands = {
                    "get_device": '#!/bin/sh\nprintf "%s\\n" /dev/sg7\n',
                    "mkltfs": '#!/bin/sh\nprintf "%s\\0" "$@" > "$ARGUMENTS"\nexit "$FORMAT_EXIT"\n',
                    "sleep": '#!/bin/sh\nexit 0\n',
                }
                for name, source in commands.items():
                    path = adapters / name
                    path.write_text(source)
                    path.chmod(0o700)
                arguments = root / "arguments"
                tape_name = "Acceptance TST001 [*] 中文"
                env = {**os.environ, "PATH": str(adapters) + os.pathsep + os.environ["PATH"],
                       "TAPE_DIR": str(root / "Tape artifacts"), "TAPE_BARCODE": "TST001",
                       "TAPE_NAME": tape_name, "ARGUMENTS": str(arguments), "FORMAT_EXIT": str(exit_code)}
                result = subprocess.run(["bash", str(adapters / "mkfs")], env=env,
                                        capture_output=True, timeout=10)
                self.assertEqual(result.returncode, exit_code, result.stderr.decode())
                self.assertEqual(arguments.read_bytes().split(b"\0")[:-1],
                                 [b"-f", b"-d", b"/dev/sg7", b"-s", b"TST001", b"-n", tape_name.encode()])


if __name__ == "__main__":
    unittest.main()
