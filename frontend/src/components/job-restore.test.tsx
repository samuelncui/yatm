import type { ReactNode } from "react";

import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { CopyStatus, Job, JobKind, JobPhase, JobStatus, Media, MediaKind, Progress, RestoreMedia } from "@/entity";

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
  cli: { deviceList, mediaInspect, mediaList: mediaListAPI },
  restoreJobCli: { restoreMedia, getProgress, listMedia, listFiles },
}));

vi.mock("react-virtuoso", () => ({ Virtuoso: () => <div>Loaded Media files</div> }));

vi.mock("@/components/job-card", async (importOriginal) => {
  const { useEffect } = await import("react");
  const actual = await importOriginal<typeof import("@/components/job-card")>();
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
  it("closes View Files while a Media row is expanded", async () => {
    const media = RestoreMedia.create({ mediaId: 9n, identity: "BPP875", total: 1n, status: CopyStatus.PENDING });
    getProgress.mockReturnValue(call({ progress: Progress.create({ totalFiles: 1n, totalBytes: 10n }) }));
    listMedia.mockReturnValue(call({ media: [media], hasMore: false }));
    listFiles.mockReturnValue(call({ items: [] }));
    const value = Job.create({
      id: 20n,
      kind: JobKind.RESTORE,
      status: JobStatus.PENDING,
      phase: JobPhase.WAITING_FOR_MEDIA,
    });

    render(<RestoreCard job={value} />);
    expect(await screen.findByText("BPP875: pending")).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "View Files" }));
    await userEvent.click(screen.getByRole("button", { name: /Media: BPP875/ }));
    expect(screen.getByText("Loaded Media files")).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "Close" }));
    expect(screen.queryByRole("dialog", { name: "View Files" })).not.toBeInTheDocument();
  });

  it("submits only after the inserted Tape matches a pending Media", async () => {
    const pending = RestoreMedia.create({ mediaId: 9n, identity: "BPP875", total: 1n, status: CopyStatus.PENDING });
    const tape = Media.create({ id: 9n, kind: MediaKind.TAPE, identity: "BPP875", name: "Required Tape" });
    getProgress.mockReturnValue(call({ progress: Progress.create({ totalFiles: 1n, totalBytes: 10n }) }));
    listMedia.mockReturnValue(call({ media: [pending], hasMore: false }));
    mediaListAPI.mockReturnValue(call({ media: [tape] }));
    mediaInspect.mockReturnValue(call({ identity: "BPP875", media: tape, fileCount: 1n }));
    const value = Job.create({
      id: 20n,
      kind: JobKind.RESTORE,
      status: JobStatus.PENDING,
      phase: JobPhase.WAITING_FOR_MEDIA,
    });

    render(<RestoreCard job={value} />);
    await screen.findByText("Required Tape: pending");
    await userEvent.click(screen.getByRole("button", { name: "Choose a backup to read" }));
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
