import { FileBrowser } from "@/components/file-browser";
import { Feedback } from "@/components/feedback";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Box, Button, FormControlLabel, Grid, MenuItem, Switch, TextField, Typography } from "@mui/material";
import { useNavigate, useSearchParams } from "react-router";
import { toast } from "react-toastify";
import { RpcError } from "@protobuf-ts/runtime-rpc";
import {
  ChonkyActions,
  ChonkyIconName,
  FileContextMenu,
  FileList,
  FileNavbar,
  FileToolbar,
  defineFileAction,
  type FileData,
  type FileBrowserHandle,
  type GenericFileActionHandler,
  type FileAction,
} from "@samuelncui/chonky";
import { filesCli } from "@/api";
import {
  FileOperationKind,
  FileOperationOutcome,
  FileScope,
  IdenticalScope,
  IdenticalSortKey,
  IdenticalSortOrder,
  IdenticalSource,
  SettingsGroup,
  type IdenticalGroup,
  type IdenticalMember,
  type IdenticalRoot,
  type IdenticalPosition,
  type IdenticalRow,
  type FileOperationSummary,
  type FileOperationEntry,
} from "@/entity";
import { allowsFileOperation, filesEntryData } from "@/components/files-browser";
import { useCommittedSettings } from "@/components/settings-editor";
import { fileOperationReference } from "@/components/file-operations";
import { revealLocationFileURL } from "@/components/original-location-link";
import { useIdenticalSparse, type IdenticalResult, type ProjectionIntervals } from "@/components/identical-sparse";
import { IdenticalScopePicker } from "@/components/identical-scope";
import { ListPlaceholder } from "@/components/list-placeholder";
import { scanSelectionEntry } from "@/components/scan-selection";
import { FileMetadataDialog } from "@/components/file-metadata-dialog";
import { useActionDialog } from "@/components/action-dialog";
import { FileInspector } from "./file-detail";
import { ArchiveLibraryAction, EditFileMetadataAction, ScanFilesAction, ViewFileDetailsAction, LocateInOtherPaneAction } from "@/actions";
import { associatedLibraryFileID, selectionForFile } from "@/components/location-files";
import { contentHex } from "@/components/content-status";
import { chonkyI18n, errorMessage, formatFilesize, runUIAction } from "@/tools";

const keepID = "identical-keep";
const mergeID = "identical-merge";
const sortNameID = "identical-sort-name";
const sortSizeID = "identical-sort-size";
const removeID = ChonkyActions.DeleteFiles.id;
type IdenticalSort = { key: IdenticalSortKey; descending: boolean };
const initialSort: IdenticalSort = { key: IdenticalSortKey.FILE_ID, descending: false };
const staleGroup = (error: unknown) => error instanceof RpcError && error.code === "ABORTED";

function memberFile(member: IdenticalMember): FileData {
  const entry = member.entry!;
  const matches = new Map<string, string[]>();
  for (const item of member.evidence) {
    const signature = contentHex(item.signature);
    matches.set(signature, [...(matches.get(signature) ?? []), item.versionId ? `version ${item.versionId}` : "current"]);
  }
  const evidence = [...matches].map(([signature, sources]) => `${signature.slice(0, 14)}… (${sources.join(", ")})`).join(" · ");
  return {
    ...filesEntryData(entry),
    details: [
      [member.locationName, entry.path, entry.sizeBytes === undefined ? "" : formatFilesize(entry.sizeBytes)].filter(Boolean).join(" · "),
      `Matching examples: ${evidence}`,
    ],
    draggable: false,
    droppable: false,
  };
}

export const IdenticalFiles = () => {
  const [params] = useSearchParams();
  const navigate = useNavigate();
  const [source, setSource] = useState(params.get("source") === "locations" ? IdenticalSource.LOCATIONS : IdenticalSource.LIBRARY);
  const [roots, setRoots] = useState<IdenticalRoot[]>(() =>
    /^[1-9]\d*$/.test(params.get("location") ?? "") ? [{ locationId: BigInt(params.get("location")!) }] : [],
  );
  const [result, setResult] = useState<IdenticalResult>();
  const [includeHidden, setIncludeHidden] = useState(false);
  const [sort, setSort] = useState<IdenticalSort>(initialSort);
  const [removed, setRemoved] = useState<Map<string, IdenticalPosition>>(new Map());
  const [suppressedIds, setSuppressedIds] = useState<Set<string>>(new Set());
  const [removedGroups, setRemovedGroups] = useState<Set<string>>(new Set());
  const [removedGroupIntervals, setRemovedGroupIntervals] = useState<Map<string, ProjectionIntervals>>(new Map());
  const [changedGroups, setChangedGroups] = useState<Set<string>>(new Set());
  const [itemStatus, setItemStatus] = useState<Map<string, { message: string; revision: number }>>(new Map());
  const [keepStatus, setKeepStatus] = useState<Map<string, string>>(new Map());
  const [unprocessedGroups, setUnprocessedGroups] = useState<Map<string, { survivor: string; revision: number }>>(new Map());
  const [busyGroups, setBusyGroups] = useState<Map<string, number>>(new Map());
  const [mergeBusy, setMergeBusy] = useState(false);
  const [switchingHidden, setSwitchingHidden] = useState(false);
  const [switchingSort, setSwitchingSort] = useState(false);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const [selected, setSelected] = useState<FileData>();
  const [metadata, setMetadata] = useState<FileData[]>([]);
  const { settings: librarySettings, error: settingsError } = useCommittedSettings(SettingsGroup.LIBRARY);
  const confirmRemove = librarySettings?.confirmRemove ?? true;
  const [revision, setRevision] = useState(0);
  const browser = useRef<FileBrowserHandle>(null);
  const generation = useRef(0);
  const statusRevision = useRef(0);
  const resultRef = useRef<IdenticalResult | undefined>(undefined);
  const changedGroupRef = useRef(new Set<string>());
  const unavailableRef = useRef(false);
  const hiddenRequest = useRef(0);
  const sortRef = useRef(sort);
  const sorting = useRef(false);
  const movedIds = useRef(new Set<bigint>());
  const resultSource = useRef<IdenticalSource | undefined>(undefined);
  const reconciliation = useRef<{ result: IdenticalResult; ids: Set<bigint>; running: boolean } | undefined>(undefined);
  const mergeActive = useRef(0);
  const activeStreams = useRef(new Map<string, number>());
  const deferredClose = useRef(new Map<string, IdenticalResult>());
  const inFlightFiles = useRef(new Set<string>());
  const busyGroupRef = useRef(new Map<string, number>());
  const removeSlots = useRef(0);
  const removeWaiters = useRef<Array<() => void>>([]);
  const scope = useMemo(() => IdenticalScope.create({ source, roots: source === IdenticalSource.LOCATIONS ? roots : [] }), [source, roots]);
  const scopeKey =
    source === IdenticalSource.LIBRARY
      ? "library"
      : "locations:" +
        roots
          .map((root) => String(root.locationId))
          .sort()
          .join(",");
  const { ask, dialog } = useActionDialog();
  const busyGroupIds = useMemo(
    () => new Set([...busyGroups].flatMap(([key, count]) => (count > 0 && key.startsWith(scopeKey + ":") ? [key.slice(scopeKey.length + 1)] : []))),
    [busyGroups, scopeKey],
  );
  const asFile = useCallback(
    (row: IdenticalRow) => {
      if (!row.member?.entry) return undefined;
      const file = memberFile(row.member);
      const id = row.member.entry.associatedFileId;
      const groupKey = result?.id + ":" + row.group?.id;
      const unprocessedSurvivor = unprocessedGroups.get(groupKey);
      const removeStatus = itemStatus.get(file.id);
      const keepDetail = source === IdenticalSource.LOCATIONS && id !== undefined ? keepStatus.get(String(id)) : undefined;
      const keepFallback =
        source === IdenticalSource.LOCATIONS &&
        unprocessedSurvivor !== undefined &&
        unprocessedSurvivor.survivor !== String(id) &&
        unprocessedSurvivor.revision > (removeStatus?.revision ?? -1)
          ? "Not processed"
          : undefined;
      const status = removeStatus?.message === "Deleting…" ? removeStatus.message : (keepDetail ?? keepFallback ?? removeStatus?.message);
      return status ? { ...file, details: [...(file.details ?? []), status] } : file;
    },
    [itemStatus, keepStatus, result?.id, source, unprocessedGroups],
  );
  const groupActionIds = useMemo(() => (source === IdenticalSource.LIBRARY ? [mergeID] : [keepID, removeID]), [source]);
  const sparse = useIdenticalSparse(
    result,
    includeHidden,
    sort.key,
    sort.descending,
    removed,
    suppressedIds,
    removedGroups,
    removedGroupIntervals,
    changedGroups,
    busyGroupIds,
    asFile,
    groupActionIds,
    source === IdenticalSource.LIBRARY ? "Related content" : "Matching content",
  );
  const { fileRows, invalidate } = sparse;
  useEffect(() => {
    unavailableRef.current = sparse.unavailable;
  }, [sparse.unavailable]);

  useEffect(() => {
    if (settingsError) toast.error(settingsError);
  }, [settingsError]);
  const closeResult = useCallback((old?: IdenticalResult) => {
    if (!old) return;
    if (activeStreams.current.has(old.id)) {
      deferredClose.current.set(old.id, old);
      return;
    }
    void filesCli.closeIdenticalResult({ resultId: old.id }).response.catch(() => undefined);
  }, []);
  const retainResult = useCallback(
    (snapshot?: IdenticalResult) => {
      if (!snapshot) return () => undefined;
      activeStreams.current.set(snapshot.id, (activeStreams.current.get(snapshot.id) ?? 0) + 1);
      return () => {
        const count = (activeStreams.current.get(snapshot.id) ?? 1) - 1;
        if (count) activeStreams.current.set(snapshot.id, count);
        else {
          activeStreams.current.delete(snapshot.id);
          const old = deferredClose.current.get(snapshot.id);
          deferredClose.current.delete(snapshot.id);
          closeResult(old);
        }
      };
    },
    [closeResult],
  );
  const clear = useCallback(() => {
    generation.current++;
    hiddenRequest.current++;
    reconciliation.current = undefined;
    closeResult(resultRef.current);
    resultRef.current = undefined;
    resultSource.current = undefined;
    setResult(undefined);
    setRemoved(new Map());
    setSuppressedIds(new Set());
    setRemovedGroups(new Set());
    setRemovedGroupIntervals(new Map());
    changedGroupRef.current = new Set();
    setChangedGroups(new Set());
    setItemStatus(new Map());
    setKeepStatus(new Map());
    setUnprocessedGroups(new Map());
    setSelected(undefined);
    setError("");
    setLoading(false);
    setSwitchingHidden(false);
    sorting.current = false;
    setSwitchingSort(false);
    browser.current?.setFileSelection(new Set());
  }, [closeResult]);
  const groupBusy = useCallback(
    (snapshot: IdenticalResult | undefined, groupId: string, change: number) => {
      if (!snapshot) return;
      const key = scopeKey + ":" + groupId;
      const count = (busyGroupRef.current.get(key) ?? 0) + change;
      if (count > 0) busyGroupRef.current.set(key, count);
      else busyGroupRef.current.delete(key);
      setBusyGroups(new Map(busyGroupRef.current));
    },
    [scopeKey],
  );
  const acquireRemoveSlot = useCallback(async () => {
    if (removeSlots.current < 4) removeSlots.current++;
    else await new Promise<void>((resolve) => removeWaiters.current.push(resolve));
    return () => {
      const next = removeWaiters.current.shift();
      if (next) next();
      else removeSlots.current--;
    };
  }, []);
  useEffect(() => {
    clear();
    return clear;
  }, [clear, scope]);
  const markChanged = useCallback((ids: string[]) => {
    if (!ids.length) return;
    changedGroupRef.current = new Set([...changedGroupRef.current, ...ids]);
    setChangedGroups(new Set(changedGroupRef.current));
  }, []);
  const applyPositions = useCallback(
    (positions: IdenticalPosition[]) => {
      if (!positions.length) return;
      setRemoved((current) => {
        const next = new Map(current);
        for (const position of positions) next.set(String(position.fileId), position);
        return next;
      });
      markChanged(positions.map((position) => position.groupId));
    },
    [markChanged],
  );
  const lookupRemoved = useCallback(
    async (ids: bigint[], current?: IdenticalResult) => {
      if (!current) return;
      for (let offset = 0; offset < ids.length; offset += 100) {
        const batch = ids.slice(offset, offset + 100);
        const reply = await filesCli.lookupIdenticalPositions({
          resultId: current.id,
          fileIds: batch,
          sortKey: sortRef.current.key,
          sortOrder: sortRef.current.descending ? IdenticalSortOrder.DESC : IdenticalSortOrder.ASC,
        }).response;
        const found = new Set(reply.positions.map((position) => String(position.fileId)));
        if (batch.some((id) => !found.has(String(id)))) throw new Error("Could not locate updated files in this result. Find again.");
        if (resultRef.current?.id === current.id) applyPositions(reply.positions);
      }
    },
    [applyPositions],
  );

  const enqueueReconciliation = useCallback(
    (current: IdenticalResult, ids: bigint[]) => {
      if (!ids.length) return;
      let queue = reconciliation.current;
      if (!queue || queue.result.id !== current.id) {
        queue = { result: current, ids: new Set<bigint>(), running: false };
        reconciliation.current = queue;
      }
      for (const id of ids) queue.ids.add(id);
      if (queue.running) return;
      queue.running = true;
      void (async () => {
        try {
          while (reconciliation.current === queue && queue.ids.size) {
            const batch = [...queue.ids].slice(0, 100);
            for (const id of batch) queue.ids.delete(id);
            const reply = await filesCli.lookupIdenticalPositions({
              resultId: current.id,
              fileIds: batch,
              sortKey: sortRef.current.key,
              sortOrder: sortRef.current.descending ? IdenticalSortOrder.DESC : IdenticalSortOrder.ASC,
            }).response;
            // A new Find may already reflect the physical operation, so missing IDs are expected.
            if (resultRef.current?.id === current.id) applyPositions(reply.positions);
          }
        } catch (failure) {
          if (resultRef.current?.id === current.id) {
            invalidate();
            toast.error(errorMessage(failure, "Could not update identical rows") + ". Find again.");
          }
        } finally {
          queue.running = false;
        }
      })();
    },
    [applyPositions, invalidate],
  );
  const noteMoved = useCallback(
    (id: bigint, operationResult: IdenticalResult) => {
      movedIds.current.add(id);
      const current = resultRef.current;
      if (!current || resultSource.current !== IdenticalSource.LOCATIONS) return;
      setSuppressedIds((ids) => new Set(ids).add(String(id)));
      if (current.id !== operationResult.id) enqueueReconciliation(current, [id]);
    },
    [enqueueReconciliation],
  );
  const search = useCallback(async () => {
    if (mergeActive.current) {
      setError("Wait for Merge to finish before finding again.");
      return;
    }
    clear();
    // The new Find supersedes earlier moves; track only moves reported from this point.
    movedIds.current.clear();
    if (scope.source === IdenticalSource.LOCATIONS && !scope.roots.length) return;
    const request = generation.current;
    setLoading(true);
    try {
      const reply = await filesCli.findIdentical({ scope }).response;
      if (request !== generation.current) {
        closeResult({ id: reply.resultId, allCount: 0, visibleCount: 0 });
        return;
      }
      const found = { id: reply.resultId, allCount: Number(reply.allRowCount), visibleCount: Number(reply.visibleRowCount) };
      resultRef.current = found;
      resultSource.current = scope.source;
      if (scope.source === IdenticalSource.LOCATIONS) {
        setSuppressedIds(new Set([...movedIds.current].map(String)));
        enqueueReconciliation(found, [...movedIds.current]);
      }
      setResult(found);
    } catch (failure) {
      if (request === generation.current) setError(errorMessage(failure, "Could not find identical files"));
    } finally {
      if (request === generation.current) setLoading(false);
    }
  }, [clear, closeResult, enqueueReconciliation, scope]);

  const switchHidden = useCallback(
    async (next: boolean) => {
      if (switchingHidden || sorting.current) return;
      const request = ++hiddenRequest.current;
      const snapshotId = resultRef.current?.id;
      setSwitchingHidden(true);
      try {
        const pins = await sparse.prepareProjection(next, sort.key, sort.descending);
        if (request !== hiddenRequest.current || resultRef.current?.id !== snapshotId) return;
        if (snapshotId) sparse.useProjection(pins);
        setSelected((current) => (current && !pins.has(current.id) ? [...pins.values()].at(-1)?.file : current));
        setIncludeHidden(next);
      } catch (failure) {
        if (request !== hiddenRequest.current || resultRef.current?.id !== snapshotId) return;
        if (snapshotId) sparse.invalidate();
        toast.error(errorMessage(failure, "Could not switch hidden files") + " Find again.");
      } finally {
        if (request === hiddenRequest.current) setSwitchingHidden(false);
      }
    },
    [sort, sparse, switchingHidden],
  );

  const switchSort = useCallback(
    async (key: IdenticalSortKey) => {
      const snapshot = resultRef.current;
      if (!snapshot) return;
      if (sorting.current || switchingHidden || mergeActive.current || busyGroupRef.current.size || reconciliation.current?.running) {
        toast.error("Wait for the current operation to finish before sorting.");
        return;
      }
      const next: IdenticalSort = { key, descending: sort.key === key ? !sort.descending : key === IdenticalSortKey.SIZE };
      const request = ++hiddenRequest.current;
      sorting.current = true;
      setSwitchingSort(true);
      try {
        const positions = new Map<string, IdenticalPosition>();
        const ids = [...removed.keys()].map(BigInt);
        for (let offset = 0; offset < ids.length; offset += 100) {
          const batch = ids.slice(offset, offset + 100);
          const reply = await filesCli.lookupIdenticalPositions({
            resultId: snapshot.id,
            fileIds: batch,
            sortKey: next.key,
            sortOrder: next.descending ? IdenticalSortOrder.DESC : IdenticalSortOrder.ASC,
          }).response;
          for (const position of reply.positions) positions.set(String(position.fileId), position);
        }
        if (positions.size !== ids.length) throw new Error("Could not locate updated files in this result. Find again.");
        const pins = await sparse.prepareProjection(includeHidden, next.key, next.descending);
        if (request !== hiddenRequest.current || resultRef.current?.id !== snapshot.id) return;
        sparse.useProjection(pins);
        setSelected((current) => (current && !pins.has(current.id) ? [...pins.values()].at(-1)?.file : current));
        setRemoved(positions);
        sortRef.current = next;
        setSort(next);
      } catch (failure) {
        if (request !== hiddenRequest.current || resultRef.current?.id !== snapshot.id) return;
        if (failure instanceof RpcError && failure.code === "FAILED_PRECONDITION") sparse.invalidate();
        toast.error(errorMessage(failure, "Could not sort identical files"));
      } finally {
        if (request === hiddenRequest.current) {
          sorting.current = false;
          setSwitchingSort(false);
        }
      }
    },
    [includeHidden, removed, sort, sparse, switchingHidden],
  );

  const stream = useCallback(
    async (
      call: ReturnType<typeof filesCli.remove> | ReturnType<typeof filesCli.keepIdentical>,
      onEntry: (entry: FileOperationEntry) => void,
      onSummary?: (summary: FileOperationSummary) => void,
    ) => {
      let summary: FileOperationSummary | undefined;
      const failures: string[] = [];
      let streamError: unknown;
      try {
        for await (const update of call.responses) {
          const entry = update.result?.entry;
          if (entry) onEntry(entry);
          if (entry?.outcome === FileOperationOutcome.FAILED && entry.error) failures.push(entry.sourcePath + ": " + entry.error);
          if (update.result?.summary) {
            summary = update.result.summary;
            onSummary?.(summary);
          }
        }
        await call;
      } catch (failure) {
        streamError = failure;
        failures.push(errorMessage(failure, "Operation canceled"));
      }
      if (staleGroup(streamError)) throw streamError;
      if (!summary?.completed) failures.push("Operation ended without a final result");
      if (summary?.failedCount) failures.push(String(summary.failedCount) + " failed");
      if (summary?.unprocessedCount) failures.push(String(summary.unprocessedCount) + " not processed");
      if (summary?.publicationPendingCount) failures.push(String(summary.publicationPendingCount) + " Library updates incomplete");
      if (failures.length) throw new Error([...new Set(failures)].join(" · "));
    },
    [],
  );

  const removeFiles = useCallback(
    async (targets: FileData[]) => {
      if (sorting.current) throw new Error("Wait for sorting to finish before deleting files.");
      const snapshot = resultRef.current;
      if (!snapshot) return;
      const tasks: Promise<void>[] = [];
      for (const file of new Map(targets.map((item) => [item.id, item])).values()) {
        if (inFlightFiles.current.has(file.id)) continue;
        inFlightFiles.current.add(file.id);
        const row = fileRows.get(file.id);
        const groupId = row?.group?.id ?? "";
        const entry = row?.member?.entry;
        const selectedId = entry?.associatedFileId ?? (entry?.reference?.target.oneofKind === "fileId" ? entry.reference.target.fileId : undefined);
        const releaseResult = retainResult(snapshot);
        groupBusy(snapshot, groupId, 1);
        if (resultRef.current?.id === snapshot.id) {
          if (selectedId !== undefined)
            setKeepStatus((current) => {
              const next = new Map(current);
              next.delete(String(selectedId));
              return next;
            });
          const revision = ++statusRevision.current;
          setItemStatus((current) => new Map(current).set(file.id, { message: "Deleting…", revision }));
        }
        tasks.push(
          (async () => {
            const releaseSlot = await acquireRemoveSlot();
            const lookups: Promise<unknown>[] = [];
            let moved = false;
            let publicationPending = false;
            let failure: unknown;
            let positionError = "";
            try {
              await stream(filesCli.remove({ sources: [fileOperationReference(file)], dryrun: false }), (outcome) => {
                if (outcome.outcome !== FileOperationOutcome.SUCCEEDED && outcome.outcome !== FileOperationOutcome.PUBLICATION_PENDING) return;
                moved = true;
                publicationPending ||= outcome.outcome === FileOperationOutcome.PUBLICATION_PENDING;
                const id = outcome.fileId ?? selectedId;
                if (id !== undefined) noteMoved(id, snapshot);
                if (id === undefined || !row?.group) {
                  positionError = "Could not identify the removed row";
                  return;
                }
                if (resultRef.current?.id === snapshot.id) {
                  applyPositions([
                    {
                      fileId: id,
                      groupId: row.group.id,
                      allPosition: includeHidden ? row.position : -1n,
                      visiblePosition: includeHidden ? undefined : row.position,
                      allGroupHeaderPosition: includeHidden ? row.groupHeaderPosition : -1n,
                      visibleGroupHeaderPosition: includeHidden ? undefined : row.groupHeaderPosition,
                    },
                  ]);
                }
                lookups.push(lookupRemoved([id], snapshot).catch((error: unknown) => error));
              });
            } catch (error) {
              failure = error;
            } finally {
              releaseSlot();
            }
            const positionFailure = (await Promise.all(lookups)).find((error) => error !== undefined);
            if (positionFailure) {
              positionError = errorMessage(positionFailure, "Could not update both row views");
            }
            if (positionError && moved && resultRef.current?.id === snapshot.id) invalidate();
            if (resultRef.current?.id === snapshot.id) {
              if (selectedId !== undefined)
                setKeepStatus((current) => {
                  const next = new Map(current);
                  next.delete(String(selectedId));
                  return next;
                });
              const revision = ++statusRevision.current;
              setItemStatus((current) =>
                new Map(current).set(file.id, {
                  message: moved
                    ? publicationPending
                      ? "Moved to Trash; Library update incomplete"
                      : "Moved to Trash"
                    : "Delete failed: " + errorMessage(failure, "No result"),
                  revision,
                }),
              );
              setRevision((value) => value + 1);
            } else if (moved) toast.success("Moved " + file.name + " to Trash");
            if (publicationPending) toast.error(file.name + ": moved to Trash; Library update incomplete");
            if (positionError && moved && resultRef.current?.id === snapshot.id) toast.error(file.name + ": " + positionError + ". Find again.");
            if (failure) throw failure;
          })().finally(() => {
            inFlightFiles.current.delete(file.id);
            groupBusy(snapshot, groupId, -1);
            releaseResult();
          }),
        );
      }
      const settled = await Promise.allSettled(tasks);
      const failures = settled.flatMap((item) => (item.status === "rejected" ? [errorMessage(item.reason, "Delete failed")] : []));
      if (failures.length) throw new Error(failures.join("; "));
    },
    [acquireRemoveSlot, applyPositions, fileRows, groupBusy, includeHidden, invalidate, lookupRemoved, noteMoved, retainResult, stream],
  );

  const keep = useCallback(
    async (group: IdenticalGroup, survivor: FileData) => {
      if (sorting.current) throw new Error("Wait for sorting to finish before keeping a file.");
      const snapshot = resultRef.current;
      if (!snapshot || unavailableRef.current || changedGroupRef.current.has(group.id)) throw new Error("This group changed. Find again.");
      if (busyGroupRef.current.has(scopeKey + ":" + group.id)) throw new Error("A file operation is already in progress for this group.");
      const findGeneration = generation.current;
      const release = retainResult(snapshot);
      groupBusy(snapshot, group.id, 1);
      const ids: bigint[] = [];
      const lookups: Promise<void>[] = [];
      let lookupFailure: unknown;
      let summary: FileOperationSummary | undefined;
      let summaryRevision = 0;
      const flush = () => {
        if (ids.length)
          lookups.push(
            lookupRemoved(ids.splice(0), snapshot).catch((error: unknown) => {
              lookupFailure ??= error;
            }),
          );
      };
      let failure: unknown;
      try {
        await stream(
          filesCli.keepIdentical({
            scope,
            groupId: group.id,
            fingerprint: group.fingerprint,
            keep: fileOperationReference(survivor),
            dryrun: false,
          }),
          (entry) => {
            if (entry.fileId === undefined) return;
            if (entry.outcome === FileOperationOutcome.FAILED || entry.outcome === FileOperationOutcome.UNPROCESSED) {
              const message = entry.outcome === FileOperationOutcome.FAILED ? "Keep failed: " + (entry.error || "Unknown error") : "Not processed";
              if (findGeneration === generation.current && resultRef.current?.id === snapshot.id)
                setKeepStatus((current) => new Map(current).set(String(entry.fileId), message));
            }
            if (entry.outcome === FileOperationOutcome.SUCCEEDED || entry.outcome === FileOperationOutcome.PUBLICATION_PENDING) {
              noteMoved(entry.fileId, snapshot);
              ids.push(entry.fileId);
              if (ids.length >= 100) flush();
            }
          },
          (next) => {
            summary = next;
            summaryRevision = ++statusRevision.current;
          },
        );
      } catch (error) {
        failure = error;
      }
      flush();
      await Promise.all(lookups);
      if (summary && (!summary.completed || summary.unprocessedCount > 0n) && findGeneration === generation.current && resultRef.current?.id === snapshot.id) {
        setUnprocessedGroups((current) =>
          new Map(current).set(snapshot.id + ":" + group.id, { survivor: String(survivor.libraryFileID ?? ""), revision: summaryRevision }),
        );
      }
      if (lookupFailure && resultRef.current?.id === snapshot.id) {
        invalidate();
        failure = failure ?? lookupFailure;
      }
      if (staleGroup(failure) && resultRef.current?.id === snapshot.id) markChanged([group.id]);
      if (resultRef.current?.id === snapshot?.id) setRevision((value) => value + 1);
      else if (!failure) toast.success("Keep completed");
      groupBusy(snapshot, group.id, -1);
      release();
      if (failure) throw failure;
    },
    [groupBusy, invalidate, lookupRemoved, markChanged, noteMoved, retainResult, scope, scopeKey, stream],
  );

  const actions = useMemo(() => {
    const sortAvailable = !switchingHidden && !switchingSort && !mergeBusy && busyGroupIds.size === 0;
    const single = (state: { group?: { id: string }; selectedFiles: FileData[]; selectedFilesForAction: FileData[] }) =>
      !!state.group &&
      !switchingSort &&
      !sparse.unavailable &&
      !changedGroups.has(state.group.id) &&
      !busyGroupIds.has(state.group.id) &&
      state.selectedFiles.length === 1 &&
      state.selectedFilesForAction.length === 1
        ? 2
        : 1;
    return [
      ...(result && !sparse.unavailable
        ? [
            defineFileAction({
              id: sortNameID,
              button: { name: `Sort by name${sort.key === IdenticalSortKey.NAME ? (sort.descending ? " ↓" : " ↑") : ""}`, toolbar: true, group: "Options" },
              customVisibility: () => (sortAvailable ? 2 : 1),
            }),
            defineFileAction({
              id: sortSizeID,
              button: { name: `Sort by size${sort.key === IdenticalSortKey.SIZE ? (sort.descending ? " ↓" : " ↑") : ""}`, toolbar: true, group: "Options" },
              customVisibility: () => (sortAvailable ? 2 : 1),
            }),
          ]
        : []),
      defineFileAction({
        id: keepID,
        requiresSelection: true,
        fileFilter: (file) => !!file && allowsFileOperation(file, FileOperationKind.REMOVE) && itemStatus.get(file.id)?.message !== "Deleting…",
        button: { name: "Keep this", contextMenu: true, icon: ChonkyIconName.toggleOn },
        customVisibility: (state) => (source === IdenticalSource.LOCATIONS ? single(state) : 0),
      }),
      defineFileAction({
        id: mergeID,
        requiresSelection: true,
        button: { name: "Merge into", contextMenu: true, icon: ChonkyIconName.copy },
        customVisibility: (state) => (source === IdenticalSource.LIBRARY ? single(state) : 0),
      }),
      defineFileAction({
        ...ChonkyActions.DeleteFiles,
        id: removeID,
        requiresSelection: true,
        button: { name: "Delete", contextMenu: true, icon: ChonkyIconName.trash },
        fileFilter: (file) => !!file && allowsFileOperation(file, FileOperationKind.REMOVE) && itemStatus.get(file.id)?.message !== "Deleting…",
        customVisibility: (state) => (source !== IdenticalSource.LOCATIONS ? 0 : sparse.unavailable || switchingSort ? 1 : state.group ? 2 : 1),
      }),
      ViewFileDetailsAction,
      LocateInOtherPaneAction,
      { ...EditFileMetadataAction, fileFilter: (file: FileData | null) => allowsFileOperation(file, FileOperationKind.UPDATE_METADATA) },
      { ...ArchiveLibraryAction, fileFilter: (file: FileData | null) => allowsFileOperation(file, FileOperationKind.ARCHIVE) },
      { ...ScanFilesAction, fileFilter: (file: FileData | null) => allowsFileOperation(file, FileOperationKind.SCAN) },
    ];
  }, [busyGroupIds, changedGroups, itemStatus, mergeBusy, result, sort, source, sparse.unavailable, switchingHidden, switchingSort]);

  const onFileAction: GenericFileActionHandler<FileAction> = (data) => {
    const selectedFiles: FileData[] = data.state.selectedFilesForAction;
    if (sparse.unavailable && data.id !== ChonkyActions.ChangeSelection.id) return;
    if (data.id === sortNameID || data.id === sortSizeID) {
      void switchSort(data.id === sortNameID ? IdenticalSortKey.NAME : IdenticalSortKey.SIZE);
      return;
    }
    if (data.id === ChonkyActions.ChangeSelection.id) {
      sparse.pinSelection(data.state.selectedFiles);
      setSelected(data.state.selectedFiles.at(-1));
      return;
    }
    if (data.id === ChonkyActions.MouseClickFile.id || data.id === ChonkyActions.OpenFiles.id) {
      setSelected(data.id === ChonkyActions.MouseClickFile.id ? data.payload.file : (data.payload.targetFile ?? data.payload.files[0]));
      return;
    }
    if (data.id === ViewFileDetailsAction.id) {
      setSelected(selectedFiles.at(-1));
      return;
    }
    if (data.id === EditFileMetadataAction.id) {
      setMetadata(selectedFiles);
      return;
    }
    if (data.id === LocateInOtherPaneAction.id && selectedFiles[0]) {
      const reference = fileOperationReference(selectedFiles[0]).target;
      navigate(
        reference.oneofKind === "fileId"
          ? "/file?file=" + reference.fileId
          : reference.oneofKind === "location"
            ? revealLocationFileURL(reference.location.locationId, reference.location.path)
            : "/file",
      );
      return;
    }
    if (data.id === ArchiveLibraryAction.id) {
      navigate("/archive", {
        state: {
          selections: selectedFiles.map((file) => ({
            selection: selectionForFile(file, FileScope.ALL),
            name: file.name,
            path: file.name,
            fileID: associatedLibraryFileID(file),
            isDir: file.isDir,
          })),
        },
      });
      return;
    }
    if (data.id === ScanFilesAction.id) {
      navigate("/scan", {
        state: { scan: { target: { kind: "files", entries: selectedFiles.map((file) => scanSelectionEntry(file, FileScope.ALL, "Identical files")) } } },
      });
      return;
    }
    const groupId = data.state.group?.id ?? sparse.fileRows.get(selectedFiles[0]?.id)?.group?.id;
    const row = groupId ? sparse.groupRows.get(groupId) : undefined;
    const group = row?.group;
    if (!group || !selectedFiles.length) return;
    if (
      data.id === mergeID &&
      source === IdenticalSource.LIBRARY &&
      !changedGroups.has(group.id) &&
      !busyGroupRef.current.has(scopeKey + ":" + group.id) &&
      selectedFiles.length === 1
    ) {
      const target = selectedFiles[0];
      const reference = fileOperationReference(target).target;
      if (reference.oneofKind !== "fileId" || data.state.selectedFiles.length !== 1) return;
      const snapshot = resultRef.current;
      ask({
        title: "Merge into " + target.name + "?",
        confirmLabel: "Merge",
        children: <p>Merge all {String(group.memberCount)} Library files and their saved versions into this file. Original disk files are kept.</p>,
        onConfirm: async () => {
          if (sorting.current) throw new Error("Wait for sorting to finish before merging files.");
          if (!snapshot || resultRef.current?.id !== snapshot.id || unavailableRef.current || changedGroupRef.current.has(group.id))
            throw new Error("The result changed. Find again.");
          if (busyGroupRef.current.has(scopeKey + ":" + group.id)) throw new Error("A file operation is already in progress for this group.");
          const release = retainResult(snapshot);
          groupBusy(snapshot, group.id, 1);
          mergeActive.current++;
          setMergeBusy(true);
          try {
            const positions = await filesCli.lookupIdenticalPositions({
              resultId: snapshot.id,
              fileIds: [reference.fileId],
              sortKey: sortRef.current.key,
              sortOrder: sortRef.current.descending ? IdenticalSortOrder.DESC : IdenticalSortOrder.ASC,
            }).response;
            const targetPosition = positions.positions[0];
            if (!targetPosition) throw new Error("Could not locate this group. Find again.");
            const otherHeader = includeHidden ? targetPosition.visibleGroupHeaderPosition : targetPosition.allGroupHeaderPosition;
            let otherInterval: { start: number; end: number } | undefined;
            if (otherHeader !== undefined) {
              const other = await filesCli.listIdenticalRows({
                resultId: snapshot.id,
                offset: otherHeader,
                limit: 1,
                includeHidden: !includeHidden,
                sortKey: sortRef.current.key,
                sortOrder: sortRef.current.descending ? IdenticalSortOrder.DESC : IdenticalSortOrder.ASC,
              }).response;
              const count = other.rows[0]?.displayMemberCount;
              if (count === undefined) throw new Error("Could not read the other hidden-file view. Find again.");
              otherInterval = { start: Number(otherHeader), end: Number(otherHeader + count) };
            }
            if (resultRef.current?.id !== snapshot.id) throw new Error("The result changed. Find again.");
            try {
              await filesCli.mergeIdentical({ scope, groupId: group.id, fingerprint: group.fingerprint, targetFileId: reference.fileId, dryrun: false })
                .response;
            } catch (failure) {
              if (staleGroup(failure) && resultRef.current?.id === snapshot.id) markChanged([group.id]);
              throw failure;
            }
            if (resultRef.current?.id !== snapshot.id) {
              toast.success("Merged into " + target.name);
              return;
            }
            const start = Number(row.groupHeaderPosition);
            const spans: ProjectionIntervals = includeHidden
              ? { all: { start, end: start + Number(row.displayMemberCount) }, visible: otherInterval }
              : { visible: { start, end: start + Number(row.displayMemberCount) }, all: otherInterval };
            setRemovedGroupIntervals((current) => new Map(current).set(group.id, spans));
            setRemovedGroups((current) => new Set(current).add(group.id));
            setRevision((value) => value + 1);
            toast.success("Merged into " + target.name);
          } catch (failure) {
            if (failure instanceof RpcError && failure.code === "FAILED_PRECONDITION" && resultRef.current?.id === snapshot.id) sparse.invalidate();
            throw failure;
          } finally {
            mergeActive.current--;
            setMergeBusy(mergeActive.current > 0);
            groupBusy(snapshot, group.id, -1);
            release();
          }
        },
      });
      return;
    }
    if (source !== IdenticalSource.LOCATIONS || ![keepID, removeID].includes(data.id)) return;
    if (
      data.id === keepID &&
      (changedGroups.has(group.id) ||
        busyGroupRef.current.has(scopeKey + ":" + group.id) ||
        selectedFiles.length !== 1 ||
        data.state.selectedFiles.length !== 1)
    )
      return;
    const run = () => (data.id === keepID ? keep(group, selectedFiles[0]) : removeFiles(selectedFiles));
    if (!confirmRemove) {
      runUIAction(run, data.id === keepID ? "Keep failed" : "Delete failed");
      return;
    }
    ask({
      title: "Delete files?",
      confirmLabel: "Delete",
      danger: true,
      children: (
        <>
          <p>
            {data.id === keepID
              ? "Keep " + selectedFiles[0].name + " and delete all other " + String(group.memberCount - 1n) + " members, including members not loaded here."
              : "Delete " + selectedFiles.length + (selectedFiles.length === 1 ? " selected file." : " selected files.")}
          </p>
          <p>Files move into each Location’s .trash folder. Library records and saved versions are kept.</p>
        </>
      ),
      onConfirm: () => {
        void runUIAction(run, data.id === keepID ? "Keep failed" : "Delete failed");
      },
    });
  };

  return (
    <Box className="browser-box library-file-browser">
      <Box sx={{ display: "flex", alignItems: "center", gap: 1, flexWrap: "wrap" }}>
        <Button variant="contained" disabled={loading || mergeBusy || (source === IdenticalSource.LOCATIONS && !roots.length)} onClick={() => void search()}>
          Find
        </Button>
        <TextField
          select
          size="small"
          label="Source"
          value={source}
          onChange={(event) => setSource(Number(event.target.value) === IdenticalSource.LOCATIONS ? IdenticalSource.LOCATIONS : IdenticalSource.LIBRARY)}
          sx={{ minWidth: 150 }}
        >
          <MenuItem value={IdenticalSource.LIBRARY}>Library</MenuItem>
          <MenuItem value={IdenticalSource.LOCATIONS}>Locations</MenuItem>
        </TextField>
        {source === IdenticalSource.LOCATIONS && <IdenticalScopePicker value={roots} onChange={setRoots} />}
        <FormControlLabel
          control={<Switch checked={includeHidden} disabled={switchingHidden} onChange={(_, checked) => void switchHidden(checked)} />}
          label="Show hidden files"
        />
      </Box>
      <Typography variant="body2" color="text.secondary" sx={{ textAlign: "left", px: 1.5, pb: error || sparse.unavailable || sparse.errors.size > 0 ? 0 : 1 }}>
        {source === IdenticalSource.LIBRARY
          ? "Files connected through matching current content or saved versions. Members may have different current content."
          : "Matching recorded content in the selected Locations. Run Scan separately to update content information."}
      </Typography>
      {error && (
        <Feedback severity="error" action={<Button onClick={() => void search()}>Retry</Button>}>
          {error}
        </Feedback>
      )}
      {sparse.unavailable && (
        <Feedback severity="warning" action={<Button onClick={() => void search()}>Find again</Button>}>
          This identical result needs a new Find to show accurate rows.
        </Feedback>
      )}
      {!sparse.unavailable && sparse.errors.size > 0 && (
        <Feedback severity="warning" action={<Button onClick={sparse.retry}>Retry rows</Button>}>
          Could not load some rows.
        </Feedback>
      )}
      <Grid className="browser-container" container columnSpacing={1.5}>
        <Grid className="browser" size={7} component="section" aria-label="Identical file groups">
          <FileBrowser
            ref={browser}
            files={sparse.files}
            folderChain={[{ id: "identical", name: "Identical files", isDir: true, openable: false }]}
            disableDragAndDrop
            disableDefaultFileActions={[
              ChonkyActions.SortFilesByName.id,
              ChonkyActions.SortFilesBySize.id,
              ChonkyActions.SortFilesByDate.id,
              ChonkyActions.SelectAllFiles.id,
              ChonkyActions.ToggleHiddenFiles.id,
              ChonkyActions.ToggleShowFoldersFirst.id,
            ]}
            clearSelectionOnOutsideClick={false}
            i18n={chonkyI18n}
            fileActions={actions}
            onFileAction={onFileAction}
            defaultSortActionId={null}
            grouping={sparse.grouping}
          >
            <FileNavbar />
            <FileToolbar layout="inline" />
            <FileList
              emptyPlaceholder={
                loading ? (
                  <ListPlaceholder loading label="Reading…" />
                ) : source === IdenticalSource.LOCATIONS && !roots.length ? (
                  <ListPlaceholder label="Choose Locations to search" />
                ) : sparse.unavailable ? (
                  <ListPlaceholder label="Find again to refresh this result" />
                ) : result ? (
                  <ListPlaceholder label="No matching groups" />
                ) : (
                  <ListPlaceholder label="Find identical files to search" />
                )
              }
            />
            <FileContextMenu />
          </FileBrowser>
        </Grid>
        <Grid className="browser" size={5}>
          <FileInspector
            target={selected ? fileOperationReference(selected) : undefined}
            name={selected?.name}
            refreshKey={revision}
            onRefresh={async () => setRevision((value) => value + 1)}
          />
        </Grid>
      </Grid>
      <FileMetadataDialog files={metadata} open={metadata.length > 0} onClose={() => setMetadata([])} onSaved={async () => setRevision((value) => value + 1)} />
      {dialog}
    </Box>
  );
};
