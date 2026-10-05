# Locations, Originals and File Versions

Status: v1 development workflow. [Library](../architecture/library.md) owns organization and content; [Jobs](../architecture/jobs.md) owns execution guarantees.

## Register and Browse

1. Open **Settings → Locations → Add location** and choose a name and authorized server directory. **Browse directories** opens a read-only chooser; choosing a directory changes the draft and does not register it until Save. **Restore destination** recommends it in the chooser; it is not write permission.
2. Configure **Ignore** with gitignore text. Matching entries are hidden from live browsing, excluded from measurement and from Scan/Archive selections (including an explicitly named path), and never become new originals or Restore links. Existing associations and writes are unaffected: explicit admission remains available, and Restore still writes its output. Required runtime/archive exclusions cannot be bypassed.
   Under **Advanced**, optionally enable **Use mmap for content reads**. Mmap is off by default and applies when Archive or Scan reads original file content, including Archive preparation hashes; it does not change ordinary directory browsing.
3. Save. The directory is immediately browsable and usable, whether registered or imported. Registration does not collect files or create a Job.
4. Choose the Location in Files. Both panes use the same source while retaining separate directories. Pages show actual files, empty/dot directories and optional Library associations. An unreadable entry stays visible with its error and cannot be operated on; other rows remain usable. A directory-level read failure clears that pane and provides Retry, never an offline listing.

File names preserve legal UTF-8 text, including backslashes, leading/trailing spaces and
control characters. The current string protocol cannot operate on non-UTF-8 filename bytes:
those names appear as escaped error rows and are still included in the directory total.
Display escapes are not paths to copy into an operation. Native filesystem restrictions
still apply when creating or restoring a name.

    yatm-cli location create --name Documents --root /allowed/documents --ignore-file ./ignore.txt
    yatm-cli ls location://Documents/photos
    yatm-cli ls --location-id 2 --path photos --query "type:file AND size:>1M" --limit 100
    yatm-cli files get --location-id 2 --path photos/example.jpg

`ls` accepts the Location's exact display name in its URI, or the existing ID flags.
Add `--use-mmap` to `location create` or `location update` when mapped original reads are desired; an update replaces the complete configuration, so repeat the Location's other desired flags and Ignore file.
The [CLI browsing examples](library.md#browse-from-the-cli) explain Library paths,
quoting and duplicate Location names.

A listing reads the directory completely and writes one JSON Lines message per batch, so it takes no page size and no cursor. A query is a paged Search that keeps both: continuing it uses the cursor it returned. Reload after navigation or when the directory itself is replaced. Paths refer to the server except CLI input/output files. `ls` is a pure, minimal read; `-l` requests attributes and `--status` requests availability. **Add to Library** opens Scan with known-only signatures and original publication. `files metadata` admits an uncollected entry when saving annotations. File details do not open full bytes through HTTP. See the [shared Files contract](../architecture/library.md#shared-organization-interface).

## Independent Settings

**Settings → Library** separates two default-on preferences:

- **Include unbacked files** affects only Library visibility. Saved-only mode retains editable logical directories and Files with any saved version, including versions whose last copy is lost. Location browsing and duplicate groups are independent.
- **Confirm Delete** controls the browser's move-to-Trash dialog. Server checks always run; a CLI mutation reports its resolved plan under `--dryrun` and writes when the flag is omitted.

    yatm-cli settings library
    yatm-cli settings library --confirm-remove=false
    yatm-cli ls --file-id 0 --scope saved

The CLI reads the current Library group before applying supplied fields, so omitted flags preserve other preferences. Startup never silently traverses existing roots. There is no watcher or periodic full scan.

## Scan and Find Duplicates

Use **Scan** for selected files, directories, a Location or Media. Choose the signature policy and result handling independently; Preview is an optional stage of the same Job. Refresh only reads the current directory.

    yatm-cli scan create --location-id 2 --path photos --signature fill-missing --result originals
    yatm-cli job wait 9 --wait-timeout 2m
    yatm-cli scan results 9 --limit 100

`--signature known-only` reuses applicable facts or cache entries without hashing on a miss; `fill-missing` reads unknown or changed files; `force-read` reads every selected ordinary file. `--result report` leaves Library facts unchanged, while `originals` publishes successful original observations. `--compare-library` looks up matching content. `--preview-policy missing-only` generates missing derivatives; `regenerate-all` also replaces existing ones, and `none` disables generation. Without a usable content identity, known-only skips Preview rather than hashing implicitly. See [Scan policies and publication boundaries](../architecture/jobs.md#scan).

`analyze create` and `preview create` are convenient presets for this same Scan service and Job kind, not separate runners. `analyze create --mode basic/incremental/force` selects known-only/fill-missing/force-read and publishes originals; `preview create` publishes originals with Preview enabled. Use `scan create --help` for the full combination of controls.

Jobs continue after navigation. Scan stops at the first failed Location and leaves that Location's prior records intact; earlier fully published Locations remain committed. A failed or cancelled preparation is terminal: delete it and create a new Scan, which reads the complete selection again. A Scan is neither a filesystem snapshot nor a durable archive.

**Duplicates in Locations** groups known signatures without automatic full-disk hashing. Expanded pages check members and distinguish confirmed, changed and unavailable entries; unopened ranges remain unchecked. Scan selected ranges to improve coverage. Unknown content does not mean unique content. Duplicate Files keep independent organization, tags and Note.

## Physical Operations and Organization

Library and Location panes share cut/paste, rename/move, mkdir and deletion; user-level Copy is not offered. Both panes must use the same source. Same-name directories merge recursively; file and type conflicts fail without overwrite, even for identical content. A known conflict leaves that selected root untouched. Roots, nested registrations, runtime/archive resources, submounts and unauthorized paths are protected. Symlinks are operated on as links, never followed.

Library actions only change logical organization. A physical move keeps the File's logical path and annotations; merged targets retain their own original associations. Delete moves Library entries to logical Trash or physical entries to the owned `.trash/<unique-id>/<name>` directory. Physical removal disconnects originals while preserving Library organization and saved versions. Directories move whole, including ignored contents. Trash remains browsable and occupies disk space; `mv` can move its content out without automatically reconnecting the old File. Scan, Archive and Restore targets exclude Trash. Ordinary file operations do not offer permanent deletion.

These actions use the [shared organization engine](../architecture/library.md#shared-organization-interface), without creating Jobs. Short edits show no progress banner; longer work exposes Cancel in a fixed notification without moving the panes. Name dialogs remain open until completion and retain errors. Leaving the page or losing the request cancels pending work; completed items remain. Inspect partial results and refresh before any new attempt. [Outcome rules](../architecture/library.md#physical-file-operations) distinguish physical failure from incomplete Library publication.

    yatm-cli mkdir --location 2 --destination . --name Selected
    yatm-cli mv --location 2 --source photos/example.jpg --destination Selected
    yatm-cli mv --library --source 42 --destination 43
    yatm-cli rm --location 2 --source Selected/example.jpg [--dryrun]
    yatm-cli files locate-original <file-id> --location-id 2 --path moved/example.jpg [--dryrun]

`mv`, `mkdir` and `rm` emit JSON Lines containing item results and a final summary. They exit nonzero on interruption, failed/unprocessed items or incomplete Library publication. A completed `--dryrun` plan exits successfully; its planned items remain `UNPROCESSED` because no changes were requested. Set a suitable request timeout for large directory operations; no separate execute, wait or resume command is required.

**Locate original** changes an association, not a disk path. It refuses a target owned by another File. Use it when limited observations cannot reliably reconnect an external move. Automatic continuity follows path → valid available signature → native identity within the observed range; it cannot prove every move-versus-copy history.

## Archive, Preview and Restore

Archive and Scan with Preview accept Library selections or live Location entries without prior content analysis. Accumulate Archive files/directories across folders in the Chonky waitlist. A Library File without an original association has no Add to Archive action; a directory can be added without checking its descendants. For Library selections, the estimate counts only Files with original associations and reports skipped Files separately; live Location selections count their observed ordinary files. Preparation skips those same Files and requires at least one eligible regular File. Expansion is bounded, overlap is deduplicated, and unknown sizes are reported separately. Archive preparation establishes File identities and actual content and freezes Library-relative archive targets. Physical selection does not imply mirrored archive paths.

    yatm-cli archive create --location 2:photos --location 3:reports
    yatm-cli archive create --file-id 42 --file-id 43
    yatm-cli files versions 42
    yatm-cli restore create 17 18 --target-location 3 --directory recovered

Preparation alone is not Archive completion. Choose Media on the resulting Job to copy and verify. An associated original that cannot be read is an error, never a substitution by another duplicate File. Preview failure does not undo analysis or archival.

Restore selects FileVersions and permits confirmed authorized destinations without previous analysis. The browser chooser lists Preferred Locations, remembers the last chosen directory and revalidates it when reopened; it does not collect browsed files. The [Restore contract](../architecture/jobs.md#restore) owns candidates, frozen paths, collisions, actual-content verification and automatic association. Existing originals are never displaced; ignored outputs stay unlinked. Explicit damaged-copy recovery never labels mismatched bytes as a verified version.

## Inventory, Integrity and Metadata Backup

- Media Scan selects `--result inventory` to publish inventory, or `--result verify` to check recorded copies. Both use the same pipeline; verification forces actual reads against the saved baseline. Unknown archives require explicit **Add to Library**.
- `scan media <media-id>` is the inventory preset; `verify create <media-id>` is the integrity preset. Mounted Volume work runs automatically. Tape waits for an explicitly selected matching device through `scan run <job-id> --device <device>`; the CLI never chooses or formats a device.
- Inventory does not claim healthy contents, and integrity findings do not accept damaged checksums as new expectations. See [historical checks and Media health](../architecture/media-io.md#copy-health).
- Unregistering a Location removes executable original/tracking associations, not disk bytes, Files, versions, copies or Preview. Failed access alone removes nothing.
- Library metadata export includes Library management metadata, not contents, Settings, operation results or Job bundles. Import is a quiesced operator action taken with no Job running; it validates live relationships, rolls back on conflicts, preserves the target Settings and keeps imported registrations immediately usable. Imported facts cannot authorize local retries.

The [compatibility policy](../README.md#pre-stable-compatibility) preserves v0.1.x migration/import. Incompatible pre-stable v1 stores are rejected before modification and require an explicitly chosen conversion or reinstall. Cross-Location transfer, uploads, editing, permissions, scheduling, multi-Executor transport and snapshots are outside this workflow.
