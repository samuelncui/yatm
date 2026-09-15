import { useCallback, useEffect, useRef, useState } from "react";
import { toast, type Id } from "react-toastify";
import type { FileData } from "@samuelncui/chonky";
import { fileOperationCli } from "@/api";
import { FileOperationKind, FileOperationOutcome, FileOperationRef, FileOperationSpec, type FileOperationSummary } from "@/entity";
import { fileLocationReference } from "@/components/location-files";
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
const operationLabels: Partial<Record<FileOperationKind, string>> = {
  [FileOperationKind.MOVE]: "Moving",
  [FileOperationKind.DELETE]: "Deleting",
  [FileOperationKind.MAKE_DIRECTORY]: "Creating folder",
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

  const start = useCallback(
    async (spec: FileOperationSpec) => {
      if (active.current) throw new Error("Another file operation is in progress");
      if (![FileOperationKind.MOVE, FileOperationKind.MAKE_DIRECTORY, FileOperationKind.DELETE].includes(spec.kind))
        throw new Error("This file operation is not supported");
      const scope = spec.destination ?? spec.sources[0];
      if (!scope) throw new Error("Choose files or a destination first");
      if (spec.sources.some((source) => !sameScope(source, scope))) throw new Error("Choose files and a destination in the same source");

      const operation = { controller: new AbortController(), notification: undefined as Id | undefined };
      active.current = operation;
      // Ordinary edits do not insert page content. Only longer requests expose cancellation.
      const timer = window.setTimeout(() => {
        if (!mounted.current || active.current !== operation) return;
        operation.notification = toast.info(
          <span>
            {operationLabels[spec.kind] ?? "Working"}… <button onClick={() => operation.controller.abort()}>Cancel</button>
          </span>,
          { autoClose: false, closeButton: false, closeOnClick: false, draggable: false },
        );
      }, 500);
      let summary: FileOperationSummary | undefined;
      let entryError = "";
      let failure: Error | undefined;
      try {
        const confirmDelete = spec.kind === FileOperationKind.DELETE && scope.target.oneofKind === "location";
        const call = fileOperationCli.execute({ spec, confirmDelete }, { abort: operation.controller.signal });
        for await (const update of call.responses) {
          if (update.entry && update.entry.outcome !== FileOperationOutcome.SUCCEEDED && update.entry.error && !entryError) {
            entryError = `${update.entry.sourcePath || update.entry.targetPath}: ${update.entry.error}`;
          }
          if (update.summary) summary = update.summary;
        }
        await call;
        if (operation.controller.signal.aborted) throw new Error("Operation canceled");
        if (!summary?.completed) throw new Error("Operation did not complete");
        const issues = [
          summary.failed > 0n ? `${summary.failed} failed` : "",
          summary.publicationPending > 0n ? `${summary.publicationPending} Library updates incomplete` : "",
          summary.unprocessed > 0n ? `${summary.unprocessed} not processed` : "",
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

  const paste = useCallback(
    async (destination: FileOperationRef) => {
      if (!clipboard?.files.length) throw new Error("Cut files first");
      await start(FileOperationSpec.create({ kind: clipboard.kind, sources: clipboard.files.map(fileOperationReference), destination }));
      if (clipboard.kind === FileOperationKind.MOVE) setClipboard(undefined);
    },
    [clipboard, start],
  );
  return { clipboard, setClipboard, start, paste };
};

export type FileOperations = ReturnType<typeof useFileOperations>;
