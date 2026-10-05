import type { ReactNode } from "react";
import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { Job, JobKind, JobPhase, JobResultOrder, JobStatus, Progress, ScanChange, ScanEntry, ScanFinding, ScanPreviewOutcome } from "@/entity";
const { getProgress, listEntries, cancel } = vi.hoisted(() => ({
  getProgress: vi.fn(),
  listEntries: vi.fn(),
  cancel: vi.fn(),
}));
vi.mock("@/api", () => ({ scanJobCli: { getProgress, listEntries }, jobCli: { cancel } }));
vi.mock("react-virtuoso", () => ({
  Virtuoso: ({ totalCount = 0, itemContent }: { totalCount?: number; itemContent: (index: number) => ReactNode }) => (
    <div>
      {Array.from({ length: totalCount }, (_, index) => (
        <div key={index}>{itemContent(index)}</div>
      ))}
    </div>
  ),
}));
vi.mock("@/pages/jobs", async () => {
  const { createContext } = await import("react");
  return { RefreshContext: createContext(async () => {}) };
});
vi.mock("@/components/job-card", async (original) => {
  const actual = await original<typeof import("@/components/job-card")>();
  const { useEffect, useRef } = await import("react");
  return {
    ...actual,
    // The shared card owns the frame, the extra rows and the actions; only the chrome and the
    // visibility reporting of the shell are replaced here.
    JobProgressCard: (props: Parameters<typeof actual.JobProgressCard>[0]) => {
      const onVisibilityChange = useRef(props.onVisibilityChange);
      useEffect(() => onVisibilityChange.current?.(true), []);
      return <actual.JobProgressCard {...props} />;
    },
  };
});
vi.mock("@/tools", async (original) => ({
  ...(await original<typeof import("@/tools")>()),
  useSharedIntersectionObserver: () => false,
}));
vi.mock("react-router", () => ({
  Link: ({ children }: { children?: ReactNode }) => <span>{children}</span>,
  useLocation: () => ({ pathname: "/jobs", search: "" }),
}));
import { ScanCard } from "./job-scan";
const call = (value: unknown) => ({ response: Promise.resolve(value) });
beforeEach(() => {
  vi.clearAllMocks();
  getProgress.mockReturnValue(
    call({ progress: Progress.create({ totalFileCount: 3n, totalBytes: 7n }), addedCount: 1n, changedCount: 1n, removedCount: 1n, processedBytes: 7n }),
  );
  listEntries.mockReturnValue(
    call({ entries: [ScanEntry.create({ id: 1n, path: "added.txt", change: ScanChange.ADDED, sizeBytes: 7n })], hasMore: false, totalEntryCount: 1n }),
  );
  cancel.mockReturnValue(call({}));
});
afterEach(() => vi.useRealTimers());
describe("Automatic Media Scan", () => {
  it("animates a bar only while the Job works and stops it when the Job waits", async () => {
    getProgress.mockReturnValue(call({ progress: Progress.create({ totalKnown: false, copiedFileCount: 2n }) }));
    const active = Job.create({ id: 30n, mediaId: 9n, kind: JobKind.SCAN, status: JobStatus.READY, phase: JobPhase.VALIDATING_SOURCE });
    const page = render(<ScanCard job={active} />);

    expect(screen.getByRole("progressbar").className).toContain("indeterminate");
    expect(await screen.findByText("2")).toBeInTheDocument();
    expect(screen.queryByText("0:00")).not.toBeInTheDocument();

    // Queuing owns an attempt without transferring bytes, so its bar stays still and Cancel remains.
    page.rerender(<ScanCard job={Job.create({ ...active, phase: JobPhase.QUEUED })} />);
    const bar = screen.getByRole("progressbar");
    expect(bar.className).not.toContain("indeterminate");
    expect(Number(bar.getAttribute("aria-valuenow"))).toBe(0);
    expect(screen.getByRole("button", { name: "Cancel" })).toBeEnabled();
    expect(screen.queryByRole("button", { name: "Choose Media to read" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Delete Job" })).not.toBeInTheDocument();

    page.rerender(<ScanCard job={Job.create({ ...active, phase: JobPhase.UNSPECIFIED })} />);
    expect(screen.getByRole("button", { name: "Choose Media to read" })).toBeEnabled();
    expect(screen.getByRole("button", { name: "Delete Job" })).toBeEnabled();
    expect(screen.queryByRole("button", { name: "Cancel" })).not.toBeInTheDocument();
  });

  it("updates elapsed time from each poll and preserves the completed server value", async () => {
    vi.useFakeTimers();
    getProgress
      .mockReturnValueOnce(call({ progress: Progress.create({ totalKnown: true, elapsedMs: 1_000n }) }))
      .mockReturnValueOnce(call({ progress: Progress.create({ totalKnown: true, elapsedMs: 2_000n }) }))
      .mockReturnValue(call({ progress: Progress.create({ totalKnown: true, elapsedMs: 2_000n }) }));
    const active = Job.create({ id: 30n, kind: JobKind.SCAN, status: JobStatus.PREPARING, phase: JobPhase.PROCESSING_CONTENT });
    const page = render(<ScanCard job={active} />);

    await act(async () => {});
    expect(screen.getByTitle("0:01")).toBeInTheDocument();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(2_000);
    });
    expect(screen.getByTitle("0:02")).toBeInTheDocument();

    page.rerender(<ScanCard job={Job.create({ ...active, status: JobStatus.COMPLETED, phase: JobPhase.COMPLETED })} />);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(2_000);
    });
    expect(screen.getByTitle("0:02")).toBeInTheDocument();
    page.unmount();
  });

  it("shows one complete file result view for Location scans without an Apply action", async () => {
    listEntries.mockReturnValue(
      call({
        entries: [ScanEntry.create({ id: 1n, path: "added.txt", change: ScanChange.ADDED, checkedAtNs: 1700000000123999999n })],
        hasMore: false,
        totalEntryCount: 1n,
      }),
    );
    render(<ScanCard job={Job.create({ id: 30n, locationId: 2n, kind: JobKind.SCAN, status: JobStatus.COMPLETED })} />);
    await waitFor(() => expect(getProgress).toHaveBeenCalledWith({ id: 30n }, { abort: expect.any(AbortSignal) }));
    expect(screen.queryByRole("button", { name: /Apply/ })).not.toBeInTheDocument();
    await userEvent.click(await screen.findByRole("button", { name: "Results" }));
    expect(await screen.findByText("added.txt")).toBeInTheDocument();
    expect(screen.getByText((text) => text.includes(new Date("2023-11-14T22:13:20.123Z").toLocaleString()))).toBeInTheDocument();
    expect(screen.queryByRole("tab", { name: "Coverage" })).not.toBeInTheDocument();
    expect(listEntries).toHaveBeenCalledWith({ id: 30n, limit: 200, cursor: "", order: JobResultOrder.ASCENDING, includeTotal: true });
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
    expect(screen.getByRole("progressbar", { name: "Loading Job results" })).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Close" }));
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Results" }));
    await screen.findByText("added.txt");
    await act(async () => {
      finish({ entries: [ScanEntry.create({ id: 2n, path: "stale.txt" })], hasMore: true, totalEntryCount: 1n });
    });
    expect(screen.queryByText("stale.txt")).not.toBeInTheDocument();
    expect(screen.getByText("added.txt")).toBeInTheDocument();
  });
  it("shows progress read errors and lets results retry independently", async () => {
    getProgress.mockImplementation(() => ({ response: Promise.reject(new Error("Progress unavailable")) }));
    listEntries.mockImplementationOnce(() => ({ response: Promise.reject(new Error("Results unavailable")) }));
    render(<ScanCard job={Job.create({ id: 30n, kind: JobKind.SCAN, status: JobStatus.COMPLETED })} />);
    expect(await screen.findByText("Progress unavailable")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Results" }));
    expect(await screen.findByText("Results unavailable")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Retry" }));
    expect(await screen.findByText("added.txt")).toBeInTheDocument();
    expect(screen.queryByText("Results unavailable")).not.toBeInTheDocument();
  });
  it("shows a per-file Preview generation failure without losing the Scan finding", async () => {
    getProgress.mockReturnValue(call({ progress: Progress.create({ totalKnown: true, totalFileCount: 1n }), addedCount: 1n, previewsFailedCount: 1n }));
    listEntries.mockReturnValue(
      call({
        entries: [
          ScanEntry.create({
            id: 1n,
            path: "image.jpg",
            change: ScanChange.ADDED,
            finding: ScanFinding.NOT_CHECKED,
            preview: ScanPreviewOutcome.FAILED,
            previewError: "Decoder could not open the image",
          }),
        ],
        hasMore: false,
        totalEntryCount: 1n,
      }),
    );
    render(<ScanCard job={Job.create({ id: 30n, kind: JobKind.SCAN, status: JobStatus.COMPLETED })} />);
    expect(await screen.findByText("Previews failed")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Results" }));
    expect(await screen.findByText("image.jpg")).toBeInTheDocument();
    expect(screen.getByText(/Preview failed: Decoder could not open the image/)).toHaveTextContent(/added/);
  });
  it.each([JobPhase.PROCESSING_CONTENT, JobPhase.QUEUED])("uses the common cancellation action for an admitted attempt at phase %s", async (phase) => {
    render(<ScanCard job={Job.create({ id: 31n, kind: JobKind.SCAN, status: JobStatus.PREPARING, phase })} />);
    await userEvent.click(screen.getByRole("button", { name: "Cancel" }));
    expect(cancel).not.toHaveBeenCalled();
    await userEvent.click(screen.getByRole("button", { name: "Stop job" }));
    expect(cancel).toHaveBeenCalledWith({ id: 31n });
  });
});
