"""Exercise shipped Tape shell adapters without a drive or LTFS installation."""

import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import time
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


class DeviceScriptTests(unittest.TestCase):
    def test_kernel_mapping_does_not_probe_the_busy_drive(self):
        script = Path(__file__).resolve().parents[1] / "scripts" / "get_device"
        cases = (("/dev/nst7", 1, 0), ("/dev/st7", 1, 0), ("/dev/nst7m", 1, 0),
                 ("/dev/tape/by-id/scratch", 1, 0), ("/dev/sda", 1, 1),
                 ("/dev/nst7", 0, 1), ("/dev/nst7", 2, 1))
        for device, nodes, expected in cases:
            with self.subTest(device=device, nodes=nodes), tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary)
                sysfs = root / "kernel device"
                generic = sysfs / "scsi_generic"
                generic.mkdir(parents=True)
                for number in range(nodes):
                    (generic / f"sg{7 + number}").mkdir()
                commands = {
                    "readlink": '''#!/bin/sh
test "$1" = -f && shift
test "$1" = -- && shift
case "$1" in
  /dev/tape/by-id/scratch) printf '%s\\n' /dev/nst7 ;;
  /dev/*) printf '%s\\n' "$1" ;;
  /sys/class/scsi_tape/st7/device) printf '%s\\n' "$KERNEL_DEVICE" ;;
  *) exit 1 ;;
esac
''',
                    "sg_map": '#!/bin/sh\n: > "$PROBED_DEVICE"\nprintf "%s\\n" "/dev/sg7  busy"\n',
                }
                for name, source in commands.items():
                    path = root / name
                    path.write_text(source)
                    path.chmod(0o700)
                probe = root / "probe"
                env = {**os.environ, "PATH": str(root) + os.pathsep + os.environ["PATH"],
                       "DEVICE": device, "KERNEL_DEVICE": str(sysfs), "PROBED_DEVICE": str(probe)}
                result = subprocess.run(["bash", str(script)], env=env, capture_output=True, timeout=10)
                self.assertEqual(result.returncode, expected, result.stderr.decode())
                self.assertEqual(result.stdout, b"/dev/sg7\n" if expected == 0 else b"")
                self.assertFalse(probe.exists(), "Resolving an in-use device must not probe it.")


@unittest.skipUnless(sys.platform == "linux", "Virtual LTFS adapters use Linux process state.")
class VirtualUnmountTests(unittest.TestCase):
    def test_waits_for_final_index_after_fuse_detaches(self):
        script = Path(__file__).resolve().parent / "ltfs-file-backend" / "umount"
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            device, tape, mount = root / "TST001", root / "Tape artifacts", root / "mount [*]"
            for directory in (device, tape, mount):
                directory.mkdir()
            trigger, ready, index = root / "detached", root / "ready", tape / "TST001.schema"
            for name, source in {
                "fusermount": '#!/bin/sh\n: > "$DETACHED"\n',
                "mountpoint": '#!/bin/sh\ntest ! -e "$DETACHED"\n',
            }.items():
                executable = root / name
                executable.write_text(source)
                executable.chmod(0o700)
            code = '''import pathlib,sys,time
trigger,ready,index = map(pathlib.Path, sys.argv[1:4])
ready.touch()
while not trigger.exists(): time.sleep(0.01)
time.sleep(0.2)
index.write_text("final Index")
'''
            # argv[0] and the final argument identify only this mount's LTFS process.
            process = subprocess.Popen(["ltfs", "-c", code, str(trigger), str(ready), str(index), str(mount)],
                                       executable=sys.executable)
            try:
                deadline = time.monotonic() + 5
                while not ready.exists() and process.poll() is None and time.monotonic() < deadline:
                    time.sleep(0.01)
                self.assertTrue(ready.exists(), "The simulated LTFS process did not start.")
                env = {**os.environ, "PATH": str(root) + os.pathsep + os.environ["PATH"],
                       "DEVICE": str(device), "TAPE_DIR": str(tape), "MOUNT_POINT": str(mount),
                       "DETACHED": str(trigger)}
                result = subprocess.run(["bash", str(script)], env=env, capture_output=True, timeout=10)
                self.assertEqual(result.returncode, 0, result.stderr.decode())
                self.assertEqual(index.read_text(), "final Index")
                process.wait(timeout=5)
            finally:
                if process.poll() is None:
                    process.kill()
                process.wait()

    def test_detaches_failed_mount_without_accepting_its_write(self):
        script = Path(__file__).resolve().parent / "ltfs-file-backend" / "umount"
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            device, tape = root / "TST001", root / "Tape artifacts"
            device.mkdir()
            tape.mkdir()
            trigger = root / "detached"
            for name, source in {
                "fusermount": '#!/bin/sh\n: > "$DETACHED"\n',
                "mountpoint": '#!/bin/sh\ntest ! -e "$DETACHED"\n',
            }.items():
                executable = root / name
                executable.write_text(source)
                executable.chmod(0o700)
            env = {**os.environ, "PATH": str(root) + os.pathsep + os.environ["PATH"],
                   "DEVICE": str(device), "TAPE_DIR": str(tape), "MOUNT_POINT": str(root / "failed mount"),
                   "DETACHED": str(trigger)}
            result = subprocess.run(["bash", str(script)], env=env, capture_output=True, timeout=10)
            self.assertNotEqual(result.returncode, 0)
            self.assertTrue(trigger.exists(), "Failed mounting still requires FUSE detachment.")
            self.assertIn("write result is unusable", result.stderr.decode())


if __name__ == "__main__":
    unittest.main()
