import type { ReactNode } from "react";

import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { ArchiveFile, ArchiveItem, CopyStatus, Job, JobKind, JobPhase, JobResultOrder, JobStatus, Progress } from "@/entity";

const { getProgress, writeMedia, listFiles } = vi.hoisted(() => ({
  getProgress: vi.fn(),
  writeMedia: vi.fn(),
  listFiles: vi.fn(),
}));

vi.mock("@/api", () => ({ archiveJobCli: { getProgress, writeMedia, listFiles } }));

vi.mock("react-virtuoso", () => ({
  Virtuoso: ({ totalCount = 0, itemContent }: { totalCount?: number; itemContent: (index: number) => ReactNode }) => (
    <div>
      {Array.from({ length: totalCount }, (_, index) => (
        <div key={index}>{itemContent(index)}</div>
      ))}
    </div>
  ),
}));

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

import { ArchiveCard } from "@/components/job-archive";

const call = <T,>(response: T) => ({ response: Promise.resolve(response) });
const job = Job.create({
  id: 20n,
  kind: JobKind.ARCHIVE,
  status: JobStatus.READY,
  phase: JobPhase.UNSPECIFIED,
});

beforeEach(() => {
  getProgress.mockReset();
  writeMedia.mockReset();
  listFiles.mockReset();
  getProgress.mockReturnValue(call({ progress: Progress.create({ totalFileCount: 1n, totalBytes: 7n }) }));
});

describe("ArchiveCard", () => {
  it("offers Media after an Archive Media failure returns to idle READY with its error", async () => {
    getProgress.mockReturnValue(call({ progress: Progress.create({ totalKnown: true, totalFileCount: 1n, stage: { phase: JobPhase.UNSPECIFIED } }) }));
    render(<ArchiveCard job={Job.create({ ...job, status: JobStatus.READY, phase: JobPhase.UNSPECIFIED, error: "Media unavailable" })} />);

    expect(await screen.findByRole("button", { name: "Choose archive storage" })).toBeEnabled();
    expect(screen.getByRole("alert")).toHaveTextContent("Media unavailable");
  });

  it("does not offer Media for a terminal FAILED Archive even when manifest totals are known", async () => {
    getProgress.mockReturnValue(
      call({ progress: Progress.create({ totalKnown: true, totalFileCount: 1n, stage: { phase: JobPhase.UNSPECIFIED } }), previewJobId: 31n }),
    );
    render(<ArchiveCard job={Job.create({ ...job, status: JobStatus.FAILED, phase: JobPhase.UNSPECIFIED })} />);

    await screen.findByRole("link", { name: "View preview job" });
    expect(screen.queryByRole("button", { name: "Choose archive storage" })).not.toBeInTheDocument();
  });

  it.each([JobPhase.COPYING_TO_MEDIA, JobPhase.QUEUED])("offers Cancel instead of the Media choice during an admitted Archive at phase %s", async (phase) => {
    const writing = Job.create({ id: 21n, kind: JobKind.ARCHIVE, status: JobStatus.READY, phase });
    render(<ArchiveCard job={writing} />);

    // A live phase says the runner owns the Job, so only stopping it is meaningful.
    expect(await screen.findByRole("button", { name: "Cancel" })).toBeEnabled();
    expect(screen.queryByRole("button", { name: "Choose archive storage" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Delete Job" })).not.toBeInTheDocument();
  });

  it.each([JobStatus.COMPLETED, JobStatus.FAILED])("blocks deletion and another Media start during live cleanup after status %s", async (status) => {
    getProgress.mockReturnValue(call({ progress: Progress.create({ totalKnown: true, totalFileCount: 1n, stage: { phase: JobPhase.FINALIZING_MEDIA } }) }));
    const cleanup = Job.create({ ...job, status, phase: JobPhase.FINALIZING_MEDIA });
    const page = render(<ArchiveCard job={cleanup} />);

    await screen.findByText("1");
    expect(screen.queryByRole("button", { name: "Delete Job" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Choose archive storage" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Start archive" })).not.toBeInTheDocument();
    expect(writeMedia).not.toHaveBeenCalled();

    page.rerender(<ArchiveCard job={Job.create({ ...cleanup, phase: JobPhase.UNSPECIFIED })} />);
    expect(screen.getByRole("button", { name: "Delete Job" })).toBeEnabled();
    expect(screen.queryByRole("button", { name: "Choose archive storage" })).not.toBeInTheDocument();
    expect(writeMedia).not.toHaveBeenCalled();
  });

  it("loads bounded archive results through the shared dialog", async () => {
    listFiles.mockReturnValue(
      call({
        items: [ArchiveItem.create({ id: 5n, sizeBytes: 7n, status: CopyStatus.COMPLETED, file: ArchiveFile.create({ targetPath: "Library/file.txt" }) })],
        hasMore: false,
        totalFileCount: 1n,
      }),
    );
    render(<ArchiveCard job={job} />);

    await userEvent.click(await screen.findByRole("button", { name: "Results" }));
    expect(await screen.findByText("Library/file.txt")).toBeInTheDocument();
    expect(listFiles).toHaveBeenCalledWith(
      { id: 20n, limit: 200, cursor: "", order: JobResultOrder.ASCENDING, includeTotal: true, filterStatus: [] },
      { abort: expect.any(AbortSignal) },
    );
  });

  it("shows the companion Preview Job or its preparation failure independently of archive progress", async () => {
    getProgress.mockReturnValue(call({ progress: Progress.create({ totalFileCount: 1n }), previewJobId: 31n, previewError: "" }));
    const view = render(<ArchiveCard job={job} />);
    expect(await screen.findByRole("link", { name: "View preview job" })).toHaveAttribute("href", "/jobs/31");
    view.unmount();

    getProgress.mockReturnValue(
      call({ progress: Progress.create({ totalFileCount: 1n }), previewJobId: 0n, previewError: "Preview configuration is unavailable" }),
    );
    render(<ArchiveCard job={job} />);
    expect(await screen.findByRole("alert")).toHaveTextContent("Preview creation failed: Preview configuration is unavailable");
    expect(screen.queryByRole("link", { name: "View preview job" })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Choose archive storage" })).toBeEnabled();
  });
});
