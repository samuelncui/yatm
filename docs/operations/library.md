# Library and Mounted Volume Workflows

These are current archive workflows. See the [online-source guide](online-sources.md) for registered originals, live browsing, and Library-selected Archive.

## Mounted Volume

Configure `paths.volumes` in [configuration](../../config.example.yaml) to contain already-mounted roots. Use **Media → Add Volume**:

1. **Initialize New** creates the immutable marker and Library Media row. **Register Existing** reads an existing marker after metadata-only deletion and recreates the Media row.
2. Archive copies ordinary files to the Volume. HDD supports concurrent random writes; HM-SMR writes sequentially.
3. Open from the Library Inspector reads a published mounted Position directly; the server checks identity, path, and file facts on every request.
4. Restore copies selected FileVersions to a locally confirmed Location and chosen subdirectory; target recommendations are not permissions. [Restore](../architecture/jobs.md#restore) defines automatic association and collision handling.
5. Scan automatically validates and publishes added, changed, and removed inventory paths. Its completed Job retains paginated changes; **Force rehash** bypasses caches during observation. Unavailable or failed scans retain the old inventory. There is no Apply step.
6. Media Delete removes only catalog metadata. Register plus a subsequent Scan can rebuild metadata from an unchanged marker and filesystem.

The OS owns mount/unmount/eject. A discovery root or its immediate child can be initialized/registered; duplicate mounted UUIDs, changed profiles, symlink path components, and escapes are rejected. See [Media/I/O](../architecture/media-io.md) for identity, integrity, and capacity guarantees.

## Organization and Search

Tags and notes belong to logical File/directory identity, not content or shared archive copies. Rename, move, and Trash preserve annotations without changing physical files. [Library](../architecture/library.md) defines normalization, limits, versions and cleanup protection.

Search runs in the active pane and keeps the other pane available for **Locate in Other Pane**. Fields are `name`, `tag`, `note`, `type`, `size`, `mtime`, `source`, and `has`; bare terms match name, Tag, or note. The [online-source guide](online-sources.md) explains source and copy-presence filters. Adjacent conditions mean AND. Boolean operators, parentheses, and `*`/`?` wildcards use the query parser's native rules. Use decimal bytes and quoted RFC3339 timestamps:

```text
name:report AND (tag:finance OR note:"final")
type:file AND size:>=1048576
mtime:>="2026-01-01T00:00:00Z" AND NOT tag:obsolete
```

Export creates a Library metadata backup, not a copy of physical content. Import replaces declared entity types transactionally and preserves prior data on invalid references or unique-key conflicts; review the [backup contract](../architecture/library.md#backup-and-legacy-compatibility) before replacement.

Select **More filters → Duplicates in Locations** for grouped current-content results; the [Demo guide](demo.md#fixtures) covers group expansion and cross-Location filters. The CLI exposes the same separately paged queries:

```shell
yatm-cli file duplicate-groups --query 'tag:review' --limit 20
yatm-cli file duplicate-members --signature <hex-signature> --query 'tag:review' --limit 50
```

Pass each response's cursor back to its own command with `--cursor`. JSON replies encode signature bytes as base64; convert them to hex for `--signature`, or use the UI's Copy signature action. These are transport representations, not a required stored signature format. Group filtering retains all members of qualifying groups, unlike ordinary flat File search.

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
metadata may retain only first/last backup dates, not every intermediate save.

The CLI uses RFC3339 with an explicit timezone; the cutoff is inclusive:

```shell
yatm-cli file inspect-selection --restore --file-id 12 --before '2026-09-01T23:59:59+08:00'
yatm-cli restore create --file-id 12 --before '2026-09-01T23:59:59+08:00' --skip-unmatched-versions --target-location 7
yatm-cli restore create 42 --file-id 12 --before '2026-09-01T23:59:59+08:00' --target-location 7
```

The last example pins version 42 for its File while other Files under selection
12 follow the cutoff. Omit `--skip-unmatched-versions` to keep missing time matches
blocking. Creation prepares a frozen Job; later waitlist/policy changes do not
change that Job's selected content. [Restore execution](../architecture/jobs.md#restore)
owns output and retry rules.

## Preview and Hash Cache

Preview generation is an optional Scan stage. Archive can request a companion Scan from its frozen input manifest. Signature policy selects cache-only reuse, filling missing content facts, or uncached reads; **Update outdated previews** independently refreshes bundles generated with different settings. Cache-only misses do not trigger hashing. See [Preview](../architecture/preview.md) for bundle ownership and [ACP integrity](../architecture/media-io.md#acp-and-integrity) for cache versus verification guarantees.
