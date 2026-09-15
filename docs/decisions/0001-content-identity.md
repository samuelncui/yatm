# Content Identity Owns Library Organization

Status: Superseded by [File organization independent of content](0002-file-organization-and-content.md). Historical rationale, not the current schema contract.

A signed File represents content independently of its Media paths, so identical signed content shares one logical identity, organization, annotations, and Preview address. Non-empty signature bytes are unique without requiring a particular encoding; directories and unsigned records may omit the identity. This keeps organization stable across physical copies and permits future signature encodings without a schema redesign; it does not make old file bytes recoverable when no physical copy remains.

See the [Library contract](../architecture/library.md) for current behavior and the [implemented online-source design](../history/online-sources.md) for the verified mutable-source scope.
