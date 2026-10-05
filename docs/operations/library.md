# Library and Mounted Volume Workflows

These workflows cover Library browsing and archive operations. See the [Location guide](locations.md) for registered originals, live browsing, and Library-selected Archive.

## Browse from the CLI

Use `ls` with a logical Library path or the display name of an existing registered Location:

```shell
yatm-cli ls
yatm-cli ls /Photos/2026
yatm-cli ls library:///Photos/2026
yatm-cli ls location://NAS
yatm-cli ls location://NAS/DCIM
yatm-cli ls /Photos/2026 --query 'name:*.jpg' --limit 20
```

The bare command lists the Library root; `Photos/2026` and `./Photos/2026` also start
there, regardless of the client's working directory. In the Location examples, `NAS`
is the exact, case-sensitive display name on the YATM server selected by `--server`,
and `DCIM` is below its registered root. `location://NAS/` also selects the root.
For duplicate display names, use `yatm-cli ls --location-id ID --path PATH`.

Quote spaces, for example `yatm-cli ls 'location://Home NAS/DCIM'`. A literal Library
path such as `'/Photos/100% ready?#'` can be written as the URI
`'library:///Photos/100%25 ready%3F%23'`; plain paths keep those characters literally.
Names containing a colon are also literal: `yatm-cli ls library:2026` selects the same
directory as `yatm-cli ls /library:2026`.
The [CLI path rules](../architecture/cli.md#directory-paths) define decoding,
root containment and selector conflicts.

An ordinary listing reads the complete directory. `--query` and Library `--recursive`
use paged Search; `--recursive` can be used without a query expression. Continue a
search with its returned `--cursor`. Existing `--file-id` and
`--location-id`/`--path` selectors remain available without a positional operand.
These positional paths apply only to `ls`; other commands keep their existing syntax.

## Mounted Volume

Configure `paths.volumes` in [configuration](../../config.example.yaml) to contain already-mounted roots. Use **Media → Add Volume**:

1. Choose a disk from the discovered candidates; an unreadable marker stays listed with its reason, and **Enter a path manually…** keeps the previous explicit form.
2. **Initialize New** creates the immutable marker and Library Media row, prefilling the name and the serial number read from the device. **Register Existing** reads an existing marker after metadata-only deletion and recreates the Media row; the marker and files stay unchanged.
3. Archive copies ordinary files to the Volume. HDD supports concurrent random writes; HM-SMR writes sequentially.
4. Restore copies selected FileVersions to a locally confirmed Location and chosen subdirectory; target recommendations are not permissions. [Restore](../architecture/jobs.md#restore) defines automatic association and collision handling.
5. Scan automatically validates and publishes added, changed, and removed inventory paths. Its completed Job retains paginated changes; **Signatures → Read every file** bypasses caches during observation. Unavailable or failed scans retain the old inventory. There is no Apply step.
6. Media Delete removes only catalog metadata. Register plus a subsequent Scan can rebuild metadata from an unchanged marker and filesystem.

The OS owns mount/unmount/eject. `yatm-cli volume candidates` lists the same discovery candidates. A discovery root or its immediate child can be initialized/registered; duplicate mounted UUIDs, changed profiles, symlink path components, and escapes are rejected. See [Media/I/O](../architecture/media-io.md) for identity, integrity, and capacity guarantees.

## Organization and Search

Tags and notes belong to logical File/directory identity, not content or shared archive copies. Rename, move, and Trash preserve annotations without changing physical files. [Library](../architecture/library.md) defines normalization, limits, versions and cleanup protection.

Search runs in the active pane and keeps the other pane available for **Locate in Other Pane**. Fields are `name`, `tag`, `note`, `type`, `size`, `mtime`, `location`, and `has`; bare terms match name, Tag, or note. Use `location:<id>` with a positive Location ID; the [Library search contract](../architecture/library.md#annotation-search-and-cleanup) defines Location and content-presence predicates. Adjacent conditions mean AND. Boolean operators, parentheses, and `*`/`?` wildcards use the query parser's native rules. Use decimal bytes and quoted RFC3339 timestamps:

```text
name:report AND (tag:finance OR note:"final")
type:file AND size:>=1048576
mtime:>="2026-01-01T00:00:00Z" AND NOT tag:obsolete
```

Export creates a Library metadata backup, not a copy of physical content or Settings. Import replaces declared entity types transactionally, always preserves the target installation's Settings, and preserves prior Library data on invalid references or unique-key conflicts; review the [backup contract](../architecture/library.md#backup-and-legacy-compatibility) before replacement.

Open **Tools → Identical files**, or **Find identical files** from Files. Nothing is queried until **Find** runs in the tool; changing the source or scope clears the results. Library matches current and saved-version content, including transitive relationships. Results use one scrollable grouped list and load visible rows automatically. Select one target and choose **Merge into** to combine the whole group and its version history. The Locations source searches complete selected Locations; search by name and see each selection as a chip. Choose **Keep this** for one survivor or **Delete** for a selection. Physical removals use each Location's Trash. Completed removals disappear without repeating Find; a changed group needs another explicit Find before Keep or Merge. Click a member for the ordinary right-side Inspector.

```shell
yatm-cli identical groups --source library --limit 20
yatm-cli identical groups --source locations --root 12 --root 18
yatm-cli identical groups --source library --result <result-id> --cursor <cursor> --limit 20
yatm-cli identical members --source library --result <result-id> --group <id> --limit 50
yatm-cli identical find --source library
yatm-cli identical rows --result <result-id> --offset 100 --limit 100
yatm-cli identical rows --result <result-id> --sort-key size --offset 100 --limit 100
yatm-cli identical positions --result <result-id> --file-id <id>
yatm-cli identical close --result <result-id>
yatm-cli identical merge --group <id> --fingerprint <fingerprint> --target-file <id> [--dryrun]
yatm-cli identical keep --source locations --root 12 --root 18 --group <id> --fingerprint <fingerprint> --keep-location 12 --keep-path Photos/image.jpg [--dryrun]
yatm-cli files remove-version --file-id <id> --version-id <id> [--dryrun]
```

The first groups or members reply includes a `result_id`; pass it as `--result` with the returned `--cursor` to continue without recalculating. `find` also returns a result ID and row counts for offset reads; `close` releases it early. A result expires after 30 minutes idle or a service restart, and then requires a new Find. Reuse the exact source/root scope and current fingerprint for mutations; changed groups require refresh. Keep processes unloaded members too. Version removal deletes the catalog record only; later existing coverage reconciliation can recreate it.

## Restore Versions

Add files or directories from multiple folders to the Restore waitlist. Ordinary
selections follow **Latest saved version**, or **At or before** a local date/time
chosen in the Restore dialog's application-owned calendar. Double-click a regular waitlist entry or use **Change version**
to choose explicit content. Custom versions remain selected when the global time
changes; **Apply current policy to all** returns them to automatic selection.

Files without a matching dated version remain in the waitlist. Change the cutoff,
choose an explicit version, remove the entry, or explicitly skip unmatched entries
before preparing. A version with no usable copy remains an error; skipping a date
mismatch never substitutes newer content or bypasses a copy failure. Selecting a
date chooses recorded file content, not a historical directory snapshot. Older
metadata may retain only first/last archive dates, not every intermediate save.

The CLI uses RFC3339 with an explicit timezone; the cutoff is inclusive:

```shell
yatm-cli restore estimate --file-id 12 --before '2026-09-01T23:59:59+08:00'
yatm-cli restore create --file-id 12 --before '2026-09-01T23:59:59+08:00' --skip-unmatched-versions --target-location 7
yatm-cli restore create 42 --file-id 12 --before '2026-09-01T23:59:59+08:00' --target-location 7
```

The last example pins version 42 for its File while other Files under selection
12 follow the cutoff. Omit `--skip-unmatched-versions` to keep missing time matches
blocking. Creation prepares a frozen Job; later waitlist/policy changes do not
change that Job's selected content. [Restore execution](../architecture/jobs.md#restore)
owns output and retry rules.

## Preview and Hash Cache

Preview generation is an optional Scan stage. Archive can request a companion Scan from its frozen input manifest. Signature policy selects cache-only reuse, filling missing content facts, or uncached reads. Preview policy independently selects no generation, missing-only generation, or regeneration of every eligible bundle. Cache-only misses do not trigger hashing. Existing Preview metadata is read independently with `preview get`. See [Preview](../architecture/preview.md) for bundle ownership and [ACP integrity](../architecture/media-io.md#acp-and-integrity) for cache versus verification guarantees.
