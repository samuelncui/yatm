import type { ReactNode } from "react";
import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { Job, JobKind, JobPhase, JobStatus, Progress, ScanChange, ScanEntry, ScanScopeResult } from "@/entity";
const { getProgress, listEntries, listScopes, cancel, retryIndex } = vi.hoisted(() => ({
  getProgress: vi.fn(),
  listEntries: vi.fn(),
  listScopes: vi.fn(),
  cancel: vi.fn(),
  retryIndex: vi.fn(),
}));
vi.mock("@/api", () => ({ scanJobCli: { getProgress, listEntries, listScopes }, jobCli: { cancel, retryIndex } }));
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
  getProgress.mockReturnValue(call({ progress: Progress.create({ totalFiles: 3n, totalBytes: 7n }), added: 1n, changed: 1n, removed: 1n, bytes: 7n }));
  listEntries.mockReturnValue(call({ entries: [ScanEntry.create({ path: "added.txt", change: ScanChange.ADDED, size: 7n })], hasMore: false }));
  cancel.mockReturnValue(call({}));
  retryIndex.mockReturnValue(call({}));
});
describe("Automatic Media Scan", () => {
  it("pages all Location scopes instead of treating the progress sample as complete", async () => {
    getProgress.mockReturnValue(call({ progress: Progress.create(), scopes: [], scopesHasMore: true }));
    listScopes.mockReturnValueOnce(
      call({ scopes: [ScanScopeResult.create({ id: 10n, locationId: 2n, path: "reports", publishedAtMs: 100n })], hasMore: true }),
    );
    listScopes.mockReturnValue(
      call({ scopes: [ScanScopeResult.create({ id: 11n, locationId: 3n, path: "photos", error: "Permission denied" })], hasMore: false }),
    );
    render(<ScanCard job={Job.create({ id: 30n, kind: JobKind.SCAN, status: JobStatus.COMPLETED })} />);
    await userEvent.click(await screen.findByRole("button", { name: "Scanned folders" }));
    expect(await screen.findByText("Location 2 / reports")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Load More" }));
    expect(await screen.findByText("Permission denied")).toBeInTheDocument();
    expect(listScopes).toHaveBeenLastCalledWith({ id: 30n, limit: 200, afterId: 10n });
    expect(listEntries).not.toHaveBeenCalled();
  });
  it("labels Media scopes without inventing a Location zero", async () => {
    getProgress.mockReturnValue(call({ scopes: [ScanScopeResult.create({ id: 1n })] }));
    listScopes.mockReturnValue(call({ scopes: [ScanScopeResult.create({ id: 1n, path: "photos" })], hasMore: false }));
    render(<ScanCard job={Job.create({ id: 30n, mediaId: 9n, kind: JobKind.SCAN, status: JobStatus.COMPLETED })} />);
    await userEvent.click(await screen.findByRole("button", { name: "Scanned folders" }));
    expect(await screen.findByText("Media / photos")).toBeInTheDocument();
    expect(screen.queryByText(/Location 0/)).not.toBeInTheDocument();
  });
  it("can close a loading results dialog and rejects late results on reopening", async () => {
    let finish!: (value: unknown) => void;
    listEntries.mockReturnValueOnce({
      response: new Promise((resolve) => {
        finish = resolve;
      }),
    });
    render(<ScanCard job={Job.create({ id: 30n, kind: JobKind.SCAN, status: JobStatus.COMPLETED })} />);
    await userEvent.click(screen.getByRole("button", { name: "Results" }));
    expect(screen.getByRole("progressbar", { name: "Loading scan results" })).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Close" }));
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Results" }));
    await screen.findByText("added.txt");
    await act(async () => {
      finish({ entries: [ScanEntry.create({ id: 2n, path: "stale.txt" })], hasMore: true });
    });
    expect(screen.queryByText("stale.txt")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Load More" })).not.toBeInTheDocument();
  });
  it("shows read errors and lets results reload without invoking Apply", async () => {
    getProgress.mockImplementation(() => ({ response: Promise.reject(new Error("Progress unavailable")) }));
    listEntries.mockImplementationOnce(() => ({ response: Promise.reject(new Error("Results unavailable")) }));
    render(<ScanCard job={Job.create({ id: 30n, kind: JobKind.SCAN, status: JobStatus.COMPLETED })} />);
    expect(await screen.findByText("Progress unavailable")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Results" }));
    expect(await screen.findByText("Results unavailable")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Reload" }));
    expect(await screen.findByText("added.txt")).toBeInTheDocument();
    expect(screen.queryByText("Results unavailable")).not.toBeInTheDocument();
  });
  it("displays published results without an Apply action", async () => {
    render(<ScanCard job={Job.create({ id: 30n, kind: JobKind.SCAN, status: JobStatus.COMPLETED })} />);
    await waitFor(() => expect(getProgress).toHaveBeenCalledWith({ id: 30n }));
    expect(screen.queryByRole("button", { name: /Apply/ })).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Results" }));
    expect(await screen.findByText("added.txt")).toBeInTheDocument();
    expect(listEntries).toHaveBeenCalledWith({ id: 30n, limit: 200, afterId: undefined });
  });
  it("uses common cancellation while publishing and RetryIndex after interruption", async () => {
    const page = render(<ScanCard job={Job.create({ id: 31n, kind: JobKind.SCAN, status: JobStatus.INDEXING, phase: JobPhase.APPLYING_SCAN })} />);
    await userEvent.click(screen.getByRole("button", { name: "Cancel" }));
    expect(cancel).not.toHaveBeenCalled();
    await userEvent.click(screen.getByRole("button", { name: "Stop job" }));
    expect(cancel).toHaveBeenCalledWith({ id: 31n });
    page.unmount();
    render(<ScanCard job={Job.create({ id: 32n, kind: JobKind.SCAN, status: JobStatus.INDEXING, phase: JobPhase.WAITING_FOR_INDEX_RETRY })} />);
    await userEvent.click(screen.getByRole("button", { name: "Retry scan" }));
    expect(retryIndex).not.toHaveBeenCalled();
    await userEvent.click(screen.getByRole("button", { name: "Retry" }));
    expect(retryIndex).toHaveBeenCalledWith({ id: 32n });
  });
});
