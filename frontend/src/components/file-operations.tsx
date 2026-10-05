import { useCallback, useEffect, useRef, useState } from "react";
import { toast, type Id } from "react-toastify";
import type { FileData } from "@samuelncui/chonky";
import { filesCli } from "@/api";
import { FileOperationKind, FileOperationOutcome, FileOperationRef, FileOperationSpec, type FileOperationSummary, type KeepIdenticalRequest } from "@/entity";
import { fileLocationReference } from "@/components/location-files";
import { allowsFileOperation } from "@/components/files-browser";
import { errorMessage } from "@/tools";

export function fileOperationReference(file: FileData): FileOperationRef {
  if (FileOperationRef.is(file.operationReference)) return FileOperationRef.clone(file.operationReference);
  if (file.physicalLocationID) return FileOperationRef.create({ target: { oneofKind: "location", location: fileLocationReference(file) } });
  if (!/^-?\d+$/.test(file.id)) throw new Error("File reference is missing. Refresh this folder.");
  return FileOperationRef.create({ target: { oneofKind: "fileId", fileId: BigInt(file.id) } });
}

function sameScope(left: FileOperationRef, right: FileOperationRef): boolean {
  if (left.target.oneofKind === "fileId") return right.target.oneofKind === "fileId";
  if (left.target.oneofKind !== "location" || right.target.oneofKind !== "location") return false;
  return left.target.location.locationId === right.target.location.locationId;
}

type Clipboard = { kind: FileOperationKind; files: FileData[] };
export function canPasteFiles(clipboard: Clipboard | undefined, destination: FileData | null | undefined): boolean {
  if (clipboard?.kind !== FileOperationKind.MOVE || !clipboard.files.length || !destination?.isDir) return false;
  if (!allowsFileOperation(destination, FileOperationKind.MKDIR)) return false;
  try {
    const target = fileOperationReference(destination);
    return clipboard.files.every((file) => allowsFileOperation(file, FileOperationKind.MOVE) && sameScope(fileOperationReference(file), target));
  } catch {
    return false;
  }
}

const operationLabels: Partial<Record<FileOperationKind, string>> = {
  [FileOperationKind.MOVE]: "Moving",
  [FileOperationKind.REMOVE]: "Deleting",
  [FileOperationKind.MKDIR]: "Creating folder",
};

/** Library and disk organization share request, feedback and refresh behavior. */
export const useFileOperations = (onComplete: () => Promise<void>) => {
  const [clipboard, setClipboard] = useState<Clipboard>();
  const active = useRef<{ controller: AbortController; notification?: Id } | undefined>(undefined);
  const mounted = useRef(true);
  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
      active.current?.controller.abort();
      if (active.current?.notification !== undefined) toast.dismiss(active.current.notification);
    };
  }, []);

  const execute = useCallback(
    async (
      kind: FileOperationKind,
      createCall: (signal: AbortSignal) => Pick<ReturnType<typeof filesCli.remove>, "responses"> & PromiseLike<unknown>,
      rename = false,
    ) => {
      if (active.current) throw new Error("Another file operation is in progress");
      const operation = { controller: new AbortController(), notification: undefined as Id | undefined };
      active.current = operation;
      // A submitted Rename settles one primitive; only batch work exposes cancellation.
      const timer = window.setTimeout(() => {
        if (!mounted.current || active.current !== operation) return;
        operation.notification = toast.info(
          <span>
            {rename ? "Renaming" : (operationLabels[kind] ?? "Working")}…{!rename && <button onClick={() => operation.controller.abort()}>Cancel</button>}
          </span>,
          { autoClose: false, closeButton: false, closeOnClick: false, draggable: false },
        );
      }, 500);
      let summary: FileOperationSummary | undefined;
      let entryError = "";
      let failure: Error | undefined;
      try {
        const call = createCall(operation.controller.signal);
        for await (const update of call.responses) {
          const result = update.result;
          if (!result) throw new Error("File operation result is missing");
          if (result.entry && result.entry.outcome !== FileOperationOutcome.SUCCEEDED && result.entry.error && !entryError) {
            entryError = `${result.entry.sourcePath || result.entry.targetPath}: ${result.entry.error}`;
          }
          if (result.summary) summary = result.summary;
        }
        await call;
        if (operation.controller.signal.aborted) throw new Error("Operation canceled");
        if (!summary?.completed) throw new Error("Operation did not complete");
        const issues = [
          summary.failedCount > 0n ? `${summary.failedCount} failed` : "",
          summary.publicationPendingCount > 0n ? `${summary.publicationPendingCount} Library updates incomplete` : "",
          summary.unprocessedCount > 0n ? `${summary.unprocessedCount} not processed` : "",
        ].filter(Boolean);
        if (issues.length) throw new Error([issues.join(" · "), entryError].filter(Boolean).join(": "));
      } catch (error) {
        failure = new Error(operation.controller.signal.aborted ? "Operation canceled" : errorMessage(error, "File operation failed"));
      } finally {
        window.clearTimeout(timer);
        if (operation.notification !== undefined) toast.dismiss(operation.notification);
        // A failed or canceled batch may still have completed entries.
        if (mounted.current) {
          try {
            await onComplete();
          } catch (error) {
            const message = errorMessage(error, "unknown refresh error");
            if (failure) failure = new Error(`${failure.message}. Refresh failed: ${message}`);
            else toast.error(`Operation succeeded, but refresh failed: ${message}`);
          }
        }
        active.current = undefined;
      }
      if (failure) throw failure;
    },
    [onComplete],
  );

  const start = useCallback(
    async (spec: FileOperationSpec) => {
      if (active.current) throw new Error("Another file operation is in progress");
      if (![FileOperationKind.MOVE, FileOperationKind.MKDIR, FileOperationKind.REMOVE].includes(spec.kind))
        throw new Error("This file operation is not supported");
      const scope = spec.destination ?? spec.sources[0];
      if (!scope) throw new Error("Choose files or a destination first");
      if (spec.sources.some((source) => !sameScope(source, scope))) throw new Error("Choose files and a destination in the same source");
      await execute(
        spec.kind,
        (signal) =>
          spec.kind === FileOperationKind.MKDIR
            ? filesCli.mkdir({ destination: spec.destination, name: spec.name, dryrun: false }, { abort: signal })
            : spec.kind === FileOperationKind.MOVE
              ? filesCli.move({ sources: spec.sources, destination: spec.destination, name: spec.name, dryrun: false }, { abort: signal })
              : filesCli.remove({ sources: spec.sources, dryrun: false }, { abort: signal }),
        spec.kind === FileOperationKind.MOVE && spec.sources.length === 1 && !!spec.name,
      );
    },
    [execute],
  );
  const startKeep = useCallback(
    (request: KeepIdenticalRequest) => execute(FileOperationKind.REMOVE, (signal) => filesCli.keepIdentical(request, { abort: signal })),
    [execute],
  );

  const paste = useCallback(
    async (destination: FileOperationRef) => {
      if (!clipboard?.files.length) throw new Error("Cut files first");
      await start(FileOperationSpec.create({ kind: clipboard.kind, sources: clipboard.files.map(fileOperationReference), destination }));
      if (clipboard.kind === FileOperationKind.MOVE) setClipboard(undefined);
    },
    [clipboard, start],
  );
  return { clipboard, setClipboard, start, startKeep, paste };
};

export type FileOperations = ReturnType<typeof useFileOperations>;
