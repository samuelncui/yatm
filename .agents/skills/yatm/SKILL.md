---
name: yatm
description: "Use yatm-cli to operate YATM: Library, live Location files, backup, restore and Scan Jobs."
---

# YATM

Use the `yatm-cli` executable through a shell. This Skill accompanies YATM v1 Alpha; use matching installed CLI help and JSON output as the operational source of truth. Catalog, Job bundle and Library backup formats have independent identities and revisions. Upgrade v0.1.x installations through the documented offline migration workflow before opening their data with v1.

## Connect

1. Run `yatm-cli --version` and `yatm-cli --help`, then run `yatm-cli <domain> --help` down to the relevant command.
2. Run `yatm-cli status` before other remote operations. Supply connection settings with global flags or the corresponding `YATM_*` environment variables.
3. Treat registered Location paths, Volume mount points, and Tape devices as paths on the YATM server.
4. Treat `--input`, `--output`, and `--basic-password-file` as paths on the machine running the agent.

## Operate

- Run read-only commands directly and interpret their JSON output.
- Run ordinary mutations when they are within the user's explicit request.
- For a Job status check, request one progress result or one bounded list page per poll. Continue only at a cadence appropriate for the operation.
- When the result reports a busy device, missing Media, or another required physical action, report the current state and the exact action needed from the user.
- Location pages are live filesystem observations with optional File IDs. Browsing and Open need no prior analysis; there is no file or Preview download command. Library metadata export remains available. Backup and Preview accept live Location or Library selections; preparation resolves identities and content. Restore selects versions and a confirmed authorized Location. The UI offers preferred Restore targets; CLI/API targets still use the same authorization checks, and preference is not permission. Review large selections before creating Jobs.
- Scan is one pipeline. `analyze`, `preview` and `verify` are CLI presets, not separate Job kinds. `scan create` selects a signature policy (`known-only`, `fill-missing`, `force-read`) and result policy (`report`, `originals`, `inventory`, `verify`). `known-only` reuses valid facts or caches without hashing on a cache miss. Verification always reads against the saved baseline. Preview policy is `none`, `missing-only` or `regenerate-all`; sequential Media do not support Preview.
- `analyze create` collects selected ranges in basic, incremental or force mode; successful ranges retain their results if another fails. Ordinary refresh creates no Job. `scan media` publishes inventory after complete validated observation, without Apply or an integrity guarantee. Read results with `scan results` and range failures with `scan scopes`.
- `fileops run` changes actual disk entries within one Location and completes within the request, without creating a Job. Read command help and resolve targets first; do not reinterpret a Library move as a physical operation. It emits JSON Lines item results and a final summary, with nonzero exit for partial failure or interruption. Use a suitable request timeout; disconnecting cancels pending work. Inspect results and current paths before a new attempt, never blindly repeat completed mutations. Permanent deletion needs explicit CLI confirmation even when browser confirmation is disabled.
- `verify create/run/entries` checks recorded Media content through actual reads in a Scan Job. Inspect typed findings; completion does not mean every copy is healthy.
- Restore links verified outputs according to the current binding and frozen version candidate, without displacing an existing original. Ignored outputs remain unlinked. Explicit `--allow-damaged-copies` permits complete damaged salvage, never a claim of verified restoration; do not enable it unless requested.
- `job wait` has a timeout and stops when intervention is needed; it does not choose Media, retry, format, or continue writes automatically.

Before File, Media, or Job deletion, Library Import, Library Trim, or Tape FORMAT:

1. Run the relevant read-only commands to resolve the current target and state.
2. Show the user the exact IDs, paths, flags, device, Media identity, or input file that will be affected.
3. Obtain explicit confirmation for that resolved operation.
4. Execute one confirmed mutation command.

For Tape FORMAT, run `yatm-cli media inspect tape` immediately before the write. Use the returned `identity` as both the requested barcode and the exact `--confirm-format` value. If the inspected Tape already has Library Media, report it and obtain separate authorization for any metadata deletion before formatting.

## Verify

After a mutation completes, run the corresponding read-only command and verify the resulting File, Media, Job, progress, Scan results, or service state. Report the command result and any remaining physical action.
