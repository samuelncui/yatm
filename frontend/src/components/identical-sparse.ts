import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { RpcError } from "@protobuf-ts/runtime-rpc";
import type { FileData } from "@samuelncui/chonky";
import { filesCli } from "@/api";
import { IdenticalSortOrder, type IdenticalPosition, type IdenticalRow, type IdenticalSortKey } from "@/entity";
import { errorMessage } from "@/tools";
import { displayedCount, mergeIntervals, toDisplayPosition, toSnapshotPosition, type Interval } from "./identical-positions";

export type IdenticalResult = { id: string; allCount: number; visibleCount: number };
export type ProjectionIntervals = { fileId: bigint; all?: Interval; visible?: Interval };
type CollapsedGroup = Interval & { fileId?: bigint };
export type SparseGroup = { id: string; name: string; memberCount: number; description?: string; actionIds?: string[]; expanded?: boolean };
export type SparseRow = { index: number; kind: "group"; group: SparseGroup } | { index: number; kind: "file"; group: SparseGroup; fileId: string };
const pageSize = 100;
const maxPages = 10;
type PinnedRows = Map<string, { row: IdenticalRow; file: FileData }>;
type Projection = {
  pins: PinnedRows;
  pages?: Map<number, IdenticalRow[]>;
  collapsed?: Map<string, CollapsedGroup>;
  removedGroups?: Map<string, ProjectionIntervals>;
  range?: [number, number];
};

function rowFileID(row: IdenticalRow | undefined) {
  const entry = row?.member?.entry;
  return entry?.associatedFileId ?? (entry?.reference?.target.oneofKind === "fileId" ? entry.reference.target.fileId : undefined);
}

function moveInterval(interval: Interval | undefined, start: bigint | undefined): Interval | undefined {
  if (!interval) return undefined;
  if (start === undefined) throw new Error("Could not locate a group in this result. Find again.");
  return { start: Number(start), end: Number(start) + interval.end - interval.start };
}

function pageNumbers(start: number, end: number, total: number, exclusions: Interval[]) {
  const pages = new Set<number>();
  for (let index = Math.max(0, start); index <= Math.min(total - 1, end); index++) pages.add(Math.floor(toSnapshotPosition(index, exclusions) / pageSize));
  return pages;
}

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
  const [loading, setLoading] = useState(false);
  const [collapsed, setCollapsed] = useState<Map<string, CollapsedGroup>>(new Map());
  const [pinned, setPinned] = useState<Map<string, { row: IdenticalRow; file: FileData }>>(new Map());
  const pending = useRef(new Map<number, AbortController>());
  const epoch = useRef(0);
  const range = useRef<[number, number]>([0, 40]);
  const previousResult = useRef<string | undefined>(undefined);
  const pinnedRef = useRef(pinned);
  const collapsedRef = useRef(collapsed);
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
    setLoading(false);
    if (previousResult.current !== resultId) {
      collapsedRef.current = new Map();
      setCollapsed(collapsedRef.current);
    }
    if (previousResult.current !== resultId) {
      pinnedRef.current = new Map();
      setPinned(pinnedRef.current);
      setUnavailable(false);
    }
    previousResult.current = resultId;
    range.current = [0, 40];
    return () => {
      epochRef.current++;
      for (const controller of pendingRequests.values()) controller.abort();
      pendingRequests.clear();
    };
  }, [resultId]);

  const projectionExclusions = useCallback(
    (positions: Map<string, IdenticalPosition>, collapsedGroups = collapsed, groupIntervals = removedGroupIntervals, showHidden = includeHidden) => {
      const intervals: Interval[] = [...collapsedGroups.values()];
      for (const position of positions.values()) {
        const value = showHidden ? position.allPosition : position.visiblePosition;
        if (value !== undefined && value >= 0n) intervals.push({ start: Number(value), end: Number(value) });
      }
      for (const groupId of removedGroups) {
        const span = groupIntervals.get(groupId);
        const interval = showHidden ? span?.all : span?.visible;
        if (interval) intervals.push(interval);
      }
      return mergeIntervals(intervals);
    },
    [collapsed, includeHidden, removedGroupIntervals, removedGroups],
  );
  const exclusions = useMemo(() => projectionExclusions(removed), [projectionExclusions, removed]);

  const prepareProjection = useCallback(
    async (showHidden: boolean, nextSortKey: IdenticalSortKey, nextDescending: boolean, nextRemoved = removed): Promise<Projection> => {
      const resort = nextSortKey !== sortKey || nextDescending !== descending;
      const changeHidden = showHidden !== includeHidden;
      const nextPages = resort || changeHidden ? new Map<number, IdenticalRow[]>() : undefined;
      const request = epoch.current;
      const positions = new Map<string, IdenticalPosition>();
      const groupAnchors = new Map<string, bigint>();
      // Selection can change while a position lookup is pending. Query only new IDs,
      // then remap the latest selection before replacing its pages and pins together.
      for (;;) {
        // Keep the current viewport until its server-ordered replacement is ready.
        const viewport = range.current;
        const selected = pinnedRef.current;
        const collapsedGroups = collapsedRef.current;
        if (!resultId) return { pins: new Map(selected), pages: nextPages };
        if (resort) {
          for (const [id, interval] of collapsedGroups) {
            if (groupAnchors.has(id)) continue;
            let fileId = interval.fileId;
            if (fileId === undefined) {
              // A header can be collapsed before any of its members have loaded.
              const reply = await filesCli.listIdenticalRows({
                resultId,
                offset: BigInt(interval.start),
                limit: 1,
                includeHidden,
                sortKey,
                sortOrder: descending ? IdenticalSortOrder.DESC : IdenticalSortOrder.ASC,
              }).response;
              if (request !== epoch.current) throw new Error("The result changed. Find again.");
              fileId = rowFileID(reply.rows[0]);
            }
            if (fileId === undefined) throw new Error("Could not locate a collapsed group. Find again.");
            groupAnchors.set(id, fileId);
          }
          for (const [id, interval] of removedGroupIntervals) groupAnchors.set(id, interval.fileId);
        }
        const ids = [...new Set([...selected.values()].map(({ row }) => rowFileID(row)).concat([...groupAnchors.values()]))].filter(
          (id): id is bigint => id !== undefined && !positions.has(String(id)),
        );
        for (let offset = 0; offset < ids.length; offset += pageSize) {
          const reply = await filesCli.lookupIdenticalPositions({
            resultId,
            fileIds: ids.slice(offset, offset + pageSize),
            sortKey: nextSortKey,
            sortOrder: nextDescending ? IdenticalSortOrder.DESC : IdenticalSortOrder.ASC,
          }).response;
          if (request !== epoch.current) throw new Error("The result changed. Find again.");
          for (const position of reply.positions) positions.set(String(position.fileId), position);
        }
        if (ids.some((id) => !positions.has(String(id)))) throw new Error("Could not locate selected files in this result. Find again.");
        if (
          selected !== pinnedRef.current ||
          collapsedGroups !== collapsedRef.current ||
          (nextPages && (viewport[0] !== range.current[0] || viewport[1] !== range.current[1]))
        )
          continue;
        const next = new Map<string, { row: IdenticalRow; file: FileData }>();
        for (const [fileId, value] of selected) {
          const id = rowFileID(value.row);
          const found = id === undefined ? undefined : positions.get(String(id));
          const position = showHidden ? found?.allPosition : found?.visiblePosition;
          const header = showHidden ? found?.allGroupHeaderPosition : found?.visibleGroupHeaderPosition;
          if (position !== undefined && header !== undefined) next.set(fileId, { ...value, row: { ...value.row, position, groupHeaderPosition: header } });
        }
        const nextCollapsed = changeHidden ? new Map<string, CollapsedGroup>() : new Map(collapsedGroups);
        const nextGroups = new Map(removedGroupIntervals);
        if (resort) {
          for (const [id, interval] of collapsedGroups) {
            const fileId = groupAnchors.get(id)!;
            const found = positions.get(String(fileId));
            const header = showHidden ? found?.allGroupHeaderPosition : found?.visibleGroupHeaderPosition;
            nextCollapsed.set(id, { ...moveInterval(interval, header === undefined ? undefined : header + 1n)!, fileId });
          }
          for (const [id, interval] of removedGroupIntervals) {
            const found = positions.get(String(interval.fileId));
            nextGroups.set(id, {
              fileId: interval.fileId,
              all: moveInterval(interval.all, found?.allGroupHeaderPosition),
              visible: moveInterval(interval.visible, found?.visibleGroupHeaderPosition),
            });
          }
        }
        const nextExclusions = projectionExclusions(nextRemoved, nextCollapsed, nextGroups, showHidden);
        const total = displayedCount(result ? (showHidden ? result.allCount : result.visibleCount) : 0, nextExclusions);
        const start = Math.min(viewport[0], Math.max(0, total - (viewport[1] - viewport[0] + 1)));
        const nextRange: [number, number] = [start, Math.min(total - 1, start + viewport[1] - viewport[0])];
        if (nextPages) {
          for (const page of pageNumbers(...nextRange, total, nextExclusions)) {
            if (nextPages.has(page)) continue;
            const reply = await filesCli.listIdenticalRows(
              {
                resultId,
                offset: BigInt(page * pageSize),
                limit: pageSize,
                includeHidden: showHidden,
                sortKey: nextSortKey,
                sortOrder: nextDescending ? IdenticalSortOrder.DESC : IdenticalSortOrder.ASC,
              },
              {},
            ).response;
            if (request !== epoch.current) throw new Error("The result changed. Find again.");
            nextPages.set(page, reply.rows);
            while (nextPages.size > maxPages) nextPages.delete(nextPages.keys().next().value!);
          }
        }
        if (
          selected !== pinnedRef.current ||
          collapsedGroups !== collapsedRef.current ||
          (nextPages && (viewport[0] !== range.current[0] || viewport[1] !== range.current[1]))
        )
          continue;
        return {
          pins: next,
          pages: nextPages,
          collapsed: resort || changeHidden ? nextCollapsed : undefined,
          removedGroups: resort ? nextGroups : undefined,
          range: nextRange,
        };
      }
    },
    [resultId, result, sortKey, descending, includeHidden, removed, removedGroupIntervals, projectionExclusions],
  );
  const useProjection = useCallback((next: Projection) => {
    epoch.current++;
    for (const controller of pending.current.values()) controller.abort();
    pending.current.clear();
    setLoading(false);
    if (next.range) range.current = next.range;
    setPages(next.pages ?? new Map());
    setErrors(new Map());
    pinnedRef.current = next.pins;
    setPinned(next.pins);
    if (next.collapsed) {
      collapsedRef.current = next.collapsed;
      setCollapsed(next.collapsed);
    }
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

  const snapshotCount = result ? (includeHidden ? result.allCount : result.visibleCount) : 0;
  const totalCount = displayedCount(snapshotCount, exclusions);

  const requestRange = useCallback(
    (startIndex: number, endIndex: number, retry = false) => {
      if (!result || unavailable || totalCount === 0) return;
      range.current = [startIndex, endIndex];
      const wanted = pageNumbers(startIndex, endIndex, totalCount, exclusions);
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
            if (pending.current.get(page) !== controller) return;
            pending.current.delete(page);
            setLoading(pending.current.size > 0);
          });
      }
      setLoading(pending.current.size > 0);
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
      const next = new Map(collapsedRef.current);
      if (next.has(groupId)) next.delete(groupId);
      else {
        const start = Number(row.groupHeaderPosition) + 1;
        const member = allRows.find((item) => item.group?.id === groupId && rowFileID(item) !== undefined);
        next.set(groupId, { start, end: start + Number(row.displayMemberCount) - 1, fileId: rowFileID(member) });
      }
      collapsedRef.current = next;
      setCollapsed(next);
    },
    [allRows, groupRows],
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
    loading: !unavailable && loading,
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
