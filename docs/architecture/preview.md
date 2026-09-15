# Preview Storage

Preview is an optional stage of the unified [Scan pipeline](jobs.md#scan), not a separate runner or Job kind. It accepts random-readable Location/Library selections and supported mounted Media with the same frozen observations, signature policy and identity validation. Sequential Tape sources reject Preview rather than staging or implicitly restoring content. Report-only scans do not admit Files merely to generate derivatives.

Generation is off by default. The shared Preview policy selects none, missing-only or regenerate-all. KNOWN_ONLY without a usable content identity skips and counts the entry, without hashing. FILL_MISSING and FORCE_READ acquire required facts through ACP. Generators may read source bytes but cannot upgrade the hash policy. Generator failures are per-item findings, not a rollback of valid observation publication; source drift and final identity failure still invalidate their scope.

Archive can create a companion SCAN configured for Preview after successful indexing, from its already frozen manifest. It does not independently re-admit or rescan live roots. Archive progress retains the companion Job ID or creation error. Companion failure does not undo Archive preparation or copying; there is no cross-Job transaction or compensation.

## Generation

The Scan manifest retains expected facts and Preview outcomes. Each attempt holds Library maintenance admission. The shared content stage supplies SHA-256, size and mtime to the [Preview manager](../../preview/preview.go), which checks source metadata before and after generation without starting another hash stream. Source enumeration, cancellation and checkpoint behavior belong to Scan.

Generators are registered by kind and configured extensions. Selection and generation use the actual original filename, not its independently organized Library name. Image dimensions, video sampling interval/frame count, output format, and other generator settings belong to [configuration](../../preview/config.go), not user Job input. ffmpeg dimensions, sampling, assets, manifest, and output bytes are bounded. Video timeline tiles retain their configured dimensions, including odd sizes, so encoded sprites and WebVTT coordinates agree regardless of the source pixel format.

## Addressing and Update Policy

The current producer uses the Library v1 signature from SHA-256 plus size. The [manager's bundle path](../../preview/preview.go) shards its hexadecimal form into three successive two-character directories and names the indexed ZIP with the remaining characters. [Bundle storage](../../preview/storage.go) puts `manifest.pb` and generated assets inside that ZIP. Direct role-based asset reads do not require unpacking the full bundle or a Preview database.

Missing-only generates assets for known content without an existing bundle. Regenerate-all replaces existing bundles even when generator settings are unchanged, independently of the signature policy; it also permits replacing damaged derivatives. Identical content shares assets across independently organized Files and versions and is generated once per Scan. Archive's companion Scan uses the same policy. Job deletion does not remove bundles.

The [HTTP asset endpoint](../../apis/files.go) resolves current display content or an explicit version_id to its own hash/size-derived Preview key and manifest role, independently of opaque archive signatures. FileCatalog GetVersion provides version-specific Preview metadata. Missing or damaged Preview bundles do not block File/version metadata responses; damaged derivatives are logged, while direct asset reads still report errors. Unknown current originals never use an old version's assets as current. Generating previews from offline archive Media remains outside the public workflow. [Generator tests](../../preview/generator_test.go) and [Preview E2E](../operations/e2e-test.md#preview-update-policy) cover the implementation.

## Asset State

Asset validity is derived independently from the [Job lifecycle](jobs.md). It does not add catalog Job states.

| Observation | Display and allowed operation | Failure/retry |
| --- | --- | --- |
| Current content unknown | No current Preview; explicit saved versions remain selectable | Request a Scan that acquires missing facts |
| Matching readable bundle | Serve the requested role | Job removal does not remove assets |
| Bundle absent or damaged | Keep File/version metadata; report missing/error on asset access | An explicit Scan with Preview can regenerate |
| Generator settings outdated | Keep the asset in missing-only mode | Regenerate-all replaces it |
| Generation fails or is canceled | Keep completed assets and successful Scan observations | Retry through Scan |
