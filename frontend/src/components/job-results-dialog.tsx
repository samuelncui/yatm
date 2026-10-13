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
import { FileRowPlaceholder, jobResultRowHeight } from "@/components/job-file-list-item";
import { firstUncovered, loadedAt, mergeWindow, planAnchor, resultPageSize as pageSize, type ResultWindow } from "./job-result-window";
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

const listingLoaders: Record<JobItemListing, (jobId: bigint, page: JobItemPageRequest, abort: AbortSignal) => Promise<JobItemPage>> = {
  "scan-entries": async (jobId, page, abort) => {
    const reply = await scanJobCli.listEntries({ id: jobId, ...page }, { abort }).response;
    return { rows: reply.entries, hasMore: reply.hasMore, total: reply.totalEntryCount };
  },
  "archive-files": async (jobId, page, abort) => {
    const reply = await archiveJobCli.listFiles({ id: jobId, filterStatus: [], ...page }, { abort }).response;
    return { rows: reply.items, hasMore: reply.hasMore, total: reply.totalFileCount };
  },
  "restore-files": async (jobId, page, abort) => {
    const reply = await restoreJobCli.listFiles({ id: jobId, filterStatus: [], ...page }, { abort }).response;
    return { rows: reply.items, hasMore: reply.hasMore, total: reply.totalFileCount };
  },
};

export const JobResultsDialog = ({ jobId, title, views }: { jobId: bigint; title: string; views: JobItemViews }) => {
  const [open, setOpen] = useState(false);
  const [viewIndex, setViewIndex] = useState(0);
  const [total, setTotal] = useState(0);
  const [windows, setWindows] = useState<ResultWindow[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");

  const viewsRef = useRef(views);
  const windowsRef = useRef<ResultWindow[]>([]);
  const totalRef = useRef(0);
  const pendingRange = useRef<{ start: number; end: number; includeTotal: boolean } | null>(null);
  const visibleRange = useRef({ start: 0, end: pageSize - 1 });
  const generation = useRef(0);
  const busy = useRef(false);
  const failed = useRef(false);
  const controller = useRef<AbortController | null>(null);

  useEffect(() => {
    viewsRef.current = views;
  }, [views]);

  useEffect(
    () => () => {
      generation.current++;
      controller.current?.abort();
    },
    [],
  );

  const publish = (next: ResultWindow[]) => {
    windowsRef.current = next;
    setWindows(next);
  };

  const fill = async (index: number, start: number, end: number, includeTotal: boolean) => {
    const view = viewsRef.current[index];
    if (!view) return;
    const run = generation.current;
    const viewport = visibleRange.current;
    // Each pass loads one window; a bounded number keeps one scroll burst responsive.
    for (let pass = 0; pass < 4; pass++) {
      const current = windowsRef.current;
      // A requested range may reach past the result set; only real rows are fetched.
      const last = totalRef.current > 0 ? Math.min(end, totalRef.current - 1) : end;
      const target = firstUncovered(current, start, last);
      if (target > last) return;
      const plan = planAnchor(view.cursorOf, current, target);
      let page: JobItemPage;
      try {
        page = await listingLoaders[view.listing](
          jobId,
          {
            limit: pageSize,
            cursor: plan.cursor,
            offset: plan.offset,
            order: plan.backwards ? JobResultOrder.DESCENDING : JobResultOrder.ASCENDING,
            includeTotal: includeTotal && current.length === 0,
          },
          controller.current!.signal,
        );
      } catch (reason) {
        if (viewport !== visibleRange.current) return;
        throw reason;
      }
      if (run !== generation.current) return;
      if (page.total !== undefined) {
        totalRef.current = Number(page.total);
        setTotal(totalRef.current);
      }
      if (page.rows.length === 0) {
        if (viewport !== visibleRange.current) return;
        if (target < totalRef.current) throw new Error("Could not load this window of Job results");
        return;
      }
      const rows = plan.backwards ? [...page.rows].reverse() : page.rows;
      publish(mergeWindow(windowsRef.current, { start: plan.start(page.rows.length), rows }, visibleRange.current.start));
      // A new viewport takes priority over filling the one the user has left.
      if (viewport !== visibleRange.current) return;
    }
  };

  const drain = async (index: number) => {
    if (busy.current) return;
    busy.current = true;
    controller.current = new AbortController();
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
      if (run === generation.current) {
        failed.current = true;
        setError(errorMessage(reason, "Could not load Job results"));
      }
    } finally {
      if (run === generation.current) {
        busy.current = false;
        setLoading(false);
      }
    }
  };

  const schedule = (index: number, range: ListRange, includeTotal = false) => {
    const wanted = { start: Math.max(0, range.startIndex), end: range.endIndex };
    if (wanted.start !== visibleRange.current.start || wanted.end !== visibleRange.current.end) visibleRange.current = wanted;
    if (failed.current) return;
    const end = Math.min(wanted.end, totalRef.current - 1);
    if (!includeTotal && firstUncovered(windowsRef.current, wanted.start, end) > end) {
      pendingRange.current = null;
      setLoading(false);
      return;
    }
    pendingRange.current = { ...wanted, includeTotal };
    setLoading(true);
    void drain(index);
  };

  const reset = (index: number) => {
    generation.current++;
    controller.current?.abort();
    busy.current = false;
    failed.current = false;
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
    controller.current?.abort();
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
                      onClick={() => {
                        failed.current = false;
                        setError("");
                        schedule(viewIndex, { startIndex: visibleRange.current.start, endIndex: visibleRange.current.end }, totalRef.current === 0);
                      }}
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
                  style={{ width: "100%", flex: "1 1 auto", minHeight: 0 }}
                  totalCount={total}
                  fixedItemHeight={jobResultRowHeight}
                  increaseViewportBy={jobResultRowHeight * 10}
                  rangeChanged={(range) => schedule(viewIndex, range)}
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
