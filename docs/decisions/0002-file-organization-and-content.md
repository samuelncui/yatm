# File Organization Is Independent of Content

Status: Accepted for v1 Draft. Supersedes [content-owned organization](0001-content-identity.md).

A File owns ongoing logical organization and at most one online original. Archived content belongs to FileVersion; physical archive inventory is independent and found by exact opaque signature. Equal content may belong to different Files without merging their annotations or history.

This permits repeated edits of unarchived content without creating catalog clutter. A File's saved content is independent of which File initiated the physical backup. Deterministic path-first tracking provides organization continuity, not proof of a filesystem move. A copied UUID or hash is evidence, not exclusive ownership.

Known archived coverage of a File's observed content qualifies as a saved version without copying the bytes again. Repeated contents reuse a FileVersion, so the model is a saved-content set, not a filesystem snapshot/event log. Signature links never establish physical deletion ownership.

The [Library contract](../architecture/library.md) owns schema, lookup and cleanup details; the [complete design](../designs/online-file-versions.md) tracks implementation acceptance.
