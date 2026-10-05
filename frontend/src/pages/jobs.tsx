import { Feedback } from "@/components/feedback";
import { createContext, memo, useCallback, useEffect, useEffectEvent, useLayoutEffect, useRef, useState } from "react";

import Box from "@mui/material/Box";
import Button from "@mui/material/Button";
import LinearProgress from "@mui/material/LinearProgress";
import { useParams, useLocation, useSearchParams, Link } from "react-router";
import { RpcError } from "@protobuf-ts/runtime-rpc";

import { jobCli } from "@/api";
import { Job, JobKind, type JobFilter } from "@/entity";
import { JobFilters, jobFilterFromParams } from "@/components/job-filters";

import { JobCard, jobLabel } from "@/components/job-card";
import { contentTime } from "@/components/content-status";
import { PageHeading } from "@/components/page-heading";
import { errorMessage } from "@/tools";
import { ArchiveCard } from "@/components/job-archive";
import { RestoreCard } from "@/components/job-restore";
import { ScanCard } from "@/components/job-scan";
import { useJobListState } from "@/components/job-list-state";

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
    if (a.createdAtNs !== b.createdAtNs) return a.createdAtNs > b.createdAtNs ? -1 : 1;
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
  const [job, setJob] = useState<Job>();
  const [error, setError] = useState("");
  const request = useRef(0);
  const refresh = useCallback(async () => {
    const sequence = ++request.current;
    try {
      const reply = await jobCli.get({ id }).response;
      if (sequence !== request.current) return;
      if (!reply.job || reply.job.deletedAtNs !== 0n) {
        setJob(undefined);
        throw new Error("This job no longer exists. Your Library files and archive copies are not deleted with it.");
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
      <Box className="browser-box jobs-page focused-job-page">
        <Box sx={{ pb: 2.5 }}>
          <PageHeading
            title={`${job ? jobLabel(job.kind) : "Job"} details`}
            description={job ? `Created ${contentTime(job.createdAtNs || undefined)}` : error ? undefined : "Loading your job…"}
            actions={
              typeof location.state?.returnTo === "string" && /^\/settings\/locations\/[1-9]\d*\?tab=jobs$/.test(location.state.returnTo) ? (
                <Button component={Link} to={location.state.returnTo}>
                  Back to Location
                </Button>
              ) : undefined
            }
          />
        </Box>
        {error && (
          <Feedback severity="error" action={<Button onClick={() => void refresh()}>Retry</Button>}>
            {error}
          </Feedback>
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
  const { snapshot, save: saveSnapshot, getSnapshot, scroll } = useJobListState(filterKey);
  const [initialScroll] = useState(snapshot?.scrollTop ?? 0);
  const jobs = snapshot?.jobs ?? null;
  const canLoadMore = snapshot?.hasMore ?? false;
  const [loadingMore, setLoadingMore] = useState(false);
  const [loadMoreFailed, setLoadMoreFailed] = useState(false);
  const [catalogError, setCatalogError] = useState("");
  const loadingMoreRef = useRef(false);
  const refreshing = useRef(false);
  const generation = useRef(0);
  const endRef = useRef<HTMLDivElement>(null);
  const listRef = useRef<HTMLDivElement>(null);
  useLayoutEffect(() => {
    if (listRef.current) listRef.current.scrollTop = initialScroll;
  }, [initialScroll]);

  const loadJobs = useCallback(async () => {
    const request = generation.current;
    const reply = await jobCli.list({ filter: { ...filter, limit: catalogPageSize } }).response;
    if (request !== generation.current) return;
    saveSnapshot({
      jobs: reply.jobs,
      revision: reply.revision,
      snapshotRevision: reply.revision,
      beforeID: reply.jobs.at(-1)?.id,
      hasMore: reply.hasMore,
      scrollTop: 0,
    });
    setLoadMoreFailed(false);
  }, [filter, saveSnapshot]);

  const loadMore = useCallback(async () => {
    const current = getSnapshot();
    if (!current?.hasMore || loadingMoreRef.current || current.beforeID === undefined) return;
    const request = generation.current;
    loadingMoreRef.current = true;
    setLoadingMore(true);
    try {
      const reply = await jobCli.list({ filter: { ...filter, limit: catalogPageSize, snapshotRevision: current.snapshotRevision, beforeId: current.beforeID } })
        .response;
      if (request !== generation.current) return;
      saveSnapshot((latest) => latest && { ...latest, jobs: mergeJobs(latest.jobs, reply.jobs), beforeID: reply.jobs.at(-1)?.id, hasMore: reply.hasMore });
      setLoadMoreFailed(false);
    } catch (error) {
      if (request !== generation.current) return;
      console.error("load more jobs failed", error);
      setLoadMoreFailed(true);
    } finally {
      if (request === generation.current) {
        loadingMoreRef.current = false;
        setLoadingMore(false);
      }
    }
  }, [filter, getSnapshot, saveSnapshot]);

  const refresh = useCallback(async () => {
    const request = generation.current;
    let more = true;
    while (more) {
      const current = getSnapshot();
      if (!current || request !== generation.current) return;
      const reply = await jobCli.list({ filter: { ...filter, changedAfterRevision: current.revision, limit: changePageSize } }).response;
      if (request !== generation.current) return;
      saveSnapshot((latest) => latest && { ...latest, revision: reply.revision, jobs: reply.jobs.length ? mergeJobs(latest.jobs, reply.jobs) : latest.jobs });
      more = reply.hasMore;
    }
  }, [filter, getSnapshot, saveSnapshot]);
  const updateCatalog = useCallback(async () => {
    if (refreshing.current) return;
    const request = generation.current;
    refreshing.current = true;
    try {
      if (!getSnapshot()) await loadJobs();
      else await refresh();
      if (request === generation.current) setCatalogError("");
    } catch (error) {
      if (request === generation.current) setCatalogError(errorMessage(error, "Could not load jobs"));
    } finally {
      if (request === generation.current) refreshing.current = false;
    }
  }, [getSnapshot, loadJobs, refresh]);
  const loadMoreEvent = useEffectEvent(loadMore);
  const updateCatalogEvent = useEffectEvent(updateCatalog);

  useEffect(() => {
    let cancelled = false;
    const requests = generation;
    let timer: ReturnType<typeof setTimeout> | undefined;

    const poll = async () => {
      await updateCatalogEvent();
      if (cancelled) return;
      timer = setTimeout(() => void poll(), 2000);
    };
    void poll();

    return () => {
      cancelled = true;
      requests.current++;
      refreshing.current = false;
      loadingMoreRef.current = false;
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
      { root: listRef.current, rootMargin: "200px" },
    );
    observer.observe(target);
    return () => observer.disconnect();
  }, [canLoadMore, loadingMore, loadMoreFailed]);

  return (
    <RefreshContext.Provider value={updateCatalog}>
      <Box className="browser-box jobs-page">
        <JobFilters />
        <div
          className="job-list"
          aria-label="Jobs"
          ref={listRef}
          onScroll={(event) => {
            const scrollTop = event.currentTarget.scrollTop;
            scroll(scrollTop);
          }}
        >
          {catalogError && (
            <Feedback severity="error" action={<Button onClick={() => void updateCatalog()}>Retry</Button>}>
              {catalogError}
            </Feedback>
          )}
          {jobs
            ? jobs.filter((job) => job.deletedAtNs === 0n).map((job) => <GetJobCard job={job} key={job.id.toString()} />)
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
