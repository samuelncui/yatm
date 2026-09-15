import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Routes, Route, Link } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { RpcError } from "@protobuf-ts/runtime-rpc";

import { Job, JobKind, JobPhase, JobStatus } from "@/entity";
import type { ListJobsReply, ListJobsRequest } from "@/entity";

const { listJobs, getJob } = vi.hoisted(() => ({ listJobs: vi.fn(), getJob: vi.fn() }));

vi.mock("@/api", () => ({ jobCli: { list: listJobs, get: getJob } }));
vi.mock("@/components/job-card", () => ({
  jobLabel: () => "Backup",
  JobCard: ({ job }: { job: Job }) => <div data-testid="job-card">{`job-${job.id}-${job.revision}-${job.status}`}</div>,
}));
vi.mock("@/components/job-archive", () => ({
  ArchiveCard: ({ job }: { job: Job }) => (
    <div data-testid="job-card">
      <Link to={`/jobs/${job.id}`}>{`job-${job.id}-${job.revision}-${job.status}`}</Link>
    </div>
  ),
}));
vi.mock("@/components/job-restore", () => ({
  RestoreCard: ({ job }: { job: Job }) => <div data-testid="job-card">{`job-${job.id}-${job.revision}-${job.status}`}</div>,
}));
vi.mock("@/components/job-preview", () => ({
  PreviewCard: ({ job }: { job: Job }) => <div data-testid="job-card">{`job-${job.id}-${job.revision}-${job.status}`}</div>,
}));

import { JobsBrowser } from "@/pages/jobs";
import { JobListStateProvider } from "@/components/job-list-state";

type ObserverEntry = Pick<IntersectionObserverEntry, "isIntersecting" | "target">;

class IntersectionObserverStub implements IntersectionObserver {
  static instances: IntersectionObserverStub[] = [];

  readonly root = null;
  readonly rootMargin = "0px";
  readonly scrollMargin = "0px";
  readonly thresholds = [0];
  private target: Element | null = null;

  constructor(private readonly callback: IntersectionObserverCallback) {
    IntersectionObserverStub.instances.push(this);
  }

  observe(target: Element) {
    this.target = target;
  }

  unobserve() {}
  disconnect() {}
  takeRecords() {
    return [];
  }

  trigger(isIntersecting: boolean) {
    if (!this.target) throw new Error("observer target is missing");
    this.callback([{ isIntersecting, target: this.target } as ObserverEntry as IntersectionObserverEntry], this);
  }
}

const call = (reply: ListJobsReply) => ({ response: Promise.resolve(reply) });
const failedCall = (error: Error) => ({ response: Promise.reject(error) });
const job = (id: bigint, createdAtMs: bigint, revision: bigint, status = JobStatus.PENDING) =>
  Job.create({
    id,
    createdAtMs,
    updatedAtMs: createdAtMs,
    revision,
    status,
    kind: JobKind.ARCHIVE,
    phase: JobPhase.WAITING_FOR_MEDIA,
  });

beforeEach(() => {
  listJobs.mockReset();
  getJob.mockReset();
  IntersectionObserverStub.instances = [];
  globalThis.IntersectionObserver = IntersectionObserverStub;
});

afterEach(() => {
  vi.useRealTimers();
  vi.restoreAllMocks();
});

describe("JobsBrowser", () => {
  it("filters before pagination and removes a Job after it leaves the selected durable status", async () => {
    vi.useFakeTimers();
    listJobs.mockImplementation(({ filter }: ListJobsRequest) => {
      if (filter?.changedAfterRevision) return call({ jobs: [job(1n, 100n, 2n, JobStatus.COMPLETED)], revision: 2n, hasMore: false });
      return call({ jobs: [job(1n, 100n, 1n)], revision: 1n, hasMore: false });
    });
    render(
      <MemoryRouter initialEntries={[`/jobs?kind=${JobKind.ARCHIVE}&status=${JobStatus.PENDING}&location=8`]}>
        <JobsBrowser />
      </MemoryRouter>,
    );
    await act(async () => Promise.resolve());
    expect(listJobs).toHaveBeenCalledWith({ filter: { kind: JobKind.ARCHIVE, status: JobStatus.PENDING, locationId: 8n, limit: 20n } });
    expect(screen.getByTestId("job-card")).toBeInTheDocument();
    await act(async () => vi.advanceTimersByTimeAsync(2000));
    expect(listJobs).toHaveBeenLastCalledWith({ filter: { kind: JobKind.ARCHIVE, status: undefined, locationId: 8n, changedAfterRevision: 1n, limit: 100n } });
    expect(screen.queryByTestId("job-card")).not.toBeInTheDocument();
  });
  it("reports an initial catalog failure instead of showing a perpetual loading indicator", async () => {
    vi.spyOn(console, "error").mockImplementation(() => {});
    listJobs.mockImplementation(() => failedCall(new RpcError("Catalog unavailable", "UNAVAILABLE")));
    render(
      <MemoryRouter>
        <JobsBrowser />
      </MemoryRouter>,
    );
    expect(await screen.findByRole("alert")).toHaveTextContent("Catalog unavailable");
    expect(screen.queryByRole("progressbar")).not.toBeInTheDocument();
    listJobs.mockImplementation(() => call({ jobs: [job(1n, 100n, 1n)], revision: 1n, hasMore: false }));
    await userEvent.click(screen.getByRole("button", { name: "Retry" }));
    await screen.findByTestId("job-card");
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("keeps cached cards visible when a change-feed refresh fails and recovers on the next poll", async () => {
    vi.useFakeTimers();
    listJobs.mockImplementationOnce(() => call({ jobs: [job(1n, 100n, 1n)], revision: 1n, hasMore: false }));
    listJobs.mockImplementationOnce(() => failedCall(new RpcError("Catalog unavailable", "UNAVAILABLE")));
    listJobs.mockImplementation(() => call({ jobs: [job(1n, 100n, 2n, JobStatus.COMPLETED)], revision: 2n, hasMore: false }));
    render(
      <MemoryRouter>
        <JobsBrowser />
      </MemoryRouter>,
    );
    await act(async () => Promise.resolve());
    await act(async () => vi.advanceTimersByTimeAsync(2000));
    expect(screen.getByRole("alert")).toHaveTextContent("Catalog unavailable");
    expect(screen.getByTestId("job-card")).toHaveTextContent("job-1-1-2");
    await act(async () => vi.advanceTimersByTimeAsync(2000));
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    expect(screen.getByTestId("job-card")).toHaveTextContent("job-1-2-4");
  });

  it.each([false, true])("does not overwrite a newer change with an in-flight snapshot page (deleted=%s)", async (deleted) => {
    vi.useFakeTimers();
    let resolvePage!: (value: ListJobsReply) => void;
    const page = new Promise<ListJobsReply>((resolve) => {
      resolvePage = resolve;
    });
    const changed = job(1n, 100n, 11n, JobStatus.COMPLETED);
    if (deleted) changed.deletedAtMs = 1n;
    listJobs.mockImplementation(({ filter }: ListJobsRequest) => {
      if (filter?.snapshotRevision) return { response: page };
      if (filter?.changedAfterRevision) return call({ jobs: [changed], revision: 11n, hasMore: false });
      return call({ jobs: [job(2n, 200n, 9n)], revision: 10n, hasMore: true });
    });
    render(
      <MemoryRouter>
        <JobsBrowser />
      </MemoryRouter>,
    );
    await act(async () => Promise.resolve());
    act(() => IntersectionObserverStub.instances[0].trigger(true));
    await act(async () => vi.advanceTimersByTimeAsync(2000));
    await act(async () => resolvePage({ jobs: [job(1n, 100n, 8n)], revision: 10n, hasMore: false }));
    expect(screen.queryByText("job-1-8-2")).not.toBeInTheDocument();
    if (!deleted) expect(screen.getByText("job-1-11-4")).toBeInTheDocument();
  });

  it.each([false, true])("removes a focused job's stale card once deletion is confirmed (rpc=%s)", async (rpc) => {
    vi.useFakeTimers();
    getJob.mockReturnValueOnce({ response: Promise.resolve({ job: job(42n, 100n, 9n) }) }).mockImplementation(() => ({
      response: rpc ? Promise.reject(new RpcError("This job no longer exists", "NOT_FOUND")) : Promise.resolve({}),
    }));
    render(
      <MemoryRouter initialEntries={["/jobs/42"]}>
        <Routes>
          <Route path="/jobs/:id" element={<JobsBrowser />} />
        </Routes>
      </MemoryRouter>,
    );
    await act(async () => Promise.resolve());
    expect(screen.getByText("job-42-9-2")).toBeInTheDocument();
    await act(async () => vi.advanceTimersByTimeAsync(2000));
    expect(screen.getByText(/This job no longer exists/)).toBeInTheDocument();
    expect(screen.queryByTestId("job-card")).not.toBeInTheDocument();
    expect(screen.queryByText("Loading your job…")).not.toBeInTheDocument();
  });

  it("returns to loaded snapshot pages and scroll without resetting the list", async () => {
    listJobs
      .mockReturnValueOnce(call({ jobs: [job(3n, 300n, 9n), job(2n, 200n, 8n)], revision: 9n, hasMore: true }))
      .mockReturnValueOnce(call({ jobs: [job(1n, 100n, 7n)], revision: 9n, hasMore: false }))
      .mockReturnValue(call({ jobs: [], revision: 9n, hasMore: false }));
    getJob.mockReturnValue({ response: Promise.resolve({ job: job(1n, 100n, 7n) }) });
    render(
      <MemoryRouter initialEntries={["/jobs"]}>
        <JobListStateProvider>
          <Routes>
            <Route path="/jobs" element={<JobsBrowser />} />
            <Route path="/jobs/:id" element={<JobsBrowser />} />
          </Routes>
        </JobListStateProvider>
      </MemoryRouter>,
    );
    await screen.findByText("job-2-8-2");
    await waitFor(() => expect(IntersectionObserverStub.instances).toHaveLength(1));
    act(() => IntersectionObserverStub.instances[0].trigger(true));
    await screen.findByText("job-1-7-2");
    fireEvent.scroll(screen.getByLabelText("Jobs"), { target: { scrollTop: 240 } });
    await userEvent.click(screen.getByRole("link", { name: "job-1-7-2" }));
    await userEvent.click(await screen.findByRole("link", { name: "Jobs" }));
    expect(await screen.findByText("job-3-9-2")).toBeInTheDocument();
    expect(screen.getByText("job-1-7-2")).toBeInTheDocument();
    expect(screen.getByLabelText("Jobs").scrollTop).toBe(240);
    expect(
      listJobs.mock.calls.filter(([request]) => request.filter?.snapshotRevision === undefined && request.filter?.changedAfterRevision === undefined),
    ).toHaveLength(1);
  });
  it("opens a created job directly instead of requiring it to appear on the first list page", async () => {
    getJob.mockReturnValue({ response: Promise.resolve({ job: job(42n, 100n, 9n) }) });
    render(
      <MemoryRouter initialEntries={["/jobs/42"]}>
        <Routes>
          <Route path="*" element={<JobsBrowser />} />
          <Route path="/jobs/:id" element={<JobsBrowser />} />
        </Routes>
      </MemoryRouter>,
    );
    expect(await screen.findByText("job-42-9-2")).toBeInTheDocument();
    expect(getJob).toHaveBeenCalledWith({ id: 42n });
    expect(listJobs).not.toHaveBeenCalled();
    expect(screen.getByRole("link", { name: "Jobs" })).toHaveAttribute("href", "/jobs");
    expect(screen.queryByRole("link", { name: "All jobs" })).not.toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Library files" })).not.toBeInTheDocument();
  });

  it.each(["/jobs?kind=1&location=8", "/settings/locations/8?tab=jobs", "https://example.com/jobs"])(
    "uses a list-only destination for the Jobs breadcrumb (origin=%s)",
    async (returnTo) => {
      getJob.mockReturnValue({ response: Promise.resolve({ job: job(42n, 100n, 9n) }) });
      render(
        <MemoryRouter initialEntries={[{ pathname: "/jobs/42", state: { returnTo } }]}>
          <Routes>
            <Route path="/jobs/:id" element={<JobsBrowser />} />
          </Routes>
        </MemoryRouter>,
      );
      const back = await screen.findByRole("link", { name: "Jobs" });
      expect(back).toHaveAttribute("href", returnTo.startsWith("/jobs?") ? returnTo : "/jobs");
      expect(back).toContainElement(screen.getByTestId("ArrowBackRoundedIcon"));
    },
  );

  it("shows a retryable error for a missing focused job", async () => {
    getJob.mockReturnValue({ response: Promise.resolve({}) });
    render(
      <MemoryRouter initialEntries={["/jobs/42"]}>
        <Routes>
          <Route path="*" element={<JobsBrowser />} />
          <Route path="/jobs/:id" element={<JobsBrowser />} />
        </Routes>
      </MemoryRouter>,
    );
    expect(await screen.findByText(/This job no longer exists/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Retry" })).toBeInTheDocument();
  });

  it("loads the initial Job page", async () => {
    listJobs.mockReturnValueOnce(call({ jobs: [job(2n, 200n, 5n), job(1n, 100n, 4n)], revision: 5n, hasMore: false }));

    render(
      <MemoryRouter>
        <Routes>
          <Route path="*" element={<JobsBrowser />} />
          <Route path="/jobs/:id" element={<JobsBrowser />} />
        </Routes>
      </MemoryRouter>,
    );

    expect(await screen.findByText("job-2-5-2")).toBeInTheDocument();
    expect(screen.getByText("job-1-4-2")).toBeInTheDocument();
    expect(listJobs).toHaveBeenCalledWith({ filter: { limit: 20n } });
  });

  it("loads the next snapshot page when the end becomes visible", async () => {
    listJobs
      .mockReturnValueOnce(call({ jobs: [job(3n, 300n, 9n), job(2n, 200n, 8n)], revision: 9n, hasMore: true }))
      .mockReturnValueOnce(call({ jobs: [job(1n, 100n, 7n)], revision: 9n, hasMore: false }));

    render(
      <MemoryRouter>
        <Routes>
          <Route path="*" element={<JobsBrowser />} />
          <Route path="/jobs/:id" element={<JobsBrowser />} />
        </Routes>
      </MemoryRouter>,
    );
    expect(await screen.findByText("job-2-8-2")).toBeInTheDocument();
    await waitFor(() => expect(IntersectionObserverStub.instances).toHaveLength(1));

    act(() => IntersectionObserverStub.instances[0].trigger(true));

    expect(await screen.findByText("job-1-7-2")).toBeInTheDocument();
    expect(listJobs).toHaveBeenNthCalledWith(2, {
      filter: { limit: 20n, snapshotRevision: 9n, beforeId: 2n },
    });
  });

  it("requires a manual retry after pagination fails", async () => {
    vi.spyOn(console, "error").mockImplementation(() => {});
    listJobs
      .mockReturnValueOnce(call({ jobs: [job(2n, 200n, 6n)], revision: 6n, hasMore: true }))
      .mockImplementationOnce(() => failedCall(new Error("page unavailable")))
      .mockReturnValueOnce(call({ jobs: [job(1n, 100n, 5n)], revision: 6n, hasMore: false }));

    render(
      <MemoryRouter>
        <Routes>
          <Route path="*" element={<JobsBrowser />} />
          <Route path="/jobs/:id" element={<JobsBrowser />} />
        </Routes>
      </MemoryRouter>,
    );
    expect(await screen.findByText("job-2-6-2")).toBeInTheDocument();
    await waitFor(() => expect(IntersectionObserverStub.instances).toHaveLength(1));

    act(() => IntersectionObserverStub.instances[0].trigger(true));
    const retry = await screen.findByRole("button", { name: "Retry loading more jobs" });
    expect(listJobs).toHaveBeenCalledTimes(2);

    await userEvent.click(retry);

    expect(await screen.findByText("job-1-5-2")).toBeInTheDocument();
    expect(listJobs).toHaveBeenCalledTimes(3);
  });

  it("merges paged updates and tombstones by revision", async () => {
    vi.useFakeTimers();
    const deleted = job(2n, 200n, 12n);
    deleted.deletedAtMs = 1n;
    listJobs
      .mockReturnValueOnce(call({ jobs: [job(2n, 200n, 9n), job(1n, 100n, 8n)], revision: 10n, hasMore: false }))
      .mockReturnValueOnce(call({ jobs: [job(1n, 100n, 11n, JobStatus.COMPLETED), deleted], revision: 12n, hasMore: true }))
      .mockReturnValueOnce(call({ jobs: [job(3n, 300n, 13n)], revision: 13n, hasMore: false }));

    render(
      <MemoryRouter>
        <Routes>
          <Route path="*" element={<JobsBrowser />} />
          <Route path="/jobs/:id" element={<JobsBrowser />} />
        </Routes>
      </MemoryRouter>,
    );
    await act(async () => Promise.resolve());
    expect(screen.getByText("job-2-9-2")).toBeInTheDocument();

    await act(async () => vi.advanceTimersByTimeAsync(2000));

    expect(screen.queryByText("job-2-9-2")).not.toBeInTheDocument();
    expect(screen.getAllByTestId("job-card").map((element) => element.textContent)).toEqual(["job-3-13-2", "job-1-11-4"]);
    expect((listJobs.mock.calls[1][0] as ListJobsRequest).filter?.changedAfterRevision).toBe(10n);
    expect((listJobs.mock.calls[2][0] as ListJobsRequest).filter?.changedAfterRevision).toBe(12n);
  });
});
