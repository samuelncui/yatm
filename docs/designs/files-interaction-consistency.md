# Files Interaction and Metadata Restore

Status: Draft. Proposed interfaces and interaction rules, not current support.

Current behavior is owned by [API/UI](../architecture/api-ui.md) and
[Library](../architecture/library.md). This proposal keeps the existing shared
organization engine and metadata replacement semantics.

## Entry Identity Across Views

Directory, search and duplicate results should carry the same `FilesEntry`:
provider reference, object kind, optional File association, observation and allowed
operations. Chonky row IDs identify display rows, not executable objects. Search
and duplicate APIs should build these entries through the existing server entry
builder rather than requiring one additional lookup per result.

The interaction controller derives menus, scope checks, confirmation and execution
from selected references. It must not infer physical versus logical operations from
the page that opened the results. Missing references or capabilities disable an
operation; there is no numeric-ID fallback. A separate navigation context supplies
an optional real destination directory; virtual search roots are not destinations.

Recommended product rule, pending confirmation: global duplicate groups represent
Library organizing objects. Their ordinary edits are logical; Locate in Location
opens the actual entry for physical organization. Direct duplicate cleanup would
need an explicit physical operation rather than silently changing Delete semantics
according to the originating pane.

## Controlled Query Ownership

Each pane/source owns a query draft and an applied snapshot. A query contains free
expression text, typed additional filters, and a files/duplicates view. The query
bar receives controlled value/change/submit/clear callbacks; it does not own a
second applied query.

- More filters edits a temporary copy. Apply replaces those values; Cancel discards
  the copy. Show the count of additional filters on the button.
- Compile expression and additional predicates once at submission. Never write
  generated clauses back into the expression. Repeated Apply is idempotent.
- Free text and additional controls are separate AND inputs. Controls do not
  pretend to rewrite predicates inside arbitrary manual OR/NOT expressions.
- Duplicate grouping is one explicit query view, not an injected text predicate
  plus a second boolean. The grouped endpoint already enforces duplicate semantics.
- Refresh uses the applied snapshot. Pane/source changes select the corresponding
  state; Clear and Back to folder reset query/cursor together. Late responses cannot
  publish into another pane, source or applied query.

Full bidirectional editing of arbitrary query syntax, if required, must reuse the
server parser for decomposition/composition. Do not add a second grammar or regex
rewriting in the frontend.

## Library Metadata Restore

The existing [backup contract](../architecture/library.md#backup-and-legacy-compatibility)
replaces declared metadata groups transactionally; it is not incremental merging
by integer ID. The UI should name this **Library metadata backup**, with
**Export backup** and **Restore backup** actions.

Before submission, an application dialog states that included metadata groups and
settings are replaced, absent records in those groups are removed, physical files
and Jobs are unchanged, and imported Locations require local confirmation. Offer
Export current metadata without automatically saving another file.

One submission owns pending/error/success state. Prevent duplicate submission and
dismissal during upload. Keep non-2xx errors visible, distinguish Busy, and do not
automatically retry. On success refresh affected Library/Media/Location/settings
views and identify paths requiring confirmation.

Counts and conflict previews require a server preparation phase bound to the same
uploaded input. Reading a header in the browser is not complete validation. Cross-
installation incremental merging is outside this proposal.

## Delivery and Acceptance

1. Finalize duplicate-result operation semantics; update shared result contracts
   and regenerate clients without v1 Draft adapters.
2. Route all result projections through the common entry/interaction controller.
3. Introduce controlled query state and replace lifecycle tests, not only snapshots.
4. Implement metadata restore feedback without changing transaction boundaries.

Test ordinary/search/grouped operations against the same references, capability
absence and virtual destinations. Test Location A to B, grouping on/off, Cancel,
repeated Apply, pane/source switches and late query responses. Test metadata Busy,
non-2xx, duplicate submission, success refresh and complete rollback on invalid
late records. No proposal permits physical cleanup or new Jobs merely by browsing.
