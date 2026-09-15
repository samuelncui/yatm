# Online File Management Requirements

Status: Approved product requirements for v1 Alpha 1. This document owns user outcomes; detailed behavior belongs in the linked design and current architecture.

## Core Outcomes

1. **Recognize content and backup coverage.** For an online file, distinguish content already known to Library from content with an archive copy. Show whether the current content is covered, not merely whether an older version was saved. Without a reliable content signature, coverage is unknown rather than absent.
2. **Organize before backup.** Online files can use logical folders, tags, Note and search without first being archived. Annotations belong to an ongoing File, not to a particular pathname or immutable byte sequence.
3. **Preserve organization through change.** Repeated edits must not multiply Files or discard annotations. Rename/move handling should preserve the association where established; uncertain identity must not silently merge unrelated files. Independent copies can diverge and remain independently organized even when their content or copied tracking attributes match.
4. **Keep saved content versions.** Ordinary edits do not create saved versions. A content state known to have archive copies is a saved version, including coverage established by an existing matching copy. Repeatedly saving the same content adds copies or reuses its version, not a new edit-history entry. Users can inspect versions, their previews and available copies, and restore a chosen version.
5. **Retain useful Library records independently of originals.** Unbacked online files, files with both originals and saved versions, and saved files whose originals are gone can share the Library. Missing originals do not erase logical paths, tags, Note or versions. Retained metadata does not guarantee recoverable bytes. Showing unbacked Files is a display preference, not a restriction on organization or a deletion operation.
6. **Find duplicate content across Locations.** Identify multiple groups using available reliable signatures; show the separate files and their locations within each group. Content equality does not merge Files, annotations or history. Unknown content cannot be declared unique; an incomplete search cannot claim complete coverage.
7. **Keep logical and physical organization distinct.** Library is always independently editable. A Location represents a real directory, not another logical tree. Users must understand whether an action changes Library organization or actual disk contents. Original locations and archive Media remain different responsibilities, even on the same physical disk.
8. **Connect daily work to backup and restore.** Browse, search, annotate, accumulate selections across directories, archive and restore within one coherent workflow. Backup verifies the selected content; saved versions remain discoverable after an original disappears. A separate content-analysis task must not be an obligatory extra step before backup. Online originals do not count as archive copies.

## Live Location Constraint and Product Direction

- **Confirmed constraint:** Location browsing must read the actual directory. A load failure shows no cached file listing; it reports the failure rather than presenting the directory as empty. Persistent Library records and saved-version previews are not a substitute Location tree.
- **Physical organization:** Location panels operate on disk; Library panels retain independent logical organization. The [shared operation contract](../architecture/api-ui.md) covers move, rename, mkdir and deletion, with common directory merging, no file overwrite and provider-specific safety boundaries. Location deletion is permanent; Library deletion retains its logical Trash semantics.
- **Simplicity requirement:** Basic browsing and ordinary interaction do not require a whole-directory synchronization lifecycle. The common Scan pipeline handles basic collection, hashing, optional Preview and copy verification with visible background progress, cancellation and results.

## Decisions Not Implied by These Requirements

- Collection, Library visibility and deletion confirmation are independent preferences under the [product contract](../history/location-file-organization.md#product-contract). None replaces server authorization or content validation.
- Path, signature, inode and xattr UUID are matching mechanisms, not user outcomes. A redesign must explicitly resolve external-move continuity; a manual-only relinking policy is not an accepted requirement.
- Live-only Location listings still permit content-signature caches and persistent File associations. Their observation-validity rules belong to the [Library contract](../architecture/library.md).
- Instant duplicate results, mandatory fresh full analysis, automatic background traversal and watchers are not interchangeable promises. Define scope and freshness without silently dropping cross-Location duplicate discovery.
- Cross-Location transfers, mounting, multi-Executor transport and automatic backup scheduling remain outside the approved scope.

## Design Navigation

- [Online originals, versions and archive copies](online-file-versions.md): established detailed model and workflows, including implementation-specific matching choices.
- [Live Locations and persistent File organization](../history/location-file-organization.md): implemented product and safety contract, with acceptance results.
- [Current Library](../architecture/library.md) and [current UI](../architecture/api-ui.md): development implementation and maintained behavior.
