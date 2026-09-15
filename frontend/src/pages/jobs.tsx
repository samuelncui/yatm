import { createContext, memo, useCallback, useEffect, useEffectEvent, useLayoutEffect, useRef, useState } from "react";

import Box from "@mui/material/Box";
import Button from "@mui/material/Button";
import LinearProgress from "@mui/material/LinearProgress";
import Alert from "@mui/material/Alert";
import ArrowBackRoundedIcon from "@mui/icons-material/ArrowBackRounded";
import { useParams, useLocation, useSearchParams, Link } from "react-router";
import { RpcError } from "@protobuf-ts/runtime-rpc";

import { jobCli } from "@/api";
import { Job, JobKind, type JobFilter } from "@/entity";
import { JobFilters, jobFilterFromParams } from "@/components/job-filters";

import { JobCard, jobLabel } from "@/components/job-card";
import { contentTime } from "@/components/content-status";
import { errorMessage } from "@/tools";
import { ArchiveCard } from "@/components/job-archive";
import { RestoreCard } from "@/components/job-restore";
import { ScanCard } from "@/components/job-scan";
import { useJobListState } from "@/components/job-list-state";
import { jobListPath } from "@/pages/routes";

export const RefreshContext = createContext<() => Promise<void>>(async () => {});

const catalogPageSize = 20n;
const changePageSize = 100n;

const mergeJobs = (current: Job[], changed: Job[]) => {
  const byID = new Map(current.map((job) => [job.id, job]));
  for (const job of changed) {
    if ((byID.get(job.id)?.revision ?? -1n) > job.revision) continue;
    // Retain tombstones so an older in-flight snapshot cannot resurrect a Job.
    byID.set(job.id, job);
  }
  return Array.from(byID.values()).sort((a, b) => {
    if (a.createdAtMs !== b.createdAtMs) return a.createdAtMs > b.createdAtMs ? -1 : 1;
    if (a.id === b.id) return 0;
    return a.id > b.id ? -1 : 1;
  });
};

export const JobsBrowser = () => {
  const route = useParams();
  const [params] = useSearchParams();
  const id = route.id ?? "";
  if (/^[1-9]\d*$/.test(id)) return <FocusedJob key={id} id={BigInt(id)} />;
  const filter = jobFilterFromParams(params);
  const filterKey = JSON.stringify(filter, (_, value) => (typeof value === "bigint" ? value.toString() : value));
  return <AllJobsBrowser key={filterKey} filter={filter} filterKey={filterKey} />;
};

const FocusedJob = ({ id }: { id: bigint }) => {
  const location = useLocation();
  const returnTo = jobListPath(location.state?.returnTo);
  const [job, setJob] = useState<Job>();
  const [error, setError] = useState("");
  const request = useRef(0);
  const refresh = useCallback(async () => {
    const sequence = ++request.current;
    try {
      const reply = await jobCli.get({ id }).response;
      if (sequence !== request.current) return;
      if (!reply.job || reply.job.deletedAtMs > 0n) {
        setJob(undefined);
        throw new Error("This job no longer exists. Your Library files and backup copies are not deleted with it.");
      }
      setJob(reply.job);
      setError("");
    } catch (error) {
      if (sequence !== request.current) return;
      if (error instanceof RpcError && error.code === "NOT_FOUND") setJob(undefined);
      setError(errorMessage(error, "Could not load this job"));
    }
  }, [id]);
  useEffect(() => {
    let active = true;
    let timer: ReturnType<typeof setTimeout>;
    const pending = request;
    const poll = async () => {
      await refresh();
      if (active) timer = setTimeout(() => void poll(), 2000);
    };
    void poll();
    return () => {
      active = false;
      pending.current++;
      clearTimeout(timer);
    };
  }, [refresh]);
  return (
    <RefreshContext.Provider value={refresh}>
      <Box className="browser-box focused-job-page">
        <header className="product-page-heading">
          <div>
            <Link className="job-back-link" to={returnTo}>
              <ArrowBackRoundedIcon fontSize="small" />
              Jobs
            </Link>
            <h1>{job ? jobLabel(job.kind) : "Job"} details</h1>
            {job ? <p>Created {contentTime(job.createdAtMs)}</p> : !error && <p>Loading your job…</p>}
          </div>
          {typeof location.state?.returnTo === "string" && /^\/settings\/locations\/[1-9]\d*\?tab=jobs$/.test(location.state.returnTo) && (
            <Button component={Link} to={location.state.returnTo}>
              Back to Location
            </Button>
          )}
        </header>
        {error && (
          <Alert severity="error">
            {error}
            <Button onClick={() => void refresh()}>Retry</Button>
          </Alert>
        )}
        {job ? (
          <div className="job-list">
            <GetJobCard job={job} />
          </div>
        ) : (
          !error && <LinearProgress />
        )}
      </Box>
    </RefreshContext.Provider>
  );
};

const AllJobsBrowser = ({ filter, filterKey }: { filter: JobFilter; filterKey: string }) => {
  const [saved, saveSnapshot] = useJobListState(filterKey);
  const [query] = useState(filter);
  const [initial] = useState(saved);
  const [jobs, setJobs] = useState<Job[] | null>(initial?.jobs ?? null);
  const [loadingMore, setLoadingMore] = useState(false);
  const [loadMoreFailed, setLoadMoreFailed] = useState(false);
  const [catalogError, setCatalogError] = useState("");
  const [canLoadMore, setCanLoadMore] = useState(initial?.hasMore ?? false);
  const revision = useRef(initial?.revision ?? 0n);
  const snapshotRevision = useRef(initial?.snapshotRevision ?? 0n);
  const beforeID = useRef(initial?.beforeID);
  const hasMore = useRef(initial?.hasMore ?? false);
  const loadingMoreRef = useRef(false);
  const refreshing = useRef(false);
  const endRef = useRef<HTMLDivElement>(null);
  const listRef = useRef<HTMLDivElement>(null);
  useLayoutEffect(() => {
    if (listRef.current) listRef.current.scrollTop = initial?.scrollTop ?? 0;
  }, [initial]);
  useEffect(() => {
    if (jobs)
      saveSnapshot({
        jobs,
        revision: revision.current,
        snapshotRevision: snapshotRevision.current,
        beforeID: beforeID.current,
        hasMore: hasMore.current,
        scrollTop: listRef.current?.scrollTop ?? 0,
      });
  }, [jobs, saveSnapshot]);

  const loadJobs = useCallback(async () => {
    const reply = await jobCli.list({ filter: { ...query, limit: catalogPageSize } }).response;
    revision.current = reply.revision;
    snapshotRevision.current = reply.revision;
    beforeID.current = reply.jobs.at(-1)?.id;
    hasMore.current = reply.hasMore;
    setCanLoadMore(reply.hasMore);
    setLoadMoreFailed(false);
    setJobs(reply.jobs);
  }, [query]);

  const loadMore = useCallback(async () => {
    if (!hasMore.current || loadingMoreRef.current || beforeID.current === undefined) return;

    loadingMoreRef.current = true;
    setLoadingMore(true);
    try {
      const reply = await jobCli.list({
        filter: {
          ...query,
          limit: catalogPageSize,
          snapshotRevision: snapshotRevision.current,
          beforeId: beforeID.current,
        },
      }).response;
      beforeID.current = reply.jobs.at(-1)?.id;
      hasMore.current = reply.hasMore;
      setCanLoadMore(reply.hasMore);
      setLoadMoreFailed(false);
      setJobs((current) => mergeJobs(current ?? [], reply.jobs));
    } catch (error) {
      console.error("load more jobs failed", error);
      setLoadMoreFailed(true);
    } finally {
      loadingMoreRef.current = false;
      setLoadingMore(false);
    }
  }, [query]);

  const refresh = useCallback(async () => {
    let hasMoreChanges = true;
    while (hasMoreChanges) {
      const reply = await jobCli.list({
        filter: { ...query, status: undefined, changedAfterRevision: revision.current, limit: changePageSize },
      }).response;
      revision.current = reply.revision;
      hasMoreChanges = reply.hasMore;
      if (reply.jobs.length > 0) {
        setJobs((current) => mergeJobs(current ?? [], reply.jobs));
      }
    }
  }, [query]);
  const updateCatalog = useCallback(async () => {
    if (refreshing.current) return;
    refreshing.current = true;
    try {
      if (jobs === null) await loadJobs();
      else await refresh();
      setCatalogError("");
    } catch (error) {
      setCatalogError(errorMessage(error, "Could not load jobs"));
    } finally {
      refreshing.current = false;
    }
  }, [jobs, loadJobs, refresh]);
  const loadMoreEvent = useEffectEvent(loadMore);
  const updateCatalogEvent = useEffectEvent(updateCatalog);

  useEffect(() => {
    let cancelled = false;
    let timer: ReturnType<typeof setTimeout> | undefined;

    const poll = async () => {
      await updateCatalogEvent();
      if (cancelled) return;
      timer = setTimeout(() => void poll(), 2000);
    };
    void poll();

    return () => {
      cancelled = true;
      if (timer) clearTimeout(timer);
    };
  }, []);

  useEffect(() => {
    const target = endRef.current;
    if (!target || !canLoadMore || loadingMore || loadMoreFailed) return;

    const observer = new IntersectionObserver(
      (entries) => {
        if (entries[0]?.isIntersecting) void loadMoreEvent();
      },
      { rootMargin: "200px" },
    );
    observer.observe(target);
    return () => observer.disconnect();
  }, [canLoadMore, loadingMore, loadMoreFailed]);

  return (
    <RefreshContext.Provider value={updateCatalog}>
      <Box className="browser-box">
        <JobFilters />
        <div
          className="job-list"
          aria-label="Jobs"
          ref={listRef}
          onScroll={(event) => {
            const scrollTop = event.currentTarget.scrollTop;
            saveSnapshot((current) => (current ? { ...current, scrollTop } : current));
          }}
        >
          {catalogError && (
            <Alert severity="error">
              {catalogError}
              <Button onClick={() => void updateCatalog()}>Retry</Button>
            </Alert>
          )}
          {jobs
            ? jobs
                .filter((job) => job.deletedAtMs === 0n && (query.status === undefined || job.status === query.status))
                .map((job) => <GetJobCard job={job} key={job.id.toString()} />)
            : !catalogError && <LinearProgress />}
          <div className="job-list-end" ref={endRef}>
            {loadingMore && <LinearProgress />}
            {loadMoreFailed && (
              <Button size="small" onClick={() => void loadMore()}>
                Retry loading more jobs
              </Button>
            )}
          </div>
        </div>
      </Box>
    </RefreshContext.Provider>
  );
};

const GetJobCard = memo(({ job }: { job: Job }) => {
  switch (job.kind) {
    case JobKind.ARCHIVE:
      return <ArchiveCard job={job} />;
    case JobKind.RESTORE:
      return <RestoreCard job={job} />;
    case JobKind.SCAN:
      return <ScanCard job={job} />;
    default:
      return <JobCard job={job} />;
  }
});
