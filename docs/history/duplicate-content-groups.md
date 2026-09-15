# Content-Grouped Duplicates in Locations

Status: Implemented in v1 Draft development. [Library search](../architecture/library.md#annotation-search-and-cleanup) and [API/UI](../architecture/api-ui.md#browsing-and-interaction) own current behavior. This is not a published-release compatibility baseline.

## Product Contract

Present **Duplicate content** as a grouped search result within Library Files, not a new tree or a new kind of File. A group means several indexed originals have the same known signature; it does not mean they share organization or should be merged.

- Keep the search controls, toolbar and Inspector. Selecting **Duplicates in Locations** requests grouped results; clearing it returns to ordinary results.
- Each collapsed group shows a representative filename, original count, distinct Location count, and matching archived-copy count. The representative name is only a label; different filenames can belong to the same group.
- Expanding a group shows separate member rows with filename, Library path, Location and physical relative path, plus the existing compact status dot. Selecting a member opens its ordinary Inspector, tags, Note and versions.
- Show file size when recorded size facts agree. Put the full opaque signature behind Technical details with a Copy action; do not make hex strings the primary heading or truncate them into an identity key.

Illustrative layout:

```text
Duplicate content
  > handbook.md       3 originals · 2 Locations · 1 archived copy
  v site-photo.jpg    2 originals · 2 Locations · No archived copies
      site-photo.jpg  Library / Photos     Daily / Camera/site-photo.jpg
      cover.jpg       Library / Website    Assets / cover.jpg
```

Groups are read-only projections. Existing File actions still operate on selected members, not an implicit representative. No automatic merge, original deletion, preferred original, or claimed reclaimable space: hard links, nested Locations and backup policy make byte savings and independent-copy counts unsafe to infer.

## Membership and Filters

Group only current published FileLocation signatures, using exact opaque bytes and at least two distinct File IDs. Never hash during search. Exclude unsigned originals and history-only matches; retain unavailable Locations' successful indexes. FileVersions remain independent histories.

Determine duplicate membership across all Locations before applying text, tag, Note, Location or coverage filters. Include a group when at least one member matches the filters. Expansion shows the whole group and marks members outside the filter, with a count such as **1 matching · 3 originals**. This preserves the useful case where a selected Location contains only one member of a cross-Location duplicate group.

Count archived Positions once per signature, not once per member or version. A matching copy does not create a member's FileVersion or establish several independent backups. Label results as the last synchronized index, not a live filesystem comparison.

## Queries and Consistency

Typed paginated catalog queries provide group summaries and group members, with protobuf and generated clients. They do not group one FileSearch page in the browser: members may lie on different pages, and a single group can exceed a page by itself.

- Derive groups with bounded GORM queries over the indexed FileLocation signature column; no persistent group table or full-catalog client download.
- Page groups by complete signature bytes; bind the opaque cursor to the query. Page members by File ID within the requested signature. These stable keys give deterministic pagination for an unchanged catalog.
- Fetch summaries/counts in a consistent metadata read transaction per request. Return each expanded group's refreshed summary with its member page so the UI can discard an obsolete expansion when membership changes.
- This is a live catalog browser, not a frozen search snapshot. On observed Sync/index changes, offer Refresh and reset group/member pagination together. Concurrent changes can require a reload to see newly qualifying groups before the current cursor; do not claim a cross-request point-in-time view.
- Keep member and group loading/error/retry independent. A group that no longer has two members shows **Results changed — refresh** rather than presenting a singleton as duplicate content.

## Delivery and Acceptance

Catalog queries, CLI, React results and explicit-reset Demo coverage use shared Chonky grouping and member interactions. Ordinary File search remains available; no v1 Draft compatibility adapter or separate duplicate-maintenance subsystem is added.

Verify two or more groups with different member counts; different names with equal content; identical names with different signatures; same-Location and cross-Location groups; a single filtered member with duplicates elsewhere; unsigned and historical-only exclusions; unavailable indexed originals; large groups and multiple group pages; refresh after Sync; shared archived-copy counts; and unchanged tags, Note, version history and physical files after browsing.
