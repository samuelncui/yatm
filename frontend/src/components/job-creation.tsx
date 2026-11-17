import { t } from "@lingui/core/macro";
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
      if (!/^[1-9]\d*$/.test(id)) throw new Error(t`Invalid Job ID.`);
      const value = await load(BigInt(id), controller.signal);
      if (!controller.signal.aborted) setResult({ id, value });
    })().catch((failure) => {
      if (!controller.signal.aborted) setError(errorMessage(failure, t`Could not load Job creation inputs`));
    });
    return () => controller.abort();
  }, [id, load, attempt]);
  if (result?.id === id) return children(result.value);
  return (
    <Stack spacing={2} sx={{ p: 2 }}>
      {error ? (
        <Feedback severity="error" action={<Button onClick={() => setAttempt((value) => value + 1)}>{t`Retry`}</Button>}>
          {error}
        </Feedback>
      ) : (
        <Box role="status">{t`Loading Job creation inputs…`}</Box>
      )}
      <Button component={Link} to={cancelTo}>
        {t`Cancel`}
      </Button>
    </Stack>
  );
}

const locationLabel = (id: bigint) => {
  const locationID = String(id);
  return t`Location ${locationID}`;
};

// A recorded input names itself until its live entry resolves.
const creationLabel = (versionID: bigint | undefined, target: FileSelection["target"] | undefined) => {
  if (versionID !== undefined) {
    const version = String(versionID);
    return t`Saved version ${version}`;
  }
  if (target?.oneofKind === "library") {
    const item = String(target.library.fileId);
    return t`Library item ${item}`;
  }
  if (target?.oneofKind === "location") {
    const locationID = String(target.location.locationId);
    const path = target.location.path;
    return t`Location ${locationID}/${path}`;
  }
  return t`Unavailable selection`;
};

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
        const label = creationLabel(versionID, target);
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
            if (!version) throw new Error(t`This saved version is unavailable.`);
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
          if (!reference) throw new Error(t`The original selection has no available reference.`);
          const { detail } = await filesCli.get({ reference }, { abort: signal }).response;
          signal.throwIfAborted();
          const file = detail?.entry;
          if (file?.error) throw new Error(file.error);
          if (!file) throw new Error(t`This selected entry is unavailable.`);
          entry.name = file.name || label;
          entry.path =
            target?.oneofKind === "location"
              ? `${detail?.original?.sourceName || locationLabel(target.location.locationId)}/${target.location.path}`
              : detail?.organization?.path || file.path || entry.name;
          entry.target = detail?.organization?.path;
          entry.isDir = file.kind === EntryKind.DIRECTORY;
          entry.fileID = filesEntryLibraryID(file)?.toString() ?? entry.fileID;
        } catch (failure) {
          signal.throwIfAborted();
          const reason = errorMessage(failure, t`Could not resolve this selection`);
          entry.unavailableReason = t`${reason} Remove it or select it again.`;
        }
        return entry;
      }),
    );
    signal.throwIfAborted();
    entries.push(...page);
  }
  return entries;
}
