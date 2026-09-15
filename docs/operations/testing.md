# Development Checks and Test Environments

Run checks in proportion to changed behavior. Commands below run from the repository root; Go tests use isolated fixtures and do not authorize access to production data or hardware.

## Remote Candidate Acceptance

On the isolated `dev` acceptance host, YATM programs must come exclusively from the corresponding checksum-verified release package. Keep test orchestration local and invoke the packaged server/CLI and installer through SSH. Transfer only the approved candidate package, checksums and fixture data; use its packaged templates/scripts for configuration. Do not transfer repository source, separately compiled test binaries such as `e2e.test`, or standalone test-helper source. Existing systemd, LTFS/FUSE and other host dependencies remain environmental prerequisites.

Choose an existing, spacious, xattr-capable mounted filesystem and create a uniquely scoped test directory beneath it. Place packages, installation copies, backups, Restore outputs and a private `TMPDIR` inside that root; export `TMPDIR` for remote installer and migration invocations as well as the controller. Check available space before extraction and each capacity fixture; the test owns only that directory and its explicitly registered test resources. A temporary pathname does not imply a disposable filesystem or sufficient capacity.

Local development and CI may compile and run Go test harnesses. Those results are separate from remote packaged-binary acceptance. If a fault-injection scenario requires private functions or a custom in-process server, run it locally/CI and record that limit; do not describe it as a `dev` candidate result. The [E2E guide](e2e-test.md#remote-packaged-binary-acceptance) owns the remote procedure.

For official LTFS file-backend acceptance, explicitly configure the optional `templates/testing/ltfs-file-backend` adapters from the package. They operate only on scoped virtual-cartridge directories and are not default installation scripts; their README describes identity and capacity fixtures. Physical Tape remains a separate acceptance boundary.

## Local Checks

```shell
go test ./...
go vet ./...
go test -race ./library ./migrate/legacy ./apis ./executor/... ./preview/...
git diff --check
```

For content-model changes, cover opaque nullable signatures, unique File/version pairs, independent same-content Files, one original per File, repeated-content version reuse, shared-copy lookup, cross-batch import rollback, and evidence-based legacy migration with source preservation. These checks do not repair or maintain compatibility with old Draft databases.

Restore association checks include binding generations, existing/stale bindings, ignored outputs, multi-version candidate freezing, exact-content adoption, namespace collisions, final Media identity failure and Library-first checkpoint retry. Scan verification checks include real uncached reads, per-item versus device errors, absent baselines and content-guarded health publication. Run both SQLite drivers and race checks for `./executor/restore`, `./executor/scan` and `./library`; imported Restore results must remain historical, and imported bad-copy observations must not become healthy or unchecked accidentally.

For Files and Location changes, include `./executor/scan`, `./executor/observation`, `./executor/fileops`, `./internal/treeops`, `./internal/ignore`, `./internal/demo`, and `./cmd/yatm-cli` in affected unit/race checks, and run the [live-file, Volume, and Preview CLI E2E](e2e-test.md). Cover optional File identity, pure browsing versus explicit collection, live paging/drift, Ignore versus authorization, guarded references, physical-operation receipts and ordered continuity. Run the shared organization contract against Library, filesystem and object-storage-semantic test adapters: directory merges, retained target associations, duplicate selections, conflicts, cancellation, paged traversal and partial publication. Do not add user Copy acceptance to the supported move/mkdir/delete contract.

Scan tests exercise one pipeline with known-only, fill-missing and force-read policies, optional comparison/Preview and each publication policy. Known-only cache misses must not hash; verification must neither trust cache nor replace its saved baseline. Preview failures must preserve otherwise valid observations, while source drift and identity failure invalidate the appropriate publication scope. `analyze`, `preview create` and `verify` CLI presets must resolve to this same Scan service, not separate runners.

Canonical CLI subprocess tests cover `files list/get/inspect/collect/metadata`, `fileops run` and `scan create/run/results/scopes`; protocol tests cover the guarded content URL used by Open. The [E2E coverage matrix](e2e-test.md) records complete server/CLI workflows; subprocess transport tests alone do not establish full business acceptance. Ignore tests compare against Git in an isolated temporary repository when Git is available; production matching does not invoke Git. Check the SQLite paths with both the default CGO driver and `CGO_ENABLED=0 go test ./library ./entity ./executor/... ./apis ./migrate/legacy ./internal/demo ./preview/...`. Regenerate IDL with `bash entity/service_gen.sh` and verify a second generation produces no changes.

For frontend changes, run the scripts in [package.json](../../frontend/package.json), regenerate protobuf Go/TypeScript outputs when IDL changes, and keep the lockfile reproducible. Use active-LTS Node and supported stable frontend dependencies. Check shell syntax for changed scripts and run builds/generation checks when those paths change. No documentation-specific dependency or CI is required.

The [Demo maintenance workflow](demo.md#maintenance) is required for frontend-visible features. Schema changes require an explicit reset of disposable Demo data. Preserve representative actionable, unavailable, empty, and error states; the Demo supplements semantic and E2E tests.

## Media and Migration Safety

- Run [mounted Volume and LTFS file-backend E2E](e2e-test.md) for affected copy/publication behavior. Use the official LTFS file backend on the isolated Linux acceptance host through packaged binaries and local orchestration; Volume tests use isolated already-mounted directories.
- Run [physical Tape E2E](physical-tape-e2e.md) only with explicitly assigned scratch media and isolated databases/configuration. A documentation update is not authorization to format, write, or test a cartridge.
- Test migration only on timestamped `cp -a` copies of explicitly approved source backups. Keep private source paths and detailed reports outside the public repository. Never modify source backups or production installations during validation.
- Compare every migrated Job item-by-item, including non-empty historical Jobs, not only catalog counts. Cover prepare, abort, repeat prepare, commit, and separately confirmed cleanup.
- Clean only validated temporary test media, mounts, databases, Job bundles, and migration copies after validation. Never use production paths as cleanup targets.
- Batch confirmed review fixes before expensive E2E rather than repeating the full suite after each comment. Record actual results and unrun checks accurately; do not claim planned tests passed.

## Delivery Gates

The candidate preserves [independent format identities](../architecture/persistence.md#published-data-formats) and legacy migration/import. Record acceptance for the exact candidate bytes; earlier development results do not certify a rebuilt release. Preserve a forward path for supported published data. Before the first stable v1 release, remove the [unpublished-Draft exception](../README.md#temporary-draft-compatibility-policy) and publish the stable API/upgrade policy.

v1 development stays on `v1`; keep `master` aligned with `origin/main`. ACP integration is pushed only to its development branch while review is incomplete. Do not push YATM, merge ACP main, tag, or publish a release without explicit confirmation. Release packaging includes the documentation and glossary; source-code links in architecture documents refer to a repository checkout, available from the [project repository](https://github.com/samuelncui/yatm).
