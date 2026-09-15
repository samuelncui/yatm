# v1 Alpha Release and Upgrade Delivery

Status: Draft — implementation and candidate acceptance complete; publication approval and public installation verification remain open.

## Delivery Goals

Publish `v1.0.0-alpha.1` after explicit approval, with a documented and tested upgrade from `v0.1.x`. Users install verified release binaries through the README commands. Installation, migration and recovery preserve user customization and retain all upgrade artifacts below the installation root.

The [migration guide](../operations/migration.md) owns the executable upgrade procedure and Tape adaptation contract. [Installation](../operations/install.md) owns fresh setup and the optional Skill. [Persistence](../architecture/persistence.md#published-data-formats) owns independent artifact identities/revisions; [candidate notes](../releases/v1.0.0-alpha.1.md) own release acceptance results.

## Public Release and Development Baseline

- [x] Privately preserve development code, Git history and release evidence outside the repository.
- [x] Withdraw the misnumbered experimental Release, assets, tag, development branch and associated CI records; preserve `v0.1.x` references and history.
- [x] Rebuild development commits on `v1` from the existing `main` baseline, with independent upgrade changes and English commit messages.
- [x] Audit reachable public references, source, documentation, UI/CLI and packaging for project-version consistency.

Withdrawal covers publisher-controlled entries. Previously downloaded copies, third-party caches and unreachable hosting objects can remain accessible. New publication requires separate approval.

## Naming and Compatibility

- [x] Use `v0.1.x → v1.0.0-alpha.1 → v1.0.0` for software versions; use legacy/current for data generations.
- [x] Package `yatm-migrate`, with legacy conversion packages and explicitly named staging/legacy tables.
- [x] Preserve Catalog, Job bundle and Library backup identities and initial revision 1, together with established protobuf, encryption-key, signature and physical storage encodings.
- [x] Keep the default installation channel stable and require an explicit Alpha version.
- [x] Preserve legacy upgrade behavior in the old installer, with complete option validation and cross-major protection.

## Installer and Recovery

- [x] Display migration instructions from the verified candidate package before cross-major consent; reject unknown versions, absent instructions, cancellation and EOF before stopping the service.
- [x] Keep `--check` read-only and distinguish pass/manual/blocking findings. Persist actual attempt reports before consent.
- [x] Preserve user scripts, helpers, configuration, permissions, service paths and working directory; fresh installation deploys default templates.
- [x] Provide initial `--config` guidance and a Volume-only configuration. Report Tape readiness independently of installation and execute no Tape scripts during preflight/acceptance.
- [x] Retain the pinned Skills CLI's optional picker after fresh install, update and same-version rerun, with ordinary-user guidance and independent cancellation/failure.
- [x] Create verified complete backups from a fixed entry list under `.yatm-upgrades/<attempt>`, excluding only the owned upgrade area; check installation-filesystem space and protect the area from Location access.
- [x] Reuse Prepare → report approval → Commit, validate every historical Job/item and logs against the explicit backup, then perform repeatable cleanup of obsolete active artifacts.
- [x] Make cleanup and historical Job repair consume an explicit complete-backup root, independent of retained active legacy tables.
- [x] Replace release-owned resource directories completely while preserving user files and still-used captured indexes.
- [x] Document and exercise in-root full rollback that retains failed contents, with separate backup deletion consent and non-atomic replacement limits.

## Candidate Acceptance

The [E2E matrix](../operations/e2e-test.md#release-candidates-and-upgrades) defines executable checks. Results belong in candidate notes only after running this candidate's actual binaries.

- [x] Real systemd fresh install, same-version rerun and `v0.1.x` upgrade; verified-package guide output and unchanged service on preflight failure, cancellation, EOF, bad checksum, unknown version or Busy.
- [x] Two upgrades without nested backups, unchanged backup hashes, preserved user scripts/helpers/configuration and no artifacts in the installation root's parent.
- [x] Backup/Prepare/Commit/cleanup/replacement failure semantics locally; actual systemd startup rejection and full in-root rollback. The candidate notes distinguish injected failures from remote execution.
- [x] Approved timestamped legacy backup copies, item-by-item historical manifests, frozen Restore paths, archived logs and repair after active legacy-table cleanup. The private historical fixture contains Archive Jobs; Restore-path coverage uses synthetic semantic fixtures.
- [x] Unit, vet, race, both SQLite drivers, generation/CLI/frontend, document links and all ten platform package checks.
- [x] Full local/CI candidate CLI E2E, plus remote Volume/Preview and official Linux LTFS file-backend regression using only the corresponding release package's YATM binaries. Keep orchestration local via SSH; transfer no repository source, separately built harness or helper source. Physical Tape hardware is a separately reported gate.
- [x] Optional Skill picker, cancellation, repeat installation and ordinary/sudo user in an isolated home; dependency absence covered by a local shell test.
- [x] Explicit-reset Demo and browser acceptance.
- [ ] Candidate, hashes and evidence review; publication approval; public-asset README installation check.
- [ ] Apply the public procedure to the authorized real installation after candidate acceptance; report service readiness and any remaining manual Tape script adaptation separately.

## Work Ownership

The main task owns release withdrawal, baseline reconstruction, version/shared contracts and integration. Installer/report behavior and migration validation/cleanup are independent implementation streams; documentation follows their confirmed interfaces. Integrate them before the review–fix loop and expensive end-to-end acceptance. Preserve existing unrelated changes and keep private migration fixtures/evidence outside the public repository.
