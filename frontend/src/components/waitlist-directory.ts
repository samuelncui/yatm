import { useCallback, useEffect, useRef, useState } from "react";
import { fileCatalogCli } from "@/api";
import { FileOperationRef, FileScope, FileSelection, InspectSelectionRequest, type RestoreVersionResolution } from "@/entity";
import { filesPage } from "@/components/files-browser";
import { associatedLibraryFileID, selectionForFile } from "@/components/location-files";
import type { SelectionEntry } from "@/components/selection-waitlist";
import { errorMessage } from "@/tools";

function directoryReference(selection: FileSelection): FileOperationRef {
  const target = selection.target;
  switch (target.oneofKind) {
    case "library":
      return FileOperationRef.create({ target: { oneofKind: "fileId", fileId: target.library.fileId } });
    case "location":
      return FileOperationRef.create({
        target: {
          oneofKind: "location",
          location: {
            locationId: target.location.locationId,
            path: target.location.path,
            bindingToken: target.location.reference?.bindingToken ?? "",
          },
        },
      });
    default:
      throw new Error("This selection has no directory reference.");
  }
}

export function useWaitlistDirectory(directory: SelectionEntry | undefined, restore: boolean, cutoff?: bigint) {
  const [page, setPage] = useState<{ key: string; entries: SelectionEntry[]; cursor: string }>({ key: "", entries: [], cursor: "" });
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const sequence = useRef(0);
  const pending = useRef(false);
  const selection = directory?.selection;
  const key = selection ? FileSelection.toJsonString(selection) : "";
  const path = directory?.path ?? "";
  const load = useCallback(
    async (cursor = "") => {
      if (!key) return;
      const request = ++sequence.current;
      pending.current = true;
      setLoading(true);
      setError("");
      try {
        const selection = FileSelection.fromJsonString(key);
        const result = await filesPage(directoryReference(selection), selection.scope, cursor);
        if (request !== sequence.current) return;
        const entries = result.files.map((file): SelectionEntry => {
          const child = selectionForFile(file, result.scope);
          return {
            key: FileSelection.toJsonString(child),
            name: file.name,
            path: [path, file.name].filter(Boolean).join("/"),
            isDir: file.isDir,
            selection: child,
            fileID: file.isDir ? undefined : associatedLibraryFileID(file),
            size: file.size,
          };
        });
        setPage((current) => ({ key, entries: cursor && current.key === key ? [...current.entries, ...entries] : entries, cursor: result.nextCursor }));
      } catch (error) {
        if (request !== sequence.current) return;
        setPage({ key, entries: [], cursor: "" });
        setError(errorMessage(error, "Could not load selected folder"));
      } finally {
        if (request === sequence.current) {
          pending.current = false;
          setLoading(false);
        }
      }
    },
    [key, path],
  );
  useEffect(() => {
    const requests = sequence;
    const inFlight = pending;
    void load();
    return () => {
      requests.current++;
      inFlight.current = false;
    };
  }, [load]);
  const visible = page.key === key ? page.entries : [];
  const [versions, setVersions] = useState<{ key: string; resolutions: RestoreVersionResolution[] }>({ key: "", resolutions: [] });
  const versionKey = restore && key ? JSON.stringify([visible.filter((entry) => entry.fileID).map((entry) => entry.fileID), cutoff?.toString()]) : "";
  const [versionError, setVersionError] = useState("");
  const [versionAttempt, setVersionAttempt] = useState(0);
  useEffect(() => {
    if (!versionKey) return;
    let active = true;
    setVersionError("");
    const [ids, before] = JSON.parse(versionKey) as [string[], string | null];
    void (async () => {
      const resolutions: RestoreVersionResolution[] = [];
      for (let offset = 0; offset < ids.length; offset += 100) {
        const result = await fileCatalogCli.inspectSelection(
          InspectSelectionRequest.create({
            restore: true,
            selections: ids
              .slice(offset, offset + 100)
              .map((id) => FileSelection.create({ target: { oneofKind: "library", library: { fileId: BigInt(id) } }, scope: FileScope.ALL })),
            versionPolicy: { beforeAtMs: before == null ? undefined : BigInt(before) },
          }),
        ).response;
        if (!active) return;
        resolutions.push(...result.resolvedVersions);
      }
      if (active) setVersions({ key: versionKey, resolutions });
    })().catch((error) => {
      if (active) setVersionError(errorMessage(error, "Could not resolve saved versions"));
    });
    return () => {
      active = false;
    };
  }, [versionKey, versionAttempt]);
  return {
    entries: visible,
    loading: !!key && (loading || page.key !== key),
    error: key ? error || versionError : "",
    resolutions: versions.key === versionKey ? versions.resolutions : [],
    loadMore: () => {
      if (page.key === key && page.cursor && !pending.current && !error) void load(page.cursor);
    },
    retry: () => {
      if (error) void load();
      else setVersionAttempt((value) => value + 1);
    },
  };
}
