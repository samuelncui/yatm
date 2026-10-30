# Library and Mounted Volumes

These workflows cover Library browsing, organization and search, mounted Volumes, metadata backup and identical-file tools under the [daily-use constraints](index.md#daily-use). [Archive, Restore and Scan](jobs.md) owns those Jobs; the [Location guide](locations.md) owns registered originals and live browsing.

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
The [CLI path rules](cli.md#directory-paths) define decoding,
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

Search runs in the active pane and keeps the other pane available for **Locate in Other Pane**. Fields are `name`, `tag`, `note`, `type`, `size`, `mtime`, `location`, and `has`; bare terms match name, Tag, or note. Use `location:<id>` with a positive Location ID; `has:original`, `has:archive` (known archive coverage, not a health check), `has:unknown` and `has:duplicates` select by content presence; the [Library search contract](../architecture/library.md#annotation-search-and-cleanup) defines them. Adjacent conditions mean AND. Boolean operators, parentheses, and `*`/`?` wildcards use the query parser's native rules. Use decimal bytes and quoted RFC3339 timestamps:

```text
name:report AND (tag:finance OR note:"final")
type:file AND size:>=1048576
mtime:>="2026-01-01T00:00:00Z" AND NOT tag:obsolete
```

## Library Settings

**Settings → Library** separates two default-on preferences:

- **Include unbacked files** affects only Library visibility. Saved-only mode retains editable logical directories and Files with any saved version, including versions whose last copy is lost. Location browsing and duplicate groups are independent.
- **Confirm Delete** controls the browser's move-to-Trash dialog. Server checks always run; a CLI mutation reports its resolved plan under `--dryrun` and writes when the flag is omitted.

    yatm-cli settings library
    yatm-cli settings library --confirm-remove=false
    yatm-cli ls --file-id 0 --scope saved

The CLI reads the current Library group before applying supplied fields, so omitted flags preserve other preferences. Startup never silently traverses existing roots. There is no watcher or periodic full scan.

## Trim

`yatm-cli library trim --positions [--files] [--dryrun]` removes invalid Positions and, with
`--files`, Files with neither an original nor saved history. Review the dry-run report first; saved version history is
kept. The [Trim contract](../architecture/library.md#trim) owns the exact rules.

## Metadata Backup

Export creates a Library metadata backup, including Locations, original links and saved Media encryption keys, not physical content or global preferences. Protect it like the keys it contains. Import replaces the entity types declared by that backup, including Location configuration when present. It preserves global Library/Preview/Job/Device preferences and rolls back on invalid references or unique-key conflicts; review the [backup contract](../architecture/library.md#backup-and-legacy-compatibility) before replacement.

Use `yatm-cli library import --input library.jsonl --dryrun` to validate a downloaded backup without
replacing Library data. Downloads remain streamed on all supported HTTP versions. With an HTTP/1.0
connection, including a proxy's upstream connection, a truncated download can appear successful;
import validation rejects the missing JSONL end record.

Import is a quiesced operator action taken with no Job running. It validates live relationships,
keeps imported registrations immediately usable, and imported facts cannot authorize local retries.

## Identical Files

Open **Tools → Identical files**, or **Find identical files** from Files. Nothing is queried until **Find** runs in the tool; changing the source or scope clears the results. Library matches current and saved-version content, including transitive relationships. Results use one scrollable grouped list and load visible rows automatically. Select one target and choose **Merge into** to combine the whole group and its version history. The chosen File retains its original; other File identities move to Library Trash and lose their original links while their disk bytes remain. The Locations source searches complete selected Locations; search by name and see each selection as a chip. Choose **Keep this** for one survivor or **Delete** for a selection. Physical removals use each Location's Trash. Completed removals disappear without repeating Find; a changed group needs another explicit Find before Keep or Merge. Click a member for the ordinary right-side Inspector.

**Duplicates in Locations** groups known signatures without automatic full-disk hashing. Expanded pages check members and distinguish confirmed, changed and unavailable entries; unopened ranges remain unchecked. Scan selected ranges to improve coverage. Unknown content does not mean unique content. Duplicate Files keep independent organization, tags and Note.

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

The first groups or members reply includes a `result_id`; pass it as `--result` with the returned `--cursor` to continue without recalculating. `find` also returns a result ID and row counts for offset reads; `close` releases it early. A result expires after 30 minutes idle or a service restart, and then requires a new Find. Reuse the exact source/root scope and current fingerprint for mutations; changed groups require refresh. Keep acts on the whole current group, including members not yet loaded. Version removal deletes the catalog record only; later existing coverage reconciliation can recreate it.
