import type { ReactNode } from "react";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { GetScanJobProgressReply, Job, JobKind, JobPhase, JobStatus, ScanEntry, ScanFinding } from "@/entity";
const { progress, entries, cancel, retry } = vi.hoisted(() => ({ progress: vi.fn(), entries: vi.fn(), cancel: vi.fn(), retry: vi.fn() }));
vi.mock("@/api", () => ({ scanJobCli: { getProgress: progress, listEntries: entries }, jobCli: { cancel, retryIndex: retry } }));
vi.mock("@/pages/jobs", async () => {
  const { createContext } = await import("react");
  return { RefreshContext: createContext(async () => {}) };
});
vi.mock("@/components/job-card", async (original) => {
  const actual = await original<typeof import("@/components/job-card")>();
  const { useEffect } = await import("react");
  return {
    ...actual,
    JobCard: ({ detail, buttons, onVisibilityChange }: { detail?: ReactNode; buttons?: ReactNode; onVisibilityChange?: (visible: boolean) => void }) => {
      useEffect(() => onVisibilityChange?.(true), [onVisibilityChange]);
      return (
        <div>
          {detail}
          {buttons}
        </div>
      );
    },
  };
});
import { ScanCard } from "./job-scan";
const call = (value: unknown) => ({ response: Promise.resolve(value) });
beforeEach(() => {
  vi.clearAllMocks();
  progress.mockReturnValue(call(GetScanJobProgressReply.create({ matched: 1n, damaged: 1n, missing: 1n })));
  entries
    .mockReturnValueOnce(call({ entries: [ScanEntry.create({ id: 1n, path: "damaged.jpg", finding: ScanFinding.MISMATCH })], hasMore: true }))
    .mockReturnValue(call({ entries: [ScanEntry.create({ id: 2n, path: "missing.jpg", finding: ScanFinding.MISSING, stale: true })], hasMore: false }));
  cancel.mockReturnValue(call({}));
  retry.mockReturnValue(call({}));
});
describe("Integrity check Job", () => {
  it("reports unhealthy and stale observations without implying a repair, with paginated results", async () => {
    render(<ScanCard job={Job.create({ id: 4n, kind: JobKind.SCAN, status: JobStatus.COMPLETED })} />);
    expect(await screen.findByText("Content mismatch")).toBeInTheDocument();
    expect(screen.queryByText(/Expected content records have not been changed/)).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Results" }));
    expect(await screen.findByText("damaged.jpg")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Load More" }));
    expect(await screen.findByText("Missing · Observation changed; not published")).toBeInTheDocument();
    expect(entries).toHaveBeenLastCalledWith({ id: 4n, limit: 200, afterId: 1n });
  });
  it("uses the shared cancellation action during verification", async () => {
    render(<ScanCard job={Job.create({ id: 4n, kind: JobKind.SCAN, status: JobStatus.INDEXING, phase: JobPhase.VERIFYING_MEDIA })} />);
    await userEvent.click(screen.getByRole("button", { name: "Cancel" }));
    expect(cancel).not.toHaveBeenCalled();
    await userEvent.click(screen.getByRole("button", { name: "Stop job" }));
    expect(cancel).toHaveBeenCalledWith({ id: 4n });
  });
});
