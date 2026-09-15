# Locations, Originals and File Versions

Status: v1 Alpha 1 workflow. [Library](../architecture/library.md) owns organization and content; [Jobs](../architecture/jobs.md) owns execution guarantees.

## Register and Browse

1. Open **Settings → Locations → Add location** and choose a name and authorized server directory. **Restore destination** recommends it in the chooser; it is not write permission.
2. Configure **Ignore** with gitignore text. It applies to automatic collection and recursive Scan/Backup, not live visibility or physical-operation authorization. Required runtime/archive exclusions cannot be bypassed.
3. Save. The directory is immediately browsable; only imported roots require local confirmation. When auto collection is on, registration also starts a metadata Scan without blocking browsing.
4. Choose the Location in Files. Both panes use the same source while retaining separate directories. Pages show actual files, empty/dot directories and optional Library associations. Errors clear that pane and provide Retry, never an offline listing.

    yatm-cli location create --name Documents --root /allowed/documents --ignore-file ./ignore.txt
    yatm-cli files list --location-id 2 --path photos --limit 100
    yatm-cli files get --location-id 2 --path photos/example.jpg

Continue with the returned cursor; reload after directory/binding changes. Paths refer to the server except CLI input/output files. `files list` is a pure read; `files collect` explicitly admits entries, and `files metadata` admits an uncollected entry when applying annotations. Open uses a guarded content reference and needs neither an existing File ID nor signature. See the [shared Files contract](../architecture/library.md#shared-organization-interface).

## Independent Settings

**Settings → Library** separates three default-on preferences:

- **Automatically collect Location files** creates basic records without hashing everything. Disabling retains Files; explicit annotation, collection, a Scan publishing originals, and Backup still collect selected inputs. Enabling offers basic collection for confirmed Locations and shows Jobs/errors. Disabling does not cancel active work.
- **Include unbacked files** affects only Library visibility. Saved-only mode retains editable logical directories and Files with any saved version, including versions whose last copy is lost. Location browsing and duplicate groups are independent.
- **Confirm permanent deletion** controls the browser dialog. Server checks always run; CLI deletion still requires its explicit confirmation flag.

    yatm-cli settings library
    yatm-cli settings library --auto-collect=false --revision <current-revision>
    yatm-cli files list --file-id 0 --scope saved

Updates use the returned revision; omitted CLI flags preserve other preferences. Startup never silently traverses existing roots. There is no watcher or periodic full scan.

## Scan and Find Duplicates

Use **Scan** for selected files, directories, a Location or Media. Choose the signature policy and result handling independently; Preview is an optional stage of the same Job. Refresh only reads the current directory.

    yatm-cli scan create --location-id 2 --path photos --signature fill-missing --result originals
    yatm-cli job wait 9 --wait-timeout 2m
    yatm-cli scan results 9 --limit 100
    yatm-cli scan scopes 9 --limit 100

`--signature known-only` reuses applicable facts or cache entries without hashing on a miss; `fill-missing` reads unknown or changed files; `force-read` reads every selected ordinary file. `--result report` leaves Library facts unchanged, while `originals` publishes successful original observations. `--compare-library` looks up matching content. `--preview-policy missing-only` generates missing derivatives; `regenerate-all` also replaces existing ones, and `none` disables generation. Without a usable content identity, known-only skips Preview rather than hashing implicitly. See [Scan policies and publication boundaries](../architecture/jobs.md#scan).

`analyze create` and `preview create` are convenient presets for this same Scan service and Job kind, not separate runners. `analyze create --mode basic/incremental/force` selects known-only/fill-missing/force-read and publishes originals; `preview create` publishes originals with Preview enabled. Use `scan create --help` for the full combination of controls.

Jobs continue after navigation. Failed ranges retain old records and report incomplete work; successful ranges retain their results. Retry makes fresh observations. A Scan is neither a filesystem snapshot nor a backup.

**Duplicates in Locations** groups known signatures without automatic full-disk hashing. Expanded pages check members and distinguish confirmed, changed and unavailable entries; unopened ranges remain unchecked. Scan selected ranges to improve coverage. Unknown content does not mean unique content. Duplicate Files keep independent organization, tags and Note.

## Physical Operations and Organization

Library and Location panes share cut/paste, rename/move, mkdir and deletion; user-level Copy is not offered. Both panes must use the same source. Same-name directories merge recursively; file and type conflicts fail without overwrite, even for identical content. A known conflict leaves that selected root untouched; a later race reports the actual completed range. Roots, nested registrations, runtime/archive resources, submounts and unauthorized paths are protected. Symlinks are operated on as links, never followed.

Library actions only change logical organization. A physical move keeps the File's logical path and annotations; merged targets retain their own original associations. Deleting originals preserves Library organization and saved versions without promising the current bytes were backed up. Directory operations include ignored contents; Ignore cannot silently reduce deletion scope.

These actions use the [shared organization engine](../architecture/library.md#shared-organization-interface), without creating Jobs. Short edits show no progress banner; longer work exposes Cancel in a fixed notification without moving the panes. Name dialogs remain open until completion and retain errors. Leaving the page or losing the request cancels pending work; completed items remain. Inspect partial results and refresh before any new attempt. New or changed objects outside the checked manifest are not silently deleted. [Outcome rules](../architecture/library.md#physical-file-operations) distinguish physical failure from incomplete Library publication.

    yatm-cli fileops run --help
    yatm-cli fileops run --kind mkdir --location 2 --destination . --name Selected
    yatm-cli fileops run --kind move --location 2 --source photos/example.jpg --destination Selected
    yatm-cli fileops run --kind move --library --source 42 --destination 43
    yatm-cli file locate-original <file-id> --location-id 2 --path moved/example.jpg --confirm

`fileops run` emits JSON Lines containing item results and a final summary. It exits nonzero on interruption, failed/unprocessed items or incomplete Library publication. Set a suitable request timeout for large directory operations; no separate execute, wait or resume command is required. CLI deletion always requires `--confirm-delete`.

**Locate original** changes an association, not a disk path. It compares the old binding and refuses a target owned by another File. Use it when limited observations cannot reliably reconnect an external move. Automatic continuity follows path → valid available signature → native identity → UUID within the observed range; it cannot prove every move-versus-copy history.

## Backup, Preview and Restore

Backup and Scan with Preview accept Library selections or live Location entries without prior content analysis. Accumulate Backup files/directories across folders in the Chonky waitlist. Expansion is bounded, overlap is deduplicated, and estimates separate missing inputs and unknown size. Backup preparation establishes File identities and actual content and freezes Library-relative archive targets. Physical selection does not imply mirrored archive paths.

    yatm-cli archive create --location 2:photos --location 3:reports
    yatm-cli archive create --file-id 42 --file-id 43
    yatm-cli file versions 42
    yatm-cli restore create 17 18 --target-location 3 --directory recovered

Preparation alone is not backup completion. Choose Media on the resulting Job to copy and verify. Missing inputs are errors, never substitutions by another duplicate File. Preview failure does not undo analysis or archival.

Restore selects FileVersions and permits confirmed authorized destinations without previous analysis. The browser chooser lists Preferred Locations, remembers the last confirmed directory and revalidates it when reopened; it does not collect browsed files. The [Restore contract](../architecture/jobs.md#restore) owns candidates, frozen paths, collisions, actual-content verification and automatic association. Existing originals are never displaced; ignored outputs stay unlinked. Explicit damaged-copy recovery never labels mismatched bytes as a verified version.

## Inventory, Integrity and Metadata Backup

- Media Scan selects `--result inventory` to publish inventory, or `--result verify` to check recorded copies. Both use the same pipeline; verification forces actual reads against the saved baseline. Unknown archives require explicit **Add to Library**.
- `scan media <media-id>` is the inventory preset; `verify create <media-id>` is the integrity preset. Mounted Volume work runs automatically. Tape waits for an explicitly selected matching device through `scan run <job-id> --device <device>`; the CLI never chooses or formats a device.
- Inventory does not claim healthy contents, and integrity findings do not accept damaged checksums as new expectations. See [historical checks and Media health](../architecture/media-io.md#copy-health).
- Unregistering a Location removes executable original/tracking associations, not disk bytes, Files, versions, copies or Preview. Failed access alone removes nothing.
- Library metadata export includes management metadata, preferences and historical operation results, not contents or Job bundles. Import validates live relationships, rolls back on conflicts and requires local root confirmation. Imported results cannot authorize local retries.

The [compatibility policy](../README.md#temporary-draft-compatibility-policy) preserves legacy and published Alpha data. Cross-Location transfer, uploads, editing, permissions, scheduling, multi-Executor transport and snapshots are outside this workflow.
