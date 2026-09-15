# Shared Files and Configurable Scan

Status: Implemented and verified in v1 development on 2026-09-11; not a published release. Maintain behavior in [Library](../architecture/library.md), [Jobs](../architecture/jobs.md#scan), [API/UI](../architecture/api-ui.md) and [Preview storage](../architecture/preview.md).

## Contracts

- Files share browsing, selection, conflict planning, directory merging and request-bound results. Library and filesystem adapters provide guarded primitives, not separate recursive operation engines. Same-name directories merge; ordinary-file/type conflicts never overwrite. User-level copy is not offered. Logical deletion and permanent physical deletion retain their distinct effects.
- Entry references are scoped provider references with observation guards, optional File associations and explicit capabilities. Names and display paths are not identities. Pure browsing does not admit Files. Object-storage contract tests cover virtual prefixes and non-native moves; an S3 integration is not included.
- Original availability and restorable archive coverage determine the primary indicator: present/covered green, present/uncovered yellow, absent/covered blue, absent/uncovered red, unresolved gray. Historical versions count only when a default restore candidate exists. Changes, unavailable versions and check findings are separate supplementary indicators. Checks have no automatic expiry or claim of continuous health.
- One SCAN Job, manifest and pipeline enumerate, resolve reusable facts, optionally hash, compare, generate previews, validate and publish. Signature policies are known-only, fill-missing and force-read. Result handling selects report-only, original publication, inventory publication or recorded-copy verification; inventory publication and verification are mutually exclusive. Verification always reads content against the frozen prior baseline.
- Preview is an optional scan stage. Known-only misses skip preview generation and are counted. Sequential Media reject previews. Preview errors do not undo successful observation publication. Location publication requires successful observed scopes; Media inventory requires a complete trustworthy physical enumeration and final identity validation.
- Restore uses a preferred-Location directory chooser with real New folder, confirmed-choice memory and no admission on navigation. Chonky owns compact breadcrumbs, path copying, consistent columns and measured row heights.
- Files use one querystring bar and a More filters dialog in both sources. Structured filters compose the same query; unsupported predicates are explicit errors. Changing sources does not rearrange the toolbar. Filtering precedes pagination and pure queries never admit entries.

## Acceptance

Passed: shared adapter contracts, indicator matrix, guarded content access, cache-only and forced-read semantics, actual-copy verification, ten integrated CLI/Volume/Preview E2E scenarios, full Go tests with both SQLite drivers, affected race checks, vet, deterministic IDL generation, and frontend tests/type/build/lint/format checks.

An explicit-reset Demo was checked in the browser for shared query filtering, status/Inspector facts, aligned rows and compact navigation, preferred-target selection and directory creation, and a completed verified Volume Restore. Official LTFS file-backend Archive/Restore and full-media spanning tests passed on Linux; no physical Tape was used. The [coverage matrix](../operations/e2e-test.md) owns repeatable commands and assertions.

Legacy compatibility remains required; incompatible v1 Draft fixtures are not silently reset. S3 integration, physical-media release acceptance and production migration activation are not established by these development checks.
