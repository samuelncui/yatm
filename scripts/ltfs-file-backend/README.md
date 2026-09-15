# Official LTFS File-Backend Adapters

These optional testing adapters are packaged under `templates/testing/ltfs-file-backend`; the installer does not activate them. They require Linux, official LTFS with its `file` backend, FUSE, `fusermount`, `mountpoint` and `realpath`.

Create a dedicated directory named with a six-character uppercase alphanumeric barcode, such as `YAT001`, inside an isolated test root. Configure it as a Tape device and point all five `scripts` commands at this adapter set. The directory name supplies the virtual cartridge identity; no physical Tape device is accepted and hardware encryption is not simulated.

Formatting requires an empty directory, apart from an optional official `filedebug_tc_conf.xml` cartridge configuration. A capacity fixture may supply that XML before formatting. The adapter never deletes or resets a previously used cartridge directory: use a new scoped fixture for each format operation. LTFS work files and captured indexes use the supplied `TAPE_DIR`.

Use these scripts for release-package acceptance of format, append, restore and integrity checking. Fault injection remains in the local/CI E2E harness, outside the release templates. The source repository and release package both contain the testing guide at `docs/operations/testing.md`.
