# Entity State Reference

Status: v1 Alpha 1 contracts. Each state is defined in its owning document, not a shared state-machine framework.

| Entity or observation | Owning contract |
| --- | --- |
| Location confirmation, per-file observation token, analysis coverage, accessibility and Busy | [Library](library.md) |
| File/FileVersion derived coverage and historical availability | [Library](library.md) |
| Job persisted lifecycle, runner phase, errors, cancellation and retry | [Jobs](jobs.md) |
| Restore copy/validation/association results and idempotence | [Jobs](jobs.md) |
| SCAN content/result policies and successful-scope publication | [Jobs](jobs.md#scan) |
| Request-bound physical file-operation outcomes (not Job states) | [Library](library.md#physical-file-operations) |
| Position health versus Media accessibility and exclusive leases | [Media/I/O](media-io.md) |
| SCAN preview-stage outcome versus asset validity | [Preview](preview.md) |

Location names are user content, not states. In particular, an imported directory is shown as requiring local confirmation because of its binding state, never because of its name. Runtime observations retain their check time and cannot silently replace durable facts after an error.
