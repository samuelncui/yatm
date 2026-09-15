import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router";

import { Job, JobKind, JobPhase, JobStatus, Progress } from "@/entity";
import { IndexingActions, JobCard, jobProgressFields } from "@/components/job-card";
import { RefreshContext } from "@/pages/jobs";

const { retryIndex, deleteJob, toastError } = vi.hoisted(() => ({ retryIndex: vi.fn(), deleteJob: vi.fn(), toastError: vi.fn() }));
vi.mock("@/api", () => ({ jobCli: { retryIndex, delete: deleteJob } }));
vi.mock("react-toastify", () => ({ toast: { error: toastError } }));
vi.mock("@/tools", async (original) => ({
  ...(await original<typeof import("@/tools")>()),
  useSharedIntersectionObserver: () => false,
}));
vi.mock("@/pages/jobs", async () => {
  const { createContext } = await import("react");
  return { RefreshContext: createContext(async () => {}) };
});
beforeEach(() => {
  vi.clearAllMocks();
  retryIndex.mockReturnValue({ response: Promise.resolve({}) });
  deleteJob.mockReturnValue({ response: Promise.resolve({}) });
});

describe("Job confirmations", () => {
  it("reviews retry against its target and cancels without execution", async () => {
    const job = Job.create({ id: 7n, kind: JobKind.SCAN, status: JobStatus.INDEXING, phase: JobPhase.WAITING_FOR_INDEX_RETRY, targetName: "Photos" });
    render(<IndexingActions job={job} retryLabel="Retry scan" />);
    await userEvent.click(screen.getByRole("button", { name: "Retry scan" }));
    const dialog = screen.getByRole("dialog", { name: "Retry scan?" });
    expect(dialog).toHaveTextContent("Scan · Photos · Job 7");
    await userEvent.click(within(dialog).getByRole("button", { name: "Cancel" }));
    expect(retryIndex).not.toHaveBeenCalled();
    await userEvent.click(screen.getByRole("button", { name: "Retry scan" }));
    await userEvent.click(screen.getByRole("button", { name: "Retry" }));
    expect(retryIndex).toHaveBeenCalledExactlyOnceWith({ id: 7n });
  });

  it("requires confirmation for deletion and does not offer another submission when only refresh fails", async () => {
    const refresh = vi.fn().mockRejectedValue(new Error("List offline"));
    render(
      <MemoryRouter>
        <RefreshContext.Provider value={refresh}>
          <JobCard job={Job.create({ id: 9n, kind: JobKind.SCAN, status: JobStatus.COMPLETED })} />
        </RefreshContext.Provider>
      </MemoryRouter>,
    );
    await userEvent.click(screen.getByRole("button", { name: "Delete Job" }));
    expect(screen.getByRole("dialog", { name: "Delete Job 9?" })).toHaveTextContent("Library files and archive copies are kept.");
    expect(deleteJob).not.toHaveBeenCalled();
    await userEvent.click(screen.getByRole("button", { name: "Delete" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(deleteJob).toHaveBeenCalledExactlyOnceWith({ ids: [9n] });
    expect(toastError).toHaveBeenCalledWith("List offline");
  });
});

describe("jobProgressFields", () => {
  it("estimates remaining time from historical throughput", () => {
    const job = Job.create({ status: JobStatus.PENDING, phase: JobPhase.WAITING_FOR_MEDIA });
    const progress = Progress.create({
      copiedBytes: 400n,
      copiedFiles: 4n,
      totalBytes: 1000n,
      totalFiles: 10n,
      historicalAverageSpeed: 100n,
    });

    const [fields, percentage] = jobProgressFields(job, progress);

    expect(fields.find((field) => field.name === "Estimated Remaining")).toMatchObject({
      value: "0:06",
      help: expect.stringContaining("historical"),
    });
    expect(percentage).toBe(40);
  });
});
