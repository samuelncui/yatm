# Architecture Overview

Status: Current v1 Alpha architecture.

The [glossary](../../CONTEXT.md) owns domain definitions. YATM organizes ongoing Files with at most one online original, saved FileVersions, and signature-linked independent Tape/Volume copies. The [Alpha release notes](../releases/v1.0.0-alpha.1.md) summarize acceptance and platform limitations; Draft designs describe proposed work separately.

## Ownership

| Component | Responsibility | Boundary |
| --- | --- | --- |
| Library | Files, annotations, versions, Media/Positions and health, Locations/originals/tracking, operation results | Metadata transactions; no physical copy orchestration |
| Executor | Job catalog/resource indexes, runner lifetime, cancellation, Job locking, device allocation | One execution owner per Job |
| Job runner | Typed specification, durable manifest, operation phase, progress | Archive/Restore own their ACP flow |
| Media Backend/Session | Physical preparation, safe paths, capabilities, finalization | Stateless factory and short-lived sessions |
| Preview manager | Generators and content-addressed derivative bundles | Independent of Media and device leases |
| gRPC/HTTP and frontend | Typed operations, browsing, content/backup transfer, polling | No second execution state machine in clients |

Application entry and configuration are in [HTTP service](../../cmd/httpd/main.go), [configuration](../../config/config.go), and the [example configuration](../../config.example.yaml). [Executor construction](../../executor/executor.go), [Job registration](../../executor/job_type.go), and [API wiring](../../apis/api.go) connect the modules.

## Main Flows

- Archive: filesystem selection → durable manifest → ACP copy → backend validation → Library publication → submitted Job items.
- Restore: explicit FileVersion selection → signature-matched candidates → ordered Media reads → verified content, version metadata and final Media identity → Library association/result publication → completed items.
- Scan: frozen Location/Library/Media input → bounded enumeration → policy-controlled facts/reads → optional comparison/Preview → final validation → selected publication policy → checkpoint. Integrity checking always reads against old baselines.
- Files browsing: actual Location directory or logical Library page → optional File associations and visible-page observations; pure reads never admit or fall back to cached physical rows.
- File operations: one shared bounded planner → guarded Library/filesystem primitives → exact successful association publication and per-item results; no Job.
- Library-selected Archive: frozen File identities/logical targets → usable online originals → verified ACP transfer → ordinary Media publication.

See [Jobs](jobs.md) for operation and retry semantics, [Media/I/O](media-io.md) for physical guarantees, and [persistence](persistence.md) for database ownership. Library organization never renames or removes physical Media files. File paths on Media remain ordinary LTFS/filesystem paths, recoverable without a YATM-specific data format.

[Entity states](states.md) navigates to each owning contract; binding, accessibility, copy health, execution phase and UI summaries are separate concepts.
