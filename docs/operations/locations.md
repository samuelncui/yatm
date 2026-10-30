# Locations and Originals

These workflows follow the [daily-use constraints](index.md#daily-use). [Library](../architecture/library.md) owns organization and content; [Jobs](../architecture/jobs.md) owns execution guarantees.

## Register and Browse

1. Open **Settings → Locations → Add location** and choose a name and authorized server directory. **Browse directories** opens a read-only chooser; choosing a directory changes the draft and does not register it until Save. **Restore destination** recommends it in the chooser; it is not write permission.
2. Configure **Ignore** with gitignore text. For a new Location, blank uses the server's default YATM-managed-entry rules; nonempty custom text replaces that default set. Later edits preserve intentionally empty rules. Matching entries are hidden from live browsing, excluded from measurement and from Scan/Archive selections (including an explicitly named path), and never become new originals or Restore links. Existing associations and writes are unaffected: explicit admission remains available, and Restore still writes its output. Required runtime/archive exclusions cannot be bypassed.
   Under **Advanced**, optionally enable **Use mmap for content reads**. Mmap is off by default and applies when Archive or Scan reads original file content, including Archive preparation hashes; it does not change ordinary directory browsing.
3. Save. Changing the root or Ignore keeps existing records; future reads use the current configuration and existing Jobs cannot be redirected. The directory is immediately browsable and usable, whether registered or imported. Registration does not collect files or create a Job.
4. Choose the Location in Files. Both panes use the same source while retaining separate directories. Pages show actual files, empty/dot directories and optional Library associations. An unreadable entry stays visible with its error and cannot be operated on; other rows remain usable. A directory-level read failure clears that pane and provides Retry, never an offline listing.

File names preserve legal UTF-8 text, including backslashes, leading/trailing spaces and
control characters. The current string protocol cannot operate on non-UTF-8 filename bytes:
those names appear as escaped error rows and are still included in the directory total.
Display escapes are not paths to copy into an operation. Native filesystem restrictions
still apply when creating or restoring a name.

    yatm-cli location create --name Documents --root /allowed/documents --ignore-file ./ignore.txt
    yatm-cli ls location://Documents/photos
    yatm-cli ls --location-id 2 --path photos --query "type:file AND size:>1048576" --limit 100
    yatm-cli files get --location-id 2 --path photos/example.jpg

`ls` accepts the Location's exact display name in its URI, or the existing ID flags.
Add `--use-mmap` to `location create` or `location update` when mapped original reads are desired; an update replaces the complete configuration, so repeat the Location's other desired flags and Ignore file.
The [CLI browsing examples](library.md#browse-from-the-cli) explain Library paths,
quoting and duplicate Location names.

A listing reads the directory completely and writes one JSON Lines message per batch, so it takes no page size and no cursor. A query is a paged Search that keeps both: continuing it uses the cursor it returned. Reload after navigation or when the directory itself is replaced. Paths refer to the server except CLI input/output files. `ls` is a pure, minimal read; `-l` requests attributes and `--status` requests availability. **Add to Library** opens Scan with known-only signatures and original publication. `files metadata` admits an uncollected entry when saving annotations. File details do not open full bytes through HTTP. See the [shared Files contract](../architecture/library.md#shared-organization-interface).

Unregistering a Location removes executable original/tracking associations, not disk bytes, Files, versions, copies or Preview. Failed access alone removes nothing.

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

The [compatibility policy](../README.md#pre-stable-compatibility) preserves v0.1.x migration/import. Incompatible pre-stable v1 stores are rejected before modification and require an explicitly chosen conversion or reinstall. Cross-Location transfer, uploads, editing, permissions, scheduling, multi-Executor transport and snapshots are outside this workflow.
