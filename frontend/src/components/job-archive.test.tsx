import type { ReactNode } from "react";

import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { ArchiveTapeWriteMode, Job, JobKind, JobPhase, JobStatus, Media, MediaKind, Progress, VolumeType } from "@/entity";

const { deviceList, mediaInspect, mediaList, getProgress, writeMedia, listFiles } = vi.hoisted(() => ({
  deviceList: vi.fn(),
  mediaInspect: vi.fn(),
  mediaList: vi.fn(),
  getProgress: vi.fn(),
  writeMedia: vi.fn(),
  listFiles: vi.fn(),
}));

vi.mock("@/api", () => ({
  cli: { deviceList, mediaInspect, mediaList },
  archiveJobCli: { getProgress, writeMedia, listFiles },
}));

vi.mock("react-virtuoso", () => ({ Virtuoso: () => <div>Loaded archive files</div> }));

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

import { ArchiveCard } from "@/components/job-archive";

const call = <T,>(response: T) => ({ response: Promise.resolve(response) });
const job = Job.create({
  id: 20n,
  kind: JobKind.ARCHIVE,
  status: JobStatus.PENDING,
  phase: JobPhase.WAITING_FOR_MEDIA,
});

beforeEach(() => {
  deviceList.mockReset();
  mediaInspect.mockReset();
  mediaList.mockReset();
  getProgress.mockReset();
  writeMedia.mockReset();
  listFiles.mockReset();
  deviceList.mockReturnValue(call({ devices: ["/dev/nst0"] }));
  mediaList.mockReturnValue(call({ media: [] }));
  getProgress.mockReturnValue(call({ progress: Progress.create({ totalFiles: 1n, totalBytes: 7n }) }));
  writeMedia.mockReturnValue(call({}));
});

describe("ArchiveCard Write Media", () => {
  it("shows the companion Preview Job or its preparation failure independently of backup progress", async () => {
    getProgress.mockReturnValue(call({ progress: Progress.create({ totalFiles: 1n }), previewJobId: 31n, previewError: "" }));
    const view = render(<ArchiveCard job={job} />);
    expect(await screen.findByRole("link", { name: "View preview job" })).toHaveAttribute("href", "/jobs/31");
    view.unmount();

    getProgress.mockReturnValue(
      call({ progress: Progress.create({ totalFiles: 1n }), previewJobId: 0n, previewError: "Preview configuration is unavailable" }),
    );
    render(<ArchiveCard job={job} />);
    expect(await screen.findByRole("alert")).toHaveTextContent("Preview creation failed: Preview configuration is unavailable");
    expect(screen.queryByRole("link", { name: "View preview job" })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Choose backup storage" })).toBeEnabled();
  });

  it("formats a new Tape using the identity read from the device", async () => {
    mediaInspect.mockReturnValue(call({ identity: "ABC001", fileCount: 0n }));
    render(<ArchiveCard job={job} />);

    await userEvent.click(screen.getByRole("button", { name: "Choose backup storage" }));
    await userEvent.click(screen.getByRole("combobox", { name: "Storage type" }));
    await userEvent.click(screen.getByRole("option", { name: "Tape" }));
    expect(await screen.findByDisplayValue("ABC001")).toBeDisabled();
    await userEvent.type(screen.getByRole("textbox", { name: "Tape Name" }), "New Tape");
    await userEvent.click(screen.getByRole("button", { name: "Start backup" }));

    await waitFor(() =>
      expect(writeMedia).toHaveBeenCalledWith(
        expect.objectContaining({
          id: 20n,
          target: {
            backend: {
              oneofKind: "tape",
              tape: expect.objectContaining({
                device: "/dev/nst0",
                barcode: "ABC001",
                name: "New Tape",
                mode: ArchiveTapeWriteMode.FORMAT,
              }),
            },
          },
        }),
      ),
    );
  });

  it("appends to an existing ltfs_v1 Tape", async () => {
    const existing = Media.create({
      id: 7n,
      kind: MediaKind.TAPE,
      identity: "ABC001",
      name: "Existing Tape",
      profile: { kind: { oneofKind: "tape", tape: { serialNumber: "", encryption: "key", format: "ltfs_v1" } } },
      createTime: 10n,
      writtenBytes: 1024n,
    });
    mediaInspect.mockReturnValue(call({ identity: "ABC001", fileCount: 12n, lastWriteTime: 20n, media: existing }));
    render(<ArchiveCard job={job} />);

    await userEvent.click(screen.getByRole("button", { name: "Choose backup storage" }));
    await userEvent.click(screen.getByRole("combobox", { name: "Storage type" }));
    await userEvent.click(screen.getByRole("option", { name: "Tape" }));
    expect(await screen.findByText(/12 files/)).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Start backup" }));
    await waitFor(() =>
      expect(writeMedia).toHaveBeenCalledWith(
        expect.objectContaining({
          target: { backend: { oneofKind: "tape", tape: expect.objectContaining({ barcode: "ABC001", mode: ArchiveTapeWriteMode.APPEND }) } },
        }),
      ),
    );
  });

  it("does not offer overwrite for an existing incompatible Tape", async () => {
    const existing = Media.create({
      id: 7n,
      kind: MediaKind.TAPE,
      identity: "ABC001",
      profile: { kind: { oneofKind: "tape", tape: { serialNumber: "", encryption: "key", format: "ltfs_v0" } } },
    });
    mediaInspect.mockReturnValue(call({ identity: "ABC001", fileCount: 12n, media: existing }));
    render(<ArchiveCard job={job} />);

    await userEvent.click(screen.getByRole("button", { name: "Choose backup storage" }));
    await userEvent.click(screen.getByRole("combobox", { name: "Storage type" }));
    await userEvent.click(screen.getByRole("option", { name: "Tape" }));
    expect(await screen.findByText(/Delete its Media metadata before formatting/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Start backup" })).toBeDisabled();
  });

  it("writes to an initialized Volume", async () => {
    const volume = Media.create({
      id: 8n,
      kind: MediaKind.VOLUME,
      identity: "11111111-1111-1111-1111-111111111111",
      name: "Offline HDD",
      mounted: true,
      filesystemAvailableBytes: 1024n,
      profile: { kind: { oneofKind: "volume", volume: { serialNumber: "disk", type: VolumeType.HDD } } },
    });
    mediaInspect.mockReturnValue(call({ identity: "ABC001", fileCount: 0n }));
    mediaList.mockReturnValue(call({ media: [volume] }));
    render(<ArchiveCard job={job} />);

    await userEvent.click(screen.getByRole("button", { name: "Choose backup storage" }));
    expect(mediaInspect).not.toHaveBeenCalled();
    await userEvent.click(screen.getByRole("combobox", { name: "Volume" }));
    await userEvent.click(screen.getByRole("option", { name: /Offline HDD/ }));
    await userEvent.click(screen.getByRole("button", { name: "Start backup" }));

    await waitFor(() =>
      expect(writeMedia).toHaveBeenCalledWith({
        id: 20n,
        target: { backend: { oneofKind: "volume", volume: { uuid: volume.identity } } },
      }),
    );
  });
});
