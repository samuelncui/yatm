# Command-line interface

Status: Current v1 development architecture.

The `yatm-cli` executable is the scripted and agent-facing surface. It calls the same
[gRPC and HTTP API](api.md) the browser does, so a CLI request carries no separate
server semantics; [Interface contracts](contracts.md) owns the rules shared with those
surfaces.

## Shape

[Command registration](../../cmd/yatm-cli/main.go) attaches one command to each public
service method, so every operation has a CLI spelling. [CLI coverage
tests](../../cmd/yatm-cli/coverage_test.go) hold the two together by resolving every
public RPC to one real parser leaf and rejecting a binding whose RPC no longer exists, so
a new public method without its command fails that check instead of leaving the surface
unreachable. The [development guide](../operations/testing.md) owns the commands that run
it.

The [bundled Skill](../../.agents/skills/yatm/SKILL.md) routes agent work to command domains
and defines operating rules. It ships inside the release package beside the
[operations guides](../operations/library.md) that own the workflows. Agents read the
matching installed `--help` down to the selected command for its inventory, arguments and
flags, and interpret its JSON output as the operational source of truth. The Skill's
domain table does not duplicate the installed command inventory; this document owns the
surface rules.

Connection settings are global flags or their `YATM_*` environment variables:
`--server`, `--timeout` (`0` removes the deadline), `--basic-user` and
`--basic-password-file`. Registered Location paths, Volume mount points and Tape
devices are paths on the server; `--input`, `--output` and `--basic-password-file` are
paths on the machine running the agent.

Unary replies print protobuf JSON, and streaming operations print JSON Lines with one
item result per line and a final summary. A Files listing is the one stream with no
summary: it writes one message per batch as the server produces it, so a reader sees the
first rows before the read finishes. Exit status distinguishes intent from
execution: `0` succeeded, `1` failed at runtime, and `2` rejected the command as a
usage or safety error. An executed stream that settles with failed, unprocessed or
publication-pending items exits nonzero even when the request itself completed.
A completed `--dryrun` plan succeeds even though its proposed entries remain
`UNPROCESSED`; planning failures and pending Library publication still exit nonzero.

A read that returns a complete directory takes no page size and no cursor; those flags
belong to the query form of the same command, which returns one bounded page and the
cursor that continues it. Passing either for a complete read is a usage error rather
than a silently ignored argument.

Long work belongs to a Job, and a waiting command always has a timeout. Physical
decisions such as choosing a device, loading Media, formatting, or continuing writes stay
explicit rather than implicit in another command. There is no file or Preview download
command; full archived content is obtained through Restore. The CLI has no command for an
operation the API does not expose.

Instant fields in JSON use decimal strings and names ending in `_ns`, following the
[shared time contract](contracts.md#time-values). Preserve them as signed 64-bit integers when
processing output. Restore `--before` accepts a timestamp with its stated timezone and retains
its nanosecond precision; dates outside the supported range are usage errors.

`job wait` succeeds for a durable `COMPLETED` Job whose phase is `COMPLETED` or
`UNSPECIFIED`, including after a service restart. It keeps waiting while an attached runner
reports `QUEUED` for an admitted attempt waiting for shared Media resources, or a finalizing
phase. `FAILED` is terminal: no Job kind supports Job-level retry. An idle `READY` Job
reports `UNSPECIFIED` and requires an explicit Media choice. Only an Archive or Restore
Media operation failure or operator cancellation, including while `QUEUED`, returns
the Job to pre-Media `READY`, retaining its prepared manifest and error or cancel reason
for another explicit Media operation. Initial preparation and Scan failure or cancellation
remain terminal. Waiting returns nonzero with `action_required` for `FAILED` or idle
`READY`, including the persisted error when present.

`archive creation ID`, `restore creation ID` and `scan creation ID` call their kind's
read-only `GetCreation` method once and print its typed response. `request` contains
the retained creation inputs; a nonempty `unavailable_reason` explains missing original
inputs that require reselection. Reading does not create a Job or change the old Job.
Review the selections and options, resolve missing inputs, and explicitly use the normal
Create flow with its current validation and pre-submit checks to submit new work.

`identical groups` and `identical members` include a disposable `result_id` in their first
reply. Later pages require `--result ID` with the returned cursor; an unavailable ID
fails and requires a new Find instead of silently repeating the complete search.
`identical find` returns the same kind of ID
and both row counts for `identical rows --result ID --offset N`; `identical positions`
locates File IDs within both hidden-file projections, and `identical close` releases the
result. An idle result expires after 30 minutes and does not survive a service restart;
run Find again if it is unavailable. Row pages default to 100 and accept at most 200
rows; group and member CLI pages default to 20. `identical rows` and `identical positions`
accept the same `--sort-key file-id|name|size` and `--order asc|desc`. The default
is File ID ascending; name starts ascending and size starts descending when selected.
Sorting changes member order within each existing group, not group order, and position
lookups use the requested order. These reads never scan or hash physical files.

`job log` reads a raw bounded byte page and returns JSON with text in `logs`
and the next byte offset as a decimal string in `offset`. Follow that returned offset, not
the text's character count; an empty `logs` ends the read. This CLI representation does not
Base64-encode the log, although the underlying RPC uses a protobuf bytes field. JSON text can
replace invalid UTF-8, including a character split by a byte-page boundary; preserve the original
Job bundle's `job.log` when exact bytes are required.
`job log-lines` reads a line page from the tail or a
returned byte cursor, toward older or newer content, with optional exact `--level` and
case-insensitive `--query` filters. The server owns page limits; the CLI supplies no limit.
An empty filtered page can still advance its cursor, so use the returned `has_older` and
`has_newer` flags to continue.
Continuation fragments can appear as context when their line start lies outside the
bounded read, even if that fragment does not establish a filter match.

## Directory paths

`ls` accepts one optional positional directory operand. A bare `ls` selects the Library
root. `/Photos/2026`, `Photos/2026`, `./Photos/2026` and `library:///Photos/2026` all
select the same logical directory relative to that root. The CLI has no persistent
working directory, never uses the client's `PWD` to resolve the operand, and never falls
back from a failed lookup to filesystem paths. Positional path syntax applies only to `ls`; `du`,
`mv`, `mkdir`, `rm` and other commands retain their existing arguments and flags.

`location://NAS/DCIM` selects `DCIM` relative to the registered root of the Location
whose full, case-sensitive display name is exactly `NAS`. Both `location://NAS` and
`location://NAS/` select that Location's root. `--server` still selects the YATM server;
the URI's Location name is not a network host. Missing or duplicate display names fail
resolution. Use `--location-id ID --path PATH` to select an ambiguous name by ID.
Lookup enumerates existing registrations in bounded pages and compares names exactly,
including Unicode names, without a fuzzy text filter, aliases or a display-name uniqueness
constraint.

The existing ID selectors remain available: `--file-id` selects a Library directory,
and `--location-id` with optional `--path` selects a registered Location directory.
A positional operand is mutually exclusive with either ID selector and with a nonempty
`--path`. More than one directory operand is a usage error.

Plain paths are literal and are not percent-decoded; a colon alone does not select a URI,
so `library:2026` names an ordinary Library directory. URI components are percent-decoded
once; encode literal `%`, `?` and `#` as `%25`, `%3F` and `%23`. Quote operands containing
spaces for the shell. A percent-encoded slash in a Location name is rejected. After URI
decoding, or directly for a plain path, `.` and `..` segments are normalized within the
selected root; traversal above that root is rejected.

Library path resolution uses existing API reads and compares each candidate directory
name by exact bytes; a fuzzy search match alone never establishes identity. Resolution
and the final List or Search share one command context and the global `--timeout` deadline. No API,
entity or storage changes are required for path lookup.

Ordinary `ls` still reads a complete directory, without `--limit` or `--cursor`.
`--query` selects a paged Search within the resolved directory. `--recursive` also
selects a paged Search over a Library subtree, even without a query expression. Both
search forms accept `--limit` and `--cursor`.
An unreadable child remains in List output with `error` and no actionable reference. The command
drains the remaining rows, then returns the existing failure exit code with `code: incomplete`;
directory or transport failures retain their own errors. Supported filenames are legal UTF-8,
including literal backslashes, whitespace and control characters. Non-UTF-8 names are escaped for
display only and are not usable path operands. Quote shell operands so the shell preserves their bytes.
The [Library guide](../operations/library.md#browse-from-the-cli) shows usage.

## Reports and mutations

The [shared rule](contracts.md#synchronous-operation-reports) owns which commands report
under `--dryrun`, what a report contains and which operations are excluded. This surface
adds only where the CLI differs: `archive write tape format` keeps `--confirm-format`,
because confirming the Tape barcode authorizes a write to a device rather than a dry-run decision,
and a report needs no follow-up command — the
same command without `--dryrun` performs the resolved work.

Tape FORMAT first inspects the device and Library registration. A successful inspection with
an empty electronic barcode accepts the user-supplied `--barcode`, normalized to six uppercase
alphanumeric characters; a nonempty inspected barcode must match it. `--confirm-format` must
exactly match that resolved barcode. Probe errors and registered Media stop before the write.
APPEND retains its requirement for a matching inspected identity and compatible registered Media.

## Skill

The CLI surface and its bundled [Skill](../../.agents/skills/yatm/SKILL.md) change
together: a command, flag, output or safety rule it describes must update the Skill in the
same commit. The Skill supplies operational guidance alongside the matching installed
CLI help and JSON output. Installed copies refresh through the
[optional Skill step](../operations/install.md#optional-agent-skill).
