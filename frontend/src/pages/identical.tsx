import { t, plural } from "@lingui/core/macro";
import { FileBrowser, useFileListPreferences } from "@/components/file-browser";
import type { FileListPreferences } from "@/state/file-lists";
import { Feedback } from "@/components/feedback";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Box, Button, FormControlLabel, Grid, MenuItem, Switch, TextField, Typography } from "@mui/material";
import { useNavigate, useSearchParams } from "react-router";
import { toast } from "react-toastify";
import { RpcError } from "@protobuf-ts/runtime-rpc";
import {
  ChonkyActions,
  CustomVisibilityState,
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
import { rowFileID, useIdenticalSparse, type IdenticalResult, type ProjectionIntervals } from "@/components/identical-sparse";
import { IdenticalScopePicker } from "@/components/identical-scope";
import { ListPlaceholder } from "@/components/list-placeholder";
import { scanSelectionEntry } from "@/components/scan-selection";
import { FileMetadataDialog } from "@/components/file-metadata-dialog";
import { useActionDialog } from "@/components/action-dialog";
import { FileInspector } from "@/components/file-detail";
import { ArchiveLibraryAction, EditFileMetadataAction, ScanFilesAction, LocateInOtherPaneAction } from "@/actions";
import { associatedLibraryFileID, selectionForFile } from "@/components/location-files";
import { contentHex } from "@/components/content-status";
import { chonkyI18n, errorMessage, formatFilesize, runUIAction } from "@/tools";

const keepID = "identical-keep";
const mergeID = "identical-merge";
const sortSizeID = ChonkyActions.SortFilesBySize.id;
const removeID = ChonkyActions.DeleteFiles.id;
// The tool sorts groups by size only; the preference keeps its direction.
const sizeOrder = (descending: boolean) => ({
  sortKey: IdenticalSortKey.SIZE,
  sortOrder: descending ? IdenticalSortOrder.DESC : IdenticalSortOrder.ASC,
});
const initialPreferences: FileListPreferences = { sort: "size", descending: true, foldersFirst: false };
const deletingStatus = () => t`Deleting…`;
const staleGroup = (error: unknown) => error instanceof RpcError && error.code === "ABORTED";

function memberFile(member: IdenticalMember): FileData {
  const entry = member.entry!;
  const matches = new Map<string, string[]>();
  for (const item of member.evidence) {
    const signature = contentHex(item.signature);
    const versionId = String(item.versionId);
    matches.set(signature, [...(matches.get(signature) ?? []), item.versionId ? t`version ${versionId}` : t`current`]);
  }
  const evidence = [...matches].map(([signature, sources]) => `${signature.slice(0, 14)}… (${sources.join(", ")})`).join(" · ");
  return {
    ...filesEntryData(entry),
    details: [
      [member.locationName, entry.path, entry.sizeBytes === undefined ? "" : formatFilesize(entry.sizeBytes)].filter(Boolean).join(" · "),
      t`Matching examples: ${evidence}`,
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
  const [preferences, setPreferences] = useFileListPreferences("identical", initialPreferences);
  const descending = preferences.descending;
  const [removed, setRemoved] = useState<Map<string, IdenticalPosition>>(new Map());
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
  const { settings: generalSettings, error: settingsError } = useCommittedSettings(SettingsGroup.GENERAL);
  const confirmRemove = generalSettings?.confirmRemove ?? true;
  const [revision, setRevision] = useState(0);
  const browser = useRef<FileBrowserHandle>(null);
  const generation = useRef(0);
  const statusRevision = useRef(0);
  const resultRef = useRef<IdenticalResult | undefined>(undefined);
  const changedGroupRef = useRef(new Set<string>());
  const unavailableRef = useRef(false);
  const hiddenRequest = useRef(0);
  const descendingRef = useRef(descending);
  const sorting = useRef(false);
  const resultSource = useRef<IdenticalSource | undefined>(undefined);
  const mergeActive = useRef(0);
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
          ? t`Not processed`
          : undefined;
      const status = removeStatus?.message === deletingStatus() ? removeStatus.message : (keepDetail ?? keepFallback ?? removeStatus?.message);
      return status ? { ...file, details: [...(file.details ?? []), status] } : file;
    },
    [itemStatus, keepStatus, result?.id, source, unprocessedGroups],
  );
  const groupActionIds = useMemo(() => (source === IdenticalSource.LIBRARY ? [mergeID] : [keepID, removeID]), [source]);
  const sparse = useIdenticalSparse(
    result,
    includeHidden,
    IdenticalSortKey.SIZE,
    descending,
    removed,
    removedGroups,
    removedGroupIntervals,
    changedGroups,
    busyGroupIds,
    asFile,
    groupActionIds,
    source === IdenticalSource.LIBRARY ? t`Related content` : t`Matching content`,
  );
  const { fileRows, invalidate, listRows, lookupPositions } = sparse;
  useEffect(() => {
    unavailableRef.current = sparse.unavailable;
  }, [sparse.unavailable]);

  useEffect(() => {
    if (settingsError) toast.error(settingsError);
  }, [settingsError]);
  // A superseded result closes at once; an operation still running on it updates only its own rows.
  const closeResult = useCallback((resultId?: string) => {
    if (resultId) void filesCli.closeIdenticalResult({ resultId }).response.catch(() => undefined);
  }, []);
  const clear = useCallback(() => {
    generation.current++;
    hiddenRequest.current++;
    closeResult(resultRef.current?.id);
    resultRef.current = undefined;
    resultSource.current = undefined;
    setResult(undefined);
    setRemoved(new Map());
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
        const reply = await lookupPositions({
          resultId: current.id,
          fileIds: batch,
          ...sizeOrder(descendingRef.current),
        });
        const found = new Set(reply.positions.map((position) => String(position.fileId)));
        if (batch.some((id) => !found.has(String(id)))) throw new Error(t`Could not locate updated files in this result. Find again.`);
        if (resultRef.current?.id === current.id) applyPositions(reply.positions);
      }
    },
    [applyPositions, lookupPositions],
  );

  const search = useCallback(async () => {
    clear();
    if (scope.source === IdenticalSource.LOCATIONS && !scope.roots.length) return;
    const request = generation.current;
    setLoading(true);
    try {
      const reply = await filesCli.findIdentical({ scope }).response;
      if (request !== generation.current) {
        closeResult(reply.resultId);
        return;
      }
      const found = { id: reply.resultId, allCount: Number(reply.allRowCount), visibleCount: Number(reply.visibleRowCount) };
      resultRef.current = found;
      resultSource.current = scope.source;
      setResult(found);
    } catch (failure) {
      if (request === generation.current) setError(errorMessage(failure, t`Could not find identical files`));
    } finally {
      if (request === generation.current) setLoading(false);
    }
  }, [clear, closeResult, scope]);

  const switchHidden = useCallback(
    async (next: boolean) => {
      if (switchingHidden || sorting.current) return;
      const request = ++hiddenRequest.current;
      const snapshotId = resultRef.current?.id;
      setSwitchingHidden(true);
      try {
        const projection = await sparse.prepareProjection(next, IdenticalSortKey.SIZE, descending);
        const { pins } = projection;
        if (request !== hiddenRequest.current || resultRef.current?.id !== snapshotId) return;
        if (snapshotId) sparse.useProjection(projection);
        setSelected((current) => (current && !pins.has(current.id) ? [...pins.values()].at(-1)?.file : current));
        setIncludeHidden(next);
      } catch (failure) {
        if (request !== hiddenRequest.current || resultRef.current?.id !== snapshotId) return;
        toast.error(errorMessage(failure, t`Could not switch hidden files`));
      } finally {
        if (request === hiddenRequest.current) setSwitchingHidden(false);
      }
    },
    [descending, sparse, switchingHidden],
  );

  const switchSort = useCallback(async () => {
    const snapshot = resultRef.current;
    if (!snapshot) return;
    if (sorting.current || switchingHidden || mergeActive.current || busyGroupRef.current.size) {
      toast.error(t`Wait for the current operation to finish before sorting.`);
      return;
    }
    const next = !descending;
    const request = ++hiddenRequest.current;
    sorting.current = true;
    setSwitchingSort(true);
    try {
      const positions = new Map<string, IdenticalPosition>();
      const ids = [...removed.keys()].map(BigInt);
      for (let offset = 0; offset < ids.length; offset += 100) {
        const batch = ids.slice(offset, offset + 100);
        const reply = await lookupPositions({
          resultId: snapshot.id,
          fileIds: batch,
          ...sizeOrder(next),
        });
        for (const position of reply.positions) positions.set(String(position.fileId), position);
      }
      if (positions.size !== ids.length) throw new Error(t`Could not locate updated files in this result. Find again.`);
      const projection = await sparse.prepareProjection(includeHidden, IdenticalSortKey.SIZE, next, positions);
      const { pins } = projection;
      if (request !== hiddenRequest.current || resultRef.current?.id !== snapshot.id) return;
      sparse.useProjection(projection);
      setSelected((current) => (current && !pins.has(current.id) ? [...pins.values()].at(-1)?.file : current));
      setRemoved(positions);
      if (projection.removedGroups) setRemovedGroupIntervals(projection.removedGroups);
      descendingRef.current = next;
      setPreferences({ ...initialPreferences, descending: next });
    } catch (failure) {
      if (request !== hiddenRequest.current || resultRef.current?.id !== snapshot.id) return;
      toast.error(errorMessage(failure, t`Could not sort identical files`));
    } finally {
      if (request === hiddenRequest.current) {
        sorting.current = false;
        setSwitchingSort(false);
      }
    }
  }, [includeHidden, lookupPositions, removed, descending, sparse, switchingHidden, setPreferences]);

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
        failures.push(errorMessage(failure, t`Operation canceled`));
      }
      if (staleGroup(streamError)) throw streamError;
      if (!summary?.completed) failures.push(t`Operation ended without a final result`);
      if (summary?.failedCount) {
        const failedCount = String(summary.failedCount);
        failures.push(t`${failedCount} failed`);
      }
      if (summary?.unprocessedCount) {
        const unprocessedCount = String(summary.unprocessedCount);
        failures.push(t`${unprocessedCount} not processed`);
      }
      if (summary?.publicationPendingCount) {
        const pendingCount = String(summary.publicationPendingCount);
        failures.push(t`${pendingCount} Library updates incomplete`);
      }
      if (failures.length) throw new Error([...new Set(failures)].join(" · "));
    },
    [],
  );

  const removeFiles = useCallback(
    async (targets: FileData[]) => {
      if (sorting.current) throw new Error(t`Wait for sorting to finish before deleting files.`);
      const snapshot = resultRef.current;
      if (!snapshot) return;
      const tasks: Promise<void>[] = [];
      for (const file of new Map(targets.map((item) => [item.id, item])).values()) {
        if (inFlightFiles.current.has(file.id)) continue;
        inFlightFiles.current.add(file.id);
        const name = file.name;
        const row = fileRows.get(file.id);
        const groupId = row?.group?.id ?? "";
        const selectedId = rowFileID(row);
        groupBusy(snapshot, groupId, 1);
        if (resultRef.current?.id === snapshot.id) {
          if (selectedId !== undefined)
            setKeepStatus((current) => {
              const next = new Map(current);
              next.delete(String(selectedId));
              return next;
            });
          const revision = ++statusRevision.current;
          setItemStatus((current) => new Map(current).set(file.id, { message: deletingStatus(), revision }));
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
                if (id === undefined || !row?.group) {
                  positionError = t`Could not identify the removed row`;
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
              positionError = errorMessage(positionFailure, t`Could not update both row views`);
            }
            if (positionError && moved && resultRef.current?.id === snapshot.id) invalidate();
            if (resultRef.current?.id === snapshot.id) {
              const revision = ++statusRevision.current;
              const failureDetail = errorMessage(failure, t`No result`);
              setItemStatus((current) =>
                new Map(current).set(file.id, {
                  message: moved ? (publicationPending ? t`Moved to Trash; Library update incomplete` : t`Moved to Trash`) : t`Delete failed: ${failureDetail}`,
                  revision,
                }),
              );
              setRevision((value) => value + 1);
            } else if (moved) toast.success(t`Moved ${name} to Trash`);
            if (publicationPending) toast.error(t`${name}: moved to Trash; Library update incomplete`);
            if (positionError && moved && resultRef.current?.id === snapshot.id) toast.error(t`${name}: ${positionError}. Find again.`);
            if (failure) throw failure;
          })().finally(() => {
            inFlightFiles.current.delete(file.id);
            groupBusy(snapshot, groupId, -1);
          }),
        );
      }
      const settled = await Promise.allSettled(tasks);
      const failures = settled.flatMap((item) => (item.status === "rejected" ? [errorMessage(item.reason, t`Delete failed`)] : []));
      if (failures.length) throw new Error(failures.join("; "));
    },
    [acquireRemoveSlot, applyPositions, fileRows, groupBusy, includeHidden, invalidate, lookupRemoved, stream],
  );

  const keep = useCallback(
    async (group: IdenticalGroup, survivor: FileData) => {
      if (sorting.current) throw new Error(t`Wait for sorting to finish before keeping a file.`);
      const snapshot = resultRef.current;
      if (!snapshot || unavailableRef.current || changedGroupRef.current.has(group.id)) throw new Error(t`This group changed. Find again.`);
      if (busyGroupRef.current.has(scopeKey + ":" + group.id)) throw new Error(t`A file operation is already in progress for this group.`);
      const findGeneration = generation.current;
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
              const keepDetail = entry.error || t`Unknown error`;
              const message = entry.outcome === FileOperationOutcome.FAILED ? t`Keep failed: ${keepDetail}` : t`Not processed`;
              if (findGeneration === generation.current && resultRef.current?.id === snapshot.id)
                setKeepStatus((current) => new Map(current).set(String(entry.fileId), message));
            }
            if (entry.outcome === FileOperationOutcome.SUCCEEDED || entry.outcome === FileOperationOutcome.PUBLICATION_PENDING) {
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
      else if (!failure) toast.success(t`Keep completed`);
      groupBusy(snapshot, group.id, -1);
      if (failure) throw failure;
    },
    [groupBusy, invalidate, lookupRemoved, markChanged, scope, scopeKey, stream],
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
              ...ChonkyActions.SortFilesBySize,
              // Sparse rows are ordered by the server, including unloaded members.
              sortKeySelector: undefined,
              button: { ...ChonkyActions.SortFilesBySize.button, icon: descending ? ChonkyIconName.sortDesc : ChonkyIconName.sortAsc },
              customVisibility: () => (sortAvailable ? CustomVisibilityState.Active : CustomVisibilityState.Disabled),
            }),
          ]
        : []),
      defineFileAction({
        id: keepID,
        requiresSelection: true,
        fileFilter: (file) => !!file && allowsFileOperation(file, FileOperationKind.REMOVE) && itemStatus.get(file.id)?.message !== deletingStatus(),
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
        fileFilter: (file) => !!file && allowsFileOperation(file, FileOperationKind.REMOVE) && itemStatus.get(file.id)?.message !== deletingStatus(),
        customVisibility: (state) => (source !== IdenticalSource.LOCATIONS ? 0 : sparse.unavailable || switchingSort ? 1 : state.group ? 2 : 1),
      }),
      LocateInOtherPaneAction,
      { ...EditFileMetadataAction, fileFilter: (file: FileData | null) => allowsFileOperation(file, FileOperationKind.UPDATE_METADATA) },
      { ...ArchiveLibraryAction, fileFilter: (file: FileData | null) => allowsFileOperation(file, FileOperationKind.ARCHIVE) },
      { ...ScanFilesAction, fileFilter: (file: FileData | null) => allowsFileOperation(file, FileOperationKind.SCAN) },
    ];
  }, [busyGroupIds, changedGroups, itemStatus, mergeBusy, result, descending, source, sparse.unavailable, switchingHidden, switchingSort]);

  const onFileAction: GenericFileActionHandler<FileAction> = (data) => {
    const selectedFiles: FileData[] = data.state.selectedFilesForAction;
    if (sparse.unavailable && data.id !== ChonkyActions.ChangeSelection.id) return;
    if (data.id === sortSizeID) {
      void switchSort();
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
        state: { scan: { target: { kind: "files", entries: selectedFiles.map((file) => scanSelectionEntry(file, FileScope.ALL, t`Identical files`)) } } },
      });
      return;
    }
    const groupId = data.state.group?.id ?? sparse.fileRows.get(selectedFiles[0]?.id)?.group?.id;
    const row = groupId ? sparse.groupRows.get(groupId) : undefined;
    const group = row?.group;
    if (!group || !selectedFiles.length) return;
    // Action visibility and the operations themselves check group state; these guards only pick the target.
    if (data.id === mergeID && source === IdenticalSource.LIBRARY && selectedFiles.length === 1) {
      const target = selectedFiles[0];
      const reference = fileOperationReference(target).target;
      if (reference.oneofKind !== "fileId" || data.state.selectedFiles.length !== 1) return;
      const snapshot = resultRef.current;
      const targetName = target.name;
      const memberCount = String(group.memberCount);
      ask({
        title: t`Merge into ${targetName}?`,
        confirmLabel: t`Merge`,
        children: (
          <p>
            {t`Merge all ${memberCount} Library files and their saved versions into this file. The selected File keeps its original link. Other Files move to Library Trash and lose their original links. Disk files are kept.`}
          </p>
        ),
        onConfirm: async () => {
          if (sorting.current) throw new Error(t`Wait for sorting to finish before merging files.`);
          if (!snapshot || resultRef.current?.id !== snapshot.id || unavailableRef.current || changedGroupRef.current.has(group.id))
            throw new Error(t`The result changed. Find again.`);
          if (busyGroupRef.current.has(scopeKey + ":" + group.id)) throw new Error(t`A file operation is already in progress for this group.`);
          groupBusy(snapshot, group.id, 1);
          mergeActive.current++;
          setMergeBusy(true);
          try {
            const positions = await lookupPositions({
              resultId: snapshot.id,
              fileIds: [reference.fileId],
              ...sizeOrder(descendingRef.current),
            });
            const targetPosition = positions.positions[0];
            if (!targetPosition) throw new Error(t`Could not locate this group. Find again.`);
            const otherHeader = includeHidden ? targetPosition.visibleGroupHeaderPosition : targetPosition.allGroupHeaderPosition;
            let otherInterval: { start: number; end: number } | undefined;
            if (otherHeader !== undefined) {
              const other = await listRows({
                resultId: snapshot.id,
                offset: otherHeader,
                limit: 1,
                includeHidden: !includeHidden,
                ...sizeOrder(descendingRef.current),
              });
              const count = other.rows[0]?.displayMemberCount;
              if (count === undefined) throw new Error(t`Could not read the other hidden-file view. Find again.`);
              otherInterval = { start: Number(otherHeader), end: Number(otherHeader + count) };
            }
            if (resultRef.current?.id !== snapshot.id) throw new Error(t`The result changed. Find again.`);
            try {
              await filesCli.mergeIdentical({ scope, groupId: group.id, fingerprint: group.fingerprint, targetFileId: reference.fileId, dryrun: false })
                .response;
            } catch (failure) {
              if (staleGroup(failure) && resultRef.current?.id === snapshot.id) markChanged([group.id]);
              throw failure;
            }
            if (resultRef.current?.id !== snapshot.id) {
              toast.success(t`Merged into ${targetName}`);
              return;
            }
            const start = Number(row.groupHeaderPosition);
            const spans: ProjectionIntervals = includeHidden
              ? { fileId: reference.fileId, all: { start, end: start + Number(row.displayMemberCount) }, visible: otherInterval }
              : { fileId: reference.fileId, visible: { start, end: start + Number(row.displayMemberCount) }, all: otherInterval };
            setRemovedGroupIntervals((current) => new Map(current).set(group.id, spans));
            setRemovedGroups((current) => new Set(current).add(group.id));
            setRevision((value) => value + 1);
            toast.success(t`Merged into ${targetName}`);
          } finally {
            mergeActive.current--;
            setMergeBusy(mergeActive.current > 0);
            groupBusy(snapshot, group.id, -1);
          }
        },
      });
      return;
    }
    if (source !== IdenticalSource.LOCATIONS || ![keepID, removeID].includes(data.id)) return;
    if (data.id === keepID && (selectedFiles.length !== 1 || data.state.selectedFiles.length !== 1)) return;
    const run = () => (data.id === keepID ? keep(group, selectedFiles[0]) : removeFiles(selectedFiles));
    if (!confirmRemove) {
      runUIAction(run, data.id === keepID ? t`Keep failed` : t`Delete failed`);
      return;
    }
    const keptName = selectedFiles[0].name;
    const otherCount = String(group.memberCount - 1n);
    const deleteCount = selectedFiles.length;
    ask({
      title: t`Delete files?`,
      confirmLabel: t`Delete`,
      danger: true,
      children: (
        <>
          <p>
            {data.id === keepID
              ? t`Keep ${keptName} and delete all other ${otherCount} members, including members not loaded here.`
              : t`${plural(deleteCount, { one: "Delete # selected file.", other: "Delete # selected files." })}`}
          </p>
          <p>{t`Files move into each Location’s .trash folder. Library records and saved versions are kept.`}</p>
        </>
      ),
      onConfirm: () => {
        void runUIAction(run, data.id === keepID ? t`Keep failed` : t`Delete failed`);
      },
    });
  };

  return (
    <Box className="browser-box library-file-browser">
      <Box sx={{ display: "flex", alignItems: "center", gap: 1, flexWrap: "wrap" }}>
        <Button variant="contained" disabled={loading || (source === IdenticalSource.LOCATIONS && !roots.length)} onClick={() => void search()}>
          {t`Find`}
        </Button>
        <TextField
          select
          size="small"
          label={t`Source`}
          value={source}
          onChange={(event) => setSource(Number(event.target.value) === IdenticalSource.LOCATIONS ? IdenticalSource.LOCATIONS : IdenticalSource.LIBRARY)}
          sx={{ minWidth: 150 }}
        >
          <MenuItem value={IdenticalSource.LIBRARY}>{t`Library`}</MenuItem>
          <MenuItem value={IdenticalSource.LOCATIONS}>{t`Locations`}</MenuItem>
        </TextField>
        {source === IdenticalSource.LOCATIONS && <IdenticalScopePicker value={roots} onChange={setRoots} />}
        <FormControlLabel
          control={<Switch checked={includeHidden} disabled={switchingHidden} onChange={(_, checked) => void switchHidden(checked)} />}
          label={t`Show hidden files`}
        />
      </Box>
      <Typography variant="body2" color="text.secondary" sx={{ textAlign: "left", px: 1.5, pb: error || sparse.unavailable || sparse.errors.size > 0 ? 0 : 1 }}>
        {source === IdenticalSource.LIBRARY
          ? t`Files connected through matching current content or saved versions. Members may have different current content.`
          : t`Matching recorded content in the selected Locations. Run Scan separately to update content information.`}
      </Typography>
      {error && (
        <Feedback severity="error" action={<Button onClick={() => void search()}>{t`Retry`}</Button>}>
          {error}
        </Feedback>
      )}
      {sparse.unavailable && (
        <Feedback severity="warning" action={<Button onClick={() => void search()}>{t`Find again`}</Button>}>
          {t`This identical result needs a new Find to show accurate rows.`}
        </Feedback>
      )}
      {!sparse.unavailable && sparse.errors.size > 0 && (
        <Feedback severity="warning" action={<Button onClick={sparse.retry}>{t`Retry rows`}</Button>}>
          {t`Could not load some rows.`} {[...new Set(sparse.errors.values())].join("; ")}
        </Feedback>
      )}
      <Grid className="browser-container" container columnSpacing={1.5}>
        <Grid className="browser" size={7} component="section" aria-label={t`Identical file groups`}>
          <FileBrowser
            instanceId="identical"
            ref={browser}
            files={sparse.files}
            folderChain={[{ id: "identical", name: t`Identical files`, isDir: true, openable: false }]}
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
              loading={switchingSort || switchingHidden || sparse.loading ? "refreshing" : undefined}
              loadingLabel={switchingSort ? t`Sorting files` : switchingHidden ? t`Updating hidden files` : t`Loading files`}
              emptyPlaceholder={
                loading ? (
                  <ListPlaceholder loading label={t`Reading…`} />
                ) : error ? (
                  <ListPlaceholder label={t`Could not find identical files`} />
                ) : source === IdenticalSource.LOCATIONS && !roots.length ? (
                  <ListPlaceholder label={t`Choose Locations to search`} />
                ) : sparse.unavailable ? (
                  <ListPlaceholder label={t`Find again to refresh this result`} />
                ) : result ? (
                  <ListPlaceholder label={t`No matching groups`} />
                ) : (
                  <ListPlaceholder label={t`Find identical files to search`} />
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
