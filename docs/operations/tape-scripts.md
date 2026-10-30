# Tape Scripts

Configured Tape scripts adapt YATM to the local LTFS distribution, drive and helpers. This guide
owns their inputs, outputs and completion requirements; the
[Tape contract](../architecture/tape.md#tape-script-compatibility) owns the compatibility policy.

## Responsibilities

Scripts are the environment adaptation boundary: they own LTFS executable paths, device mapping, vendor options and local helper logic. Preserve that customization. Installation checks never run Tape scripts, mount a cartridge, format Media or establish physical Tape readiness.

## Inputs and Outputs

| Input | Meaning |
| --- | --- |
| `DEVICE` | Configured Tape device; helpers may resolve its SCSI generic mapping. |
| `KEY_FILE` | Temporary owner-only key file supplied to `scripts.encrypt` for both LTFS and tar; removed by operation cleanup. |
| `MOUNT_POINT` | Mount destination for the current operation. |
| `TAPE_BARCODE`, `TAPE_NAME` | Cartridge identity/name passed to formatting and encryption scripts. |
| `TAPE_DIR` | Per-Job, per-cartridge artifact directory once identity is known; write captured indexes and LTFS diagnostics here. |
| `OUT` | Output filename for the device-information script's JSON result. |

Device-information scripts write JSON containing `barcode` to `OUT`. The service accepts the
historical full cartridge label (for example `ABC001L5`), takes its first six characters and normalizes
the identity to uppercase. Explicit user-supplied Tape identities remain six-character values.
Successful `{"barcode":""}` output means the cartridge memory barcode is unassigned: FORMAT uses the
explicit user-supplied barcode and writes it through the formatting script. APPEND and Restore
require an existing device identity. An unreadable field or failed probe exits nonzero rather than
reporting an unassigned barcode. FORMAT still erases existing content regardless of barcode state.
Check this distinction in customized probes: historical shell pipelines can hide a failed helper
and return an empty barcode with exit status zero. Preserve local device/helper choices while
propagating failures; the [bundled probe](../../scripts/readinfo) shows an explicit field check.
Scripts run from the service working directory. Migration resolves literal relative capture options
from that same directory, independently of `paths.work`; offline backup validation maps this path
into the preserved installation.

## Mount and Unmount

Choose the bundled mount template that matches the installed LTFS distribution:

| LTFS distribution | Mount template | Index capture arguments |
| --- | --- | --- |
| HPE or older capture-enabled builds using the bare option | [`scripts/mount`](../../scripts/mount) | `-o work_directory="${TAPE_DIR}" -o capture_index` |
| Current official LTFS using a capture directory | [`scripts/mount.openltfs`](../../scripts/mount.openltfs) | `-o work_directory="${TAPE_DIR}" -o capture_index="${TAPE_DIR}"` |

The mount script must make the current cartridge's captured Index available as `TAPE_DIR/<barcode>.schema`, where `<barcode>` is the mounted cartridge identity. Mount/unmount receive `TAPE_DIR`, not the formatting script's barcode/name inputs. The final Index must describe the completed write before YATM publishes it.

Create `TAPE_DIR`, then resolve it to an absolute path before passing LTFS its directory options, so background execution does not reinterpret a relative `paths.work`:

```shell
mkdir -p -- "${TAPE_DIR}"
TAPE_DIR=$(cd -- "${TAPE_DIR}" && pwd -P)
```

The [bundled mount template](../../scripts/mount) illustrates this contract; its flags are not universal across LTFS distributions.

Unmount must return only after final Index output is complete, the mount has ended, and the Tape device has been released. A successful `umount` invocation alone may precede background LTFS cleanup. An ejecting setup must also wait for its configured eject completion condition. The [bundled unmount template](../../scripts/umount) shows one bounded wait implementation; retain the equivalent behavior appropriate to the local environment. Nonzero exit or timeout reports failure rather than claiming successful publication.

## Bundled Templates

Bundled mount scripts resolve the Job's Tape artifact directory to an absolute path before passing it to LTFS. LTFS can change its working directory when it runs in the background; captured indexes must still land in the supplied Job directory when `paths.work` is relative.

The official LTFS file-backend adapters select a supported capture option at setup: an absolute
directory value, or bare `capture_index` with an absolute work directory for older builds. They wait
for their mount's LTFS process to exit after FUSE detaches. FUSE detachment alone can precede the final Index
write; the test adapter therefore supplies the same completed-finalization boundary as physical
device release.

The bundled encryption script loads the drive and retries `stenc` at most 60 times. If
every attempt fails, it exits nonzero so the Tape Session cannot proceed. Tar and LTFS both use
this adapter contract; the native tar provider does not replace it with a key-management protocol. Its `stenc` arguments remain part of the environment-specific script contract.
The script includes a commented stenc 2.0.0 command, which reads the barcode descriptor
from the key file's second line instead of accepting `-kd`; operators select the form
supported by their local tool.

Installation checks never run Tape scripts. A successful installer does not certify customized
scripts or physical hardware.
