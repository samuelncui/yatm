import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { RpcError } from "@protobuf-ts/runtime-rpc";
import type { FileData } from "@samuelncui/chonky";
import { filesCli } from "@/api";
import { IdenticalSortOrder, type IdenticalPosition, type IdenticalRow, type IdenticalSortKey } from "@/entity";
import { errorMessage } from "@/tools";
import { displayedCount, mergeIntervals, toDisplayPosition, toSnapshotPosition, type Interval } from "./identical-positions";

export type IdenticalResult = { id: string; allCount: number; visibleCount: number };
export type ProjectionIntervals = { all?: Interval; visible?: Interval };
export type SparseGroup = { id: string; name: string; memberCount: number; description?: string; actionIds?: string[]; expanded?: boolean };
export type SparseRow = { index: number; kind: "group"; group: SparseGroup } | { index: number; kind: "file"; group: SparseGroup; fileId: string };
const pageSize = 100;
const maxPages = 10;

export function useIdenticalSparse(
  result: IdenticalResult | undefined,
  includeHidden: boolean,
  sortKey: IdenticalSortKey,
  descending: boolean,
  removed: Map<string, IdenticalPosition>,
  suppressedIds: Set<string>,
  removedGroups: Set<string>,
  removedGroupIntervals: Map<string, ProjectionIntervals>,
  changedGroups: Set<string>,
  busyGroups: Set<string>,
  memberFile: (row: IdenticalRow) => FileData | undefined,
  actionIds: string[],
  description: string,
) {
  const [pages, setPages] = useState<Map<number, IdenticalRow[]>>(new Map());
  const [errors, setErrors] = useState<Map<number, string>>(new Map());
  const [unavailable, setUnavailable] = useState(false);
  const [collapsed, setCollapsed] = useState<Map<string, Interval>>(new Map());
  const [pinned, setPinned] = useState<Map<string, { row: IdenticalRow; file: FileData }>>(new Map());
  const pending = useRef(new Map<number, AbortController>());
  const epoch = useRef(0);
  const range = useRef<[number, number]>([0, 40]);
  const previousResult = useRef<string | undefined>(undefined);
  const previousHidden = useRef(includeHidden);
  const pinnedRef = useRef(pinned);
  const resultId = result?.id;
  useEffect(() => {
    pinnedRef.current = pinned;
  }, [pinned]);

  useEffect(() => {
    const epochRef = epoch;
    const pendingRequests = pending.current;
    epochRef.current++;
    for (const controller of pendingRequests.values()) controller.abort();
    pendingRequests.clear();
    setPages(new Map());
    setErrors(new Map());
    if (previousResult.current !== resultId || previousHidden.current !== includeHidden) setCollapsed(new Map());
    if (previousResult.current !== resultId) {
      pinnedRef.current = new Map();
      setPinned(pinnedRef.current);
      setUnavailable(false);
    }
    previousResult.current = resultId;
    previousHidden.current = includeHidden;
    range.current = [0, 40];
    return () => {
      epochRef.current++;
      for (const controller of pendingRequests.values()) controller.abort();
      pendingRequests.clear();
    };
  }, [resultId, includeHidden, sortKey, descending]);

  const prepareProjection = useCallback(
    async (showHidden: boolean, nextSortKey: IdenticalSortKey, nextDescending: boolean) => {
      const positions = new Map<string, IdenticalPosition>();
      // Selection can change while a position lookup is pending. Query only new IDs,
      // then remap the latest selection before replacing its pages and pins together.
      for (;;) {
        const selected = pinnedRef.current;
        if (!resultId || !selected.size) return new Map(selected);
        const ids = [...selected.values()].flatMap(({ row }) => {
          const entry = row.member?.entry;
          const id = entry?.associatedFileId ?? (entry?.reference?.target.oneofKind === "fileId" ? entry.reference.target.fileId : undefined);
          return id === undefined || positions.has(String(id)) ? [] : [id];
        });
        for (let offset = 0; offset < ids.length; offset += pageSize) {
          const reply = await filesCli.lookupIdenticalPositions({
            resultId,
            fileIds: ids.slice(offset, offset + pageSize),
            sortKey: nextSortKey,
            sortOrder: nextDescending ? IdenticalSortOrder.DESC : IdenticalSortOrder.ASC,
          }).response;
          for (const position of reply.positions) positions.set(String(position.fileId), position);
        }
        if (ids.some((id) => !positions.has(String(id)))) throw new Error("Could not locate selected files in this result. Find again.");
        if (selected !== pinnedRef.current) continue;
        const next = new Map<string, { row: IdenticalRow; file: FileData }>();
        for (const [fileId, value] of selected) {
          const entry = value.row.member?.entry;
          const id = entry?.associatedFileId ?? (entry?.reference?.target.oneofKind === "fileId" ? entry.reference.target.fileId : undefined);
          const found = id === undefined ? undefined : positions.get(String(id));
          const position = showHidden ? found?.allPosition : found?.visiblePosition;
          const header = showHidden ? found?.allGroupHeaderPosition : found?.visibleGroupHeaderPosition;
          if (position !== undefined && header !== undefined) next.set(fileId, { ...value, row: { ...value.row, position, groupHeaderPosition: header } });
        }
        return next;
      }
    },
    [resultId],
  );
  const useProjection = useCallback((next: Map<string, { row: IdenticalRow; file: FileData }>) => {
    setPages(new Map());
    pinnedRef.current = next;
    setPinned(next);
  }, []);
  const invalidate = useCallback(() => setUnavailable(true), []);

  const allRows = useMemo(() => {
    const rows = new Map<number, IdenticalRow>();
    for (const page of pages.values()) for (const row of page) rows.set(Number(row.position), row);
    for (const value of pinned.values()) rows.set(Number(value.row.position), value.row);
    return [...rows.values()];
  }, [pages, pinned]);
  const groupRows = useMemo(() => {
    const groups = new Map<string, IdenticalRow>();
    for (const row of allRows) if (row.group && (!groups.has(row.group.id) || !row.member)) groups.set(row.group.id, row);
    return groups;
  }, [allRows]);
  const fileRows = useMemo(() => {
    const files = new Map<string, IdenticalRow>();
    for (const row of allRows) {
      const file = memberFile(row);
      if (file) files.set(file.id, row);
    }
    return files;
  }, [allRows, memberFile]);

  const exclusions = useMemo(() => {
    const intervals: Interval[] = [...collapsed.values()];
    for (const position of removed.values()) {
      const value = includeHidden ? position.allPosition : position.visiblePosition;
      if (value !== undefined && value >= 0n) intervals.push({ start: Number(value), end: Number(value) });
    }
    for (const groupId of removedGroups) {
      const span = removedGroupIntervals.get(groupId);
      const interval = includeHidden ? span?.all : span?.visible;
      if (interval) intervals.push(interval);
    }
    return mergeIntervals(intervals);
  }, [collapsed, includeHidden, removed, removedGroupIntervals, removedGroups]);
  const snapshotCount = result ? (includeHidden ? result.allCount : result.visibleCount) : 0;
  const totalCount = displayedCount(snapshotCount, exclusions);

  const requestRange = useCallback(
    (startIndex: number, endIndex: number, retry = false) => {
      if (!result || unavailable || totalCount === 0) return;
      range.current = [startIndex, endIndex];
      const wanted = new Set<number>();
      for (let index = Math.max(0, startIndex); index <= Math.min(totalCount - 1, endIndex); index++)
        wanted.add(Math.floor(toSnapshotPosition(index, exclusions) / pageSize));
      for (const [page, controller] of pending.current)
        if (!wanted.has(page)) {
          controller.abort();
          pending.current.delete(page);
        }
      for (const page of wanted) {
        if (pages.has(page) || pending.current.has(page) || (errors.has(page) && !retry)) continue;
        const controller = new AbortController();
        const request = epoch.current;
        pending.current.set(page, controller);
        void filesCli
          .listIdenticalRows(
            {
              resultId: result.id,
              offset: BigInt(page * pageSize),
              limit: pageSize,
              includeHidden,
              sortKey,
              sortOrder: descending ? IdenticalSortOrder.DESC : IdenticalSortOrder.ASC,
            },
            { abort: controller.signal },
          )
          .response.then((reply) => {
            if (request !== epoch.current || controller.signal.aborted) return;
            setPages((current) => {
              const next = new Map(current);
              next.delete(page);
              next.set(page, reply.rows);
              while (next.size > maxPages) next.delete(next.keys().next().value!);
              return next;
            });
            setErrors((current) => {
              const next = new Map(current);
              next.delete(page);
              return next;
            });
          })
          .catch((failure) => {
            if (request !== epoch.current || controller.signal.aborted) return;
            if (failure instanceof RpcError && failure.code === "FAILED_PRECONDITION") setUnavailable(true);
            else
              setErrors((current) => {
                const next = new Map(current);
                next.delete(page);
                next.set(page, errorMessage(failure, "Could not load rows"));
                while (next.size > maxPages) next.delete(next.keys().next().value!);
                return next;
              });
          })
          .finally(() => {
            if (pending.current.get(page) === controller) pending.current.delete(page);
          });
      }
    },
    [errors, exclusions, includeHidden, sortKey, descending, pages, result, totalCount, unavailable],
  );
  useEffect(() => {
    if (result) requestRange(...range.current);
  }, [requestRange, result]);
  const retry = useCallback(() => {
    setErrors(new Map());
    requestRange(...range.current, true);
  }, [requestRange]);
  const toggleGroup = useCallback(
    (groupId: string) => {
      const row = groupRows.get(groupId);
      if (!row) return;
      setCollapsed((current) => {
        const next = new Map(current);
        if (next.has(groupId)) next.delete(groupId);
        else {
          const start = Number(row.groupHeaderPosition) + 1;
          next.set(groupId, { start, end: start + Number(row.displayMemberCount) - 1 });
        }
        return next;
      });
    },
    [groupRows],
  );
  const pinSelection = useCallback(
    (files: FileData[]) => {
      const next = new Map<string, { row: IdenticalRow; file: FileData }>();
      for (const file of files) {
        const row = fileRows.get(file.id) ?? pinnedRef.current.get(file.id)?.row;
        if (row) next.set(file.id, { row, file });
      }
      pinnedRef.current = next;
      setPinned(next);
    },
    [fileRows],
  );
  const removedCounts = useMemo(() => {
    const counts = new Map<string, number>();
    for (const position of removed.values()) {
      if (!includeHidden && position.visiblePosition === undefined) continue;
      counts.set(position.groupId, (counts.get(position.groupId) ?? 0) + 1);
    }
    return counts;
  }, [includeHidden, removed]);
  const sparseRows = useMemo(() => {
    const rows: SparseRow[] = [];
    for (const row of allRows) {
      if (!row.group || removedGroups.has(row.group.id)) continue;
      const entry = row.member?.entry;
      const fileId = entry?.associatedFileId ?? (entry?.reference?.target.oneofKind === "fileId" ? entry.reference.target.fileId : undefined);
      if (fileId !== undefined && (removed.has(String(fileId)) || suppressedIds.has(String(fileId)))) continue;
      const index = toDisplayPosition(Number(row.position), exclusions);
      if (index === undefined) continue;
      const group: SparseGroup = {
        id: row.group.id,
        name: row.group.name,
        memberCount: Math.max(0, Number(row.displayMemberCount) - (removedCounts.get(row.group.id) ?? 0)),
        description: changedGroups.has(row.group.id)
          ? "Group changed. Find again before Keep or Merge."
          : busyGroups.has(row.group.id)
            ? "File operation in progress"
            : description,
        actionIds,
        expanded: !collapsed.has(row.group.id),
      };
      const file = memberFile(row);
      if (file) rows.push({ index, kind: "file", group, fileId: file.id });
      else if (!row.member) rows.push({ index, kind: "group", group });
    }
    return rows.sort((a, b) => a.index - b.index);
  }, [actionIds, allRows, busyGroups, changedGroups, collapsed, description, exclusions, memberFile, removed, removedCounts, removedGroups, suppressedIds]);
  const files = useMemo(() => {
    const next = new Map<string, FileData>();
    const visibleIds = new Set(sparseRows.flatMap((row) => (row.kind === "file" ? [row.fileId] : [])));
    for (const row of allRows) {
      const file = memberFile(row);
      if (file && visibleIds.has(file.id)) next.set(file.id, file);
    }
    for (const [id, value] of pinned) {
      const entry = value.row.member?.entry;
      const fileId = entry?.associatedFileId ?? (entry?.reference?.target.oneofKind === "fileId" ? entry.reference.target.fileId : undefined);
      if (removedGroups.has(value.row.group?.id ?? "") || (fileId !== undefined && (removed.has(String(fileId)) || suppressedIds.has(String(fileId)))))
        continue;
      next.set(id, memberFile(value.row) ?? value.file);
    }
    return [...next.values()];
  }, [allRows, memberFile, pinned, removed, removedGroups, sparseRows, suppressedIds]);
  return {
    files: unavailable ? [] : files,
    grouping: {
      mode: "continuous" as const,
      sparse: { totalCount: unavailable ? 0 : totalCount, rows: unavailable ? [] : sparseRows, onRangeChanged: requestRange, onToggleGroup: toggleGroup },
    },
    errors,
    unavailable,
    invalidate,
    retry,
    groupRows,
    fileRows,
    pinSelection,
    prepareProjection,
    useProjection,
  };
}
