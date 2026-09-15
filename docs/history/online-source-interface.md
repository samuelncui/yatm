# Locations and File Content Interface

Status: Implemented in the v1 development branch, verified with [indexing workflows](indexing-workflows.md); not a published release. This is a frozen interface design. Maintain behavior in [API/UI architecture](../architecture/api-ui.md). React review drafts are not release artifacts.

## Navigation

### Product Interaction Contract

Use a file-first workflow: find and organize a File, understand its last indexed backup coverage, then archive current content or restore a dated saved version. A Location is setup and synchronization, not another editable Library. The UI must never equate indexing, an inaccessible disk, or a retained version record with a verified backup.

- Files offers visible Location and backup-status filters alongside text search; advanced query syntax remains optional. Preserve the complete browser toolbar and active-pane behavior.
- File rows carry a small trailing backup-status dot with hover/focus text; the Inspector keeps only a short status line and relevant actions. Current original and saved versions are separate views. Present dates, file sizes and storage names first; put hashes, signatures, permissions and internal identifiers behind Technical details.
- Saved versions are a paged dated list, not a dropdown of database IDs. An imported version with no known date says so. Selecting a version shows only its content, copies and restore action; annotations still belong to the File.
- Locations uses a compact table and routed add/detail pages with inline configuration, Save/Cancel and unsaved-change protection. Configuration, read-only files and related Jobs are separate sections; setup and access observations remain distinct. Original/Restore uses are independent, with Ignore and optional tracking writes specific to originals.
- Both typed indexing jobs share **Jobs → New → Scan**, selecting Location or Media; object shortcuts preselect the target. Creation leads to its background Job; navigation does not stop large scans. Say that it updates the Library without backing up or changing originals. Results use file/path changes and plain-language counts, not File-ID arrows or runner phases.
- Backup and Restore accumulate cross-directory selections, review server-expanded totals and then prepare a canonical Job detail. Backup chooses destination Media; Restore chooses a registered restore-enabled directory/subdirectory and source Media. Latest saved versions are the ordinary Restore default; explicit versions remain selectable. Job creation alone is never presented as completed copying.

Date-led recovery follows the user-facing emphasis in [Arq's restore flow](https://www.arqbackup.com/documentation/arq7/English.lproj/restore.html) and [Backblaze's restore browser](https://www.backblaze.com/computer-backup/docs/create-a-restore-web-ui). YATM still selects individual saved content versions, not a point-in-time filesystem snapshot.

- **Settings** is an independent fixed sidebar destination with keyboard-accessible **Locations / Library data** page tabs, not a collapsible or floating sidebar menu.
- **Settings → Locations** manages original-directory registrations, configuration and read-only physical index browsing.
- **Library → Files** manages logical organization, tags, notes, search and Archive selection.
- **Library → Media** manages independent archival inventory.
- **Jobs** owns progress, results, cancellation and retry.

Library has no third Locations file tree. Location filters and contextual links connect logical organization to the physical index. Paths belong to the Executor's access namespace even though the current deployment stores their metadata in Library. Multi-Executor transport and WebDAV are outside scope.

Preserve the full Chonky toolbar, Actions/Options, selection-dependent operations, dual panes, Inspector, dot-directory navigation and search-specific actions. Physical Location browsing must not acquire rename/move/delete controls.

Use concise contextual copy: directory names identify registrations, not scan policies; avoid repeating the domain model beside every File. Explain exceptional states and irreversible choices where they arise. The current Inspector copy and version-preview behavior are owned by [API/UI architecture](../architecture/api-ui.md#browsing-and-interaction).

## Location Configuration

Register a name, uses and an Executor-accessible directory inside administrator access ranges. New originals need Scan, not confirmation; restore-only Locations need no scan. Imported paths need explicit local confirmation, followed by successful Scan before original access.

The **Ignore** field is one monospaced multiline text box: **Uses gitignore rules. One rule per line, relative to this location.** Preserve raw text, order, comments and meaningful whitespace. Follow [Git's pattern rules](https://git-scm.com/docs/gitignore#_pattern_format), including blank lines, comments, escapes, wildcards, character classes, directory-only rules, root anchoring and ! exceptions. Do not read filesystem .gitignore files or tracked-file state. An ignored parent is pruned; a child exception alone cannot reopen it.

Required runtime/archive exclusions are non-overridable and displayed separately. Root/Ignore edits retain the cached index but require a successful Sync before content access. Ordinary dot directories and .yatm.json are not implicitly excluded.

Optional **Write a tracking UUID when missing** defaults off. Explain that it adds an extended attribute, never overwrites an existing value, and degrades safely when unsupported. Matching internals are not configurable UI.

Floating labels and outline notches use shared MUI typography. Verify empty, filled and focused fields in a real rendering; do not mask broken notches with per-page backgrounds.

## States and Operations

Binding validity, access observation and Sync outcome are independent:

| Fact | Display and next step |
| --- | --- |
| Imported binding | Review imported path; approve the local path before syncing |
| New/changed original binding | Scan required; cached index remains viewable |
| Current configuration indexed | Indexed; this alone does not imply present accessibility |
| Successful access observation | Available with check time |
| Failed access observation | Unavailable with reason/check time; retain cached browsing |
| No observation | Not yet checked; do not infer unavailable |
| Failed Location scan | Retry the existing Job; retain the prior successful index |

List RPCs do not probe every root. Details request an access observation. **Scan** provides Force rehash, metadata-only observation, and Generate previews (off by default); preview update options are effective only when generation is enabled.

The physical browser is explicitly the **Last synchronized index**. Preserve stable, revision-bound paging and a route to the File Inspector. Sync is not a backup or filesystem snapshot.

## File Inspector and Content Actions

Show one original in **Overview**, a separate **Saved versions** tab, and the selected content's **Archived copies**. Keep File-level tags and Note independent of content selection.

Distinguish unknown current content, known content without a copy, content matching a copy, historical versions with unarchived current changes, and inaccessible originals. Under [Library's coverage-to-version rule](../architecture/library.md#archive-inventory), content with known archived copies appears in Saved versions even if another File initiated archival. Show one version with its copies, not one version per copy or a request to back up again merely to enable Restore.

**Duplicates in Locations** searches published original signatures through [Library's paginated query](../architecture/library.md#annotation-search-and-cleanup), never an automatic identity or annotation merge. History-only matches and unsigned content are excluded.

The duplicate checkbox opens grouped results using shared Chonky presentation; ordinary search still returns individual File rows. [API/UI architecture](../architecture/api-ui.md#browsing-and-interaction) owns the implemented interaction contract.

Current-original and specific-version previews must not be confused. Unknown current content never uses a historical image as its current preview. Restore explicitly selects FileVersions and reports missing copies or colliding logical targets before creating an executable Job.

[Restore-as-original linking](../designs/restore-original-linking.md) is a separate Draft: recovery provenance and changing the sole original binding are different operations.

Archive selection from either pane uses Library logical paths and the File's one verified original. New Backup has no separate raw Source input. Online originals do not count toward archived-copy totals.

## Acceptance

Use actual React/MUI/Chonky rendering, not generated concept artwork. Keep independent local prototype sources until a later authorized commit, then remove disposable drafts. Do not promise compatibility with earlier prototype routes.

Acceptance covers registration without confirmation, imported confirmation, Ignore text preservation, cached unavailable browsing, background Scan creation/retry, Settings tab navigation, row status tooltips and selection, cross-Location duplicate search, complete Library toolbar, original/version/copy distinctions and explicit version Restore. Reset Demo and real Volume CLI workflows complement each other. [The content-model design](online-file-versions.md#implementation-and-acceptance) defines its separate backend scope.

## Pane Sources and Library Visibility

Library remains logically editable. Whether unbacked originals appear in its tree is a visibility policy, not a different identity model, a physical mirror or a global organization lock.

Replace each file pane's fixed Library root control with a compact, keyboard-accessible source menu. It offers **Library** and registered Locations grouped under **Locations**; the selected Location's name becomes that pane's root label. Keep the remaining breadcrumb for the selected tree. Prefer the menu over an always-expanded button for every Location so many registrations do not crowd the toolbar. Each pane selects and remembers its source and navigation independently, allowing Library/Location and Location/Location comparisons. Settings remains the configuration destination, not a prerequisite for physical browsing.

- **Library** displays logical paths and retains rename, move, directory creation, tags and Note. These operations never change physical originals or Media paths.
- **Location** displays the published physical index, including unsigned or unbacked originals. Physical rename, move and delete are unavailable; File annotations and content/version inspection continue to reference the same File. Inaccessible Locations retain clearly identified cached browsing.
- Switching a pane changes its query and available actions, not File identity, organization, version history or the other pane. Cross-pane physical mutations must not be inferred from drag and drop. Preserve the full supported toolbar and Inspector.

Use an explicit **Include unbacked files** Library visibility option instead of an independent/follow-filesystem mode. Recommended default is enabled, preserving current visibility. With it disabled, the normal Library tree and its scoped search show regular Files with at least one saved version, together with the logical directories needed to organize them. Existing logical directories remain editable; this option is not a pruning operation. Location browsing and cross-Location duplicate search continue to cover all published originals, independently of this Library preference.

The criterion is saved-version existence, not current-content coverage. A File with saved history remains visible after edits, original disappearance, Location unregistration or loss of its last recorded copy. A File with no saved version remains retained when hidden; its identity, annotations and logical path are not deleted. Re-enabling the option exposes it again, including an unlinked File that is no longer present in a Location. Known shared archived coverage establishes a version through the existing Library publication rule; queries do not create one.

The option must constrain bounded server-side tree/search queries before paging; filtering one loaded browser page is not sufficient. Never approximate it with the current-content `has:archive` predicate. Save preference changes without scanning, copying, migrating or reparenting Files, and leave existing Job selections and destinations frozen. A separate Saved files tree is unnecessary: Library itself provides the saved-version scope.

Acceptance covers independent pane navigation, logical editing in either visibility setting, read-only physical paths, hidden unsigned/unbacked Files remaining available in Locations, edited Files retaining visible saved history, archive-only Files, unavailable cached Locations, new saved content becoming visible, unlinked metadata reappearing when inclusion is enabled, paginated scope correctness and unchanged Job manifests. CLI query-scope coverage and actual browser/Demo checks are recorded in the [workflow verification](indexing-workflows.md#verification-record).
