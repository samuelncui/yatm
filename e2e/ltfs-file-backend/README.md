# Official LTFS File-Backend Adapters

These optional testing adapters are packaged under `templates/testing/ltfs-file-backend`; the installer does not activate them. They require Linux, official LTFS with its `file` backend, FUSE, `fusermount`, `mountpoint` and `realpath`.

After installing the isolated package, copy this adapter set under its `scripts/virtual-tape` directory and configure all five `scripts` commands there. This keeps the configured adapters in the complete installation backup and preserves them during replacement. Create a dedicated directory named with a six-character uppercase alphanumeric barcode, such as `YAT001`, outside the installation but inside the isolated test root, and configure it as a Tape device. The directory name supplies the virtual cartridge identity; no physical Tape device is accepted and hardware encryption is not simulated.

Formatting requires an empty directory, apart from an optional official `filedebug_tc_conf.xml` cartridge configuration. A capacity fixture may supply that XML before formatting. Use a new scoped fixture for each format operation. LTFS work files and captured indexes use the supplied `TAPE_DIR`, resolved to an absolute path and passed as `capture_index=<directory>` to official LTFS.

Resolve the selected `mkltfs` and `ltfs` programs to absolute paths in the isolated adapter copies before starting systemd. The service then uses the same implementation checked by the controller. Normal unmount waits for both FUSE detachment and the mount's LTFS process to finish; the final captured Index is available for publication only after both complete.

For older capture-enabled builds that accept only `-o capture_index`, set
`YATM_E2E_LTFS_CAPTURE_INDEX=bare` in the native test or package-controller environment. Current
official LTFS uses the default `directory` form. The package controller records this choice in the
installed mount script, including for SSH execution, so systemd uses the selected syntax. Select
the installed build's syntax during preparation and record it with the executable identity.

Use these scripts for release-package acceptance of format, append, restore and integrity checking. Fault injection remains in the local/CI E2E harness, outside the release templates. See the public [development and release guide](https://github.com/samuelncui/yatm/blob/v1/docs/operations/testing.md).
