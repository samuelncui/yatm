import { useEffect, useState, type ReactNode } from "react";
import { Link } from "react-router";
import { Box, Button, Stack } from "@mui/material";
import { filesCli } from "@/api";
import { EntryKind, FileSelection } from "@/entity";
import { errorMessage } from "@/tools";
import { Feedback } from "./feedback";
import { filesEntryLibraryID, libraryDirectoryReference, locationDirectoryReference } from "./files-browser";
import type { SelectionEntry } from "@/state/selections";

// Route consumers own these reads. Cards only link to a creation route.
export function JobCreationLoader<T>({
  id,
  load,
  cancelTo,
  children,
}: {
  id: string;
  load: (id: bigint, signal: AbortSignal) => Promise<T>;
  cancelTo: string;
  children: (value: T) => ReactNode;
}) {
  const [result, setResult] = useState<{ id: string; value: T }>();
  const [error, setError] = useState("");
  const [attempt, setAttempt] = useState(0);
  useEffect(() => {
    const controller = new AbortController();
    setResult(undefined);
    setError("");
    void (async () => {
      if (!/^[1-9]\d*$/.test(id)) throw new Error("Invalid Job ID.");
      const value = await load(BigInt(id), controller.signal);
      if (!controller.signal.aborted) setResult({ id, value });
    })().catch((failure) => {
      if (!controller.signal.aborted) setError(errorMessage(failure, "Could not load Job creation inputs"));
    });
    return () => controller.abort();
  }, [id, load, attempt]);
  if (result?.id === id) return children(result.value);
  return (
    <Stack spacing={2} sx={{ p: 2 }}>
      {error ? (
        <Feedback severity="error" action={<Button onClick={() => setAttempt((value) => value + 1)}>Retry</Button>}>
          {error}
        </Feedback>
      ) : (
        <Box role="status">Loading Job creation inputs…</Box>
      )}
      <Button component={Link} to={cancelTo}>
        Cancel
      </Button>
    </Stack>
  );
}

export async function readCreationEntries(selections: FileSelection[], versionIDs: bigint[], signal: AbortSignal): Promise<SelectionEntry[]> {
  const inputs = [
    ...selections.map((selection) => ({ selection, versionID: undefined })),
    ...versionIDs.map((versionID) => ({ selection: undefined, versionID })),
  ];
  const entries: SelectionEntry[] = [];
  // Only the recorded roots are resolved. No descendant enumeration or unbounded fan-out.
  for (let start = 0; start < inputs.length; start += 20) {
    signal.throwIfAborted();
    const page = await Promise.all(
      inputs.slice(start, start + 20).map(async ({ selection, versionID }) => {
        const target = selection?.target;
        const label =
          versionID !== undefined
            ? `Saved version ${versionID}`
            : target?.oneofKind === "library"
              ? `Library item ${target.library.fileId}`
              : target?.oneofKind === "location"
                ? `Location ${target.location.locationId}/${target.location.path}`
                : "Unavailable selection";
        const entry: SelectionEntry = {
          key: selection ? FileSelection.toJsonString(selection) : `version:${versionID}`,
          selection,
          versionID: versionID?.toString(),
          fileID: target?.oneofKind === "library" ? String(target.library.fileId) : undefined,
          name: label,
          path: label,
        };
        try {
          if (versionID !== undefined) {
            const { version } = await filesCli.getVersion({ id: versionID }, { abort: signal }).response;
            signal.throwIfAborted();
            if (!version || version.id !== versionID || version.fileId <= 0n) throw new Error("This saved version is unavailable.");
            entry.version = version;
            entry.versionID = undefined;
            entry.fileID = String(version.fileId);
          }
          const reference = entry.version
            ? libraryDirectoryReference(String(entry.version.fileId))
            : target?.oneofKind === "library"
              ? libraryDirectoryReference(String(target.library.fileId))
              : target?.oneofKind === "location"
                ? locationDirectoryReference(String(target.location.locationId), target.location.path)
                : undefined;
          if (!reference) throw new Error("The original selection has no available reference.");
          const { detail } = await filesCli.get({ reference }, { abort: signal }).response;
          signal.throwIfAborted();
          const file = detail?.entry;
          if (file?.error) throw new Error(file.error);
          const actual = file?.reference?.target;
          const expected = reference.target;
          const matches =
            expected.oneofKind === "fileId"
              ? actual?.oneofKind === "fileId" && actual.fileId === expected.fileId
              : expected.oneofKind === "location" &&
                actual?.oneofKind === "location" &&
                actual.location.locationId === expected.location.locationId &&
                actual.location.path === expected.location.path;
          if (!file || !matches) throw new Error("This selected entry is unavailable.");
          entry.name = file.name || label;
          entry.path =
            target?.oneofKind === "location"
              ? `${detail?.original?.sourceName || `Location ${target.location.locationId}`}/${target.location.path}`
              : detail?.organization?.path || file.path || entry.name;
          entry.target = detail?.organization?.path;
          entry.isDir = file.kind === EntryKind.DIRECTORY;
          entry.fileID = filesEntryLibraryID(file)?.toString() ?? entry.fileID;
        } catch (failure) {
          signal.throwIfAborted();
          entry.unavailableReason = `${errorMessage(failure, "Could not resolve this selection")} Remove it or select it again.`;
        }
        return entry;
      }),
    );
    signal.throwIfAborted();
    entries.push(...page);
  }
  return entries;
}
