# Code Layout and Test Ownership

Where new Go types, files and tests belong. [AGENTS.md](../../AGENTS.md#repository-layout) owns the
repository layout, and the [architecture overview](../architecture/overview.md) owns module ownership
and dependency direction.

## Type Roles and File Layout

Every durable type remains in the package that owns its invariant. When one package needs several
representations of the same subject, names make their lifecycle explicit:

- a private `...Row` is the exact persisted table shape and carries only persistence behavior;
- a runtime aggregate or snapshot carries execution state but has no ORM tags or hooks;
- a projection is a read result assembled for a particular query and is never saved as a row;
- an adapter translates between an owning representation and a real cross-module interface;
- an `entity` type is a wire or stored-blob contract, not the default internal model.

Files follow the same roles. A cohesive type may keep its ordinary domain filename; when roles would
otherwise mix, use focused names such as `job_row.go`, `job.go`, `file_projection.go` or
`match_store.go`. Focused responsibility files live inside their existing owner; a catch-all
`models.go` file is not an ownership boundary.
A new subpackage is introduced only when it owns behavior and invariants behind a useful interface;
moving passive structs into a separate package only adds navigation and dependency cost.

Persistence hooks never assemble presentation state. Queries explicitly load the projection or
snapshot they promise, so production behavior does not depend on callers remembering to disable an
ORM hook. The process composition root creates the Settings module, and attempt admission freezes the
validated effective settings used by a runner.

## Test Ownership

Tests live beside the behavior they protect. Row tests own table names, columns and indexes; module
tests own transactions and state changes; interface contract tests run against each real adapter; API,
CLI and end-to-end tests retain only behavior visible at those boundaries. A lower-level test is not
repeated through test-only compatibility methods, and tests for removed commands or superseded wire
shapes are deleted rather than retained as historical documentation. Stable wire numbers, published
formats and supported migration inputs remain positive compatibility tests.
