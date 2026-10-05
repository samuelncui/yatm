import { useCallback, useEffect, useRef, useState } from "react";
import { filesCli } from "@/api";
import type { FileVersion } from "@/entity";

export const versionArchiveTime = (version: FileVersion) => version.lastArchivedAtNs ?? version.firstArchivedAtNs;

/** Match Library: last archive date descending, absent last dates last, then ID descending. */
export const compareVersionDates = (left: FileVersion, right: FileVersion) => {
  const a = left.lastArchivedAtNs;
  const b = right.lastArchivedAtNs;
  if (a === b) return left.id === right.id ? 0 : left.id > right.id ? -1 : 1;
  if (a === undefined) return 1;
  if (b === undefined) return -1;
  return a > b ? -1 : 1;
};

async function* versionPages(fileID: bigint, afterId: bigint, limit: 20 | 100, signal: AbortSignal) {
  while (!signal.aborted) {
    const page = await filesCli.listVersions({ fileId: fileID, afterId, limit }, { abort: signal }).response;
    if (signal.aborted) return;
    yield page;
    if (!page.hasMore || !page.versions.length) return;
    afterId = page.versions.at(-1)!.id;
  }
}

/** History refreshes replace all loaded pages together; a failed read keeps the complete list. */
export function useFileVersionPages(fileID: bigint | undefined, refresh?: unknown, retainPages = false) {
  const [versions, setVersions] = useState<FileVersion[]>([]);
  const [more, setMore] = useState(false);
  const [loading, setLoading] = useState(false);
  const [failure, setFailure] = useState<unknown>();
  const request = useRef<AbortController | null>(null);
  const loadedPages = useRef(1);
  const previousFileID = useRef(fileID);
  const load = useCallback(
    async (afterId = 0n) => {
      if (fileID === undefined) return;
      request.current?.abort();
      const controller = new AbortController();
      request.current = controller;
      setLoading(true);
      try {
        const pageCount = !afterId && retainPages ? loadedPages.current : 1;
        const values: FileVersion[] = [];
        let pages = 0;
        let hasMore = false;
        for await (const page of versionPages(fileID, afterId, 20, controller.signal)) {
          values.push(...page.versions);
          hasMore = page.hasMore;
          if (++pages >= pageCount) break;
        }
        if (controller.signal.aborted) return;
        loadedPages.current = afterId ? loadedPages.current + pages : pages;
        setVersions((current) => (afterId ? [...current, ...values] : values));
        setMore(hasMore);
        setFailure(undefined);
      } catch (error) {
        if (!controller.signal.aborted) setFailure(error);
      } finally {
        if (!controller.signal.aborted) setLoading(false);
      }
    },
    [fileID, retainPages],
  );
  useEffect(() => {
    if (!retainPages || previousFileID.current !== fileID) {
      setVersions([]);
      setMore(false);
      setFailure(undefined);
      setLoading(false);
      loadedPages.current = 1;
    }
    previousFileID.current = fileID;
    const pending = request;
    void load();
    return () => pending.current?.abort();
  }, [fileID, load, refresh, retainPages]);
  return { versions, more, loading, failure, load, remove: (id: bigint) => setVersions((current) => current.filter((version) => version.id !== id)) };
}

/** Overview needs the newest archive date, which may occur beyond the first ID-ordered page. */
export function useLatestSavedVersion(fileID: bigint | undefined, refresh: unknown) {
  const [version, setVersion] = useState<FileVersion | null>();
  useEffect(() => {
    setVersion(undefined);
    if (fileID === undefined) {
      setVersion(null);
      return;
    }
    const controller = new AbortController();
    const load = async () => {
      let newest: FileVersion | undefined;
      for await (const page of versionPages(fileID, 0n, 100, controller.signal)) {
        for (const candidate of page.versions) {
          if (!newest || compareVersionDates(candidate, newest) < 0) newest = candidate;
        }
      }
      if (!controller.signal.aborted) setVersion(newest ?? null);
    };
    // History errors belong to Saved versions; Overview has no Preview without a known version.
    void load().catch(() => {
      if (!controller.signal.aborted) setVersion(null);
    });
    return () => controller.abort();
  }, [fileID, refresh]);
  return version;
}
