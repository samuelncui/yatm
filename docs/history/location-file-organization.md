# Live Locations and Persistent File Organization

Status: Implemented and verified in development on 2026-09-11; not a released v1 compatibility baseline. The [requirements](../designs/online-files-requirements.md) own user outcomes; [current architecture](../architecture/overview.md) owns maintained behavior. This document records the implemented scope and acceptance.

## Product Contract

Locations read and manage actual disk entries. Library retains independently organized Files, annotations, original references and saved versions. Analysis and backup add content facts. Whole-Location analysis is never a prerequisite for browsing, annotation or backup.

| Setting | Default | Meaning |
| --- | --- | --- |
| Automatically collect Location files | On | Admit basic observations without hashing. Registration starts basic background collection; browsing/refresh collects discovered files. Enabling starts collection of existing confirmed Locations and reports Jobs/errors. Disabling does not delete records or cancel Jobs. |
| Include unbacked files | On | Library presentation only; saved-version existence controls filtering before pagination. |
| Confirm permanent deletion | On | Application-owned deletion confirmation. Disabling skips only this interaction, never server validation or CLI explicit deletion authorization. |

Existing registrations are not recursively collected merely by upgrading or starting the server. Imported paths need local confirmation, not full analysis. No watcher, periodic traversal or automatic backup is included.

## Live Browsing and Admission

Read only the requested directory, including empty and dot directories, with bounded pages and no content hashing. Cursors bind the directory observation, query and root binding; detected change requires reload. Failed loads clear rows and display an error, never a cached inventory or false empty result. Files can have no Library ID. Associations supply annotations but do not establish physical existence.

Library keeps organization and last observations, not a replacement physical directory index. New Files initially use `Unforged/<Location>/...`; physical and logical names are subsequently independent. Unavailability and disappearance retain Library information. Automatic collection failure must not prevent live browsing. Explicit annotation, admission, analysis or backup synchronously resolves necessary associations through shared backend logic.

Continuity uses ordered matching: `path → applicable signature → native identity → UUID`, without hashing solely to match. Complete earlier rounds before creating unmatched Files; copies remain independent even with equal content or copied UUIDs. A same-path replacement continues its File and invalidates stale current content, not annotations or versions. YATM moves update known descendant references directly. External matching is limited to successfully observed scopes; unread/unavailable Locations cannot donate associations. Insufficient evidence retains records and permits explicit relocation, not a whole-disk guess.

## Analyze and Content Facts

Analyze replaces Location Sync. Selected files, directories or a whole Location support basic metadata, incremental content, force rehash and optional Preview. Basic collection uses the same Job machinery. Ordinary Refresh is not Analyze. Long operations expose progress, cancellation, retry and paginated results.

Publish successful observed ranges per Location; failed ranges retain prior facts and cannot imply absence. Report incomplete ranges independently. Signatures retain their observation basis and become unknown after detected changes. Historical versions/previews cannot substitute for unknown current content.

Duplicate queries reuse known signature groups without automatic rehash, validate members in pages and distinguish confirmed, changed, unchecked and unavailable observations. Analyze selected ranges expands coverage; unknown signature never proves uniqueness. Matching archive inventory may establish saved versions, but cannot invent archive dates or healthy-copy evidence.

## Real File Operations

Support open/download/properties/refresh/name filtering, multiselect, copy/cut/paste/drag, rename, same-Location move/copy, mkdir and permanent deletion. Exclude cross-Location transfer, uploads, editing, compression, permissions and crossing submount/overlapping registered ranges.

| Action | Disk | Library |
| --- | --- | --- |
| Rename/move | Change actual path | Update original references, retain logical organization |
| Copy | Independent copy, actual transfer verification | Independent admission; no inherited tags/Note/history |
| Create directory | Create actual directory | No mandatory logical node |
| Permanently delete | Remove selected actual objects | Clear original/executable tracking references; retain Files, versions and copies |
| Library organization/removal | No disk changes | Existing logical semantics |

Default is no overwrite and no directory merge. Reject root deletion, descendant moves, registered roots, archive/runtime resources and unauthorized paths. Never follow symlink targets; rename/move/delete the link itself and reproduce links during directory copy. Special files are not copied as ordinary bytes. Ignore controls automatic admission and recursive analysis/backup, not disk permissions: directory operations include ignored children; explicit ordinary-file selection may annotate/analyze/back up ignored entries. Administrator boundaries remain mandatory.

Delete confirmation identifies scope and irreversibility without promising recovery from old versions. The server validates the frozen manifest even with confirmation disabled and never deletes newly appeared entries outside it. Batch results distinguish success, failure and unprocessed items, not directory-wide atomicity.

Short operations stay on the page; long copying/recursive deletion has Job progress without forcing navigation. Stable operation/item identities preserve completed output on retry. Copy cancellation removes incomplete output only. Physical success followed by Library failure reports the stage and retries association publication, not physical mutation. No universal filesystem transaction or additional crash-only state framework is introduced.

## Interfaces and Workflows

Typed selections contain Location, relative path, binding token and expected object facts. Server access/mutation revalidates them. Live rows have optional File associations; admission creates real File IDs, never synthetic catalog IDs. Typed prepare/execute/result APIs expose no shell or arbitrary unbounded absolute-path interface. Library transactions remain metadata-only; Job databases own manifests/results; Executor owns execution/cancellation/resource coordination.

Chonky owns clipboard, drag/drop and availability through formal extensions. Location actions are physical; Library actions are logical. Mixed-source dragging performs no implicit physical operation; waitlist drops only select. Preserve dual panes, compact roots/breadcrumbs, Inspector and cross-directory waitlists. Settings separates collection, visibility and confirmation.

Backup admits live selections without Analyze, freezes identity/content/logical target during indexing and retains ACP/Media publication guarantees. Restore retains version selection and association rules with live destination checks. Open/Download permits unknown signatures and unadmitted entries with safe HEAD/Range responses. Preview freezes live selections independently. CLI covers settings, browsing, Analyze, operations and results; permanent deletion requires explicit authorization.

Only legacy compatibility is required. Change protobuf first and regenerate clients; do not preserve obsolete v1 Draft Sync/readiness adapters or automatically clear data. Export/import retains management facts, not raw content; imported bindings remain unconfirmed.

## Delivery and Acceptance

The implementation includes shared types/boundaries, live access/admission, settings and Analyze, physical operations and Chonky, content workflows, CLI, Demo and integration. Current contracts are maintained in the owning architecture and operations documents.

Acceptance covers unadmitted access/annotation/backup; independent collection/visibility; scoped continuity and edited copies; incomplete duplicate coverage; real operations, symlinks, boundaries, collisions, submounts, partial failure/retry; deletion retaining history; physical-success/metadata-failure recovery; actual transfer mismatch rejection; background progress after navigation. Preserve multi-version images and 2/3/4-member duplicate groups in Demo, adding unadmitted entries and physical operations.

Use real CLI-subprocess E2E and update the [coverage matrix](../operations/e2e-test.md); retain internal fault injection. Run relevant Go tests/vet/race, both SQLite drivers, generated/client/frontend checks and explicit reset Demo browser smoke. Run isolated Volume/Preview and official Linux LTFS file-backend regression after transfer integration; no physical tapes. Preserve unrelated edits; do not commit, push or publish automatically.

Acceptance on 2026-09-11 passed full Go unit tests and vet, affected-package race and both SQLite-driver checks, IDL/CLI checks, 204 frontend tests with production build, nine isolated CLI E2E scenarios, and explicit-reset Demo browser checks. Official LTFS file-backend acceptance on Linux passed `TestLTFSFullTapeSpansMediaAndRestores` (84.71 seconds) and `TestLTFSArchiveRestore` (3.00 seconds), 87.730 seconds total. The latter includes append, Preview, Library export/import, Restore, integrity observations and finalization fault probes. All Media used isolated directories; no physical Tape or production data was accessed.
