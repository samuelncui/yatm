import { Feedback } from "@/components/feedback";
import { type ReactNode, useEffect, useRef, useState } from "react";
import { Virtuoso, type ListRange } from "react-virtuoso";

import Button from "@mui/material/Button";
import Dialog from "@mui/material/Dialog";
import DialogActions from "@mui/material/DialogActions";
import DialogContent from "@mui/material/DialogContent";
import DialogTitle from "@mui/material/DialogTitle";
import LinearProgress from "@mui/material/LinearProgress";
import Tab from "@mui/material/Tab";

import { archiveJobCli, restoreJobCli, scanJobCli } from "@/api";
import { PageTabs } from "@/components/page-navigation";
import { FileRowPlaceholder } from "@/components/job-file-list-item";
import { JobResultOrder } from "@/entity";
import { errorMessage } from "@/tools";

/**
 * Every Job item list renders through this module and nothing else pages a Job
 * manifest listing; `eslint.config.js` restricts the calls below to this file.
 */

export type JobItemListing = "scan-entries" | "archive-files" | "restore-files";

type JobItemPageRequest = {
  limit: number;
  cursor: string;
  order: JobResultOrder;
  offset?: bigint;
  includeTotal: boolean;
};

type JobItemPage = {
  rows: unknown[];
  hasMore: boolean;
  total?: bigint;
};

export type JobItemView<T = any> = {
  id: string;
  label: string;
  emptyLabel: string;
  listing: JobItemListing;
  /** The row's own order key, which becomes the cursor of the next page. */
  cursorOf: (row: T) => string;
  render: (row: T) => ReactNode;
};

export type JobItemViews = JobItemView[];

const pageSize = 200;

const listingLoaders: Record<JobItemListing, (jobId: bigint, page: JobItemPageRequest) => Promise<JobItemPage>> = {
  "scan-entries": async (jobId, page) => {
    const reply = await scanJobCli.listEntries({ id: jobId, ...page }).response;
    return { rows: reply.entries, hasMore: reply.hasMore, total: reply.totalEntryCount };
  },
  "archive-files": async (jobId, page) => {
    const reply = await archiveJobCli.listFiles({ id: jobId, filterStatus: [], ...page }).response;
    return { rows: reply.items, hasMore: reply.hasMore, total: reply.totalFileCount };
  },
  "restore-files": async (jobId, page) => {
    const reply = await restoreJobCli.listFiles({ id: jobId, filterStatus: [], ...page }).response;
    return { rows: reply.items, hasMore: reply.hasMore, total: reply.totalFileCount };
  },
};

type LoadedWindow = { start: number; rows: unknown[] };

const windowEnd = (window: LoadedWindow) => window.start + window.rows.length;

const loadedAt = (windows: LoadedWindow[], index: number): unknown => {
  for (const window of windows) {
    if (index >= window.start && index < windowEnd(window)) return window.rows[index - window.start];
  }
  return undefined;
};

const isCovered = (windows: LoadedWindow[], start: number, end: number) => {
  for (let index = start; index <= end; index++) {
    if (loadedAt(windows, index) === undefined) return false;
  }
  return true;
};

const firstUncovered = (windows: LoadedWindow[], start: number, end: number) => {
  for (let index = start; index <= end; index++) {
    if (loadedAt(windows, index) === undefined) return index;
  }
  return end + 1;
};

/** Overlapping windows repeat the same manifest rows, so only new trailing rows are kept. */
const mergeWindow = (windows: LoadedWindow[], incoming: LoadedWindow): LoadedWindow[] => {
  const merged: LoadedWindow[] = [];
  for (const window of [...windows, incoming].sort((left, right) => left.start - right.start)) {
    const last = merged.at(-1);
    if (last && window.start <= windowEnd(last)) {
      last.rows = [...last.rows, ...window.rows.slice(Math.max(0, windowEnd(last) - window.start))];
      continue;
    }
    merged.push({ start: window.start, rows: [...window.rows] });
  }
  return merged;
};

type JobItemAnchor =
  | { kind: "head"; limit: number }
  | { kind: "offset"; offset: number; limit: number }
  | { kind: "after"; cursor: string; limit: number }
  | { kind: "before"; cursor: string; limit: number };

/**
 * A visible position next to a loaded window continues from that window's edge;
 * any other position needs an offset anchor, which is the only way to locate it.
 */
const planAnchor = (view: JobItemView, windows: LoadedWindow[], index: number): { anchor: JobItemAnchor; start: (rows: number) => number } => {
  const after = windows.find((window) => windowEnd(window) === index);
  if (after) {
    return {
      anchor: { kind: "after", cursor: view.cursorOf(loadedAt(windows, windowEnd(after) - 1)), limit: pageSize },
      start: () => windowEnd(after),
    };
  }
  const before = windows.find((window) => window.start === index + 1);
  if (before) {
    return {
      anchor: { kind: "before", cursor: view.cursorOf(loadedAt(windows, before.start)), limit: pageSize },
      start: (rows) => Math.max(0, before.start - rows),
    };
  }
  if (windows.length === 0 && index === 0) return { anchor: { kind: "head", limit: pageSize }, start: () => 0 };
  return { anchor: { kind: "offset", offset: index, limit: pageSize }, start: () => index };
};

const requestFor = (anchor: JobItemAnchor, includeTotal: boolean): JobItemPageRequest => {
  switch (anchor.kind) {
    case "head":
      return { limit: anchor.limit, cursor: "", order: JobResultOrder.ASCENDING, includeTotal };
    case "offset":
      return { limit: anchor.limit, cursor: "", order: JobResultOrder.ASCENDING, offset: BigInt(anchor.offset), includeTotal };
    case "after":
      return { limit: anchor.limit, cursor: anchor.cursor, order: JobResultOrder.ASCENDING, includeTotal };
    case "before":
      return { limit: anchor.limit, cursor: anchor.cursor, order: JobResultOrder.DESCENDING, includeTotal };
  }
};

export const JobResultsDialog = ({ jobId, title, views }: { jobId: bigint; title: string; views: JobItemViews }) => {
  const [open, setOpen] = useState(false);
  const [viewIndex, setViewIndex] = useState(0);
  const [total, setTotal] = useState(0);
  const [windows, setWindows] = useState<LoadedWindow[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");

  const viewsRef = useRef(views);
  const windowsRef = useRef<LoadedWindow[]>([]);
  const totalRef = useRef(0);
  const pendingRange = useRef<{ start: number; end: number; includeTotal: boolean } | null>(null);
  const visibleRange = useRef({ start: 0, end: pageSize - 1 });
  const generation = useRef(0);
  const busy = useRef(false);

  useEffect(() => {
    viewsRef.current = views;
  }, [views]);

  useEffect(
    () => () => {
      generation.current++;
    },
    [],
  );

  const publish = (next: LoadedWindow[]) => {
    windowsRef.current = next;
    setWindows(next);
  };

  const fill = async (index: number, start: number, end: number, includeTotal: boolean) => {
    const view = viewsRef.current[index];
    if (!view) return;
    const run = generation.current;
    // Each pass loads one window; a bounded number keeps one scroll burst responsive.
    for (let pass = 0; pass < 4; pass++) {
      const current = windowsRef.current;
      // A requested range may reach past the result set; only real rows are fetched.
      const last = totalRef.current > 0 ? Math.min(end, totalRef.current - 1) : end;
      if (last < start || isCovered(current, start, last)) return;
      const target = firstUncovered(current, start, last);
      const plan = planAnchor(view, current, target);
      const page = await listingLoaders[view.listing](jobId, requestFor(plan.anchor, includeTotal && current.length === 0));
      if (run !== generation.current) return;
      if (page.total !== undefined) {
        totalRef.current = Number(page.total);
        setTotal(totalRef.current);
      }
      if (page.rows.length === 0) return;
      const rows = plan.anchor.kind === "before" ? [...page.rows].reverse() : page.rows;
      publish(mergeWindow(windowsRef.current, { start: plan.start(page.rows.length), rows }));
    }
  };

  const drain = async (index: number) => {
    if (busy.current) return;
    busy.current = true;
    const run = generation.current;
    setLoading(true);
    // A retry starts from a clean error state; loaded windows stay untouched.
    setError("");
    try {
      while (pendingRange.current && run === generation.current) {
        const wanted = pendingRange.current;
        pendingRange.current = null;
        await fill(index, wanted.start, wanted.end, wanted.includeTotal);
        // A stopped window never grows again, so a missing row ends the loop.
        if (wanted.includeTotal && windowsRef.current.length === 0) return;
      }
    } catch (reason) {
      if (run === generation.current) setError(errorMessage(reason, "Could not load Job results"));
    } finally {
      if (run === generation.current) {
        busy.current = false;
        setLoading(false);
      }
    }
  };

  const schedule = (index: number, range: ListRange, includeTotal = false) => {
    const wanted = { start: Math.max(0, range.startIndex), end: range.endIndex };
    visibleRange.current = wanted;
    pendingRange.current = { ...wanted, includeTotal };
    void drain(index);
  };

  const reset = (index: number) => {
    generation.current++;
    busy.current = false;
    pendingRange.current = null;
    publish([]);
    totalRef.current = 0;
    setTotal(0);
    setError("");
    setViewIndex(index);
    schedule(index, { startIndex: 0, endIndex: pageSize - 1 }, true);
  };

  const show = () => {
    setOpen(true);
    reset(0);
  };
  const close = () => {
    generation.current++;
    busy.current = false;
    pendingRange.current = null;
    setLoading(false);
    setOpen(false);
  };

  const view = views[viewIndex] ?? views[0];
  return (
    <>
      <Button size="small" onClick={show}>
        Results
      </Button>
      {open && view && (
        <Dialog
          open
          onClose={close}
          maxWidth="lg"
          fullWidth
          scroll="paper"
          className="job-view-dialog"
          slotProps={{ paper: { sx: { height: "calc(100% - 64px)" } } }}
        >
          <DialogTitle>{title}</DialogTitle>
          <DialogContent dividers className="job-results-content" sx={{ p: 0 }}>
            {views.length > 1 && (
              <PageTabs value={viewIndex} onChange={(_, value: number) => reset(value)} aria-label={`${title} views`}>
                {views.map((candidate) => (
                  <Tab key={candidate.id} label={candidate.label} />
                ))}
              </PageTabs>
            )}
            <div className="job-results-page">
              {loading && <LinearProgress className="job-results-loading" aria-label="Loading Job results" />}
              {error && (
                <Feedback
                  severity="error"
                  action={
                    <Button
                      onClick={() =>
                        schedule(viewIndex, { startIndex: visibleRange.current.start, endIndex: visibleRange.current.end }, totalRef.current === 0)
                      }
                    >
                      Retry
                    </Button>
                  }
                >
                  {error}
                </Feedback>
              )}
              {total > 0 && (
                <Virtuoso
                  style={{ width: "100%", height: "100%" }}
                  totalCount={total}
                  defaultItemHeight={54}
                  rangeChanged={(range) => schedule(viewIndex, range)}
                  scrollSeekConfiguration={{ enter: (velocity) => velocity > 1000, exit: (velocity) => velocity < 100 }}
                  itemContent={(index) => {
                    const row = loadedAt(windows, index);
                    return row === undefined ? <FileRowPlaceholder /> : view.render(row);
                  }}
                />
              )}
              {!loading && !error && total === 0 && <div className="job-results-empty">{view.emptyLabel}</div>}
            </div>
          </DialogContent>
          <DialogActions>
            <Button onClick={close}>Close</Button>
          </DialogActions>
        </Dialog>
      )}
    </>
  );
};
