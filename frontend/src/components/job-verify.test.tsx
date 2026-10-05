import type { ReactNode } from "react";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { GetScanJobProgressResponse, Job, JobKind, JobPhase, JobResultOrder, JobStatus, ScanEntry, ScanFinding } from "@/entity";
const { progress, entries, cancel } = vi.hoisted(() => ({ progress: vi.fn(), entries: vi.fn(), cancel: vi.fn() }));
vi.mock("@/api", () => ({ scanJobCli: { getProgress: progress, listEntries: entries }, jobCli: { cancel } }));
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
  progress.mockReturnValue(call(GetScanJobProgressResponse.create({ matchedCount: 1n, damagedCount: 1n, missingCount: 1n })));
  entries.mockReturnValue(
    call({
      entries: [
        ScanEntry.create({ id: 1n, path: "damaged.jpg", finding: ScanFinding.MISMATCH }),
        ScanEntry.create({ id: 2n, path: "missing.jpg", finding: ScanFinding.MISSING }),
      ],
      hasMore: false,
      totalEntryCount: 2n,
    }),
  );
  cancel.mockReturnValue(call({}));
});
describe("Integrity check Job", () => {
  it("reports unhealthy observations without implying a repair, with a complete result list", async () => {
    render(<ScanCard job={Job.create({ id: 4n, kind: JobKind.SCAN, status: JobStatus.COMPLETED })} />);
    expect(await screen.findByText("Content mismatch")).toBeInTheDocument();
    expect(screen.queryByText(/Expected content records have not been changed/)).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Results" }));
    expect(await screen.findByText("damaged.jpg")).toBeInTheDocument();
    expect(await screen.findAllByText("Missing")).toHaveLength(2);
    // The list spans its whole result set from the first load and reports its size.
    expect(entries).toHaveBeenCalledTimes(1);
    expect(entries).toHaveBeenLastCalledWith({ id: 4n, limit: 200, cursor: "", order: JobResultOrder.ASCENDING, includeTotal: true });
  });
  it("uses the shared cancellation action during verification", async () => {
    render(<ScanCard job={Job.create({ id: 4n, kind: JobKind.SCAN, phase: JobPhase.VERIFYING_MEDIA, status: JobStatus.READY })} />);
    await userEvent.click(screen.getByRole("button", { name: "Cancel" }));
    expect(cancel).not.toHaveBeenCalled();
    await userEvent.click(screen.getByRole("button", { name: "Stop job" }));
    expect(cancel).toHaveBeenCalledWith({ id: 4n });
  });
});
