import type { ReactNode } from "react";

import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";

import {
  CopyStatus,
  Job,
  JobKind,
  JobPhase,
  JobResultOrder,
  JobStatus,
  Media,
  MediaKind,
  Progress,
  RestoreCandidate,
  RestoreItem,
  RestoreMedia,
} from "@/entity";

const { deviceList, mediaInspect, mediaListAPI, restoreMedia, getProgress, listMedia, listFiles } = vi.hoisted(() => ({
  deviceList: vi.fn(),
  mediaInspect: vi.fn(),
  mediaListAPI: vi.fn(),
  restoreMedia: vi.fn(),
  getProgress: vi.fn(),
  listMedia: vi.fn(),
  listFiles: vi.fn(),
}));

vi.mock("@/api", () => ({
  mediaCli: { listDevices: deviceList, inspect: mediaInspect, list: mediaListAPI },
  restoreJobCli: { restoreMedia, getProgress, listMedia, listFiles },
}));

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

import { RestoreCard } from "@/components/job-restore";

const call = <T,>(response: T) => ({ response: Promise.resolve(response) });

beforeEach(() => {
  deviceList.mockReset();
  mediaInspect.mockReset();
  mediaListAPI.mockReset();
  restoreMedia.mockReset();
  getProgress.mockReset();
  listMedia.mockReset();
  listFiles.mockReset();
  deviceList.mockReturnValue(call({ devices: ["/dev/nst0"] }));
  mediaListAPI.mockReturnValue(call({ media: [] }));
  restoreMedia.mockReturnValue(call({}));
});

describe("RestoreCard", () => {
  it.each([JobPhase.COPYING_FROM_MEDIA, JobPhase.QUEUED])(
    "offers Cancel instead of another Media choice during an admitted Restore at phase %s",
    async (phase) => {
      getProgress.mockReturnValue(call({ progress: Progress.create({ totalKnown: true, totalFileCount: 1n, stage: { phase } }) }));
      listMedia.mockReturnValue(call({ media: [RestoreMedia.create({ mediaId: 9n, identity: "BPP875", status: CopyStatus.PENDING })], hasMore: false }));
      render(<RestoreCard job={Job.create({ id: 20n, kind: JobKind.RESTORE, status: JobStatus.READY, phase })} />);

      await screen.findByText("BPP875: pending");
      expect(screen.getByRole("button", { name: "Cancel" })).toBeEnabled();
      expect(screen.queryByRole("button", { name: "Choose archive storage to read" })).not.toBeInTheDocument();
      expect(screen.queryByRole("button", { name: "Delete Job" })).not.toBeInTheDocument();
    },
  );

  it("offers Media after a Restore Media failure returns to idle READY with its error", async () => {
    getProgress.mockReturnValue(call({ progress: Progress.create({ totalKnown: true, totalFileCount: 1n, stage: { phase: JobPhase.UNSPECIFIED } }) }));
    listMedia.mockReturnValue(call({ media: [RestoreMedia.create({ mediaId: 9n, identity: "BPP875", status: CopyStatus.PENDING })], hasMore: false }));
    render(<RestoreCard job={Job.create({ id: 20n, kind: JobKind.RESTORE, status: JobStatus.READY, error: "Media unavailable" })} />);

    expect(await screen.findByRole("button", { name: "Choose archive storage to read" })).toBeEnabled();
    expect(screen.getByRole("alert")).toHaveTextContent("Media unavailable");
  });

  it("does not offer Media for a terminal FAILED Restore even when manifest totals are known", async () => {
    getProgress.mockReturnValue(
      call({ progress: Progress.create({ totalKnown: true, totalFileCount: 1n, stage: { phase: JobPhase.UNSPECIFIED } }), summary: { pendingFiles: 1n } }),
    );
    listMedia.mockReturnValue(call({ media: [], hasMore: false }));
    render(<RestoreCard job={Job.create({ id: 20n, kind: JobKind.RESTORE, status: JobStatus.FAILED })} />);

    await screen.findByText("1 pending");
    expect(screen.queryByRole("button", { name: "Choose archive storage to read" })).not.toBeInTheDocument();
  });

  it("loads bounded restore results through the shared dialog", async () => {
    const media = RestoreMedia.create({ mediaId: 9n, identity: "BPP875", fileCount: 1n, status: CopyStatus.PENDING });
    getProgress.mockReturnValue(call({ progress: Progress.create({ totalFileCount: 1n, totalBytes: 10n }) }));
    listMedia.mockReturnValue(call({ media: [media], hasMore: false }));
    listFiles.mockReturnValue(
      call({
        items: [
          RestoreItem.create({
            id: 4n,
            sizeBytes: 10n,
            status: CopyStatus.PENDING,
            candidate: RestoreCandidate.create({ mediaId: 9n, mediaPath: "saved/file.txt" }),
          }),
        ],
        hasMore: false,
        totalFileCount: 1n,
      }),
    );
    const value = Job.create({
      id: 20n,
      kind: JobKind.RESTORE,
      status: JobStatus.READY,
      phase: JobPhase.UNSPECIFIED,
    });

    render(<RestoreCard job={value} />);
    expect(await screen.findByText("BPP875: pending")).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "Results" }));
    expect(await screen.findByText("saved/file.txt")).toBeInTheDocument();
    // One composite sequence spans every Media, so the view needs no Media argument.
    expect(listFiles).toHaveBeenCalledWith({ id: 20n, limit: 200, cursor: "", order: JobResultOrder.ASCENDING, includeTotal: true, filterStatus: [] });

    await userEvent.click(screen.getByRole("button", { name: "Close" }));
    expect(screen.queryByRole("dialog", { name: "Restore results" })).not.toBeInTheDocument();
  });

  it("submits only after the inserted Tape matches a pending Media", async () => {
    const pending = RestoreMedia.create({ mediaId: 9n, identity: "BPP875", fileCount: 1n, status: CopyStatus.PENDING });
    const tape = Media.create({ id: 9n, kind: MediaKind.TAPE, identity: "BPP875", name: "Required Tape" });
    getProgress.mockReturnValue(call({ progress: Progress.create({ totalFileCount: 1n, totalBytes: 10n }) }));
    listMedia.mockReturnValue(call({ media: [pending], hasMore: false }));
    mediaListAPI.mockReturnValue(call({ media: [tape] }));
    mediaInspect.mockReturnValue(call({ identity: "BPP875", media: tape, fileCount: 1n }));
    const value = Job.create({
      id: 20n,
      kind: JobKind.RESTORE,
      status: JobStatus.READY,
      phase: JobPhase.UNSPECIFIED,
    });

    render(<RestoreCard job={value} />);
    await screen.findByText("Required Tape: pending");
    await userEvent.click(screen.getByRole("button", { name: "Choose archive storage to read" }));
    await screen.findByRole("combobox", { name: "Drive Device" });
    expect(screen.queryByRole("combobox", { name: "Storage type" })).not.toBeInTheDocument();
    const submit = await screen.findByRole("button", { name: "Start restore" });
    await waitFor(() => expect(submit).toBeEnabled());
    await userEvent.click(submit);

    expect(mediaInspect).toHaveBeenCalledWith({
      target: { oneofKind: "tape", tape: { device: "/dev/nst0" } },
      identity: undefined,
    });
    expect(restoreMedia).toHaveBeenCalledWith({
      id: 20n,
      target: { backend: { oneofKind: "tape", tape: { device: "/dev/nst0" } }, expectedMediaId: 9n, expectedIdentity: "BPP875" },
    });
  });
});
