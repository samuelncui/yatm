# Archive, Restore and Scan

Run the three kinds of Job: copy files to Media, restore saved versions to a Location, and observe
files or Media to publish content facts, inventory and integrity results. Loading a Tape for these
Jobs is described in [Devices and Tape operations](devices.md).

## Archive and Restore

Archive and Scan with Preview accept Library selections or live Location entries without prior content analysis. Accumulate Archive files/directories across folders in the Chonky waitlist. A Library File without an original association has no Add to Archive action; a directory can be added without checking its descendants. For Library selections, the estimate counts only Files with original associations and reports skipped Files separately; live Location selections count their observed ordinary files. Preparation skips those same Files and requires at least one eligible regular File. Expansion is bounded, overlap is deduplicated, and unknown sizes are reported separately. Archive preparation establishes File identities and actual content and freezes Library-relative archive targets. Physical selection does not imply mirrored archive paths.

    yatm-cli archive create --location 2:photos --location 3:reports
    yatm-cli archive create --file-id 42 --file-id 43
    yatm-cli files versions 42
    yatm-cli restore create 17 18 --target-location 3 --directory recovered

Preparation alone is not Archive completion. Choose Media on the resulting Job to copy and verify. An associated original that cannot be read is an error, never a substitution by another duplicate File. Preview failure does not undo analysis or archival.

Restore selects FileVersions and permits confirmed authorized destinations without previous analysis. The browser chooser lists Preferred Locations, remembers the last chosen directory and revalidates it when reopened; it does not collect browsed files. The [Restore contract](../architecture/jobs.md#restore) owns candidates, frozen paths, collisions, actual-content verification and automatic association. Existing originals are never displaced; ignored outputs stay unlinked. Explicit damaged-copy recovery never labels mismatched bytes as a verified version.

## Restore Saved Versions

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

## Scan

Use **Scan** for selected files, directories, a Location or Media. Choose the signature policy and result handling independently; Preview is an optional stage of the same Job. Refresh only reads the current directory.

    yatm-cli scan create --location-id 2 --path photos --signature fill-missing --result originals
    yatm-cli job wait 9 --wait-timeout 2m
    yatm-cli scan results 9 --limit 100

`--signature known-only` reuses applicable facts or cache entries without hashing on a miss; `fill-missing` reads unknown or changed files; `force-read` reads every selected ordinary file. `--result report` leaves Library facts unchanged, while `originals` publishes successful original observations. `--compare-library` looks up matching content. `--preview-policy missing-only` generates missing derivatives; `regenerate-all` also replaces existing ones, and `none` disables generation. Without a usable content identity, known-only skips Preview rather than hashing implicitly. See [Scan policies and publication boundaries](../architecture/jobs.md#scan).

`analyze create` and `preview create` are convenient presets for this same Scan service and Job kind, not separate runners. `analyze create --mode basic/incremental/force` selects known-only/fill-missing/force-read and publishes originals; `preview create` publishes originals with Preview enabled. Use `scan create --help` for the full combination of controls.

Jobs continue after navigation. Scan stops at the first failed Location and leaves that Location's prior records intact; earlier fully published Locations remain committed. A failed or cancelled preparation is terminal. Create and explicitly submit a new Scan to read the complete selection again; the earlier Job can be retained with its evidence. A Scan is neither a filesystem snapshot nor a durable archive.

## Scan Signatures and Previews

Preview generation is an optional Scan stage. Archive can request a companion Scan from its frozen input manifest. Scan keeps source/range, Results and Previews visible; Advanced holds signature acquisition and optional archived-copy counting. Its summary explains the effective choices. Signature policy selects cache-only reuse, filling missing content facts, or uncached reads. Preview policy independently selects no generation, missing-only generation, or regeneration of every eligible bundle. Cache-only misses are skipped rather than hashed; choose reading missing facts when new files need Previews. Known-only can still read known content for decoding. Results-only avoids catalog publication but still performs requested reads and Preview writes. Comparison reports recorded archived copies, not fresh health checks. Existing Preview metadata is read independently with `preview get`. See [Preview](../architecture/preview.md) for bundle ownership and [ACP integrity](../architecture/media-io.md#acp-and-integrity) for cache versus verification guarantees.

Installing the helper does not enable generation. In **Settings → Preview**, enable generation, choose Image or Video and edit the corresponding extensions and output options. Shared controls set concurrent files, timeout, input-pixel limit and an optional helper command override. Disabled controls preserve their values. Existing Preview assets remain accessible when generation is disabled or the helper is absent. Check installation independently with `yatm-cli preview capabilities`.

## Media Inventory and Integrity

- Media Scan selects `--result inventory` to publish inventory, or `--result verify` to check recorded copies. Both use the same pipeline; verification forces actual reads against the saved baseline. Unknown archives require explicit **Add to Library**.
- `scan media <media-id>` is the inventory preset; `verify create <media-id>` is the integrity preset. Mounted Volume work runs automatically. Tape waits for an explicitly selected matching device through `scan run <job-id> --device <device>`; the CLI never chooses or formats a device.
- Inventory does not claim healthy contents, and integrity findings do not accept damaged checksums as new expectations. See [historical checks and Media health](../architecture/library.md#copy-health).

## Job Execution Settings

**Settings → Jobs** holds the pipeline limits that every new Archive, Restore and Scan attempt reads
when it starts: manifest batch, queued reads, queued results, results per update and the update
interval. Queue limits count items, not bytes or memory. A running attempt keeps the limits it
started with. From the CLI, read the group with `yatm-cli settings job` and replace it with
`--execution-json FILE`. The [execution settings contract](../architecture/jobs.md#execution-settings)
lists the defaults and valid ranges.

