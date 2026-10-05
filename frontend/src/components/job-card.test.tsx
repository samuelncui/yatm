import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Button } from "@mui/material";
import { MemoryRouter } from "react-router";

import { Job, JobKind, JobPhase, JobStatus } from "@/entity";
import { JobCard, jobStatusLabel } from "@/components/job-card";
import { RefreshContext } from "@/pages/jobs";

const { deleteJob, toastError } = vi.hoisted(() => ({ deleteJob: vi.fn(), toastError: vi.fn() }));
vi.mock("@/api", () => ({ jobCli: { delete: deleteJob } }));
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
  deleteJob.mockReturnValue({ response: Promise.resolve({}) });
});

describe("Job status labels", () => {
  const label = (value: Parameters<typeof Job.create>[0]) => jobStatusLabel(Job.create(value));

  it("names a settled attempt from the single status and its reason", () => {
    expect(label({ kind: JobKind.SCAN, status: JobStatus.FAILED, error: "injected failure" })).toBe("Failed");
    expect(label({ kind: JobKind.RESTORE, status: JobStatus.FAILED, error: "Cancelled by the operator" })).toBe("Failed");
  });

  it("names a Job with no live phase by its durable state", () => {
    expect(label({ kind: JobKind.SCAN, status: JobStatus.PREPARING, phase: JobPhase.UNSPECIFIED })).toBe("Preparing");
    expect(label({ kind: JobKind.SCAN, status: JobStatus.COMPLETED, phase: JobPhase.UNSPECIFIED })).toBe("Completed");
  });

  it("names the Media an idle ready Job waits for", () => {
    expect(label({ kind: JobKind.ARCHIVE, status: JobStatus.READY, phase: JobPhase.UNSPECIFIED })).toBe("Choose archive storage");
    expect(label({ kind: JobKind.RESTORE, status: JobStatus.READY, phase: JobPhase.UNSPECIFIED })).toBe("Choose archive storage to read");
    expect(label({ kind: JobKind.SCAN, status: JobStatus.READY, phase: JobPhase.UNSPECIFIED })).toBe("Choose Media to read");
  });

  it("names what a running Job is doing from its live phase", () => {
    expect(label({ kind: JobKind.SCAN, status: JobStatus.PREPARING, phase: JobPhase.PROCESSING_CONTENT })).toBe("processing content");
    expect(label({ kind: JobKind.ARCHIVE, status: JobStatus.READY, phase: JobPhase.COPYING_TO_MEDIA })).toBe("copying to media");
    expect(label({ kind: JobKind.ARCHIVE, status: JobStatus.READY, phase: JobPhase.QUEUED })).toBe("Queued — waiting for Media");
  });

  it("never shows a settled failure without a reason", () => {
    render(
      <MemoryRouter>
        <JobCard job={Job.create({ id: 9n, kind: JobKind.SCAN, status: JobStatus.FAILED })} />
      </MemoryRouter>,
    );
    expect(screen.getByRole("alert")).toHaveTextContent("The attempt recorded no reason.");
  });
});

describe("Job confirmations", () => {
  it("keeps every immediate action in one non-shrinking scroll row", () => {
    render(
      <MemoryRouter>
        <JobCard job={Job.create({ id: 8n, kind: JobKind.SCAN })} buttons={<Button>Review results</Button>} />
      </MemoryRouter>,
    );
    const row = screen.getByRole("button", { name: "Review results" }).parentElement;
    expect(screen.getByRole("button", { name: "View Log" }).parentElement).toBe(row);
    expect(screen.getByRole("button", { name: "Delete Job" }).parentElement).toBe(row);
    expect(row).toHaveStyle({ overflowX: "auto", flexWrap: "nowrap" });
    expect(screen.getByRole("button", { name: "Review results" })).toHaveStyle({ flexShrink: "0" });
  });

  it.each([JobPhase.QUEUED, JobPhase.COPYING_TO_MEDIA])("does not offer deletion during an admitted attempt at phase %s", (phase) => {
    render(
      <MemoryRouter>
        <JobCard job={Job.create({ id: 8n, kind: JobKind.ARCHIVE, status: JobStatus.READY, phase })} />
      </MemoryRouter>,
    );
    expect(screen.queryByRole("button", { name: "Delete Job" })).not.toBeInTheDocument();
    expect(deleteJob).not.toHaveBeenCalled();
  });

  it("keeps offscreen card bodies mounted without requiring progress polling", () => {
    const visibility = vi.fn();
    render(
      <MemoryRouter>
        <JobCard job={Job.create({ id: 8n, kind: JobKind.SCAN })} visible={false} onVisibilityChange={visibility} detail={<div>Stable metrics</div>} />
      </MemoryRouter>,
    );
    expect(screen.getByText("Stable metrics").parentElement).toHaveClass("job-card-body");
    expect(visibility).toHaveBeenCalledWith(false);
  });

  it("uses the shared body wrapper for consistent title-to-progress spacing", () => {
    render(
      <MemoryRouter>
        <JobCard job={Job.create({ id: 8n, kind: JobKind.SCAN, status: JobStatus.COMPLETED })} visible detail={<div>Job details</div>} />
      </MemoryRouter>,
    );

    expect(screen.getByText("Job details").parentElement).toHaveClass("job-card-body");
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
    expect(deleteJob).toHaveBeenCalledExactlyOnceWith({ ids: [9n], dryrun: false });
    expect(toastError).toHaveBeenCalledWith("List offline");
  });
});

it.each([
  [JobKind.ARCHIVE, "archive"],
  [JobKind.RESTORE, "restore"],
  [JobKind.SCAN, "scan"],
] as const)("links Job kind %s to its normal creation form without reading inputs on the card", (kind, route) => {
  render(
    <MemoryRouter>
      <JobCard job={Job.create({ id: 21n, kind, status: JobStatus.FAILED })} />
    </MemoryRouter>,
  );
  expect(screen.getByRole("link", { name: "Recreate" })).toHaveAttribute("href", `/${route}?recreate=21`);
  expect(deleteJob).not.toHaveBeenCalled();
});
