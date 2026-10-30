# Documentation Rules

How repository documentation is owned, decided and updated. Contributors follow these rules for
every change to a document.

## Documentation Ownership

Every maintained fact has one owner. Other documents link to that owner instead of restating it.

| Location | Owns | Does not own |
| --- | --- | --- |
| Root `README.md` | Released product introduction, quick start and documentation entry points | Development status, architecture detail or repeated runbooks |
| Root `CONTEXT.md` | Domain vocabulary and core domain invariants | Schemas, algorithms, task notes or UI layout |
| Root `AGENTS.md` | Contributor rules, safety gates, repository layout and required verification | Product or architecture contracts |
| Root `TODO.md` | Concise confirmed pending work with source Issue links | Detailed designs, implemented behavior or completed-work history |
| `docs/README.md` | Developer entry, complete documentation index and the pre-stable compatibility policy | Daily-use instructions or documentation rules |
| `docs/operations/index.md` | User entry for installation, configuration and operation | Architecture, contributor rules or test procedures |
| `docs/architecture/` | Current implemented behavior, module interfaces, invariants and durable constraints | Proposals, implementation plans, acceptance transcripts or superseded behavior |
| `docs/operations/` | Packaged user guides: current setup, migration, configuration, use and maintenance | Domain definitions, architecture specifications, test procedures or measurement results |
| `docs/development/` | Contributor procedures: documentation rules, code layout, checks, E2E, performance and the Demo | Release gates, user procedures or product contracts |
| `docs/release/` | Maintainer release procedures: SOP, builds, package acceptance, the physical Tape gate and physical measurements | Contributor checks, user procedures or measurement research |
| `docs/research/` | Public technical studies, evidence, limitations and evaluation of possible approaches | Product guarantees, implementation backlogs, private incident reports or raw acceptance logs |
| `.agents/skills/yatm/` | Installed agent operating and safety instructions | Human-facing architecture or change history |
| Component `README.md` files | Commands and usage local to that component, such as `frontend/` conventions | Product behavior owned by `docs/` |

## Decision Authority

Within overlapping scope, broader accepted constraints bound narrower descriptions: the repository
engineering rules, the domain invariants and compatibility policy, the architecture overview, area
architecture, operations guides, and finally implementation and tests. A lower level may provide
evidence and implementation detail, but its existence cannot justify complexity or override a broader
decision. When they conflict, update the lower-level documents, code and tests together.

The repository intentionally has no documentation archive, ADR archive or redirect stubs.
Git already retains deleted text and rationale. Root `TODO.md` records concise confirmed pending
work; detailed implementation plans belong in an Issue or pull request. Remove completed TODO
items instead of retaining a work history. Public research follows the scope above.
Published change notes belong to GitHub Releases. Raw acceptance logs, checksums,
measurements, screenshots and host-specific reports belong in CI artifacts rather than Markdown.

## Update Workflow

1. Establish the intended behavior from the decision authority above, then inspect `CONTEXT.md`, the
   owning document and the implementation. A mismatch is something to resolve, not a reason to
   promote the current implementation into a requirement.
2. Update the implementation, semantic tests, owning architecture document and affected operating
   guide together. Describe the final behavior in present tense; remove the replaced text instead of
   marking it historical, implemented or superseded.
3. Keep unsettled implementation designs in a PR or Issue. Public studies may evaluate alternatives
   in `docs/research/`, with evidence, assumptions and remaining proof clearly identified. After
   implementation, update the current architecture and link to research for supporting evidence.
   Update or remove invalidated research conclusions instead of accumulating superseded studies.
4. Keep implementation detail only when callers or maintainers must know it to use the module
   correctly. Function names, CSS placement, one measurement, one candidate checksum and step-by-step
   development history belong in code, tests or verification output.
5. Keep repository documentation self-contained. Every repository-relative link or file reference
   must resolve to a tracked file, or to a new file committed in the same change; external references
   use stable public URLs. Private notes, ignored artifacts and machine-local paths are never sources
   for repository documentation.
6. Before delivery, run `make check` or at minimum `node dev/check-documents.mjs` and
   `git diff --check`. The document check enforces the allowed tree, complete index, valid relative
   links and the absence of private/history references.

For research, use official specifications and source code together with relevant original community
reports and maintainer responses. Record what was observed, what was actually resolved and which
explanations remain hypotheses; a forum's resolved label alone is not evidence of a verified fix.
Use stable public citations and concise summaries. Keep credentials, local paths, device identifiers,
private host details and raw logs out of public studies; include only the technical context needed
to assess the evidence. Hardware measurements establish the tested configuration's behavior, while
general guarantees require an applicable specification or other evidence with a defined scope.

Use English Markdown, short topic-based kebab-case filenames and repository-relative links. Current
architecture filenames are version-independent. Adding a new documentation file requires a distinct
current owner that cannot be expressed clearly in an existing file; update this index in the same
change.
