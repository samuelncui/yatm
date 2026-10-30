# Release Process

The maintainer procedure for building, accepting and publishing a release.

## Release Packages

Release packages must include the separate User Guide entry, all current user guides and their
required local attachments. Architecture, research, contributor rules and developer validation stay
in the repository. Packaging uses an explicit allowlist rather than copying the whole tree; update
that list in [build.sh](../../build/release/build.sh) and its validation whenever the user documentation set changes. Required user procedures
must work from the extracted package; optional implementation references may link to the exact
release's public source.

## Release SOP

Run this procedure for every release. The release workflow enforces the automatic gates below;
source review and physical acceptance retain their own explicit decisions.

1. **Review scope and repository content.** Compare the public baseline with the final source by
   Files/Locations, Identical, Jobs, Preview, installation/migration and API/CLI. Map retained
   capabilities to their consumers and meaningful tests; resolve unclear removals before freezing
   source. Check Demo coverage, including large results and real licensed media where relevant.
   Review terminology and affected operator/architecture instructions against actual commands and
   behavior. Update the existing owner, remove superseded claims and close verified issue records.
   Check the [Tape script compatibility contract](../architecture/tape.md#tape-script-compatibility)
   against the published adapters: run unchanged historical scripts through the current caller,
   and verify that upgrade preserves customized scripts/helpers. For an explicitly approved
   incompatibility, check the actionable installer notice and the adapted script boundary.
   Cover the [Tape lifecycle](../architecture/tape.md#tape-lifecycle), including empty cartridge memory
   barcode FORMAT, subsequent identity checks, APPEND and Restore.
   Keep these checks in the automated suite; a new-template hardware pass alone does not cover upgrades.
   Inspect modules, tools and temporary files for a current runtime, generator, test or packaging
   consumer before deleting them. Review ignored local directories separately; CI cannot inspect
   workstation leftovers. Accepted limitations remain documented and do not become release blockers.
2. **Audit the history and prepare a reviewed commit stack.** Scan the development range before
   restructuring it, including intermediate source and commit messages. Review credentials,
   internal services, personal paths, real data, configuration and private notes manually as well
   as with the pinned scanner. Rotate an exposed real credential before attempting public-history
   cleanup; history rewriting needs separate authorization. Recheck the public branch tip and
   retain a local backup. Re-slice only unpublished development, from its final source, into a
   stack whose parent is that tip: each commit introduces the final form of one concern with its
   tests, later fixes and renames belong to the commit they correct, and commits follow dependency
   order. An incompatible contract change lands with all of its callers. Documentation lands with
   its concern, or in one final documentation commit when new documents and the index link to
   each other.
   Every commit must build and pass its Go and frontend tests on its own, and the stack's final
   tree must equal the reviewed source. Do not publish the backup branch or rewrite already
   public history. Review every commit and message again; restructuring does not sanitize
   content. Finish required dependency releases and update their pins before freezing YATM.
3. **Complete review and fixes with affected checks.** Review the stack and finish focused
   fixes/verification until no unresolved, unaccepted finding remains. Fold each fix into the
   stack commit that owns the change and recheck the affected commits. Use
   [Evidence Reuse](#evidence-reuse); do not run a full preflight per fix.
   Complete the [performance acceptance](../development/performance.md#performance-acceptance) before source-push approval,
   retaining unaffected measurements and checking only changed operations again.
   Start affected hardware verification from a reviewed local build as soon as its source and
   nonphysical checks pass, following [Evidence Reuse](#evidence-reuse). Do not
   wait for GitHub or the full release matrix to investigate or verify a hardware fix.
   For workflow edits, validate GitHub syntax and expression contexts before pushing with
   `go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.12 -shellcheck= -pyflakes=`;
   script diagnostics remain covered by the existing shell checks.
4. **Run the final source preflight on the clean stack tip.** Go commands in release gates
   and package builders use `GOWORK=off` so dependencies resolve from committed module files.
   Development checks can still use a local workspace. The candidate workflow owns
   the final full run before its platform matrix. A local full run is available for an intentional
   integration check or diagnosis, but is not a prerequisite that CI must then repeat.
   Put Bash 4.4 or newer on `PATH` for
   the Linux installer unit tests; macOS's bundled Bash 3.2 is insufficient. Install the frozen frontend dependencies,
   pinned scanner and the protobuf tools used by current generated source: protoc 26.0,
   protoc-gen-go 1.33.0 and protoc-gen-go-grpc 1.3.0. Preview acceptance also requires a separate
   FFmpeg fixture generator with libx264, 10-bit libx265, PNG, lavfi and display-rotation support.
   Native release CI also prepares the checksum-pinned CC0 camera samples from the
   [worker fixture inventory](../../previewworker/testdata/raw-fixtures.json), so real-camera
   acceptance runs for every advertised RAW family instead of skipping for missing local data.
   `node build/release/check-preview-fixtures.mjs` generates tiny real inputs and fails on missing
   tools or features. Linux CI builds the pinned generator with
   `bash build/release/build-test-ffmpeg.sh OUTPUT_DIRECTORY` after installing pkg-config,
   libx264-dev, libx265-dev and zlib1g-dev, and adds its `bin` directory to `PATH`. This is a test
   dependency; installed YATM and its packaged helper do not need it. Use a complete Git checkout and run:

   ```shell
   RELEASE_VERSION=vX.Y.Z RELEASE_COMMIT="$(git rev-parse HEAD)" \
     GITLEAKS_BIN=/path/to/scanner/gitleaks make release-check
   ```

   The preflight rejects dirty or mismatched inputs, incomplete history, unowned source paths,
   runtime databases/logs, local configuration, compiled output, archives and scratch files.
   It scans current source and all history reachable from HEAD, then runs documentation/IDL,
   Go/frontend/build-tool checks, full race checks, both SQLite paths and two clean generations.
   Native Preview race tests and vet run with its private libraries during each runnable helper build.
   Its corresponding-source archive has a separate inventory that retains required vendor/native
   sources. Filename gates do not prove that a module is used or documentation is semantically correct.
5. **Build and accept one candidate set.** GitHub Actions requires the exact reviewed commit to
   exist in this repository's remote. Obtain explicit authorization for that source push before
   dispatch; this does not authorize a tag, Release or asset upload. Dispatch the Release candidate
   workflow with `publish=false`. Its platform matrix waits for `make release-check`. Candidate CI
   validates the complete [package set](#release-backend-builds), public archive content, packaged documents, licenses,
   identities and checksums. Check the exact extracted bytes at affected package boundaries
   for installation/upgrade, supported `v0.1.x` migration, Archive, Restore, Scan, Volume/LTFS
   and native Preview. Apply [Evidence Reuse](#evidence-reuse) to retain valid
   earlier behavior and hardware evidence instead of repeating every workflow after a rebuild.
   Use the [local package controller](package-acceptance.md#remote-packaged-binary-acceptance) with the exact
   Linux main/Preview archives, `--ltfs` and a new private evidence directory for its named workflows.
   Its report does not replace separate browser, legacy-backup or physical-media acceptance.
   Complete the [Media and migration gates](#media-and-migration-safety), including separately
   authorized scratch Tape acceptance. A skipped or unavailable required check remains open.
6. **Approve and publish the accepted bytes.** Review the final public source, commit message,
   Release text and all assets. Obtain publication approval before tag or Release creation and
   asset upload; any further source push also needs explicit authorization.
   Follow the existing [upload procedure](#candidate-and-publication) with the successful candidate
   run ID; it rechecks provenance, content and checksums without rebuilding. Verify public asset
   checksums against the accepted files after upload.
7. **Record evidence and clean owned resources.** Report passes, failures, accepted limitations and
   unrun checks accurately. Keep raw verification output in CI artifacts and sensitive evidence
   private. Remove only temporary processes, directories and test resources owned by that run.

## Release Backend Builds

Linux backend builds require Zig **0.15.2** and always enable CGO, selecting the existing mattn C SQLite driver. `build/backend/build.sh` preserves version/commit metadata and uses external static musl linking. `build/backend/cross-linux.sh` defaults to amd64; set `GOARCH` and, for ARM, `GOARM` explicitly. Non-Linux release builds retain their existing `CGO_ENABLED=0` strategy.

| Go target | Zig target | CPU selection |
| --- | --- | --- |
| linux/amd64 | x86_64-linux-musl | default |
| linux/386 | x86-linux-musl | default |
| linux/arm64 | aarch64-linux-musl | default |
| linux/arm, GOARM=5 | unsupported | Zig 0.15.2 lacks the ARMv5 `__sync` atomics required by Go CGO |
| linux/arm, GOARM=6 | arm-linux-musleabihf | arm1176jzf_s |
| linux/arm, GOARM=7 | arm-linux-musleabihf | cortex_a7 |
| linux/s390x | s390x-linux-musl | default |

ARMv5 and other unmapped Linux targets or ARM values fail before compilation. CI does not build ARMv5, and the candidate set contains no ARMv5 archive. There is no automatic pure-Go fallback. Every built Linux program passes `build/release/check-static-linux.mjs`: ELF program headers must have no interpreter or dynamic segment, Go build metadata must enable CGO, and no native Preview module may enter the main dependency graph. This excludes shared libc/SQLite/FFmpeg dependencies. Runtime acceptance remains Linux amd64; other mapped release targets are experimental until exercised on matching hardware.

Distributed main binaries omit Go debug data and strip native debug data at the Linux external
link, so compiler and cache paths do not become public package content. Package scanning checks
the final executable strings as well as text files; trimming Go source paths alone is insufficient.

```shell
GOARCH=amd64 ./build/backend/cross-linux.sh
node build/release/check-static-linux.mjs output/yatm-httpd
```

The [optional native Preview helper](../../previewworker/build/README.md) has its own module, build, platform baseline and private native libraries. Its native dependencies are never linked into the main binaries.

The source-check job builds the frontend once and collects its locked production dependency
notices. The internal `shared-frontend` artifact records version, commit, lockfile hash and every
file checksum. Each main-platform build validates its identity and exact file inventory, then
copies the same frontend and Node notices and adds its own Go/native notices. Source maps, private
configuration, missing files and extra files are rejected. The internal manifest is never copied
into a release archive. Local `make release` calls the same collector; set
`FRONTEND_ARTIFACT_DIRECTORY` to reuse a previously collected artifact with matching inputs.
Preview native-object caching follows the [native build guide](../../previewworker/build/README.md#native-compiler-cache).

Release validation requires exactly nine main archives, three optional helper archives and three matching source archives. Each main archive still passes `build/release/check-release.mjs`; `build/release/check-candidate-set.mjs` additionally verifies the complete platform set, matching identities, helper file boundaries and corresponding-source checksums. Linux E2E receives the extracted helper through `YATM_TEST_PREVIEW_HELPER` so native Preview acceptance cannot silently skip.

Release builds require clean committed source and a matching `RELEASE_COMMIT`. Inputs are exported
from that commit, so ignored workstation files cannot enter recursive copies. The platform matrix
starts only after the source checks, full race suite and both SQLite paths pass. Frontend tests use
two workers to bound local and CI resource use.

## Content Scanning

Public content checks use checksum-pinned Gitleaks 8.30.1 with its default credential rules and
additional personal-path/private-service checks. The installer also pins the default rule source;
scans retain its credential rules while removing global dependency/generated-file exclusions.
Install it with
`bash build/release/install-gitleaks.sh /path/to/scanner`, set `GITLEAKS_BIN` to that directory's
`gitleaks`, and run `node build/release/check-content.mjs source . HEAD`. A different revision range
can inspect unpublished development before restructuring it. `node build/release/check-content.mjs artifacts /path/to/candidates` checks
nested archives and embedded executable/library strings, normalizing npm `.tgz` names in a private
scan copy so the scanner opens them. Source maps and dependency sources are
included; the only project exception is the exact deterministic physical-Tape fixture seed at its
owned path. A repository's `.gitleaksignore` may contain explicitly approved historical fingerprints
in full `commit:file:rule:line` form. Only the Git history scan reads them; current source and package
scans always use an empty ignore file. Commit-free, wildcard and path/rule-wide exceptions are
rejected. Additions require a separate decision about the specific already published finding;
new commits remain fully scanned. Checksum-validated dependency sources have rule-specific exceptions for public source
identifiers, deterministic test values and recorded upstream build/sample paths. Each is restricted
to its exact nested member and matched value; other credentials or paths in those members still fail. Failures
report rule counts without match snippets, and temporary scanner reports are
deleted instead of uploaded as public CI evidence. Scanner regression tests use the same executable.

## Candidate and Publication

The **Release candidate** workflow runs only through `workflow_dispatch`. Set `version` and leave
`publish=false` to build the candidate set at the selected commit, validate every package and run the
full Linux E2E suite against extracted candidate binaries and the native helper. A successful run
retains the complete accepted set, companion checksums and `SHA256SUMS` in `accepted-candidates`.
Build completion establishes neither performance acceptance nor publication approval.
Creating a GitHub Release does not start a build or upload.

For local or remote package acceptance, download only the selected platform's artifacts into
one directory identified by the candidate run. For example, Linux amd64 acceptance uses:

```shell
gh run download "$CANDIDATE_RUN_ID" --repo samuelncui/yatm \
  --name candidate-linux-amd64 --name candidate-preview-linux-amd64 \
  --dir "$CANDIDATE_DIRECTORY"
```

Multiple selected artifacts are extracted into per-artifact subdirectories. The Preview artifact
includes its helper and matching corresponding-source archive. Verify the
downloaded checksums and expected commit, and retain these files for subsequent cases and physical
acceptance. Record the final successful run and artifact IDs; tests started before candidate CI
finishes remain provisional until that same run succeeds. Do not download the complete
`accepted-candidates` merely to repeat passed CI inventory, content or checksum checks locally.
The publication job retrieves that complete artifact on GitHub when it needs all release assets.

To deliver those accepted bytes, explicitly create the matching Release and tag first, then dispatch
with the same `version`, `publish=true` and its successful build `candidate_run_id`. Publishing runs
no builds or E2E; it downloads that run's immutable artifact by ID. The run must belong to this
repository and the Release candidate workflow (by workflow ID and path), identify a manual build of
that version, and have completed successfully. Its accepted artifact must still be available and unexpired. The release
tag, including an annotated tag, must resolve to the run's exact commit, and the Release prerelease
flag must match the version. Candidate identity comes from run metadata and package contents rather
than the upload checkout. Release tools come from `v1`, pinned to the same commit for preflight and
upload; no code from the candidate artifact is executed during upload.

Upload repeats provenance and public-content checks, validates the exact 9+3+3 set and every checksum
with the existing package validator, and sends the unchanged archives and checksum files to that
existing Release.
Only the upload job has `contents: write`; candidate lookup and download use scoped `actions: read`.
The workflow neither creates a Release or tag nor replaces existing assets. An expired artifact needs
a new build and acceptance run before publication. Mocked provenance and upload tests run through
`node --test build/release/publish-candidate.test.mjs` without publishing anything.

## Evidence Reuse

Finish review and fixes before the final full source and package acceptance. The candidate CI
runs `make release-check` once before its platform matrix; a duplicate local full preflight is
not required. Use `make check` for an intentional full integration check, not after every edit.
If a final check finds a defect, verify the fix locally at its affected boundaries before
starting the replacement candidate. Changes to shared contracts or infrastructure may justify
wider checks; record the reason instead of automatically repeating everything.

Retain passing evidence for unaffected components when their implementation, relevant
dependencies, configuration, fixtures, harness and environment remain unchanged. Record the
old and final source identities and the reviewed diff that establishes this. New archives still
need their own content, identity and checksum checks. Rebuilt binaries require relevant package
smoke tests; changed behavior requires its package workflows. Acceptance of earlier bytes alone
cannot certify a new archive, but a new archive does not require repeating unrelated benchmarks
or every exploratory/manual check.

Hardware verification may use locally built binaries after the affected source checks pass;
GitHub availability and completion of the full platform matrix are not prerequisites. Build
from the reviewed commit with the repository's existing scripts and record the source,
toolchain, build flags, binary checksums and runtime adapters. Use the release package when
installation or package layout is part of the check. Follow the
[physical Tape execution procedure](../release/physical-tape-runbook.md#automated-stages) for host context,
absolute LTFS adapter paths, the scoped two-partition file-backend precheck and durable staged
execution. Hardware work can start independently of CI's final release-byte acceptance;
that acceptance remains required for release delivery. Source CI and other platform builds can
run in parallel. Keep the final artifacts' identity, content and affected package smoke checks;
reuse expensive hardware results, including a full Tape write, when comparison confirms that
the relevant source, dependencies, build configuration and runtime inputs are unchanged.
If those inputs differ or cannot be established, repeat the affected checks and explain why.

Reuse downloaded candidate files for subsequent checks when their run, commit and checksums
match. Transfer only the packages and fixtures needed by the selected host and cases. A new
report or another case selection does not require another download. Complete-set validation
belongs to candidate CI; use its run and artifact records for checks already completed there.

## Media and Migration Safety

- Run [mounted Volume and LTFS file-backend E2E](../development/e2e-test.md) for affected copy/publication behavior. Use the official LTFS file backend on the isolated Linux acceptance host through packaged binaries and local orchestration; Volume tests use isolated already-mounted directories.
- Run [physical Tape E2E](physical-tape-e2e.md) only with explicitly assigned scratch media and isolated databases/configuration. A documentation update is not authorization to format, write, or test a cartridge.
- Test migration only on timestamped `cp -a` copies of explicitly approved source backups. Keep private source paths and detailed reports outside the public repository. Never modify source backups or production installations during validation.
- Compare every migrated Job item-by-item, including non-empty historical Jobs, not only catalog counts. Cover prepare, abort, repeat prepare, commit, and separately confirmed cleanup.
- Clean only validated temporary test media, mounts, databases, Job bundles, and migration copies after validation. Never use production paths as cleanup targets.
- Batch confirmed review fixes before expensive E2E rather than repeating the full suite after each comment. Record actual results and unrun checks accurately; do not claim planned tests passed.

## Delivery Gates

The candidate preserves [independent format identities](../architecture/persistence.md#published-data-formats) and legacy migration/import. Record acceptance for the exact candidate bytes, reusing unaffected source-level evidence under [Evidence Reuse](#evidence-reuse); earlier development results alone do not certify a rebuilt release. Preserve a forward path for supported published data. Before the first stable v1 release, replace the [pre-stable compatibility policy](../README.md#pre-stable-compatibility) with the published stable API/upgrade policy.

Branch, push and publication gates follow [Delivery and Authorization](../../AGENTS.md#delivery-and-authorization). Release packages include only the operator documents allowlisted in [Release Packages](#release-packages); development and architecture documents remain in the repository checkout.
